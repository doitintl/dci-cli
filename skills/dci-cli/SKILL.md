---
name: dci-cli
description: Operate the DoiT Cloud Intelligence CLI (`dci`) for DoiT Cloud Intelligence workflows. Use when the agent needs to install or verify the CLI, authenticate, troubleshoot auth or `customerContext`, inspect capabilities, run read-only list/get/report/query commands, compose `dci query` requests in either inline SQL shorthand or stdin JSON, analyze cost/report output, or draft safe create/update/delete commands and payloads.
---

# DCI CLI

## Overview

Use `dci` as the primary interface for DoiT Cloud Intelligence CLI tasks. Prefer read-only discovery first, prefer `--output toon` (compact and token-efficient; `--output json` when you need standard JSON) for agent work, and use env-scoped `DCI_CUSTOMER_CONTEXT=<customer-context>` when switching customer context temporarily.

Set `DCI_AGENT_MODE=1` (or pass `--agent`) to run in agent mode: output defaults to compact TOON, terminal decoration is disabled, and banners/hints are routed to stderr so stdout stays parseable. `dci` also auto-detects common agent environments, so this is usually already on — run `dci status` to confirm.

TOON list output folds rows into a compact table, and columns whose values are nested objects (e.g. `labels` on reports, `alertThresholds` on budgets) are omitted by default. To include one, request it explicitly: `-C id,labels` selects exactly those columns, or a custom `-f` filter keeps every field it projects. Explicitly requested object values arrive as compact JSON strings inside the cell. For the complete nested structure, use the item's `get-*` command or `--output json`.

## Quick Start

1. Confirm the CLI exists and is runnable: `dci --version`
2. Check session and active context: `dci status`
3. Discover command shape before drafting or running commands: `dci --help` and `dci <command> --help`
4. Prefer `list-*`, `get-*`, `get-report`, and `query` before `create-*`, `update-*`, or `delete-*`

## Query Modes

Use `dci query` in two modes:

- Use inline SQL shorthand for quick exploration or when the user explicitly asks for SQL, for example `dci query body.query:"SELECT * FROM <billing-table> LIMIT 10"`.
- Use stdin JSON for structured Cloud Analytics report-style queries with metrics, grouping, time ranges, and display options.

Load [query-patterns.md](references/query-patterns.md) when you need query examples or need to choose between SQL shorthand and JSON input.

## Safety

- Prefer env-scoped `DCI_CUSTOMER_CONTEXT=<customer-context> dci ...` over `dci customer-context set` unless the user explicitly wants a persistent local change.
- Treat `create-*`, `update-*`, `delete-*`, invite, ingest, and comment-post commands as side-effectful.
- Keep shared examples anonymized. Redact customer IDs, report IDs, emails, and URLs unless the user explicitly asks for live values.
- When a command may fail because of permissions or context, explain that `dci login` proves authentication but not authorization.

## Reference Map

- Load [capabilities.md](references/capabilities.md) for the capability tree, command families, and invocation patterns.
- Load [examples.md](references/examples.md) for generalized install/auth, discovery, report, query, and mutation examples.
- Load [query-patterns.md](references/query-patterns.md) for SQL shorthand and stdin JSON query workflows.
- Load [cost-optimization.md](references/cost-optimization.md) for an anonymized 30-day cost analysis example.
- Load [evals.md](references/evals.md) to validate the skill against realistic user prompts.
