// Chapter: file-shaped exports — header paging and time windows
// (EXPORT-SPEC.md §3, §4). The JSON list endpoints carry their continuation
// token in the response body, which is what --all follows in pagination.go.
// An export whose body is a file cannot: DataHub's record export returns the
// token in the X-Next-Page-Token header, and caps the requested time window
// at 366 days. Both are server mechanics the user should not have to hand-run
// — before this chapter, --all on such an export silently returned only the
// first page, and a wider window was rejected by the API with a bare 400.
// This chapter makes --all walk the header tokens AND the sequential time
// windows, merging the pages into one body: CSV pages are re-emitted against
// the union of their headers (the export documents that consecutive pages can
// carry different label and metric columns), NDJSON pages are concatenated.
package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
)

// fileExportSpec describes one operation whose response body is a file. The
// window cap is a server constraint the CLI hides: values are taken from the
// OpenAPI document ("At most 366 days after the start time"), never guessed,
// and windowSource records that so a future change is traceable.
type fileExportSpec struct {
	// startFlag/endFlag are the CLI flags, startParam/endParam the query
	// parameters they become. Empty when the operation has no window cap.
	startFlag  string
	endFlag    string
	startParam string
	endParam   string
	maxWindow  time.Duration
	// windowSource is the evidence for maxWindow: "spec" (declared in the
	// OpenAPI document) or "verified" (probed against the live API).
	windowSource string
	// reimportable marks an export whose CSV can be rewritten into the
	// ingest vocabulary by --for-reimport.
	reimportable bool
}

var fileExportOperations = map[string]fileExportSpec{
	"export-datahub-dataset-records": {
		startFlag:    "--start-time",
		endFlag:      "--end-time",
		startParam:   "startTime",
		endParam:     "endTime",
		maxWindow:    366 * 24 * time.Hour,
		windowSource: "spec",
		reimportable: true,
	},
}

// maxFileExportRequests bounds the --all fetch loop for a file export. At the
// export's 50,000-row page cap this is 10,000,000 rows across up to 200
// requests — beyond any real dataset, while still guaranteeing termination
// against a server that keeps returning tokens.
const maxFileExportRequests = 200

// exportWindowWalk tracks the range the user asked for while the requests go
// out one server-sized window at a time.
type exportWindowWalk struct {
	spec         fileExportSpec
	requestedEnd time.Time
	currentEnd   time.Time
	windows      int
}

// next returns the window after the one just finished, and false once the
// requested range is fully covered. Windows are half-open and abut exactly —
// the export's startTime is inclusive and endTime exclusive — so no row is
// fetched twice and none is skipped.
func (walk *exportWindowWalk) next() (time.Time, time.Time, bool) {
	if walk == nil || !walk.currentEnd.Before(walk.requestedEnd) {
		return time.Time{}, time.Time{}, false
	}
	start := walk.currentEnd
	end := start.Add(walk.spec.maxWindow)
	if end.After(walk.requestedEnd) {
		end = walk.requestedEnd
	}
	walk.currentEnd = end
	walk.windows++
	return start, end, true
}

func parseExportWindowBound(value string) (time.Time, error) {
	return time.Parse(time.RFC3339, strings.TrimSpace(value))
}

func formatExportWindowBound(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

// clampExportWindow shortens an over-long export window to the server's cap
// and returns the walk state the --all loop continues from. Only under --all:
// without it there is no loop to continue with, and silently exporting a
// narrower range than the one asked for would be the worst possible answer —
// validateExportWindow rejects that combination up front instead.
func clampExportWindow(request *http.Request) (*http.Request, *exportWindowWalk) {
	spec, known := fileExportOperations[invokedCommandName]
	if !known || spec.maxWindow == 0 || request.Method != http.MethodGet || !allPagesActive() {
		return request, nil
	}
	query := request.URL.Query()
	start, startErr := parseExportWindowBound(query.Get(spec.startParam))
	end, endErr := parseExportWindowBound(query.Get(spec.endParam))
	if startErr != nil || endErr != nil || !end.After(start) {
		// Not a window this chapter understands: let the API rule on it.
		return request, nil
	}
	if end.Sub(start) <= spec.maxWindow {
		return request, nil
	}
	clampedEnd := start.Add(spec.maxWindow)
	clamped := cloneRequestWithQuery(request, map[string]string{spec.endParam: formatExportWindowBound(clampedEnd)}, nil)
	return clamped, &exportWindowWalk{spec: spec, requestedEnd: end, currentEnd: clampedEnd, windows: 1}
}

// cloneRequestWithQuery copies a request with query parameters set and
// removed. Used for every follow-up page so the auth, headers and context of
// the original request carry over untouched.
func cloneRequestWithQuery(request *http.Request, set map[string]string, remove []string) *http.Request {
	clone := request.Clone(request.Context())
	query := clone.URL.Query()
	for key, value := range set {
		query.Set(key, value)
	}
	for _, key := range remove {
		query.Del(key)
	}
	clone.URL.RawQuery = query.Encode()
	return clone
}

// appliesToFileExport reports whether the header-token merge path should
// engage: --all passed, a successful GET against the DoiT API, and a
// file-shaped body. The JSON collections are handled by applies() instead.
func (t paginatingTransport) appliesToFileExport(request *http.Request, response *http.Response) bool {
	if !allPagesActive() || request.Method != http.MethodGet || response == nil {
		return false
	}
	if response.StatusCode != http.StatusOK || !sameHostAsAPIBase(request) {
		return false
	}
	return rawPassthroughContentType(response.Header.Get("Content-Type"))
}

// mergeFileExportPages follows X-Next-Page-Token to the end of the current
// window, then advances through any remaining windows, and merges everything
// into one body. A single-page export short-circuits: the bytes the API sent
// are returned untouched, so --all on a one-page export is byte-identical to
// the same command without it.
func (t paginatingTransport) mergeFileExportPages(request *http.Request, response *http.Response, walk *exportWindowWalk) (*http.Response, error) {
	contentType := response.Header.Get("Content-Type")
	first, err := readDecodedBody(response)
	if err != nil {
		return nil, fmt.Errorf("--all: reading the export failed: %w", err)
	}
	pages := [][]byte{first}
	token := strings.TrimSpace(response.Header.Get(nextPageTokenHeader))

	base := request
	requests := 1
	truncated := false
	for {
		var pageRequest *http.Request
		switch {
		case token != "":
			pageRequest = cloneRequestWithQuery(base, map[string]string{"pageToken": token}, nil)
		case walk != nil:
			start, end, more := walk.next()
			if !more {
				return t.finishFileExport(response, contentType, pages, requests, walk, "", false)
			}
			pageRequest = cloneRequestWithQuery(base, map[string]string{
				walk.spec.startParam: formatExportWindowBound(start),
				walk.spec.endParam:   formatExportWindowBound(end),
			}, []string{"pageToken"})
			// Later token pages belong to this window, not the first one.
			base = pageRequest
		default:
			return t.finishFileExport(response, contentType, pages, requests, walk, "", false)
		}

		if requests >= maxFileExportRequests {
			truncated = true
			break
		}
		pageResponse, pageErr := t.next.RoundTrip(pageRequest)
		if pageErr != nil {
			return nil, fmt.Errorf("--all: fetching page %d failed: %w", requests+1, pageErr)
		}
		if pageResponse.StatusCode != http.StatusOK {
			_ = pageResponse.Body.Close()
			return nil, fmt.Errorf("--all: fetching page %d failed with HTTP status %d", requests+1, pageResponse.StatusCode)
		}
		body, bodyErr := readDecodedBody(pageResponse)
		if bodyErr != nil {
			return nil, fmt.Errorf("--all: reading page %d failed: %w", requests+1, bodyErr)
		}
		pages = append(pages, body)
		token = strings.TrimSpace(pageResponse.Header.Get(nextPageTokenHeader))
		requests++
	}
	return t.finishFileExport(response, contentType, pages, requests, walk, token, truncated)
}

// finishFileExport merges the collected pages onto the first response and
// reports what it did. The resume token is kept in the header when the
// request cap cut the fetch short, so the note can name a command that
// continues from there instead of silently returning a partial export.
func (t paginatingTransport) finishFileExport(response *http.Response, contentType string, pages [][]byte, requests int, walk *exportWindowWalk, token string, truncated bool) (*http.Response, error) {
	merged, rows, err := mergeFileExportBodies(contentType, pages)
	if err != nil {
		return nil, fmt.Errorf("--all: merging the export failed: %w", err)
	}
	if truncated {
		response.Header.Set(nextPageTokenHeader, token)
	} else {
		response.Header.Del(nextPageTokenHeader)
	}
	if rows >= 0 {
		response.Header.Set(rowCountHeader, strconv.Itoa(rows))
	}
	noteFileExportMerged(requests, rows, walk, token, truncated)
	return restoreBody(response, merged), nil
}

func noteFileExportMerged(requests, rows int, walk *exportWindowWalk, token string, truncated bool) {
	if cli.Stderr == nil {
		return
	}
	if truncated {
		resume := fmt.Sprintf("--page-token %s", token)
		if walk != nil {
			resume = fmt.Sprintf("%s %s %s", walk.spec.startFlag, formatExportWindowBound(walk.currentEnd), resume)
		}
		_, _ = fmt.Fprintf(cli.Stderr, "note: --all stopped after %d requests; resume with %s\n", requests, resume)
		return
	}
	if requests <= 1 {
		return
	}
	windows := ""
	if walk != nil && walk.windows > 1 {
		windows = fmt.Sprintf(" across %d time windows", walk.windows)
	}
	rowCount := ""
	if rows >= 0 {
		rowCount = fmt.Sprintf(" (%d rows)", rows)
	}
	_, _ = fmt.Fprintf(cli.Stderr, "note: --all merged %d pages%s%s\n", requests, windows, rowCount)
}

// readDecodedBody reads a response body whole, undoing any content encoding
// through restish's registered decoders. The file-export twin of
// decodeJSONBody, minus the JSON parse.
func readDecodedBody(response *http.Response) ([]byte, error) {
	defer func() { _ = response.Body.Close() }()
	if err := cli.DecodeResponse(response); err != nil {
		return nil, err
	}
	return io.ReadAll(response.Body)
}

// mergeFileExportBodies joins the pages of a file export, returning the
// merged body and the number of data rows in it (-1 when the count is not
// knowable). A single page is returned verbatim.
func mergeFileExportBodies(contentType string, pages [][]byte) ([]byte, int, error) {
	if len(pages) == 1 {
		return pages[0], -1, nil
	}
	if rawPassthroughCSV(contentType) {
		return mergeCSVExportPages(pages)
	}
	return mergeLineDelimitedPages(pages)
}

// mergeLineDelimitedPages concatenates NDJSON pages, making sure a page that
// does not end in a newline cannot glue its last record to the next page's
// first one.
func mergeLineDelimitedPages(pages [][]byte) ([]byte, int, error) {
	var merged []byte
	rows := 0
	for _, page := range pages {
		trimmed := strings.TrimRight(string(page), "\r\n")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		for _, line := range strings.Split(trimmed, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			rows++
		}
		merged = append(merged, []byte(trimmed)...)
		merged = append(merged, '\n')
	}
	return merged, rows, nil
}

// mergeCSVExportPages re-emits CSV pages against the union of their headers.
// The export computes its business columns from the rows of each page, so
// page 2 can introduce a label or metric column page 1 never had (and can
// order them differently); a naive concatenation would put those values under
// the wrong headings. Columns keep first-seen order, and a row from a page
// that lacked a column gets an empty cell for it.
//
// This buffers the whole export in memory, which the union guarantee makes
// unavoidable: a column discovered on the last page has to add a cell to
// every row already emitted.
func mergeCSVExportPages(pages [][]byte) ([]byte, int, error) {
	columns := make([]string, 0, 16)
	columnIndex := map[string]int{}
	rows := make([][]string, 0, 1024)

	for pageNumber, page := range pages {
		if strings.TrimSpace(string(page)) == "" {
			continue
		}
		reader := csv.NewReader(strings.NewReader(string(page)))
		reader.FieldsPerRecord = -1
		records, err := reader.ReadAll()
		if err != nil {
			return nil, 0, fmt.Errorf("page %d did not parse as CSV: %v", pageNumber+1, err)
		}
		if len(records) == 0 {
			continue
		}
		mapping := make([]int, len(records[0]))
		for position, column := range records[0] {
			index, seen := columnIndex[column]
			if !seen {
				index = len(columns)
				columnIndex[column] = index
				columns = append(columns, column)
			}
			mapping[position] = index
		}
		for _, record := range records[1:] {
			row := make([]string, len(columns))
			for position, cell := range record {
				if position < len(mapping) {
					row[mapping[position]] = cell
				}
			}
			rows = append(rows, row)
		}
	}

	var out strings.Builder
	writer := csv.NewWriter(&out)
	if err := writer.Write(columns); err != nil {
		return nil, 0, err
	}
	for _, row := range rows {
		// Rows built before a later page widened the union are short; the
		// missing trailing columns are absent values, so pad with empties.
		for len(row) < len(columns) {
			row = append(row, "")
		}
		if err := writer.Write(row); err != nil {
			return nil, 0, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, 0, err
	}
	return []byte(out.String()), len(rows), nil
}

// validateExportWindow rejects a window wider than the server's cap when
// --all is absent. The API answers such a request with a bare 400 ("window
// must not exceed 366 days"), leaving the user to do calendar arithmetic and
// stitch the halves; --all does exactly that for them, so the error points
// there rather than at the arithmetic.
func validateExportWindow(commandName string, args []string) error {
	spec, known := fileExportOperations[commandName]
	if !known || spec.maxWindow == 0 || invocationHasFlag(args, "--all") {
		return nil
	}
	rawStart, hasStart := flagValueFromArgs(args, spec.startFlag)
	rawEnd, hasEnd := flagValueFromArgs(args, spec.endFlag)
	if !hasStart || !hasEnd {
		return nil
	}
	start, startErr := parseExportWindowBound(rawStart)
	end, endErr := parseExportWindowBound(rawEnd)
	if startErr != nil || endErr != nil || !end.After(start) {
		// Malformed bounds are the API's to reject, with its own message.
		return nil
	}
	if end.Sub(start) <= spec.maxWindow {
		return nil
	}
	days := int(spec.maxWindow.Hours() / 24)
	evidence := "declared in the API spec"
	if spec.windowSource == "verified" {
		evidence = "verified against the live API"
	}
	return invocationPreflightError{
		detail: structuredError{
			Code: "USAGE_ERROR",
			Message: fmt.Sprintf("%s %s to %s spans %d days; %s exports at most %d days per request (%s)",
				commandName, formatExportWindowBound(start), formatExportWindowBound(end),
				int(end.Sub(start).Hours()/24), commandName, days, evidence),
			Hint:      fmt.Sprintf("Pass --all to export the whole range (the CLI walks the %d-day windows and merges the pages), or narrow the window", days),
			Retryable: false,
		},
		exitCode: exitUsage,
	}
}

// validateReimportFlag rejects --for-reimport where it cannot mean anything:
// on a command with no CSV export to rewrite, or alongside --format jsonl
// (the NDJSON stream is already in the ingest event shape, so there are no
// columns to drop or rename).
func validateReimportFlag(commandName string, args []string) error {
	if !invocationHasFlag(args, "--for-reimport") {
		return nil
	}
	if spec, known := fileExportOperations[commandName]; !known || !spec.reimportable {
		return invocationPreflightError{
			detail: structuredError{
				Code:      "USAGE_ERROR",
				Message:   fmt.Sprintf("--for-reimport rewrites an exported CSV for ingestion; %s does not export CSV", commandName),
				Hint:      "Drop --for-reimport, or run it on dci export-datahub-dataset-records",
				Retryable: false,
			},
			exitCode: exitUsage,
		}
	}
	if format, passed := flagValueFromArgs(args, "--format"); passed && !strings.EqualFold(strings.TrimSpace(format), "csv") {
		return invocationPreflightError{
			detail: structuredError{
				Code:      "USAGE_ERROR",
				Message:   fmt.Sprintf("--for-reimport rewrites CSV columns and cannot be combined with --format %s", format),
				Hint:      "Drop --format to export CSV, or drop --for-reimport — the jsonl export is already in the ingest event shape",
				Retryable: false,
			},
			exitCode: exitUsage,
		}
	}
	return nil
}
