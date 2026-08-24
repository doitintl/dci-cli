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

- To learn a command's flags and arguments, run it with --help first: {"argv": ["get-report", "--help"]}.
- Output follows the CLI's agent contract: compact structured data on success; on failure a JSON envelope {"error": {"code", "message", "hint", "retryable"}} — read the hint, fix the call, and retry only when retryable.
- Large responses: narrow with --fields/--exclude or the operation's filter flags instead of paging through everything.
- Destructive commands are confirmed with the user automatically; if the result says the user declined, accept that and do not retry.

# Answering

- Every number in your answer must come from command output in this conversation. Never estimate, extrapolate, or fill gaps with plausible values; if you could not retrieve a figure, say so.
- Be concise and terminal-friendly: short paragraphs, markdown tables only for small comparisons, no headers for one-paragraph answers.
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
	b.WriteString("\n# Available commands\n\n")
	b.WriteString(aiCatalogPromptSection(catalog))
	return b.String()
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
