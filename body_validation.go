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
	"unicode"

	"github.com/danielgtaylor/shorthand/v2"
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
var schemaBodyFieldSketchPattern = regexp.MustCompile(`^  ([A-Za-z_][A-Za-z0-9_-]*)(\*?): ?(.*)$`)
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

	if len(unknownFields) > 0 {
		unknownFields = uniqueSortedStrings(unknownFields)
		return requestBodyValidationError{
			unknownFields: unknownFields,
			validFields:   sortedFieldNames(validFields),
		}
	}
	if stdinBuffered {
		// Restish merges piped input with argument edits; shape-checking the
		// arguments alone would guess at that merge.
		return nil
	}
	return validateBodyValueShapes(requestSchemaTopLevelFieldSketches(command.Long), bodyArguments)
}

// requestBodyShapeError reports body shorthand that cannot produce the shape
// the request schema declares: text the shorthand parser rejects outright, or
// a scalar where the schema wants an array. Born from dogfood feedback
// (2026-08-28/29): array fields invite `tags: prod, billing` (a parse error —
// shorthand commas separate properties) and `tags: prod billing` (a string
// the API rejects), and both deserve the corrected line, not a downstream
// parser message or an API 400.
type requestBodyShapeError struct {
	problem    string
	suggestion string // the corrected shorthand ("tags: [prod, billing]"); "" when none derivable
	syntax     string // generic syntax reminder, shown only without a suggestion
}

func (shapeError requestBodyShapeError) Error() string {
	if shapeError.suggestion != "" {
		return shapeError.problem + " — did you mean: " + shapeError.suggestion
	}
	if shapeError.syntax != "" {
		return shapeError.problem + " — array values are bracketed: " + shapeError.syntax
	}
	return shapeError.problem
}

func (shapeError requestBodyShapeError) ExitCode() int {
	return exitUsage
}

func (shapeError requestBodyShapeError) AgentErrorCode() string {
	return "USAGE_ERROR"
}

func (shapeError requestBodyShapeError) AgentErrorHint() string {
	return "Write array values in brackets (field: [a, b]); run the command with --help to inspect the request schema, or set DCI_SKIP_BODY_VALIDATION=1 to bypass validation"
}

func (shapeError requestBodyShapeError) AgentErrorRetryable() bool {
	return false
}

// bodyShorthandParseOptions mirrors restish's GetBody parse exactly
// (cli/input.go), so a body this validation passes is a body restish parses
// the same way — and a body it rejects would have failed there anyway,
// just with a less helpful message.
var bodyShorthandParseOptions = shorthand.ParseOptions{
	EnableFileInput:       true,
	EnableObjectDetection: true,
}

// validateBodyValueShapes parses pure-shorthand body arguments with the same
// parser restish hands them to at request time, and checks the parsed values
// against the schema's array fields. Runs only when every argument is
// shorthand: a whole-body token (@file, <file) is passed through untouched.
func validateBodyValueShapes(sketches []bodyFieldSketch, bodyArguments []string) error {
	if len(bodyArguments) == 0 || len(sketches) == 0 {
		return nil
	}
	for _, argument := range bodyArguments {
		if strings.HasPrefix(argument, "@") || strings.HasPrefix(argument, "<") {
			return nil
		}
	}
	arrayFields := map[string]bool{}
	var firstArray *bodyFieldSketch
	for index := range sketches {
		if sketchIsArray(sketches[index].sketch) {
			arrayFields[sketches[index].name] = true
			if firstArray == nil {
				firstArray = &sketches[index]
			}
		}
	}
	body := strings.Join(bodyArguments, " ")
	parsed, parseErr := shorthand.Unmarshal(body, bodyShorthandParseOptions, nil)
	if parseErr != nil {
		shapeError := requestBodyShapeError{problem: "request body shorthand did not parse: " + parseErr.Error()}
		if repaired := repairUnbracketedArrays(body, arrayFields); repaired != "" {
			shapeError.suggestion = repaired
		} else if firstArray != nil {
			shapeError.syntax = arrayValueExample(*firstArray)
		}
		return shapeError
	}
	object, isObject := parsed.(map[string]any)
	if !isObject {
		if parsed == nil {
			return nil
		}
		example := sketches[0].name + ": value"
		if firstArray != nil {
			example = arrayValueExample(*firstArray)
		}
		return requestBodyShapeError{
			problem: fmt.Sprintf("the request body is an object — write its fields as name: value pairs (e.g. %s)", example),
		}
	}
	for _, field := range sketches {
		if !sketchIsArray(field.sketch) {
			continue
		}
		value, present := object[field.name]
		if !present {
			continue
		}
		if _, isArray := value.([]any); isArray {
			continue
		}
		shapeError := requestBodyShapeError{
			problem: fmt.Sprintf("%s expects %s", field.name, arrayPhrase(field.elem)),
			syntax:  arrayValueExample(field),
		}
		if items := suggestedArrayItems(value); items != "" {
			suggestion := field.name + ": [" + items + "]"
			if shorthandYieldsArray(field.name, suggestion) {
				shapeError.suggestion = suggestion
			}
		}
		return shapeError
	}
	return nil
}

// arrayPhrase spells what an array field expects, element type included when
// the schema names one.
func arrayPhrase(elem string) string {
	switch elem {
	case "":
		return "an array"
	case "{":
		return "an array of objects"
	}
	return "an array of " + elem + "s"
}

// arrayValueExample spells a complete example assignment for an array field.
func arrayValueExample(field bodyFieldSketch) string {
	items := arrayItemsExample(field.elem)
	if items == "" {
		items = "a, b"
		if field.elem == "{" {
			items = "{…}"
		}
	}
	return field.name + ": [" + items + "]"
}

// suggestedArrayItems respells one parsed scalar as array items: a string is
// split on the commas and spaces the user separated it with, other scalars
// pass through. "" when no sensible items exist (nil, empty, composites).
func suggestedArrayItems(value any) string {
	switch typed := value.(type) {
	case nil, map[string]any, map[any]any, []any:
		return ""
	case string:
		parts := strings.FieldsFunc(typed, func(r rune) bool {
			return r == ',' || unicode.IsSpace(r)
		})
		for index, part := range parts {
			if !shorthandSafeItemPattern.MatchString(part) {
				parts[index] = fmt.Sprintf("%q", part)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%v", value)
	}
}

// shorthandSafeItemPattern matches array items that need no quoting inside a
// shorthand array literal.
var shorthandSafeItemPattern = regexp.MustCompile(`^[A-Za-z0-9_.@/-]+$`)

// shorthandYieldsArray confirms a suggested assignment actually parses to an
// array before it is offered — a suggestion that fails the same way is worse
// than none.
func shorthandYieldsArray(field, assignment string) bool {
	parsed, err := shorthand.Unmarshal(assignment, shorthand.ParseOptions{EnableObjectDetection: true}, nil)
	if err != nil {
		return false
	}
	object, isObject := parsed.(map[string]any)
	if !isObject {
		return false
	}
	_, isArray := object[field].([]any)
	return isArray
}

// repairUnbracketedArrays rebuilds the one mistake worth guessing at: comma-
// separated array items written without brackets ("tags: prod, billing").
// Shorthand commas separate properties, so the loose items arrive as
// top-level segments with no field name of their own — fold each run of
// nameless segments back into the array field that precedes it, bracket that
// field's value, and offer the rebuilt line only if it parses to the shapes
// the schema wants. "" whenever the input doesn't fit that one mistake.
func repairUnbracketedArrays(body string, arrayFields map[string]bool) string {
	if len(arrayFields) == 0 {
		return ""
	}
	type segmentGroup struct {
		name    string
		value   string
		wrapped bool
	}
	var groups []segmentGroup
	repaired := false
	for _, segment := range splitTopLevelShorthandSegments(body) {
		if match := shorthandSegmentFieldPattern.FindStringSubmatch(segment); match != nil {
			groups = append(groups, segmentGroup{name: match[1], value: strings.TrimSpace(match[2])})
			continue
		}
		if len(groups) == 0 {
			return ""
		}
		last := &groups[len(groups)-1]
		if !arrayFields[last.name] || strings.HasPrefix(last.value, "[") {
			return ""
		}
		last.value += ", " + strings.TrimSpace(segment)
		last.wrapped = true
		repaired = true
	}
	if !repaired {
		return ""
	}
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		value := group.value
		if group.wrapped {
			value = "[" + value + "]"
		}
		parts = append(parts, group.name+": "+value)
	}
	suggestion := strings.Join(parts, ", ")
	parsed, err := shorthand.Unmarshal(suggestion, shorthand.ParseOptions{EnableObjectDetection: true}, nil)
	if err != nil {
		return ""
	}
	object, isObject := parsed.(map[string]any)
	if !isObject {
		return ""
	}
	for _, group := range groups {
		if !group.wrapped {
			continue
		}
		if _, isArray := object[group.name].([]any); !isArray {
			return ""
		}
	}
	return suggestion
}

// shorthandSegmentFieldPattern matches a top-level "name: value" segment; a
// segment without it is a loose array item. Plain top-level names only — a
// dotted path can't be bracket-repaired from out here.
var shorthandSegmentFieldPattern = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_-]*)\s*:(.*)$`)

// splitTopLevelShorthandSegments splits a shorthand body on the commas that
// separate top-level properties, leaving commas inside brackets, braces, and
// quoted strings alone.
func splitTopLevelShorthandSegments(body string) []string {
	var segments []string
	depth := 0
	inQuote := false
	start := 0
	for index := 0; index < len(body); index++ {
		switch character := body[index]; {
		case inQuote:
			if character == '\\' {
				index++
			} else if character == '"' {
				inQuote = false
			}
		case character == '"':
			inQuote = true
		case character == '[' || character == '{':
			depth++
		case character == ']' || character == '}':
			if depth > 0 {
				depth--
			}
		case character == ',' && depth == 0:
			segments = append(segments, body[start:index])
			start = index + 1
		}
	}
	return append(segments, body[start:])
}

func requestSchemaTopLevelFields(longHelp string) map[string]bool {
	list := requestSchemaTopLevelFieldList(longHelp)
	if list == nil {
		return nil
	}
	fields := make(map[string]bool, len(list))
	for _, field := range list {
		fields[strings.TrimSuffix(field, "*")] = true
	}
	return fields
}

// requestSchemaTopLevelFieldList returns the request body's top-level fields
// in schema order, each keeping the schema's trailing `*` required marker.
// nil when the help carries no object request schema.
func requestSchemaTopLevelFieldList(longHelp string) []string {
	sketches := requestSchemaTopLevelFieldSketches(longHelp)
	if sketches == nil {
		return nil
	}
	fields := make([]string, 0, len(sketches))
	for _, field := range sketches {
		name := field.name
		if field.required {
			name += "*"
		}
		fields = append(fields, name)
	}
	return fields
}

// bodyFieldSketch is one top-level request-schema field with the raw one-line
// value sketch to the right of its colon ("[", "{", "(string)"), for the
// session's placeholder ghost (ai_placeholder.go) and the value-shape
// validation below. For array fields, elem names the element type parsed
// from the schema's next line ("string", "integer", "{" for objects; ""
// when unknown).
type bodyFieldSketch struct {
	name     string
	required bool
	sketch   string
	elem     string
}

// requestSchemaTopLevelFieldSketches is the parse behind
// requestSchemaTopLevelFieldList, keeping the required marker and value
// sketch separate. Same contract: nil when the help carries no object
// request schema, an empty non-nil slice for an object with no fields.
func requestSchemaTopLevelFieldSketches(longHelp string) []bodyFieldSketch {
	_, afterHeading, found := strings.Cut(longHelp, "## Request Schema")
	if !found {
		return nil
	}
	_, schemaBlock, found := strings.Cut(afterHeading, "```schema")
	if !found {
		return nil
	}
	schemaBlock, _, _ = strings.Cut(schemaBlock, "```")
	lines := strings.Split(schemaBlock, "\n")
	var fields []bodyFieldSketch
	foundObject := false
	for index, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if !foundObject {
			if trimmedLine == "{" {
				foundObject = true
			} else if strings.HasPrefix(trimmedLine, "[") {
				return nil
			}
			continue
		}
		if match := schemaBodyFieldSketchPattern.FindStringSubmatch(line); match != nil {
			sketch := strings.TrimSpace(match[3])
			fields = append(fields, bodyFieldSketch{
				name:     match[1],
				required: match[2] == "*",
				sketch:   sketch,
				elem:     arrayElemSketch(sketch, lines[index+1:]),
			})
		}
	}
	if !foundObject {
		return nil
	}
	if fields == nil {
		fields = []bodyFieldSketch{}
	}
	return fields
}

// sketchIsArray reports an array-typed field. Restish renders array values
// multi-line, so the field's own line carries just the opener.
func sketchIsArray(sketch string) bool {
	return strings.HasPrefix(sketch, "[")
}

// sketchIsObject reports an object-typed field: a nested block opener, the
// "(object)" empty-object special case, or a schema-composition block.
func sketchIsObject(sketch string) bool {
	for _, prefix := range []string{"{", "(object", "allOf{", "oneOf{", "anyOf{"} {
		if strings.HasPrefix(sketch, prefix) {
			return true
		}
	}
	return false
}

// arrayElemSketch names an array field's element type. Restish renders the
// element schema on the lines after the "[" opener ("    (string)"); a
// single-line "[string]" spelling is accepted too. "" when the elements are
// not a plain scalar or object.
func arrayElemSketch(sketch string, rest []string) string {
	if !sketchIsArray(sketch) {
		return ""
	}
	if sketch == "[{" {
		return "{"
	}
	if sketch != "[" {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(sketch, "["), "]"))
		if !strings.HasSuffix(sketch, "]") || inner == "" || strings.HasPrefix(inner, "<") {
			return ""
		}
		return schemaTypeWord(inner)
	}
	for _, line := range rest {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if sketchIsObject(trimmed) {
			return "{"
		}
		if strings.HasPrefix(trimmed, "(") {
			return schemaTypeWord(trimmed)
		}
		return ""
	}
	return ""
}

// schemaTypeWord extracts the bare type word from a schema value sketch:
// "(string minLen:1) The name" → "string", "(integer|null)" → "integer",
// a bare "string" stays as is. "" when no type word is recognizable.
func schemaTypeWord(sketch string) string {
	sketch = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sketch), "("))
	if cut := strings.IndexAny(sketch, ") "); cut >= 0 {
		sketch = sketch[:cut]
	}
	for _, part := range strings.Split(sketch, "|") {
		if part != "" && part != "null" {
			return part
		}
	}
	return ""
}

// arrayItemsExample spells example items for an array field's element type,
// for ghost labels and error suggestions ("a, b" for strings). "" when the
// elements are objects or unknown — no literal example would be typeable.
func arrayItemsExample(elem string) string {
	switch elem {
	case "string":
		return "a, b"
	case "integer", "number":
		return "1, 2"
	case "boolean":
		return "true, false"
	}
	return ""
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
	return currencyFromJSONBody(stdinBody)
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
