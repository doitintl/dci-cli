package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

const queryRequestHelp = "Runs a report query.\n" +
	"## Request Schema (application/json)\n\n" +
	"```schema\n" +
	"{\n" +
	"  config: {\n" +
	"    aggregation: (string)\n" +
	"    metrics: [\n" +
	"      {\n" +
	"        type*: (string)\n" +
	"      }\n" +
	"    ]\n" +
	"  }\n" +
	"}\n" +
	"```\n"

func TestRequestSchemaTopLevelFields(t *testing.T) {
	fields := requestSchemaTopLevelFields(queryRequestHelp)
	if !fields["config"] {
		t.Errorf("fields = %v, want config", fields)
	}
	if fields["aggregation"] || fields["type"] {
		t.Errorf("nested fields leaked into top level: %v", fields)
	}
}

func TestRequestSchemaTopLevelFieldsSkipsArrayBodies(t *testing.T) {
	help := "## Request Schema (application/json)\n```schema\n[\n  {\n    id: (string)\n  }\n]\n```\n"
	if fields := requestSchemaTopLevelFields(help); fields != nil {
		t.Errorf("array body produced fields %v", fields)
	}
}

func TestValidateRequestBodyRejectsUnknownShorthandField(t *testing.T) {
	command := &cobra.Command{Long: queryRequestHelp}
	errorValue := validateRequestBody(command, []string{`body.query: "SELECT * FROM billing"`})
	if errorValue == nil {
		t.Fatal("unknown top-level field accepted")
	}
	if !strings.Contains(errorValue.Error(), "body") || !strings.Contains(errorValue.Error(), "config") {
		t.Fatalf("error = %q", errorValue)
	}
	if errorValue.(requestBodyValidationError).ExitCode() != exitUsage {
		t.Fatalf("exit code = %d", errorValue.(requestBodyValidationError).ExitCode())
	}
}

func TestValidateRequestBodyAcceptsKnownFields(t *testing.T) {
	command := &cobra.Command{Long: queryRequestHelp}
	for _, args := range [][]string{{`config.timeInterval: day`}, {`{"config": {}}`}} {
		if err := validateRequestBody(command, args); err != nil {
			t.Fatalf("valid body rejected: %v", err)
		}
	}
}

func TestValidateRequestBodySkipsPathParameters(t *testing.T) {
	command := &cobra.Command{Use: "update-resource resource-id", Long: queryRequestHelp}
	for _, pathParameter := range []string{"project.dataset", "urn:resource"} {
		if err := validateRequestBody(command, []string{pathParameter, `config.timeInterval: day`}); err != nil {
			t.Fatalf("path parameter %q rejected as a body field: %v", pathParameter, err)
		}
	}
}

func TestValidateRequestBodyRejectsUnknownPipedJSONAndPreservesInput(t *testing.T) {
	inputFile, err := os.CreateTemp(t.TempDir(), "request-body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inputFile.WriteString(`{"query":"SELECT * FROM billing"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := inputFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	previousInput := cli.Stdin
	cli.Stdin = inputFile
	t.Cleanup(func() {
		cli.Stdin = previousInput
		inputFile.Close()
	})

	command := &cobra.Command{Long: queryRequestHelp}
	if err := validateRequestBody(command, nil); err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("error = %v", err)
	}
	bufferedInput, err := io.ReadAll(cli.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(bufferedInput) != `{"query":"SELECT * FROM billing"}` {
		t.Fatalf("buffered input = %q", bufferedInput)
	}
}

const importCloudflowFlowHelp = "Import a flow bundle.\n" +
	"## Request Schema (application/json)\n\n" +
	"```schema\n" +
	"{\n" +
	"  bindings: {\n" +
	"    connections: {\n" +
	"    }\n" +
	"  }\n" +
	"  bundle*: {\n" +
	"    kind*: (string)\n" +
	"  }\n" +
	"  options: {\n" +
	"    namePrefix: (string)\n" +
	"  }\n" +
	"}\n" +
	"```\n"

// pipeRequestBody points cli.Stdin at a temp file holding body, the way a pipe
// or `< file.json` redirect reaches the CLI, and restores it afterwards.
func pipeRequestBody(t *testing.T, body string) {
	t.Helper()
	inputFile, err := os.CreateTemp(t.TempDir(), "request-body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inputFile.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if _, err := inputFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	previousInput := cli.Stdin
	previousBody := bufferedRequestBody
	cli.Stdin = inputFile
	t.Cleanup(func() {
		cli.Stdin = previousInput
		bufferedRequestBody = previousBody
		inputFile.Close()
	})
}

const bareCloudflowBundle = `{"kind":"cloudflow.doit.com/FlowBundle","schemaVersion":1,` +
	`"rootFlow":"flow-1","flows":[{"key":"flow-1","name":"Nightly report","firstNode":"n1",` +
	`"nodes":[{"key":"n1","type":"action"}]}]}`

// A bare bundle is what `dci export-cloudflow-flow` writes; import wants it
// nested under `bundle`. The CLI wraps it so the two commands compose.
func TestValidateRequestBodyWrapsBareCloudflowBundle(t *testing.T) {
	pipeRequestBody(t, bareCloudflowBundle)

	command := &cobra.Command{Use: "import-cloudflow-flow", Long: importCloudflowFlowHelp}
	if err := validateRequestBody(command, nil); err != nil {
		t.Fatalf("bare bundle rejected: %v", err)
	}
	forwardedBody, err := io.ReadAll(cli.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Bundle struct {
			Kind          string `json:"kind"`
			SchemaVersion int    `json:"schemaVersion"`
			RootFlow      string `json:"rootFlow"`
			Flows         []struct {
				Name string `json:"name"`
			} `json:"flows"`
		} `json:"bundle"`
	}
	if err := json.Unmarshal(forwardedBody, &request); err != nil {
		t.Fatalf("wrapped body is not valid JSON (%v): %s", err, forwardedBody)
	}
	if request.Bundle.Kind != cloudflowBundleKind || request.Bundle.SchemaVersion != 1 {
		t.Fatalf("bundle discriminator lost: %s", forwardedBody)
	}
	if request.Bundle.RootFlow != "flow-1" || len(request.Bundle.Flows) != 1 ||
		request.Bundle.Flows[0].Name != "Nightly report" {
		t.Fatalf("bundle contents lost: %s", forwardedBody)
	}
}

// A request that already carries `bundle` — the shape needed to pass bindings
// — must reach the API byte-for-byte.
func TestValidateRequestBodyLeavesWrappedImportRequestAlone(t *testing.T) {
	request := `{"bundle":` + bareCloudflowBundle + `,"bindings":{"connections":{"aws":"conn-1"}}}`
	pipeRequestBody(t, request)

	command := &cobra.Command{Use: "import-cloudflow-flow", Long: importCloudflowFlowHelp}
	if err := validateRequestBody(command, nil); err != nil {
		t.Fatalf("wrapped request rejected: %v", err)
	}
	forwardedBody, err := io.ReadAll(cli.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(forwardedBody) != request {
		t.Fatalf("wrapped request was rewritten:\n got %s\nwant %s", forwardedBody, request)
	}
}

// The wrap is scoped to the import command: a bundle piped anywhere else stays
// an unknown-field error rather than being silently reshaped.
func TestValidateRequestBodyDoesNotWrapBundleForOtherCommands(t *testing.T) {
	pipeRequestBody(t, bareCloudflowBundle)

	command := &cobra.Command{Use: "query-reports", Long: queryRequestHelp}
	err := validateRequestBody(command, nil)
	if err == nil {
		t.Fatal("bare bundle accepted by an unrelated command")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRequestBodyCanBeBypassed(t *testing.T) {
	t.Setenv("DCI_SKIP_BODY_VALIDATION", "1")
	command := &cobra.Command{Long: queryRequestHelp}
	if err := validateRequestBody(command, []string{`query: "SELECT * FROM billing"`}); err != nil {
		t.Fatalf("bypass rejected body: %v", err)
	}
}

func TestExtractRequestCurrency(t *testing.T) {
	validFields := map[string]bool{"config": true}
	if currency := extractRequestCurrency(validFields, nil, nil); currency != "" {
		t.Errorf("unspecified currency = %q, want empty", currency)
	}
	if currency := extractRequestCurrency(validFields, []string{`config.currency: EUR`}, nil); currency != "EUR" {
		t.Errorf("shorthand currency = %q, want EUR", currency)
	}
	if currency := extractRequestCurrency(validFields, []string{`{"config":{"currency":"gbp"}}`}, nil); currency != "GBP" {
		t.Errorf("inline JSON currency = %q, want GBP", currency)
	}
	bodyFile := t.TempDir() + "/query.json"
	if err := os.WriteFile(bodyFile, []byte(`{"config":{"currency":"cad"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if currency := extractRequestCurrency(validFields, []string{"@" + bodyFile}, nil); currency != "CAD" {
		t.Errorf("file currency = %q, want CAD", currency)
	}
	stdinBody := []byte(`{"config":{"currency":"ils"}}`)
	if currency := extractRequestCurrency(validFields, nil, stdinBody); currency != "ILS" {
		t.Errorf("stdin currency = %q, want ILS", currency)
	}
	if currency := extractRequestCurrency(map[string]bool{"body": true}, nil, nil); currency != "" {
		t.Errorf("non-report currency = %q, want empty", currency)
	}
}
