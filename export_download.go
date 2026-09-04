// Chapter: file-shaped responses & downloads (EXPORT-SPEC.md §2, §5, §6).
// Almost every DCI operation answers with JSON that the table/TOON/CSV
// renderers shape into a view. A few answer with a body that is already a
// file: DataHub's record export streams CSV (default) or newline-delimited
// JSON. For those the only correct rendering is the bytes themselves —
// restish marshals a CSV body into one JSON string with literal \n escapes,
// and an NDJSON body (no registered content type, so it stays []byte) into
// base64, both of which are unusable without --rsh-raw. This chapter passes
// such bodies through verbatim, writes any command's output to a file with
// --output-file, applies the re-import column surgery the export documents
// (--for-reimport), and says on stderr when more rows are waiting behind a
// page token.
package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

const (
	// nextPageTokenHeader carries the continuation token for exports whose
	// body is a file: a CSV or NDJSON stream has nowhere to put a wrapper
	// field, so the token rides in a response header instead (the JSON list
	// endpoints keep theirs in the body — collectionPageToken).
	nextPageTokenHeader = "X-Next-Page-Token"
	// rowCountHeader is the number of rows in the page, likewise unable to
	// live in a file-shaped body.
	rowCountHeader = "X-Row-Count"
)

// rawPassthroughContentType reports whether a successful body is a file
// rather than a document to render. Deliberately an exact allowlist of the
// media types the API actually exports: text/plain is excluded because edge
// and gateway error pages use it, and those must keep flowing through the
// error contract in error_contract.go rather than being dumped to stdout as
// if they were data.
func rawPassthroughContentType(contentType string) bool {
	value := strings.ToLower(strings.TrimSpace(contentType))
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	switch value {
	case "text/csv", "application/csv", "application/x-ndjson", "application/jsonl", "application/x-jsonlines", "application/x-ldjson":
		return true
	}
	return false
}

// rawPassthroughCSV reports whether the content type is one of the CSV
// spellings, the only shape --for-reimport can rewrite.
func rawPassthroughCSV(contentType string) bool {
	value := strings.ToLower(strings.TrimSpace(contentType))
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return value == "text/csv" || value == "application/csv"
}

// rawBodyFileExtension is the extension for a derived download filename when
// the API sends no Content-Disposition.
func rawBodyFileExtension(contentType string) string {
	if rawPassthroughCSV(contentType) {
		return ".csv"
	}
	if rawPassthroughContentType(contentType) {
		return ".jsonl"
	}
	return ".json"
}

// rawPassthroughBody returns the bytes of a file-shaped success body.
// Restish parses text/csv through its Text content type (a Go string) and
// leaves NDJSON as raw []byte, so both shapes have to be accepted here.
func rawPassthroughBody(resp cli.Response) ([]byte, bool) {
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, false
	}
	if !rawPassthroughContentType(headerValue(resp.Headers, "Content-Type")) {
		return nil, false
	}
	switch body := resp.Body.(type) {
	case []byte:
		return body, true
	case string:
		return []byte(body), true
	}
	return nil, false
}

// rawBodyOutputGuard is the outermost response formatter: a file-shaped body
// bypasses every renderer and transform below it (they exist to shape JSON
// documents and would only corrupt a file), and --output-file redirects
// whatever the formatters below do write.
type rawBodyOutputGuard struct {
	next cli.ResponseFormatter
}

// installRawBodyOutputGuard wraps the active formatter. Installed last so it
// sits outside dciResponseGuard: the error paths there still see every
// non-2xx and HTML-error response, because this guard only claims successful
// bodies whose content type is on the allowlist.
func installRawBodyOutputGuard() {
	if cli.Formatter == nil {
		return
	}
	cli.Formatter = rawBodyOutputGuard{next: cli.Formatter}
}

func (guard rawBodyOutputGuard) Format(resp cli.Response) error {
	body, isRaw := rawPassthroughBody(resp)
	if isRaw {
		body = applyExportReimportTransform(body, headerValue(resp.Headers, "Content-Type"))
	}

	// A failed or empty response never touches the file: creating (and so
	// truncating) it for a 404 or a 204 would destroy a previous export and
	// leave a zero-byte file looking like the answer.
	if !isRaw && (resp.Status < 200 || resp.Status >= 300 || responseBodyIsEmpty(resp.Body)) {
		return guard.next.Format(resp)
	}

	target, err := resolveOutputFilePath(resp)
	if err != nil {
		return reportOutputFileError(err)
	}

	if target == "" {
		if !isRaw {
			return guard.next.Format(resp)
		}
		if _, writeErr := cli.Stdout.Write(body); writeErr != nil {
			return writeErr
		}
		noteRawExportContinuation(resp)
		return nil
	}

	file, err := os.Create(target)
	if err != nil {
		return reportOutputFileError(err)
	}
	written, formatErr := guard.writeTo(file, resp, body, isRaw)
	closeErr := file.Close()
	if formatErr != nil {
		return formatErr
	}
	if closeErr != nil {
		return reportOutputFileError(closeErr)
	}
	noteOutputFileWritten(target, written)
	noteRawExportContinuation(resp)
	return nil
}

// writeTo sends the command's output to file: the raw bytes for a
// file-shaped body, and otherwise whatever the formatters below would have
// printed, captured by pointing cli.Stdout at the file for the duration of
// the call. Swapping the writer (rather than teaching every renderer about a
// destination) is what makes --output-file work uniformly for table, JSON,
// YAML, CSV and TOON output.
func (guard rawBodyOutputGuard) writeTo(file *os.File, resp cli.Response, body []byte, isRaw bool) (int64, error) {
	if isRaw {
		written, err := file.Write(body)
		return int64(written), err
	}
	counter := &countingWriter{next: file}
	previous := cli.Stdout
	cli.Stdout = counter
	defer func() { cli.Stdout = previous }()
	if err := guard.next.Format(resp); err != nil {
		return counter.written, err
	}
	return counter.written, nil
}

type countingWriter struct {
	next    io.Writer
	written int64
}

func (writer *countingWriter) Write(data []byte) (int, error) {
	n, err := writer.next.Write(data)
	writer.written += int64(n)
	return n, err
}

// resolveOutputFilePath resolves --output-file for this response. A path
// naming an existing directory (or one written with a trailing separator)
// takes the filename the API suggests in Content-Disposition, so
// `--output-file .` lands the export under the name the console would have
// used; anything else is used verbatim, overwriting like a shell redirect.
func resolveOutputFilePath(resp cli.Response) (string, error) {
	requested := strings.TrimSpace(viper.GetString("output-file"))
	if requested == "" {
		return "", nil
	}
	directory := strings.HasSuffix(requested, string(os.PathSeparator)) || requested == "."
	if !directory {
		if info, err := os.Stat(requested); err == nil && info.IsDir() {
			directory = true
		}
	}
	if !directory {
		return requested, nil
	}
	name := suggestedDownloadFilename(resp)
	if name == "" {
		return "", fmt.Errorf("--output-file %s is a directory and the API suggested no filename; pass a file path instead", requested)
	}
	path := filepath.Join(requested, name)
	// A derived name is the server's choice, not the user's, so it must never
	// clobber something already on disk.
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists; pass an explicit --output-file path to overwrite it", path)
	}
	return path, nil
}

// suggestedDownloadFilename reads the filename out of Content-Disposition.
// The value is server-controlled, so only its base name is ever used: a
// header carrying "../../.ssh/authorized_keys" must not escape the directory
// the user pointed at.
func suggestedDownloadFilename(resp cli.Response) string {
	disposition := headerValue(resp.Headers, "Content-Disposition")
	if disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			candidate := filepath.Base(strings.TrimSpace(params["filename"]))
			if candidate != "" && candidate != "." && candidate != ".." && candidate != string(os.PathSeparator) && !strings.HasPrefix(candidate, ".") {
				return candidate
			}
		}
	}
	command := invokedCommandName
	if command == "" {
		return ""
	}
	return command + rawBodyFileExtension(headerValue(resp.Headers, "Content-Type"))
}

func noteOutputFileWritten(path string, written int64) {
	if cli.Stderr == nil {
		return
	}
	_, _ = fmt.Fprintf(cli.Stderr, "wrote %s to %s\n", humanByteSize(written), path)
}

func humanByteSize(size int64) string {
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(size)/float64(1<<30))
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/float64(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/float64(1<<10))
	default:
		return fmt.Sprintf("%d bytes", size)
	}
}

// noteRawExportContinuation warns that a file-shaped response is one page of
// several. The JSON list commands get this from notePageTokenDropped, which
// reads the token out of the body; here the token is a header, and nothing in
// the CSV or NDJSON stream hints that more rows exist — a truncated export
// looks exactly like a complete one.
func noteRawExportContinuation(resp cli.Response) {
	if allPagesActive() || cli.Stderr == nil {
		return
	}
	token := strings.TrimSpace(headerValue(resp.Headers, nextPageTokenHeader))
	if token == "" {
		return
	}
	shown := "this page written"
	if rows := strings.TrimSpace(headerValue(resp.Headers, rowCountHeader)); rows != "" {
		shown = fmt.Sprintf("%s rows written", rows)
	}
	_, _ = fmt.Fprintf(cli.Stderr, "note: more rows available (%s); pass --all to fetch every page, or re-run with --page-token %s\n", shown, token)
}

func reportOutputFileError(err error) error {
	responseExitCode = exitUsage
	detail := structuredError{
		Code:      "USAGE_ERROR",
		Message:   fmt.Sprintf("unable to write --output-file: %v", err),
		Hint:      "Pass a writable file path, or drop --output-file to print to stdout",
		Retryable: false,
	}
	if agentErrorContractEnabled() {
		writeStructuredError(cli.Stderr, detail)
		return nil
	}
	if cli.Stderr != nil {
		_, _ = fmt.Fprintf(cli.Stderr, "Error: %s\n", detail.Message)
	}
	return nil
}

// datahubExportProvenanceColumns are the export-only columns --for-reimport
// drops, and datahubExportIDColumn the one it renames, both straight from the
// operation's own re-import recipe: "drop the batch, source, export_time and
// updated_by columns, and either drop event_id or rename it to id".
var datahubExportProvenanceColumns = []string{"batch", "source", "export_time", "updated_by"}

const (
	datahubExportIDColumn = "event_id"
	datahubIngestIDColumn = "id"
)

// applyExportReimportTransform rewrites an exported CSV into the ingest
// vocabulary so `dci ingest-datahub-events-csv` accepts it unchanged. A body
// that is not CSV, or that does not parse, is returned untouched with a note:
// this is a convenience over the bytes the API sent, and losing the export to
// a formatting surprise would be worse than skipping the rewrite.
func applyExportReimportTransform(body []byte, contentType string) []byte {
	if !viper.GetBool("export-for-reimport") {
		return body
	}
	if !rawPassthroughCSV(contentType) {
		noteReimportSkipped("the response is not CSV")
		return body
	}
	rewritten, err := rewriteCSVForReimport(body)
	if err != nil {
		noteReimportSkipped(err.Error())
		return body
	}
	return rewritten
}

func noteReimportSkipped(reason string) {
	if cli.Stderr == nil {
		return
	}
	_, _ = fmt.Fprintf(cli.Stderr, "note: --for-reimport left the response unchanged (%s)\n", reason)
}

func rewriteCSVForReimport(body []byte) ([]byte, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return body, nil
	}
	reader := csv.NewReader(strings.NewReader(string(body)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("the CSV did not parse: %v", err)
	}
	if len(records) == 0 {
		return body, nil
	}

	dropped := map[string]bool{}
	for _, column := range datahubExportProvenanceColumns {
		dropped[column] = true
	}
	header := records[0]
	kept := make([]int, 0, len(header))
	outHeader := make([]string, 0, len(header))
	for index, column := range header {
		if dropped[column] {
			continue
		}
		kept = append(kept, index)
		if column == datahubExportIDColumn {
			column = datahubIngestIDColumn
		}
		outHeader = append(outHeader, column)
	}

	var out strings.Builder
	writer := csv.NewWriter(&out)
	if err := writer.Write(outHeader); err != nil {
		return nil, err
	}
	row := make([]string, len(kept))
	for _, record := range records[1:] {
		for position, index := range kept {
			if index < len(record) {
				row[position] = record[index]
				continue
			}
			row[position] = ""
		}
		if err := writer.Write(row); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}
