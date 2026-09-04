package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

// writingFormatter stands in for the renderers below the raw-body guard: it
// writes to cli.Stdout the way restish's formatter does, so an --output-file
// test can prove the redirect captures their output too.
type writingFormatter struct {
	payload string
	called  bool
}

func (formatter *writingFormatter) Format(resp cli.Response) error {
	formatter.called = true
	_, err := cli.Stdout.Write([]byte(formatter.payload))
	return err
}

func captureExportStdout(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := cli.Stdout
	var stdout bytes.Buffer
	cli.Stdout = &stdout
	t.Cleanup(func() { cli.Stdout = previous })
	return &stdout
}

func resetExportDownloadState(t *testing.T) {
	t.Helper()
	previousCommand := invokedCommandName
	viper.Set("output-file", "")
	viper.Set("export-for-reimport", false)
	viper.Set("all-pages", false)
	t.Cleanup(func() {
		invokedCommandName = previousCommand
		viper.Set("output-file", "")
		viper.Set("export-for-reimport", false)
		viper.Set("all-pages", false)
	})
}

func csvResponse(body string) cli.Response {
	return cli.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "text/csv; charset=utf-8"},
		Body:    body,
	}
}

func TestRawPassthroughContentType(t *testing.T) {
	for _, contentType := range []string{
		"text/csv", "text/csv; charset=utf-8", "TEXT/CSV", "application/csv",
		"application/x-ndjson", "application/jsonl", "application/x-ldjson",
	} {
		if !rawPassthroughContentType(contentType) {
			t.Errorf("rawPassthroughContentType(%q) = false, want true", contentType)
		}
	}
	// text/plain and text/html carry gateway error pages, which must stay on
	// the error contract rather than being dumped to stdout as data.
	for _, contentType := range []string{
		"application/json", "application/yaml", "text/plain", "text/html", "",
	} {
		if rawPassthroughContentType(contentType) {
			t.Errorf("rawPassthroughContentType(%q) = true, want false", contentType)
		}
	}
}

func TestRawPassthroughBodyAcceptsStringAndBytes(t *testing.T) {
	if body, ok := rawPassthroughBody(csvResponse("a,b\n1,2\n")); !ok || string(body) != "a,b\n1,2\n" {
		t.Errorf("string body = %q, %v; want the CSV and true", body, ok)
	}
	ndjson := cli.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "application/x-ndjson"},
		Body:    []byte("{\"id\":1}\n"),
	}
	if body, ok := rawPassthroughBody(ndjson); !ok || string(body) != "{\"id\":1}\n" {
		t.Errorf("byte body = %q, %v; want the NDJSON and true", body, ok)
	}
	// A non-2xx CSV body is an error the contract must still classify.
	failure := csvResponse("boom")
	failure.Status = 500
	if _, ok := rawPassthroughBody(failure); ok {
		t.Error("5xx CSV claimed by the passthrough; want the error contract to keep it")
	}
}

func TestRawBodyOutputGuardWritesFileBodyVerbatim(t *testing.T) {
	resetExportDownloadState(t)
	stdout := captureExportStdout(t)
	next := &recordingFormatter{}
	guard := rawBodyOutputGuard{next: next}

	csv := "event_id,usage_date\nabc,2026-01-01T00:00:00Z\n"
	if err := guard.Format(csvResponse(csv)); err != nil {
		t.Fatal(err)
	}
	if next.called {
		t.Error("delegated a file-shaped body to the renderers; want a verbatim write")
	}
	if stdout.String() != csv {
		t.Errorf("stdout = %q, want the CSV verbatim", stdout.String())
	}
}

func TestRawBodyOutputGuardDelegatesJSONBodies(t *testing.T) {
	resetExportDownloadState(t)
	captureExportStdout(t)
	next := &recordingFormatter{}
	guard := rawBodyOutputGuard{next: next}

	if err := guard.Format(cli.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    map[string]interface{}{"name": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if !next.called {
		t.Error("JSON body was not delegated to the renderers")
	}
}

func TestRawBodyOutputGuardWritesOutputFile(t *testing.T) {
	resetExportDownloadState(t)
	stdout := captureExportStdout(t)
	stderr := capturePaginationStderr(t)
	path := filepath.Join(t.TempDir(), "export.csv")
	viper.Set("output-file", path)

	csv := "event_id,usage_date\nabc,2026-01-01T00:00:00Z\n"
	if err := (rawBodyOutputGuard{next: &recordingFormatter{}}).Format(csvResponse(csv)); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != csv {
		t.Errorf("file = %q, want the CSV verbatim", written)
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want nothing once --output-file redirected it", stdout.String())
	}
	if !strings.Contains(stderr.String(), path) {
		t.Errorf("stderr = %q, want the written path", stderr.String())
	}
}

func TestRawBodyOutputGuardRedirectsRenderedOutputToFile(t *testing.T) {
	resetExportDownloadState(t)
	stdout := captureExportStdout(t)
	capturePaginationStderr(t)
	path := filepath.Join(t.TempDir(), "table.txt")
	viper.Set("output-file", path)

	next := &writingFormatter{payload: "| name |\n| x |\n"}
	if err := (rawBodyOutputGuard{next: next}).Format(cli.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    map[string]interface{}{"name": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != next.payload {
		t.Errorf("file = %q, want the rendered output", written)
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want nothing once --output-file redirected it", stdout.String())
	}
}

func TestRawBodyOutputGuardReportsUnwritableOutputFile(t *testing.T) {
	resetExportDownloadState(t)
	captureExportStdout(t)
	stderr := capturePaginationStderr(t)
	resetErrorContractState()
	t.Cleanup(resetErrorContractState)
	viper.Set("output-file", filepath.Join(t.TempDir(), "missing-dir", "export.csv"))

	if err := (rawBodyOutputGuard{next: &recordingFormatter{}}).Format(csvResponse("a,b\n")); err != nil {
		t.Fatalf("Format returned %v; want a reported error, not a panic-inducing one", err)
	}
	if responseExitCode != exitUsage {
		t.Errorf("responseExitCode = %d, want %d", responseExitCode, exitUsage)
	}
	if !strings.Contains(stderr.String(), "--output-file") {
		t.Errorf("stderr = %q, want the flag named", stderr.String())
	}
}

func TestResolveOutputFilePathUsesSuggestedNameInDirectory(t *testing.T) {
	resetExportDownloadState(t)
	directory := t.TempDir()
	viper.Set("output-file", directory)
	resp := csvResponse("a\n")
	resp.Headers["Content-Disposition"] = `attachment; filename="records-20260101-p0.csv"`

	path, err := resolveOutputFilePath(resp)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(directory, "records-20260101-p0.csv") {
		t.Errorf("path = %q, want the suggested name inside the directory", path)
	}

	// A derived name must never clobber an existing file.
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveOutputFilePath(resp); err == nil {
		t.Error("resolveOutputFilePath overwrote an existing file; want a refusal")
	}
}

func TestResolveOutputFilePathIgnoresTraversalInSuggestedName(t *testing.T) {
	resetExportDownloadState(t)
	directory := t.TempDir()
	viper.Set("output-file", directory)
	resp := csvResponse("a\n")
	resp.Headers["Content-Disposition"] = `attachment; filename="../../escape.csv"`

	path, err := resolveOutputFilePath(resp)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(directory, "escape.csv") {
		t.Errorf("path = %q, want the base name inside the requested directory", path)
	}
}

func TestResolveOutputFilePathFallsBackToCommandName(t *testing.T) {
	resetExportDownloadState(t)
	directory := t.TempDir()
	viper.Set("output-file", directory)
	invokedCommandName = "export-datahub-dataset-records"

	path, err := resolveOutputFilePath(csvResponse("a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(directory, "export-datahub-dataset-records.csv") {
		t.Errorf("path = %q, want the command name with a .csv extension", path)
	}
}

func TestResolveOutputFilePathVerbatimWithoutDirectory(t *testing.T) {
	resetExportDownloadState(t)
	path := filepath.Join(t.TempDir(), "chosen.csv")
	viper.Set("output-file", path)
	got, err := resolveOutputFilePath(csvResponse("a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
}

func TestNoteRawExportContinuation(t *testing.T) {
	resetExportDownloadState(t)
	resp := csvResponse("a\n")
	resp.Headers[nextPageTokenHeader] = "tok2"
	resp.Headers[rowCountHeader] = "50000"

	stderr := capturePaginationStderr(t)
	noteRawExportContinuation(resp)
	note := stderr.String()
	if !strings.Contains(note, "tok2") || !strings.Contains(note, "--all") || !strings.Contains(note, "50000") {
		t.Errorf("note = %q, want the token, --all, and the row count", note)
	}

	// Under --all the fetch already followed the token; no note.
	viper.Set("all-pages", true)
	stderr = capturePaginationStderr(t)
	noteRawExportContinuation(resp)
	if stderr.String() != "" {
		t.Errorf("note under --all = %q, want none", stderr.String())
	}
	viper.Set("all-pages", false)

	// Last page: no token, no note.
	stderr = capturePaginationStderr(t)
	noteRawExportContinuation(csvResponse("a\n"))
	if stderr.String() != "" {
		t.Errorf("note without a token = %q, want none", stderr.String())
	}
}

func TestRewriteCSVForReimport(t *testing.T) {
	export := "event_id,batch,source,export_time,updated_by,usage_date,fixed.project,metric.cost\n" +
		"abc,api_1,api,2026-02-23T23:22:39Z,someone@example.com,2026-01-01T00:00:00Z,\"Acme, Inc\",1.5\n"
	rewritten, err := rewriteCSVForReimport([]byte(export))
	if err != nil {
		t.Fatal(err)
	}
	want := "id,usage_date,fixed.project,metric.cost\n" +
		"abc,2026-01-01T00:00:00Z,\"Acme, Inc\",1.5\n"
	if string(rewritten) != want {
		t.Errorf("rewritten =\n%s\nwant\n%s", rewritten, want)
	}
}

func TestApplyExportReimportTransformOnlyRewritesCSV(t *testing.T) {
	resetExportDownloadState(t)
	body := []byte("event_id,batch,source,export_time,updated_by,usage_date\nabc,b,s,t,u,2026-01-01T00:00:00Z\n")

	// Flag off: untouched.
	if got := applyExportReimportTransform(body, "text/csv"); string(got) != string(body) {
		t.Errorf("without the flag the body changed to %q", got)
	}

	viper.Set("export-for-reimport", true)
	if got := applyExportReimportTransform(body, "text/csv"); !strings.HasPrefix(string(got), "id,usage_date") {
		t.Errorf("rewritten body = %q, want the ingest header", got)
	}

	// NDJSON is already in the ingest event shape: unchanged, with a note.
	stderr := capturePaginationStderr(t)
	ndjson := []byte("{\"id\":\"abc\"}\n")
	if got := applyExportReimportTransform(ndjson, "application/x-ndjson"); string(got) != string(ndjson) {
		t.Errorf("NDJSON body changed to %q", got)
	}
	if !strings.Contains(stderr.String(), "--for-reimport") {
		t.Errorf("stderr = %q, want a note that the rewrite was skipped", stderr.String())
	}

	// Unparsable CSV keeps the export rather than losing it to a rewrite.
	stderr = capturePaginationStderr(t)
	broken := []byte("event_id,batch\n\"unterminated,x\n")
	if got := applyExportReimportTransform(broken, "text/csv"); string(got) != string(broken) {
		t.Errorf("broken CSV changed to %q, want it untouched", got)
	}
	if !strings.Contains(stderr.String(), "--for-reimport") {
		t.Errorf("stderr = %q, want a note that the rewrite was skipped", stderr.String())
	}
}

func TestHumanByteSize(t *testing.T) {
	for _, testCase := range []struct {
		size int64
		want string
	}{
		{512, "512 bytes"},
		{2048, "2.0 KB"},
		{5 * 1 << 20, "5.0 MB"},
		{3 * 1 << 30, "3.0 GB"},
	} {
		if got := humanByteSize(testCase.size); got != testCase.want {
			t.Errorf("humanByteSize(%d) = %q, want %q", testCase.size, got, testCase.want)
		}
	}
}

func TestSuggestedDownloadFilenameWithoutHeaderOrCommand(t *testing.T) {
	resetExportDownloadState(t)
	invokedCommandName = ""
	if got := suggestedDownloadFilename(csvResponse("a\n")); got != "" {
		t.Errorf("suggestedDownloadFilename = %q, want empty so the caller can ask for a path", got)
	}
	if _, err := resolveOutputFilePathForDirectory(t); err == nil {
		t.Error("a directory target with no derivable name was accepted; want an error")
	}
}

// resolveOutputFilePathForDirectory points --output-file at a directory with
// no Content-Disposition and no command name to derive from.
func resolveOutputFilePathForDirectory(t *testing.T) (string, error) {
	t.Helper()
	viper.Set("output-file", t.TempDir()+string(os.PathSeparator))
	return resolveOutputFilePath(cli.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "text/csv"},
		Body:    "a\n",
	})
}

func TestRawBodyFileExtension(t *testing.T) {
	for contentType, want := range map[string]string{
		"text/csv":             ".csv",
		"application/csv":      ".csv",
		"application/x-ndjson": ".jsonl",
		"application/json":     ".json",
	} {
		if got := rawBodyFileExtension(contentType); got != want {
			t.Errorf("rawBodyFileExtension(%q) = %q, want %q", contentType, got, want)
		}
	}
}

func TestInstallRawBodyOutputGuardIsOutermost(t *testing.T) {
	previous := cli.Formatter
	t.Cleanup(func() { cli.Formatter = previous })
	inner := &recordingFormatter{}
	cli.Formatter = inner
	installRawBodyOutputGuard()
	guard, ok := cli.Formatter.(rawBodyOutputGuard)
	if !ok {
		t.Fatalf("cli.Formatter = %T, want rawBodyOutputGuard", cli.Formatter)
	}
	if fmt.Sprintf("%p", guard.next) != fmt.Sprintf("%p", inner) {
		t.Error("guard does not wrap the previously installed formatter")
	}
}

func TestRawBodyOutputGuardLeavesFileAloneOnFailure(t *testing.T) {
	resetExportDownloadState(t)
	captureExportStdout(t)
	capturePaginationStderr(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "records.csv")
	if err := os.WriteFile(path, []byte("a previous export\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set("output-file", path)

	next := &recordingFormatter{}
	if err := (rawBodyOutputGuard{next: next}).Format(cli.Response{
		Status:  404,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    map[string]interface{}{"error": "not found"},
	}); err != nil {
		t.Fatal(err)
	}
	if !next.called {
		t.Error("a failed response was not delegated to the error contract")
	}
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the existing file was destroyed by a failed request: %v", err)
	}
	if string(kept) != "a previous export\n" {
		t.Errorf("file = %q, want the previous export untouched", kept)
	}
}

func TestRawBodyOutputGuardLeavesFileAloneOnEmptyBody(t *testing.T) {
	resetExportDownloadState(t)
	captureExportStdout(t)
	capturePaginationStderr(t)
	path := filepath.Join(t.TempDir(), "records.csv")
	viper.Set("output-file", path)

	if err := (rawBodyOutputGuard{next: &recordingFormatter{}}).Format(cli.Response{
		Status:  204,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    nil,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("an empty response created a zero-byte file; want no file at all")
	}
}
