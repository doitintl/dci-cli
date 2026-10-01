package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandSearchTokenizeSplitsPunctuationAndCamelCase(t *testing.T) {
	got := commandSearchTokenize("dci list-budgets --filter riskStatus:atRisk, owner's \"Team Backyard\"")
	want := []string{"dci", "list", "budgets", "filter", "risk", "status", "at", "risk", "owner", "team", "backyard"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
}

func TestCommandSearchStem(t *testing.T) {
	for word, want := range map[string]string{
		"budgets":   "budget",
		"anomalies": "anomaly",
		"listing":   "list",
		"invoices":  "invoice",
		"status":    "statu", // crude on purpose; prefix matching recovers it
		"access":    "access",
		"gcp":       "gcp",
		"owner's":   "owner",
	} {
		if got := commandSearchStem(word); got != want {
			t.Errorf("stem(%q) = %q, want %q", word, got, want)
		}
	}
}

func TestCommandSearchQueryTermsDropStopwordsAndDuplicateSynonyms(t *testing.T) {
	terms := commandSearchQueryTerms("What budgets are about to overspend?")
	if len(terms) != 2 || terms[0].word != "budget" || terms[1].word != "overspend" {
		t.Fatalf("terms = %+v, want budget and overspend", terms)
	}
	if len(terms[0].synonyms) != 0 {
		t.Errorf("budget synonyms = %v, want none", terms[0].synonyms)
	}
	// "overspend" expands to budget and risk; budget is already a query word
	// and must not score every budget command a second time.
	if !reflect.DeepEqual(terms[1].synonyms, []string{"risk"}) {
		t.Errorf("overspend synonyms = %v, want [risk]", terms[1].synonyms)
	}

	if terms := commandSearchQueryTerms("the a to of"); len(terms) != 0 {
		t.Errorf("stopword-only query produced %+v", terms)
	}
}

// searchFixtureCatalog mirrors the shape of real entries: a list command with
// rich notes and flags, a question command whose name carries the task, a
// terse spec summary rescued by curated keywords, and a destructive sibling.
func searchFixtureCatalog() []commandCatalogEntry {
	return []commandCatalogEntry{
		{
			Path:    []string{"list-budgets"},
			Summary: "List budgets",
			Flags: []commandCatalogFlag{
				{Name: "--filter", Description: "Filter by owner, budgetName, lastModified, or riskStatus (atRisk, onTrack, unknown)"},
				{Name: "--output", Description: "Select output"},
			},
			Examples: []commandCatalogExample{
				{Description: "Your budgets with amount, spend to date, and risk.", Command: "dci list-budgets"},
				{Description: "Budgets projected to breach, earliest first.", Command: "dci list-budgets --filter riskStatus:atRisk"},
			},
			Notes:   "The CLI default view shows budget name, owner, amount, spend to date, risk, and updated.",
			Related: []string{"budgets-at-risk", "get-budget", "delete-budget"},
		},
		{
			Path:     []string{"budgets-at-risk"},
			Summary:  "List budgets projected to breach their configured amount before the period ends",
			Examples: []commandCatalogExample{{Description: "Budgets at risk, earliest breach first.", Command: "dci budgets-at-risk"}},
			Keywords: []string{"overspend", "breach", "over budget"},
			Related:  []string{"list-budgets", "get-budget"},
		},
		{
			Path:        []string{"delete-budget"},
			Summary:     "Delete a budget",
			Destructive: true,
			Examples:    []commandCatalogExample{{Description: "Preview the deletion.", Command: "dci delete-budget \"Team Backyard\" --dry-run"}},
		},
		{
			Path:     []string{"query"},
			Summary:  "Run a query",
			Examples: []commandCatalogExample{{Description: "Run an ad-hoc Cloud Analytics query from a JSON config.", Command: "dci query < query.json"}},
			Keywords: []string{"spend", "cost", "how much", "by service", "last month"},
		},
		{
			Path:     []string{"get-cloud-diagram-cost-snapshot"},
			Summary:  "Get diagram cost snapshot",
			Examples: []commandCatalogExample{{Description: "Cost snapshot for a layer.", Command: "dci get-cloud-diagram-cost-snapshot <layer-id>"}},
		},
		{
			Path:        []string{"beta", "run-report"},
			Summary:     "Run a saved report asynchronously",
			Stage:       "beta",
			EarlyAccess: "Ask your account manager for access.",
		},
		{Path: []string{"status"}, Summary: "Show DoiT CLI configuration and active context", Keywords: []string{"logged in", "who am i"}},
	}
}

func TestSearchCommandCatalogRanking(t *testing.T) {
	entries := searchFixtureCatalog()
	cases := map[string]string{
		"budgets about to overspend":       "budgets-at-risk",
		"delete a budget":                  "delete-budget",
		"remove a budget":                  "delete-budget", // synonym
		"how much did we spend last month": "query",         // curated keywords beat a name hit on "cost"
		"run a saved report in background": "beta run-report",
		"am I logged in":                   "status",
		"budgt":                            "", // typo: no prefix of length 4 matches
		"list my budgets":                  "list-budgets",
	}
	for query, want := range cases {
		results, err := searchCommandCatalog(entries, query, 5)
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		if want == "" {
			if len(results) != 0 {
				t.Errorf("%q: got %+v, want no results", query, results)
			}
			continue
		}
		if len(results) == 0 || results[0].Command != want {
			got := make([]string, 0, len(results))
			for _, result := range results {
				got = append(got, result.Command)
			}
			t.Errorf("%q: top results %v, want %q first", query, got, want)
		}
	}
}

func TestSearchCommandCatalogResultShape(t *testing.T) {
	results, err := searchCommandCatalog(searchFixtureCatalog(), "overspending budgets", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("limit not applied: %d results", len(results))
	}
	top := results[0]
	if top.Command != "budgets-at-risk" || top.Example != "dci budgets-at-risk" || top.Score <= results[1].Score {
		t.Fatalf("top = %+v", top)
	}
	if !reflect.DeepEqual(top.Matched, []string{"overspend", "budget"}) {
		t.Errorf("matched = %v", top.Matched)
	}
	if !reflect.DeepEqual(top.Related, []string{"list-budgets", "get-budget"}) {
		t.Errorf("related = %v", top.Related)
	}

	beta, err := searchCommandCatalog(searchFixtureCatalog(), "run report", 1)
	if err != nil {
		t.Fatal(err)
	}
	if beta[0].Stage != "beta" || beta[0].EarlyAccess == "" || beta[0].Example != "" {
		t.Errorf("beta result = %+v, want stage, early_access, no example", beta[0])
	}
}

func TestSearchCommandCatalogRejectsEmptyQuery(t *testing.T) {
	for _, query := range []string{"", "   ", "the a to"} {
		if _, err := searchCommandCatalog(searchFixtureCatalog(), query, 5); err == nil {
			t.Errorf("query %q accepted", query)
		}
	}
}

func newCommandSearchTestCommand() *cobra.Command {
	command := &cobra.Command{Use: "commands", Args: cobra.NoArgs, Run: func(*cobra.Command, []string) {}}
	command.Flags().Bool("json", false, "")
	command.Flags().Bool("beta", false, "")
	registerCommandSearchFlags(command)
	return command
}

func TestRunCommandSearchEmitsJSONInAgentMode(t *testing.T) {
	oldAgentMode := agentMode
	agentMode = true
	t.Cleanup(func() { agentMode = oldAgentMode })

	command := newCommandSearchTestCommand()
	if err := command.Flags().Set("search", "delete a budget"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCommandSearch(command, searchFixtureCatalog(), "delete a budget", &out); err != nil {
		t.Fatal(err)
	}
	var response commandSearchResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if response.Version != catalogSchemaVersion || response.Query != "delete a budget" || response.Hint != commandSearchNextStepHint {
		t.Fatalf("envelope = %+v", response)
	}
	if len(response.Results) == 0 || response.Results[0].Command != "delete-budget" || !response.Results[0].Destructive {
		t.Fatalf("results = %+v", response.Results)
	}
}

func TestRunCommandSearchJSONNoMatchCarriesHint(t *testing.T) {
	command := newCommandSearchTestCommand()
	if err := command.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCommandSearch(command, searchFixtureCatalog(), "xylophone", &out); err != nil {
		t.Fatal(err)
	}
	var response commandSearchResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 || response.Hint != commandSearchNoMatchHint {
		t.Fatalf("response = %+v", response)
	}
	if !strings.Contains(out.String(), `"results": []`) {
		t.Errorf("results must be an empty array, not null:\n%s", out.String())
	}
}

func TestRunCommandSearchTextForHumans(t *testing.T) {
	oldAgentMode := agentMode
	agentMode = false
	t.Cleanup(func() { agentMode = oldAgentMode })

	command := newCommandSearchTestCommand()
	var out bytes.Buffer
	if err := runCommandSearch(command, searchFixtureCatalog(), "budgets about to overspend", &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"budgets-at-risk",
		"e.g. dci budgets-at-risk",
		"delete-budget",
		"(destructive)",
		commandSearchNextStepHint,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "{") {
		t.Errorf("human output looks like JSON:\n%s", text)
	}

	out.Reset()
	if err := runCommandSearch(command, searchFixtureCatalog(), "xylophone", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No commands match") || !strings.Contains(out.String(), "dci commands --json") {
		t.Errorf("no-match text = %q", out.String())
	}
}

func TestRunCommandSearchRejectsBadLimit(t *testing.T) {
	for _, limit := range []string{"0", "51", "-3"} {
		command := newCommandSearchTestCommand()
		if err := command.Flags().Set("limit", limit); err != nil {
			t.Fatal(err)
		}
		if err := runCommandSearch(command, searchFixtureCatalog(), "budget", &bytes.Buffer{}); err == nil {
			t.Errorf("--limit %s accepted", limit)
		}
	}
}

func TestCommandSearchRequested(t *testing.T) {
	command := newCommandSearchTestCommand()
	if _, ok := commandSearchRequested(command); ok {
		t.Fatal("search reported without --search")
	}
	if err := command.Flags().Set("search", "budgets"); err != nil {
		t.Fatal(err)
	}
	if query, ok := commandSearchRequested(command); !ok || query != "budgets" {
		t.Fatalf("query = %q, ok = %v", query, ok)
	}
}

func TestCatalogEntryCarriesCuratedKeywords(t *testing.T) {
	entry := commandCatalogEntry{Path: []string{"query"}}
	applyCommandDocToCatalogEntry(&entry)
	if len(entry.Keywords) == 0 {
		t.Fatal("query doc keywords not copied onto the catalog entry")
	}
	found := false
	for _, keyword := range entry.Keywords {
		if keyword == "spend" {
			found = true
		}
	}
	if !found {
		t.Errorf("query keywords = %v, want to include spend", entry.Keywords)
	}
}
