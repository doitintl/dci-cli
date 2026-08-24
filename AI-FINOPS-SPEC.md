# AI FinOps Coverage & CSP Navigation — Design Spec

Follow-up to [AI-SPEC.md](AI-SPEC.md) (PRs #95/#100/#101). Two goals:

1. **FinOps prompt coverage** — a repeatable 10-prompt evaluation suite drawn from the
   [FinOps Framework](https://www.finops.org/framework/) capabilities, with a measured
   baseline and the gaps it exposed.
2. **CSP navigation** — teach the AI session about the CSP tenant (the Doer-only
   aggregation of all customers' billing data) so multi-customer and book-of-business
   questions resolve there instead of failing or, worse, answering from the wrong tenant.

Every factual claim below was validated live on 2026-08-24 against the current binary
(post-#101 main). Evidence is summarized inline; raw transcripts stayed out of the repo
(they contain customer data — this repo is public).

---

## 1. The FinOps-10 evaluation suite

Ten prompts, each mapped to a FinOps Framework capability, run through one-shot
`dci ai "<prompt>"` against the active tenant. The suite lives in
[eval/finops-prompts.txt](eval/finops-prompts.txt) with a runner
([eval/run-finops.sh](eval/run-finops.sh)); it is a dogfood/regression harness, not a
shipped feature — run it after any prompt, playbook, or loop change and compare
wall-clock, tool-call count, and answer sanity against the table below.

| # | Capability | Prompt | Baseline | Tool calls | Verdict |
|---|---|---|---|---|---|
| P01 | Reporting & Analytics | What did we spend on cloud last month, and how does it compare to the month before? | 32s | 1 | sane, insightful |
| P02 | Allocation | Break down last month's cloud spend by service — what are the top 10 cost drivers? | 25s | 1 | sane |
| P03 | Anomaly Management | Were there any cost anomalies in the last 30 days? What caused the biggest one? | 17s | 2 | **failed — CLI bug, §2.1** |
| P04 | Forecasting | Based on the current trend, roughly what will our total cloud bill be for this month? | 53s | 2 | sane (weekday/weekend-adjusted projection) |
| P05 | Budgeting | Are any of our budgets at risk of being exceeded this month? | 15s | 1 | sane |
| P06 | Rate Optimization | How much are we saving from committed-use discounts and savings plans, and how much eligible spend is still on-demand? | **151s** | **14** | sane but inefficient, §2.2 |
| P07 | Workload Optimization | What are the top cost savings opportunities you can find right now? | 63s | 9 | sane but inefficient, §2.2 |
| P08 | Unit Economics / Trends | Which services grew the most month-over-month, in dollars and percent? | 35s | 1 | sane |
| P09 | Allocation Coverage | What share of our spend is untagged or unallocated to any team? | 90s | 5 | sane, discovery-heavy |
| P10 | GenAI Cost Management | How much do we spend on tokens per AI model? | 91s | 1 | sane (flagship regression holds) |

Scorecard: **9/10 correct and sane; 1 hard failure caused by a CLI bug (§2.1); 3 runs
spent most of their time exploring surface the playbook doesn't cover (§2.2).**

### 1.1 `effort=medium` run (2026-08-24) — default effort flipped to medium

Full suite (P01–P12, doer account) re-run on post-#103 main under
`DCI_AI_EFFORT=medium`. Caveat on reading the speed column: the binary also carries the
F2 playbook section, `--rollup`, and the exit-30 fix, so the wall-clock deltas fold in
those improvements too — the gate for the default flip was answer substance, not speed.

| # | Baseline | Medium | Tool calls (base → med) | Verdict at medium |
|---|---|---|---|---|
| P01 | 32s | 26s | 1 → 1 | sane; same insight quality |
| P02 | 25s | 22s | 1 → 1 | sane; notes top-10 ≠ full bill |
| P03 | 17s (**failed**) | 33s | 2 → 3 | **correct** — 132 anomalies, biggest one root-caused to SKU level |
| P04 | 53s | 41s | 2 → 2 | sane; excludes partial-day and placeholder rows from the run rate |
| P05 | 15s | 13s | 1 → 1 | sane |
| P06 | 151s | 69s | 14 → 9 | sane; keeps both reconciliation caveats |
| P07 | 63s | 28s | 9 → 4 | sane; discloses dismissed-insights and page-cap blind spots |
| P08 | 35s | 36s | 1 → 1 | sane; $ and % both correct |
| P09 | 90s | 62s | 5 → 4 | sane; refuses to sum overlapping tag sources |
| P10 | 91s | 61s | 1 → 1 | **verified by hand**: 3 family sums reconcile against raw rows (incl. the `claude-4.6-opus-*` naming trap); Cursor billing-classification caveat stated |
| P11 | — | 49s | — → 7 | correct: no book-of-business tags exist for this account; says so and offers alternatives instead of fabricating |
| P12 | — | 18s | — → 2 | correct: switches to CSP, right provider growth ranking |

12/12 substantively correct; none of the `effort=low` failure modes (mis-merged model
families) appeared, no turn hit the output-token ceiling. **Decision: the default
reasoning effort is now `medium`** (`aiDefaultEffort`, `resolveAIEffort`). Precedence is
unchanged (`DCI_AI_EFFORT` > `"effort"` in settings > default); the new value `default`
opts back into the API's uncapped adaptive thinking — `high` is *not* that opt-out, it
is a cap above medium. P11/P12 have no baseline column: this run is their first
recorded measurement.

### 1.2 Full-stack retest (2026-08-24, post-#104–#107) — with token telemetry

Full suite on main carrying everything: medium default, concurrent tool batches (#106),
`DCI_AI_STATS` telemetry (#104), rollup, session-scoped context. 12/12 substantively
correct (P10 hand-checked: numbers identical to the original baseline). First run with
token columns (`in` = uncached input, `out` = output, both summed across rounds).

| # | Orig baseline | §1.1 medium | Full stack | Rounds | Tools | in / out tokens |
|---|---|---|---|---|---|---|
| P01 | 32s | 26s | 28s¹ | 2 | 1 | 1.3k / 1.1k |
| P02 | 25s | 22s | 22s | 2 | 1 | 1.2k / 0.7k |
| P03 | 17s (failed) | 33s | 29s ✓ | 4 | 3 | 17.5k / 1.1k |
| P04 | 53s | 41s | 30s | 2 | 2 | 1.8k / 1.0k |
| P05 | 15s | 13s | 15s | 2 | 1 | 0.6k / 0.4k |
| P06 | 151s | 69s | 90s² | 6 | 11 | 36.5k / 3.4k |
| P07 | 63s | 28s | 33s | 4 | 4 | 17.0k / 1.2k |
| P08 | 35s | 36s | 33s | 2 | 1 | 3.2k / 1.5k |
| P09 | 90s | 62s | 57s | 4 | 5 | 5.1k / 2.0k |
| P10 | 91s | 61s | 74s³ | 2 | 1 | 7.8k / **5.0k** |
| P11 | 133s (failed) | 49s | 44s ✓ | 6 | 7 | 5.4k / 2.3k |
| P12 | 36s (wrong tenant) | 18s | 24s ✓ | 3 | 2 | 1.6k / 0.8k |

¹ TTFT 18.3s — the run pays the ~16.5k-token prompt-cache **write**; every later run in
the 5-minute window reads it (TTFT 1.4–3.7s). Interactive sessions pay this once.
² Run-to-run variance, not regression: this path took 11 calls including one
self-corrected flag error (savings-plans before the org id) where §1.1's took 9. The
concurrency is visible in the transcript (six calls issued back-to-back); P06 remains
the noisiest prompt — single-run deltas on it are not meaningful.
³ P10's cost is now dominated by its own answer: 5.0k output tokens (a 20+-row table
plus caveats) is roughly half the wall clock at streaming speed.

Suite total (P01–P10): 572s at the original baseline (with one hard failure) → **411s,
12/12 correct** on the full stack.

## 2. Findings

### 2.1 Exit-30 collision: validation errors masquerade as destructive commands — FIXED

`error_contract.go` maps API `VALIDATION_ERROR` to **exit 30**; the AI executor treated
any exit 30 as "destructive command needs approval". P03's model sent
`--sort-order descending` (API wants `desc`), got a validation error → the session
auto-declined it as a "destructive" command and told the model *"the user declined — do
not retry"*, killing legitimate self-correction. In the interactive TUI this surfaced as
a bogus y/N prompt for a read-only list command.

**Fix (this branch):** approval opens only when exit 30 is accompanied by the
destructive contract's own envelope code `DESTRUCTIVE_REQUIRES_CONFIRMATION`; any other
exit-30 envelope flows to the model as a plain, self-correctable error.
(`ai_tools.go`, regression test in `ai_tools_test.go`.)

### 2.2 Playbook gaps: commitments, insights, allocation coverage

The three slow runs share one cause — the embedded playbook covers `query` deeply but
says nothing about the rest of the FinOps surface, so the model explores with `--help`,
guesses enum values, and trips flag conflicts:

- **P06 (151s, 14 calls):** wandered through `list-commitments`, `list-aws-savings-plans`
  (needs an org id it had to discover via `list-aws-organizations`),
  `list-aws-recommendations`, three `list-dimensions --search` rounds, and four queries
  before assembling the (good) answer.
- **P07 (9 calls):** two `--help` round-trips, one `--all`+`--max-results` flag-conflict
  error, and three guesses at `list-insights --category` values before finding `FinOps`.
- **P09 (5 calls):** re-derived the unlabeled-bucket pattern (already in the playbook)
  plus an allocations sweep.

**Proposal (F2):** add a compact "FinOps surfaces" section to the embedded playbook — a
few lines each for anomalies (`list-anomalies` + valid sort enums), budgets, insights
(`list-insights`, category vocabulary, `dailySavings` semantics, dismissed-insights
default), commitments/savings-plans (org-id prerequisite chain), and the
allocation-coverage recipe (total vs `--drop-unlabeled-rows` delta). Estimated cost:
≤600 tokens in the cached prefix; expected effect: P06-style runs collapse from ~14
calls to 2–4.

### 2.3 Efficiency profile is otherwise healthy

Five of ten prompts resolved in a single tool call (the #100/#101 work paying off), and
the reasoning-dominated latency profile matches the #101 analysis (server-side thinking
is the floor; `effort=medium` proved out as the speed lever and is now the default, §1.1).

---

## 3. CSP: the all-customers tenant

### 3.1 What it is (validated)

- Customer context `csp.doit.com` (id `CIgtnEximnd4fevT3qIU`); both forms pass
  `validateCustomerContextValue` and resolve (`dci validate` returns the tenant).
- An **aggregation of all DoiT customers' billing data**, Doer-only (console gates it;
  the API authorizes whatever the token allows, per AI-SPEC §6).
- Carries **account-team dimensions**, so book-of-business questions are answerable.

### 3.2 Data-surface map: CSP vs a regular customer tenant (validated by dimension diff)

**Only in CSP (fixed dimensions):**

| id | label | notes |
|---|---|---|
| `csp_primary_domain` | Customer | one value per customer (primary domain) |
| `csp_strategic_account_manager` | AM/SAM | **values are doer emails**; a large empty-string bucket = unassigned spend |
| `csp_technical_account_manager` | TAM / FDE | two catalog entries share this id |
| `csp_customer_success_manager` | CSM | doer emails |
| `csp_field_sales_representative` | FSR | doer emails |
| `csp_territory`, `csp_payee_country`, `csp_payer_country` | geo | |
| `csp_classification`, `customer_type`, `csp_dci_tier`, `csp_committed` | segmentation | `csp_committed` is the strings `"true"`/`"false"` plus nulls; null markers are inconsistent (`""` and `null` both occur) |

**Missing from CSP (validated by the reverse diff):**

- **All customer labels**: 0 `label`/`tag`/`project_label` dimensions (a regular tenant
  had 457). "Labels are missing" is exact.
- **Resource-level dimensions**: `resource_id`, cluster/GKE/Kubernetes, workload,
  reservation, caller IP, etc. CSP is aggregated — no resource granularity.
- **`system_label` dimensions are listed (1,193) but effectively unqueryable**: a
  1-month `genai/model_family` grouping timed out at the API edge (Cloudflare 524 after
  ~125s). Treat system_label grouping as unsupported in CSP.

**Also present:** curated attribution groups (AWS EC2 / S3 tiers / Flexsave breakdowns,
GCP instance types, …) usable for cross-customer service analysis.

### 3.3 Performance profile (validated)

| Query shape | Cold | Warm repeat |
|---|---|---|
| 1-month, one fixed group, top-10 | ~58s | ~3.5s |
| 3-month, one fixed group, top-10 | ~97s | ~3.5s |
| 4 low-cardinality fixed groups, 1 month | ~71s | — |
| any `system_label` group | **>125s → HTTP 524** | — |

Two consequences:

1. **The 2-minute `run_dci_command` timeout is marginal for CSP.** A cold 3-month query
   already runs ~97s; anything heavier dies at 120s before the API's own 524 arrives.
   → decision F4.
2. **Iterate-narrow-first is cheap**: the server result cache makes refinements ~3.5s,
   so the AI should issue a small 1-month scoping query before a wide one.
   (API bug worth filing: warm responses still report `cacheHit: false`.)

### 3.4 Baseline behavior without CSP knowledge (validated — the motivating failures)

- *"Which of my customers are growing the fastest?"* — 133s, 8 tool calls (including an
  authorization error on a reseller endpoint), ends **asking the user** what "customer"
  means. It cannot know: the prompt names neither the CSP tenant nor the signed-in user.
- *"Across all DoiT customers, which cloud provider grew fastest?"* — **silently wrong**:
  answered from the active tenant's own bill and presented it as the cross-customer
  answer (a footnote admitted "one customer at a time").

### 3.5 Changes

**F1 — Signed-in identity joins the volatile prompt.** Add
`Signed in as: <email> (Doer)` to `aiVolatileSystem` (claims already parsed from the
cached JWT by `cachedTokenClaims`; the TUI banner shows them today). Without this,
"my customers / my book" is unanswerable; with it, filtering
`csp_strategic_account_manager == <email>` is one query. Doer-gated: customers see no
identity line beyond what they already see (their own single tenant).

**F2 — Playbook: FinOps surfaces section** (§2.2).

**F3 — Doer-mode prompt section: "The CSP tenant".** Appended only when the session is
tenant-aware AND the cached token is a doer (`cachedTokenIsDoer()` — customers and
partners must never see CSP vocabulary). Content, all validated above:

- When a question spans multiple customers or a book of business, switch to
  `csp.doit.com` via `set_customer_context`; switch back (or to the specific customer)
  for deep dives.
- Dimension vocabulary: the `csp_*` table from §3.2, including the role dimensions and
  the email-valued AM fields; treat the empty-string bucket as "unassigned", exclude it
  by default and say so.
- Constraints: no labels/tags, no resource-level dimensions, never group by
  `system_label` (times out), data is less complete than the per-customer tenant — for
  label- or resource-level questions, name the customer and switch to it.
- Query discipline: month interval, top-N group limits, `metricFilter`, scope with one
  cheap 1-month query first (warm cache makes refinement ~3.5s).
- Worked example: *"which of my customers are growing fastest"* = 3-month monthly cost
  by `csp_primary_domain` filtered on `csp_strategic_account_manager == <signed-in
  email>` (mention CSM/FSR/TAM variants for doers whose book is a different role).

Estimated prompt cost: ~500 tokens, doer-cached-prefix only.

**F4 — Tool timeout must exceed CSP cold-query latency.** Options: (a) raise
`aiToolCommandTimeout` to 5 minutes globally; (b) 5 minutes only when the child argv is
a `query` (list endpoints stay at 2); (c) keep 2 minutes and teach the prompt to warn.
**Recommended: (b)** — queries are the only measured >2-minute legitimate children, and
a wedged non-query child should still die fast.

**F5 — Skill parity.** Mirror F2+F3 content into the installable skill
(`skills/dci-cli/`) so external agents (Claude Code, etc.) driving `dci` get the same
navigation; the CSP section ships in a new `references/csp-patterns.md` loaded on
demand, keeping customer-facing contexts clean (external skill consumers may be
customers — the reference must state it applies only to doer accounts, and the CSP
tenant simply errors for non-doers).

**F6 — Eval harness in-repo.** `eval/finops-prompts.txt` + `eval/run-finops.sh`
(this branch) as the regression suite; add the two CSP baseline prompts once F1–F4 land
so the suite covers the doer path.

### 3.6 Explicitly out of scope

- Fixing CSP `system_label` query timeouts, the misreported `cacheHit`, and label
  completeness — API/omni-side; file internally.
- Margin/revenue metrics in CSP (`extended` metric types) — not validated in this pass;
  the prompt must not promise them.
- A `/book` session command or AM-role auto-detection — revisit after F1–F3 dogfood.

---

## 4. Decisions (maintainer — decided 2026-08-24, all implemented on this branch)

| # | Decision | Decided |
|---|---|---|
| F1 | Identity in the volatile prompt | **Yes** — email + role for every session (volatile tail; `aiVolatileSystem`) |
| F2 | FinOps-surfaces playbook section | **Yes, full section** (`aiFinOpsSurfacesSection`, cached prefix; mirrored to SKILL.md) |
| F3 | Doer-gated CSP prompt section | **Yes** — gated on `cachedTokenIsDoer()`; customers/partners byte-identical (tested) |
| F4 | Timeout for CSP queries | **5 min for `query` children only** (`aiToolQueryTimeout`); others keep 2 min |
| F5 | Skill parity | **Yes** — `references/csp-patterns.md` (doer-only caveat up top) + FinOps surfaces in SKILL.md |
| F6 | Eval harness | **In-repo `eval/`** + P11/P12 doer/CSP prompts added to the suite |

Post-decision validation note: the book-of-business filter shape
(`filters: [{id, type: "fixed", values: [email]}]`) was validated live — and a filtered
cold CSP query returns in seconds (the filter narrows the scan), so filtered queries are
now the recommended CSP pattern in both the prompt and the skill.

Two follow-ups from the same observations, also implemented and validated live:

- **`--rollup <cols>`** — client-side aggregation (group by result columns, sum metric
  columns, drop the rest; `rolledUpFrom` marker, self-correcting `rollupError`). Period
  totals previously landed on the model as row-level arithmetic inside hidden reasoning
  — the dominant latency and the one measured accuracy risk. The model adopts it
  unprompted: a 3-month top-services totals question now resolves in one call, ~20s.
- **Session-scoped `set_customer_context`** — the agent's switch no longer writes the
  persisted context file; children get `DCI_CUSTOMER_CONTEXT`, the prompt/status line
  and user dispatches follow the override, and `/customer` persists as before while
  clearing it. Validated: two one-shot CSP runs left the saved context byte-identical.

## 5. Validation log

| Assumption | Method | Result |
|---|---|---|
| CSP reachable as `csp.doit.com` and by id | `dci validate` under `DCI_CUSTOMER_CONTEXT` | both resolve |
| CSP is doer-only | design: console gate + API token authorization | accepted (not independently testable without a non-doer account) |
| "Labels are missing" | full dimension diff CSP vs regular tenant | exact: 0 label/tag/project_label (vs 457); resource dims also absent |
| system_labels usable in CSP | 1-month `genai/model_family` grouping | **refuted** — HTTP 524 after ~125s |
| AM data present + filterable | grouped query on `csp_strategic_account_manager` | doer emails + one large unassigned (`""`) bucket |
| Segmentation dims value shapes | 4-way grouped query | `customer_type`/`csp_classification`/`csp_dci_tier` strings; `csp_committed` `"true"`/`"false"`+null |
| CSP query latency | timed cold/warm queries | 58s/97s cold, ~3.5s warm; `cacheHit` misreports false |
| AI handles multi-customer questions today | baseline runs B1/B2 | **refuted** — B1 asks the user; B2 answers from the wrong tenant |
| Session can switch to CSP | `validateCustomerContextValue("csp.doit.com")` + live validate | passes |
| Identity available for "my book" | `cachedTokenClaims()` carries email + DoitEmployee | yes; not currently in the model's prompt |
| FinOps-10 baseline | 10 one-shot runs, pty-traced | table in §1 |
| Exit-30 approval collision | live repro + `DCI_AGENT_MODE=1` exit code check | confirmed; fixed on this branch |
