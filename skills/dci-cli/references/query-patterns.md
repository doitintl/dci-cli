# DCI Query Patterns

Use this file when the task centers on `dci query`.

## Query Model

`dci query` runs a Cloud Analytics report query without persisting it. Its request body contains a `config` object with metrics, grouping, time ranges, filters, and display options. It does not accept SQL strings.

When a user asks for SQL or references an old `body.query:"SELECT ..."` example, translate the intent into a report `config` instead.

## Structured JSON Query

```bash
dci query < query.json
```

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

## Recommended Agent Behavior

- Translate SQL-oriented prompts into Cloud Analytics report configs.
- Use JSON for reusable cost reports, dashboards, and 30-day optimization workflows.
- Prefer `--output toon` (compact, token-efficient; the agent-mode default). Use `--output json` when you need standard JSON, e.g. to pipe into `jq`.
- Use env-scoped `DCI_CUSTOMER_CONTEXT=<customer-context>` if a customer switch is needed temporarily.
