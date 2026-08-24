package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		want    []string
		wantErr bool
	}{
		{name: "simple", line: "list-budgets", want: []string{"list-budgets"}},
		{name: "flags", line: "list-anomalies --filter x -D acme.com", want: []string{"list-anomalies", "--filter", "x", "-D", "acme.com"}},
		{name: "collapsed whitespace", line: "  a \t b  ", want: []string{"a", "b"}},
		{name: "single quotes", line: `get-report 'My Monthly Report'`, want: []string{"get-report", "My Monthly Report"}},
		{name: "double quotes with escape", line: `q "say \"hi\" now"`, want: []string{"q", `say "hi" now`}},
		{name: "backslash escapes space", line: `get-report My\ Report`, want: []string{"get-report", "My Report"}},
		{name: "empty quotes make empty arg", line: `cmd ''`, want: []string{"cmd", ""}},
		{name: "empty", line: "", want: nil},
		{name: "unterminated single", line: "cmd 'oops", wantErr: true},
		{name: "unterminated double", line: `cmd "oops`, wantErr: true},
		{name: "trailing backslash", line: `cmd oops\`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitCommandLine(tc.line)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitCommandLine(%q) = %v, want error", tc.line, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitCommandLine(%q) error: %v", tc.line, err)
			}
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("splitCommandLine(%q) = %#v, want %#v", tc.line, got, tc.want)
			}
		})
	}
}

func aiTestCatalog() []aiCatalogEntry {
	return []aiCatalogEntry{
		{Path: "customer-context set", Summary: "Set the default customerContext"},
		{Path: "customer-context show", Summary: "Show the current default customerContext"},
		{Path: "list-anomalies", Summary: "List anomalies"},
		{Path: "list-budgets", Summary: "List budgets"},
		{Path: "status", Summary: "Show CLI status"},
	}
}

func TestAIRouteLine(t *testing.T) {
	catalog := aiTestCatalog()

	if route := aiRouteLine("   ", catalog, nil); route.kind != aiRouteEmpty {
		t.Fatalf("blank line routed as %v, want empty", route.kind)
	}
	if route := aiRouteLine("why did spend spike?", catalog, nil); route.kind != aiRouteChat || route.text != "why did spend spike?" {
		t.Fatalf("plain text routed as %+v, want chat", route)
	}
	if route := aiRouteLine("/help", catalog, nil); route.kind != aiRouteVerb || route.verb != "help" {
		t.Fatalf("/help routed as %+v, want verb help", route)
	}
	if route := aiRouteLine("/exit", catalog, nil); route.kind != aiRouteVerb || route.verb != "quit" {
		t.Fatalf("/exit routed as %+v, want verb quit (alias)", route)
	}
	// Bare exit/quit leave the session; anything longer is a question.
	for _, line := range []string{"exit", "quit", "EXIT", " exit "} {
		if route := aiRouteLine(line, catalog, nil); route.kind != aiRouteVerb || route.verb != "quit" {
			t.Fatalf("%q routed as %+v, want verb quit", line, route)
		}
	}
	if route := aiRouteLine("exit strategy for GKE?", catalog, nil); route.kind != aiRouteChat {
		t.Fatalf("question starting with exit routed as %+v, want chat", route)
	}
	if route := aiRouteLine("/customer acme.com", catalog, nil); route.kind != aiRouteVerb || route.verb != "customer" || len(route.args) != 1 || route.args[0] != "acme.com" {
		t.Fatalf("/customer acme.com routed as %+v", route)
	}
	route := aiRouteLine("/list-budgets --output json", catalog, nil)
	if route.kind != aiRouteDispatch || strings.Join(route.argv, " ") != "list-budgets --output json" {
		t.Fatalf("/list-budgets routed as %+v, want dispatch", route)
	}
	// Multi-token catalog paths dispatch on their first token.
	if route := aiRouteLine("/customer-context show", catalog, nil); route.kind != aiRouteDispatch {
		t.Fatalf("/customer-context show routed as %+v, want dispatch", route)
	}
	// Unknown token with a catalog present: suggestions, never dispatch.
	route = aiRouteLine("/lst-budgets", catalog, nil)
	if route.kind != aiRouteUnknown {
		t.Fatalf("/lst-budgets routed as %+v, want unknown", route)
	}
	if len(route.suggestions) == 0 || route.suggestions[0] != "list-budgets" {
		t.Fatalf("suggestions = %v, want list-budgets first", route.suggestions)
	}
	// Empty catalog (no cached spec yet): dispatch optimistically.
	if route := aiRouteLine("/list-budgets", nil, nil); route.kind != aiRouteDispatch {
		t.Fatalf("empty-catalog dispatch routed as %+v", route)
	}
	if route := aiRouteLine(`/get-report "oops`, catalog, nil); route.kind != aiRouteInvalid {
		t.Fatalf("unterminated quote routed as %+v, want invalid", route)
	}
	if route := aiRouteLine("/", catalog, nil); route.kind != aiRouteEmpty {
		t.Fatalf("bare slash routed as %+v, want empty", route)
	}
}

func TestAISuggestionsPrefixBeforeSubstringAndCapped(t *testing.T) {
	catalog := []aiCatalogEntry{
		{Path: "get-budget"}, {Path: "list-budget-alerts"}, {Path: "list-budgets"},
		{Path: "list-reports"}, {Path: "update-budget"}, {Path: "delete-budget"},
	}
	got := aiSuggestions(catalog, "list-b", 3)
	if len(got) != 2 || got[0] != "list-budget-alerts" || got[1] != "list-budgets" {
		t.Fatalf("suggestions = %v", got)
	}
	got = aiSuggestions(catalog, "budget", 3)
	if len(got) != 3 {
		t.Fatalf("cap not applied: %v", got)
	}
}

func TestAICompletionsFor(t *testing.T) {
	catalog := aiTestCatalog()

	if got := aiCompletionsFor("list", catalog, nil, 6); got != nil {
		t.Fatalf("non-slash input completed: %v", got)
	}
	if got := aiCompletionsFor("/list-budgets --out", catalog, nil, 6); got != nil {
		t.Fatalf("post-token input completed: %v", got)
	}
	got := aiCompletionsFor("/", catalog, nil, 20)
	if len(got) != len(aiSessionVerbs)+len(catalog) {
		t.Fatalf("bare slash offers %d candidates, want %d", len(got), len(aiSessionVerbs)+len(catalog))
	}
	if got[0].Value != aiSessionVerbs[0].name {
		t.Fatalf("session verbs must list first, got %q", got[0].Value)
	}
	got = aiCompletionsFor("/LI", catalog, nil, 6)
	if len(got) != 2 || got[0].Value != "list-anomalies" || got[1].Value != "list-budgets" {
		t.Fatalf("case-insensitive prefix completion = %v", got)
	}
	got = aiCompletionsFor("/budg", catalog, nil, 6)
	if len(got) != 1 || got[0].Value != "list-budgets" {
		t.Fatalf("substring completion = %v", got)
	}
	if got := aiCompletionsFor("/xyz", catalog, nil, 6); len(got) != 0 {
		t.Fatalf("no-match completion = %v", got)
	}
	if got := aiCompletionsFor("/", catalog, nil, 3); len(got) != 3 {
		t.Fatalf("limit not applied: %v", got)
	}
}

func TestAIHistoryRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if history := loadAIHistory(dir); len(history) != 0 {
		t.Fatalf("missing file loaded as %v", history)
	}

	history := loadAIHistory(dir)
	history = appendAIHistory(dir, history, "/status")
	history = appendAIHistory(dir, history, "/status") // consecutive duplicate: dropped
	history = appendAIHistory(dir, history, "/list-budgets")
	history = appendAIHistory(dir, history, "   ") // blank: dropped
	if len(history) != 2 {
		t.Fatalf("in-memory history = %v", history)
	}

	reloaded := loadAIHistory(dir)
	if len(reloaded) != 2 || reloaded[0] != "/status" || reloaded[1] != "/list-budgets" {
		t.Fatalf("reloaded history = %v", reloaded)
	}

	info, err := os.Stat(aiHistoryPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("history file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestAIHistoryLoadCapsAtMax(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < aiHistoryMax+50; i++ {
		b.WriteString("/status\n/list-budgets\n")
	}
	if err := os.WriteFile(aiHistoryPath(dir), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if history := loadAIHistory(dir); len(history) != aiHistoryMax {
		t.Fatalf("loaded %d entries, want cap %d", len(history), aiHistoryMax)
	}
}

func TestAIHandleCustomer(t *testing.T) {
	dir := t.TempDir()

	message, err := aiHandleCustomer(dir, nil)
	if err != nil || !strings.Contains(message, "not set") {
		t.Fatalf("unset context: %q, %v", message, err)
	}

	message, err = aiHandleCustomer(dir, []string{"acme.com"})
	if err != nil || !strings.Contains(message, "acme.com") {
		t.Fatalf("set context: %q, %v", message, err)
	}
	data, err := os.ReadFile(customerContextPath(dir))
	if err != nil || string(data) != "acme.com\n" {
		t.Fatalf("persisted context = %q, %v", data, err)
	}

	message, err = aiHandleCustomer(dir, nil)
	if err != nil || !strings.Contains(message, "acme.com") {
		t.Fatalf("show context: %q, %v", message, err)
	}

	if _, err := aiHandleCustomer(dir, []string{"x"}); err == nil {
		t.Fatal("invalid token accepted")
	}
	if _, err := aiHandleCustomer(dir, []string{"a", "b"}); err == nil {
		t.Fatal("two arguments accepted")
	}
}

func TestAIHistoryPathStaysInConfigDir(t *testing.T) {
	if got := aiHistoryPath("/cfg"); got != filepath.Join("/cfg", aiHistoryFileName) {
		t.Fatalf("history path = %q", got)
	}
}
