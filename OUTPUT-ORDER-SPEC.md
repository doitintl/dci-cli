# Design spec: configurable terminal-friendly output ordering in `dci`

Status: **draft for maintainer review**.
Audited at commit `0cbe012`; every claim cites the function and file it is based on, line numbers approximate at that commit. Builds on the chart-placement fix in [PR #133](https://github.com/doitintl/dci-cli/pull/133), which this spec assumes lands first — `noteChartWidth`/`flushPendingChart` exist on that PR's head (`1eb5385`, charts.go), **not yet on main**: at the audited commit the chart still prints *above* the table (`maybeRenderChart` runs inside `renderTable` before the marshaled bytes reach stdout, main.go:3169), which is exactly what #133 fixes.

Scope: the ordering of *rows* in human-terminal output — which end of a table carries the content the reader came for. Everything here is **human-presentation only**: agent mode, machine formats (json, yaml, csv, toon), and piped output are byte-for-byte unchanged, same discipline as TUI-SPEC §3.1 and TIMEZONE-SPEC §4.3. Per the maintainer's steer, the configuration surface is a first-class part of the design (§6), not an afterthought.

---

## 1. Summary and decisions

The originating feedback (Alfredo, dogfood session, 2026-08-28, verbatim):

> It might sound unorthodox, but as the terminal is scroll-up by default (on contrary to web UIs that are scroll-down), what if we invert the output defaults? For example, table values ascending instead of descending, chart last instead of up there, etc. (`dci get-report --chart "Customers by Cloud Spend"` as an example of heavy scroll up for meaning content). Maybe the direction could be contextual, depending on the lines of output VS. the terminal height.

The chart half is fixed by PR #133 (open at audit time; see the header note for the exact state on main): it moves the `--chart` render to after the table reaches stdout, so the chart sits under the table, next to the prompt. This spec covers the deferred half — the ordering of the data itself — which was split out because it changes data presentation for every consumer and deserves its own decision.

| # | Decision | Recommendation |
|---|----------|----------------|
| D1 | What flips | The three client-side rankings the CLI itself imposes: curated list-view recency sorts, the insights savings ranking, and the pivot's group ranking (§4). Flat report rows already read terminal-first and stay as they are. |
| D2 | What never flips | Machine formats, agent mode, piped output, API-semantic server order, column order (periods stay oldest→newest left→right), totals placement (already at the bottom) (§4.3). |
| D3 | Contextual (height-dependent) direction | **Rejected as a default**; the value spelling `auto` is reserved if it is ever wanted (§5). |
| D4 | Configuration surface | `--output-order` flag > `DCI_OUTPUT_ORDER` env > persisted `cli_settings.json` > built-in default (§6). |
| D5 | Default | `terminal` — the fix is the point — shipped in the same release that updates the help-center docs; `classic` is the one-word opt-out. Maintainer call; the soft-launch alternative is documented (§10, Q1). |
| D6 | Where the code lands | New sibling `output_order.go` + `output_order_test.go`; the three sort sites take a direction, nothing else moves (§9). |

Vocabulary used throughout: **`terminal` ordering** anchors the most important row nearest the prompt (the terminal fills bottom-up into scrollback); **`classic` ordering** is today's behavior, matching the DoiT console's web tables (most important row first, reading top-down).

---

## 2. The problem

A web UI is scroll-down: the viewport starts at the top of the content, so the most important row goes first. A terminal is scroll-up: after a command finishes, the user is looking at the *last* lines of output, and everything above may already be in scrollback. Today's orderings are web orderings:

- `dci list-reports` puts the most recently updated report on the **first** row (`sortRowsByEpochDesc`, list_views.go:356) — for a long listing, the rows still on screen are the *stalest* ones.
- `dci list-insights` leads with the highest-savings insight (`sortInsightsBySavings`, response_transform.go:256) — the reader lands on the least valuable tail.
- `dci get-report` (pivot view) ranks the biggest spender **first** (pivot.go:116) — Alfredo's `--chart "Customers by Cloud Spend"` example: the meaningful rows scroll away, the long tail sits at the prompt.

Two orderings are *already* terminal-friendly and are cited here as precedent, not as changes:

- The pivot's `TOTAL` row(s) are appended **last** (pivot.go:135–147) — the summary is the line nearest the prompt.
- Flat report rows sort ascending cell-by-cell with time columns leading (`sortReportRows`, response_transform.go:617), so the newest period is the last row.

---

## 3. Audit: every ordering the CLI controls

Orderings the CLI imposes client-side (candidates to flip):

| Site | Function | Today (classic) | Under `terminal` |
|------|----------|-----------------|-------------------|
| Curated list views with `sortField` (`list-reports`, `list-allocations`, `list-alerts`) | `sortRowsByEpochDesc` (list_views.go:356), declared per view (list_views.go:78, :103, :214) | Newest first | Oldest first — newest row lands at the prompt |
| `list-insights` savings ranking | `sortInsightsBySavings` (response_transform.go:256) | Highest savings first | Lowest first — the top insight lands at the prompt |
| Pivot group rows | `sort.SliceStable(groupOrder, …)` (pivot.go:116) | Largest row total first | Smallest first — biggest spenders sit just above `TOTAL` |

Orderings that are already terminal-shaped (unchanged in both modes):

| Site | Function | Behavior |
|------|----------|----------|
| Flat report rows | `sortReportRows` (response_transform.go:617) | Ascending cell-by-cell, time leads → newest period last |
| Pivot `TOTAL` rows | pivot.go:135–147 | Appended after the group rows — bottom of the table |
| `--chart` | `noteChartWidth`/`flushPendingChart` (charts.go on PR #133's head `1eb5385`; on main the pre-fix `maybeRenderChart` call still renders it above the table, main.go:3169) | Renders after the table is on screen once #133 lands |
| Hidden-columns hint | `renderHiddenColumnsHint` (main.go:3171) | Appended under the table |
| Update notice | deferred `maybeNotifyUpdate` (main.go:723, update.go:70) | Last thing printed, on stderr |

Orderings the CLI does **not** own (never touched, either mode):

| Site | Why it is semantic |
|------|--------------------|
| `list-anomalies` | Server-side `--sort-by`/`--sort-order` — the user's explicit request (list_views.go:17–19) |
| `list-budgets` under `riskStatus:atRisk` | Server orders by projected breach date (list_views.go:19) |
| Every list view without a `sortField` | API order is plain but not ours to reinterpret; reversing an order we do not understand is not "ascending", it is scrambling |
| Pivot period **columns** | Columns are read left→right regardless of scroll direction; they stay oldest→newest (pivot.go:88) |
| Interactive table viewer (`-M interactive`) | Full-screen viewport with its own sort keys (tui_viewer.go:159) — there is no scrollback to compensate for |

---

## 4. Semantics of `terminal` ordering

### 4.1 The principle

One sentence, applied uniformly: **the row the reader came for lands nearest the prompt.** Per data type that means:

- **Recency** (list views sorted by an epoch field): oldest above, newest below.
- **Magnitude/rank** (insights savings, pivot group totals): smallest above, largest below.
- **Time periods as rows** (flat report rows): oldest above, newest below — already the case.
- **Summary rows** (pivot `TOTAL`): below the data — already the case.

`terminal` ordering is a pure reversal of the ranking direction. It never changes *which* rows render, *which* columns render, or the column order — the same cells appear, mirrored vertically.

### 4.2 The gate

The flip applies only when all of these hold:

1. The resolved output format is `table` or `auto` (`rsh-output-format`, set in the `PersistentPreRunE`, main.go:2136–2147). TOON is a presentation format for the *curated views* (list_views.go ground rules) but its consumers are agents; it keeps classic ordering even at a TTY.
2. A human is watching: `tuiActive()` (tui.go:30) **or** `sessionRenderActive()` (main.go:1212) — the `dci ai` session transcript scrolls exactly like a terminal, so dispatch children flip too.
3. Not agent mode (already implied by `tuiActive`; stated for the session-render leg, whose children run `DCI_AGENT_MODE=0`, ai_tui.go:1855).

A useful corollary: piped output — including `dci list-reports | less` — keeps classic ordering *regardless of configuration*, because `tuiActive()` is false on a pipe. That is correct on its own merits: a pager is scroll-down, like a web page. The configuration (§6) selects which ordering humans get; it does not force reordering onto machine consumers.

### 4.3 What never changes (the "never" list)

Mirroring TIMEZONE-SPEC §8:

1. **JSON, YAML, CSV bytes.** The transform-level sorts that exist for determinism (`sortReportRows`; `sortInsightsBySavings` runs before the presentation gate, response_transform.go:167) keep their current direction in machine formats.
2. **TOON bytes**, at a TTY or not.
3. **Agent mode**, in every format.
4. **Piped/redirected stdout**, in every format.
5. **Server-imposed order** (anomalies `--sort-by`, budgets at-risk ordering, any view without a declared client-side sort).
6. **Column order** — the pivot's `group, periods…, total, trend` order (pivot.go:176–185) and every curated view's column list.
7. **Row identity** — empty-row dropping, `--search` filtering, `--max-rows` capping all select the same rows in either ordering (§7.3).
8. **Exit codes, stderr contracts, pagination notes** — wording and routing untouched.

---

## 5. Contextual mode: considered and rejected

Alfredo's "maybe the direction could be contextual, depending on the lines of output vs. the terminal height" was weighed and is **not recommended**, for four reasons:

1. **Predictability.** The same command in the same shell would order rows differently after a window resize, in a tmux split, or on a colleague's screen. Ordering is the kind of output property users build muscle memory against ("the newest is at the bottom"); a conditional direction breaks that permanently — there is never a rule of thumb to learn. This is the same reasoning that made the pivot unconditionally the human report view rather than a row-count heuristic (`shouldPivotReportRows`, response_transform.go:469).
2. **Height is unreliable exactly where it matters.** Width has a solid precedent (`detectTerminalWidth` probes stdout then `COLUMNS`, main.go:3250; `tuiWidth` for prompts, tui.go:66; the `dci ai` dispatcher forwards `COLUMNS` to its children, ai_tui.go:1857). Height has no equivalent today: dispatch children have no PTY and no `LINES`, so the session — the surface Alfredo was using — would need a new `LINES` forwarding convention just to make the heuristic fire, and plain `LINES` is not exported by default in most shells.
3. **The threshold is unknowable before rendering.** Row count is known pre-render, but rendered *line* count is not: `-M wrap` multiplies lines per row (main.go:2092), the chart adds 12–15 lines after the fact, and the hidden-columns hint wraps. Any pre-render estimate misclassifies near the boundary — and near the boundary is where a short table would suddenly flip.
4. **Short output makes the flip harmless anyway.** When everything fits on screen, both orderings are fully visible; the cost of `terminal` ordering on a 5-row table is one glance upward. A heuristic would spend its complexity budget on the case where the choice matters least.

The value spelling `auto` is **reserved** so the door stays open without committing to semantics now: it is handled like any other unaccepted value under the one parsing rule in §6.1 (flag hard-errors, env/file warn and fall through), with error/warning text that says "reserved, not supported yet" instead of listing it as a typo. If it is ever built, the measurement should mirror the width precedent: `term.GetSize` on stdout for height, `LINES` fallback, and a `LINES=<height>` entry in `aiDispatchEnv`.

---

## 6. Configuration surface

The maintainer's explicit requirement: configurable, first-class. Three layers, one precedence rule, one spelling.

### 6.1 Naming and values

- **Flag**: `--output-order <terminal|classic>`, a persistent string flag on the `dci` command group, registered in `addOutputFlag` (main.go:2085) beside `--output`/`--table-mode`, with static completion via `registerStaticFlagCompletions` (main.go:2122).
- **Env var**: `DCI_OUTPUT_ORDER=<terminal|classic>`. Value-carrying (not boolish) because the domain is an enum with a reserved third value; precedent for value-carrying `DCI_*` vars: `DCI_TZ` (main.go:3672), `DCI_AI_EFFORT` (ai_session.go:151).
- **One parsing rule** (authoritative; §5 and §9.1 defer to it), matching how the CLI already splits the two cases: a value the layer does not accept — a typo or the reserved `auto` — is a **hard usage error on the flag** (an explicit per-invocation ask fails loudly, exactly like invalid `--output`, main.go:2145) and a **warn-once-and-fall-through in the env var and the persisted file** (ambient configuration must not break every invocation over a typo — the `DCI_AGENT_MODE` rule, main.go:689–693, and the `DCI_TZ` fallback, main.go:3674). `auto` differs only in wording: "reserved, not supported yet" rather than the unknown-value message.
- **Persisted setting**: `{"output_order": "classic"}` in a new `cli_settings.json` in the config dir (`dciConfigDir()`, main.go:131), handled exactly like `ai_settings.json` (ai_session.go:75–95): read-tolerant, `0o600`, absent file = defaults. A new file rather than a new key in `ai_settings.json` because that file is the AI session's chapter and this setting governs the whole CLI; rather than restish's `apis.json` because `ensureConfig` (main.go:171) treats that file as restish's contract.
- **Setter**: `dci config output-order <terminal|classic>` (and bare `dci config` printing the resolved values with their sources). A small command in the new chapter file, following the `registerStatusCommands` pattern (main.go:1733). Editing JSON by hand must never be the only path to a persisted preference. `dci status` gains one line — `Output order: terminal (default)` / `… (DCI_OUTPUT_ORDER)` / `… (cli_settings.json)` — matching how it attributes the API base and customer context today (main.go:1751, :1773).

### 6.2 Precedence

```
--output-order flag  >  DCI_OUTPUT_ORDER  >  cli_settings.json  >  default (terminal)
```

Resolved once per invocation in the `PersistentPreRunE` into a viper key (`output-order`), the same always-reset discipline as every other table key there (main.go:2150–2258, "viper state persists across in-process runs"). The three sort sites consult a single helper (§9.1); nothing reads the env or file twice.

### 6.3 `dci ai` sessions

Dispatch children (`aiDispatchEnv`, ai_tui.go:1853) need **no new plumbing**: they share the parent's process environment (`aiChildEnv` extends `os.Environ`) and config dir, so both `DCI_OUTPUT_ORDER` and `cli_settings.json` reach the child naturally, and `DCI_SESSION_RENDER=1` puts the child on the human leg of the gate (§4.2). A user typing `list-reports --output-order classic` inside the session gets the flag through argv like any other flag. One test pins this (§9.3).

### 6.4 Why not a boolish `DCI_CLASSIC_ORDER`?

The `DCI_NO_*` negative-toggle convention (`DCI_NO_TUI`, `DCI_NO_RESOLVE`) fits kill switches — features that are on or off. Ordering is a choice between two (potentially three, §5) named behaviors, and the flag, env var, and persisted key should share one vocabulary. A boolean would also paint `auto` into a corner.

---

## 7. Interactions

### 7.1 `--chart`

Terminal-ordered once PR #133 lands: the chart renders after the table is on stdout. Two consistency points this spec pins:

- Chart **period axis** stays oldest→newest left→right in both orderings — time on a chart axis is not a scroll-direction question.
- The stacked chart's **group fold** consumes `groupOrder` largest-first to keep the top `chartMaxGroups` and fold the tail into "other" (`setChartSeries`, charts.go:58–82; pivot.go:154–168). The pivot must therefore build the chart series from the **classic** (descending) ranking and reverse only the emitted rows — reversing before the fold would chart the five *smallest* groups. This is the one real footgun in the implementation and gets a dedicated test (§9.3).

### 7.2 `--all` pagination

The paginating transport merges every page into one collection **before** restish parses it (pagination.go:177–239), and every ordering in §3 is applied after that merge in the response transform. So `terminal` ordering is coherent across pages by construction — there is no per-page ordering to keep straight. The page-cap resume note and `notePageTokenDropped` (pagination.go:336) are stderr chatter and unaffected.

### 7.3 `--max-rows`

The report-row cap slices `rows[:limit]` in the flat path (response_transform.go:103–112) and does not apply to the pivot (the pivot returns earlier, response_transform.go:97–101). Flat rows are unchanged by this spec, so the cap keeps exactly today's row selection. Rule for any future capped-and-flipped path: **cap in classic ranking, then mirror for display** — ordering must never change which rows survive (§4.3 #7).

### 7.4 `--output csv/json/yaml/toon`

Untouched (§4.3). Note the subtlety that machine formats are not "unsorted": `sortReportRows` determinism and the insights savings ranking are part of today's machine-visible bytes and keep their direction. The gate lives at the *direction decision*, not around the sort calls.

### 7.5 Hidden-columns hint and update notice

Both already sit at the terminal-friendly end — the hint is appended under the table (main.go:3171), the update notice is a deferred last write to stderr (main.go:723). Under `terminal` ordering they read even better (summary, escape hatches, and housekeeping nearest the prompt); no change either way.

### 7.6 Interactive viewer and heatmap

`-M interactive` is out of scope (§8): a full-screen viewport has no scrollback problem, and its own sort keys already let the user choose direction. The pivot heatmap's totals-row detection keys off the *last* `pivot-total-rows` rows (main.go:4048) — totals stay last in both orderings, so the shading logic is untouched.

---

## 8. Non-goals and scope cuts

- **No contextual/`auto` mode** (§5) — spelling reserved, semantics deferred.
- **No new sort keys.** This spec chooses the direction of existing rankings; it does not add `--sort-by` to list commands or the pivot. That is a separate (worthwhile) feature with API implications.
- **No `-M interactive` changes**, including its initial sort direction.
- **No TOON/agent shaping changes**, including the `trend` column and omission markers.
- **No column-order changes** anywhere.
- **No change to the `dci ai` transcript's own layout** — the session renders dispatch output as-is; it inherits the ordering through the child (§6.3).
- **Phase 2 (only if wanted after phase 1 feedback)**: the `auto` height heuristic with `LINES` forwarding; per-command overrides if any view proves to want its own default.

Phase 1 is the whole of §4 + §6: the three direction flips, the three-layer configuration, `dci config`, and the `dci status` line.

---

## 9. Implementation sketch

Everything stays inside `package main`, chapter-per-file (AGENTS.md).

### 9.1 New sibling `output_order.go` (~120 lines)

```go
// parseOutputOrder validates a spelling: "terminal", "classic", or
// "" (unset); anything else (typos and the reserved "auto" alike) is
// not accepted — the caller decides the severity per §6.1's rule:
// usage error on the flag, warn-and-fall-through for env/file.
func parseOutputOrder(v string) (order string, ok bool)

// resolveOutputOrder applies flag > env > cli_settings.json > default
// once per invocation; called from the PersistentPreRunE. Returns an
// error only for an invalid flag value (§6.1).
func resolveOutputOrder(flagValue string) (string, error)

// terminalOrderActive is the single question the sort sites ask:
// resolved order == "terminal" AND the §4.2 gate holds.
func terminalOrderActive() bool

// cli_settings.json load/save, mirroring ai_session.go:75–95.
type cliSettings struct{ OutputOrder string `json:"output_order,omitempty"` }
```

Plus the `dci config` command registration (or a sibling `config_command.go` if it outgrows the chapter).

### 9.2 Touches to existing chapters (all direction-only)

- **main.go** `addOutputFlag`: register `--output-order` + completion; resolve into viper in the `PersistentPreRunE` beside the other always-reset keys.
- **list_views.go**: `sortRowsByEpochDesc` gains a direction (rename to `sortRowsByEpoch(items, field, newestFirst bool)`); `applyListView` passes `!terminalOrderActive()`.
- **response_transform.go**: `sortInsightsBySavings` likewise. The call stays where it is (before the presentation gate) so machine formats keep today's bytes; only the direction consults the helper.
- **pivot.go**: rank `groupOrder` descending exactly as today, hand the chart its series (§7.1), **then** reverse `groupOrder` for row emission when `terminalOrderActive()`. `TOTAL` rows stay appended last.
- **main.go** `registerStatusCommands`: the one-line attribution in `dci status`.

No restish, transport, or renderer changes; `renderTable` never learns about ordering.

### 9.3 Testing plan (style of the existing `_test.go` siblings)

- `output_order_test.go`: parse table (valid, unset, typo, reserved `auto` — asserting §6.1's split: flag → usage error, env/file → warn once and fall through); precedence table across flag/env/file/default (`t.Setenv`, temp config dir — the `resolveAIEffort` test pattern); gate truth table over `tuiActive`/`sessionRenderActive`/agent/format.
- `list_views_test.go`: one view with `sortField` asserted both directions; a view without `sortField` asserted **unchanged** under `terminal` (the §3 "never scramble API order" rule).
- `response_transform_test.go`: insights ranking both directions; JSON bytes identical under `terminal` (machine-format invariant).
- `pivot_test.go`: group rows reversed, `TOTAL` still last, heatmap `pivot-total-rows` still correct; **the chart-fold test** — with >`chartMaxGroups` groups and `terminal` ordering, `setChartSeries` still receives the largest-first series and folds the tail.
- `ai_picker_test.go`/`ai_tui_test.go`: `aiDispatchEnv` passes `DCI_OUTPUT_ORDER` through when set in the parent environment (inheritance pin, §6.3).
- Existing suites are the regression net: they run headless (`tuiActive` false), so every current assertion doubles as proof that non-TTY output is byte-identical.
- `ai_tui_e2e_test.go`: not required for phase 1; if a session ordering bug is ever reported, its keystroke replay lands there per that file's header rules.

---

## 10. Risks and open questions for the maintainer

**Q1 — Default: `terminal` now, or soft launch?** Recommended: default `terminal` (D5). It only affects human TTY rendering, the opt-out is one persisted command, and shipping the feature off-by-default historically means it never gets dogfooded. Alternative if the muscle-memory risk feels high: ship phase 1 defaulting to `classic`, announce, flip the default one release later. Either way the flip release is a UX milestone — a candidate **minor** bump (v2.8.0) under the AGENTS.md versioning policy rather than a routine patch.

**Q2 — Help-center docs.** The curated-view and insights behaviors are mirrored in the CLI docs generated from omni (`generate-cli-docs`, command-notes — see the sync warnings at list_views.go:21–23 and response_transform.go:132–134). Those pages show classic ordering in their examples. The docs update should land with (or before) the default flip, and the generated pages should state the ordering is terminal-adaptive and configurable. Who owns that omni PR?

**Q3 — Muscle memory / support surface.** "The top row" changes meaning for `list-reports`, `list-insights`, and report pivots at a TTY. Screenshots in runbooks, internal Slack lore ("the first row is your biggest spender") and copy-paste habits will lag. Mitigations: the `dci status` attribution line, a `CHANGELOG` entry with a before/after snippet, and the one-word opt-out. Is that enough, or do we want a one-time stderr notice on first flipped render (the `maybeHintAgentMode` pattern, main.go:1327)?

**Q4 — Does `list-budgets` deserve a `sortField` first?** It deliberately has none (server may reorder by breach risk, list_views.go:19), so it does not flip. If users read a flipped `list-reports` next to an unflipped `list-budgets` as inconsistency, the fix is a server-order conversation, not a client-side reversal — flagged so it does not get "fixed" wrongly later.

**Q5 — Spelling bikeshed.** `terminal|classic` is proposed for self-description (`--output-order terminal` reads as "ordered for the terminal"). Alternatives considered: `asc|desc` (wrong — §4 is not a single sort direction), `bottom|top` (ambiguous about what is at the bottom), `prompt|web` (cute, opaque). Happy to rename before it ships; renaming after is a breaking config change.
