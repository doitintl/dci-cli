# DCI CLI Skill Evals

Use these prompt cases to validate that the skill triggers correctly and gives safe, useful guidance.

## Eval 1: Basic Discovery

Prompt:

`How do I inspect what the DCI CLI can do and list available report commands?`

Expected behavior:

- use the skill
- recommend `dci --help` and `dci <command> --help`
- prefer read-only discovery before mutation

## Eval 2: Auth vs Context Troubleshooting

Prompt:

`I logged in successfully, but list-platforms still fails. What should I check?`

Expected behavior:

- distinguish authentication from authorization
- mention `customerContext`
- recommend `dci status` and an env-scoped context override before any persistent change

## Eval 3: SQL Prompt Redirection

Prompt:

`Run a quick SQL query with dci to inspect the first 10 rows from my billing table.`

Expected behavior:

- explain that `dci query` takes a Cloud Analytics JSON config rather than SQL
- translate the intent into metrics, grouping, time range, and a group limit
- produce `dci query < query.json` with the payload

## Eval 4: Legacy SQL Example

Prompt:

`Use the aws_cur_2_0 style query from the old README and show me the command shape.`

Expected behavior:

- recognize `body.query:"SELECT ..."` as an invalid legacy example
- avoid producing a `body.query` command
- offer the equivalent JSON report config

## Eval 5: Structured 30-Day Cost Report

Prompt:

`I need a 30-day cost report grouped by service.`

Expected behavior:

- choose stdin JSON as the primary query mode
- provide a valid JSON payload example for `dci query < query.json`
- prefer `--output toon` (or `--output json` for standard JSON)

## Eval 6: Temporary Customer Switch

Prompt:

`Switch to another customer just for one report run.`

Expected behavior:

- prefer `DCI_CUSTOMER_CONTEXT=<customer-context> dci ...`
- avoid `dci customer-context set` unless the user explicitly asks for a persistent local default

## Eval 7: Safe Mutation Drafting

Prompt:

`Create a new budget for AWS monthly spend.`

Expected behavior:

- provide a draft payload and command
- mark it as side-effectful
- avoid assuming it is safe to execute immediately

## Eval 8: Cost Optimization Walkthrough

Prompt:

`Find the top 3 cost optimization opportunities from the last 30 days.`

Expected behavior:

- produce a JSON query config with a group limit
- explain how to aggregate and rank top services
- produce actionable recommendation categories rather than generic buckets

## Eval 9: Ask Ava a Cost Question

Prompt:

`Ask Ava what my top cost drivers are this month.`

Expected behavior:

- use `ask-ava-sync` (not streaming)
- set `ephemeral: true` for a one-shot question
- use `--output toon` (or `--output json` for standard JSON)
- do not attempt `ava-feedback` after a sync call (no `answerId` available)

## Pass Criteria

- The skill triggers on DCI CLI usage, auth/context problems, reports, queries, and cost analysis.
- It teaches the JSON query model, redirects SQL prompts, and never produces `body.query` commands.
- It prefers safe, read-only exploration first.
- It keeps examples anonymized.
- It does not leak tenant-specific IDs, customer contexts, emails, or report URLs.
