# DCI Query Patterns

Use this file when the task centers on `dci query`.

## Query Model

`dci query` runs a Cloud Analytics report query without persisting it. Its request body contains a `config` object with metrics, grouping, time ranges, filters, and display options. It does not accept SQL strings.

When a user asks for SQL, raw billing rows, or references an old `body.query:"SELECT ..."` example, explain that SQL and raw-row inspection are unsupported. Translate the request only when its analysis goal can be represented by a Cloud Analytics report config. State any semantic differences explicitly, or ask the user to clarify the analysis goal.

## Structured JSON Query

```bash
dci query < query.json
```

`query.json` — top 10 services by cost over the last 30 days:

```json
{
  "config": {
    "dataSource": "billing",
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
    "metricFilter": {
      "metric": { "type": "basic", "value": "cost" },
      "operator": "gt",
      "values": [0.1]
    },
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

Practical rules:

- **Always set a group `limit`.** An unlimited grouped query over 30 days can return thousands
  of rows; agent mode caps output at 500 rows (`rowsOmitted` marks truncation) but the query
  itself still does the full work.
- **Add a `metricFilter`** (e.g. cost > 0.1) to drop zero-cost noise rows.
- Use `timeInterval: "month"` for trend questions — far fewer rows than daily granularity.
- Use `--rows keyed` when consuming results programmatically (`result.rows` become objects
  keyed by schema names).
- Use `--pivot --output table` when presenting results to a human.
- Use `--output csv` for spreadsheet export.

## Recommended Agent Behavior

- Translate SQL-oriented prompts only when their analysis goal is representable, and state any semantic differences.
- Do not present an aggregated report config as equivalent to raw-row SQL inspection.
- Use JSON for reusable cost reports, dashboards, and 30-day optimization workflows.
- Prefer `--output toon` (compact, token-efficient; the agent-mode default). Use `--output json` when you need standard JSON, e.g. to pipe into `jq`.
- Check `rowCount` / `rowsTotal` before loading full results into context.
- Use env-scoped `DCI_CUSTOMER_CONTEXT=<customer-context>` if a customer switch is needed temporarily.
