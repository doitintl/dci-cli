# Design spec: reliable CloudFlow generation from the MCP and CLI surfaces

Status: **draft for maintainer review**.

Scope: changes to **`doitintl/doit-mcp-server`** (the shared core the remote Worker wraps) and
**`doitintl/dci-cli`** that make an agent-generated CloudFlow flow land correctly on the first
pass. **Hard constraint: no platform/backend change.** Every proposal here is implementable in
the two client surfaces against the API exactly as it ships today.

Companion to [CLOUDFLOW-AUTHORING-SPEC.md](CLOUDFLOW-AUTHORING-SPEC.md), which covers the full
knowledge/retrieval/verification stack including backend work (operation search, schema
endpoints, deep dry-run validation via CMP-44169). This spec is deliberately the subset that
depends on nobody else's roadmap.

Out of scope, and named explicitly in §7 so nobody plans around them: deterministic flow update
and flow deletion. Both need endpoints that do not exist.

Grounded in a live end-to-end session (2026-09-04) that generated one trivial flow through the
MCP surface; every claim below cites either that session or a file in the two repos.

---

## 1. The evidence

The task: *manual trigger → fetch results of saved report `<id>` → Python node returning
`rowCount` and `firstRow`.* Three nodes, no branching, no external writes. About as small as a
flow gets.

What it actually cost, start to finish:

| | |
|---|---|
| Wall clock | ~45 minutes |
| MCP tool calls | ~15 |
| Flows created | **3** (one wanted, one duplicate, one throwaway probe) |
| Builder-generated code nodes that worked | **0 of 2** |
| Context spent on run-inspection payloads | ~120 KB across 4 calls |
| Passes needed before the flow produced correct data | **3** |

The failures, in order:

1. **`build_cloud_flow` exceeded the client's 60s cap and returned an error — while the work
   completed server-side anyway.** Flow row created at 11:52:34, fully built by 11:54:11. The
   tool reported failure at ~60s. Nothing in the error said the build was still running.
2. **`refine_cloudflow`, twice, did the same** — called ~11:55:45, landed 11:57:12; called
   ~12:00:15, landed 12:01:33. Consistently ~90s of work after the timeout.
3. Because a timeout is indistinguishable from a failure, the natural recovery — retry — is
   exactly wrong here. That is how one requested flow became three.
4. **Both builder-generated `codeNode`s were broken, and both failed silently.** The first
   defined `def handler(input, ...)` that nothing calls; the second read a bare `input`
   variable the runtime does not inject. Both nodes reported status `complete` and produced
   `{"message": null}`. `import --dryRun` passed. `test_run --dryRun` passed. The run
   succeeded. Only reading the node's output against a report I knew had ~240 rows revealed
   `rowCount: 0`.
5. Diagnosing it required **importing a throwaway flow whose only job was to dump the Python
   node's globals** — the only way to discover that upstream data arrives via `nodes`, keyed by
   node name.
6. `get_cloudflow_flow_run` returned the action node's full 27,751-byte report payload on
   every poll, four times, to read one 165-byte result.

**The finding that matters most:** step 5 was unnecessary. The `nodes` contract, the
`return`-don't-assign rule, and the required `schema` field are *already documented* — twice,
correctly, in prose better than what I derived. See §3.

## 2. What the surfaces look like today

`doitintl/doit-mcp-server` serves two tool families through one server
(`src/server.ts` `ListToolsRequestSchema` → `[...HAND_WRITTEN_TOOLS, ...generatedToolDefinitions]`):

| Family | Source | CloudFlow members | Naming |
|---|---|---|---|
| Hand-written | `src/tools/cloudflow.ts` (866 lines) | `build_cloud_flow`, `refine_cloudflow`, `trigger_cloud_flow`, connections, templates, `list_cloudflows` | curated ("Use this when the user wants to…") |
| Generated | `src/tools/generated/` from bundled `openapi.json` | `import_cloudflow_flow`, `export_cloudflow_flow`, `test_run_cloudflow_flow`, `list_cloudflow_flow_runs`, `get_cloudflow_flow_run` | `operationId` snake_cased (`generateTools.ts:8-14`) |

`doitintl/doit-external-api-mcp` is a second, thinner generator over the same spec, with a
`src/overrides.ts` description-override hook that is **currently empty**. Its
`src/customTools/` directory is the equivalent extension point.

`dci-cli` covers the same operations from the published spec, plus a real knowledge layer:
`skills/dci-cli/references/cloudflow-authoring.md` (221 lines), shipped and updated via
`dci skill`.

The complete CloudFlow API surface (from the bundled spec) is 16 operations. Notably absent:
**no flow delete, no flow update/patch.** Import is create-only; the only in-place mutation is
the NL `refineCloudFlow`. This is the structural reason a repair loop accumulates drafts (§7).

## 3. The core finding: the contract is written down twice and reaches the model zero times

Every runtime rule I spent 40 minutes rediscovering is already written, accurately, in two
places:

- `dci-cli`: `skills/dci-cli/references/cloudflow-authoring.md` lines 122-133 — the `nodes`
  dict keyed by node name, the top-level-`return` execution contract, the required `schema`,
  and even *both* broken builder conventions, dated 2026-09-02 and 2026-09-03.
- `doit-mcp-server`: `docs/cloudflow-authoring.md` (75 lines) — the same contract, written for
  exactly this audience. Its own opening says: *"The `dci` CLI ships a fuller version of this
  guidance; MCP clients get none of it otherwise."*

That sentence is correct and it is the bug. **`docs/cloudflow-authoring.md` is referenced by
nothing in `src/`** — verified by grep across the repo. And the server that would deliver it:

- declares the `resources` capability, then serves an empty list —
  `src/server.ts:208-212` returns `{ resources: [] }`;
- passes no `instructions` to the `Server` constructor (`src/server.ts:147-159`), so the
  protocol's own channel for "here is how to use this server" is unused;
- carries no CloudFlow guidance in any tool description.

The knowledge layer is written and costs nothing further to author. It is simply not wired to
an output. Fixing that is P1, and it is a day of work at most.

## 4. Proposals

Ordered by (value ÷ effort). P1–P3 would each, alone, have prevented a distinct failure in §1.

### P1 — Deliver the authoring contract to the model

**Symptom:** §1 steps 4–5. The agent authors code nodes from priors because nothing tells it
the runtime contract, and the failure is silent, so it does not learn from the result either.

**Change**, in `doit-mcp-server`, three layers deep-to-shallow — implement all three; they
reach different clients:

1. **Server `instructions`** (`src/server.ts:147`). The MCP SDK's `ServerOptions` carries an
   `instructions` string delivered at `initialize`; well-behaved clients place it in system
   context. Put ~15 lines there: the authoring loop, and the codeNode contract in full. This is
   the only channel that reaches the model *before* it starts, without it having to ask.
2. **An MCP resource** — register `docs/cloudflow-authoring.md` at a stable URI
   (`doit://docs/cloudflow-authoring`) and return it from the existing (currently empty)
   `ListResourcesRequestSchema` handler. Cheap, and makes the doc addressable for clients that
   browse resources.
3. **Tool descriptions**, where the rule is actionable at the moment of use:
   - `build_cloud_flow` / `refine_cloudflow`: *"Generated codeNode code is frequently broken in
     ways that pass validation and fail silently at run time. Always `export_cloudflow_flow`
     and test-run before reporting success."*
   - `import_cloudflow_flow` / `export_cloudflow_flow`: the one-line codeNode contract
     (`nodes` dict keyed by node name; top-level `return`; `schema` required).

**Why descriptions and not only the doc:** a description is unconditionally in context at tool
selection time; a resource is read only if the client thinks to. Neither substitutes for the
other.

**Mirror in `doit-external-api-mcp`:** populate the empty `src/overrides.ts` with the same
CloudFlow descriptions. That file exists for precisely this and has never been used.

**Effort:** small. **Verification:** an eval (§6) asserting a generated codeNode uses `nodes`
and ends in a top-level `return`.

### P2 — Lint `codeNode` bodies before the flow exists

**Symptom:** §1 step 4. Three server-side gates — import dry-run, test-run dry-run, and the run
itself — all pass a code node that cannot produce output. Every documented failure mode is
statically detectable from the bundle JSON.

**Change:** a pure client-side check over a `FlowBundle`, flagging, per `codeNode`:

| Check | Rule |
|---|---|
| No top-level `return` | Body's outermost block has no `return` statement → *"code node produces no output"* |
| Uncalled entry-point function | Defines `def handler(...)` / `def main(...)` never called at top level |
| Phantom `input` | References a bare `input` identifier → *"no injected `input`; use `nodes[\"<name>\"]`"* |
| `output =` assignment | Assigns `output` instead of returning |
| Missing `schema` | `schema` absent/empty → test-run rejects it |
| Unknown node name | `nodes["X"]` where `X` matches no node name in the bundle — catches rename drift |

The last one is worth the parser on its own: it is undetectable by any server-side check that
does not know the graph, and it silently returns nothing.

**Where:**
- MCP: run inside `import_cloudflow_flow` as a pre-flight, returning findings **alongside**
  the API's own dry-run plan (not instead of it). Also expose standalone as
  `validate_cloudflow_bundle` so an agent can check before it has anything to import.
- CLI: hook into `validateRequestBody` (`body_validation.go:54`), which already
  intercepts and repairs `import-cloudflow-flow` bodies (`wrapBareCloudflowBundle`).

**Fidelity note:** a regex pass gets the last four rows; the first two want a real parse. Python
`ast` is not available in a Node MCP server, so either accept regex-level fidelity (still
catches every failure observed to date) or use a small Python tokenizer in the CLI and keep MCP
regex-only. Recommend starting regex-only in both, warnings-first, and revisiting if false
positives appear.

**Effort:** medium (a day, mostly test cases). **Verification:** replay both broken node bodies
from §1 as fixtures; both must be flagged, and the working one must not be.

### P3 — Return the flow ID instead of timing out

**Symptom:** §1 steps 1–3. This is the duplicate-flow generator, and the fix is nearly free
because the machinery already exists.

`consumeCloudflowBuilderStream` (`src/tools/cloudflow.ts:233-280`) already captures the flow ID
mid-stream from the `cloudflow_created` custom event into a caller-owned accumulator. Its own
doc comment says the accumulator is passed in rather than returned *"so a caller can still
report the flow ID the stream emitted before an error interrupted it — a partially built flow
is recoverable."* The design anticipated this exactly; only the deadline is missing.

**Change:**
- Wrap stream consumption in a deadline (default ~45s, env-overridable) chosen to land under
  common client caps — the observed cap in this session was 60s.
- On deadline, **return a success-shaped partial result** rather than throwing:
  ```json
  { "flowId": "…", "conversationId": "…", "status": "building",
    "note": "The builder is still running server-side. Do NOT call build again — poll
             export_cloudflow_flow for this flowId until nodes appear." }
  ```
- Keep streaming progress notifications (already emitted per step,
  `src/server.ts:221-227`); some clients reset their timer on progress, which extends the
  budget for free.
- State the polling contract in the tool description, since the note alone arrives only after
  the model has already decided how to recover.

**Why this is right even though it looks like papering over slowness:** the operation genuinely
is long-running and the API offers no async handle. Given that, a truthful partial result
strictly dominates an error that hides a completed side effect.

**Effort:** small. **Verification:** an integration test with a stubbed slow SSE stream
asserting a partial result carrying `flowId`, and no thrown error.

### P4 — Shape run-inspection responses

**Symptom:** §1 step 6 — 27,751 bytes to read a 165-byte answer, four times.
`get_cloudflow_flow_run` returns every node's full `input`/`output`; the generated path returns
the body verbatim (`parseAs: "text"`, `generated/callOperation.ts`). For authoring, the
interesting payload is almost always the *last* node's output.

**Change:** a hand-written `inspect_cloudflow_run` wrapping the same endpoint, with:
`nodeName?` (default: last terminal node), `outputsOnly?` (default true),
`maxBytesPerNode?` (default ~2 KB), and large arrays summarized as
`{ length, first, truncated: true }` rather than dropped from the end.

Keep the generated `get_cloudflow_flow_run` exposed for full fidelity.

**CLI equivalent:** `--node <name>` and `--outputs-only` flags on `get-cloudflow-flow-run`.

**Effort:** small. **Verification:** the §1 run through the new tool must fit in <2 KB and still
show `rowCount`/`firstRow`.

### P5 — Generate `Idempotency-Key` automatically on both surfaces

**Symptom:** the spec makes `Idempotency-Key` a required header, so it surfaces as a **required
tool input** and the model invents one. In this session I minted
`rowcounter-v10-verify-20260904-1153` by hand. Model-authored keys are a correctness hazard in
both directions: reusing one silently returns a cached response instead of acting, and varying
one on an intended retry starts a second run.

**Neither surface handles this today, but the CLI already has the mechanism**, just narrowly
scoped:

- `ensureDryRunIdempotencyKey` (`destructive_contract.go:331`) mints a key automatically — but
  only under `agent-dry-run`, for commands that also expose a `dry-run` flag.
- Outside that path, `requiredOperationFlags` (`command_catalog.go:85-89`) marks the flag
  **required** for `import-cloudflow-flow`, so a real import still needs a caller-supplied key.
  (Only the beta surface auto-generates unconditionally — `betaCatalogEntries`,
  `command_catalog.go:284`.)
- MCP does neither: the generated tool simply passes the required parameter through.

**Change:**
- Both surfaces: default the key to a generated UUIDv4 for mutating CloudFlow calls when the
  caller supplies none, keeping it overridable so a deliberate retry can reuse one. In the CLI
  this is mostly widening `ensureDryRunIdempotencyKey` beyond the dry-run path; in MCP it is a
  default in the tool's input handling.
- Document the retry semantics in the tool description — the correct text already exists in
  `docs/cloudflow-authoring.md` ("retrying with the same key can never start a second run; on a
  5xx the run may have started anyway — check the runs list before minting a new key").
- CLI gap regardless of the above: `requiredOperationFlags` lists `import-cloudflow-flow` but
  **not `test-run-cloudflow-flow`**, which also requires a key — so agents reading
  `dci commands --json` are not told. Add it.

**Caveat worth deciding explicitly:** auto-generating a fresh key per call makes retries *less*
idempotent unless the key is derived from call arguments or cached per logical operation. For
an agent surface, a fresh key plus the "check the runs list before retrying" rule is the safer
default — but that is a judgement call, not an obvious win, and it belongs in the decision log.

**Effort:** small on both. **Verification:** call `test_run_cloudflow_flow` with no key; a run
starts.

### P6 — One-call verification

**Symptom:** confirming a flow works took test-run + 3-5 polls + a full run fetch — a loop the
agent hand-rolls every time, with the §1 step-6 payload cost on each poll.

**Change:** `verify_cloudflow_flow(flowId)` — composes existing endpoints only: test-run
(auto-key, per P5) → poll `list_cloudflow_flow_runs` (small responses) until terminal → return
the P4-shaped per-node outputs. Bounded by the same deadline discipline as P3, returning
`{ runId, status: "running" }` if it overruns.

**Effort:** small (pure composition). **Verification:** the whole §1 verification in one call
under 2 KB.

## 5. Sequencing

1. **P1** (guidance) and **P5** (idempotency) — hours each, no new surface area, immediate
   effect on first-pass success. Ship together.
2. **P3** (partial result) — removes the duplicate-flow failure mode; unblocks trusting the
   builder at all.
3. **P2** (codeNode lint) — the largest single reliability win; wants real test fixtures.
4. **P4/P6** (response shaping, composite verify) — cost and ergonomics, best designed once P2
   defines what the agent needs to see.

P1 is a prerequisite for meaningfully evaluating the rest: until the model is told the
contract, every eval measures whether it guessed.

## 6. Evals

Extend the existing convention (`skills/dci-cli/references/evals.md`). The §1 task is seed
eval #1 — small enough to run cheaply, and it exercised every failure mode in this spec at once.

Assertions worth having:
- Generated/repaired `codeNode` reads via `nodes[...]` and ends in a top-level `return`.
- The lint (P2) flags both §1 fixtures and passes the working body — a fixture-level regression
  test, no API calls.
- A builder call that overruns the deadline yields a `flowId` and **no second flow** — assert
  on flow count before/after, which is what actually went wrong.
- Verification reports `rowCount: 244` for the §1 report rather than "the flow ran
  successfully". The distinction between *ran* and *produced correct data* is the one this
  whole spec exists to defend.

## 7. Explicitly out of scope: what genuinely needs backend

Stated so nobody plans around a client-side fix that cannot exist:

- **No flow update/patch endpoint.** The 16 CloudFlow operations include no way to write a
  flow's graph deterministically. Import is create-only (new IDs); the only in-place mutation
  is the NL `refineCloudFlow`. So the repair loop is unavoidably *export → fix → import as a
  new flow*, and the direct-graph writes in the external-API epic (CMP-43801) remain the real
  answer.
- **No flow delete endpoint.** `deleteCloudflowConnection` exists; there is no flow equivalent.
  The two junk flows from §1 can only be removed in the Console.
- Together these mean **this spec cannot eliminate draft accumulation — only reduce how often a
  repair pass is needed** (P1, P2) and stop *tooling artifacts* from creating extra flows (P3).
  Worth being honest about: P3 removes the duplicates caused by retrying a phantom failure, not
  the ones inherent to create-only repair.

## 8. Open questions

- Does the pinned `@modelcontextprotocol/sdk` (`^1.0.3`) accept `instructions` in
  `ServerOptions`, and does the remote Worker transport forward it at `initialize`? P1's first
  layer depends on it; layers 2 and 3 do not.
- The remote Worker (`doiteng/doit-mcp-server-remote`) could not be inspected from this session
  (cross-tier repo attach). Assumed to wrap this core without overriding tool descriptions,
  server instructions, or the resource list — worth confirming before P1 lands, since it is the
  transport that actually served this session.
- Should P2's lint block the import or only warn? Recommend warnings-first, matching the
  rollout posture already chosen for deep dry-run validation (D3 in the authoring spec).
- Is 45s the right P3 deadline? 60s is the only client cap measured. If other clients cap
  lower, the default should drop rather than be tuned per client.
- Does `docs/cloudflow-authoring.md` belong in the MCP repo at all once P1 ships, or should
  both surfaces render one source of truth? Two hand-maintained copies of a runtime contract
  will drift — and the contract is exactly the thing that must not.
