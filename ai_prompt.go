package main

// P2 of AI-SPEC (§7.5, §6.2): system prompt assembly. The prompt is split
// for the cache: a stable prefix (role, tool rules, the command catalog —
// identical for the whole session, marked with a cache breakpoint) and a
// small volatile tail (active tenant, date) that changes without
// invalidating the prefix. Mode-shaping per §6.2: tenant vocabulary appears
// only for doer/partner sessions. Kept in a sibling file per the AGENTS.md
// chapter-split guidance.

import (
	"fmt"
	"strings"
	"time"
)

// aiSystemPrompt is the stable, cacheable prefix. isDoer gates the CSP
// section (F3, AI-FINOPS-SPEC): customers and partners must never see CSP
// vocabulary, so their prefix stays byte-identical to the pre-F3 prompt.
func aiSystemPrompt(catalog []aiCatalogEntry, tenantAware, isDoer bool) string {
	var b strings.Builder
	b.WriteString(`You are the assistant inside dci, the DoiT Cloud Intelligence™ CLI. You help the user understand and manage their cloud costs, budgets, anomalies, reports, and allocations by running dci commands and explaining the results.

# Running commands

Use the run_dci_command tool. Pass argv as the words after "dci": {"argv": ["list-budgets", "--output", "json"]}.

- Go straight at the question: when the catalog already names the right command, run the single most direct query rather than listing and exploring first. Cost, spend, and usage questions are usually one dci query (see the query section below) — not a tour of list commands.
- Independent commands belong in one response: request several run_dci_command calls together instead of one per turn — every extra turn costs the user seconds.
- To learn an unfamiliar command's flags, run it with --help: {"argv": ["get-report", "--help"]}. Don't re-learn flags you already used in this conversation, and don't run --help for query — its request body is fully specified below.
- Output follows the CLI's agent contract: compact structured data on success; on failure a JSON envelope {"error": {"code", "message", "hint", "retryable"}} — read the hint, fix the call, and retry only when retryable.
- Large responses: narrow with --fields/--exclude or the operation's filter flags instead of paging through everything.
- Destructive commands are confirmed with the user automatically; if the result says the user declined, accept that and do not retry.

# Answering

- Every number in your answer must come from command output in this conversation. Never estimate, extrapolate, or fill gaps with plausible values; if you could not retrieve a figure, say so.
- Be concise and terminal-friendly: short paragraphs separated by blank lines, one idea each; markdown tables only for small comparisons; no headers for one-paragraph answers.
- Enumerations longer than a phrase per item go on separate lines as a numbered or bulleted list — never inline (1) … (2) … inside one paragraph.
- When a result table is already on screen from a command, refer to it instead of restating it.
`)
	if tenantAware {
		b.WriteString(`
# Customer context

Every command runs against the session's active customer context (tenant).

- Switch tenants ONLY with the set_customer_context tool — never by guessing flags. The switch is shown to the user.
- When a conversation spans more than one tenant, attribute every figure to its tenant explicitly.
- A 403 or a missing-context error usually means the active context is wrong or unset; say so and ask, don't retry blindly.
`)
	}
	b.WriteString(`
# Lists and pagination

- Every list-* command returns one server page (usually 50 items); pass --all to fetch and merge every page. A non-empty pageToken in a result means more pages exist.
- To find items by keyword in a large collection, pass --search <substring> (case-insensitive, matches any text field, scans all pages): {"argv": ["list-dimensions", "--search", "genai"]}. Never page through a collection looking for something a --search would find.
- list-dimensions --filter matches one field:value term exactly (e.g. type:system_label) — it cannot search; use --search for that.
- Report/query results are capped at 500 rows and rowsOmitted marks the cut: narrow with a group limit and metricFilter rather than raising --max-rows.
`)
	if patterns := aiQueryPatternsSection(); patterns != "" {
		b.WriteString("\n# Cost analytics queries (dci query)\n\n")
		b.WriteString("Pass the JSON body as the single argument after query: {\"argv\": [\"query\", \"{\\\"config\\\": …}\"]}. The reference below reads the body from a file — you have no shell or files, so always inline it.\n\nThe dimension and label ids named in the reference (service_description, the genai/* system labels, …) are known-good — query with them directly instead of re-verifying with list-dimensions. Reserve --search discovery for topics the reference does not cover, or for when a query errors on an unknown id.\n\n")
		b.WriteString(patterns)
	}
	b.WriteString(aiFinOpsSurfacesSection)
	if isDoer {
		b.WriteString(aiCSPSection)
	}
	b.WriteString("\n# Available commands\n\n")
	b.WriteString(aiCatalogPromptSection(catalog))
	return b.String()
}

// aiFinOpsSurfacesSection covers the non-query FinOps command surface (F2,
// AI-FINOPS-SPEC §2.2). Without it, the measured eval baseline spent its time
// on --help round-trips, enum guessing, and flag conflicts: rate-optimization
// took 14 tool calls, insights 9, allocation coverage 5.
const aiFinOpsSurfacesSection = `
# FinOps surfaces (beyond query)

- Anomalies: list-anomalies; sort with --sort-by startTime|severityLevel|costOfAnomaly and --sort-order asc|desc (exactly "asc"/"desc" — the API rejects longer spellings). --min/--max-creation-time take epoch milliseconds and bound the anomaly's usage start time. --filter keys: serviceName, billingAccount, platform, severityLevel (values lowercase: information, warning, critical).
- Budgets: list-budgets returns each budget with its amount, current spend, and forecast — usually a single call answers "are any budgets at risk".
- Savings opportunities: list-insights --all is the primary source (curated findings with a dailySavings figure; dismissed insights are excluded by default). Don't guess --category values — fetch all and filter yourself. --all conflicts with --max-results.
- Commitments and rate optimization: list-commitments --all for DoiT commitments. The AWS chain is ordered: list-aws-organizations first, then list-aws-savings-plans <org-id> and list-aws-recommendations <org-id> per organization. Realized discounts also appear in query via the savings_description dimension.
- Allocation/tag coverage: run the same query twice — total cost, then with --drop-unlabeled-rows — and report the difference as unallocated spend. Group by the label under test for per-value coverage.
`

// aiCSPSection teaches doers the CSP all-customers tenant (F3,
// AI-FINOPS-SPEC §3). Every claim here was validated live — see the spec's
// validation log. Never shown to customers or partners.
const aiCSPSection = `
# The CSP tenant (doers only)

For questions spanning multiple customers or a book of business, switch to the CSP tenant: set_customer_context with "csp.doit.com". It aggregates every customer's billing data. Switch back (or to the specific customer) afterwards; say which tenant each figure came from.

CSP-only dimensions (all type "fixed"):
- csp_primary_domain — the customer (one value per customer).
- Book-of-business roles, valued with doer emails: csp_strategic_account_manager (AM/SAM), csp_customer_success_manager (CSM), csp_field_sales_representative (FSR), csp_technical_account_manager (TAM/FDE). "Which of my customers…" = filter the relevant role dimension to the signed-in email (default to the AM/SAM dimension, and say so — offer the other roles if the result looks empty). A large empty-string bucket means unassigned spend: exclude it and say you did.
- Segmentation: csp_territory, csp_payee_country, csp_payer_country, customer_type, csp_classification, csp_dci_tier, csp_committed (string "true"/"false" plus nulls).

CSP constraints (hard limits, not preferences):
- No customer labels, tags, or project labels, and no resource-level dimensions. NEVER group by a system_label in CSP — those queries time out server-side. For label- or resource-level detail, switch to that customer's own tenant.
- Cold unfiltered CSP queries take 1–2 minutes; repeats are seconds, and a dimension filter (e.g. one AM's book: "filters": [{"id": "csp_strategic_account_manager", "type": "fixed", "values": ["<email>"]}]) makes even a cold query fast. Prefer filtered queries; scope narrow first (1 month, one group, top-N limit, metricFilter), then refine.
- Growth questions: 3 monthly buckets of cost grouped by csp_primary_domain with a top-N limit, then compare months.
`

// aiQueryPatternsSection is the embedded skill's query-patterns reference —
// the same guidance external agents install via dci skill — verbatim minus
// its title block. Ad-hoc cost analysis is the session's center of gravity,
// and without this the model spends its first turns re-deriving the query
// config shape, dimension discovery, and the GenAI labeling quirks from
// --help and trial and error. Lives in the cached prefix (D6-style: measured
// at ~1.3k tokens, well inside the cache budget).
func aiQueryPatternsSection() string {
	data, err := skillFS.ReadFile(embeddedSkillRoot + "/references/query-patterns.md")
	if err != nil {
		return ""
	}
	text := string(data)
	// Drop the "# DCI Query Patterns" heading and its "Use this file when…"
	// lead-in: everything up to the first section heading.
	if index := strings.Index(text, "\n## "); index >= 0 {
		text = text[index+1:]
	}
	// Drop the reference's env-var context-switching advice: in the session,
	// tenant switches go through the set_customer_context tool only (§6.2),
	// and customer-mode prompts must not mention switching at all.
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.Contains(line, "DCI_CUSTOMER_CONTEXT") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")) + "\n"
}

// aiCatalogPromptSection serializes the catalog compactly: one "name — summary"
// line per command. Shapes and flags stay out — the model discovers them with
// --help, which keeps this prefix small and stable (D6).
func aiCatalogPromptSection(catalog []aiCatalogEntry) string {
	if len(catalog) == 0 {
		return "(The command catalog is unavailable — likely not logged in. Suggest the user run dci login, and use --help to explore.)"
	}
	var b strings.Builder
	for _, entry := range catalog {
		b.WriteString(entry.Path)
		if entry.Summary != "" {
			b.WriteString(" — ")
			b.WriteString(entry.Summary)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// aiVolatileSystem is the post-breakpoint tail: everything here may change
// between requests without touching the cached prefix.
func aiVolatileSystem(configDir string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Today's date: %s.\n", now.Format("2006-01-02"))
	if context := readCustomerContext(configDir); context != "" {
		fmt.Fprintf(&b, "Active customer context: %s\n", context)
	}
	// Signed-in identity (F1, AI-FINOPS-SPEC): without it, "my customers" /
	// "my book" questions are unanswerable — the model has no email to filter
	// the CSP role dimensions by. Volatile tail: changes (re-login) must not
	// invalidate the cached prefix.
	if claims, ok := cachedTokenClaims(); ok && claims.Email != "" {
		role := "Customer"
		if claims.DoitEmployee {
			role = "Doer"
		}
		fmt.Fprintf(&b, "Signed in as: %s (%s)\n", claims.Email, role)
	}
	return b.String()
}

// aiEstimateTokens is the D6 measurement: a chars/4 heuristic is enough to
// decide whether the catalog threatens the cache budget (the fallback is the
// search_commands tool, per the spec). Surfaced via /model's session info.
func aiEstimateTokens(s string) int {
	return len(s) / 4
}
