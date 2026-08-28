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
		Use:  "add-ticket-tags ticketid",
		Long: "Add tags.\n\n## Request Schema (application/json)\n\n```schema\n{\n  tags*: [string]\n}\n```\n",
	}
	ticketTags.Flags().String("note", "", "")
	ticketTags.Flags().Bool("force", false, "")
	api.AddCommand(ticketTags)
	api.AddCommand(&cobra.Command{
		Use:  "create-thing",
		Long: "Create.\n\n## Request Schema (application/json)\n\n```schema\n{\n  config*: {\n    currency: string\n  }\n  name: string\n}\n```\n",
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
	t.Cleanup(func() { cli.Root, resolutionIndex = oldRoot, oldIndex })
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
		{"add-ticket-tags", "ticketid · tags*: [string]", 1, false},
		{"add-ticket-tags 318240 tags: a", "ticketid · tags*: [string]", 1, false},
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
		{"add-ticket-tags", "ticketid · tags*: [string]"},
		{"add-ticket-tags 318240", "tags*: [string]"},
		{"add-ticket-tags 318240 tags: a, b", ""},
		// Flags and their values are skipped; bool flags take no value.
		{"add-ticket-tags --note x 318240", "tags*: [string]"},
		{"add-ticket-tags --force 318240", "tags*: [string]"},
		{"add-ticket-tags --note=x", "ticketid · tags*: [string]"},
		// Whole-body tokens consume every body placeholder.
		{"add-ticket-tags 318240 @body.json", ""},
		{"add-ticket-tags 318240 <body.json", ""},
		// Inline JSON (shell-quoted, as the splitter delivers it) consumes
		// the fields it names, nothing else.
		{`add-ticket-tags 318240 '{"tags":["a"]}'`, ""},
		{`add-ticket-tags 318240 '{"other":1}'`, "tags*: [string]"},
		// Positional order wins: a body-shaped token still fills an open
		// path slot, exactly as the CLI would parse it.
		{"add-ticket-tags tags: a", "tags*: [string]"},
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
	remaining := []aiPlaceholder{{label: "ticketid"}, {label: "tags*: [string]", body: true, name: "tags"}}
	if got := aiGhostText(remaining, false, 80); got != "ticketid tags*: [string]" {
		t.Fatalf("ghost = %q", got)
	}
	if got := aiGhostText(remaining, true, 80); got != "ticketid tags*: [string] …" {
		t.Fatalf("ghost with ellipsis = %q", got)
	}
	if got := aiGhostText(remaining, false, 12); got != "ticketid ta…" {
		t.Fatalf("trimmed ghost = %q", got)
	}
	if got := aiGhostText(remaining, false, 3); got != "" {
		t.Fatalf("ghost under the legibility floor = %q", got)
	}
	if got := aiGhostText(nil, true, 80); got != "" {
		t.Fatalf("ghost with nothing remaining = %q", got)
	}
}

func TestAINormalizeSchemaSketch(t *testing.T) {
	cases := map[string]string{"{": "object", "[": "[…]", "[{": "[…]", "[string]": "[string]", "string": "string", "": ""}
	for raw, want := range cases {
		if got := aiNormalizeSchemaSketch(raw); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", raw, got, want)
		}
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
	if m.ghost != "ticketid tags*: [string]" {
		t.Fatalf("ghost after the command = %q", m.ghost)
	}
	m = aiType(m, "318240 ")
	if m.ghost != "tags*: [string]" {
		t.Fatalf("ghost after the path argument = %q", m.ghost)
	}

	// An open quote does not parse: the ghost freezes instead of erroring.
	m = aiType(m, `"pro`)
	if m.ghost != "tags*: [string]" {
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

	// Esc clears the input and the ghost with it.
	m.input.Reset()
	m = aiType(m, "/get-report")
	if m.ghost == "" {
		t.Fatal("no ghost for a resolvable command")
	}
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
	if !strings.Contains(frame, "/add-ticket-tags  ticketid tags*: [string]") {
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
