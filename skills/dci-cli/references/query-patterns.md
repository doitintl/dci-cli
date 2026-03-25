# DCI Query Patterns

Use this file when the task centers on `dci query`.

## Choose the Query Mode

Use inline SQL shorthand when:

- the user explicitly asks for SQL
- the user wants a quick inspection of billing rows
- the user references examples like `SELECT * FROM aws_cur_2_0 LIMIT 10`

Use stdin JSON when:

- the query needs Cloud Analytics-native metrics, grouping, time ranges, filters, or display options
- the query should be portable and easy to version
- the user is asking for a structured report rather than a raw table query

## SQL Shorthand

Quick inspection:

```bash
dci query body.query:"SELECT * FROM <billing-table> LIMIT 10" --output json
```

Top services by spend:

```bash
dci query body.query:"SELECT service_description, SUM(cost) AS total_cost FROM <billing-table> GROUP BY 1 ORDER BY 2 DESC LIMIT 10" --output json
```

Practical notes:

- Keep SQL examples short and copyable.
- Use placeholders such as `<billing-table>` in shared docs.
- Explain that actual table names are tenant- and data-source-dependent.
- If quoting becomes messy, switch to a file or shell variable instead of inventing escaping tricks inline.

## Structured JSON Query

Use JSON when the request is really a Cloud Analytics report query.

Example:

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

- Offer SQL shorthand first only when the user asks for SQL or wants a very quick billing-table check.
- Offer JSON first for reusable cost reports, dashboards, or 30-day optimization workflows.
- Prefer `--output json`.
- Use env-scoped `DCI_CUSTOMER_CONTEXT=<customer-context>` if a customer switch is needed temporarily.
