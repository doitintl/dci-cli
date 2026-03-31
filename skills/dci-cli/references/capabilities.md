# DCI CLI Capabilities

Use this file when you need the command map, not the procedural guidance.

## Invocation Patterns

- Flags-only read commands: `dci list-alerts --output json`
- Positional-ID read commands: `dci get-alert <alert-id> --output json`
- Inline shorthand bodies: `dci invite-user email: user@example.com, organizationId: <org-id>, roleId: <role-id>`
- Inline SQL shorthand with `query`: `dci query body.query:"SELECT * FROM <billing-table> LIMIT 10"`
- Stdin JSON bodies: `dci query < query.json`

## Capability Tree

```text
dci
├── Session and Context
│   ├── status
│   ├── login
│   ├── logout
│   ├── completion {bash,fish,powershell,zsh}
│   └── customer-context {show,set,clear}
├── Discovery and Metadata
│   ├── validate
│   ├── list-dimensions / get-dimensions
│   ├── list-organizations
│   ├── list-platforms
│   ├── list-products
│   ├── list-roles
│   ├── list-users
│   └── list-account-team
├── Analytics
│   ├── Alerts: create-alert, get-alert, list-alerts, update-alert, delete-alert
│   ├── Budgets: create-budget, get-budget, list-budgets, update-budget, delete-budget
│   ├── Reports: create-report, get-report, get-report-config, list-reports, query, update-report, delete-report
│   ├── Allocations: create-allocation, get-allocation, list-allocations, update-allocation, delete-allocation
│   ├── Labels: create-label, get-label, list-labels, update-label, delete-label, get-label-assignments, assign-objects-to-label
│   ├── Annotations: create-annotation, get-annotation, list-annotations, update-annotation, delete-annotation
│   ├── Sharing: get-resource-permission, update-resource-permission
│   └── Anomalies: get-anomaly, list-anomalies
├── Billing and Operations
│   ├── Invoices: get-invoice, list-invoices
│   ├── Cloud Incidents: get-known-issue, list-known-issues
│   ├── Assets: create-asset, get-asset, id-of-asset, id-of-assets
│   ├── Support Requests: id-of-tickets, id-of-tickets-post, id-of-ticket-get, id-of-ticket-comments-list, id-of-ticket-comments-post
│   ├── Cloud Diagrams: find-cloud-diagrams
│   └── Commitment Manager: get-commitment, list-commitments
├── DataHub: create-datahub-dataset, get-datahub-dataset, list-datahub-datasets, update-datahub-dataset, delete-datahub-dataset, delete-datahub-datasets, datahub-events, datahub-events-csv-file, delete-datahub-events-by-filter
├── Ava: ask-ava-sync, ask-ava-streaming, ava-feedback, delete-ava-conversation
└── Skill: skill {claude,codex,kiro,gemini}
```

## Working Rules

- Prefer `--output json` for agent workflows.
- Run `dci <command> --help` before drafting complex request bodies.
- Prefer read-only commands before mutation.
- Treat auth, permissions, and `customerContext` as separate concerns.
