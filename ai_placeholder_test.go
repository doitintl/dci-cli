package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

// placeholderTestTree installs a fake cobra tree and resolution index for the
// ghost-signature tests, restoring both afterwards: a bodied API operation
// with flags, a body-only operation with an optional field, a resolvable
// operation, a beta subtree, and a local command group.
func placeholderTestTree(t *testing.T) {
	t.Helper()
	oldRoot, oldIndex := cli.Root, resolutionIndex
	root := &cobra.Command{Use: "dci"}
	api := &cobra.Command{Use: "dci"}
	ticketTags := &cobra.Command{
		Use:     "add-ticket-tags ticketid",
		Long:    "Add tags.\n\n## Request Schema (application/json)\n\n```schema\n{\n  tags*: [\n    (string)\n  ]\n}\n```\n",
		Example: "  dci add-ticket-tags 318240 tags: [prod]\n",
	}
	ticketTags.Flags().String("note", "", "")
	ticketTags.Flags().Bool("force", false, "")
	api.AddCommand(ticketTags)
	api.AddCommand(&cobra.Command{
		Use:  "create-thing",
		Long: "Create.\n\n## Request Schema (application/json)\n\n```schema\n{\n  config*: {\n    currency: (string)\n  }\n  name: (string)\n  labels: [\n    (string)\n  ]\n}\n```\n",
	})
	api.AddCommand(&cobra.Command{
		Use:  "create-widget",
		Long: "Create.\n\n## Request Schema (application/json)\n\n```schema\n{\n  a*: (string)\n  b*: (string)\n}\n```\n",
	})
	api.AddCommand(&cobra.Command{Use: "get-report report-id"})
	beta := &cobra.Command{Use: "beta"}
	beta.AddCommand(&cobra.Command{Use: "run-report id"})
	api.AddCommand(beta)
	root.AddCommand(api)
	group := &cobra.Command{Use: "customer-context"}
	group.AddCommand(&cobra.Command{Use: "set <value>"})
	root.AddCommand(group)
	cli.Root = root
	resolutionIndex = map[string]resolutionListTarget{
		"get-report":      {listPath: "/analytics/v1/reports", resource: "reports"},
		"beta run-report": {listPath: "/analytics/v1/reports", resource: "reports", hasBody: true},
	}
	oldParameters := operationPathParameters
	operationPathParameters = map[string][]*cli.Param{
		"add-ticket-tags": {{Name: "ticketId", Type: "integer", Example: 318240}},
		"get-report":      {{Name: "reportId", Type: "string"}},
	}
	t.Cleanup(func() {
		cli.Root, resolutionIndex, operationPathParameters = oldRoot, oldIndex, oldParameters
	})
}

func placeholderLabels(placeholders []aiPlaceholder) []string {
	labels := make([]string, 0, len(placeholders))
	for _, placeholder := range placeholders {
		labels = append(labels, placeholder.label)
	}
	return labels
}

func TestAIPlaceholderSignatures(t *testing.T) {
	placeholderTestTree(t)
	cases := []struct {
		argv         string
		want         string // labels joined by " · "; "-" for a nil signature
		words        int
		optionalBody bool
	}{
		{"add-ticket-tags", "ticketid · tags*: [a, b]", 1, false},
		{"add-ticket-tags 318240 tags: a", "ticketid · tags*: [a, b]", 1, false},
		{"create-thing", "config*: object", 1, true},
		{"get-report", "report-name-or-id", 1, false},
		{"beta run-report", "report-name-or-id", 2, false},
		{"customer-context set", "<value>", 2, false},
		{"customer-context", "-", 0, false}, // group: the popup's job
		{"beta", "-", 0, false},
		{"no-such-command", "-", 0, false},
	}
	for _, testCase := range cases {
		signature := aiPlaceholderSignatureFor(strings.Fields(testCase.argv))
		if testCase.want == "-" {
			if signature != nil {
				t.Fatalf("signature(%q) = %v, want nil", testCase.argv, signature)
			}
			continue
		}
		if signature == nil {
			t.Fatalf("signature(%q) = nil", testCase.argv)
		}
		if got := strings.Join(placeholderLabels(signature.placeholders), " · "); got != testCase.want {
			t.Fatalf("signature(%q) labels = %q, want %q", testCase.argv, got, testCase.want)
		}
		if signature.words != testCase.words {
			t.Fatalf("signature(%q) words = %d, want %d", testCase.argv, signature.words, testCase.words)
		}
		if signature.optionalBody != testCase.optionalBody {
			t.Fatalf("signature(%q) optionalBody = %v, want %v", testCase.argv, signature.optionalBody, testCase.optionalBody)
		}
	}
	if signature := aiPlaceholderSignatureFor(nil); signature != nil {
		t.Fatalf("nil argv signature = %v", signature)
	}
}

func TestAIPlaceholdersRemaining(t *testing.T) {
	placeholderTestTree(t)
	cases := []struct {
		argv string
		want string // remaining labels joined by " · "; "" for none
	}{
		// Path first, then body, positionally.
		{"add-ticket-tags", "ticketid · tags*: [a, b]"},
		{"add-ticket-tags 318240", "tags*: [a, b]"},
		{"add-ticket-tags 318240 tags: a, b", ""},
		// Flags and their values are skipped; bool flags take no value.
		{"add-ticket-tags --note x 318240", "tags*: [a, b]"},
		{"add-ticket-tags --force 318240", "tags*: [a, b]"},
		{"add-ticket-tags --note=x", "ticketid · tags*: [a, b]"},
		// Whole-body tokens consume every body placeholder.
		{"add-ticket-tags 318240 @body.json", ""},
		{"add-ticket-tags 318240 <body.json", ""},
		// Inline JSON (shell-quoted, as the splitter delivers it) consumes
		// the fields it names, nothing else.
		{`add-ticket-tags 318240 '{"tags":["a"]}'`, ""},
		{`add-ticket-tags 318240 '{"other":1}'`, "tags*: [a, b]"},
		// Positional order wins: a body-shaped token still fills an open
		// path slot, exactly as the CLI would parse it.
		{"add-ticket-tags tags: a", "tags*: [a, b]"},
		// Name-based consumption is order-free once the body starts, and a
		// dotted shorthand path consumes its top-level field.
		{"create-thing config.currency: USD", ""},
		{"create-thing name: x", "config*: object"},
		// A resolvable command's surplus positionals are name words: one
		// slot, however many words the name has.
		{"get-report monthly spend", ""},
		{"get-report", "report-name-or-id"},
	}
	for _, testCase := range cases {
		argv, err := splitCommandLine(testCase.argv)
		if err != nil {
			t.Fatalf("splitCommandLine(%q): %v", testCase.argv, err)
		}
		signature := aiPlaceholderSignatureFor(argv)
		if signature == nil {
			t.Fatalf("signature(%q) = nil", testCase.argv)
		}
		got := strings.Join(placeholderLabels(aiPlaceholdersRemaining(signature, argv)), " · ")
		if got != testCase.want {
			t.Fatalf("remaining(%q) = %q, want %q", testCase.argv, got, testCase.want)
		}
	}
	if remaining := aiPlaceholdersRemaining(nil, nil); remaining != nil {
		t.Fatalf("nil signature remaining = %v", remaining)
	}
}

func TestAIGhostText(t *testing.T) {
	remaining := []aiPlaceholder{{label: "ticketid"}, {label: "tags*: [a, b]", body: true, name: "tags"}}
	if got := aiGhostText(remaining, false, false, 80); got != "ticketid tags*: [a, b]" {
		t.Fatalf("ghost = %q", got)
	}
	if got := aiGhostText(remaining, true, false, 80); got != "ticketid tags*: [a, b] …" {
		t.Fatalf("ghost with ellipsis = %q", got)
	}
	if got := aiGhostText(remaining, false, false, 12); got != "ticketid ta…" {
		t.Fatalf("trimmed ghost = %q", got)
	}
	if got := aiGhostText(remaining, false, false, 3); got != "" {
		t.Fatalf("ghost under the legibility floor = %q", got)
	}
	if got := aiGhostText(nil, true, true, 80); got != "" {
		t.Fatalf("ghost with nothing remaining = %q", got)
	}
	pickable := []aiPlaceholder{{label: "report-name-or-id", pickable: true}}
	if got := aiGhostText(pickable, false, true, 80); got != "report-name-or-id "+aiPickerCue {
		t.Fatalf("ghost with the picker cue = %q", got)
	}
	// The cue sits at the tail, so a narrow pane trims it before the
	// argument name.
	if got := aiGhostText(pickable, false, true, 20); got != "report-name-or-id (…" {
		t.Fatalf("trimmed cue ghost = %q", got)
	}
}

func TestAIPickerCueApplies(t *testing.T) {
	placeholderTestTree(t)
	cue := func(line string) bool {
		t.Helper()
		argv, err := splitCommandLine(line)
		if err != nil {
			t.Fatalf("splitCommandLine(%q): %v", line, err)
		}
		signature := aiPlaceholderSignatureFor(argv)
		return aiPickerCueApplies(aiPlaceholdersRemaining(signature, argv), argv)
	}
	if !cue("get-report") {
		t.Fatal("no cue on the zero-argument pickable command")
	}
	if cue("get-report --id") {
		t.Fatal("cue despite --id suppressing the picker")
	}
	if cue("add-ticket-tags") {
		t.Fatal("cue on a non-resolvable command")
	}
	if cue("beta run-report") {
		t.Fatal("cue on a bodied resolvable command — the zero-argument picker never opens there")
	}
	t.Setenv("DCI_NO_RESOLVE", "1")
	if cue("get-report") {
		t.Fatal("cue despite DCI_NO_RESOLVE suppressing the picker")
	}
}

func TestAISketchLabel(t *testing.T) {
	cases := []struct {
		field bodyFieldSketch
		want  string
	}{
		// Arrays render as the literal syntax to type, not type notation —
		// "[string]" read as an annotation, so users typed bare strings
		// (dogfood, 2026-08-29).
		{bodyFieldSketch{sketch: "[", elem: "string"}, "[a, b]"},
		{bodyFieldSketch{sketch: "[", elem: "integer"}, "[1, 2]"},
		{bodyFieldSketch{sketch: "[", elem: "boolean"}, "[true, false]"},
		{bodyFieldSketch{sketch: "[", elem: "{"}, "[…]"},
		{bodyFieldSketch{sketch: "["}, "[…]"},
		{bodyFieldSketch{sketch: "[<any>]"}, "[…]"},
		{bodyFieldSketch{sketch: "{"}, "object"},
		{bodyFieldSketch{sketch: "(object)"}, "object"},
		{bodyFieldSketch{sketch: "(string minLen:1) The name"}, "string"},
		{bodyFieldSketch{sketch: "(integer|null)"}, "integer"},
		{bodyFieldSketch{sketch: "string"}, "string"},
		{bodyFieldSketch{sketch: ""}, ""},
	}
	for _, testCase := range cases {
		if got := aiSketchLabel(testCase.field); got != testCase.want {
			t.Fatalf("label(%q, elem %q) = %q, want %q", testCase.field.sketch, testCase.field.elem, got, testCase.want)
		}
	}
}

func TestAIValueGhost(t *testing.T) {
	placeholderTestTree(t)
	cases := []struct {
		input string // the session line, "/" included, spacing preserved
		want  string
	}{
		// A bare field prefix ghosts the value's literal syntax — the moment
		// the old ghost went silent right when the shape needed teaching.
		{"/add-ticket-tags 318240 tags:", " [a, b]"},
		{"/add-ticket-tags 318240 tags: ", "[a, b]"},
		// Inside an unclosed array: items after the opener, then the closing
		// guidance — silence returns only once the bracket closes.
		{"/add-ticket-tags 318240 tags: [", "a, b]"},
		{"/add-ticket-tags 318240 tags: [prod", ", …]"},
		{"/add-ticket-tags 318240 tags: [prod,", " …]"},
		{"/add-ticket-tags 318240 tags: [prod, ", "…]"},
		{"/add-ticket-tags 318240 tags: [prod, billing]", ""},
		// Objects get their own brackets; scalars their type word.
		{"/create-thing config:", " {…}"},
		{"/create-thing config: {", "…}"},
		{"/create-thing config: {currency: USD}", ""},
		{"/create-thing name:", " string"},
		// A dotted path's leaf type is unknown up here: no value ghost.
		{"/create-thing config.currency:", ""},
		// Compact shorthand keeps the RIGHT field's syntax across boundaries
		// inside one token (PR #140 review: the stale field ghosted "…]").
		{"/create-thing name:hi,labels:[", "a, b]"},
		{"/create-thing name:hi,labels:", " [a, b]"},
		{"/create-thing labels:[a],name:", " string"},
		// Not value entry: values typed through, whole-body tokens, unfilled
		// path slots (positional order wins — "tags:" is the path argument).
		{"/add-ticket-tags 318240 tags: a, b", ""},
		{"/add-ticket-tags 318240 @body.json", ""},
		{"/add-ticket-tags tags:", ""},
		{"/get-report", ""},
	}
	for _, testCase := range cases {
		trimmed := strings.TrimSpace(testCase.input)
		argv, err := splitCommandLine(strings.TrimPrefix(trimmed, "/"))
		if err != nil {
			t.Fatalf("splitCommandLine(%q): %v", testCase.input, err)
		}
		signature := aiPlaceholderSignatureFor(argv)
		if got := aiValueGhost(signature, argv, testCase.input); got != testCase.want {
			t.Fatalf("valueGhost(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

func TestAIFieldExampleExcerpt(t *testing.T) {
	example := "/create-x 1 tags: [prod, dev], config: {currency: USD}, note: hi"
	cases := []struct{ field, want string }{
		{"tags", "tags: [prod, dev]"},
		{"config", "config: {currency: USD}"},
		{"note", "note: hi"},
		{"missing", ""},
	}
	for _, testCase := range cases {
		if got := aiFieldExampleExcerpt(example, testCase.field); got != testCase.want {
			t.Fatalf("excerpt(%q) = %q, want %q", testCase.field, got, testCase.want)
		}
	}
	// The field name inside the command word is not an assignment.
	if got := aiFieldExampleExcerpt("/add-ticket-tags 1 tags: [a]", "tags"); got != "tags: [a]" {
		t.Fatalf("excerpt past the command word = %q", got)
	}
	if got := aiFieldExampleExcerpt("", "tags"); got != "" {
		t.Fatalf("excerpt of an empty example = %q", got)
	}
}

func TestAISpliceGhost(t *testing.T) {
	// The rendered input row: prompt, typed text, a styled cursor cell, and
	// the textarea's plain-space padding out to the frame width.
	const width = 40
	typed := "› /get-report "
	row := typed + "\x1b[7m \x1b[0m" + strings.Repeat(" ", width-len([]rune(typed))-1)
	keep := len([]rune(typed)) + 1 // prompt + text + the cursor's cell

	spliced := aiSpliceGhost(row, keep, "report-name-or-id", width)
	if !strings.Contains(spliced, "\x1b[7m") {
		t.Fatalf("splice dropped the cursor cell: %q", spliced)
	}
	// The exact plain-text row: typed text, the cursor's one cell, the ghost
	// right after it — the textarea's full-width padding gone.
	if got := stripANSI(spliced); got != typed+" report-name-or-id" {
		t.Fatalf("spliced row = %q, want the ghost right after the cursor cell", got)
	}

	// A ghost wider than the budget is trimmed with the cut marked.
	tight := aiSpliceGhost(row, width-8, "report-name-or-id", width)
	if got := stripANSI(tight); !strings.HasSuffix(got, "…") {
		t.Fatalf("overflowing ghost not trimmed: %q", got)
	}

	if got := aiSpliceGhost(row, keep, "", width); got != row {
		t.Fatalf("empty ghost changed the row: %q", got)
	}
	if got := aiSpliceGhost(row, width, "ghost", width); got != row {
		t.Fatalf("no budget still spliced: %q", got)
	}
}

func TestAIGhostFollowsTypingAndFreezesMidToken(t *testing.T) {
	placeholderTestTree(t)
	m := aiTestModel(t)

	m = aiType(m, "/add-ticket-tags ")
	if m.ghost != "ticketid tags*: [a, b]" {
		t.Fatalf("ghost after the command = %q", m.ghost)
	}
	m = aiType(m, "318240 ")
	if m.ghost != "tags*: [a, b]" {
		t.Fatalf("ghost after the path argument = %q", m.ghost)
	}

	// An open quote does not parse: the ghost freezes instead of erroring.
	m = aiType(m, `"pro`)
	if m.ghost != "tags*: [a, b]" {
		t.Fatalf("ghost mid-token = %q, want the frozen previous value", m.ghost)
	}
	m = aiType(m, `d" tags: x`)
	if m.ghost != "" {
		t.Fatalf("ghost after all placeholders consumed = %q", m.ghost)
	}
}

func TestAIGhostGates(t *testing.T) {
	placeholderTestTree(t)
	m := aiTestModel(t)

	// Session verbs and plain chat show no ghost.
	for _, line := range []string{"/model claude", "why did spend spike?"} {
		m.input.Reset()
		m = aiType(m, line)
		if m.ghost != "" {
			t.Fatalf("ghost for %q = %q, want none", line, m.ghost)
		}
	}

	// User-defined commands show no ghost even when they shadow nothing.
	m.userCommands = map[string]aiUserCommand{"top5": {Command: "list-reports --limit 5"}}
	m.input.Reset()
	m = aiType(m, "/top5 ")
	if m.ghost != "" {
		t.Fatalf("ghost for a user command = %q, want none", m.ghost)
	}

	// A pickable command's ghost carries the picker cue, so the
	// zero-argument list stops being a feature users find by accident.
	m.input.Reset()
	m = aiType(m, "/get-report")
	if m.ghost != "report-name-or-id "+aiPickerCue {
		t.Fatalf("pickable ghost = %q, want the picker cue appended", m.ghost)
	}

	// Esc clears the input and the ghost with it.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(aiModel)
	if m.ghost != "" {
		t.Fatalf("ghost survived Esc = %q", m.ghost)
	}
}

func TestAIGhostRendersInTheFrame(t *testing.T) {
	placeholderTestTree(t)
	m := aiTestModel(t)
	m = aiType(m, "/add-ticket-tags ")
	frame := stripANSI(m.View().Content)
	if !strings.Contains(frame, "/add-ticket-tags  ticketid tags*: [a, b]") {
		t.Fatalf("frame missing the ghost after the cursor cell:\n%s", frame)
	}

	// With the cursor moved off the end of the line, the ghost hides
	// rather than rendering under the edit point.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = updated.(aiModel)
	if frame := stripANSI(m.View().Content); strings.Contains(frame, "ticketid tags*") {
		t.Fatalf("ghost rendered during a mid-line edit:\n%s", frame)
	}
}

func TestAITabActionFor(t *testing.T) {
	placeholderTestTree(t)
	cases := []struct {
		input string
		kind  aiTabActionKind
		text  string // insert for aiTabInsert, hint for aiTabHint
	}{
		// The empty pickable slot submits so the picker opens (Tab = Enter).
		{"/get-report", aiTabPicker, ""},
		{"/get-report ", aiTabPicker, ""},
		// A gated pickable slot degrades to the value hint (type: no example).
		{"/get-report --id", aiTabHint, "report-name-or-id (string)"},
		// A plain path value slot hints with the spec example.
		{"/add-ticket-tags", aiTabHint, "ticketid — e.g. 318240"},
		// Path filled → insert the next required field's prefix, separator
		// depending on what the line already carries. An array field's
		// prefix brings its opening bracket along.
		{"/add-ticket-tags 318240", aiTabInsert, " tags: ["},
		{"/add-ticket-tags 318240 ", aiTabInsert, "tags: ["},
		{"/create-thing", aiTabInsert, " config: "},
		// Mid-body, shorthand properties are comma-separated.
		{"/create-widget a: 1", aiTabInsert, ", b: "},
		{"/create-widget a: 1,", aiTabInsert, " b: "},
		{"/create-widget a: 1, ", aiTabInsert, "b: "},
		// Mid-value, Tab offers the spec example's assignment instead of
		// jamming the next field's prefix into the value.
		{"/add-ticket-tags 318240 tags:", aiTabHint, "e.g. tags: [prod]"},
		{"/add-ticket-tags 318240 tags: [", aiTabHint, "e.g. tags: [prod]"},
		{"/add-ticket-tags 318240 tags: [prod", aiTabHint, "e.g. tags: [prod]"},
		// Mid-value with no spec example → inert (the value ghost already
		// shows the syntax).
		{"/create-thing config:", aiTabNone, ""},
		// Nothing to offer → Tab stays inert.
		{"/add-ticket-tags 318240 tags: prod", aiTabNone, ""},
		{"/model", aiTabNone, ""},
		{"/no-such-command", aiTabNone, ""},
		{"plain question", aiTabNone, ""},
		{`/add-ticket-tags "open`, aiTabNone, ""},
	}
	for _, testCase := range cases {
		action := aiTabActionFor(testCase.input, nil)
		if action.kind != testCase.kind {
			t.Fatalf("tab(%q) kind = %v, want %v", testCase.input, action.kind, testCase.kind)
		}
		if got := action.insert + action.hint; got != testCase.text {
			t.Fatalf("tab(%q) text = %q, want %q", testCase.input, got, testCase.text)
		}
	}
	// A user-defined command shadowing nothing still gets no tab action.
	if action := aiTabActionFor("/top5 ", map[string]aiUserCommand{"top5": {Command: "list-reports"}}); action.kind != aiTabNone {
		t.Fatalf("tab on a user command = %v, want none", action.kind)
	}
}

func TestAITabInsertsFieldPrefixInTheSession(t *testing.T) {
	placeholderTestTree(t)
	m := aiTestModel(t)
	m = aiType(m, "/add-ticket-tags 318240")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(aiModel)
	if got := m.input.Value(); got != "/add-ticket-tags 318240 tags: [" {
		t.Fatalf("input after Tab = %q", got)
	}
	// The prefix used to consume its placeholder and silence the ghost right
	// where the value's shape needed teaching; now the value ghost carries
	// the items and the closing bracket.
	if m.ghost != "a, b]" {
		t.Fatalf("ghost after the array prefix = %q", m.ghost)
	}
	m = aiType(m, "prod, billing]")
	if m.ghost != "" {
		t.Fatalf("ghost after the array closed = %q", m.ghost)
	}

	// On a value slot, Tab swaps the ghost for the hint and inserts nothing.
	m.input.Reset()
	m = aiType(m, "/add-ticket-tags ")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(aiModel)
	if got := m.input.Value(); got != "/add-ticket-tags " {
		t.Fatalf("value-slot Tab changed the input: %q", got)
	}
	if m.ghost != "ticketid — e.g. 318240" {
		t.Fatalf("value-slot Tab ghost = %q", m.ghost)
	}
	// The hint is transient: the next keystroke recomputes the normal ghost
	// (the typed digit starts the path value, consuming that slot).
	m = aiType(m, "3")
	if m.ghost != "tags*: [a, b]" {
		t.Fatalf("ghost after typing past the hint = %q", m.ghost)
	}
}

func TestAITabOnEmptyPickableSlotOpensThePickerPath(t *testing.T) {
	placeholderTestTree(t)
	m := aiTestModel(t)
	// The submit path routes against the completion catalog; the fixture
	// command must be in it or the dispatch is refused as unknown.
	m.catalog = append(m.catalog, aiCatalogEntry{Path: "get-report"})
	m = aiType(m, "/get-report ")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(aiModel)
	// No cached names in the test config dir: the submit path arms the
	// async fetch, exactly as Enter would.
	if m.fetchIntent == nil {
		t.Fatal("Tab on the empty pickable slot did not take the picker path")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input not submitted by Tab: %q", got)
	}
}
