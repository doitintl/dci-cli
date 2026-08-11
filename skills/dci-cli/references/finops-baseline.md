# FinOps Baseline Workflow

Use this file when asked to "set up FinOps basics", "bring this account up to standard",
"create budgets for our main services", or any greenfield cost-governance request. The goal:
take a customer from unmanaged spend to a working baseline — visibility, budgets, anomaly
awareness, alerts, and showback — in one agent session.

Work read-first, propose before mutating, and get explicit approval before every `create-*`
command (they are side-effectful; deletes additionally require `--yes`).

## Step 1 — Understand the spend

```bash
dci query --rows keyed --output json < top-services.json   # 30d cost by service, top 10, with a metricFilter
dci list-anomalies --max-results 10
dci list-budgets
dci list-allocations
```

Summarize: top services and their monthly run rate (include the `currency`), existing
budgets/allocations coverage, recent anomalies. Gaps = the plan.

## Step 2 — Budgets

Check for API-suggested budgets first. A suggestion supplies the shape, but accepting it
requires an existing matching budget:

```bash
dci list-budget-suggestions
dci create-budget < budget.json
dci accept-budget-suggestion <suggestion-id> budgetId: <created-budget-id>
```

Present both mutations for approval before running them. For services without suggestions,
draft `create-budget` payloads from observed spend (e.g. last full month × 1.1,
`type: recurring`, `timeInterval: month`). Include alert thresholds (50/75/90%) so budgets notify.

## Step 3 — Anomaly and cost alerts

Anomaly detection runs automatically; make sure someone hears it:

```bash
dci create-alert < alert.json   # e.g. month-over-month cost increase > N% on top services
```

Draft alert configs per top service or per cloud provider; run `dci create-alert --help`
for the schema.

## Step 4 — Showback structure

If spend isn't attributable, propose allocations (by project/label/account) so future
reports and budgets can be scoped:

```bash
dci create-allocation < allocation.json
```

## Step 5 — Report and hand off

Produce a short summary: what exists now (budgets with links from `url`/`urlUI` fields),
what was created, what needs a human decision. Deep-link each created resource:

```bash
dci open budget <id>     # prints the console URL in agent mode
dci open report <id>
```

## The as-code loop

Every resource supports pull → edit → push, which keeps changes reviewable:

```bash
dci get-report-config <id> > report.json   # pull
# edit report.json with the user
dci update-report <id> < report.json       # push (validate first with --dry-run)
```

The same pattern works for budgets (`get-budget` / `update-budget`), alerts, and
allocations. Prefer this loop over describing changes in prose — files diff, prose doesn't.
