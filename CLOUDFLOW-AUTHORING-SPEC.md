# Design spec: agent-authored CloudFlow flows from natural language

Status: **draft for maintainer review**.

Scope: how an AI agent (Claude, ChatGPT, or any tool-capable model) turns a natural-language
specification — including one written by a non-technical user — into a correct CloudFlow
`FlowBundle` JSON that imports cleanly through `dci import-cloudflow-flow`. Covers the knowledge
layer (skill content), the retrieval layer (new read-only API endpoints backed by the existing
Firestore API-model store), the verification layer (dry-run), and the end-to-end authoring
pipeline. Grounded in a live inspection of the dev Firestore model store (2026-08-25,
`doitintl-cmp-dev`, `(default)` database) — every claim about stored models cites what was read.

Out of scope: implementing the API endpoints themselves (they live in the platform backend, not
this repo — this spec is the coordination document); replacing the Console's own NL builder
(`buildCloudFlow`/`refineCloudFlow`, §7 — complementary, not competing); fine-tuning or any
bespoke model training — **decided against**: the model store evolves with the product, and a
retrieval loop tracks that evolution for free while a tuned model goes stale on every change.

---

## 1. Summary

Three layers, each solving the failure mode the previous one can't:

| Layer | Solves | Mechanism |
|---|---|---|
| Knowledge (skill) | Flow anatomy, wiring, workflow discipline | `references/cloudflow-authoring.md` in the shipped `dci-cli` skill |
| Retrieval (API + CLI) | Exact operation parameters — no guessing | Operation search + schema endpoints over the Firestore model store |
| Verification (dry-run) | Everything that still slips through | `import-cloudflow-flow --dry-run` as the agent's error loop |

The agent's loop: **ground** (tenant connections, existing flows) → **classify** (flow archetype)
→ **clarify** (only real ambiguities, in plain language) → **confirm plan** (prose, before any
JSON) → **resolve each API node** (search operation → fetch schema → fill scaffold) → **assemble
bundle** → **dry-run** → **fix** → **import as draft**. Import is create-only and lands as a
draft, so the model never needs to be perfect: unbound references flag nodes incomplete and a
human finishes them in the builder before publishing.

## 2. What exists today

- `export-cloudflow-flow` / `import-cloudflow-flow` with `--dry-run`: the dry-run plan returns
  *every* validation error at once plus requirement resolutions (`bound`/`suggested`/
  `willCreate`/`unbound`) with candidate IDs — already an agent-shaped error report. The CLI
  auto-wraps a bare bundle under the request's `bundle` field (`wrapBareCloudflowBundle`,
  `body_validation.go`).
- The shipped `dci-cli` skill (`skills/dci-cli/`, installed and updated via `dci skill`) already
  documents the export/import workflow (`references/examples.md`) and ships an OpenAI agent
  manifest (`agents/openai.yaml`) — distribution to both Claude and ChatGPT surfaces is solved.
- The platform stores a **normalized, cross-provider API model catalog** in Firestore (§3) —
  the same models the CloudFlow builder UI uses to render parameter forms.
- The public API spec already defines server-side NL endpoints: `buildCloudFlow`
  (`POST /cloudflow/v1/flows/actions/build`) and `refineCloudFlow`
  (`POST /cloudflow/v1/flows/{flowId}/actions/refine`), both streaming build events. The CLI
  does not expose them yet (§7).

## 3. The Firestore model store (as inspected)

Path: `cloudflowEngine/integrations/apis/{provider}/services/{service}/versions/{version}/`
with subcollections `operations`, `models`, `waiters`. Note: the `apis` and `triggers` parents
are virtual documents — enumerating them requires `showMissing=true`.

Seven providers, uniformly structured: **AWS, GCP, Azure, Oracle, Anthropic, OpenAI, DoiT**
(the platform's own API is itself a node provider). AWS alone has 300+ services;
`triggers/sources` currently holds `systemEvents`.

An operation document (inspected: `AWS/services/S3/versions/2006-03-01/operations/PutObject`)
carries:

| Field | Content | Why it matters for agents |
|---|---|---|
| `description` | Curated, concise, imperative ("Add an object to an S3 bucket. … Requires `s3:PutObject` permission.") | Written for exactly this use — capability search and agent context. Not the raw AWS doc. |
| `searchVector` | **768-dim embedding** | Semantic operation search is already provisioned server-side — the discovery problem (§6) has infrastructure waiting. |
| `parameters` | Connection/environment-level inputs (`accountId` for AWS; `serviceAccountResource` for GCP) | Distinct from the API payload — a node needs both, and the split must be explicit in the schema endpoint response. |
| `inputModel` / `outputModel` | Shape names resolved in the sibling `models` collection (`PutObjectRequest` / `PutObjectOutput`) | The actual request/response schemas. |
| `type` | `rpc` (AWS) / `http` (GCP) | Provider-specific invocation styles, normalized. |
| `documentation` | Raw provider HTML | Bulky; strip or summarize for agent responses. |
| GCP extras | `scopes`, `permissions`, `resource` | Lets the agent tell the user what a connection must be able to do. |

Shape documents (`models/{ShapeName}`) are **fully dereferenced inline** — botocore-style
`type`/`members`/`requiredMembers`, with enums, maps, and nested structures embedded, each
member carrying provider `documentation` HTML plus a `model`. Measured on `PutObjectRequest`
(41 members, required `[Bucket, Key]`): 64 KB raw Firestore JSON, **2.9 KB with documentation
stripped**. The schema-to-docs ratio is ~1:10, so a docs-stripped JSON Schema translation is
token-cheap even for large operations. Output shapes matter too: the agent needs `outputModel`
to know what fields a downstream node can reference from an upstream node's result.

Two implications worth calling out:

1. **Mechanical translation to JSON Schema is straightforward** — the store is already
   normalized and dereferenced; the endpoint (§5) is a format shim plus doc-stripping, not a
   modeling project.
2. **The embeddings change the discovery answer** (§6): capability search ("upload a file")
   can be vector search over `searchVector` rather than keyword-only.

## 4. Knowledge layer: `references/cloudflow-authoring.md`

New reference in the shipped skill, wired into `SKILL.md` the way `query-patterns.md` is:

- **Bundle anatomy**: `kind: "cloudflow.doit.com/FlowBundle"`, `schemaVersion`, flows →
  nodes/edges, triggers, `requirements` and how requirement keys map to import `bindings`.
- **Wiring**: how a node references an upstream node's output (the data-passing syntax),
  conditions/branches, loops. This layer is CloudFlow-proprietary, small, and stable — schemas
  can't teach it; annotated examples must.
- **Flow archetypes**: the recurring shapes (scheduled report → destination; event →
  notification; threshold → remediation; fetch → transform → Datastore; data → LLM → route on
  the answer), each with its node graph sketched and slots marked. Curated from real exports
  via `export-cloudflow-flow --include-variable-values=false`.
- **What never travels / must not be generated**: credentials, tenant IDs, schedules, execution
  state, Slack-channel and policy references.
- **The hard rule**: *never write an API node's parameters from memory — search the operation,
  fetch its schema, fill against it, dry-run.* Models know the mainstream cloud APIs well
  enough to be dangerous; the rule makes retrieval non-optional so the long tail doesn't ship
  guessed field names.
- **The authoring pipeline** (§6), verbatim commands included.
- **Prefer modify over create**: when a similar flow exists, export → edit → import anchors
  everything and beats de-novo generation.

## 5. Retrieval layer: two read-only API endpoints

Exposed through the DCI API, so they become locked-down CLI commands and MCP/Custom-GPT actions
with no per-surface work.

**Operation search** — `dci list-cloudflow-operations --provider aws --search "upload file"`.
Backed by vector search over `searchVector` (falling back to keyword over
name + `description`). Returns operation IDs, the curated one-line descriptions, and
service/provider context. Keyword-stuffing is unnecessary; the embeddings already exist.

**Operation schema** — `dci get-cloudflow-operation-schema aws s3 PutObject`. Returns:

- the **connection-level `parameters`** and the **`inputModel` translated to JSON Schema**,
  clearly separated (the split observed in §3 — conflating them is the likeliest integration
  bug);
- documentation stripped by default (§3 measurements justify this); `--include-docs` restores
  plain-text summaries per field;
- **depth control**: top level with nested structures collapsed to stubs, expandable via
  `--field <path>` — some shapes (e.g. `ec2:RunInstances`) are enormous fully expanded;
- a **node scaffold**: ready-to-paste node JSON with correct node `type`, operation reference,
  connection requirement key, and required parameters stubbed. Fill-in-the-blanks beats
  synthesis, and the scaffold pins the node envelope, which no provider schema can teach;
- the `outputModel` schema on request (`--output-model`) so downstream wiring is grounded too;
- a `completeness` hint for the store's thin spots (polymorphic/union payloads, "body is
  whatever the resource type is" operations) so the agent knows when to ask instead of filling
  silence.

**Deep dry-run validation** (backend, same models): extend the import dry-run to validate each
API node's filled parameters against the store — unknown field, missing required member, enum
violation, type mismatch. Catches human-edited bundles too, and turns any residual guessing
into an in-loop, actionable error instead of a runtime failure after publish.

## 6. Discovery: from non-technical language to the right operation

Frontier models map intent → service → operation well for the mainstream surface (that
knowledge is abundant in training data); the risks are elsewhere, and each has a cheap fix:

| Risk | Fix |
|---|---|
| User names no cloud ("save the file") | **Ground in the tenant first**: `list-cloudflow-connections` — one AWS connection means S3 without a question; two clouds means one plain-language question. |
| Long-tail operations | Vector search over the curated descriptions (§5) — the embeddings are already stored. |
| Common intents deserve determinism | A small curated intent catalog (~50–100 rows: "store an object → s3:PutObject / storage.objects.insert / blob upload") in the skill; search is the fallback. |
| Open-ended composition | Classify into an archetype (§4) first; discovery becomes "which template, what fills the slots". |
| Wrong guess ships silently | **Confirm the plan in prose before generating JSON** — "Every Monday I'll run your cost report, upload the CSV to `finance-reports`, and post the link to #finops. Correct?" — the one party who knows the intent vets it at the cost of a sentence. |

## 7. Relationship to `buildCloudFlow` / `refineCloudFlow`

The platform already has a server-side NL builder streaming build events. Two front doors, one
need:

- **Server builder**: interactive Console UX, no local tooling, platform-controlled quality.
  The CLI should expose both endpoints (`dci build-cloudflow`, `dci refine-cloudflow`) once the
  operations are in the CLI's allowlist — SSE handling follows the `ask-ava-streaming`
  precedent.
- **Agent path (this spec)**: reviewable JSON artifacts, diffable in git, cross-tenant via
  export/import, composable into larger agent workflows, works in CI, and usable by any model
  the customer brings.

They converge underneath: the retrieval endpoints and deep dry-run validation (§5) improve
both — the server builder needs exactly the same operation search and parameter validation
internally. The skill should teach agents to consider `build-cloudflow` as a first draft
generator where available, then refine the exported JSON through the local loop.

## 8. Evals

Extend the skill's eval convention (`references/evals.md`):

- Prompts phrased as a **non-technical user** would ("make sure my team gets the cost report
  every week"), not as an engineer would.
- Assert on: chosen operations (catch `s3:PutBucketAcl` for "share the file"), dry-run plan
  cleanliness, declared requirements, and — where deep validation lands — zero parameter
  errors.
- Include cases whose reference answer requires parameters no model would guess (exotic enum
  values, deeply nested members): the regression test that retrieval is being used, not memory.

## 9. Sequencing

1. **Skill reference** (`cloudflow-authoring.md`): anatomy, wiring, archetypes from real
   exports, the hard rule, the pipeline. Ships value immediately — dry-run alone already closes
   the loop for structure-level errors. (This repo.)
2. **Schema endpoint**, AWS first (largest, most guess-prone), with scaffold + depth control.
   (Backend; CLI picks it up via the OpenAPI surface + allowlist.)
3. **Search endpoint** over the existing embeddings. (Backend.)
4. **Deep dry-run validation** against the models. (Backend.)
5. **Expose `build-cloudflow`/`refine-cloudflow`** in the CLI. (This repo, once allowlisted.)
6. **Evals** once failure modes stabilize.

## 10. Open questions

- Which embedding model produced `searchVector` (768-dim)? The search endpoint must embed
  queries with the same model; if it's an internal choice, the endpoint hides it — but the CLI
  spec should not assume client-side embedding.
- How thin is the store on polymorphic payloads (GCP/Azure "resource as body" operations)? The
  §5 `completeness` hint needs a survey to calibrate.
- Node envelope schema: is there a canonical machine-readable schema for node JSON (the
  envelope around the operation call), or is the builder UI the only source? If the latter, the
  scaffold in §5 is the way to pin it.
- Token budget for `--help-full` on `import-cloudflow-flow`: does the bundle schema in the
  OpenAPI spec go deep enough to be the authoritative anatomy reference, or does the skill
  carry that alone?
