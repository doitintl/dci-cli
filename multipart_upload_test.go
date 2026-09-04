package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

// csvUploadHelp mirrors the long help restish generates for
// ingest-datahub-events-csv from the production spec (`--help-full`).
const csvUploadHelp = "Sends a batch of events to DataHub using a CSV file.\n" +
	"## Request Schema (multipart/form-data)\n\n```schema\n{\n" +
	"  file: (string format:binary) The CSV file to upload, either uncompressed or compressed in ZIP or GZ format. The maximum file size is 30 MB.\n" +
	"  provider: (string) The identifier of the data provider.\n" +
	"}\n```\n"

func csvUploadSketches(t *testing.T) []bodyFieldSketch {
	t.Helper()
	sketches := requestSchemaTopLevelFieldSketches(csvUploadHelp)
	if len(sketches) != 2 {
		t.Fatalf("expected two sketches, got %+v", sketches)
	}
	return sketches
}

func writeUploadFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type formPart struct {
	filename    string
	contentType string
	body        string
}

func parseMultipart(t *testing.T, body []byte, contentType string) map[string]formPart {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type %q: %v", contentType, err)
	}
	if mediaType != multipartMediaType {
		t.Fatalf("media type = %q, want %s", mediaType, multipartMediaType)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	parts := map[string]formPart{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		parts[part.FormName()] = formPart{
			filename:    part.FileName(),
			contentType: part.Header.Get("Content-Type"),
			body:        string(data),
		}
	}
	return parts
}

func TestRequestSchemaMediaType(t *testing.T) {
	if got := requestSchemaMediaType(csvUploadHelp); got != multipartMediaType {
		t.Fatalf("media type = %q, want %s", got, multipartMediaType)
	}
	if got := requestSchemaMediaType(queryRequestHelp); got != "application/json" {
		t.Fatalf("json media type = %q", got)
	}
	if got := requestSchemaMediaType("Lists budgets.\n"); got != "" {
		t.Fatalf("expected no media type for a bodyless command, got %q", got)
	}
	if !isMultipartCommand(&cobra.Command{Long: csvUploadHelp}) {
		t.Fatal("csv upload help should mark the command multipart")
	}
	if isMultipartCommand(&cobra.Command{Long: queryRequestHelp}) {
		t.Fatal("json command must not be treated as multipart")
	}
}

func TestBuildMultipartBodyEncodesFileAndTextParts(t *testing.T) {
	path := writeUploadFixture(t, "events.csv", "usage_date,metric.cost\n2026-09-01T00:00:00Z,12\n")
	args := []string{"provider:", "litellm-usage,", "file:", "@" + path}

	body, contentType, err := buildMultipartBody("ingest-datahub-events-csv", csvUploadSketches(t), args, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	parts := parseMultipart(t, body, contentType)
	file, ok := parts["file"]
	if !ok {
		t.Fatalf("no file part in %+v", parts)
	}
	if file.filename != "events.csv" {
		t.Errorf("filename = %q, want events.csv", file.filename)
	}
	if file.contentType != "text/csv" {
		t.Errorf("file content type = %q, want text/csv", file.contentType)
	}
	if !strings.HasPrefix(file.body, "usage_date,metric.cost") {
		t.Errorf("file body = %q", file.body)
	}
	provider, ok := parts["provider"]
	if !ok || provider.body != "litellm-usage" || provider.filename != "" {
		t.Errorf("provider part = %+v", provider)
	}
	if len(parts) != 2 {
		t.Errorf("expected exactly two parts, got %d", len(parts))
	}
}

func TestBuildMultipartBodyContentTypesByExtension(t *testing.T) {
	for _, testCase := range []struct{ name, contentType string }{
		{"events.csv.gz", "application/gzip"},
		{"events.zip", "application/zip"},
		{"EVENTS.CSV", "text/csv"},
		{"events.dat", "application/octet-stream"},
	} {
		path := writeUploadFixture(t, testCase.name, "data")
		body, contentType, err := buildMultipartBody("ingest-datahub-events-csv", csvUploadSketches(t), []string{"provider: x, file: @" + path}, false)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		file := parseMultipart(t, body, contentType)["file"]
		if file.contentType != testCase.contentType {
			t.Errorf("%s: content type = %q, want %q", testCase.name, file.contentType, testCase.contentType)
		}
		if file.filename != testCase.name {
			t.Errorf("%s: filename = %q", testCase.name, file.filename)
		}
	}
}

func TestBuildMultipartBodyPreflightErrors(t *testing.T) {
	existing := writeUploadFixture(t, "events.csv", "usage_date\n2026-09-01T00:00:00Z\n")
	empty := writeUploadFixture(t, "empty.csv", "")
	directory := t.TempDir()

	testCommand := "test-upload"
	uploadSizeLimits[testCommand] = 16
	t.Cleanup(func() { delete(uploadSizeLimits, testCommand) })

	testCases := []struct {
		name     string
		command  string
		args     []string
		stdin    bool
		problem  string
		hint     string
		wantHint bool
	}{
		{
			name: "binary field without @", command: "ingest-datahub-events-csv",
			args:    []string{"provider: x, file: events.csv"},
			problem: "file must name a file to upload", hint: "Use file: @events.csv", wantHint: true,
		},
		{
			name: "missing file", command: "ingest-datahub-events-csv",
			args:    []string{"provider: x, file: @" + filepath.Join(directory, "nope.csv")},
			problem: "cannot read", hint: "relative paths resolve from the current directory", wantHint: true,
		},
		{
			name: "stdin body", command: "ingest-datahub-events-csv",
			args: []string{"provider: x"}, stdin: true,
			problem: "needs a filename", hint: "file: @events.csv", wantHint: true,
		},
		{
			name: "binary field absent", command: "ingest-datahub-events-csv",
			args:    []string{"provider: x"},
			problem: "file is required for this upload", hint: "Add file: @<path>", wantHint: true,
		},
		{
			name: "oversize", command: testCommand,
			args:    []string{"provider: x, file: @" + existing},
			problem: "the API accepts up to", hint: "gzip -k", wantHint: true,
		},
		{
			name: "empty file", command: "ingest-datahub-events-csv",
			args:    []string{"provider: x, file: @" + empty},
			problem: "is empty",
		},
		{
			name: "directory", command: "ingest-datahub-events-csv",
			args:    []string{"provider: x, file: @" + directory},
			problem: "is a directory", hint: "Point file: @<path>", wantHint: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := buildMultipartBody(testCase.command, csvUploadSketches(t), testCase.args, testCase.stdin)
			var uploadError multipartUploadError
			if !errors.As(err, &uploadError) {
				t.Fatalf("expected multipartUploadError, got %v", err)
			}
			if !strings.Contains(uploadError.problem, testCase.problem) {
				t.Errorf("problem = %q, want it to contain %q", uploadError.problem, testCase.problem)
			}
			if testCase.wantHint && !strings.Contains(uploadError.AgentErrorHint(), testCase.hint) {
				t.Errorf("hint = %q, want it to contain %q", uploadError.AgentErrorHint(), testCase.hint)
			}
			if uploadError.ExitCode() != exitUsage || uploadError.AgentErrorCode() != "USAGE_ERROR" || uploadError.AgentErrorRetryable() {
				t.Errorf("error contract = exit %d code %s retryable %v", uploadError.ExitCode(), uploadError.AgentErrorCode(), uploadError.AgentErrorRetryable())
			}
		})
	}
}

func TestValidateRequestBodySkipsShapeCheckForMultipart(t *testing.T) {
	command := &cobra.Command{Long: csvUploadHelp}
	// The file does not exist: the shape check (file input enabled) would
	// fail here with shorthand's message; the multipart encoder owns that.
	if err := validateRequestBody(command, []string{"provider:", "x,", "file:", "@/nonexistent/events.csv"}); err != nil {
		t.Fatalf("expected the field-name check alone to pass, got %v", err)
	}
	err := validateRequestBody(command, []string{"provider:", "x,", "attachment:", "@events.csv"})
	var validationError requestBodyValidationError
	if !errors.As(err, &validationError) {
		t.Fatalf("expected an unknown-field error, got %v", err)
	}
}

func TestBuildOperationRequestMirrorsRestish(t *testing.T) {
	operation := cli.Operation{
		Name:          "upload-attachment",
		Method:        http.MethodPost,
		URITemplate:   "https://api.example.com/tickets/{id}/attachments",
		PathParams:    []*cli.Param{{Name: "id", Type: "string"}},
		QueryParams:   []*cli.Param{{Name: "notify", Type: "boolean"}, {Name: "tag", Type: "array"}},
		HeaderParams:  []*cli.Param{{Name: "X-Trace", Type: "string"}},
		BodyMediaType: multipartMediaType,
	}
	command := &cobra.Command{Use: "upload-attachment id"}
	command.Flags().Bool("notify", false, "")
	command.Flags().StringSlice("tag", nil, "")
	command.Flags().String("x-trace", "", "")
	if err := command.Flags().Parse([]string{"--notify", "--tag", "a", "--tag", "b", "--x-trace", "t-1"}); err != nil {
		t.Fatal(err)
	}

	request, err := buildOperationRequest(operation, command, []string{"T-42"}, []byte("body"), "multipart/form-data; boundary=x")
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost {
		t.Errorf("method = %s", request.Method)
	}
	if request.URL.Path != "/tickets/T-42/attachments" {
		t.Errorf("path = %s", request.URL.Path)
	}
	if got := request.URL.Query()["tag"]; strings.Join(got, ",") != "a,b" {
		t.Errorf("tag query = %v", got)
	}
	if got := request.URL.Query().Get("notify"); got != "true" {
		t.Errorf("notify query = %q", got)
	}
	if got := request.Header.Get("X-Trace"); got != "t-1" {
		t.Errorf("X-Trace header = %q", got)
	}
	if got := request.Header.Get("Content-Type"); got != "multipart/form-data; boundary=x" {
		t.Errorf("content type = %q", got)
	}
	if request.ContentLength != 4 {
		t.Errorf("content length = %d, want 4", request.ContentLength)
	}
}

func TestSetMultipartOperationsIndexesOnlyMultipart(t *testing.T) {
	setMultipartOperations([]cli.Operation{
		{Name: "ingest-datahub-events", BodyMediaType: "application/json"},
		{Name: "ingest-datahub-events-csv", BodyMediaType: multipartMediaType},
		{Name: "list-budgets"},
	})
	t.Cleanup(func() { multipartOperations = map[string]cli.Operation{} })
	if len(multipartOperations) != 1 {
		t.Fatalf("indexed %d operations, want 1: %v", len(multipartOperations), multipartOperations)
	}
	if _, ok := multipartOperations["ingest-datahub-events-csv"]; !ok {
		t.Fatal("csv upload not indexed")
	}
}

func TestUploadSizeLimitsNameCuratedCommands(t *testing.T) {
	docs, err := loadCommandDocs()
	if err != nil {
		t.Fatal(err)
	}
	for name := range uploadSizeLimits {
		if _, ok := docs[name]; !ok {
			t.Errorf("uploadSizeLimits names %q, which has no command-docs file; the command may have been renamed or removed", name)
		}
	}
}

func TestUploadSizeLimitsAgainstSpec(t *testing.T) {
	spec := loadCommandDocsSpec(t)
	for name := range uploadSizeLimits {
		operation := spec.operation(name)
		if operation == nil {
			t.Errorf("uploadSizeLimits names %q, which is not an operation in the spec", name)
			continue
		}
		if !strings.Contains(operation.BodyMediaType, multipartMediaType) {
			t.Errorf("%q is no longer a multipart operation (media type %q)", name, operation.BodyMediaType)
		}
	}
}

// csvUploadSpec is a hermetic OpenAPI document with the DataHub CSV upload
// (multipart) and the JSON events ingest, so the built binary can exercise
// both paths against a local server.
const csvUploadSpec = `{
  "openapi": "3.0.0",
  "info": {"title": "DCI test", "version": "1.0.0"},
  "paths": {
    "/datahub/v1/csv/upload": {
      "post": {
        "operationId": "datahubEventsCSVFile",
        "x-cli-name": "ingest-datahub-events-csv",
        "requestBody": {"required": true, "content": {"multipart/form-data": {"schema": {
          "type": "object",
          "properties": {
            "provider": {"type": "string", "description": "The data provider."},
            "file": {"type": "string", "format": "binary", "description": "The CSV file. The maximum file size is 30 MB."}
          }
        }}}},
        "responses": {"201": {"description": "OK", "content": {"application/json": {"schema": {
          "type": "object", "properties": {"batch": {"type": "string"}, "ingestedRows": {"type": "integer"}}
        }}}}}
      }
    },
    "/datahub/v1/events": {
      "post": {
        "operationId": "datahubEvents",
        "x-cli-name": "ingest-datahub-events",
        "requestBody": {"required": true, "content": {"application/json": {"schema": {
          "type": "object",
          "properties": {"events": {"type": "array", "items": {"type": "object", "properties": {"provider": {"type": "string"}, "time": {"type": "string"}}}}}
        }}}},
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

// extractJSON returns the JSON document in a built binary's combined output,
// skipping restish's TLS warnings and the agent-mode banner around it.
func extractJSON(t *testing.T, output string, target any) {
	t.Helper()
	start := strings.Index(output, "{")
	end := strings.LastIndex(output, "}")
	if start < 0 || end < start {
		t.Fatalf("no JSON document in output:\n%s", output)
	}
	if err := json.Unmarshal([]byte(output[start:end+1]), target); err != nil {
		t.Fatalf("output is not the expected JSON: %v\n%s", err, output)
	}
}

type recordedUpload struct {
	contentType string
	provider    string
	filename    string
	fileType    string
	fileBody    string
	tenant      string
	jsonBody    string
	path        string
}

func TestIngestDatahubEventsCSVUploadsMultipart(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()

	var recorded recordedUpload
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/openapi.json":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, csvUploadSpec)
		case "/datahub/v1/csv/upload":
			recorded.path = request.URL.Path
			recorded.contentType = request.Header.Get("Content-Type")
			recorded.tenant = request.Header.Get(strings.TrimSuffix(tenantIDHeaderPrefix, ":"))
			if err := request.ParseMultipartForm(1 << 20); err != nil {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":"`+err.Error()+`"}`)
				return
			}
			recorded.provider = request.FormValue("provider")
			file, header, err := request.FormFile("file")
			if err != nil {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":"missing file"}`)
				return
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			recorded.filename = header.Filename
			recorded.fileType = header.Header.Get("Content-Type")
			recorded.fileBody = string(data)
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"batch":"`+header.Filename+`_1","ingestedRows":2}`)
		case "/datahub/v1/events":
			recorded.path = request.URL.Path
			recorded.contentType = request.Header.Get("Content-Type")
			body, _ := io.ReadAll(request.Body)
			recorded.jsonBody = string(body)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"ok":true}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	environment := append(seedHermeticAPIConfig(t, home, server.URL), "DCI_API_KEY=test-key")

	csvPath := writeUploadFixture(t, "events.csv", "usage_date,metric.cost\n2026-09-01T00:00:00Z,1\n2026-09-02T00:00:00Z,2\n")

	t.Run("uploads the file as a multipart part", func(t *testing.T) {
		recorded = recordedUpload{}
		res := runCLIWithEnv(t, bin, home, environment,
			"ingest-datahub-events-csv", "provider:", "litellm-usage,", "file:", "@"+csvPath, "--output", "json", "-D", "acme.com")
		if res.timedOut {
			t.Fatalf("timed out; output:\n%s", res.output)
		}
		if res.exitCode != 0 {
			t.Fatalf("exit %d; output:\n%s", res.exitCode, res.output)
		}
		if recorded.path != "/datahub/v1/csv/upload" {
			t.Fatalf("server saw path %q; output:\n%s", recorded.path, res.output)
		}
		if !strings.HasPrefix(recorded.contentType, "multipart/form-data; boundary=") {
			t.Errorf("content type = %q", recorded.contentType)
		}
		if recorded.provider != "litellm-usage" {
			t.Errorf("provider = %q", recorded.provider)
		}
		if recorded.filename != "events.csv" || recorded.fileType != "text/csv" {
			t.Errorf("file part = %q (%s)", recorded.filename, recorded.fileType)
		}
		if !strings.Contains(recorded.fileBody, "2026-09-02T00:00:00Z,2") {
			t.Errorf("file body = %q", recorded.fileBody)
		}
		if recorded.tenant != "acme.com" {
			t.Errorf("tenant header = %q, want acme.com", recorded.tenant)
		}
		var result struct {
			Batch        string `json:"batch"`
			IngestedRows int    `json:"ingestedRows"`
		}
		extractJSON(t, res.output, &result)
		if result.Batch != "events.csv_1" || result.IngestedRows != 2 {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("missing @ fails before any request", func(t *testing.T) {
		recorded = recordedUpload{}
		res := runCLIWithEnv(t, bin, home, environment,
			"ingest-datahub-events-csv", "provider:", "litellm-usage,", "file:", "events.csv")
		if res.exitCode != exitUsage {
			t.Fatalf("exit %d, want %d; output:\n%s", res.exitCode, exitUsage, res.output)
		}
		if !strings.Contains(res.output, "file must name a file to upload") || !strings.Contains(res.output, "file: @events.csv") {
			t.Fatalf("expected the @ hint; output:\n%s", res.output)
		}
		if recorded.path != "" {
			t.Fatalf("a request reached the server: %s", recorded.path)
		}
	})

	t.Run("missing file fails before any request", func(t *testing.T) {
		recorded = recordedUpload{}
		res := runCLIWithEnv(t, bin, home, environment,
			"ingest-datahub-events-csv", "provider:", "x,", "file:", "@"+filepath.Join(home, "absent.csv"))
		if res.exitCode != exitUsage {
			t.Fatalf("exit %d, want %d; output:\n%s", res.exitCode, exitUsage, res.output)
		}
		if !strings.Contains(res.output, "cannot read") {
			t.Fatalf("expected the missing-file error; output:\n%s", res.output)
		}
		if recorded.path != "" {
			t.Fatalf("a request reached the server: %s", recorded.path)
		}
	})

	t.Run("agent mode returns the structured usage error", func(t *testing.T) {
		recorded = recordedUpload{}
		res := runCLIWithEnv(t, bin, home, environment,
			"ingest-datahub-events-csv", "--agent", "provider:", "x,", "file:", "events.csv")
		if res.exitCode != exitUsage {
			t.Fatalf("exit %d, want %d; output:\n%s", res.exitCode, exitUsage, res.output)
		}
		var envelope structuredErrorEnvelope
		extractJSON(t, res.output, &envelope)
		if envelope.Error.Code != "USAGE_ERROR" || !strings.Contains(envelope.Error.Hint, "file: @events.csv") {
			t.Fatalf("envelope = %+v", envelope.Error)
		}
	})

	t.Run("json operations still go through restish", func(t *testing.T) {
		recorded = recordedUpload{}
		res := runCLIWithEnv(t, bin, home, environment,
			"ingest-datahub-events", "events[0]{provider: x, time: 2026-09-01T00:00:00Z}")
		if res.timedOut {
			t.Fatalf("timed out; output:\n%s", res.output)
		}
		if res.exitCode != 0 {
			t.Fatalf("exit %d; output:\n%s", res.exitCode, res.output)
		}
		if recorded.path != "/datahub/v1/events" {
			t.Fatalf("server saw path %q", recorded.path)
		}
		if strings.HasPrefix(recorded.contentType, "multipart/") {
			t.Fatalf("json operation was sent as multipart: %s", recorded.contentType)
		}
		if !strings.Contains(recorded.jsonBody, `"provider":"x"`) {
			t.Fatalf("json body = %q", recorded.jsonBody)
		}
	})
}
