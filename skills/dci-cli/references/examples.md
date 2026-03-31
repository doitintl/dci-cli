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
dci list-alerts --output json
dci get-alert <alert-id> --output json
dci list-dimensions --output json
dci list-users --output json
dci list-platforms --output json
```

## Query Examples

Quick SQL shorthand:

```bash
dci query body.query:"SELECT * FROM <billing-table> LIMIT 10" --output json
```

Service aggregation with SQL shorthand:

```bash
dci query body.query:"SELECT service_description, SUM(cost) AS total_cost FROM <billing-table> GROUP BY 1 ORDER BY 2 DESC LIMIT 10" --output json
```

Structured JSON query:

```bash
dci query < query.json
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

## Report Drill-Down

```bash
dci list-reports --output json
dci get-report <report-id> --output json
dci get-report-config <report-id> --output json
```

Override the time range when supported:

```bash
dci get-report <report-id> --time-range P30D --output json
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
dci ask-ava-sync ephemeral: true, question: "What are my top 3 cost drivers this month?" --output json
```

Response shape:

```json
{
  "answer": "Your top 3 services are ...",
}
```

Multi-turn conversation (set `ephemeral: false` to get a `conversationId`):

```bash
dci ask-ava-sync ephemeral: false, question: "What are my top cost drivers?" --output json
# response includes "conversationId": "<conversation-id>"

dci ask-ava-sync ephemeral: false, conversationId: <conversation-id>, question: "Break down EC2 by region" --output json
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
