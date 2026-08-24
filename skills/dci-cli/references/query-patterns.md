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

## Grouping by Labels & Dimension Discovery

Label-scoped analysis (team/env/product tags, `system_label` taxonomies like `genai/*`) has three sharp edges:

1. **Discovery.** `list-dimensions` can hold ~1,000 dimensions across ~20 pages, and its `--filter` only does exact `field:value` matches — it cannot search. To find dimensions for a topic, use the client-side substring search (implies `--all`; a `searchDropped` marker records how many items were filtered out):

   ```bash
   dci list-dimensions --search '<topic>'
   ```

2. **The null-group bucket.** Grouping all billing data by a sparse label yields one giant row where the label is null — every unlabeled cost aggregated together, often dwarfing the labeled rows. There is no server-side "label exists" filter (`regexp` filters do not exclude the bucket). Pass `--drop-unlabeled-rows` to remove it (and `[Value N/A]` rows) in the CLI; leave it off when unlabeled spend is what you're measuring.

3. **Inconsistent null markers.** Absent group values render as empty strings in some rows and as the literal `[Value N/A]` in others; treat both as null.

Worked example — spend by AI model from `genai/*` system labels (available on accounts with GenAI cost data):

```json
{
  "config": {
    "dataSource": "billing",
    "group": [
      {"id": "genai/model", "type": "system_label"},
      {"id": "genai/cost_type", "type": "system_label"},
      {"id": "genai/usage_type", "type": "system_label"},
      {"id": "genai/billing_category", "type": "system_label"}
    ],
    "metrics": [{"type": "basic", "value": "cost"}],
    "metricFilter": {"metric": {"type": "basic", "value": "cost"}, "operator": "gt", "values": [0]},
    "timeInterval": "month",
    "timeRange": {"mode": "last", "amount": 1, "unit": "month", "includeCurrent": false}
  }
}
```

Provider caveats for token-spend classification: Anthropic rows mark tokens via `genai/cost_type = tokens`; OpenAI rows only via `genai/usage_type` (`Input`/`Output`/cache values); Cursor rows only via `genai/billing_category` (`Usage-based` is billed spend; `Included in Business`, `Errored, Not Charged`, and `Free` are not; `User API Key` is billed elsewhere — skip it to avoid double counting). Model names are not normalized across sources (`Claude Opus 4.8` vs `claude-opus-4-8` vs effort-suffixed variants like `...-thinking-high`) — normalize client-side before aggregating per model.

The `metricFilter` above is also the answer to zero-noise rows: multi-label grouping emits every observed combination, including structurally-zero ones; filtering `cost > 0` server-side removes them before they cross the wire.

## Recommended Agent Behavior

- Translate SQL-oriented prompts only when their analysis goal is representable, and state any semantic differences.
- Do not present an aggregated report config as equivalent to raw-row SQL inspection.
- Use JSON for reusable cost reports, dashboards, and 30-day optimization workflows.
- Prefer `--output toon` (compact, token-efficient; the agent-mode default). Use `--output json` when you need standard JSON, e.g. to pipe into `jq`.
- Check `rowCount` / `rowsTotal` before loading full results into context.
- Use env-scoped `DCI_CUSTOMER_CONTEXT=<customer-context>` if a customer switch is needed temporarily.
