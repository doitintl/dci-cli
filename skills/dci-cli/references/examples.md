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

## Safe Mutation Templates

Draft first, then confirm with the user before running live changes.

```bash
dci create-budget < budget-create.json
dci update-alert <alert-id> < alert-update.json
dci delete-budget <budget-id>
dci invite-user email: user@example.com, organizationId: <org-id>, roleId: <role-id>
```

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
