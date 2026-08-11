package main

import (
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
