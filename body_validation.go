package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

type requestBodyValidationError struct {
	unknownFields []string
	validFields   []string
}

func (validationError requestBodyValidationError) Error() string {
	return fmt.Sprintf(
		"unknown request body field(s): %s; valid top-level fields: %s",
		strings.Join(validationError.unknownFields, ", "),
		strings.Join(validationError.validFields, ", "),
	)
}

func (validationError requestBodyValidationError) ExitCode() int {
	return exitUsage
}

func (validationError requestBodyValidationError) AgentErrorCode() string {
	return "USAGE_ERROR"
}

func (validationError requestBodyValidationError) AgentErrorHint() string {
	return "Run the command with --help to inspect the request schema; use --rsh-no-cache if the API schema changed recently, or set DCI_SKIP_BODY_VALIDATION=1 to bypass validation"
}

func (validationError requestBodyValidationError) AgentErrorRetryable() bool {
	return false
}

var shorthandBodyFieldPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)\s*[.\[:{]`)
var schemaBodyFieldPattern = regexp.MustCompile(`^  ([A-Za-z_][A-Za-z0-9_-]*)\*?:`)
var currencyBodyFieldPattern = regexp.MustCompile(`(?:^|[,\s])config\.currency:\s*"?([A-Za-z]{3})"?`)
var bufferedRequestBody []byte

func validateRequestBody(command *cobra.Command, args []string) error {
	validFields := requestSchemaTopLevelFields(command.Long)
	if len(validFields) == 0 {
		return nil
	}

	bodyArguments := args
	pathParameterCount := len(strings.Fields(command.Use)) - 1
	if pathParameterCount > 0 && pathParameterCount <= len(args) {
		bodyArguments = args[pathParameterCount:]
	}
	stdinFields, stdinBuffered := bufferStdinTopLevelFields()
	if stdinBuffered && len(bodyArguments) == 0 {
		if wrappedFields, wrapped := wrapBareCloudflowBundle(command.Name()); wrapped {
			stdinFields = wrappedFields
		}
	}
	requestReportCurrency = extractRequestCurrency(validFields, bodyArguments, bufferedRequestBody)
	if skip, _ := parseBoolish(os.Getenv("DCI_SKIP_BODY_VALIDATION")); skip {
		return nil
	}
	unknownFields := make([]string, 0)
	for _, argument := range bodyArguments {
		if strings.HasPrefix(argument, "@") || strings.HasPrefix(argument, "<") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(argument), "{") {
			for _, field := range jsonTopLevelFields([]byte(argument)) {
				if !validFields[field] {
					unknownFields = append(unknownFields, field)
				}
			}
			continue
		}
		match := shorthandBodyFieldPattern.FindStringSubmatch(argument)
		if match != nil && !validFields[match[1]] {
			unknownFields = append(unknownFields, match[1])
		}
	}

	if stdinBuffered {
		for _, field := range stdinFields {
			if !validFields[field] {
				unknownFields = append(unknownFields, field)
			}
		}
	}

	if len(unknownFields) == 0 {
		return nil
	}
	unknownFields = uniqueSortedStrings(unknownFields)
	return requestBodyValidationError{
		unknownFields: unknownFields,
		validFields:   sortedFieldNames(validFields),
	}
}

func requestSchemaTopLevelFields(longHelp string) map[string]bool {
	_, afterHeading, found := strings.Cut(longHelp, "## Request Schema")
	if !found {
		return nil
	}
	_, schemaBlock, found := strings.Cut(afterHeading, "```schema")
	if !found {
		return nil
	}
	schemaBlock, _, _ = strings.Cut(schemaBlock, "```")
	fields := map[string]bool{}
	foundObject := false
	for _, line := range strings.Split(schemaBlock, "\n") {
		trimmedLine := strings.TrimSpace(line)
		if !foundObject {
			if trimmedLine == "{" {
				foundObject = true
			} else if strings.HasPrefix(trimmedLine, "[") {
				return nil
			}
			continue
		}
		if match := schemaBodyFieldPattern.FindStringSubmatch(line); match != nil {
			fields[match[1]] = true
		}
	}
	if !foundObject {
		return nil
	}
	return fields
}

func jsonTopLevelFields(data []byte) []string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil
	}
	fields := make([]string, 0, len(object))
	for field := range object {
		fields = append(fields, field)
	}
	return fields
}

func bufferStdinTopLevelFields() ([]string, bool) {
	inputInfo, err := cli.Stdin.Stat()
	if err != nil || inputInfo.Mode()&os.ModeCharDevice != 0 {
		return nil, false
	}
	data, err := io.ReadAll(cli.Stdin)
	if err != nil {
		return nil, false
	}
	cli.Stdin = &bufferedBodyInput{Reader: bytes.NewReader(data), info: inputInfo}
	bufferedRequestBody = data
	trimmedData := bytes.TrimSpace(data)
	if len(trimmedData) == 0 || trimmedData[0] != '{' {
		return nil, true
	}
	return jsonTopLevelFields(trimmedData), true
}

// cloudflowBundleKind is the discriminator every exported CloudFlow bundle
// carries — a single-value enum in the API schema, so matching it is exact.
const cloudflowBundleKind = "cloudflow.doit.com/FlowBundle"

// wrapBareCloudflowBundle nests a bare bundle piped into import-cloudflow-flow
// under the `bundle` field the operation's request schema expects, so what
// export-cloudflow-flow writes can be imported without hand-editing:
//
//	dci export-cloudflow-flow FLOW > bundle.json
//	dci import-cloudflow-flow --idempotency-key "$(uuidgen)" < bundle.json
//
// None of a bare bundle's top-level fields are in the import request schema,
// so validateRequestBody would reject every one of them: this only rescues
// input that would otherwise fail. A request that already carries `bundle`
// (with `bindings` and `options` alongside it) passes through untouched —
// callers who need bindings write the full request shape themselves.
//
// Returns the rewritten body's top-level fields when it wrapped.
func wrapBareCloudflowBundle(commandName string) ([]string, bool) {
	if commandName != "import-cloudflow-flow" {
		return nil, false
	}
	body := bytes.TrimSpace(bufferedRequestBody)
	var probe struct {
		Kind   string          `json:"kind"`
		Bundle json.RawMessage `json:"bundle"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, false
	}
	if probe.Kind != cloudflowBundleKind || len(probe.Bundle) > 0 {
		return nil, false
	}
	wrapped, err := json.Marshal(map[string]json.RawMessage{"bundle": body})
	if err != nil {
		return nil, false
	}
	info, err := cli.Stdin.Stat()
	if err != nil {
		return nil, false
	}
	bufferedRequestBody = wrapped
	cli.Stdin = &bufferedBodyInput{Reader: bytes.NewReader(wrapped), info: info}
	return []string{"bundle"}, true
}

func extractRequestCurrency(validFields map[string]bool, bodyArguments []string, stdinBody []byte) string {
	if !validFields["config"] {
		return ""
	}
	for _, argument := range bodyArguments {
		if match := currencyBodyFieldPattern.FindStringSubmatch(argument); match != nil {
			return strings.ToUpper(match[1])
		}
		trimmedArgument := strings.TrimSpace(argument)
		if currency := currencyFromJSONBody([]byte(trimmedArgument)); currency != "" {
			return currency
		}
		if len(trimmedArgument) > 1 && (trimmedArgument[0] == '@' || trimmedArgument[0] == '<') {
			if data, err := os.ReadFile(trimmedArgument[1:]); err == nil {
				if currency := currencyFromJSONBody(data); currency != "" {
					return currency
				}
			}
		}
	}
	if currency := currencyFromJSONBody(stdinBody); currency != "" {
		return currency
	}
	// No currency declared anywhere in the request: the API applies its
	// documented default, USD. Returning it keeps the currency context always
	// resolved for report-shaped responses, so tables money-format and agents
	// never read unlabeled cost columns (AI-POLISH-SPEC F4).
	return "USD"
}

func currencyFromJSONBody(data []byte) string {
	var body struct {
		Config struct {
			Currency string `json:"currency"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &body); err == nil && body.Config.Currency != "" {
		return strings.ToUpper(body.Config.Currency)
	}
	return ""
}

type bufferedBodyInput struct {
	*bytes.Reader
	info fs.FileInfo
}

func (input *bufferedBodyInput) Stat() (fs.FileInfo, error) {
	return input.info, nil
}

func uniqueSortedStrings(values []string) []string {
	uniqueValues := make(map[string]bool, len(values))
	for _, value := range values {
		uniqueValues[value] = true
	}
	return sortedFieldNames(uniqueValues)
}

func sortedFieldNames(fields map[string]bool) []string {
	names := make([]string, 0, len(fields))
	for field := range fields {
		names = append(names, field)
	}
	sort.Strings(names)
	return names
}
