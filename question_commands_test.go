package main

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func TestLocalDCICommandRegistered(t *testing.T) {
	cases := map[string]bool{
		"budgets-at-risk":  true,
		"anomalies-recent": true,
		"beta":             false,
		"list-budgets":     false,
		"":                 false,
	}
	for name, want := range cases {
		if got := localDCICommandRegistered(name); got != want {
			t.Errorf("localDCICommandRegistered(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestPreflightLocalDCICommandHelpNeedsNoCredentials(t *testing.T) {
	previousCredentials := invocationCredentialsAvailable
	previousInteractive := invocationInteractive
	invocationCredentialsAvailable = func() bool { return false }
	invocationInteractive = func() bool { return false }
	t.Cleanup(func() {
		invocationCredentialsAvailable = previousCredentials
		invocationInteractive = previousInteractive
	})

	if err := preflightLocalDCICommand("budgets-at-risk", []string{"dci", "dci", "budgets-at-risk", "--help"}); err != nil {
		t.Fatalf("help invocation rejected: %v", err)
	}
}

func TestPreflightLocalDCICommandRejectsHeadlessAuthentication(t *testing.T) {
	previousCredentials := invocationCredentialsAvailable
	previousInteractive := invocationInteractive
	invocationCredentialsAvailable = func() bool { return false }
	invocationInteractive = func() bool { return false }
	t.Cleanup(func() {
		invocationCredentialsAvailable = previousCredentials
		invocationInteractive = previousInteractive
	})

	err := preflightLocalDCICommand("anomalies-recent", []string{"dci", "dci", "anomalies-recent"})
	if err == nil {
		t.Fatal("unauthenticated headless invocation accepted")
	}
	detail := err.(invocationPreflightError).StructuredError()
	if detail.Code != "AUTHENTICATION_REQUIRED" || err.(invocationPreflightError).ExitCode() != exitAuthentication {
		t.Fatalf("error = %#v", err)
	}
}

func TestPreflightLocalDCICommandAllowsDryRunWithoutCredentials(t *testing.T) {
	previousCredentials := invocationCredentialsAvailable
	previousInteractive := invocationInteractive
	invocationCredentialsAvailable = func() bool { return false }
	invocationInteractive = func() bool { return false }
	t.Cleanup(func() {
		invocationCredentialsAvailable = previousCredentials
		invocationInteractive = previousInteractive
	})

	if err := preflightLocalDCICommand("budgets-at-risk", []string{"dci", "dci", "budgets-at-risk", "--dry-run"}); err != nil {
		t.Fatalf("dry-run invocation rejected: %v", err)
	}
}

// TestPreflightLocalDCICommandEnforcesWrappedOperationMaxResultsCap guards
// the gap an adversarial review caught: budgets-at-risk wraps list-budgets
// (server cap 250, pagingCaps) but declares its own --max-results flag, so
// without this check an over-cap value would sail past validation and get
// silently clamped server-side instead of rejected client-side.
func TestPreflightLocalDCICommandEnforcesWrappedOperationMaxResultsCap(t *testing.T) {
	previousCredentials := invocationCredentialsAvailable
	invocationCredentialsAvailable = func() bool { return true }
	t.Cleanup(func() { invocationCredentialsAvailable = previousCredentials })

	err := preflightLocalDCICommand("budgets-at-risk", []string{"dci", "dci", "budgets-at-risk", "--max-results", "1000"})
	if err == nil {
		t.Fatal("over-cap --max-results accepted")
	}
	detail := err.(invocationPreflightError).StructuredError()
	if detail.Code != "USAGE_ERROR" || !strings.Contains(detail.Message, "250") {
		t.Fatalf("error = %#v", err)
	}

	// anomalies-recent wraps list-anomalies, which has no known cap: the same
	// value must pass through unchallenged.
	if err := preflightLocalDCICommand("anomalies-recent", []string{"dci", "dci", "anomalies-recent", "--max-results", "1000"}); err != nil {
		t.Fatalf("uncapped operation rejected: %v", err)
	}
}

// TestPreflightAPIInvocationRoutesQuestionCommandsAroundGASpecValidation
// guards the reason this preflight branch exists: budgets-at-risk and
// anomalies-recent are hand-registered local commands, not GA-spec
// operations, so validating them against the loaded operation catalog would
// always reject them as unknown.
func TestPreflightAPIInvocationRoutesQuestionCommandsAroundGASpecValidation(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "list-budgets"}}}
	configureInvocationPreflightTest(t, api, true, false, false)

	if err := preflightAPIInvocation([]string{"dci", "dci", "budgets-at-risk"}); err != nil {
		t.Fatalf("error = %v", err)
	}
}

func TestAIAPIOperationsHydratedIgnoresQuestionCommands(t *testing.T) {
	command := &cobra.Command{Use: "dci"}
	for _, name := range []string{"beta", "help", "budgets-at-risk", "anomalies-recent"} {
		command.AddCommand(&cobra.Command{Use: name, Run: func(*cobra.Command, []string) {}})
	}
	if aiAPIOperationsHydrated(command) {
		t.Fatal("question commands and beta/help alone should not count as hydrated")
	}

	command.AddCommand(&cobra.Command{Use: "list-budgets", Run: func(*cobra.Command, []string) {}})
	if !aiAPIOperationsHydrated(command) {
		t.Fatal("a real spec operation should count as hydrated")
	}
}

// TestQuestionCommandCatalogEntriesReportCorrectShape guards a bug found
// during manual verification: buildCommandCatalog's two generic loops (spec
// operations, and cli.Root's non-"dci" children) both miss budgets-at-risk
// and anomalies-recent, since they are hand-registered under the hidden dci
// command rather than GA-spec operations — the command_catalog.go call site
// wires questionCommandCatalogEntries in specifically to cover that gap.
func TestQuestionCommandCatalogEntriesReportCorrectShape(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	dciCommand := &cobra.Command{Use: "dci", Hidden: true}
	cli.Root.AddCommand(dciCommand)
	dciCommand.AddCommand(&cobra.Command{Use: "help"})
	registerQuestionCommands()
	t.Cleanup(func() { cli.Root = oldRoot })

	entries := questionCommandCatalogEntries()
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	for _, entry := range entries {
		if entry.OutputShape != "api_response" {
			t.Errorf("%v: output shape = %q, want api_response", entry.Path, entry.OutputShape)
		}
		if !entry.RequiresAuth {
			t.Errorf("%v: requires_auth = false, want true", entry.Path)
		}
		if entry.Destructive {
			t.Errorf("%v: destructive = true, want false", entry.Path)
		}
	}
}

func TestRecentWindowStart(t *testing.T) {
	before := time.Now().Add(-24 * time.Hour).UnixMilli()
	got, err := recentWindowStart("24h")
	if err != nil {
		t.Fatalf("recentWindowStart: %v", err)
	}
	after := time.Now().Add(-24 * time.Hour).UnixMilli()
	if got < before || got > after {
		t.Fatalf("recentWindowStart(24h) = %d, want between %d and %d", got, before, after)
	}

	if _, err := recentWindowStart("not-a-duration"); err == nil {
		t.Fatal("invalid window accepted")
	}
	if _, err := recentWindowStart("0h"); err == nil {
		t.Fatal("non-positive window accepted")
	}
	if _, err := recentWindowStart("-1h"); err == nil {
		t.Fatal("negative window accepted")
	}
}

func TestFindGAOperationMissingOperation(t *testing.T) {
	previous := loadOperationAPI
	loadOperationAPI = func(base string, root *cobra.Command) (cli.API, error) {
		return cli.API{Operations: []cli.Operation{{Name: "list-reports"}}}, nil
	}
	t.Cleanup(func() { loadOperationAPI = previous })

	if _, err := findGAOperation("list-budgets"); err == nil {
		t.Fatal("missing operation accepted")
	}
}

func TestQuestionRequestURIAppendsQueryParameters(t *testing.T) {
	operation := cli.Operation{URITemplate: "https://api.doit.com/analytics/v1/budgets"}
	query := url.Values{"filter": {"riskStatus:atRisk"}, "maxResults": {"5"}}

	got := questionRequestURI(operation, query)
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("questionRequestURI produced an unparseable URL %q: %v", got, err)
	}
	if parsed.Query().Get("filter") != "riskStatus:atRisk" || parsed.Query().Get("maxResults") != "5" {
		t.Fatalf("query = %v", parsed.Query())
	}
}

func TestQuestionRequestURIPreservesExistingQueryString(t *testing.T) {
	operation := cli.Operation{URITemplate: "https://api.doit.com/anomalies/v1?includeNotifications=false"}
	query := url.Values{"sortBy": {"startTime"}}

	got := questionRequestURI(operation, query)
	if !strings.Contains(got, "includeNotifications=false") || !strings.Contains(got, "sortBy=startTime") {
		t.Fatalf("uri = %q", got)
	}
	if strings.Count(got, "?") != 1 {
		t.Fatalf("uri has more than one query separator: %q", got)
	}
}

func TestBudgetsAtRiskQueryFixesRiskStatusFilter(t *testing.T) {
	command := newBudgetsAtRiskCommand()
	if err := command.Flags().Set("max-results", "10"); err != nil {
		t.Fatalf("set max-results: %v", err)
	}

	query := budgetsAtRiskQuery(command)
	if query.Get("filter") != "riskStatus:atRisk" {
		t.Fatalf("filter = %q, want riskStatus:atRisk", query.Get("filter"))
	}
	if query.Get("maxResults") != "10" {
		t.Fatalf("maxResults = %q, want 10", query.Get("maxResults"))
	}
}

func TestBudgetsAtRiskQueryOmitsUnsetPagingFlags(t *testing.T) {
	command := newBudgetsAtRiskCommand()

	query := budgetsAtRiskQuery(command)
	if _, ok := query["maxResults"]; ok {
		t.Fatalf("maxResults should be absent when unset, got %v", query)
	}
	if _, ok := query["pageToken"]; ok {
		t.Fatalf("pageToken should be absent when unset, got %v", query)
	}
}

func TestAnomaliesRecentQueryFixesSortAndWindow(t *testing.T) {
	command := newAnomaliesRecentCommand()
	if err := command.Flags().Set("window", "1h"); err != nil {
		t.Fatalf("set window: %v", err)
	}
	if err := command.Flags().Set("severity", "critical"); err != nil {
		t.Fatalf("set severity: %v", err)
	}

	before := time.Now().Add(-1 * time.Hour).UnixMilli()
	query, err := anomaliesRecentQuery(command)
	after := time.Now().Add(-1 * time.Hour).UnixMilli()
	if err != nil {
		t.Fatalf("anomaliesRecentQuery: %v", err)
	}
	if query.Get("sortBy") != "startTime" || query.Get("sortOrder") != "desc" {
		t.Fatalf("sort params = %v", query)
	}
	if query.Get("filter") != "severityLevel:critical" {
		t.Fatalf("filter = %q, want severityLevel:critical", query.Get("filter"))
	}
	minCreationTime, err := strconv.ParseInt(query.Get("minCreationTime"), 10, 64)
	if err != nil || minCreationTime < before || minCreationTime > after {
		t.Fatalf("minCreationTime = %q, want between %d and %d", query.Get("minCreationTime"), before, after)
	}
}

func TestAnomaliesRecentQueryRejectsInvalidWindow(t *testing.T) {
	command := newAnomaliesRecentCommand()
	if err := command.Flags().Set("window", "not-a-duration"); err != nil {
		t.Fatalf("set window: %v", err)
	}

	if _, err := anomaliesRecentQuery(command); err == nil {
		t.Fatal("invalid window accepted")
	}
}
