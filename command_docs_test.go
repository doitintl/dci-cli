package main

// Validator for the curated command docs (COMMAND-DOCS-SPEC.md D4). The
// offline tests check file invariants, rendering, and the local command tree;
// TestCommandDocsAgainstSpec and TestCommandDocsCoverage need the production
// OpenAPI spec and skip unless DCI_COMMAND_DOCS_SPEC points at a copy (CI
// fetches https://api.doit.com/openapi.yaml; locally:
// curl -sSL https://api.doit.com/openapi.yaml -o /tmp/openapi.yaml &&
// DCI_COMMAND_DOCS_SPEC=/tmp/openapi.yaml go test -run TestCommandDocs .).

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/openapi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

func TestCommandDocsEmbeddedFilesParse(t *testing.T) {
	docs, err := loadCommandDocs()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 {
		t.Fatal("no command docs embedded")
	}
	for key, doc := range docs {
		if doc.Notes != "" {
			if lines := mdxUnsafeNoteLines(doc.Notes); len(lines) > 0 {
				t.Errorf("%s: notes lines %v contain an unescaped { or < outside code; MDX would not render them literally", key, lines)
			}
			if !admonitionFencesBalanced(doc.Notes) {
				t.Errorf("%s: notes have an unbalanced ::: admonition fence", key)
			}
		}
		for index, example := range doc.Examples {
			words := exampleInvocationWords(example.Command)
			for position, word := range words {
				if strings.HasPrefix(word, "<") && len(word) > 1 && !isCommandDocPlaceholder(word) {
					t.Errorf("%s: example %d token %q is not a valid <placeholder> (lowercase letters, digits, dashes); stdin is a bare < followed by a file", key, index+1, word)
				}
				if word == "<" && position == len(words)-1 {
					t.Errorf("%s: example %d has a bare < with no file after it", key, index+1)
				}
			}
			if strings.Contains(example.Command, "\t") {
				t.Errorf("%s: example %d contains a tab", key, index+1)
			}
		}
	}
}

func TestParseCommandDocRejectsUnquotedShorthand(t *testing.T) {
	_, err := parseCommandDoc("patch-anomaly.yaml", []byte(`command: patch-anomaly
examples:
  - description: Unquoted shorthand.
    command: dci patch-anomaly <id> customerFeedback.reviewStatus: UNDER_REVIEW
`))
	if err == nil || !strings.Contains(err.Error(), "quote every command: value") {
		t.Fatalf("expected the quoting hint, got %v", err)
	}
}

func TestParseCommandDocInvariants(t *testing.T) {
	cases := map[string]struct {
		file, body, want string
	}{
		"file name mismatch":             {"get-anomaly.yaml", "command: patch-anomaly\nexamples:\n  - {description: x, command: \"dci patch-anomaly <id>\"}\n", "belongs in patch-anomaly.yaml"},
		"no examples":                    {"get-anomaly.yaml", "command: get-anomaly\n", "at least one example"},
		"draft without examples is fine": {"get-anomaly.yaml", "command: get-anomaly\ndraft: true\n", ""},
		"wrong prefix":                   {"skill-list.yaml", "command: skill list\nexamples:\n  - {description: x, command: \"dci skill update\"}\n", "must start with \"dci skill list\""},
		"env prefix allowed":             {"status.yaml", "command: status\nexamples:\n  - {description: x, command: \"DCI_TZ=UTC dci status\"}\n", ""},
		"unknown field":                  {"status.yaml", "command: status\nexample: x\n", "field example not found"},
		"self related":                   {"status.yaml", "command: status\nrelated: [status]\nexamples:\n  - {description: x, command: \"dci status\"}\n", "lists the command itself"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseCommandDoc(testCase.file, []byte(testCase.body))
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("want error containing %q, got %v", testCase.want, err)
			}
		})
	}
}

func TestParseCommandDocsFSAggregatesErrors(t *testing.T) {
	fsys := fstest.MapFS{
		"command-docs/a.yaml": {Data: []byte("command: b\n")},
		"command-docs/c.yaml": {Data: []byte("command: c\n")},
		"command-docs/d.yaml": {Data: []byte("command: d\nexamples:\n  - {description: x, command: \"dci d\"}\n")},
	}
	docs, err := parseCommandDocsFS(fsys, "command-docs")
	if err == nil {
		t.Fatal("expected aggregated errors")
	}
	for _, want := range []string{"a.yaml", "c.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s:\n%v", want, err)
		}
	}
	if _, ok := docs["d"]; !ok {
		t.Error("valid file should still load alongside broken ones")
	}
}

func TestSplitShellWords(t *testing.T) {
	cases := map[string][]string{
		`dci open budget "Team Backyard"`:         {"dci", "open", "budget", "Team Backyard"},
		`dci x a{b: "c d", e: 'f g'} < file.json`: {"dci", "x", "a{b:", "c d,", "e:", "f g}", "<", "file.json"},
		`dci x --filter "a:b|c:d"`:                {"dci", "x", "--filter", "a:b|c:d"},
		`dci x comment: "He said \"hi\""`:         {"dci", "x", "comment:", `He said "hi"`},
		"dci  x\tspaced":                          {"dci", "x", "spaced"},
	}
	for input, want := range cases {
		if got := splitShellWords(input); !reflect.DeepEqual(got, want) {
			t.Errorf("splitShellWords(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExampleInvocationWordsStopsAtShellOperators(t *testing.T) {
	got := exampleInvocationWords(`DCI_TZ=UTC dci query --output json < q.json | jq '.result'`)
	want := []string{"dci", "query", "--output", "json", "<", "q.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSplitExampleCommandSeparatesPositionalsFlagsAndBody(t *testing.T) {
	parts := splitExampleCommand(
		`dci patch-anomaly <anomaly-id> --output json customerFeedback{reviewStatus: RESOLVED, resolution: NOT_ANOMALY}`,
		1, 1, func(flag string) bool { return flag == "--output" })
	if !reflect.DeepEqual(parts.Positionals, []string{"<anomaly-id>"}) {
		t.Errorf("positionals = %q", parts.Positionals)
	}
	if !reflect.DeepEqual(parts.Flags, []string{"--output"}) || parts.FlagValues["--output"] != "json" {
		t.Errorf("flags = %q values = %v", parts.Flags, parts.FlagValues)
	}
	body, ok, err := exampleBody(parts)
	if err != nil || !ok {
		t.Fatalf("body parse: ok=%t err=%v", ok, err)
	}
	feedback, _ := body.(map[string]any)["customerFeedback"].(map[string]any)
	if feedback["reviewStatus"] != "RESOLVED" || feedback["resolution"] != "NOT_ANOMALY" {
		t.Errorf("parsed body = %v", body)
	}

	stdin := splitExampleCommand(`dci patch-anomaly <anomaly-id> < feedback.json`, 1, 1, nil)
	if stdin.Stdin != "feedback.json" || len(stdin.Body) != 0 {
		t.Errorf("stdin parts = %+v", stdin)
	}
	if _, ok, _ := exampleBody(stdin); ok {
		t.Error("stdin example must not report an inline body")
	}
}

func TestCommandDocKeyAndHelpSwap(t *testing.T) {
	root := &cobra.Command{Use: "dci"}
	apiParent := &cobra.Command{Use: "dci", Hidden: true}
	root.AddCommand(apiParent)
	patch := &cobra.Command{Use: "patch-anomaly id", GroupID: "anomalies", Example: "  synthesized"}
	apiParent.AddGroup(&cobra.Group{ID: "anomalies", Title: "Anomalies"})
	apiParent.AddCommand(patch)
	skill := &cobra.Command{Use: "skill"}
	list := &cobra.Command{Use: "list"}
	skill.AddCommand(list)
	root.AddCommand(skill)

	if got := commandDocKey(patch); got != "patch-anomaly" {
		t.Errorf("API command key = %q", got)
	}
	if got := commandDocKey(list); got != "skill list" {
		t.Errorf("nested local key = %q", got)
	}

	restore := applyCommandDocHelp(patch)
	if !strings.Contains(patch.Example, "# Take an anomaly into review.") || strings.Contains(patch.Example, "synthesized") {
		t.Errorf("curated examples not applied:\n%s", patch.Example)
	}
	restore()
	if patch.Example != "  synthesized" {
		t.Errorf("Example not restored: %q", patch.Example)
	}

	if got := renderCommandDocArguments(patch); !strings.HasPrefix(got, "  id  Anomaly ID") {
		t.Errorf("arguments block = %q", got)
	}
	if got := renderCommandDocArguments(list); got != "" {
		t.Errorf("command without documented arguments rendered %q", got)
	}
}

func TestLookupCommandDocSkipsDrafts(t *testing.T) {
	doc, err := parseCommandDoc("x.yaml", []byte("command: x\ndraft: true\nexamples:\n  - {description: generated, command: \"dci x a: b\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Draft {
		t.Fatal("draft flag not parsed")
	}
	cmd := &cobra.Command{Use: "x", Example: "keep"}
	root := &cobra.Command{Use: "dci"}
	root.AddCommand(cmd)
	// No embedded doc named "x" exists, and a draft would be treated the same:
	// the fallback example stays.
	applyCommandDocHelp(cmd)()
	if cmd.Example != "keep" {
		t.Errorf("fallback example changed to %q", cmd.Example)
	}
}

func TestRenderCommandDocExamplesFormat(t *testing.T) {
	got := renderCommandDocExamples(commandDoc{Examples: []commandDocExample{
		{Description: "First.", Command: "dci a"},
		{Description: "Second.", Command: "dci a\n  b: c"},
	}})
	want := "  # First.\n  dci a\n\n  # Second.\n  dci a b: c"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMdxNoteChecks(t *testing.T) {
	if lines := mdxUnsafeNoteLines(":::info\nUse `{id}` and \\{escaped\\} text, `<a>` too.\n```\n{raw} <ok>\n```\n:::"); len(lines) != 0 {
		t.Errorf("safe notes flagged lines %v", lines)
	}
	if lines := mdxUnsafeNoteLines("plain {jsx}\nfine\n<tag>"); !reflect.DeepEqual(lines, []int{1, 3}) {
		t.Errorf("unsafe lines = %v", lines)
	}
	if admonitionFencesBalanced(":::tip\nopen") || !admonitionFencesBalanced(":::tip\nx\n:::\n") {
		t.Error("admonition balance check wrong")
	}
}

// newLocalCommandTree builds the CLI's local command tree the way run() does,
// with a stand-in `dci` API parent carrying the CLI-wide persistent flags, so
// docs for local commands can be checked for flag and subcommand existence
// offline. cli.Root is swapped for the test's lifetime.
func newLocalCommandTree(t *testing.T) *cobra.Command {
	t.Helper()
	oldRoot := cli.Root
	root := &cobra.Command{Use: "dci"}
	root.PersistentFlags().Bool("agent", false, "")
	root.PersistentFlags().Bool("no-agent", false, "")
	cli.Root = root
	t.Cleanup(func() { cli.Root = oldRoot })
	root.AddCommand(&cobra.Command{Use: "dci", Hidden: true})
	configDir := t.TempDir()
	registerStatusCommands(configDir)
	registerConfigCommand(configDir)
	registerAuthCommands(configDir)
	registerCustomerContextCommands(configDir)
	registerUpdateCommand(configDir)
	registerVersionCommand()
	registerDocsCommand()
	registerOpenCommand(configDir)
	registerSkillCommands()
	registerCommandCatalog()
	registerQuestionCommands()
	addOutputFlag()
	return root
}

// findLocalCommand resolves a doc's command path in the local tree, looking
// under the root and under the `dci` API parent (question commands live
// there).
func findLocalCommand(root *cobra.Command, commandPath string) *cobra.Command {
	fields := strings.Fields(commandPath)
	if found, _, err := root.Find(fields); err == nil && found != nil && found != root && strings.Join(strings.Fields(commandDocKey(found)), " ") == commandPath {
		return found
	}
	if dciCmd := findDCICommand(); dciCmd != nil {
		if found, _, err := dciCmd.Find(fields); err == nil && found != nil && found != dciCmd && commandDocKey(found) == commandPath {
			return found
		}
	}
	return nil
}

func flagKnown(cmd *cobra.Command, name string) (known bool, takesValue bool) {
	lookup := func(set *pflag.FlagSet) *pflag.Flag {
		if strings.HasPrefix(name, "--") {
			return set.Lookup(strings.TrimPrefix(name, "--"))
		}
		if len(name) == 2 {
			return set.ShorthandLookup(strings.TrimPrefix(name, "-"))
		}
		return nil
	}
	for _, set := range []*pflag.FlagSet{cmd.Flags(), cmd.InheritedFlags(), cmd.PersistentFlags()} {
		if flag := lookup(set); flag != nil {
			return true, flagTakesValue(flag)
		}
	}
	// The CLI-wide flags every API and question command carries.
	if dciCmd := findDCICommand(); dciCmd != nil {
		if flag := lookup(dciCmd.PersistentFlags()); flag != nil {
			return true, flagTakesValue(flag)
		}
	}
	if flag := lookup(cli.Root.PersistentFlags()); flag != nil {
		return true, flagTakesValue(flag)
	}
	return false, false
}

// flagTakesValue reports whether the next word after the flag is its value:
// booleans and flags with an optional value (`--chart[=kind]`) take none.
func flagTakesValue(flag *pflag.Flag) bool {
	return flag.Value.Type() != "bool" && flag.NoOptDefVal == ""
}

func TestCommandDocsMatchLocalCommands(t *testing.T) {
	root := newLocalCommandTree(t)
	docs, err := loadCommandDocs()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for key, doc := range docs {
		cmd := findLocalCommand(root, key)
		if cmd == nil {
			continue // an API operation — TestCommandDocsAgainstSpec covers it
		}
		checked++
		for index, example := range doc.Examples {
			parts := splitExampleCommand(example.Command, len(strings.Fields(key)), 0, func(flag string) bool {
				_, takesValue := flagKnown(cmd, flag)
				return takesValue
			})
			for _, flag := range parts.Flags {
				if known, _ := flagKnown(cmd, flag); !known {
					t.Errorf("%s: example %d uses unknown flag %s", key, index+1, flag)
				}
			}
		}
		for name := range doc.Arguments {
			if !strings.Contains(cmd.Use, name) {
				t.Errorf("%s: documents argument %q, which the usage line %q does not declare", key, name, cmd.Use)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no local command docs were checked; the tree or the keys are wrong")
	}
}

// --- Spec-gated checks -----------------------------------------------------

type commandDocsSpec struct {
	api cli.API
	raw map[string]any
}

func loadCommandDocsSpec(t *testing.T) commandDocsSpec {
	t.Helper()
	specPath := os.Getenv("DCI_COMMAND_DOCS_SPEC")
	if specPath == "" {
		t.Skip("DCI_COMMAND_DOCS_SPEC not set; skipping spec-backed command docs validation")
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	entrypoint, _ := url.Parse(defaultAPIBase + "/")
	specURL, _ := url.Parse(defaultAPIBase + "/openapi.yaml")
	api, err := openapi.New().Load(*entrypoint, *specURL, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(data)),
	})
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse raw spec: %v", err)
	}
	return commandDocsSpec{api: api, raw: raw}
}

func (spec commandDocsSpec) operation(name string) *cli.Operation {
	for index := range spec.api.Operations {
		operation := &spec.api.Operations[index]
		if operation.Name == name {
			return operation
		}
		for _, alias := range operation.Aliases {
			if alias == name {
				return operation
			}
		}
	}
	return nil
}

// rawOperation finds the raw spec object for a loaded operation by method and
// path, the only identity both sides share unchanged.
func (spec commandDocsSpec) rawOperation(operation cli.Operation) map[string]any {
	paths, _ := spec.raw["paths"].(map[string]any)
	for specPath, methods := range paths {
		methodMap, _ := methods.(map[string]any)
		for method, raw := range methodMap {
			if strings.EqualFold(method, operation.Method) && strings.HasSuffix(operation.URITemplate, specPath) {
				if operationMap, ok := raw.(map[string]any); ok {
					return operationMap
				}
			}
		}
	}
	return nil
}

func (spec commandDocsSpec) resolve(node any) map[string]any {
	object, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	ref, hasRef := object["$ref"].(string)
	if !hasRef {
		return object
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var current any = spec.raw
	for _, part := range strings.Split(ref[2:], "/") {
		next, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = next[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
	}
	return spec.resolve(current)
}

func (spec commandDocsSpec) requestSchema(operation cli.Operation) map[string]any {
	raw := spec.rawOperation(operation)
	if raw == nil {
		return nil
	}
	body := spec.resolve(raw["requestBody"])
	if body == nil {
		return nil
	}
	content, _ := body["content"].(map[string]any)
	media, ok := content["application/json"].(map[string]any)
	if !ok {
		for _, first := range content {
			media, _ = first.(map[string]any)
			break
		}
	}
	if media == nil {
		return nil
	}
	return spec.resolve(media["schema"])
}

// checkBodyAgainstSchema walks a parsed example body against the request
// schema: unknown properties (where the schema closes the object), enum
// membership, and — at the top level only — required fields. oneOf/anyOf
// variants are not resolved (any value passes); `<placeholder>` strings pass
// enum checks.
func (spec commandDocsSpec) checkBodyAgainstSchema(value any, schema map[string]any, at string, topLevel bool) []string {
	schema = spec.mergeAllOf(schema)
	if schema == nil || schema["oneOf"] != nil || schema["anyOf"] != nil {
		return nil
	}
	var problems []string
	label := at
	if label == "" {
		label = "body"
	}
	if enum, ok := schema["enum"].([]any); ok {
		if text, isString := value.(string); isString && !isCommandDocPlaceholder(text) {
			found := false
			for _, member := range enum {
				if fmt.Sprint(member) == text {
					found = true
					break
				}
			}
			if !found {
				problems = append(problems, fmt.Sprintf("%s: %q is not one of the enum values %v", label, text, enum))
			}
		}
		return problems
	}
	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		additional := schema["additionalProperties"]
		if topLevel {
			if required, ok := schema["required"].([]any); ok {
				for _, name := range required {
					if _, present := typed[fmt.Sprint(name)]; !present {
						problems = append(problems, fmt.Sprintf("%s: required field %q is missing", label, name))
					}
				}
			}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childAt := key
			if at != "" {
				childAt = at + "." + key
			}
			if propertySchema, ok := properties[key]; ok {
				problems = append(problems, spec.checkBodyAgainstSchema(typed[key], spec.resolve(propertySchema), childAt, false)...)
				continue
			}
			if additionalSchema, ok := additional.(map[string]any); ok {
				problems = append(problems, spec.checkBodyAgainstSchema(typed[key], spec.resolve(additionalSchema), childAt, false)...)
				continue
			}
			if closed, isBool := additional.(bool); (isBool && !closed) || (!isBool && len(properties) > 0) {
				problems = append(problems, fmt.Sprintf("%s: unknown field %q", label, key))
			}
		}
	case []any:
		items := spec.resolve(schema["items"])
		for index, item := range typed {
			problems = append(problems, spec.checkBodyAgainstSchema(item, items, fmt.Sprintf("%s[%d]", at, index), false)...)
		}
	}
	return problems
}

func (spec commandDocsSpec) mergeAllOf(schema map[string]any) map[string]any {
	parts, ok := schema["allOf"].([]any)
	if !ok {
		return schema
	}
	merged := map[string]any{}
	properties := map[string]any{}
	var required []any
	for key, value := range schema {
		if key != "allOf" {
			merged[key] = value
		}
	}
	for _, part := range parts {
		resolved := spec.mergeAllOf(spec.resolve(part))
		for key, value := range resolved {
			switch key {
			case "properties":
				for name, property := range value.(map[string]any) {
					properties[name] = property
				}
			case "required":
				required = append(required, value.([]any)...)
			default:
				if _, exists := merged[key]; !exists {
					merged[key] = value
				}
			}
		}
	}
	if existing, ok := merged["properties"].(map[string]any); ok {
		for name, property := range existing {
			properties[name] = property
		}
	}
	if len(properties) > 0 {
		merged["properties"] = properties
	}
	if existing, ok := merged["required"].([]any); ok {
		required = append(required, existing...)
	}
	if len(required) > 0 {
		merged["required"] = required
	}
	return merged
}

func TestCommandDocsAgainstSpec(t *testing.T) {
	spec := loadCommandDocsSpec(t)
	root := newLocalCommandTree(t)
	docs, err := loadCommandDocs()
	if err != nil {
		t.Fatal(err)
	}
	dciCmd := findDCICommand()

	commandExists := func(name string) bool {
		if spec.operation(name) != nil {
			return true
		}
		if _, ok := docs[name]; ok {
			return true
		}
		return findLocalCommand(root, name) != nil
	}

	keys := make([]string, 0, len(docs))
	for key := range docs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		doc := docs[key]
		operation := spec.operation(key)
		if operation == nil {
			if findLocalCommand(root, key) == nil {
				t.Errorf("%s: no such command in the spec or the local command tree", key)
			}
			continue
		}
		if operation.Name != key {
			t.Errorf("%s: file is named after alias %q; the canonical command is %q", key, key, operation.Name)
		}
		paramNames := map[string]bool{}
		for _, parameter := range operation.PathParams {
			paramNames[parameter.Name] = true
			paramNames[parameter.OptionName()] = true
		}
		for name := range doc.Arguments {
			if !paramNames[name] {
				t.Errorf("%s: documents argument %q, which is not a path parameter of the operation", key, name)
			}
		}
		for _, related := range doc.Related {
			if !commandExists(related) {
				t.Errorf("%s: related command %q does not exist", key, related)
			}
		}
		operationFlag := func(name string) (*cli.Param, bool) {
			for _, parameter := range append(append([]*cli.Param{}, operation.QueryParams...), operation.HeaderParams...) {
				if "--"+parameter.OptionName() == name {
					return parameter, true
				}
			}
			return nil, false
		}
		takesValue := func(flag string) bool {
			if parameter, ok := operationFlag(flag); ok {
				return parameter.Type != "boolean"
			}
			_, value := flagKnown(dciCmd, flag)
			return value
		}
		schema := spec.requestSchema(*operation)
		for index, example := range doc.Examples {
			parts := splitExampleCommand(example.Command, 1, len(operation.PathParams), takesValue)
			if len(parts.Command) == 0 {
				continue // parseCommandDoc already reported the prefix
			}
			for _, flag := range parts.Flags {
				if _, ok := operationFlag(flag); ok {
					continue
				}
				if known, _ := flagKnown(dciCmd, flag); !known {
					t.Errorf("%s: example %d uses unknown flag %s", key, index+1, flag)
				}
			}
			if got, want := len(parts.Positionals), len(operation.PathParams); got != want {
				t.Errorf("%s: example %d has %d positional argument(s), the operation takes %d: %q", key, index+1, got, want, example.Command)
			}
			if operation.BodyMediaType == "" {
				if len(parts.Body) > 0 || parts.Stdin != "" {
					t.Errorf("%s: example %d passes a body (%q) but the operation takes none", key, index+1, strings.Join(parts.Body, " "))
				}
				continue
			}
			body, inline, parseErr := exampleBody(parts)
			if parseErr != nil {
				t.Errorf("%s: example %d body does not parse as shorthand: %v", key, index+1, parseErr)
				continue
			}
			if !inline {
				continue
			}
			if validFields := requestSchemaTopLevelFields(operation.Long); len(validFields) > 0 {
				if object, ok := body.(map[string]any); ok {
					for field := range object {
						if !validFields[field] {
							t.Errorf("%s: example %d uses unknown top-level field %q", key, index+1, field)
						}
					}
				}
			}
			if shapeErr := validateBodyValueShapes(requestSchemaTopLevelFieldSketches(operation.Long), parts.Body); shapeErr != nil {
				t.Errorf("%s: example %d body shape: %v", key, index+1, shapeErr)
			}
			if schema != nil {
				for _, problem := range spec.checkBodyAgainstSchema(body, schema, "", true) {
					t.Errorf("%s: example %d: %s", key, index+1, problem)
				}
			}
		}
	}
}

// TestCommandDocsCoverage reports (never fails on) operations without a
// curated doc; tools/commanddocs coverage prints the same list as CI
// annotations, and the release gate (COMMAND-DOCS-SPEC.md D5) is where a gap
// blocks.
func TestCommandDocsCoverage(t *testing.T) {
	spec := loadCommandDocsSpec(t)
	docs, err := loadCommandDocs()
	if err != nil {
		t.Fatal(err)
	}
	var missing, drafts []string
	total := 0
	for _, operation := range spec.api.Operations {
		if operation.Hidden {
			continue
		}
		total++
		doc, ok := docs[operation.Name]
		switch {
		case !ok:
			missing = append(missing, operation.Name)
		case doc.Draft:
			drafts = append(drafts, operation.Name)
		}
	}
	sort.Strings(missing)
	sort.Strings(drafts)
	t.Logf("command docs coverage: %d/%d operations curated, %d drafts, %d missing", total-len(missing)-len(drafts), total, len(drafts), len(missing))
	if len(missing) > 0 {
		t.Logf("missing: %s", strings.Join(missing, ", "))
	}
}
