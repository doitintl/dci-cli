# DCI CLI Examples

All examples in this file are generalized and anonymized. Replace placeholders before running commands against a live environment.

## Install and Session

```bash
brew install doitintl/dci-cli/dci
dci login
dci status
dci --help
```

Temporary customer context override:

```bash
DCI_CUSTOMER_CONTEXT=<customer-context> dci status
```

Persist only when the user explicitly wants a local default:

```bash
dci customer-context set <customer-context>
```

## Discovery and Read-Only Navigation

```bash
dci list-alerts --output toon
dci get-alert <alert-id> --output toon
dci list-dimensions --output toon
dci list-users --output toon
dci list-platforms --output toon
```

## Query Examples

Queries use a Cloud Analytics JSON `config`. There is no SQL input mode, and unknown top-level body fields are rejected.

```bash
dci query < query.json
```

Present results to a human as a pivoted table, or export to a spreadsheet:

```bash
dci query --pivot --output table < query.json
dci query --output csv < query.json > results.csv
```

Consume results programmatically with schema-named rows:

```bash
dci query --rows keyed --output json < query.json | jq '.result.rows[0].cost'
```

`query.json` example:

```json
{
  "config": {
    "dataSource": "billing",
    "layout": "table",
    "timeInterval": "day",
    "timeRange": {
      "mode": "last",
      "amount": 30,
      "unit": "day",
      "includeCurrent": false
    },
    "displayValues": "actuals_only",
    "metrics": [
      {
        "type": "basic",
        "value": "cost"
      }
    ],
    "group": [
      {
        "id": "service_description",
        "type": "fixed",
        "limit": {
          "metric": {
            "type": "basic",
            "value": "cost"
          },
          "sort": "desc",
          "value": 10
        }
      }
    ]
  }
}
```

## Answering Cost Questions

Map the user's question to the read commands before reaching for anything else:

- "Why did our bill spike yesterday?" → `dci list-anomalies`, `dci get-anomaly <id>`, then a scoped `dci query`
- "Are we on track for this month's budgets?" → `dci list-budgets` (utilization, forecast)
- "What does service X cost per environment?" → `dci query` grouped by the label, `--pivot` for humans
- "Hand this to a human" → `dci open report <id>` (prints the console deep link in agent mode)

## Report Drill-Down

```bash
dci list-reports --output toon
dci get-report <report-id> --output toon
dci get-report-config <report-id> --output toon
```

Override the time range when supported:

```bash
dci get-report <report-id> --time-range P30D --output toon
```

## Resource Names

Get a resource by (partial) name instead of an ID:

```bash
dci get-report "monthly aws" --output toon
```

An ambiguous name fails with `NAME_AMBIGUOUS` (exit 2) listing candidates — re-run with the intended ID:

```text
error: "monthly" matches 3 reports: Monthly AWS Spend (<report-id-1>), Monthly GCP Spend (<report-id-2>), Monthly Azure Spend (<report-id-3>)
```

```bash
dci get-report <report-id-1> --output toon
```

Delete by name — preview, confirm the resolved target, then re-run with the ID:

```bash
dci delete-report "monthly aws" --dry-run
# confirmation shows: delete-report targets report "Monthly AWS Spend" (<report-id>)
dci delete-report <report-id> --yes   # only after user approval
```

## Safe Mutation Templates

Draft first, then confirm with the user before running live changes.

```bash
dci create-budget < budget-create.json
dci update-alert <alert-id> < alert-update.json
dci delete-budget <budget-id>
dci invite-user email: user@example.com, organizationId: <org-id>, roleId: <role-id>
```

## CloudFlow Export and Import

Copy a flow between tenants. `export-cloudflow-flow` writes a portable bundle —
the flow plus every subflow it references, with no credentials, tenant IDs,
schedules, or execution state. It defaults to JSON output because the bundle is
a document meant to be saved and replayed, so redirecting it to a file is safe:

```bash
dci export-cloudflow-flow <flow-id> > bundle.json

# strip variable values; names, types, and required-ness always travel
dci export-cloudflow-flow <flow-id> --include-variable-values=false > bundle.json
```

`export-cloudflow-flow` takes a literal flow ID, not a flow name — get it from
`dci list-cloudflows`.

Import is create-only: each call creates new **draft** flows with new IDs.
Nothing is published and no schedule activates until the target tenant
publishes. The bundle can be piped straight in — the CLI nests it under the
request's `bundle` field for you. A real import needs `--idempotency-key`;
generate a fresh one per import (a dry run gets one automatically):

```bash
# validate first — writes nothing, returns an import plan
dci import-cloudflow-flow --dry-run < bundle.json

# then import for real
dci import-cloudflow-flow --idempotency-key "$(uuidgen)" < bundle.json
```

The import lands in the authenticated tenant. Doers targeting another tenant
add `-D <customer-domain>` to both commands.

Always dry-run first. The plan lists each requirement the bundle declares
(connections, Datastore tables, global variables) with its resolution —
`bound`, `suggested`, `willCreate`, or `unbound` — plus candidate IDs in the
target tenant, the flows that would be created, and every validation error at
once.

Unbound connections and Datastore tables leave the referencing nodes flagged
incomplete; unbound global variables are auto-created. Policy and Slack-channel
references cannot travel between tenants at all — export records them as
unsupported references and import flags those nodes incomplete. Fix incomplete
nodes in the builder before publishing.

To bind requirements to target-tenant resources, or to set import options, send
the full request shape instead of a bare bundle:

```bash
jq '{bundle: ., bindings: {connections: {"<requirement-key>": "<connection-id>"}}, options: {createMissingTables: true, namePrefix: "Copy of "}}' \
  bundle.json | dci import-cloudflow-flow --idempotency-key "$(uuidgen)"
```

Requirement keys come from the dry-run plan (or the bundle's own
`requirements`); candidate IDs come from `dci list-cloudflow-connections`.

## Ava (AI Assistant)

Agents should prefer `ask-ava-sync` over `ask-ava-streaming`. The sync endpoint returns clean JSON; streaming returns raw SSE chunks mixed with internal lifecycle events.

One-shot question (recommended for agents):

```bash
dci ask-ava-sync ephemeral: true, question: "What are my top 3 cost drivers this month?" --output toon
```

Response shape:

```json
{
  "answer": "Your top 3 services are ...",
}
```

Multi-turn conversation (set `ephemeral: false` to get a `conversationId`):

```bash
dci ask-ava-sync ephemeral: false, question: "What are my top cost drivers?" --output toon
# response includes "conversationId": "<conversation-id>"

dci ask-ava-sync ephemeral: false, conversationId: <conversation-id>, question: "Break down EC2 by region" --output toon
```

Delete a conversation when done:

```bash
dci delete-ava-conversation --conversation-id <conversation-id>
```

Note: `delete-ava-conversation` uses a `--conversation-id` flag, not a positional argument.

Feedback (requires `answerId` which only `ask-ava-streaming` returns):

```bash
dci ava-feedback answerId: <answer-id>, conversationId: <conversation-id>, feedback{positive: true, text: "Useful summary"}
```

## Troubleshooting Pattern

Use this order:

1. `dci status`
2. `dci --help` or `dci <command> --help`
3. `DCI_CUSTOMER_CONTEXT=<customer-context> dci <read-only-command>`
4. Explain whether the failure looks like auth, permissions, missing context, or command-shape error
