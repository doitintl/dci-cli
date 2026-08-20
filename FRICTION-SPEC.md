# Design spec: closing the agent/human friction gaps in `dci`

Status: **merged 2026-08-19** via [PR #82](https://github.com/doitintl/dci-cli/pull/82) (rebase-merge, head `d537f7a`); unreleased — ships with the next tagged release, per the repo's batching policy. Untracked, not for commit.
Covers Phase 0 (docs & skill), P1+P3 (truncation notes, cap validation), P2 (`--all`), P5 (`--drop-unlabeled-rows`, with the semantics fix the verification re-run forced: all-label-dimensions-null rather than any-null, label groups read from the request config), and P6 (time-column dedupe). Verified by re-running the motivating journey: cold-start discovery is 1 command (was ~20), and the canonical query reproduces the original per-model numbers. Remaining: the §8 data-side asks (owner: maintainer, with the omni/API teams), and the post-release follow-through — installed skill copies refresh via `dci skill update`, and the help-center CLI reference regenerates from omni's `generate-cli-docs` action.
All §9 open questions are resolved with the maintainer (flag name and explicitness, no agent-mode auto-pagination, no `--nonzero`, `--all` pulled into Phase 1 — maintainer's call, against the draft's recommendation).
Audited at commit `eeef290`. Every claim cites the function and file it is based on; line numbers are approximate at that commit. Live-API probes were run 2026-08-19 against customer context `RSTDkHhaoGWwOEvlYlHyBUhm` with the installed `dci`.

Source material: [AGENT-FRICTION-NOTES.md](AGENT-FRICTION-NOTES.md), written during an agent session answering "token spend per AI model" via the CLI alone. This spec turns those notes into executable changes, corrects two factual errors in them (§2.1, §2.3), and separates CLI work from data-pipeline asks that don't belong in this repo (§8).

---

## 1. Summary

The session hit five distinct failures. Ranked by damage:

1. **Silent collection truncation.** List commands return one server page; in `csv`/`table` output the `pageToken` is dropped with no signal that anything is missing (§2.1). An agent shipped an analysis over 50 of 955 dimensions before noticing.
2. **A parameter-validation trap.** `--max-results 501` silently returns *fewer* rows than `--max-results 500` — the server resets out-of-range values to the default 50 (§2.2).
3. **Undocumented, silently-failing `--filter`.** The working syntax is exact-match `field:value`; every other form is ignored without error, returning the unfiltered listing as if it matched (§2.4).
4. **No way to exclude the null group bucket** from label-grouped report queries — a $418k noise row that every consumer must drop client-side (§2.5).
5. **Flat report output carries redundant and noise columns** — duplicate time columns, all-zero cross-product rows (§2.6).

The proposal, in one line each:

- **P1** — surface truncation everywhere: a stderr note when a page token exists and the output format can't carry it (§4.1).
- **P2** — `--all`: opt-in client-side auto-pagination for list commands, reusing the loop the CLI already ships for name resolution (§4.2). Explicit-only in every mode (§9.3).
- **P3** — client-side validation of paging params against the loaded OpenAPI spec, so out-of-range values fail loudly instead of shrinking the result (§4.3).
- **P4** — document what exists: `--filter` syntax, `--max-rows` scope, the pagination recipe, and the server-side `metricFilter` recipe for zero-row noise, in help text and the agent skill (§4.4).
- **P5** — `--drop-unlabeled-rows` (decided, §9.1): client-side removal of null-group report rows, plus an API ask for a real `exists` filter mode (§4.5).
- **P6** — flat-output shaping: suppress datetime dimension columns that duplicate the `timestamp` column, following the pivot classifier's own precedent (§4.6).
- **P7** — data-pipeline asks (genai label normalization, uniform token cost encoding, one null sentinel) filed as issues against the owning systems, not patched in the CLI (§8).

Everything here benefits both audiences: humans reading tables hit the same silent 50-row cliff and the same $418k noise row; agents just hit them faster and in bulk.

---

## 2. The failures, with root causes

### 2.1 Silent collection truncation (corrected)

Observed: `dci list-dimensions --max-rows 0 --output csv` returned 51 lines (50 dimensions + header) of a 955-dimension collection, with no indication of truncation.

Three mechanisms compound:

1. **`--max-rows` does not do what its position suggests.** It caps *report result rows* only — `effectiveMaxRows` is consulted exclusively in the report-container path of `transformSuccessBody` (response_transform.go:78, resolver at response_transform.go:403–418). It never touches list responses. But it is registered as a global persistent flag on every command (main.go:1622), so `list-dimensions --max-rows 0` parses fine and reads as "give me everything". The agent reasonably inferred list semantics; nothing corrected the inference.
2. **The server pages at 50 by default** (`maxResults` default from the OpenAPI spec), and the CLI issues exactly one request per invocation.
3. **`csv` and `table` outputs drop the wrapper.** Both render through `toTableRows` (csv_output.go:25; main.go:2080), which unwraps the collection array and discards its siblings — including `pageToken` (main.go:2345–2369). TOON deliberately keeps wrapper siblings *because* agents need them for pagination (comment at main.go:2129–2135, verified live: `pageToken: c2t1…` / `rowCount: 50` trail the listing). JSON/YAML keep them too.

**Correction to the friction notes:** agent-mode TOON *does* surface the page token. The observed data loss required the `--output csv` path (or `table`). The notes' claim that there is "no signal at all" is true only for csv/table — but those are exactly the formats a human reads and an agent reaches for when it wants grep-able output, so the severity stands.

The CLI already knows which responses are list-shaped and what the metadata keys are: `hasListMetadata` / `listWrapperRows` enumerate `pageToken`, `nextPageToken`, `cursor`, … (output_contract.go:235–268; same vocabulary in response_transform.go:519–526). The knowledge exists; it just doesn't reach the user on the lossy formats.

### 2.2 The `--max-results` reset trap (new finding)

Live probe, `list-dimensions` (2026-08-19):

| `--max-results` | rows returned |
|---|---|
| 100 | 100 |
| 400 | 400 |
| 500 | 500 |
| **501** | **50** |
| 1000 | 50 |

The server's cap is 500, and **out-of-range values are silently reset to the default 50** rather than clamped to 500 or rejected. So the natural "just ask for a big page" move returns less data than a modest ask, with no error. Worse, caps differ per endpoint: budgets and assets **reject** `maxResults` above 250 instead of resetting (comment + workaround at name_resolution.go:574–577) — two different failure modes for the same mistake.

The CLI loads the OpenAPI spec for every operation (restish loader registered at main.go:363; preflight already resolves the invoked operation via `invocationOperation`, invocation_preflight.go:97), so parameter bounds are — or can be made — available client-side (§4.3, §9.2).

### 2.3 `--max-results` wasn't the pagination fix anyway (corrected)

The friction notes claimed "`--max-results 1000` didn't help — the server caps the page size at 50". Wrong on both counts: the cap is 500, and values ≤500 are honored. Raising the page size *would* have fetched all 955 dimensions in two pages. The deeper point survives: nothing told the user a second page existed (§2.1), and no flag fetches it automatically (§4.2).

### 2.4 `--filter`: undocumented syntax, silent no-op on error

The help string (from the OpenAPI param description) says only: "An expression for filtering the results. The fields eligible for filtering are: type, label, key."

Live probes (2026-08-19, `list-dimensions`):

| expression | behavior |
|---|---|
| `type:system_label` | ✔ filters (exact match) |
| `label:genai/model` | ✔ filters (exact match, 1 row) |
| `type=system_label` | ✘ **silently ignored** — full unfiltered listing |
| `label contains genai` | ✘ silently ignored |
| `label~genai` | ✘ silently ignored |
| `label:genai*` | 0 rows (no glob — `*` matched literally) |
| `type:system_label label:genai/model` | 0 rows (space-joined terms treated as one value) |

Exact-match-only makes `--filter` useless for the discovery task that motivated it ("find the dimension for AI models" — you must already know the name). And silent fallback to unfiltered output is worse than an error: a human skims plausible rows and moves on; an agent reads 50 unfiltered rows as "no match for my filter" or, worse, as a confirmation.

Whether match modes beyond exact exist server-side is unknown; the syntax is not in the public help. §4.4 documents what is verified; §8.3 asks the API for substring match or documents that it will never exist.

### 2.5 No "dimension is not null" filter

Grouping all billing data by a sparse system label produces a null-group row aggregating everything unlabeled — $418k against $207k of signal in the session's query. Probes: `filters[].mode: regexp` with `.*` and `.+` both still return the null bucket (includeNull defaults to false; the bucket is evidently not treated as a filterable null value). There is no `exists`/`not null` mode in the filter enum (`is`, `starts_with`, `ends_with`, `contains`, `regexp` — query `--help-full` schema).

Client-side, the machinery for dropping rows by shape already exists: `dropEmptyReportRows` removes rows with a null dimension *and* all-zero metrics (response_transform.go:588–612, gated by `--include-empty-rows`). The null bucket survives because its metric is huge — the rule is deliberately conservative. §4.5 adds the explicit opt-in.

### 2.6 Flat-output noise

Two independent nuisances in `--flat`/csv report output:

- **Redundant time columns**: each row carries `month`, `year`, *and* `timestamp` for the same bucket. The pivot classifier already encodes the judgment that `timestamp`-typed columns are redundant with the datetime dimension columns (`classifyPivotColumns` skips them, pivot.go:202–203); the flat path just never got the memo.
- **All-zero cross-product rows**: grouping by N labels emits rows for every observed combination, including structurally-zero ones (`code_execution` × every model × every product ≈ 40% of one session query). These have non-null dimensions, so `dropEmptyReportRows` correctly keeps them — "a real group with a genuine zero cost" (response_transform.go:588–591) — but at multi-label grain they are almost always noise.

---

## 3. Design principles

1. **Never lose data silently.** Any response wider than what is rendered must say so, on every output format, the way report row-capping already does (stderr note at response_transform.go:84–86).
2. **Machine output stays deterministic.** No auto-pagination by default in agent mode (§9.3): an agent transcript must not change shape because a collection grew past a page boundary. Opt-in via `--all` is explicit and visible in the transcript.
3. **Validate client-side what the server mishandles.** The 501→50 reset is a server bug from the CLI's point of view; the CLI is the layer that can afford to be strict (§4.3).
4. **Prefer documentation to code when the mechanism exists.** Half the session's friction was discovery cost against working, undocumented features (`--filter` exact match, TOON's pageToken, `--max-rows` scope). The `dci skill` channel (skill_management.go) and the omni-generated help-center docs exist precisely to amortize this.
5. **Don't patch data problems in the display layer.** Unnormalized `genai/model` values and per-provider cost encodings are pipeline defects; a CLI-side rename table would rot instantly (§8).

---

## 4. Proposals

### 4.1 P1 — truncation visibility (Phase 1)

When a rendered response is list-shaped (`hasListMetadata`, output_contract.go:261) and carries a non-empty `pageToken`/`nextPageToken`/`cursor`, and the output format is one that drops wrapper siblings (`table`, `csv`), emit one stderr note:

```
note: more results available; showing first 50. Re-run with --page-token c2t1X2Rlc2NyaXB0aW9u, raise --max-results, or pass --all
```

- Include the actual token so the fix is copy-pasteable, mirroring the hidden-columns hint that echoes a ready-made `-C` list (main.go:2518–2519).
- Include `rowCount` when present. If the API ever grows a `totalCount`, "50 of 955" beats "first 50" — worth an API ask (§8.3).
- Emission point: the render path where format is known — table marshal (main.go:2080 vicinity) and `dciCSVContentType.Marshal` (csv_output.go:16) — not `transformSuccessBody`, which runs before format dispatch for some paths and would double-note.
- TOON and JSON/YAML: no note; the token is already in-band (main.go:2135). Do **not** add stderr chatter to formats agents parse structurally.
- `--quiet`-style suppression rides whatever convention the existing notes use (they are unconditional today; keep parity).

### 4.2 P2 — `--all` auto-pagination (Phase 1)

New global flag, effective only on operations whose spec declares both a page-size and page-token parameter; a no-op with a stderr warning elsewhere.

Semantics:

- Fetch pages sequentially until `pageToken` is empty, requesting the endpoint's max page size (from the spec if declared, else 500 with the budgets/assets 250 exception — the same table `fetchResourceNames` hardcodes, name_resolution.go:574–577; §9.2 covers extracting bounds from the spec instead).
- Merge: concatenate the collection arrays (`listWrapperRows` identifies the key, output_contract.go:235), sum `rowCount`, drop the final empty `pageToken`, then hand the merged body to the normal transform/render pipeline so every format sees one big page.
- Safety valve: hard page cap (default 40 pages ≈ 20k rows; `--all` + cap hit → stderr note with the resume token). Loop, token threading, and bearer/tenant header handling copy `fetchResourceNames` (name_resolution.go:565–626) — the CLI already ships this exact loop for name resolution with `maxPages` discipline; P2 is a generalization, not new machinery.
- Mutual exclusion: `--all` with explicit `--page-token` or `--max-results` is an error — mixed intent.
- `--all` composes with `--filter`, `-C`, `--fields`, `--output` — all downstream of the merge.

### 4.3 P3 — client-side paging-param validation (Phase 1)

In preflight, where the operation is already resolved (`preflightAPIInvocation` → `invocationOperation`, invocation_preflight.go:42, 97): if the invocation passes a page-size parameter and the spec declares a `maximum` (or the CLI knows an endpoint cap), reject out-of-range values with a structured error naming the cap — never forward a value the server will mangle:

```
error: --max-results 501 exceeds this endpoint's maximum of 500 (the API silently resets out-of-range values to 50)
```

Reject rather than clamp: a clamp would still surprise ("I asked for 1000, got 500"), and the structured-error contract (error_contract.go) gives agents a parseable reason to retry correctly. If the spec doesn't declare bounds (§9.2), fall back to the known per-endpoint table, and file the omission upstream.

### 4.4 P4 — document what exists (Phase 0, no code except help strings)

1. **`--max-rows` help text** (main.go:1622): append "report result rows only; list commands page server-side — see --max-results/--page-token/--all". Same clarification wherever the flag table is mirrored in omni docs.
2. **`--filter` syntax**: document verified behavior — exact-match `field:value`, one term, no globs, unknown expressions currently ignored server-side — in the command's help via the CLI's help augmentation, and in the help-center command notes (precedent: the insights view's mirrored notes, response_transform.go:102–105 → omni `command-notes/get-insight-results.mdx`).
3. **Agent skill** (`dci skill <agent>`, skill_management.go): add a "collections & pagination" section — TOON keeps `pageToken`; csv/table don't (until P1); the pagination recipe; the 500/250 caps; the exact-match filter; and the report-query recipes discovered in the session (system-label discovery via paginated `list-dimensions` + local grep; null-bucket drop via `--drop-unlabeled-rows`; the `genai/*` taxonomy pointer; and the server-side zero-row recipe — `metricFilter: {metric: {type: basic, value: cost}, operator: ">", values: [0]}` — which is the decided answer to §2.6's all-zero rows, in place of any new flag, §9.4). This is the highest-leverage change in the whole spec: it converts one session's discovery cost into every future agent's starting knowledge, and it ships without touching runtime code.
4. **README/docs**: nothing user-facing changes in Phase 0 beyond generated flag help; keep restish invisible per repo convention (AGENTS.md).

### 4.5 P5 — null-group row control (Phase 2 + API ask)

- CLI: new report-query flag `--drop-unlabeled-rows` (decided, §9.1 — explicit-only, never auto-enabled): drop rows where **any** grouped dimension cell is null, regardless of metric value — the complement of today's conservative `dropEmptyReportRows`. Implemented next to it (response_transform.go:588), same `emptyRowsDropped`-style counter in the body (`unlabeledRowsDropped`) so machine consumers see the shaping.
- API ask (filed upstream, §8.3): an `exists` filter mode, so the rows never cross the wire. The CLI flag remains useful regardless (works today, works on old servers).

### 4.6 P6 — flat-output time-column dedupe (Phase 2)

In flat/csv report rendering, when the schema contains both a `timestamp`-typed column and datetime dimension columns for the same grain, suppress the datetime dimension columns by default (keep `timestamp` — it is the machine-sortable one), following `classifyPivotColumns`' redundancy judgment (pivot.go:202–203). Escape hatch: they reappear under `-C`/`--fields` explicit selection (precedent: explicit selections are never dropped, toonRowOptions, main.go:2161–2169). All-zero row suppression gets **no flag** (decided, §9.4): genuine zeros are information (response_transform.go:590–591), and the server-side `metricFilter` recipe (§4.4.3) already removes them before they cross the wire.

---

## 5. What never changes

1. TOON/JSON/YAML keep wrapper metadata in-band, byte-stable — no stderr notes added to them (§4.1).
2. No auto-pagination without `--all`; agent-mode defaults unchanged (§3.2, §9.3).
3. `--max-rows` semantics (report rows only) — clarified, not changed.
4. `dropEmptyReportRows` default behavior — P5 is a separate opt-in.
5. No CLI-side rewriting of data values: no model-name normalization tables, no `[Value N/A]`→`""` mapping (§8.2) without a data-side decision first.
6. Restish stays pinned at v0.21.2 (AGENTS.md); nothing here needs upstream restish changes — the pagination loop is CLI-side HTTP, same as name resolution today.

---

## 6. Phasing

**Phase 0 — docs & skill only.** §4.4. No behavior change, ships in a day, converts the session's discoveries into shared knowledge. `docs:`/`chore:` commits (changelog-invisible per AGENTS.md).

**Phase 1 — loud failures + `--all`.** P1 truncation notes, P3 param validation, and P2 auto-pagination. (P2 pulled forward from a separate phase by maintainer decision 2026-08-19 — the agent-ergonomics win ships sooner; the draft's preference for a smaller first review was overruled. Mitigation: land P1+P3 and P2 as **separate commits** so each stays review-sized, even though they ship in the same release.) `feat:`/`fix:` commits, patch release per versioning policy.

**Phase 2 — report shaping.** P5 (`--drop-unlabeled-rows`) + P6 (time-column dedupe).

**Parallel — upstream issues (§8):** file immediately; none block Phases 0–2.

---

## 7. Testing strategy

- **P1**: fixtures for list responses with/without `pageToken` rendered as table/csv/toon/json — note appears exactly on {table,csv}×{token present}, absent otherwise; note text contains the literal token; TOON/JSON byte-identical to today.
- **P3**: preflight unit tests per parameter source (spec-declared max, hardcoded endpoint table, no bounds) × (in-range, boundary, out-of-range) — structured error shape asserted via the existing error-contract tests (error_contract_test.go).
- **P2**: pagination loop against a stub server (httptest, as name_resolution_test.go does today): multi-page merge, rowCount summing, page-cap hit with resume token, empty first page, token loop (same token twice → error, not infinite loop), budgets/assets 250 cap, `--all`+`--page-token` mutual exclusion.
- **P5/P6**: golden-file tests alongside the existing report transform fixtures (response_transform_test.go): null-bucket dropped with counter; timestamp dedupe on daily and hourly fixtures; `-C` re-adding a suppressed column.
- **Live-probe regression note**: the 501→50 server behavior and the filter syntax table (§2.2, §2.4) are server-side facts that can drift; record them in the test suite as skipped-by-default integration probes (env-gated, like any live tests) so a future change is detected rather than re-discovered.

---

## 8. Data-side asks (filed upstream, not fixed here)

Owning systems: the DoiT analytics pipeline / API (omni). File as issues; link them from the friction notes.

1. **Normalize `genai/model`.** The same model appears as `Claude Opus 4.8`, `claude-opus-4-8`, and Cursor's `claude-opus-4-8-thinking-high`; `genai/base_model` maps in *both* directions and is empty for Cursor rows. Ask: one canonical `genai/base_model` spelling, populated for every source, effort/thinking suffixes normalized away.
2. **Uniform token-cost encoding.** Anthropic: `genai/cost_type=tokens`; OpenAI: only `genai/usage_type` (`Input`/`Output`/cache); Cursor: only `genai/billing_category`. Ask: `genai/cost_type` populated consistently. Also: one null convention — the API emits both absent labels and the literal string `[Value N/A]`, which render differently in CSV and force dual handling.
3. **API ergonomics**: (a) `exists`/not-null filter mode (§4.5); (b) `totalCount` on paged collections (§4.1); (c) page-size `maximum` declared in the OpenAPI spec for every paged endpoint, and out-of-range handling changed from silent-reset to a 400 (§2.2); (d) substring match mode for `list-dimensions --filter`, or documentation that exact-match is the contract (§2.4).

---

## 9. Decisions (all landed with the maintainer, 2026-08-19)

1. ~~**P5 flag name**~~ — **decided: `--drop-unlabeled-rows`, explicit-only.** Verb-first and self-documenting, parallels `--include-empty-rows`. No auto-enable when all groups are labels: the null bucket is sometimes the question itself ("how much spend is *not* labeled?"), and implicit row-dropping in an analytics tool silently reshapes totals.
2. ~~**Spec-declared bounds**~~ — **resolved 2026-08-19 against the live spec**: `api.doit.com/openapi.yaml` has 15 `maxResults` parameter definitions; most declare `maximum` (observed combos: max 500/default 50, max 200/default 50, max 100/default 50 or 100, max 5000/default 1000) and a few declare no bounds at all. So P3 reads bounds from the loaded spec, falling back to the name_resolution.go table only for the unbounded declarations — and §8.3(c) narrows to "declare `maximum` on the stragglers".
3. ~~**`--all` in agent mode**~~ — **decided: no auto-pagination anywhere; `--all` is always explicit.** Deterministic transcripts and predictable token cost win; the P1 note plus skill guidance make the opt-in discoverable at the moment of truncation. Revisit only if transcripts show agents pagination-looping by hand anyway.
4. ~~**`--nonzero`**~~ — **decided: no flag.** The server-side `metricFilter` recipe (already in the query schema, filters before rows cross the wire, works in the Console too) is documented in the P4 skill text (§4.4.3) instead.
5. ~~**Phase ordering**~~ — **decided: P2 pulled into Phase 1** (maintainer's call; the draft recommended keeping it separate). Session evidence: P1+P4 would have saved ~6 of the session's ~20 tool calls, P2 another ~4. Mitigation for review size: P1+P3 and P2 land as separate commits within the phase (§6).
