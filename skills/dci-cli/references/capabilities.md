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
│   ├── completion
│   └── customer-context
├── Discovery and Metadata
│   ├── validate
│   ├── list-dimensions
│   ├── get-dimensions
│   ├── list-organizations
│   ├── list-platforms
│   ├── list-products
│   ├── list-roles
│   ├── list-users
│   └── list-account-team
├── Analytics
│   ├── Alerts
│   ├── Budgets
│   ├── Reports
│   ├── Allocations
│   ├── Labels
│   ├── Annotations
│   ├── Sharing
│   └── Anomalies
├── Billing and Operations
│   ├── Invoices
│   ├── Cloud Incidents
│   ├── Assets
│   ├── Support Requests
│   ├── Cloud Diagrams
│   └── Commitment Manager
├── DataHub
└── Ava
```

## Working Rules

- Prefer `--output json` for agent workflows.
- Run `dci <command> --help` before drafting complex request bodies.
- Prefer read-only commands before mutation.
- Treat auth, permissions, and `customerContext` as separate concerns.
