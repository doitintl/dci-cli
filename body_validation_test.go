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

func TestValidateRequestBodyReadsShellSplitValuesAsValues(t *testing.T) {
	command := &cobra.Command{Long: queryRequestHelp}
	// The shell splits `config.currency: usd.legacy, config.timeInterval: day`
	// into four arguments; "usd.legacy," is a value, not a field named "usd".
	if err := validateRequestBody(command, []string{"config.currency:", "usd.legacy,", "config.timeInterval:", "day"}); err != nil {
		t.Fatalf("value with a dot rejected as a field: %v", err)
	}
	err := validateRequestBody(command, []string{"config.timeInterval:", "day,", "body.query:", "x.y"})
	if err == nil || !strings.Contains(err.Error(), "body") {
		t.Fatalf("unknown field after a comma not reported: %v", err)
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
	// The bypass covers the value-shape check too.
	tagsCommand := &cobra.Command{Use: "add-ticket-tags ticketid", Long: tagsRequestHelp}
	if err := validateRequestBody(tagsCommand, []string{"318240", "tags:", "prod,", "billing"}); err != nil {
		t.Fatalf("bypass rejected mis-shaped body: %v", err)
	}
}

// tagsRequestHelp is shaped exactly as restish renders a request schema into
// a command's Long: arrays multi-line with the element type on its own line,
// scalars parenthesized with trailing doc text.
const tagsRequestHelp = "Add tags.\n" +
	"## Request Schema (application/json)\n\n" +
	"```schema\n" +
	"{\n" +
	"  tags*: [\n" +
	"    (string)\n" +
	"  ]\n" +
	"  note: (string) A note\n" +
	"  limits: [\n" +
	"    (integer min:0)\n" +
	"  ]\n" +
	"}\n" +
	"```\n"

func TestRequestSchemaFieldSketchesCarryArrayElems(t *testing.T) {
	sketches := requestSchemaTopLevelFieldSketches(tagsRequestHelp)
	want := []bodyFieldSketch{
		{name: "tags", required: true, sketch: "[", elem: "string"},
		{name: "note", sketch: "(string) A note"},
		{name: "limits", sketch: "[", elem: "integer"},
	}
	if len(sketches) != len(want) {
		t.Fatalf("sketches = %v, want %v", sketches, want)
	}
	for index, field := range want {
		if sketches[index] != field {
			t.Fatalf("sketch[%d] = %+v, want %+v", index, sketches[index], field)
		}
	}
	// A nested object's fields never leak an elem to the top level.
	for _, field := range requestSchemaTopLevelFieldSketches(queryRequestHelp) {
		if field.elem != "" {
			t.Fatalf("nested schema leaked elem %q into %q", field.elem, field.name)
		}
	}
}

func TestSchemaTypeWord(t *testing.T) {
	cases := map[string]string{
		"(string)":                "string",
		"(string minLen:1) A doc": "string",
		"(integer|null)":          "integer",
		"(null|boolean)":          "boolean",
		"string":                  "string",
		"":                        "",
	}
	for sketch, want := range cases {
		if got := schemaTypeWord(sketch); got != want {
			t.Fatalf("typeWord(%q) = %q, want %q", sketch, got, want)
		}
	}
}

// The Alfredo replays (Slack, 2026-08-29): comma-separated array items
// without brackets are a shorthand parse error at request time, and bare
// strings send a scalar the API rejects — both now die before dispatch,
// with the corrected line.
func TestValidateRequestBodyCatchesUnbracketedArrayItems(t *testing.T) {
	command := &cobra.Command{Use: "add-ticket-tags ticketid", Long: tagsRequestHelp}
	errorValue := validateRequestBody(command, []string{"318240", "tags:", "prod,", "billing"})
	if errorValue == nil {
		t.Fatal("unbracketed array items accepted")
	}
	if !strings.Contains(errorValue.Error(), "did you mean: tags: [prod, billing]") {
		t.Fatalf("error = %q", errorValue)
	}
	shapeError, isShapeError := errorValue.(requestBodyShapeError)
	if !isShapeError {
		t.Fatalf("error type = %T", errorValue)
	}
	if shapeError.ExitCode() != exitUsage || shapeError.AgentErrorCode() != "USAGE_ERROR" || shapeError.AgentErrorRetryable() {
		t.Fatalf("error contract = %d %s %v", shapeError.ExitCode(), shapeError.AgentErrorCode(), shapeError.AgentErrorRetryable())
	}
}

func TestValidateRequestBodyCatchesScalarWhereArrayExpected(t *testing.T) {
	command := &cobra.Command{Use: "add-ticket-tags ticketid", Long: tagsRequestHelp}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"318240", "tags:", "prod", "billing"}, "tags expects an array of strings — did you mean: tags: [prod, billing]"},
		{[]string{"318240", "tags:", "prod"}, "tags expects an array of strings — did you mean: tags: [prod]"},
		{[]string{"318240", "limits:", "5"}, "limits expects an array of integers — did you mean: limits: [5]"},
		// A bare prefix submitted with no value: no items to suggest, so the
		// generic bracketed spelling shows instead.
		{[]string{"318240", "tags:"}, "tags expects an array of strings — array values are bracketed: tags: [a, b]"},
	}
	for _, testCase := range cases {
		errorValue := validateRequestBody(command, testCase.args)
		if errorValue == nil {
			t.Fatalf("scalar accepted for %v", testCase.args)
		}
		if errorValue.Error() != testCase.want {
			t.Fatalf("error for %v = %q, want %q", testCase.args, errorValue.Error(), testCase.want)
		}
	}
}

func TestValidateRequestBodyAcceptsWellShapedArrays(t *testing.T) {
	command := &cobra.Command{Use: "add-ticket-tags ticketid", Long: tagsRequestHelp}
	for _, args := range [][]string{
		{"318240", "tags:", "[prod,", "billing]"},
		{"318240", "tags[]:", "prod"},
		{"318240", "tags:", "[prod],", "note:", "hi"},
		{"318240", `{"tags":["prod"]}`},
		// Whole-body tokens pass through untouched — even ones whose file
		// does not exist (restish owns that failure at request time).
		{"318240", "@nosuchfile.json"},
	} {
		if err := validateRequestBody(command, args); err != nil {
			t.Fatalf("well-shaped body %v rejected: %v", args, err)
		}
	}
}

func TestValidateRequestBodyRejectsNonObjectShorthand(t *testing.T) {
	command := &cobra.Command{Use: "add-ticket-tags ticketid", Long: tagsRequestHelp}
	errorValue := validateRequestBody(command, []string{"318240", "prod"})
	if errorValue == nil {
		t.Fatal("non-object body accepted")
	}
	if !strings.Contains(errorValue.Error(), "the request body is an object") ||
		!strings.Contains(errorValue.Error(), "tags: [a, b]") {
		t.Fatalf("error = %q", errorValue)
	}
}

func TestRepairUnbracketedArrays(t *testing.T) {
	arrayFields := map[string]bool{"tags": true}
	cases := []struct{ body, want string }{
		{"tags: prod, billing", "tags: [prod, billing]"},
		{"tags: a, b, note: x", "tags: [a, b], note: x"},
		// Not the one mistake this repairs: loose items after a non-array
		// field, an already-bracketed value, a leading nameless segment, or
		// nothing dangling at all.
		{"note: a, b", ""},
		{"tags: [a], b", ""},
		{"prod, billing", ""},
		{"tags: [prod, billing]", ""},
	}
	for _, testCase := range cases {
		if got := repairUnbracketedArrays(testCase.body, arrayFields); got != testCase.want {
			t.Fatalf("repair(%q) = %q, want %q", testCase.body, got, testCase.want)
		}
	}
	if got := repairUnbracketedArrays("tags: a, b", nil); got != "" {
		t.Fatalf("repair with no array fields = %q", got)
	}
}

func TestSuggestedArrayItems(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{"prod billing", "prod, billing"},
		{"prod,billing", "prod, billing"},
		{"has space!", `has, "space!"`},
		{int64(5), "5"},
		{true, "true"},
		{"", ""},
		{nil, ""},
		{[]any{"a"}, ""},
		{map[string]any{}, ""},
	}
	for _, testCase := range cases {
		if got := suggestedArrayItems(testCase.value); got != testCase.want {
			t.Fatalf("items(%#v) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

func TestExtractRequestCurrency(t *testing.T) {
	validFields := map[string]bool{"config": true}
	// Unspecified stays empty here: the USD API default is applied in the
	// response transform, where money-typed columns are known — a usage-only
	// result must not inherit a currency (AI-POLISH-SPEC F4).
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
