package main

// Collections & pagination: every list command returns one server page per
// request. This chapter makes that visible (a stderr note when a rendering
// format drops the continuation token) and safe (client-side validation of
// --max-results against server caps the API does not enforce sanely — most
// endpoints silently reset out-of-range values to the default page size).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

// pagingCap is the server-side ceiling for a list command's --max-results
// parameter, with the evidence for it. "spec" caps are declared as `maximum`
// in the OpenAPI document (extracted 2026-08-19); "verified" caps were probed
// against the live API where the spec declares no bound.
type pagingCap struct {
	limit  int
	source string
}

var pagingCaps = map[string]pagingCap{
	// Declared in the OpenAPI spec.
	"list-insights":                    {500, "spec"},
	"list-insight-resource-results":    {5000, "spec"},
	"replace-insight-resource-results": {5000, "spec"},
	"list-aws-member-accounts":         {500, "spec"},
	"list-aws-organizations":           {500, "spec"},
	"list-aws-organizations-settings":  {500, "spec"},
	"list-aws-planned-purchases":       {500, "spec"},
	"list-aws-reserved-instances":      {500, "spec"},
	"list-aws-savings-plans":           {500, "spec"},
	"list-cloudflow-connections":       {100, "spec"},
	"list-cloudflow-templates":         {500, "spec"},
	"list-cloudflows":                  {500, "spec"},
	"list-service-quotas":              {200, "spec"},
	"list-tickets":                     {100, "spec"},
	// A file-shaped export rather than a JSON collection, but the cap works
	// the same way and --all reads it to size its pages (boostPageSize).
	"export-datahub-dataset-records": {50000, "spec"},
	// Not declared in the spec; probed live (2026-08-19): values above the
	// cap are silently reset to the default page size of 50.
	"list-dimensions": {500, "verified"},
	// The endpoints reject values above 250 (see fetchResourceNames).
	"list-budgets": {250, "verified"},
	"list-assets":  {250, "verified"},
	// budgets-at-risk (question_commands.go) wraps list-budgets and inherits
	// its cap unchanged.
	"budgets-at-risk": {250, "verified"},
}

// validateMaxResults rejects a --max-results value above the endpoint's known
// cap before the request is sent. Forwarding it would not fail loudly: the
// server resets out-of-range values to the default page size (50), so asking
// for 1000 returns fewer rows than asking for 500 — the worst kind of clamp.
func validateMaxResults(commandName string, args []string) error {
	entry, known := pagingCaps[commandName]
	if !known {
		return nil
	}
	raw, passed := flagValueFromArgs(args, "--max-results")
	if !passed {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= entry.limit {
		// Non-numeric values are pflag's parse error to report.
		return nil
	}
	evidence := "declared in the API spec"
	if entry.source == "verified" {
		evidence = "verified against the live API"
	}
	return invocationPreflightError{
		detail: structuredError{
			Code: "USAGE_ERROR",
			Message: fmt.Sprintf("--max-results %d exceeds the maximum of %d for %s (%s); the API silently resets out-of-range values to the default page size instead of clamping them",
				value, entry.limit, commandName, evidence),
			Hint:      fmt.Sprintf("Pass --max-results %d or less and iterate with --page-token, or pass --all to fetch every page", entry.limit),
			Retryable: false,
		},
		exitCode: exitUsage,
	}
}

// flagValueFromArgs extracts the value of a --flag from raw arguments,
// supporting both "--flag value" and "--flag=value" forms.
func flagValueFromArgs(args []string, name string) (string, bool) {
	for index, argument := range args {
		if argument == name && index+1 < len(args) {
			return args[index+1], true
		}
		if strings.HasPrefix(argument, name+"=") {
			return strings.TrimPrefix(argument, name+"="), true
		}
	}
	return "", false
}

// validateAllPagesFlags rejects --all combined with manual paging flags:
// --all owns the paging loop, so an explicit page token or page size signals
// mixed intent rather than a request the CLI can honor.
func validateAllPagesFlags(args []string) error {
	if !invocationHasFlag(args, "--all") {
		return nil
	}
	for _, conflicting := range []string{"--page-token", "--max-results"} {
		if invocationHasFlag(args, conflicting) {
			return invocationPreflightError{
				detail: structuredError{
					Code:      "USAGE_ERROR",
					Message:   fmt.Sprintf("--all fetches every page itself and cannot be combined with %s", conflicting),
					Hint:      fmt.Sprintf("Drop %s to fetch the full collection, or drop --all to page manually", conflicting),
					Retryable: false,
				},
				exitCode: exitUsage,
			}
		}
	}
	return nil
}

// validateSearchFlags rejects --search combined with manual paging flags:
// --search implies --all (searching one page would silently miss the rest of
// the collection), so --all's conflicts apply to it too.
func validateSearchFlags(args []string) error {
	if !invocationHasFlag(args, "--search") {
		return nil
	}
	for _, conflicting := range []string{"--page-token", "--max-results"} {
		if invocationHasFlag(args, conflicting) {
			return invocationPreflightError{
				detail: structuredError{
					Code:      "USAGE_ERROR",
					Message:   fmt.Sprintf("--search scans the full collection (implying --all) and cannot be combined with %s", conflicting),
					Hint:      fmt.Sprintf("Drop %s to search the whole collection, or drop --search to page manually", conflicting),
					Retryable: false,
				},
				exitCode: exitUsage,
			}
		}
	}
	return nil
}

// pagingValueFlag is a paging flag that carries a value, and the query
// parameter the operation must declare for that value to reach the API.
// Ordered, not a map, so an invocation passing both gets a stable error.
var pagingValueFlags = []struct {
	flag  string
	param string
}{
	{"--page-token", "pageToken"},
	{"--max-results", "maxResults"},
}

// validatePagingFlagsSupported rejects a paging flag whose value the CLI
// would silently drop. 187 of the 218 operations declare no pageToken and 23
// of the list commands return their whole collection in one response; on
// those, `--max-results 10` looked accepted and changed nothing, which reads
// as "this collection has 200 items" rather than "your page size was
// ignored". --all is only noted, never rejected: it asks for the complete
// collection, and a command that does not page has already answered that.
func validatePagingFlagsSupported(operation *cli.Operation, args []string) error {
	if operation == nil {
		return nil
	}
	for _, entry := range pagingValueFlags {
		if !invocationHasFlag(args, entry.flag) || operationHasQueryParam(operation, entry.param) {
			continue
		}
		return invocationPreflightError{
			detail: structuredError{
				Code:      "USAGE_ERROR",
				Message:   fmt.Sprintf("%s does not page: it takes no %s parameter, so %s would be ignored", operation.Name, entry.param, entry.flag),
				Hint:      fmt.Sprintf("Drop %s — this command returns the whole response in one request", entry.flag),
				Retryable: false,
			},
			exitCode: exitUsage,
		}
	}
	noteAllPagesIneffective(operation, args)
	return nil
}

// noteAllPagesIneffective says on stderr that --all had nothing to follow.
// Worth saying: passing it is a reasonable defensive habit, and silence
// leaves the user unsure whether the result was complete.
func noteAllPagesIneffective(operation *cli.Operation, args []string) {
	if !invocationHasFlag(args, "--all") || operationHasQueryParam(operation, "pageToken") {
		return
	}
	writer := cli.Stderr
	if writer == nil {
		writer = os.Stderr
	}
	_, _ = fmt.Fprintf(writer, "note: --all has no effect on %s; the API returns the whole response in one request\n", operation.Name)
}

func operationHasQueryParam(operation *cli.Operation, name string) bool {
	if operation == nil {
		return false
	}
	for _, parameter := range operation.QueryParams {
		if parameter != nil && parameter.Name == name {
			return true
		}
	}
	return false
}

// allPagesMaxPages bounds the --all fetch loop. At the default page size this
// is ~2,000 rows and at the common 500 cap ~20,000 — far beyond any known
// collection, while still guaranteeing termination against a server that
// keeps returning tokens.
const allPagesMaxPages = 40

// installPaginatingTransport routes HTTP calls through the --all pagination
// wrapper, exactly once per process. The name resolver keeps the unwrapped
// transport: it runs its own page loop, and re-paginating it would only buy
// redundant merging.
func installPaginatingTransport() {
	if _, installed := http.DefaultTransport.(paginatingTransport); installed {
		return
	}
	resolverHTTPClient.Transport = http.DefaultTransport
	http.DefaultTransport = paginatingTransport{next: http.DefaultTransport}
}

// paginatingTransport implements --all: when active, a GET returning a paged
// JSON collection is followed through its page tokens and the pages are
// merged into a single response before restish parses it, so every output
// format and transform downstream sees one complete collection. Installed
// around http.DefaultTransport at startup; inert unless --all was passed.
type paginatingTransport struct {
	next http.RoundTripper
}

func (t paginatingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = t.boostPageSize(request)
	// A file-shaped export caps its time window server-side; --all covers a
	// wider range by walking the windows, starting from a clamped first one.
	request, exportWindow := clampExportWindow(request)
	response, err := t.next.RoundTrip(request)
	if err != nil {
		return response, err
	}
	if t.appliesToFileExport(request, response) {
		// CSV/NDJSON pages: the token is a header and the body is a file, so
		// the JSON merge below cannot see either (export_pagination.go).
		return t.mergeFileExportPages(request, response, exportWindow)
	}
	if !t.applies(request, response) {
		return response, err
	}

	root, raw, parseErr := decodeJSONBody(response)
	if parseErr != nil || root == nil {
		return restoreBody(response, raw), err
	}
	token := collectionPageToken(root)
	collectionKey, rows, isList := listWrapperRows(root)
	if token == "" || !isList {
		return restoreBody(response, raw), nil
	}

	pages := 1
	for token != "" && pages < allPagesMaxPages {
		pageRequest := request.Clone(request.Context())
		query := pageRequest.URL.Query()
		query.Set("pageToken", token)
		pageRequest.URL.RawQuery = query.Encode()

		pageResponse, pageErr := t.next.RoundTrip(pageRequest)
		if pageErr != nil {
			return nil, fmt.Errorf("--all: fetching page %d failed: %w", pages+1, pageErr)
		}
		if pageResponse.StatusCode != http.StatusOK {
			_ = pageResponse.Body.Close()
			return nil, fmt.Errorf("--all: fetching page %d failed with HTTP status %d", pages+1, pageResponse.StatusCode)
		}
		pageRoot, _, pageParseErr := decodeJSONBody(pageResponse)
		if pageParseErr != nil || pageRoot == nil {
			return nil, fmt.Errorf("--all: page %d is not a JSON object: %v", pages+1, pageParseErr)
		}
		if pageRows, ok := pageRoot[collectionKey].([]interface{}); ok {
			rows = append(rows, pageRows...)
		}
		token = collectionPageToken(pageRoot)
		pages++
	}

	root[collectionKey] = rows
	root["rowCount"] = len(rows)
	root["pagesFetched"] = pages
	for _, key := range []string{"pageToken", "nextPageToken", "cursor", "nextCursor"} {
		delete(root, key)
	}
	if token != "" {
		// Page cap hit: keep the resume token in-band and say so.
		root["pageToken"] = token
		if cli.Stderr != nil {
			_, _ = fmt.Fprintf(cli.Stderr, "note: --all stopped after %d pages; resume with --page-token %s\n", pages, token)
		}
	}

	merged, marshalErr := json.Marshal(root)
	if marshalErr != nil {
		return nil, fmt.Errorf("--all: merging pages failed: %w", marshalErr)
	}
	return restoreBody(response, merged), nil
}

// boostPageSize raises maxResults to the endpoint's known cap so --all
// fetches the fewest pages possible; the boosted request is what the page
// loop clones, so every page keeps the size. Safe because --all cannot be
// combined with an explicit --max-results: any maxResults already on the URL
// is the spec default, not a user choice.
func (t paginatingTransport) boostPageSize(request *http.Request) *http.Request {
	if !allPagesActive() || request.Method != http.MethodGet {
		return request
	}
	boost := viper.GetInt("all-pages-boost")
	if boost <= 0 {
		return request
	}
	query := request.URL.Query()
	query.Set("maxResults", strconv.Itoa(boost))
	request = request.Clone(request.Context())
	request.URL.RawQuery = query.Encode()
	return request
}

// applies reports whether the merged-pagination path should engage for this
// request/response pair: --all passed, a GET against the DoiT API, and a
// successful JSON response. Everything else passes through untouched.
func (t paginatingTransport) applies(request *http.Request, response *http.Response) bool {
	if !allPagesActive() || request.Method != http.MethodGet || response == nil {
		return false
	}
	if response.StatusCode != http.StatusOK {
		return false
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "json") {
		return false
	}
	return sameHostAsAPIBase(request)
}

// sameHostAsAPIBase reports whether a request is aimed at the configured DCI
// API. The transport wraps http.DefaultTransport process-wide, so every
// merge path has to check this before touching a response — the update check
// and the OAuth exchange travel through it too.
func sameHostAsAPIBase(request *http.Request) bool {
	base, err := apiBase()
	if err != nil {
		return false
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return false
	}
	return strings.EqualFold(request.URL.Host, baseURL.Host)
}

func allPagesActive() bool {
	return viper.GetBool("all-pages")
}

// decodeJSONBody fully reads a response body (undoing any content encoding
// via restish's registered decoders) and parses it as a JSON object. Returns
// the raw decoded bytes so callers can restore the body untouched when the
// shape is not a paged collection.
func decodeJSONBody(response *http.Response) (map[string]interface{}, []byte, error) {
	defer func() { _ = response.Body.Close() }()
	if err := cli.DecodeResponse(response); err != nil {
		return nil, nil, err
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, nil, err
	}
	var root map[string]interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, raw, err
	}
	return root, raw, nil
}

// restoreBody rebuilds a response around plain (decoded) bytes.
func restoreBody(response *http.Response, body []byte) *http.Response {
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Del("Content-Encoding")
	response.Header.Del("Content-Length")
	return response
}

// collectionPageToken returns a list wrapper's continuation token, if any.
func collectionPageToken(body interface{}) string {
	root, ok := body.(map[string]interface{})
	if !ok {
		return ""
	}
	for _, key := range []string{"pageToken", "nextPageToken", "cursor", "nextCursor"} {
		if token, ok := root[key].(string); ok && strings.TrimSpace(token) != "" {
			return token
		}
	}
	return ""
}

// notePageTokenDropped warns on stderr when a paged collection is rendered by
// a format that discards the wrapper metadata (table, csv). TOON and JSON
// carry pageToken in-band and need no note; without one here, a truncated
// collection in table/csv output is indistinguishable from a complete one.
func notePageTokenDropped(body interface{}) {
	token := collectionPageToken(body)
	if token == "" || cli.Stderr == nil {
		return
	}
	root, ok := body.(map[string]interface{})
	if !ok {
		return
	}
	if _, _, isList := listWrapperRows(root); !isList {
		return
	}
	shown := "this page rendered"
	if count, ok := root["rowCount"]; ok {
		shown = fmt.Sprintf("first %v rendered", count)
	}
	_, _ = fmt.Fprintf(cli.Stderr, "note: more results available (%s); pass --all to fetch every page, or re-run with --page-token %s\n", shown, token)
}
