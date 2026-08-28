package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rest-sh/restish/cli"
)

// seedAIPickerFixtures installs a resolution index and a name cache for the
// given config dir, restoring globals afterwards.
func seedAIPickerFixtures(t *testing.T, configDir string) {
	t.Helper()
	oldIndex := resolutionIndex
	oldParams := operationPathParameters
	resolutionIndex = map[string]resolutionListTarget{
		"get-report":      {resource: "reports", listPath: "/reports", listOperation: "list-reports"},
		"create-report":   {resource: "reports", listPath: "/reports", listOperation: "list-reports", hasBody: true},
		"beta run-report": {resource: "reports", listPath: "/reports", listOperation: "list-reports"},
	}
	operationPathParameters = map[string][]*cli.Param{
		"get-report": {{Name: "id", Type: "string"}},
	}
	t.Cleanup(func() {
		resolutionIndex = oldIndex
		operationPathParameters = oldParams
	})
	cache := nameCacheFile{
		Version:   nameCacheVersion,
		FetchedAt: time.Now(),
		Resources: map[string][]nameCacheEntry{
			"reports": {
				{ID: "AAAAAAAAAAAAAAAAAAA1", Name: "BigQuery Storage costs"},
				{ID: "AAAAAAAAAAAAAAAAAAA2", Name: "BigQuery Storage type"},
				{ID: "AAAAAAAAAAAAAAAAAAA3", Name: "Monthly AWS Spend"},
			},
		},
	}
	if err := writeNameCache(configDir, cache); err != nil {
		t.Fatal(err)
	}
}

func TestAINameSelectionFor(t *testing.T) {
	dir := t.TempDir()
	seedAIPickerFixtures(t, dir)

	// Zero-argument resolvable command: pick from everything.
	selection := aiNameSelectionFor([]string{"get-report", "--chart"}, dir, readCustomerContext(dir))
	if selection == nil || len(selection.candidates) != 3 || len(selection.positionals) != 0 {
		t.Fatalf("zero-arg selection = %+v", selection)
	}
	if selection.resource != "report" {
		t.Fatalf("resource = %q", selection.resource)
	}

	// Ambiguous name: only the matching candidates.
	selection = aiNameSelectionFor([]string{"get-report", "bigquery", "--chart"}, dir, readCustomerContext(dir))
	if selection == nil || len(selection.candidates) != 2 {
		t.Fatalf("ambiguous selection = %+v", selection)
	}

	// Unique match: the child resolves it identically — no picker.
	if s := aiNameSelectionFor([]string{"get-report", "Monthly AWS Spend"}, dir, readCustomerContext(dir)); s != nil {
		t.Fatalf("unique match opened a picker: %+v", s)
	}
	// Multi-word split name matching several entries still picks.
	if s := aiNameSelectionFor([]string{"get-report", "BigQuery", "Storage"}, dir, readCustomerContext(dir)); s == nil || len(s.candidates) != 2 {
		t.Fatalf("multi-word ambiguous selection = %+v", s)
	}
	// Non-resolvable command: never.
	if s := aiNameSelectionFor([]string{"status"}, dir, readCustomerContext(dir)); s != nil {
		t.Fatalf("non-resolvable command picked: %+v", s)
	}
	// --id opts out; ID-shaped input skips.
	if s := aiNameSelectionFor([]string{"get-report", "--id", "bigquery"}, dir, readCustomerContext(dir)); s != nil {
		t.Fatal("--id did not opt out")
	}
	if s := aiNameSelectionFor([]string{"get-report", "AAAAAAAAAAAAAAAAAAA1"}, dir, readCustomerContext(dir)); s != nil {
		t.Fatal("ID-shaped argument opened a picker")
	}
	// Body-taking operations keep their usage error on zero args.
	if s := aiNameSelectionFor([]string{"create-report"}, dir, readCustomerContext(dir)); s != nil {
		t.Fatal("hasBody operation opened the zero-arg picker")
	}
	// DCI_NO_RESOLVE opts the whole feature out.
	t.Setenv("DCI_NO_RESOLVE", "1")
	if s := aiNameSelectionFor([]string{"get-report"}, dir, readCustomerContext(dir)); s != nil {
		t.Fatal("DCI_NO_RESOLVE ignored")
	}
	t.Setenv("DCI_NO_RESOLVE", "")

	// Empty cache: dispatch as today.
	if s := aiNameSelectionFor([]string{"get-report"}, t.TempDir(), ""); s != nil {
		t.Fatal("empty cache opened a picker")
	}
}

func TestAINameSelectionForBetaCommands(t *testing.T) {
	dir := t.TempDir()
	seedAIPickerFixtures(t, dir)

	// Zero-argument /beta run-report: pick from every report, like the GA
	// commands do.
	selection := aiNameSelectionFor([]string{"beta", "run-report"}, dir, readCustomerContext(dir))
	if selection == nil || len(selection.candidates) != 3 || len(selection.positionals) != 0 {
		t.Fatalf("beta zero-arg selection = %+v", selection)
	}
	entry := nameCacheEntry{ID: "AAAAAAAAAAAAAAAAAAA3", Name: "Monthly AWS Spend"}
	if got := strings.Join(selection.apply(entry), " "); got != "beta run-report AAAAAAAAAAAAAAAAAAA3" {
		t.Fatalf("beta apply = %q", got)
	}

	// An ambiguous name argument picks among the matches — the subcommand
	// word must not be misread as the name.
	selection = aiNameSelectionFor([]string{"beta", "run-report", "bigquery"}, dir, readCustomerContext(dir))
	if selection == nil || len(selection.candidates) != 2 {
		t.Fatalf("beta ambiguous selection = %+v", selection)
	}
	if len(selection.positionals) != 1 || selection.positionals[0] != 2 {
		t.Fatalf("beta positionals = %v", selection.positionals)
	}

	// Beta commands without a resolution target (operation IDs) dispatch
	// as typed.
	if s := aiNameSelectionFor([]string{"beta", "get-report-results"}, dir, readCustomerContext(dir)); s != nil {
		t.Fatalf("non-resolvable beta command picked: %+v", s)
	}
}

func TestAINameSelectionApply(t *testing.T) {
	entry := nameCacheEntry{ID: "AAAAAAAAAAAAAAAAAAA2", Name: "BigQuery Storage type"}

	appendCase := aiNameSelection{argv: []string{"get-report", "--chart"}}
	if got := strings.Join(appendCase.apply(entry), " "); got != "get-report --chart AAAAAAAAAAAAAAAAAAA2" {
		t.Fatalf("append apply = %q", got)
	}

	replaceCase := aiNameSelection{argv: []string{"get-report", "bigquery", "--chart"}, positionals: []int{1}}
	if got := strings.Join(replaceCase.apply(entry), " "); got != "get-report AAAAAAAAAAAAAAAAAAA2 --chart" {
		t.Fatalf("replace apply = %q", got)
	}

	multiWord := aiNameSelection{argv: []string{"get-report", "BigQuery", "Storage", "--chart"}, positionals: []int{1, 2}}
	if got := strings.Join(multiWord.apply(entry), " "); got != "get-report AAAAAAAAAAAAAAAAAAA2 --chart" {
		t.Fatalf("multi-word apply = %q", got)
	}
}

func TestAINameSelectionFiltered(t *testing.T) {
	selection := aiNameSelection{candidates: []nameCacheEntry{
		{ID: "AAAAAAAAAAAAAAAAAAA1", Name: "BigQuery Storage costs"},
		{ID: "AAAAAAAAAAAAAAAAAAA3", Name: "Monthly AWS Spend"},
	}}
	if got := selection.filtered("  "); len(got) != 2 {
		t.Fatalf("empty filter = %v", got)
	}
	got := selection.filtered("monthly")
	if len(got) != 1 || got[0].Name != "Monthly AWS Spend" {
		t.Fatalf("filter = %v", got)
	}
	if got := selection.filtered("zzz"); len(got) != 0 {
		t.Fatalf("no-match filter = %v", got)
	}
}

func TestAIPositionalIndexes(t *testing.T) {
	// Without a flag set, long flags never consume the next word.
	got := aiPositionalIndexes([]string{"get-report", "--chart", "bigquery"}, nil, 1)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("indexes = %v", got)
	}
	if got := aiPositionalIndexes([]string{"get-report", "--", "--not-a-flag"}, nil, 1); len(got) != 1 || got[0] != 2 {
		t.Fatalf("post -- indexes = %v", got)
	}
	if got := aiPositionalIndexes([]string{"get-report", "--output=json"}, nil, 1); len(got) != 0 {
		t.Fatalf("value-flag indexes = %v", got)
	}
}

func TestAIPickerFlowInSession(t *testing.T) {
	m := aiTestModel(t)
	seedAIPickerFixtures(t, m.configDir)
	m.catalog = append(m.catalog, aiCatalogEntry{Path: "get-report", Summary: "Get a report"})

	m = aiType(m, "/get-report bigquery")
	m, _ = aiPress(m, tea.KeyEnter)
	if m.picker == nil || m.running != nil {
		t.Fatalf("ambiguous dispatch did not open the picker (picker=%v running=%v)", m.picker != nil, m.running != nil)
	}
	if !strings.Contains(m.View().Content, "Select a report") {
		t.Fatal("picker not rendered")
	}
	if !strings.Contains(m.statusLine(), "enter run") {
		t.Fatalf("status = %q", m.statusLine())
	}

	// Filter down to one and run it.
	m = aiType(m, "type")
	m, _ = aiPress(m, tea.KeyEnter)
	if m.picker != nil {
		t.Fatal("picker still open after selection")
	}
	if m.running == nil {
		t.Fatal("selection did not dispatch")
	}
	if got := strings.Join(m.running.argv, " "); got != "get-report AAAAAAAAAAAAAAAAAAA2" {
		t.Fatalf("dispatched argv = %q", got)
	}
	m.running.cancel()
}

func TestAIPickerEscCancels(t *testing.T) {
	m := aiTestModel(t)
	seedAIPickerFixtures(t, m.configDir)
	m.catalog = append(m.catalog, aiCatalogEntry{Path: "get-report", Summary: "Get a report"})

	m = aiType(m, "/get-report")
	m, _ = aiPress(m, tea.KeyEnter)
	if m.picker == nil {
		t.Fatal("zero-arg dispatch did not open the picker")
	}
	m, _ = aiPress(m, tea.KeyEscape)
	if m.picker != nil || m.running != nil {
		t.Fatal("esc did not cancel the picker")
	}
	if !strings.Contains(aiTranscriptText(m), "selection canceled") {
		t.Fatal("cancel note missing from the transcript")
	}
}

func TestAIDispatchEnvForcesHumanMode(t *testing.T) {
	env := aiDispatchEnv(132, 40, "")
	for _, want := range []string{"DCI_NO_TUI=1", "DCI_AGENT_MODE=0", "DCI_SESSION_RENDER=1", "COLOR=1", "CLICOLOR_FORCE=1", "COLUMNS=132", "LINES=40"} {
		found := false
		for _, entry := range env {
			if entry == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("dispatch env missing %s", want)
		}
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "DCI_CUSTOMER_CONTEXT=") {
			t.Fatalf("no override set, but dispatch env carries %s", entry)
		}
	}

	// Before the first window-size message the model's height is 0 — the
	// child must not receive a fabricated LINES value. Pin the parent env so
	// an inherited LINES can't mask (or fake) the assertion.
	t.Setenv("LINES", "99")
	for _, entry := range aiDispatchEnv(132, 0, "") {
		if strings.HasPrefix(entry, "LINES=") && entry != "LINES=99" {
			t.Fatalf("zero height exported %s", entry)
		}
	}
}

func TestAIDispatchEnvCarriesSessionCustomer(t *testing.T) {
	// User dispatches must run against the agent's session-scoped tenant —
	// exactly once, with any inherited value replaced, since getenv semantics
	// on duplicate entries are platform-defined.
	t.Setenv("DCI_CUSTOMER_CONTEXT", "stale.example.com")
	env := aiDispatchEnv(132, 40, "csp.doit.com")
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "DCI_CUSTOMER_CONTEXT=") {
			count++
			if entry != "DCI_CUSTOMER_CONTEXT=csp.doit.com" {
				t.Fatalf("dispatch env context = %s", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("DCI_CUSTOMER_CONTEXT appears %d times, want exactly 1", count)
	}
}

func TestAIDispatchEnvInheritsOutputOrder(t *testing.T) {
	// The ordering setting reaches dispatch children through the inherited
	// environment (OUTPUT-ORDER-SPEC §6.3) — no dedicated plumbing.
	t.Setenv(outputOrderEnvName, "classic")
	env := aiDispatchEnv(132, 40, "")
	found := false
	for _, entry := range env {
		if entry == outputOrderEnvName+"=classic" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dispatch env dropped %s", outputOrderEnvName)
	}
}

func TestAIDispatchDestructiveApprovalFlow(t *testing.T) {
	m := aiTestModel(t)
	updated, _ := m.Update(aiCmdDoneMsg{
		argv:     []string{"delete-report", "AAAAAAAAAAAAAAAAAAA1"},
		output:   "Error: delete-report targets report \"prod\" (AAAAAAAAAAAAAAAAAAA1); re-run with --yes\n",
		exitCode: aiDestructiveExitCode,
		elapsed:  time.Second,
	})
	m = updated.(aiModel)
	if m.dispatchApproval == nil {
		t.Fatal("exit 30 did not open the dispatch approval")
	}
	if !strings.Contains(m.dispatchApproval.summary, "targets report") {
		t.Fatalf("summary = %q", m.dispatchApproval.summary)
	}
	if !strings.Contains(m.statusLine(), "destructive") {
		t.Fatalf("status = %q", m.statusLine())
	}
	// Stray keys are ignored; y re-runs with --yes.
	m = aiType(m, "x")
	if m.dispatchApproval == nil || m.input.Value() != "" {
		t.Fatal("stray key answered or typed during dispatch approval")
	}
	updated2, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = updated2.(aiModel)
	if m.dispatchApproval != nil || m.running == nil || cmd == nil {
		t.Fatal("y did not re-dispatch")
	}
	got := strings.Join(m.running.argv, " ")
	if got != "delete-report AAAAAAAAAAAAAAAAAAA1 --yes" {
		t.Fatalf("re-dispatch argv = %q", got)
	}
	m.running.cancel()
}

func TestAIDispatchDestructiveDeclined(t *testing.T) {
	m := aiTestModel(t)
	updated, _ := m.Update(aiCmdDoneMsg{argv: []string{"delete-report", "x"}, output: "Error: needs --yes", exitCode: aiDestructiveExitCode})
	m = updated.(aiModel)
	m, _ = aiPress(m, tea.KeyEscape)
	if m.dispatchApproval != nil || m.running != nil {
		t.Fatal("esc did not decline")
	}
	if !strings.Contains(aiTranscriptText(m), "declined") {
		t.Fatal("decline note missing")
	}
	// An approved run that still exits 30 renders as a card, never loops.
	updated2, _ := m.Update(aiCmdDoneMsg{argv: []string{"delete-report", "x", "--yes"}, output: "still blocked", exitCode: aiDestructiveExitCode})
	m = updated2.(aiModel)
	if m.dispatchApproval != nil {
		t.Fatal("--yes exit 30 reopened the approval")
	}
}

func TestAIBareQueryGetsBuilderNotice(t *testing.T) {
	m := aiTestModel(t)
	m.catalog = append(m.catalog, aiCatalogEntry{Path: "query", Summary: "Run an analytics query"})
	m = aiType(m, "/query")
	m, _ = aiPress(m, tea.KeyEnter)
	if m.running != nil {
		t.Fatal("bare /query dispatched a child that cannot run the builder")
	}
	if !strings.Contains(aiTranscriptText(m), "query builder needs a regular terminal") {
		t.Fatal("builder notice missing")
	}
}

func TestAIBetaPickerFlowInSession(t *testing.T) {
	m := aiTestModel(t)
	seedAIPickerFixtures(t, m.configDir)
	m.catalog = append(m.catalog,
		aiCatalogEntry{Path: "beta", Summary: "Early-access commands"},
		aiCatalogEntry{Path: "beta run-report", Summary: "(beta) Run a saved report asynchronously"},
	)

	m = aiType(m, "/beta run-report")
	m, _ = aiPress(m, tea.KeyEnter)
	if m.picker == nil || m.running != nil {
		t.Fatalf("beta zero-arg dispatch did not open the picker (picker=%v running=%v)", m.picker != nil, m.running != nil)
	}
	m = aiType(m, "monthly")
	m, _ = aiPress(m, tea.KeyEnter)
	if m.picker != nil || m.running == nil {
		t.Fatal("selection did not dispatch")
	}
	if got := strings.Join(m.running.argv, " "); got != "beta run-report AAAAAAAAAAAAAAAAAAA3" {
		t.Fatalf("dispatched argv = %q", got)
	}
	m.running.cancel()
}
