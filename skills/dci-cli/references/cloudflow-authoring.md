# CloudFlow Flow Authoring

Turn a natural-language specification into a CloudFlow flow by producing a portable
`FlowBundle` JSON and importing it as a **draft** — nothing runs or publishes until a human
publishes in the builder, so authoring never needs to be perfect to be useful. Two
complementary paths; pick per task:

- **Server-side NL builder** — `dci build-cloud-flow` creates a new draft flow from a prompt;
  `dci refine-cloud-flow <flow-id>` modifies an existing one. Body: `question` (required),
  `conversationId` (optional, to continue iterating). Both stream raw build events (SSE).
  Fastest first draft when the flow can live in the authenticated tenant directly.
- **Local bundle authoring** — export → edit → import (workflow in
  [examples.md](examples.md), "CloudFlow Export and Import"). Produces reviewable, diffable
  JSON; the only path when the change must be inspected before it exists, or when copying
  across tenants.

**Prefer clone-and-edit over generation from scratch.** Find the closest existing flow
(`dci list-cloudflows`), export it, and modify the bundle. A real export anchors every syntax
detail — node parameter shapes, in-node references to upstream outputs, trigger config — that
generation would otherwise have to guess.

## Hard rules

- **Never invent node parameters or in-node reference syntax.** Parameter shapes and the
  tokens a node uses to reference an upstream node's output are copied from a real export of a
  flow that uses the same node type — never written from memory. No similar flow to clone?
  Build the graph skeleton (nodes, transitions, requirements) with parameters left minimal,
  import as draft, and tell the user which nodes to finish in the builder. A draft with honest
  gaps beats a plausible-looking flow with guessed field names.
- **Verify IDs the user supplies before building around them** — a report ID with
  `dci list-reports --search <name-or-id>` or `-C id,reportName` (not `get-report`, which
  executes the report query — expensive and quota-bound), a flow ID with
  `dci list-cloudflows -C id,name`, a connection with `dci list-cloudflow-connections`. IDs in
  a prompt are claims, not facts.
- **Always dry-run first.** `dci import-cloudflow-flow --dry-run < bundle.json` writes nothing
  and returns every validation error at once, plus each requirement's resolution and candidate
  IDs. Fix and repeat until the plan is clean, then import for real with a fresh
  `--idempotency-key`.
- **Do not fabricate what a bundle cannot carry**: credentials, tenant IDs, schedules,
  execution state, Slack-channel and policy references never travel (the last two export as
  `unsupportedReferences` and leave nodes flagged incomplete).

## Authoring pipeline

1. **Ground** — establish the target tenant first. **Doer caveat**: CloudFlow endpoints
   currently reject customer-context impersonation (`tenant_id_mismatch` — the tenant must
   match the bearer token), so `-D`/`DCI_CUSTOMER_CONTEXT` work for analytics grounding but
   NOT for `cloudflow` commands; flows land in the token's own tenant. Then inventory what
   exists there: `dci list-cloudflow-connections` (which clouds are even connected — this
   resolves "save the file" to S3 vs GCS without a question),
   `dci list-cloudflows -C id,name` (a clone candidate? the default columns omit the `id`
   that `export-cloudflow-flow` needs), `dci list-cloudflow-templates`. Verify any IDs from
   the prompt.
2. **Classify** the request into an archetype (below) — composition becomes "which shape, what
   fills the slots".
3. **Clarify** only genuine ambiguities, in plain language ("you have AWS and GCP connected —
   where should this go?"). The tenant inventory answers most of them silently.
4. **Confirm the plan in prose before writing JSON** — one sentence per node, e.g. "Every
   Monday: run report X, transform rows to events, push to DataHub, post the run link to
   #finops. Correct?" The user vets intent at the cost of a sentence.
5. **Resolve and assemble** — clone-and-edit the bundle per the hard rules.
6. **Dry-run → fix → import**, then report: flows created, requirements bound or left unbound,
   and exactly which incomplete nodes need finishing in the builder before publish.

## Bundle anatomy

Authoritative schemas: `dci export-cloudflow-flow --help-full` and
`dci import-cloudflow-flow --help-full`. The shape in brief:

- Top level: `kind: "cloudflow.doit.com/FlowBundle"`, `schemaVersion: 1`, `rootFlow` (key into
  `flows`), `flows` (root flow plus its subflows, max 20), `requirements`.
- Flow: `key`, `name`, `triggerType`, `firstNode` (entry node key), `nodes` (max 150),
  `localVariables` (max 50), `unsupportedReferences`.
- Node: `key`, `name`, `type` (observed in real exports: `manualTrigger`, `triggerNode`,
  `actionNode`, `filterNode`, `transformation`, `codeNode`, `httpNode`, `datastoreNode` —
  validated at import), `parameters` (shapes below; tenant-scoped values appear as tokens),
  `approval` (recipients stripped), `transitions` (`target` = next node's key, optional
  `label`/`pathId` for branch paths). The graph is nodes + transitions from `firstNode`.

**In-node wiring (confirmed against real exports, 2026-08-26).** A reference to an upstream
node's data is a structured object, not a template string:
`{"referencedNodeId": "<node-key>", "referencedField": ["path", "to", "field"], "type": "output"}`
(`"type": "input"` reads what that node was sent instead of what it produced). An API call
node (`actionNode`) carries:

```json
"parameters": {
  "provider": "DoiT",
  "operation": {"id": "getReport", "provider": "DoiT", "service": "Reports", "version": "v1"},
  "configurationValues": {},
  "formValues": {"id": "<literal or reference object>"}
}
```

`formValues` holds the API's request parameters (literals or reference objects, nested to any
depth); `configurationValues` holds connection/environment inputs (an AWS `accountId`, a GCP
`serviceAccountResource`). `filterNode` uses `conditionGroups` → `conditions`
(`{field, comparisonOperator, type: "STATIC", value}`); `transformation` uses a
`transformations` array (`concatenation`, `extract`, …) over a referenced field. These shapes
are stable, but per the hard rules, still copy the details from a real export of the same node
type.
- Tokens inside `parameters` and variable values: `$req:<section>/<key>` points into
  `requirements` (`connections`, `datastoreTables`, `globalVariables`);
  `$bundle:flows/<flowKey>` points at a subflow in the same bundle. Every tenant-scoped
  reference goes through one of these — a literal connection or table ID inside a bundle is a
  bug.
- Import request: `{bundle, bindings, options}` — `bindings` maps requirement keys to
  target-tenant resource IDs (dry-run lists keys and candidates); `options` offers
  `createMissingTables` and `namePrefix`. A bare bundle piped to `import-cloudflow-flow` is
  wrapped automatically; use the full shape when you need bindings or options.

## Archetypes

Most requests are one of these shapes; clone the nearest real flow of the same shape:

- **Scheduled report → destination**: schedule trigger → DoiT report/query → transform →
  storage or notification.
- **Event → notification**: event/webhook trigger → filter → notification (Slack/email).
- **Threshold → remediation**: schedule or event trigger → fetch state → filter/branch on
  condition → cloud action (with `approval` on the destructive node) → notification.
- **Fetch → transform → ingest**: trigger → fetch (DoiT or cloud API) → transform rows to the
  destination's event shape → Datastore/DataHub write. Mind row volume: batch or page rather
  than assuming one call.
- **Data → LLM → route**: fetch → LLM node with a prompt → branch on the answer.

## Current limitation

Operation search and per-operation parameter-schema retrieval are not yet public API — until
they are, exported flows are the only ground truth for API-node parameters (hence the hard
rules). When those endpoints land, resolve each API node by searching for the operation and
fetching its schema instead of hunting for a clone; the hard rules relax to "never write
parameters a schema didn't confirm".
