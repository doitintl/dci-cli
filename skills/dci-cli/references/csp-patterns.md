# CSP: The All-Customers Tenant (DoiT Employees Only)

**Applies only to doer (DoiT employee) accounts.** For customers and partners the CSP
tenant simply returns an authorization error — skip this file entirely.

The CSP tenant (`csp.doit.com`, customer id `CIgtnEximnd4fevT3qIU`) aggregates every
DoiT customer's billing data. Use it for questions that span multiple customers or an
account team's book of business; switch to the specific customer's own tenant for
anything deeper.

```bash
DCI_CUSTOMER_CONTEXT=csp.doit.com dci query < query.json
```

## CSP-Only Dimensions (all type `fixed`)

| id | meaning |
|---|---|
| `csp_primary_domain` | The customer (one value per customer) |
| `csp_strategic_account_manager` | AM/SAM — values are doer emails |
| `csp_customer_success_manager` | CSM — doer emails |
| `csp_field_sales_representative` | FSR — doer emails |
| `csp_technical_account_manager` | TAM/FDE — doer emails |
| `csp_territory`, `csp_payee_country`, `csp_payer_country` | Geography |
| `customer_type`, `csp_classification`, `csp_dci_tier` | Segmentation |
| `csp_committed` | String `"true"`/`"false"` plus nulls |

Book-of-business questions ("which of my customers…") = filter the relevant role
dimension to the user's email. Default to `csp_strategic_account_manager` and say so;
offer the other roles if the result looks empty. A large empty-string bucket in any
role dimension is unassigned spend — exclude it and say you did.

## Hard Constraints

- **No customer labels, tags, or project labels**, and no resource-level dimensions
  (no `resource_id`, cluster, or Kubernetes dims). CSP is aggregated data.
- **Never group by a `system_label` in CSP.** They appear in `list-dimensions` but the
  queries time out server-side (HTTP 524). For label-level detail, switch to the
  customer's own tenant.
- **Cold unfiltered CSP queries take 1–2 minutes**; repeats of the same query return in
  seconds, and a dimension `filter` (e.g. one AM's book) cuts a cold query to seconds
  too. Prefer filtered queries; scope with one cheap query first (1 month, one group
  dimension, top-N limit, `metricFilter`), then refine.

## Worked Example — Fastest-Growing Customers in a Book

3 monthly buckets grouped by customer, filtered to one AM, then compare months:

```json
{
  "config": {
    "dataSource": "billing",
    "group": [
      {"id": "csp_primary_domain", "type": "fixed",
       "limit": {"metric": {"type": "basic", "value": "cost"}, "sort": "desc", "value": 20}}
    ],
    "filters": [
      {"id": "csp_strategic_account_manager", "type": "fixed", "values": ["<doer-email>"]}
    ],
    "metrics": [{"type": "basic", "value": "cost"}],
    "metricFilter": {"metric": {"type": "basic", "value": "cost"}, "operator": "gt", "values": [100]},
    "timeInterval": "month",
    "timeRange": {"mode": "last", "amount": 3, "unit": "month", "includeCurrent": false}
  }
}
```

Attribute every figure to the CSP tenant explicitly, and switch back to the previous
context when done.
