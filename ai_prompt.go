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

// aiSystemPrompt is the stable, cacheable prefix.
func aiSystemPrompt(catalog []aiCatalogEntry, tenantAware bool) string {
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
	b.WriteString("\n# Available commands\n\n")
	b.WriteString(aiCatalogPromptSection(catalog))
	return b.String()
}

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
	return b.String()
}

// aiEstimateTokens is the D6 measurement: a chars/4 heuristic is enough to
// decide whether the catalog threatens the cache budget (the fallback is the
// search_commands tool, per the spec). Surfaced via /model's session info.
func aiEstimateTokens(s string) int {
	return len(s) / 4
}
