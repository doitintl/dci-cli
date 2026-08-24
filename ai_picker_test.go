package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rest-sh/restish/cli"
)

// seedAIPickerFixtures installs a resolution index and a name cache for the
// given config dir, restoring globals afterwards.
func seedAIPickerFixtures(t *testing.T, configDir string) {
	t.Helper()
	oldIndex := resolutionIndex
	oldParams := operationPathParameters
	resolutionIndex = map[string]resolutionListTarget{
		"get-report":    {resource: "reports", listPath: "/reports", listOperation: "list-reports"},
		"create-report": {resource: "reports", listPath: "/reports", listOperation: "list-reports", hasBody: true},
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
	selection := aiNameSelectionFor([]string{"get-report", "--chart"}, dir)
	if selection == nil || len(selection.candidates) != 3 || len(selection.positionals) != 0 {
		t.Fatalf("zero-arg selection = %+v", selection)
	}
	if selection.resource != "report" {
		t.Fatalf("resource = %q", selection.resource)
	}

	// Ambiguous name: only the matching candidates.
	selection = aiNameSelectionFor([]string{"get-report", "bigquery", "--chart"}, dir)
	if selection == nil || len(selection.candidates) != 2 {
		t.Fatalf("ambiguous selection = %+v", selection)
	}

	// Unique match: the child resolves it identically — no picker.
	if s := aiNameSelectionFor([]string{"get-report", "Monthly AWS Spend"}, dir); s != nil {
		t.Fatalf("unique match opened a picker: %+v", s)
	}
	// Multi-word split name matching several entries still picks.
	if s := aiNameSelectionFor([]string{"get-report", "BigQuery", "Storage"}, dir); s == nil || len(s.candidates) != 2 {
		t.Fatalf("multi-word ambiguous selection = %+v", s)
	}
	// Non-resolvable command: never.
	if s := aiNameSelectionFor([]string{"status"}, dir); s != nil {
		t.Fatalf("non-resolvable command picked: %+v", s)
	}
	// --id opts out; ID-shaped input skips.
	if s := aiNameSelectionFor([]string{"get-report", "--id", "bigquery"}, dir); s != nil {
		t.Fatal("--id did not opt out")
	}
	if s := aiNameSelectionFor([]string{"get-report", "AAAAAAAAAAAAAAAAAAA1"}, dir); s != nil {
		t.Fatal("ID-shaped argument opened a picker")
	}
	// Body-taking operations keep their usage error on zero args.
	if s := aiNameSelectionFor([]string{"create-report"}, dir); s != nil {
		t.Fatal("hasBody operation opened the zero-arg picker")
	}
	// DCI_NO_RESOLVE opts the whole feature out.
	t.Setenv("DCI_NO_RESOLVE", "1")
	if s := aiNameSelectionFor([]string{"get-report"}, dir); s != nil {
		t.Fatal("DCI_NO_RESOLVE ignored")
	}
	t.Setenv("DCI_NO_RESOLVE", "")

	// Empty cache: dispatch as today.
	if s := aiNameSelectionFor([]string{"get-report"}, t.TempDir()); s != nil {
		t.Fatal("empty cache opened a picker")
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
	got := aiPositionalIndexes([]string{"get-report", "--chart", "bigquery"}, nil)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("indexes = %v", got)
	}
	if got := aiPositionalIndexes([]string{"get-report", "--", "--not-a-flag"}, nil); len(got) != 1 || got[0] != 2 {
		t.Fatalf("post -- indexes = %v", got)
	}
	if got := aiPositionalIndexes([]string{"get-report", "--output=json"}, nil); len(got) != 0 {
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
	if !strings.Contains(m.View(), "Select a report") {
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
	m, _ = aiPress(m, tea.KeyEsc)
	if m.picker != nil || m.running != nil {
		t.Fatal("esc did not cancel the picker")
	}
	if !strings.Contains(aiTranscriptText(m), "selection canceled") {
		t.Fatal("cancel note missing from the transcript")
	}
}
