package main

import (
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

// TestPreflightLocalDCICommandChecksHelpBeforeMaxResultsCap guards a PR
// review finding: preflightAPIInvocation checks --help before running any
// flag validation (help always renders, regardless of other flag values),
// and preflightLocalDCICommand had inverted that order — an agent probing
// `budgets-at-risk --max-results 99999 --help` to learn valid ranges got an
// opaque cap error instead of the help text it asked for.
func TestPreflightLocalDCICommandChecksHelpBeforeMaxResultsCap(t *testing.T) {
	previousCredentials := invocationCredentialsAvailable
	invocationCredentialsAvailable = func() bool { return false }
	t.Cleanup(func() { invocationCredentialsAvailable = previousCredentials })

	if err := preflightLocalDCICommand("budgets-at-risk", []string{"dci", "dci", "budgets-at-risk", "--max-results", "99999", "--help"}); err != nil {
		t.Fatalf("help invocation with an over-cap flag rejected: %v", err)
	}
}

// TestPreflightLocalDCICommandEnforcesAllAndSearchFlagConflicts guards a PR
// review finding: preflightLocalDCICommand only ran validateMaxResults,
// skipping validateAllPagesFlags/validateSearchFlags — so
// `budgets-at-risk --all --max-results 10` was accepted where the
// equivalent `list-budgets --all --max-results 10` is rejected. --all and
// --search are persistent dciCmd flags every question command inherits.
func TestPreflightLocalDCICommandEnforcesAllAndSearchFlagConflicts(t *testing.T) {
	previousCredentials := invocationCredentialsAvailable
	invocationCredentialsAvailable = func() bool { return true }
	t.Cleanup(func() { invocationCredentialsAvailable = previousCredentials })

	cases := []struct {
		name string
		args []string
	}{
		{"all with max-results", []string{"dci", "dci", "budgets-at-risk", "--all", "--max-results", "10"}},
		{"all with page-token", []string{"dci", "dci", "budgets-at-risk", "--all", "--page-token", "tok"}},
		{"search with max-results", []string{"dci", "dci", "anomalies-recent", "--search", "foo", "--max-results", "10"}},
		{"search with page-token", []string{"dci", "dci", "anomalies-recent", "--search", "foo", "--page-token", "tok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commandName := tc.args[2]
			err := preflightLocalDCICommand(commandName, tc.args)
			if err == nil {
				t.Fatal("conflicting flag combination accepted")
			}
			if err.(invocationPreflightError).StructuredError().Code != "USAGE_ERROR" {
				t.Fatalf("error = %#v", err)
			}
		})
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

// TestQuestionCommandWrappedOperationMapsToRealGACommandNames guards a PR
// review finding: every response-presentation feature keyed on
// invokedCommandName (list_views.go's curated views, charts.go's
// utilization-bar column, response_transform.go's UTC-label columns for
// anomaly usage windows) switches on the literal wrapped-operation name
// ("list-budgets", "list-anomalies"). Without this map, main.go's
// PersistentPreRunE would set invokedCommandName to the question command's
// own name and every one of those features would silently no-op — for
// anomalies-recent specifically, that meant startTime/endTime rendered in
// the viewer's local zone instead of staying UTC labels, mis-dating usage-
// window boundaries near midnight.
func TestQuestionCommandWrappedOperationMapsToRealGACommandNames(t *testing.T) {
	cases := map[string]string{
		"budgets-at-risk":  "list-budgets",
		"anomalies-recent": "list-anomalies",
	}
	for command, want := range cases {
		if got := questionCommandWrappedOperation[command]; got != want {
			t.Errorf("questionCommandWrappedOperation[%q] = %q, want %q", command, got, want)
		}
	}
	if _, ok := questionCommandWrappedOperation["list-budgets"]; ok {
		t.Error("a real GA command name should not itself be a map key")
	}
}

// TestPersistentPreRunEResolvesQuestionCommandToWrappedOperationName drives
// the real dciCmd.PersistentPreRunE (mirroring TestOutputFlagValidation's
// setup) to confirm invokedCommandName ends up as the wrapped GA operation
// name, not the question command's own name, after a real Execute().
func TestPersistentPreRunEResolvesQuestionCommandToWrappedOperationName(t *testing.T) {
	oldRoot := cli.Root
	oldInvokedCommandName := invokedCommandName
	previousLoadOperationAPI := loadOperationAPI
	loadOperationAPI = func(base string, root *cobra.Command) (cli.API, error) {
		return cli.API{}, errors.New("no network in this test")
	}
	t.Cleanup(func() {
		cli.Root = oldRoot
		invokedCommandName = oldInvokedCommandName
		loadOperationAPI = previousLoadOperationAPI
		viper.Reset()
	})
	stubDestructiveMetadata(t)

	root := &cobra.Command{Use: "dci"}
	dciCmd := &cobra.Command{Use: "dci"}
	root.AddCommand(dciCmd)
	dciCmd.AddCommand(newBudgetsAtRiskCommand())
	cli.Root = root
	addOutputFlag()

	root.SetArgs([]string{"dci", "budgets-at-risk"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	// findGAOperation fails fast on the stubbed loader; the PersistentPreRunE
	// assignment under test has already run by the time RunE gets there, and
	// the resulting error is irrelevant here.
	_ = root.Execute()

	if invokedCommandName != "list-budgets" {
		t.Fatalf("invokedCommandName = %q, want %q", invokedCommandName, "list-budgets")
	}
}
