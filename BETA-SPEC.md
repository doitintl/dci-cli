# Beta Commands — Design Spec

A `dci beta` command surface for not-yet-GA API operations, modeled on
[`gcloud beta`](https://docs.cloud.google.com/sdk/gcloud/reference/beta). Researched
2026-08-26 against the live prod/dev specs and the omni source; codebase facts verified
against current main; prod availability verified by live probe (§1.1).

The immediate driver is the async report execution API (epic CMP-44423): five endpoints
that are live on the production API but hidden from the production OpenAPI spec and
gated per customer by an early-access feature flag. Today the only way to reach them from
the CLI is `DCI_API_BASE_URL=https://api-dev.doit.com`, which is wrong on every axis —
dev tokens, dev data, per-invocation env juggling.

**Design constraint: no omni changes.** Everything below runs entirely inside this repo,
using only the two publicly served spec artifacts. A server-published beta spec remains a
possible later optimization (§3).

---

## 1. Research findings

### 1.1 How the API side actually works

- **The dev spec is a strict superset of prod.** `https://api-dev.doit.com/openapi.yaml`
  = prod `openapi.yaml` ∪ `services/external-api/openapi.dev.overlay.yaml` (omni). The
  overlay's stated purpose: *"Dev-only OpenAPI additions that should appear in ReadMe dev
  publishes but not prod."* Both spec URLs are public and unauthenticated.
- **Dev-only operations today (21), prod availability probed live 2026-08-26** (omni
  context, dry-run/read-only calls):
  | Group | Ops | On prod `api.doit.com`? | Flag |
  |---|---|---|---|
  | Async report runs (CMP-44423) | 5 — run inline, run by id, poll, results, cancel | **yes** — dry-run 200, typed 404 from handler on fake operation id | `CMP-44423` |
  | PerfectScale for Commitments GCP | 8 | **yes** — settings returns 200 (needs `X-Tenant-Id` header; query param not accepted) | (unknown) |
  | Virtual tags + suggestions | 8 | **no** — router-level `404 page not found`, api-dev only | (unknown) |
- **The async report routes are registered unconditionally in the prod API binary**
  (omni `server/services/scheduled-tasks/cmd/api/api.go:1548-1560`). The gate is
  per-customer, not per-environment: `RequireAsyncReportExecution` middleware returns
  **404** when the customer's early-access features don't include `CMP-44423`
  (`framework/mid/feature_flag.go`). Enablement lives at
  `console.doit.com/customers/<id>/early-access-features`.
- Incidental: prod's Cloudflare WAF rejects generic HTTP clients (`error code: 1010`)
  until a real User-Agent is set. Irrelevant for the CLI (restish sends its own UA);
  relevant to anyone probing with curl/urllib.

**Consequence — the central design fact:** beta commands target **prod `api.doit.com`**
with prod credentials. Only the *command surface* (the spec) is missing from prod, not
the endpoints. No dev environment involvement at runtime.

### 1.2 The gcloud model, distilled

`gcloud beta <command>` is a parallel command tree: same invocation shape as GA, an
explicit `beta` prefix on every call so instability is self-documenting in scripts and
shell history, `(BETA)` branding in help, and no entitlement check client-side — the
backend rejects callers who lack access. We adopt the prefix-tree model and the
server-side-gating philosophy, but **not** gcloud's duplicate-everything approach
(gcloud mirrors the whole GA surface under `beta`; for us that would double the catalog,
the AI prompt budget, and completion noise for zero benefit — the beta tree carries beta
ops only).

### 1.3 Relevant CLI architecture (verified against main)

- One restish API entry `"dci"` in `apis.json`; spec discovered via restish location
  hints (`/openapi.yaml` on the base), cached as `<UserCacheDir>/dci/dci.cbor` with a
  24 h TTL, invalidated by any CLI version bump or `--rsh-no-cache` (restish
  `cli/api.go:95-141`).
- There is **no client-side operation filter**: every non-hidden operation in the spec
  becomes a command. The three surface enumerators (root help/completion
  `main.go:1130`, `dci commands` `command_catalog.go:119`, AI catalog
  `ai_slash.go:256`) all honor `Operation.Hidden` uniformly.
- `dci commands` already demonstrates spec-parsing outside restish's fetch/cache path:
  `loadCatalogAPI` (`command_catalog.go:88-115`) feeds an HTTP response into
  `openapi.New().Load(...)` directly — the exact pattern the embedded beta spec reuses.
- Hydration never triggers OAuth from a background path: completion and the AI session
  hydrate only from an existing `.cbor` (`main.go:1100-1121`, `ai_slash.go:295-312`).
- No feature-flag/beta/experimental concept exists anywhere in the repo today.

---

## 2. Design

### 2.1 Surface

```
dci beta                         # branded help: list of beta commands + disclaimer
dci beta <command> [args]        # invoke a beta operation, e.g.:
dci beta run-report 8EmhotO0poyBBOm2kO7q
dci beta <command> --help        # per-command help, "(BETA)" prefix on the summary
```

- `beta` is a visible root command with its own group line in root help:
  `beta — Early-access commands (may change or be removed without notice)`.
- Beta command names live in their own namespace — no collision handling with GA names
  is needed.
- Every beta invocation prints a one-line stderr notice (suppressed in agent mode, like
  onboarding text): `note: beta command — requires early access; behavior may change.`
  Stderr only, so piped stdout/JSON is untouched.

### 2.2 Spec sourcing — vendored, generated, embedded

The beta surface ships **inside the binary**: a self-contained
[`beta/openapi.beta.yaml`](beta/openapi.beta.yaml) committed to this repo and embedded
via `go:embed`. It is never hand-edited; it is the output of a generator driven by a
hand-curated manifest:

**`beta/manifest.yaml`** — the editorial layer, the only file humans touch:

```yaml
source: https://api-dev.doit.com/openapi.yaml
operations:
  - operationId: asyncRunInline
    cliName: run-report-config
    earlyAccess: CMP-44423
  - operationId: asyncRunReportById
    cliName: run-report
    earlyAccess: CMP-44423
  - operationId: getAsyncOperation
    cliName: get-report-operation
    earlyAccess: CMP-44423
  - operationId: getAsyncOperationResults
    cliName: get-report-results
    earlyAccess: CMP-44423
  - operationId: cancelAsyncOperation
    cliName: cancel-report-operation
    earlyAccess: CMP-44423
```

Inclusion in the manifest is the per-operation dial: an op enters `dci beta` only when a
maintainer lists it, after verifying it is actually deployed on prod (virtual tags, for
example, stay out until they are). `cliName` and `earlyAccess` become `x-cli-name` /
`x-dci-early-access` extensions in the generated spec — all the editorial polish that
the omni-published design would have needed upstream PRs for is local here.

**`tools/betaspec`** — a small Go generator (`go generate ./...`): fetches the public
dev spec, extracts the manifest's operations plus the **transitive `$ref` closure** of
their parameters/schemas/responses, injects the extensions, and writes a self-contained
`beta/openapi.beta.yaml` with a provenance header (source URL, source spec SHA-256,
generation date). Deterministic output — same input spec, byte-identical file.

Why vendored beats the alternatives (full comparison in §3):

- **Zero runtime dependencies** — no fetch, no 24 h spec cache, no api-dev coupling at
  runtime, beta commands and their completion work offline and instantly.
- **Zero omni work.**
- **Tested surface** — the binary ships exactly the command shapes it was tested
  against; API-side spec churn can't change CLI behavior between releases.
- **Reviewable drift** — every shape change arrives as a git diff (§2.3), not a silent
  server-side flip.

The trade-off is a drift window between API changes and the next CLI release. Two facts
make it small: this CLI releases often, and the server validates all request bodies
anyway — stale shapes degrade to a server-side 400, never anything silent. Pre-GA API
shapes do land in a public repo, but they are already public at api-dev's unauthenticated
spec URL.

### 2.3 Generation pipeline — automated, but commit-time, not release-time

`go:embed` requires the file to exist in the committed tree the release tag points at —
so the spec **cannot** be generated during the release build without giving up
reproducible-from-tag binaries. Generating inside `release.yml` would also make releases
hostage to api-dev availability and ship shape changes that never appeared in a
reviewable diff (GoReleaser-cross in Docker adds one more network-fragility layer).
Instead:

1. **`refresh-beta-spec.yml` workflow** — weekly cron + `workflow_dispatch`: run the
   generator; if `beta/openapi.beta.yaml` changes, open a PR (`chore:` prefix — not
   user-facing until released). Drift arrives as a reviewable diff with the fix
   attached; nothing merges unseen.
2. **Promotion detection, same workflow** — the generator also fetches the prod spec;
   if a manifest operation now exists in prod `openapi.yaml`, the PR flags it as
   **promoted to GA** (see §2.8 for the lifecycle).
3. **Staleness check at release** — a job in `post-release-verify.yml` regenerates and
   **warns** on mismatch rather than blocking, so an api-dev outage or a mid-flight API
   change can never hold a release hostage. The committed file is always the source of
   truth for what ships.
4. **Manual refresh** — `workflow_dispatch` (or `go generate` locally) when the API
   team announces an overlay change, closing the cron-cadence drift window on demand.

### 2.4 CLI mechanics

Embedding eliminates most of the plumbing a fetched beta spec would need — no second
`apis.json` entry, no auth-cache aliasing, no `dci-beta.cbor`, no never-OAuth completion
special-casing:

- **Hydration:** when `argv` starts with `beta` (after `normalizeArgs`), parse the
  embedded spec through the same restish openapi loader `dci commands` already uses
  (`openapi.New().Load` over a synthetic response, cf. `command_catalog.go:88-115`) and
  mount the resulting operations as a `betaCmd` cobra subtree **under the existing
  `dci` API identity**, so auth, token refresh, customer context (`applyCustomerContext`
  injects both the `X-Tenant-Id` header and the legacy query param — covers both
  behaviors seen in §1.1), table rendering, and the error contract all apply unchanged.
  Lazy — GA invocations never parse the beta spec.
- **Completion:** `dci beta <TAB>` hydrates from the embedded spec — always available,
  offline, no ActiveHelp fallback needed.
- **`DCI_API_BASE_URL`** still applies (transport-level), so beta commands can be
  pointed at api-dev for CLI development itself.
- New chapter file `beta_commands.go` + `beta_commands_test.go`, per the
  one-file-per-chapter convention.

### 2.5 Gating UX — no client-side entitlement check

Like gcloud: everyone can see and invoke beta commands; the server decides. The CLI's
job is to make the rejection legible. The CMP-44423 middleware returns **404** for
non-entitled customers, which the error contract would render as a misleading "not
found". Beta commands therefore get a 404 interceptor:

```
Error: this beta feature requires early access enrollment (CMP-44423).
Enable it at https://console.doit.com/customers/<context>/early-access-features
or ask your DoiT account team, then retry.
```

Driven by `x-dci-early-access` from the embedded spec — no hardcoded flag table in the
CLI code. The same extension renders in `--help`: `Requires early access: CMP-44423.`
(Caveat: a genuine 404 — bad operation id — on an entitled customer shows the same
hint; it is phrased as a possibility, and the trade-off is acceptable versus probing
entitlement on every call. The typed RFC 9457 `operation_not_found` body observed in
§1.1 lets the interceptor skip the hint when the server names a specific error code.)

### 2.6 Ergonomics for the async-report five

1. **Clean names via the manifest** (§2.2) — `dci beta run-report`, not
   `dci beta async-run-report-by-id`.
2. **Auto-generated `Idempotency-Key`** — the API requires the header on every submit
   and cancel; making users invent UUIDs is hostile. The CLI injects a random UUID when
   the flag is absent (dedup is content-based server-side, so a fresh key per
   invocation is correct). Users who want replay semantics pass the flag explicitly.
   Implemented generically: any beta op with a required `Idempotency-Key` header param
   gets the default — no per-command table.
3. **`--wait` (phase 2)** — the API is a textbook LRO: `202` + `Location`, poll with
   server-provided `Retry-After` (1→30 s), `425`/`422` on early/failed results fetch.
   A `--wait` flag on the two run commands that polls to a terminal state and then
   fetches results (rendered through the normal table pipeline) is the gcloud-grade
   UX. Deliberately phase 2: v1 ships the raw verbs so the API team gets feedback on
   the primitive shapes first.

### 2.7 Catalog, AI session, docs

- **`dci commands`** grows `--beta`: appends beta entries from the embedded spec (no
  network, unlike the GA path's live fetch) with a new `"stage": "beta"` field.
  Additive, so `catalogSchemaVersion` stays `"1"`; GA entries omit the field. Default
  output stays GA-only — agents shouldn't wander into beta surface unprompted.
- **AI session:** excluded from `aiSessionCatalog()` in v1 (prompt-cache budget, and
  entitlement makes autonomous use flaky). Phase 2: include with a `(beta)` marker when
  the tenant is confirmed entitled, or teach `search_commands` to reach them on request.
- **Skill corpus:** no structural change — `SKILL.md` already delegates discovery to
  `dci commands --json`; add one prose line about `--beta` (accepting the
  digest-manifest update in `skill_management.go:22`).
- **README:** short "Beta commands" section, user-facing language only (no
  restish/overlay jargon per repo convention).

### 2.8 Promotion lifecycle

When an operation GAs, omni moves it from the overlay into prod `openapi.yaml`; it
appears as a root GA command automatically at the next spec-cache refresh. The refresh
workflow (§2.3) detects the promotion and opens a PR; policy on merge:

- Keep the op in the manifest for **one release cycle** after GA, so `dci beta
  run-report` and `dci run-report` both work briefly (dual presence is harmless — the
  trees are separate namespaces).
- Then drop it from the manifest; unknown-beta-command errors run did-you-mean against
  the **GA** tree too: `"run-report" is not a beta command — it is now generally
  available: dci run-report`.

---

## 3. Alternatives considered

**Spec sourcing** (why vendored won):

| Source | Verdict |
|---|---|
| Vendored + generated `beta/openapi.beta.yaml` | **Chosen** — zero omni work, zero runtime deps, local editorial control, reviewable drift; cost: drift window between releases (mitigated §2.3) |
| omni publishes `api.doit.com/openapi.beta.yaml` (union + markers) | Cleanest steady state — server-driven surface, no drift, kill switch without a CLI release — but requires omni work: serving the artifact, `x-dci-stage`/`x-dci-early-access`/`x-cli-name` upstream, and a decision that prod may serve pre-GA shapes. Kept as a later optimization: the CLI architecture is identical, only the spec source swaps |
| Fetch api-dev spec at runtime (surface only; requests to prod) | Runtime dependency on dev infra with no prod SLA; dev spec runs ahead of prod deployments; beta set must be computed as dev-minus-prod per hydration; flag mapping hardcoded. Weakest option |
| Hand-written native cobra commands | Best immediate UX for five endpoints, but no general mechanism — every future beta group is more hand-written code; abandons the spec-driven architecture; more transport plumbing than it looks |

**Surface model** (why the gcloud prefix won):

| Model | Verdict |
|---|---|
| `dci beta <cmd>` prefix tree (gcloud) | **Chosen** — instability explicit at every call site, clean namespace, zero GA-surface pollution |
| `DCI_BETA=1` env unhiding beta ops inline at root | Invisible at the call site, scripts silently depend on beta, all-or-nothing; mixes beta into the AI catalog and completion by default |
| Per-command `--beta` flag on GA commands | Only works when a GA twin exists; the async five have no GA twins |
| Point users at `DCI_API_BASE_URL=api-dev` (status quo) | Dev tokens, dev data, wrong environment — the problem statement, not a solution |

---

## 4. Work items

All in this repo; no omni prerequisites.

1. **Generator + manifest**: `beta/manifest.yaml` (async-report five),
   `tools/betaspec` ($ref-closure extraction, extensions injection, provenance header,
   deterministic output), initial committed `beta/openapi.beta.yaml`.
2. **`beta_commands.go`**: embedded-spec hydration into `betaCmd` under the `dci` API
   identity, help branding + stderr notice, 404 early-access interceptor,
   Idempotency-Key default, completion. Matching `_test.go` (hydration can be tested
   entirely offline against the embedded spec — no HTTP fixtures needed).
3. **Workflows**: `refresh-beta-spec.yml` (cron + dispatch → PR on drift, promotion
   detection), staleness warn-job in `post-release-verify.yml`.
4. **Integration**: `command_catalog.go` `--beta` + `stage` field; root help group
   line; README section; skill prose line.
5. **Phase 2**: `--wait` LRO polling, AI-session exposure, GA did-you-mean on
   promotion.

Versioning: new command group → **minor** bump per repo policy.

## 5. Open questions

1. Which flag (if any) gates the ps4commitments routes, and why do they reject the
   legacy `customerContext=` query param that the async-report routes accept
   (`X-Tenant-Id` works — `applyCustomerContext` sends both, so the CLI is unaffected)?
   Blocks adding that group to the manifest, not the first wave.
2. Manifest curation requires knowing an op is deployed on prod — today verified by
   manual probe (§1.1). Worth automating in the refresh workflow later (a dry-run/GET
   probe with a CI token), or acceptable as a manual checklist item per manifest PR?
3. Should `dci beta` be hidden for non-entitled tenants at all? Current answer: no —
   discoverability is the point of beta; the server gate plus the 404 hint is enough.
   Revisit if a beta ever gates something confidential rather than merely unfinished.
