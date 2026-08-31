---
name: dci-cli
description: Operate the Cloud Intelligence™ CLI (`dci`) for Cloud Intelligence™ workflows. Use when the agent needs to install or verify the CLI, authenticate, troubleshoot auth or `customerContext`, inspect capabilities, run read-only list/get/report/query commands, compose `dci query` JSON payloads, analyze cost/report output, or draft safe create/update/delete commands and payloads.
---

# DCI CLI

## Overview

Use `dci` as the primary interface for Cloud Intelligence™ CLI tasks. Prefer read-only discovery first, prefer `--output toon` (compact and token-efficient; `--output json` when you need standard JSON) for agent work, and use env-scoped `DCI_CUSTOMER_CONTEXT=<customer-context>` when switching customer context temporarily.

Set `DCI_AGENT_MODE=1` (or pass `--agent`) to run in agent mode: output defaults to compact TOON, terminal decoration is disabled, and banners/hints are routed to stderr so stdout stays parseable. `dci` also auto-detects common agent environments, so this is usually already on — run `dci status` to confirm.

In explicit agent mode, failures are written to stderr as a JSON `error` envelope with a stable `code`, `message`, and `retryable` value. Inspect optional `hint`, `http_status`, `request_id`, `retry_after`, and `resolved` fields before deciding whether to correct the request or retry it. Only `retryable: true` failures are worth retrying; a `retryable: false` error means the request itself must change.

Exit codes are stable across agent and human mode: 0 success, 1 generic, 2 usage error, 10 authentication, 11 permission/context, 20 not found, 21 conflict, 30 validation or unconfirmed destructive command, 40 upstream/server error, 41 network error, 50 rate limited.

TOON list output folds rows into a compact table, and columns whose values are nested objects (e.g. `labels` on reports, `alertThresholds` on budgets) are omitted by default. To include one, request it explicitly: `-C id,labels` selects exactly those columns, or a custom `-f` filter keeps every field it projects. Explicitly requested object values arrive as compact JSON strings inside the cell. For the complete nested structure, use the item's `get-*` command or `--output json`.

Use `--fields id,name` to project list or detail responses before output, and use `--exclude description` to remove fields.

## Collections & Pagination

- Every `list-*` command returns **one server page** (usually 50 items) per invocation. `--max-rows` does not apply to lists — it caps report/query rows only.
- **Pass `--all` to fetch the full collection**: the CLI follows the server's page tokens at the endpoint's maximum page size and merges everything into one response (`pagesFetched` records the page count; a rare cap-stop keeps a resume `pageToken` and notes it on stderr). `--all` cannot be combined with `--page-token` or `--max-results`.
- Without `--all`: TOON (agent-mode default) and JSON keep the pagination metadata in-band — a non-empty `pageToken` means more pages exist; fetch them by re-running with `--page-token <token>`. `table` and `csv` output drop the wrapper but emit a stderr note with the continuation token when results were truncated.
- `--max-results` raises the page size but has a server-side cap that varies by endpoint (500 on most, 250 on budgets/assets, lower on some). The CLI rejects known-over-cap values up front; on endpoints it doesn't know, **the server silently resets out-of-range values to the default of 50**, so asking for 1000 returns fewer rows than asking for 500.
- **Pass `--search <substring>` to search a collection by keyword**: a client-side, case-insensitive filter matched against every text field of every item, implying `--all` so the whole collection is scanned (`searchDropped` records how many items were filtered out). This is the way to find dimensions for a topic: `dci list-dimensions --search genai`. Like `--all`, it cannot be combined with `--page-token` or `--max-results`.
- `list-dimensions --filter` matches a single `field:value` term **exactly** (fields: `type`, `label`, `key` — e.g. `type:system_label`). No substrings, globs, or multi-term expressions; an unrecognized expression is silently ignored and returns the unfiltered listing. To *search*, use `--search` instead.

## Report Results

- Report/query results are capped at 500 rows in agent mode. When the output contains `rowsOmitted`, the result was truncated: narrow the query with a group `limit` and a `metricFilter`, or pass `--max-rows <n>` (`--max-rows 0` for unlimited) when you genuinely need everything. Check `rowCount`/`rowsTotal` before dumping results into context.
- Always include a group `limit` in query configs (e.g. top 10 by cost); unlimited grouped queries can return thousands of rows.
- Use `--rows keyed` to receive `result.rows` as schema-named objects instead of positional arrays — no manual zipping with `result.schema`.
- The interactive human table view pivots report results by default (groups × time periods with totals, a first→last `trend` column, and heatmap shading). Agent mode and machine formats stay flat and colorless; pass `--pivot` to force the pivot when presenting to a human, or `--flat` when a human-mode invocation needs flat rows. The pivot's `trend` column is the agent-readable version of the heatmap — cite it when summarizing movement.
- Use `--output csv` to export list or report results for spreadsheets.
- Rows with a null group and zero metrics are dropped by default; pass `--include-empty-rows` to keep them (`emptyRowsDropped` marks how many were removed).
- When grouping by sparse labels, pass `--drop-unlabeled-rows` to drop rows where any grouped dimension is null or `[Value N/A]` regardless of cost (`unlabeledRowsDropped` marks how many) — otherwise one giant null-group row aggregates all unlabeled spend. Leave it off when the unlabeled bucket is the question.
- Flat/table/TOON/CSV report rows keep the machine-sortable `timestamp` column and omit datetime dimension columns (`year`, `month`, …) that repeat it; select one explicitly with `-C` to restore it.
- Report results include a `currency` field when the query config specifies one — always report monetary values with their currency. Human tables render known-currency amounts with the currency sign rounded to whole units; `--raw-numbers` restores exact values.

## Insights

- `list-insights` excludes dismissed insights by default; pass `--include-dismissed` to keep them (`dismissedOmitted` marks how many were removed). Results are sorted by `summary.potentialDailySavings` (USD) descending in every output format.
- The default table/TOON view shows `title`, `dailySavings` (from `summary.potentialDailySavings`, formatted as USD like `$500.00`; blank when zero), `provider` (from `cloudProvider`), `categories`, `lastUpdated`, and `source`, with the title column given width priority so it renders untruncated where possible. Easy wins (non-empty `easyWinDescription`) carry an " (easy win)" title suffix in table and TOON views, plus a green title in interactive tables; a row's `reportUrl` becomes a terminal hyperlink on the title. `--output json` and explicit `-C`/`--fields` selections keep the raw field names.
- Server-side filters already exist as flags: `--cloud-provider aws|gcp|azure`, `--source <source>` (repeatable), `--easy-win`, `--category`, `--priority`, `--tag`, `--search-term`.

## Budgets at Risk & Recent Anomalies

- `dci budgets-at-risk` answers "which budgets are at risk" in one call: it is `list-budgets --filter riskStatus:atRisk`, pre-sorted by earliest projected breach date. Every `list-budgets` response also carries a `riskStatus` (`atRisk`/`onTrack`/`unknown`) per budget and a top-level `riskAggregations` (`total`/`atRisk`/`onTrack`/`unknown` counts across the full filtered result set, not just the page) — use `list-budgets --filter riskStatus:<value>` directly when you need `onTrack` or `unknown` instead.
- `dci anomalies-recent --window 24h` answers "what's recent" in one call: it is `list-anomalies --sort-by startTime --sort-order desc` bounded to a time window (a Go duration string — `24h`, `168h` for 7 days; there is no `d` unit). Add `--severity information|warning|critical` to filter. Every `list-anomalies` response also carries `totalCount`/`totalCountExact` and an `anomalySummary` (`countBySeverity`, `totalCostOfAnomaly` in USD) across the full filtered result set, not just the page — `rowCount` keeps its page-scoped meaning.
- Both commands take `--max-results`/`--page-token` like the list command they wrap, and the usual `--output`/`--fields`/`--exclude` flags apply.

## Anomalies, Commitments & Allocation Coverage

- `list-anomalies` sorts with `--sort-by startTime|severityLevel|costOfAnomaly` and `--sort-order asc|desc` — exactly `asc`/`desc`; the API rejects `ascending`/`descending` even though the flag help suggests them. `--min/--max-creation-time` take epoch **milliseconds** and bound the anomaly's usage start time. `--filter` keys: `serviceName`, `billingAccount`, `platform`, `severityLevel` (lowercase values: `information`, `warning`, `critical`).
- Commitments: `list-commitments --all` for DoiT commitments. `list-aws-savings-plans` and `list-aws-recommendations` REQUIRE an org-id argument from `list-aws-organizations` — never call them without it: fetch the organizations first (alone), then batch the per-org calls together in your next response. Realized discounts also surface in `query` via the `savings_description` dimension.
- Allocation/tag coverage: run the same query twice — total cost, then with `--drop-unlabeled-rows` — and report the difference as unallocated spend. Group by the label under test for per-value coverage.

## Resource Names

- Positional resource arguments accept names, not just IDs: exact match, unique case-insensitive substring, or close-typo fuzzy (`dci get-report "monthly aws"`). Prefer IDs when known — deterministic and no lookup round-trip; use names when exploring or when the user gave one.
- `NAME_AMBIGUOUS` (exit 2) lists up to 10 `name (id)` candidates in the message: pick the intended ID and re-run. Never re-guess with another fuzzy string. `NAME_NOT_FOUND` (exit 20): browse with the `list-*` command from the hint; the hint says when the client-side scan was truncated (>1500 items).
- An argument without spaces that matches no name — or whose lookup request fails — is sent to the API verbatim, so exact IDs that don't match the 20-char shape (e.g. asset IDs like `g-suite-2319621428`) work without `--id`; a 404 on that request hints at `--name`. `NAME_NOT_FOUND` therefore only surfaces for multi-word names or under `--name`.
- Escape hatches: `--id` treats positionals as literal IDs, `--name` forces a lookup when a real name matches the 20-char ID shape, and `DCI_NO_RESOLVE=1` disables resolution entirely for scripts.
- `dci commands --json` marks resolvable commands with `resolvesNames: true`.

## Quick Start

1. Confirm the CLI exists and is runnable: `dci --version`
2. Check session and active context: `dci status`; confirm identity and permissions with `dci validate`
3. Discover command shape before drafting or running commands: `dci --help` and `dci <command> --help` (terse; add `--help-full` when you need the request/response schemas)
4. Prefer `list-*`, `get-*`, `get-report`, and `query` before `create-*`, `update-*`, or `delete-*`

Use `dci skill list` to inspect the files embedded in the installed CLI. Use `dci skill update <agent>` to refresh one installed copy, or omit the agent to update every detected installation; locally edited managed files require an explicit `--force` overwrite and are saved in a uniquely named sibling backup directory first.

## Queries

`dci query` runs a Cloud Analytics report query from a JSON `config` containing metrics, grouping, time ranges, filters, and display options. The API does not expose a SQL request field, and the CLI rejects unknown top-level body fields.

```bash
dci query < query.json
```

Load [query-patterns.md](references/query-patterns.md) for payload examples.

## Safety

- Prefer env-scoped `DCI_CUSTOMER_CONTEXT=<customer-context> dci ...` over `dci customer-context set` unless the user explicitly wants a persistent local change.
- Treat `create-*`, `update-*`, `delete-*`, invite, ingest, and comment-post commands as side-effectful.
- Use `dci commands --json` when you need machine-readable argument, flag, output-shape, authentication, and destructive-operation metadata.
- Run a side-effectful command with `--dry-run` first. Most commands print a local preview without sending a request; commands with an API-native `dryRun` parameter send a simulation request and return an action marked `"dry_run": true`.
- Pass `--yes` only after the user has approved a command classified as destructive. Do not set `DCI_CONFIRM_DESTRUCTIVE=1` as a blanket bypass.
- A destructive command given a name resolves it first: the confirmation names the true target (e.g. `delete-report targets report "Monthly Spend" (<report-id>)`), and the agent-mode error envelope carries `resolved: {input, name, id}`. Re-run with the ID from the hint, never the original fuzzy input.
- `--dry-run` performs name resolution too (read-only), so the previewed target is the real one.
- Keep shared examples anonymized. Redact customer IDs, report IDs, emails, and URLs unless the user explicitly asks for live values.
- When a command may fail because of permissions or context, explain that `dci login` proves authentication but not authorization; `dci validate` confirms both identity and access.
- In CI or headless environments, always set `DCI_API_KEY`: without credentials the CLI fails fast with `AUTHENTICATION_REQUIRED` instead of opening a browser.

## Documentation

- CLI guide: https://help.doit.com/docs/cli (append `.md` to any Help Center URL for plain Markdown, e.g. https://help.doit.com/docs/cli.md)
- Machine-readable Help Center index: https://help.doit.com/llms.txt (full corpus: https://help.doit.com/llms-full.txt)
- API reference: https://developer.doit.com/
- From the terminal: `dci docs` prints these entry points; `dci <command> --help` is terse by default (`--help-full` adds the complete request/response schemas); `dci commands --json` is the machine-readable catalog.

## Reference Map

- Load [capabilities.md](references/capabilities.md) for the capability tree, command families, and invocation patterns.
- Load [examples.md](references/examples.md) for generalized install/auth, discovery, report, query, and mutation examples.
- Load [query-patterns.md](references/query-patterns.md) for JSON query workflows.
- Load [cost-optimization.md](references/cost-optimization.md) for an anonymized 30-day cost analysis example.
- Load [finops-baseline.md](references/finops-baseline.md) for the greenfield workflow: bring an account from unmanaged spend to budgets, alerts, and allocations in one session.
- Load [evals.md](references/evals.md) to validate the skill against realistic user prompts.
- Load [csp-patterns.md](references/csp-patterns.md) **only for DoiT-employee (doer) accounts** asking multi-customer or book-of-business questions — the CSP all-customers tenant, its dimensions, and its constraints.
