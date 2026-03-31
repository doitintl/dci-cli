# Cost Optimization Example

This is an anonymized, reusable version of the 30-day cost optimization walkthrough.

## Goal

Find the top 3 cost optimization opportunities with the biggest likely impact for a single customer context.

## Safe Context Handling

Prefer a temporary override:

```bash
DCI_CUSTOMER_CONTEXT=<customer-context> dci status
```

Do not assume the local saved context should be overwritten.

## Fast Path: SQL Shorthand

Use SQL shorthand when the user wants a quick billing-table pass:

```bash
dci query body.query:"SELECT service_description, SUM(cost) AS total_cost FROM <billing-table> GROUP BY 1 ORDER BY 2 DESC LIMIT 10" --output json
```

Use this to identify the largest service pools quickly.

## Structured Path: JSON Report Query

Use JSON when you want a portable, report-like query:

```bash
cat >/tmp/dci-cost-query.json <<'EOF'
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
EOF

DCI_CUSTOMER_CONTEXT=<customer-context> dci query < /tmp/dci-cost-query.json --output json
```

Aggregate the rows:

```bash
DCI_CUSTOMER_CONTEXT=<customer-context> dci query < /tmp/dci-cost-query.json --output json | jq '.result.rows | group_by(.[0]) | map({service: .[0][0], total_cost: (map(.[4]) | add)}) | sort_by(-.total_cost) | .[:10]'
```

## How To Write The Top 3

Pick actionable categories, not generic buckets. Good examples:

- database rightsizing and storage optimization
- compute fleet efficiency plus commitment review
- AI / model-mix governance

For each recommendation:

1. cite the cost concentration
2. explain why it is likely actionable
3. suggest the next drill-down command or report
4. label savings numbers as estimates, not guarantees

Avoid using catch-all lines such as `Other services` as a top recommendation unless you unpack them first.
