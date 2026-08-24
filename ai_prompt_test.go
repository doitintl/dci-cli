package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAISystemPromptModes(t *testing.T) {
	catalog := aiTestCatalog()

	doer := aiSystemPrompt(catalog, true, true)
	if !strings.Contains(doer, "# Customer context") || !strings.Contains(doer, "set_customer_context") {
		t.Fatal("tenant-aware prompt missing the customer context section")
	}
	if !strings.Contains(doer, "csp.doit.com") || !strings.Contains(doer, "csp_strategic_account_manager") {
		t.Fatal("doer prompt missing the CSP section (F3)")
	}
	customer := aiSystemPrompt(catalog, false, false)
	if strings.Contains(customer, "Customer context") || strings.Contains(customer, "tenant") {
		t.Fatal("customer-mode prompt leaks tenant vocabulary (AI-SPEC §6.2)")
	}
	if strings.Contains(customer, "csp") || strings.Contains(customer, "CSP") {
		t.Fatal("customer-mode prompt leaks CSP vocabulary (F3 gate)")
	}
	// Tenant-aware but not a doer (partner with a context set): still no CSP.
	partner := aiSystemPrompt(catalog, true, false)
	if strings.Contains(partner, "csp") || strings.Contains(partner, "CSP") {
		t.Fatal("non-doer tenant-aware prompt leaks CSP vocabulary (F3 gate)")
	}
	for _, prompt := range []string{doer, customer} {
		if !strings.Contains(prompt, "list-budgets — List budgets") {
			t.Fatal("catalog line missing from prompt")
		}
		if !strings.Contains(prompt, "run_dci_command") {
			t.Fatal("tool guidance missing from prompt")
		}
		// FinOps surfaces (F2) are tenant-agnostic: every mode gets them.
		if !strings.Contains(prompt, "# FinOps surfaces") || !strings.Contains(prompt, "list-insights --all") {
			t.Fatal("FinOps surfaces section missing (F2)")
		}
		if !strings.Contains(prompt, `--sort-order asc|desc`) {
			t.Fatal("anomaly sort enums missing (F2; the API help text misleads with ascending/descending)")
		}
	}
}

func TestAISystemPromptEmbedsQueryPatterns(t *testing.T) {
	prompt := aiSystemPrompt(aiTestCatalog(), false, false)

	// The embedded skill reference: query config shape, dimension discovery,
	// and the GenAI worked example must reach the model without a --help or
	// list-dimensions round-trip.
	for _, needle := range []string{
		"# Cost analytics queries (dci query)",
		`"dataSource": "billing"`,
		"genai/model",
		"metricFilter",
		"--search",
		"--drop-unlabeled-rows",
		// The reference's ids are known-good: the model must go straight to
		// the query instead of burning a round-trip re-verifying them.
		"known-good",
	} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("prompt missing %q", needle)
		}
	}
	// Session-hostile advice from the reference must be stripped: tenant
	// switches go through set_customer_context, never an env var.
	if strings.Contains(prompt, "DCI_CUSTOMER_CONTEXT") {
		t.Fatal("prompt leaks DCI_CUSTOMER_CONTEXT switching advice")
	}
	// The section must stay inside the cache budget's order of magnitude.
	if estimate := aiEstimateTokens(aiQueryPatternsSection()); estimate > 2500 {
		t.Fatalf("query patterns section estimates %d tokens", estimate)
	}
}

func TestAISystemPromptEmptyCatalog(t *testing.T) {
	prompt := aiSystemPrompt(nil, false, false)
	if !strings.Contains(prompt, "dci login") {
		t.Fatal("empty-catalog prompt must point at login")
	}
}

func TestAIVolatileSystem(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	volatile := aiVolatileSystem(dir, now, "")
	if !strings.Contains(volatile, "2026-08-24") {
		t.Fatalf("volatile system missing date: %q", volatile)
	}
	if strings.Contains(volatile, "customer context") {
		t.Fatalf("no context set, but volatile mentions one: %q", volatile)
	}
	if err := os.WriteFile(customerContextPath(dir), []byte("acme.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if volatile := aiVolatileSystem(dir, now, ""); !strings.Contains(volatile, "acme.com") {
		t.Fatalf("volatile system missing active context: %q", volatile)
	}
}

func TestAIEstimateTokens(t *testing.T) {
	if got := aiEstimateTokens(strings.Repeat("a", 400)); got != 100 {
		t.Fatalf("estimate = %d, want 100", got)
	}
}

func TestAIUserCommands(t *testing.T) {
	dir := t.TempDir()
	content := `{
		"top5": {"command": "list-reports --limit 5", "summary": "Top five reports"},
		"review": {"prompt": "Review last month's spend for"},
		"help": {"command": "status"},
		"bad name": {"command": "status"},
		"empty": {}
	}`
	if err := os.WriteFile(dir+"/"+aiUserCommandsFileName, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := loadAIUserCommands(dir)
	if len(commands) != 3 {
		t.Fatalf("loaded %d commands, want 3 (verb-shadowing and bad names dropped): %v", len(commands), commands)
	}
	if _, shadowed := commands["help"]; shadowed {
		t.Fatal("verb-shadowing name survived")
	}

	route := aiRouteLine("/top5 --output json", aiTestCatalog(), commands)
	if route.kind != aiRouteDispatch || strings.Join(route.argv, " ") != "list-reports --limit 5 --output json" {
		t.Fatalf("saved command route = %+v", route)
	}
	route = aiRouteLine("/review acme.com", aiTestCatalog(), commands)
	if route.kind != aiRouteChat || route.text != "Review last month's spend for acme.com" {
		t.Fatalf("saved prompt route = %+v", route)
	}
	// Empty entries fall through to the catalog/unknown path.
	route = aiRouteLine("/empty", aiTestCatalog(), commands)
	if route.kind != aiRouteUnknown {
		t.Fatalf("empty saved command route = %+v", route)
	}

	completions := aiCompletionsFor("/", aiTestCatalog(), commands, 30)
	var found bool
	for _, completion := range completions {
		if completion.Value == "top5" && completion.Summary == "Top five reports" {
			found = true
		}
	}
	if !found {
		t.Fatalf("saved commands missing from completion: %+v", completions)
	}
}
