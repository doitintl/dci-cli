# Design spec: terminal UI layer for `dci` human mode

Status: **draft for maintainer review**.
Audited at commit `8bc2ce3`; every claim cites the function and file it is based on, line numbers approximate at that commit.

Scope: the eight features agreed in conversation (fuzzy resource picker, interactive ambiguity resolution, styled destructive confirmation, interactive query builder, scrollable table viewer, inline charts, login polish, markdown rendering). Everything here is **human-interactive-mode only**: agent mode, non-TTY, and CI behavior is byte-for-byte unchanged, enforced by a single shared gate (§3.1).

---

## 1. Summary

| # | Feature | One line | Phase |
|---|---------|----------|-------|
| F1 | Fuzzy resource picker | `dci get-report` with no argument opens a filter-as-you-type picker over the name cache instead of erroring | P1 |
| F2 | Ambiguity selection upgrade | The existing numeric `promptNameSelection` becomes an arrow-key select | P1 |
| F3 | Destructive confirmation prompt | Interactive humans get a styled y/N prompt instead of the `--yes` usage error | P1 |
| F4 | Interactive query builder | `dci query` on a TTY with no stdin walks through building a report config, prints the JSON, then runs it | P3 |
| F5 | Interactive table viewer | `--table-mode interactive`: full-screen scrollable/sortable results table | P3 |
| F6 | Inline charts | Utilization bars in `list-budgets` rows; `--chart` sparkline/line graph for time-series query results | P2 |
| F7 | Login polish | Spinner while waiting for the browser OAuth round-trip, styled success line | P2 |
| F8 | Markdown rendering | Glamour-rendered upgrade notice and `--help` epilogs | P2 (partial), see §11.3 |

Dependency decisions (§2): adopt the Charm stack — `huh` for prompts, `bubbletea`+`bubbles`+`lipgloss` for full-screen views. **No** `tcell`/`tview`, **no** `go-fuzzyfinder` (revised from the earlier conversation: `bubbles/list` has built-in fuzzy filtering, avoiding a second terminal backend). `asciigraph` for line charts; bar cells are hand-rolled like the existing heatmap. `glamour` is already in the tree at v0.6.0 via restish.

---

## 2. Framework decisions

### 2.1 What we adopt

- **`charmbracelet/huh`** — one-shot prompts (select, multi-select, confirm, input) without an event loop. Used by F2, F3, F4. Replaces the pattern restish's indirect `survey/v2` dependency (go.mod, indirect) represents; survey is archived upstream and we should not start using it.
- **`charmbracelet/bubbletea` + `bubbles` + `lipgloss`** — full-screen interactive views. Used by F1 (filterable list), F5 (table viewer), F7 (spinner). `huh` itself is built on bubbletea, so this is one runtime, not two.
- **`asciigraph`** — zero-dependency line graphs for F6's `--chart`.

Pin the newest stable major of each at implementation time; verify `go build` for windows/amd64 and the goreleaser cross matrix before committing (Windows ARM64 is already excluded, AGENTS.md).

### 2.2 What we explicitly do not adopt

- **`tview`/`tcell`** — a second terminal backend and a clashing widget idiom; everything it offers that we need, bubbles covers.
- **`ktr0731/go-fuzzyfinder`** — recommended in the earlier conversation, **revised here**: it drags in tcell. `bubbles/list` ships fuzzy filtering out of the box and keeps us on one stack. The picker UX is equivalent.
- **`pterm`, `go-pretty`, `ntcharts`** — overlap with the existing simpletable renderer (main.go:3235) and heatmap shading (main.go:795); revisit ntcharts only if a full dashboard view is ever wanted.

### 2.3 Dependency risk

- **Binary size**: bubbletea+bubbles+lipgloss+huh adds roughly 2–4 MB to a binary that already ships glamour/chroma/survey transitively. Measure in CI before/after P1; abort criterion is maintainer's (§12, Q5).
- **glamour version skew**: restish v0.21.2 pins glamour v0.6.0. Using glamour *directly* at a newer version is legal under MVS but bumps restish's resolved version too — restish must be re-verified against it. F8 therefore starts with what v0.6.0 offers and treats any bump as its own change (§11.3).
- **Go 1.25.13** (go.mod): no constraint issues; all listed libraries support it.

---

## 3. Shared infrastructure (new sibling file `tui.go`)

Per the AGENTS.md chapter-split guidance, all TUI plumbing lands in new sibling files `tui.go` / `tui_test.go` in `package main`, not in main.go. (Note in passing: AGENTS.md still says "The entire CLI is a single main.go file" — stale since the chapter split; worth a docs fixup independent of this spec.)

### 3.1 The gate

One function, one definition of "a human is present":

```go
// tuiActive reports whether interactive TUI enhancements may render.
func tuiActive() bool {
    if on, valid := parseBoolish(os.Getenv("DCI_NO_TUI")); valid && on {
        return false
    }
    return !agentMode && stdoutIsTTY() &&
        term.IsTerminal(int(os.Stdin.Fd())) &&
        os.Getenv("TERM") != "dumb"
}
```

This is the existing `nameSelectionInteractive` predicate (name_resolution.go:529) plus a kill switch and a dumb-terminal guard. `nameSelectionInteractive` is redefined in terms of it. The env name `DCI_NO_TUI` follows the existing negative-toggle convention (`DCI_NO_RESOLVE`, `DCI_NO_UPDATE_CHECK`, name_resolution.go:152, update.go:160).

Because `agentMode` already folds in `DCI_AGENT_MODE`, `--agent/--no-agent`, the agent-env list, and non-TTY stdout (resolveAgentMode, main.go:752), every existing mode override disables the TUI for free, and `dci status` already explains why (main.go:1335).

### 3.2 Output discipline

The CLI's contract is stdout = data, stderr = chatter (README "Agent Mode"; the hint routing at main.go:1173 is the precedent). All prompts and spinners (F1–F4, F7) render to **stderr** — `huh` and `bubbletea` both accept a `WithOutput(os.Stderr)` option — so `dci get-report | jq .` piped output is never contaminated even in edge cases where the gate misfires. The one exception is F5's full-screen viewer, which by definition replaces stdout rendering and only ever runs when stdout is the TTY.

`NO_COLOR` is respected automatically by lipgloss; the existing `heatmapEnabled(…, noColor)` logic (main.go:795) stays the authority for table shading.

### 3.3 Testability

Follow the existing seam pattern: `nameSelectionPrompt` and `nameSelectionInteractive` are already package-level function vars swapped in tests (name_resolution.go:529–534). Every TUI entry point gets the same treatment (`var pickResource = runResourcePicker`, etc.), so all existing behavior tests keep running headless with fakes. Bubbletea models additionally get unit tests driving `Update` with synthetic key messages (charm's `teatest` if it earns its keep, plain model-poking otherwise).

---

## 4. F1 — Fuzzy resource picker

### Current behavior

`resolvePathArguments` returns nil immediately when `len(args) == 0` (name_resolution.go:147–150), and cobra's generated `ExactArgs(1)` — already wrapped once by `relaxResolvableArgsValidation` for multi-word names (name_resolution.go:274) — rejects the invocation with a usage error. So `dci get-report`⏎ is a dead end, and the discovery loop is: `list-reports`, read, copy, re-run.

The ingredients for something better already exist: a per-context on-disk name cache with 10-minute-fresh/24-hour-servable TTLs (name_completion.go:28–33, `nameCacheEntry{ID, Name}` at :35), a reader (`cachedResolverEntries`, name_completion.go:122), a detached background refresher (`spawnDetachedNameRefresh`, name_completion.go:136), and a synchronous pager (`fetchResourceNames`, name_resolution.go:565).

### Design

When `tuiActive()` and a command in `resolutionIndex` (name_resolution.go:50) is invoked with **zero** positional arguments:

1. Serve the picker via `readNameCache` (name_completion.go:60) so stale-but-servable entries (within the 24-hour TTL, name_completion.go:31) display immediately, and kick `spawnDetachedNameRefresh` when the cache is not fresh. (`cachedResolverEntries` is unsuitable here: it returns entries only when the cache is fresh and hides the cache state, name_completion.go:122–128.)
2. Cache absent → synchronous `fetchResourceNames` behind a spinner ("fetching report names…", stderr), honoring the existing `resolverMaxPages` cap and its truncation wording (name_resolution.go:488–492).
3. Render a `bubbles/list` with fuzzy filtering enabled, on stderr: name as title, ID as dim description line. Enter selects, Esc/Ctrl-C cancels.
4. Selection flows into the **same** downstream path as a typed argument: recorded in `resolvedTargets` (name_resolution.go:67) so the destructive gate shows the true target, announced via `announceResolution` (name_resolution.go:324).
5. Cancel exits via `nameSelectionCancelledError` (name_resolution.go:550): `USAGE_ERROR`, exit 2 — unchanged vocabulary.

```
$ dci get-report
  Select a report (42) — type to filter
  > Monthly AWS Spend            2vP…kQ
    Monthly GCP Spend            8xL…mN
    GenAI token spend per model  Rt3…9a
  esc cancel · enter select
```

Applies automatically to every resource the resolution index derives from the spec (reports, budgets, allocations, …). `open` is **not** covered by that mechanism: it is a custom command outside `resolutionIndex`, and its zero-arg form deliberately opens the console home (open_command.go:35–74). It gets its own trigger instead: `dci open <resource>` with exactly one argument — today a usage error (`consoleURLForArgs`, open_command.go:77–82) — opens the picker for that resource type when `tuiActive()`, and keeps the usage error otherwise. Excluded everywhere: operations with `hasBody` (surplus args are body shorthand, name_resolution.go:29–34) — for those, zero args keeps today's usage error.

### Mechanics that need care

- **Arg injection.** `resolvePathArguments` mutates `args[0]` in place and relies on cobra passing the same backing slice to Run (name_resolution.go:144–147). An empty slice can't be grown in place. The resolution call happens inside the per-command RunE wrapper installed in main.go (call at main.go:1800), which controls what it passes downstream — the wrapper changes from mutate-in-place to pass-the-rewritten-slice. This is the only structural change to existing code and needs a test proving restish's URI substitution sees the injected ID.
- **Args validation.** Extend the `relaxResolvableArgsValidation` wrapper (name_resolution.go:274): zero args is valid *only* when `tuiActive()`; otherwise the original validator runs and today's usage error (with its `Run dci list-…` hint) is preserved verbatim.

### Fallback matrix

| Context | `dci get-report`⏎ behaves |
|---|---|
| Interactive human TTY | picker |
| `DCI_NO_TUI=1`, `TERM=dumb` | today's usage error |
| Agent mode (any trigger), piped stdout, CI | today's usage error |
| Cache absent + network down | spinner → `NETWORK_ERROR` exit 41 (`nameResolutionNetworkError`, name_resolution.go:504) |

### Tests

Wrapper injection reaches URI substitution; zero-arg gating per matrix row; cancel exit code; cache-stale refresh kick; `hasBody` exclusion; picker fed from cache fixture without network.

---

## 5. F2 — Ambiguity selection upgrade

### Current behavior

Already interactive: when a fuzzy match is ambiguous and `nameSelectionInteractive()` holds, `promptNameSelection` prints a numbered list on stderr and `Fscanln`s a digit (name_resolution.go:536–548). Non-interactive contexts get `NAME_AMBIGUOUS`, exit 2 (name_resolution.go:471).

### Design

Swap the body of `promptNameSelection` for a `huh.Select[nameCacheEntry]` on stderr — same candidates (already capped at 10 by `capNameCandidates`, name_resolution.go:447), name + ID per option, Esc → the existing `nameSelectionCancelledError`. The function signature, the `nameSelectionPrompt` seam, the gate, and both error paths are untouched, so this is a ~30-line change and all existing tests that fake the prompt keep passing.

If `huh` fails to initialize (exotic terminal), fall back to the current numbered `Fscanln` prompt rather than erroring — keep the old body as `promptNameSelectionBasic`.

---

## 6. F3 — Destructive confirmation prompt

### Current behavior

There is **no prompt today, for anyone**: without `--yes` or `DCI_CONFIRM_DESTRUCTIVE=1`, `enforceDestructiveConfirmation` returns `destructiveConfirmationError` — exit 30, message naming the resolved target (destructive_contract.go:204–241, :64–72). Humans and agents alike must re-run. The resolved name/ID display promised in the README is that error message.

### Design

In `enforceDestructiveConfirmation`, after the existing checks conclude "not confirmed" (destructive_contract.go:236): if `tuiActive()`, render a styled confirm instead of returning the error.

```
┌─ Destructive action ─────────────────────────────┐
│  delete-report                                   │
│  Report: Monthly AWS Spend (2vP…kQ)              │
│  This cannot be undone.                          │
│    ▸ Cancel      Delete                          │
└──────────────────────────────────────────────────┘
```

- Content comes from `resolvedTargets` via `commandResolvedTarget` (name_resolution.go:317), exactly what the error shows today; no resolution → command name only, same as the error's fallback branch.
- **Default is Cancel**; Esc, Ctrl-C, and EOF all cancel. Cancel returns the existing `destructiveConfirmationError` so exit code (30), `DESTRUCTIVE_REQUIRES_CONFIRMATION` payload, and hint text stay stable for anything watching.
- Confirm proceeds exactly as `--yes` does (sets nothing new; the function just returns nil after the same `destructiveActionName` bookkeeping at destructive_contract.go:230).
- `--yes`, `DCI_CONFIRM_DESTRUCTIVE=1`, `--dry-run`, and every agent/non-TTY path are bit-identical to today.

### The policy question (decision needed, §12 Q1)

This **loosens** the interactive contract: today a human cannot destroy anything without typing `--yes`; after F3 a single Enter… no — default-Cancel means Enter cancels; destroying requires an explicit arrow/tab + Enter or `y`. That is still one deliberate step weaker than `--yes`. Options:

- **(a) Confirm prompt, default Cancel** — as specced. Matches every mainstream CLI (gh, kubectl, terraform without `-auto-approve` equivalents).
- **(b) Type-the-name-to-confirm** (GitHub repo-deletion style) for extra friction on `delete-*`, plain y/N for lesser destructive ops — the destructive set is spec-derived (`setDestructiveOperations`, destructive_contract.go:156), so tiering is possible but needs a tier source.
- **(c) Keep `--yes` mandatory**, use the TUI only to render the refusal more legibly.

Spec recommends **(a)**; the fuzzy-match safety story ("a fuzzy match can never delete the wrong thing silently", README) is preserved because the prompt displays the resolved name *and* ID.

### Tests

Confirm→nil, cancel→error-with-exit-30 (all three cancel gestures), `--yes` bypasses prompt, agent mode never prompts, resolved-target rendering with and without a resolution.

---

## 7. F4 — Interactive query builder

### Current behavior

`dci query` is the spec-generated operation taking the report config as the request body, normally via stdin (`dci query < query.json`, main.go:980). On a TTY with no redirect, restish waits on empty stdin — effectively a hang until Ctrl-D — and nothing teaches the schema. Body validation already knows the valid top-level fields from the cached OpenAPI spec (`bufferStdinTopLevelFields` / valid-field checks, body_validation.go:63–158).

### Design

Trigger: command is `query`, `tuiActive()`, **and stdin is a TTY** (nothing piped). Then instead of waiting on stdin, run a `huh` multi-group form:

1. **Time range** — presets (last 7/30 days, MTD, custom ISO pair).
2. **Dimensions / group-bys** — fuzzy multi-select fed by a programmatic `list-dimensions` call (same bearer-token client pattern as `fetchResourceNames`, name_resolution.go:562–569). The collection holds ~1,000 dimensions across ~20 pages (skills/dci-cli/references/query-patterns.md, "Grouping by Labels & Dimension Discovery") and `--max-results` is capped at 500 for it (pagination.go:50) — the fetch pages to completion and caches alongside the name cache, honoring the same TTLs.
3. **Metric** — select (cost/usage/savings + custom).
4. **Filters** — optional repeated filter rows whose fields, operators, and value shapes derive from the query request schema's filter object. (Not from `list-dimensions --filter`: that is a catalog-listing filter with unrelated exact `type`/`label`/`key` semantics — a different contract entirely.)
5. **Review** — render the composed JSON (chroma-highlighted; already in-tree), then a three-way choice: **Run**, **Save as query.json and run**, **Print and exit**.

The form's field names and enums are **derived from the cached OpenAPI request schema** — the same source body_validation reads — not hand-maintained, so API additions appear without CLI releases. Hand-shaping is limited to grouping, ordering, and presets.

"Print and exit" is the point, not a bonus: the builder is a teaching tool for the scriptable interface. The composed JSON goes to **stdout** in that branch (it *is* the data), the form itself to stderr.

Execution then re-enters the normal path by handing the composed JSON to the existing body plumbing (`cli.Stdin` is already re-pointable — body_validation.go:158 does exactly this substitution), so validation, `--dry-run`, output shaping, and `--max-rows` behave as if the user had piped the file.

### Deliberately out of scope (v1)

Nested filter groups, saved-query management (`~/.config/dci/queries/`), editing an existing query.json, and any attempt to mirror the full report-config schema. Escape hatch on every screen: "drop to editor" opens `$EDITOR` on the current JSON.

### Fallbacks

Piped stdin, agent mode, `DCI_NO_TUI` → exactly today's behavior. A bare `dci query` in a *non*-TTY-stdin context still waits on stdin as it does now (scripts feeding via heredoc keep working).

### Tests

Trigger predicate (TTY-stdin × mode matrix); schema-derived field extraction against a spec fixture; composed JSON validates through `validateRequestBody`; stdin-substitution round-trip; print-and-exit stdout purity.

---

## 8. F5 — Interactive table viewer

### Current behavior

`--table-mode` accepts `fit` (truncate) and `wrap` (main.go:1629, dispatch at main.go:2659), rendering via simpletable (main.go:3235). Wide responses — anomalies at 16 columns — squeeze badly enough that a comment calls it out (main.go:2622) and default column views exist to hide the overflow (`applyListView`, list_views.go:267; `--table-columns` main.go:1629-area flags).

### Design

New value: `--table-mode interactive` (alias `-M i`). Gated on `tuiActive()`; if the gate fails, **fall back to `fit` with a one-line stderr note** rather than erroring, so a saved shell alias never breaks a pipe.

A bubbletea alt-screen program wrapping `bubbles/table`, fed the same `[]map[string]any` rows the static renderer receives (share the row-extraction in `toTableRows`, main.go:2345-area — the viewer is a second renderer, not a second pipeline):

- **←/→ h-scroll** columns (the 16-column anomaly fix), **↑/↓/PgUp/PgDn** rows.
- **`s`** cycles sort on the focused column (string/numeric/epoch-aware, reusing the classifier that powers right-alignment at main.go:3277 and `sortRowsByEpochDesc`, list_views.go:356).
- **`/`** filter rows (client-side substring).
- **`c`** copy focused row's `id` to clipboard *if* rows carry IDs (`rowsCarryIDs`, list_views.go:454); **`enter`** prints the focused row's id/name to stdout on exit (stdout stays clean until exit; the selection is the only stdout output). Note this is **not** pipeable in v1: piping stdout makes it non-TTY, which both fails `tuiActive()` and trips agent-mode detection (resolveAgentMode, main.go:776), so `… -M i | xargs …` falls back to `fit`. Pipe composability requires an fzf-style renderer that draws on `/dev/tty` with its own gate — deferred, folded into open question Q3.
- **`q`/Esc** quit; exit prints nothing.

Timestamps and column views apply before row extraction, so `DCI_TZ`, `--utc`, and `--table-columns` keep working identically. Heatmap shading does **not**: it is applied inside the static simpletable renderer (`newHeatmap`/`heat.colorize`, main.go:3251–3267), so the viewer explicitly reuses those helpers on its own cells to preserve `--heatmap`.

### Deliberately out of scope (v1)

Enter-to-drill-down (spawning `get-*` from inside the viewer) — tempting, but it turns a renderer into a shell; revisit after v1 feedback. Live re-fetch/pagination inside the viewer (`--all` exists for that, pagination.go:107).

### Tests

Fallback note on non-TTY; sort correctness per column class; selection-to-stdout contract; row parity with the static renderer on shared fixtures.

---

## 9. F6 — Inline charts

Two small, independent pieces; no bubbletea, no alt-screen.

### 9.1 Budget utilization bars

`list-budgets`' table view gains a `utilization` column: `▓▓▓▓▓▓░░░░ 63%`, red past 90%/over-budget. Rendered as a plain string cell inside the existing view machinery (`setListViewConfig`/cell helpers like `moneyCell`, list_views.go:322, :520) — the heatmap already establishes magnitude-shading precedent and its gate (`heatmapEnabled`, main.go:795: interactive, non-agent, `NO_COLOR`-aware; monochrome blocks still render under `NO_COLOR`, only the red is dropped). Machine formats must never see the column — and that is *not* automatic: `presentationView()` applies curated views to `toon` as well as table/auto (response_transform.go:213–224), and toon is the agent-mode default. The utilization column is therefore registered only when the resolved output format is table/auto **and** `tuiActive()`-adjacent shading rules allow it, keeping TOON/agent output byte-identical.

Requires amount + actuals in the list response; if the listing doesn't carry actuals, the column must be left out of the column list *before* registration — `applyListView` registers every listed column with `setListViewConfig`, so an empty-string derive would render a permanently blank column, not omit it (list_views.go:295–315). Availability is detected from the first row before the view's column list is built. **Verify the response shape against the live API before building** (open item, §12 Q4).

### 9.2 `--chart` for time-series results

New flag on report-shaped output (`query`, `get-report` results): when the result set has a time dimension and ≥2 buckets, render an asciigraph line chart of the primary metric **after** the table, to stderr in human mode. One series v1; grouped results plot the total with a note. When the shape doesn't qualify, the flag is a no-op with a one-line stderr note in human mode. Agent mode: the flag is accepted and ignored with no output at all — charts are decoration by the CLI's own definition (README: agent mode strips decoration).

The report-container row/schema shapes needed are already classified by the response transform and pivot code (response_transform.go, pivot.go) — the chart consumes the same normalized rows as the pivot.

---

## 10. F7 — Login polish

### Current behavior

`login` runs the OAuth flow by internally invoking `validate` with stdout/stderr discarded (main.go:1486–1493) — the user stares at a silent terminal while the browser round-trip happens, then gets `Authenticated successfully.` on stderr (main.go:1509). First-run auto-configuration prints its own separate hint (main.go:279).

### Design

Wrap the internal `cli.Run()` call with a `bubbles/spinner` on stderr when `tuiActive()`:

```
⠋ Waiting for browser sign-in… (Ctrl-C to cancel)
```

On success, replace the spinner line with a lipgloss-styled confirmation that also surfaces what today is invisible: the authenticated identity source and, for doers, the applied customer context (`applyDoerContext` already returns whether it acted, main.go:1501):

```
✓ Signed in via DoiT Console.
  Customer context: doit.com (doer auto-configured — change with: dci customer-context set)
```

On failure the spinner clears and the existing error path runs unchanged. Non-TTY: today's behavior exactly. Scope: the `login` RunE body **plus** a presentation split in `applyDoerContext` — it prints its own two stderr lines today (main.go:1440–1441), which would duplicate the styled confirmation; the helper keeps the context-write and returns what happened, and the caller owns all output. `logout` gets the matching one-line ✓ for symmetry.

---

## 11. F8 — Markdown rendering

### 11.1 Upgrade notice

`updateNotice` composes a plain string (update.go:247) printed to stderr when a newer release exists (`maybeNotifyUpdate`, update.go:46, already TTY-gated via `stderrIsTTY`, update.go:63). Style it with lipgloss (boxed, version arrow bold) — glamour is overkill for two lines. Same for `registerUpgradeCommand`'s output (update.go:80).

### 11.2 Help epilogs

Root help already gets custom assembly (help override around main.go:1160). Render the "getting started" epilog and command-group long help through glamour **v0.6.0 as pinned** (headings, code spans) when `tuiActive()`; plain text otherwise. No version bump required for this subset.

### 11.3 `dci whats-new` (optional, cut first)

A command rendering the latest GitHub release notes via glamour, reusing `fetchLatestVersion`'s release-API client (update.go:112). Needs the release *body*, not just the tag — one field addition to that call. This is the only F8 piece that would benefit from a newer glamour; if v0.6.0 renders GitHub-flavored notes acceptably, ship on it; otherwise defer the command rather than bump the pin (§2.3).

---

## 12. Phasing, and open questions for the maintainer

### Phasing

- **P1 — prompts on existing seams** (F2, F3, F1): highest leverage, smallest surface. F2 is a function-body swap; F3 is one branch in `enforceDestructiveConfirmation`; F1 is the largest (arg-injection change) but rides entirely on the name cache. Introduces the full dep set, so the size measurement (§2.3) happens here.
- **P2 — polish** (F7, F6, F8.1–8.2): independent, small, no new deps beyond asciigraph.
- **P3 — programs** (F5, then F4): full bubbletea apps; F4 last because it has the most product-shape risk.

Each phase is a separately releasable unit; per the versioning policy these are patch bumps until F4/F5 land, which together plausibly justify a minor (AGENTS.md: "major UX overhauls").

### Open questions

1. **F3 policy**: confirm-prompt default-Cancel (recommended), type-the-name tiering, or keep `--yes` mandatory? (§6)
2. **F1 scope**: should the picker also trigger on `NAME_NOT_FOUND` (typo → "did you mean" picker seeded with fuzzy matches) or only on zero args? Spec says zero args only for v1; the not-found path already has good hints (name_resolution.go:335).
3. **F5 selection contract**: is Enter-prints-id-to-stdout the right primitive even though it isn't pipeable in v1 (piped stdout fails the gate, §8) — and is an fzf-style `/dev/tty` renderer with its own terminal rule worth speccing to make `… -M i | xargs …` real, or should selection do nothing in v1?
4. **F6.1 data check**: does the `list-budgets` response carry actuals/utilization today? Probe: `dci list-budgets --output json` against a live customer context, checking each item for a budget amount plus a current-spend/actuals field; if absent, this becomes an API ask, not a CLI feature.
5. **Dependency budget**: is there a binary-size ceiling that would veto the Charm stack (current binary ~15–20 MB estimated; +2–4 MB expected)? Measured, not guessed, at P1.
6. **`DCI_NO_TUI`** naming okay, or fold into `DCI_AGENT_MODE=1` as the only opt-out (rejected here because a human may want plain prompts without agent output shaping)?
7. **F4 placement**: build in-CLI as specced, or prototype as an agent-skill recipe first and let usage decide? In-CLI is specced because the target user is precisely the human without an agent.
