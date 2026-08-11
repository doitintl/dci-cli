---
name: dci-cli
description: Operate the Cloud Intelligence™ CLI (`dci`) for Cloud Intelligence™ workflows. Use when the agent needs to install or verify the CLI, authenticate, troubleshoot auth or `customerContext`, inspect capabilities, run read-only list/get/report/query commands, compose `dci query` JSON payloads, analyze cost/report output, or draft safe create/update/delete commands and payloads.
---

# DCI CLI

## Overview

Use `dci` as the primary interface for Cloud Intelligence™ CLI tasks. Prefer read-only discovery first, prefer `--output toon` (compact and token-efficient; `--output json` when you need standard JSON) for agent work, and use env-scoped `DCI_CUSTOMER_CONTEXT=<customer-context>` when switching customer context temporarily.

Set `DCI_AGENT_MODE=1` (or pass `--agent`) to run in agent mode: output defaults to compact TOON, terminal decoration is disabled, and banners/hints are routed to stderr so stdout stays parseable. `dci` also auto-detects common agent environments, so this is usually already on — run `dci status` to confirm.

In explicit agent mode, failures are written to stderr as a JSON `error` envelope with a stable `code`, `message`, and `retryable` value. Inspect optional `hint`, `http_status`, `request_id`, and `retry_after` fields before deciding whether to correct the request or retry it. Only `retryable: true` failures are worth retrying; a `retryable: false` error means the request itself must change.

Exit codes are stable across agent and human mode: 0 success, 1 generic, 2 usage error, 10 authentication, 11 permission/context, 20 not found, 21 conflict, 30 validation or unconfirmed destructive command, 40 upstream/server error, 41 network error, 50 rate limited.

TOON list output folds rows into a compact table, and columns whose values are nested objects (e.g. `labels` on reports, `alertThresholds` on budgets) are omitted by default. To include one, request it explicitly: `-C id,labels` selects exactly those columns, or a custom `-f` filter keeps every field it projects. Explicitly requested object values arrive as compact JSON strings inside the cell. For the complete nested structure, use the item's `get-*` command or `--output json`.

Use `--fields id,name` to project list or detail responses before output, and use `--exclude description` to remove fields.

## Report Results

- Report/query results are capped at 500 rows in agent mode. When the output contains `rowsOmitted`, the result was truncated: narrow the query with a group `limit` and a `metricFilter`, or pass `--max-rows <n>` (`--max-rows 0` for unlimited) when you genuinely need everything. Check `rowCount`/`rowsTotal` before dumping results into context.
- Always include a group `limit` in query configs (e.g. top 10 by cost); unlimited grouped queries can return thousands of rows.
- Use `--rows keyed` to receive `result.rows` as schema-named objects instead of positional arrays — no manual zipping with `result.schema`.
- The interactive human table view pivots report results by default (groups × time periods with totals). Agent mode and machine formats stay flat; pass `--pivot` to force the pivot when presenting to a human, or `--flat` when a human-mode invocation needs flat rows.
- Use `--output csv` to export list or report results for spreadsheets.
- Rows with a null group and zero metrics are dropped by default; pass `--include-empty-rows` to keep them (`emptyRowsDropped` marks how many were removed).
- Report results include a `currency` field when the query config specifies one — always report monetary values with their currency. Human tables render known-currency amounts with the currency sign rounded to whole units; `--raw-numbers` restores exact values.

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
