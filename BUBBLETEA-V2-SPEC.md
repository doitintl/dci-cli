# Design spec: migrating the TUI layer to Bubble Tea v2

Status: **implemented** — the migration landed in the same PR as this spec; §13 records where the implementation deviates from the plan below.
Audited at commit `f1eaf2d`; upstream versions checked against the Go module proxy and charm.land docs on 2026-08-25.

Scope: moving the Charm stack from v1 (`github.com/charmbracelet/*`) to v2 (`charm.land/*/v2`) across every TUI chapter — `tui.go`, `tui_picker.go`, `tui_querybuilder.go`, `tui_viewer.go`, `ai_tui.go`, `charts.go`, `self_update.go` — plus their tests. Everything outside the TUI layer (restish, table rendering, agent mode, output contracts) is untouched by construction.

---

## 1. Summary and recommendation

**Recommendation: migrate, in one atomic PR, released as a routine patch version.**

Bubble Tea v2 went stable in February 2026 and the entire ecosystem this CLI sits on has followed: bubbles, lipgloss, huh, and ntcharts all have stable v2 releases compatible with each other. The v1 line is frozen — bubbles v1.0.0 was explicitly published as an "honorary" final release before v2, and huh's v1 line stopped at v1.0.0 — so staying on v1 means no upstream fixes for the exact components `dci ai` and the interactive viewer are built on.

Unlike the restish v2 question (issue #20, AGENTS.md), **this is not a rewrite**. Restish v2 deletes the in-process library model this CLI is built on; Bubble Tea v2 keeps the model — the same Elm loop, the same `Model`/`Update`/`View` architecture, the same in-process library — and changes API surface: import paths, the key/mouse message types, `View()`'s return type, and how terminal features (alt screen, mouse) are declared. Every change lands inside our own ~2,500 lines of TUI code and ~2,300 lines of TUI tests, all in `package main` — the two Bubble Tea programs and the huh forms behind the `tuiActive()` gate, plus `charts.go`, whose rendering runs outside any program and is gated by its own table-output/color checks rather than `tuiActive()` (§7.6), so its downsampling change gets its own validation. The port is mechanical for most call sites, with a handful of genuinely changed idioms called out in §6.

Estimated effort: 2–4 focused days including cross-terminal dogfooding. Risk is contained (§9); the biggest single unknown is ntcharts v2's pending upstream patches (§9.4).

| Question | Answer |
|---|---|
| Is v2 stable? | Yes — bubbletea v2.0.9, bubbles v2.2.1, lipgloss v2.0.6, huh v2.0.3, ntcharts v2.2.0, all stable tags |
| Is this restish-v2-shaped? | No — same in-process model, API migration only |
| Can it be done incrementally? | Not usefully — one `package main` shares `tea.Cmd`/`lipgloss.Style` values across files (§10.1) |
| Does restish constrain us? | No — restish depends on glamour/termenv, not on bubbletea/bubbles/huh (§5) |
| User-facing changes? | None intended; rendering fidelity should improve (§4) |

---

## 2. What is on v1 today

The current direct Charm-stack dependencies (go.mod):

| Module | Pinned | Used by |
|---|---|---|
| `github.com/charmbracelet/bubbletea` v1.3.10 | `tea` programs | `tui_viewer.go`, `ai_tui.go` |
| `github.com/charmbracelet/bubbles` v1.0.0 | table, textarea, viewport, spinner | `tui_viewer.go`, `ai_tui.go` |
| `github.com/charmbracelet/huh` v1.0.0 | one-shot forms | `tui.go`, `tui_picker.go`, `tui_querybuilder.go`, `self_update.go` |
| `github.com/charmbracelet/lipgloss` v1.1.1-pre | styles everywhere | all TUI files, `charts.go` |
| `github.com/NimbleMarkets/ntcharts` v0.5.1 | stacked bar chart | `charts.go` |
| `github.com/muesli/termenv` v0.16.0 | one call: `chartColorCapable` | `charts.go` |

Two full Bubble Tea programs exist: the interactive table viewer (`tableViewerModel`, alt screen on stderr) and the `dci ai` session (`aiModel`, alt screen, optional mouse capture). Everything else is huh forms, lipgloss string styling, and a hand-rolled ANSI spinner (`startTUISpinner`) that uses no framework at all.

`glamour` (markdown in `dci ai`) is imported directly but resolved at v1.0.0 via restish's requirement — see §5 for why it stays.

---

## 3. Upstream state (checked 2026-08-25)

- **bubbletea v2.0.0** released 2026-02-24; current **v2.0.9** (2026-08-19). Nine patch releases in six months — actively maintained, and the churn is settling.
- **bubbles v2.2.1** (2026-08-24), **lipgloss v2.0.6** (2026-08-11), **huh v2.0.3** (2026-03-10) — all built on bubbletea v2.
- **ntcharts v2.2.0** (2026-05-28) — the maintainers keep the bubbletea-v1 library on `main` and develop v2 on the `v2` branch, backporting fixes to both.
- Charm moved to **vanity import paths**: `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`, `charm.land/huh/v2`. The `github.com/charmbracelet/*/v2` paths resolve to the same code, but `charm.land` is the documented canonical form — use it.
- Go requirement: bubbletea v2 needs go ≥ 1.25.0, huh v2 needs go ≥ 1.25.8. Our go.mod declares 1.25.13 — no constraint.
- v1 status: bubbles v1.0.0 and huh v1.0.0 were cut as final v1 releases immediately before the v2 launch. New component work (and the fixes that come with it) lands on v2 only.

---

## 4. Why migrate — tied to problems this codebase already carries

Each of these maps to a workaround or a comment in the current code:

1. **The OSC-11 swallowing bug is fixed at the root.** `aiMarkdownStyle` (ai_tui.go:1531) resolves the glamour style *before* the program starts because "Bubble Tea's input reader swallows the terminal's reply" to a background query, garbling the textarea. v2 makes background-color detection a first-class async protocol (`tea.RequestBackgroundColor` → `tea.BackgroundColorMsg`) handled by the input loop itself. We keep the pre-program detection (§7.4) but the entire failure class the comment warns about is gone upstream.
2. **Alt-screen smearing.** `aiFitFrame`'s long comment (ai_tui.go:1770) documents the v1 renderer skipping lines whose content it believes unchanged, leaving frames the terminal disturbed "smeared until every cell is repainted" — hence the ctrl+l guidance in `/help`. v2's rewritten renderer owns the full cell grid and enables synchronized output, which directly targets this. `aiFitFrame` stays (its width-clamping half is still ours), but the repair-gesture UX debt should shrink.
3. **Grapheme-correct width.** v2 auto-enables Unicode mode 2027 handling; emoji or CJK in customer names, report titles, and status snippets stop mis-measuring. Our `aiTrimTo`/`aiStatusRow` cell arithmetic gets more honest inputs from `lipgloss.Width`.
4. **A real terminal cursor is available.** The `dci ai` textarea currently simulates a cursor (v1 has no alternative). v2 can drive the actual terminal cursor — better for IMEs and screen readers. Deliberately deferred to a follow-up (§10.2), but only reachable from v2.
5. **The v1 line is frozen.** Every future bubbles/huh fix — table, textarea, viewport, filtering — lands on v2. The longer we wait, the more the versions we pin fall behind, and the CLI releases often.
6. **Windows input handling was rewritten** in v2. We ship via WinGet and Scoop with zero Windows TUI coverage in CI; inheriting upstream's Windows fixes matters more for us than for projects that can test it.

What we do **not** gain: no feature in the current TUI-SPEC/AI-SPEC surface requires v2. This is a maintenance-position migration, not a feature unlock — which is exactly why it should happen while the diff is mechanical, not later under pressure.

---

## 5. Target dependency set

| Change | From | To |
|---|---|---|
| swap | `github.com/charmbracelet/bubbletea` v1.3.10 | `charm.land/bubbletea/v2` v2.0.9 |
| swap | `github.com/charmbracelet/bubbles` v1.0.0 | `charm.land/bubbles/v2` v2.2.1 |
| swap | `github.com/charmbracelet/lipgloss` v1.1.1-pre | `charm.land/lipgloss/v2` v2.0.6 |
| swap | `github.com/charmbracelet/huh` v1.0.0 | `charm.land/huh/v2` v2.0.3 |
| swap | `github.com/NimbleMarkets/ntcharts` v0.5.1 | `github.com/NimbleMarkets/ntcharts/v2` v2.2.0 |
| promote | `github.com/charmbracelet/colorprofile` (indirect) | direct (replaces the `termenv` comparison, §7.6) |
| drop direct | `github.com/muesli/termenv` v0.16.0 | stays indirect via restish/glamour |

**Restish does not constrain this migration.** `go mod graph` confirms restish v0.21.2 pulls glamour and termenv — not bubbletea, bubbles, or huh. Since Go major versions are distinct module paths, our v2 modules coexist with whatever v1 code restish's tree wants; nothing restish compiles against changes.

**Consequences of coexistence:**

- bubbletea v1, bubbles v1, and huh v1 leave the module graph entirely (we were their only path in).
- lipgloss **v1 stays in the binary** via restish → glamour v1. Two lipgloss majors ship side by side. That is correct-by-construction (separate packages), costs binary size only, and ends whenever restish's stack moves — not ours to force.
- **glamour stays at v1 on purpose.** A glamour v2.0.1 exists, but restish keeps glamour v1 in the tree regardless; moving our one call site (`renderAIMarkdown`) to v2 would *add* a second glamour instead of replacing the first. Glamour renders markdown to a string and touches no Bubble Tea types, so it is severable from this migration. Revisit alongside any restish bump.
- Measure the release binary before/after in the PR (the goreleaser matrix already builds every platform). Expect low-single-digit MB growth from the dual lipgloss and the new renderer; flag it in the PR body.

---

## 6. API migration map

The changes that actually touch our code, per symbol. Items marked ⚠ are behavior changes, not just renames.

### 6.1 bubbletea

| v1 (call sites) | v2 |
|---|---|
| `import tea "github.com/charmbracelet/bubbletea"` | `import tea "charm.land/bubbletea/v2"` |
| `Init() tea.Cmd` | unchanged |
| `Update(tea.Msg) (tea.Model, tea.Cmd)` | unchanged |
| `View() string` | ⚠ `View() tea.View` — build the frame string as today, return `tea.NewView(s)` with feature fields set |
| `tea.NewProgram(m, tea.WithAltScreen())` | ⚠ option gone — set `view.AltScreen = true` in `View()` (declarative, per-frame) |
| `tea.WithMouseCellMotion()` option, `tea.EnableMouseCellMotion` / `tea.DisableMouse` commands | ⚠ all gone — set `view.MouseMode = tea.MouseModeCellMotion` in `View()` when `m.mouseOn` |
| `tea.WithOutput(os.Stderr)`, `tea.WithInput(os.Stdin)` | kept (signatures now take file handles — verify at impl) |
| `case tea.KeyMsg:` / `func(msg tea.KeyMsg)` | ⚠ `tea.KeyPressMsg` (v2 `KeyMsg` is an interface over press/release; we only ever want presses) |
| `msg.Type == tea.KeyEnter` (and Esc, Up, Down, Tab, Backspace, PgUp/PgDn, CtrlC, CtrlL, Space) | `msg.Code == tea.KeyEnter` etc., or keep the existing `msg.String()` switches |
| `msg.Type == tea.KeyRunes` + `string(msg.Runes)` | ⚠ `msg.Text != ""` + `msg.Text` (covers space too — v1 needed `KeySpace` special-cased, v2 space has `Text == " "` but `String() == "space"`) |
| `tea.KeyType` (test helper parameter) | key codes are runes; helpers take `rune` / construct `tea.KeyPressMsg{Code: ..., Text: ...}` |
| `case tea.MouseMsg:` (forwarded to viewport) | v2 splits into `MouseClickMsg`/`MouseWheelMsg`/`MouseMotionMsg`/`MouseReleaseMsg` under a `tea.MouseMsg` interface — keep the interface case, forward as today |
| `tea.WindowSizeMsg`, `tea.Quit`, `tea.QuitMsg`, `tea.Batch`, `tea.Cmd`, `tea.Msg` | unchanged |
| `tea.ClearScreen` (ctrl+l repair) | still exists — keep |

⚠ **Key-name audit**: every `msg.String()` comparison in the codebase (`"esc"`, `"enter"`, `"ctrl+c"`, `"pgup"`, `"q"`, `"y"`, `"/"`, …) must be re-verified against v2's naming table in one sitting — v2 renamed at least space (`" "` → `"space"`). Grep inventory: ~40 comparisons across `tui_viewer.go` and `ai_tui.go`.

### 6.2 bubbles

| v1 | v2 |
|---|---|
| `viewport.New(80, 20)` | `viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))` |
| `m.view.Width = w` / `m.view.Height = h` | `m.view.SetWidth(w)` / `m.view.SetHeight(h)` |
| `HalfViewUp()` / `HalfViewDown()` | `HalfPageUp()` / `HalfPageDown()` |
| `SetContent`, `GotoBottom`, `AtBottom` | unchanged |
| `textarea.New()`, `Prompt`, `ShowLineNumbers`, `CharLimit`, `SetHeight`, `SetWidth`, `KeyMap`, `Focus` | unchanged |
| `input.FocusedStyle.CursorLine = …` etc., `input.BlurredStyle = …` | ⚠ styles moved into a `Styles` struct (`Focused`/`Blurred` `StyleState`): start from `textarea.DefaultStyles(isDark)`, clear the cursor-line band and placeholder exactly as today, apply with `input.SetStyles(styles)` |
| (virtual cursor implicit) | ⚠ v2 supports the real terminal cursor; call `SetVirtualCursor(true)` to preserve today's behavior byte-for-byte (§10.2 for the real-cursor follow-up) |
| `textarea.Blink` | kept (virtual-cursor blink command) |
| `table.New(table.WithFocused(true))`, `SetRows/SetColumns/SetWidth/SetHeight/SetCursor/Cursor/Update/View` | shape unchanged — verify option names and default styles at impl |
| `spinner.New(spinner.WithSpinner(spinner.MiniDot))`, `spinner.TickMsg`, `m.spin.Tick` | unchanged |

### 6.3 lipgloss

| v1 | v2 |
|---|---|
| `import "github.com/charmbracelet/lipgloss"` | `import "charm.land/lipgloss/v2"` |
| `NewStyle`, `Color("2")`, `Width`, `JoinVertical/Horizontal`, `RoundedBorder`, `MaxWidth`, alignment consts | unchanged (`Color` now returns `color.Color`; our usage is unaffected) |
| `lipgloss.AdaptiveColor{Light, Dark}` (chart palette) | ⚠ removed from core — `charm.land/lipgloss/v2/compat.AdaptiveColor{Light: lipgloss.Color(l), Dark: lipgloss.Color(d)}`; the compat package does its own one-time background detection, matching v1 semantics for code that renders outside a program (exactly what charts.go is) |
| `lipgloss.HasDarkBackground()` | ⚠ `lipgloss.HasDarkBackground(stdin, stdout) (bool, error)` — explicit, queried once pre-program (fits `aiMarkdownStyle`'s existing design); `compat` also exposes the v1-style implicit form |
| `lipgloss.ColorProfile()` | ⚠ removed — `colorprofile.Detect(os.Stderr, os.Environ())` (§7.6) |

⚠ **Downsampling moved out of `Style.Render`.** In v1 the global renderer detected the terminal and every `.Render()` degraded colors to it. In v2, degradation happens at the program boundary (Bubble Tea downsamples for you) or at an explicit writer (`lipgloss.Fprintln`, `colorprofile.Writer`) — a bare `fmt.Fprintln(os.Stderr, style.Render(...))` emits whatever color the style holds. Consequences for us in §7.1/§7.6.

### 6.4 huh

| v1 | v2 |
|---|---|
| `import "github.com/charmbracelet/huh"` | `import "charm.land/huh/v2"` |
| `NewForm/NewGroup/NewSelect[T]/NewMultiSelect[T]/NewConfirm/NewOption`, `Title/Description/Options/Filtering/Filterable/Height/Value/Affirmative/Negative` | unchanged |
| `WithOutput/WithInput/WithWidth/WithShowHelp`, `Run`, `ErrUserAborted` | unchanged |
| themes | ⚠ theme constructors now take an explicit `isDark bool` (`huh.ThemeCharm(isDark)`); we use the default theme today — verify the default detects the background sanely on a stderr-rendered form, else pass `WithTheme(huh.ThemeCharm(dark))` from one pre-detected value (§7.1) |

### 6.5 ntcharts

| v1 | v2 |
|---|---|
| `github.com/NimbleMarkets/ntcharts/barchart` | `github.com/NimbleMarkets/ntcharts/v2/barchart` |
| `barchart.New(w, h, barchart.WithNoAxis())`, `BarData`, `BarValue{Name, Value, Style}`, `PushAll`, `Draw`, `View` | shape unchanged; `Style` is now a lipgloss **v2** style — which is why ntcharts must move in the same commit as lipgloss |

---

## 7. Per-file work plan

### 7.1 `tui.go` — forms, styles, spinner
- Import swaps (huh v2, lipgloss v2). All styles here use ANSI colors 1–3 ("1", "2", "3") — safe on every terminal, no downsampling concern.
- `tuiForm` keeps `WithOutput(os.Stderr).WithInput(os.Stdin).WithWidth(tuiWidth()).WithShowHelp(true)` unchanged.
- Add one process-wide lazily-computed `tuiDarkBackground` (via `lipgloss.HasDarkBackground(os.Stdin, os.Stderr)`, defaulting to dark on error) if dogfooding shows huh v2's default theme guessing wrong on stderr forms; wire it as `WithTheme(huh.ThemeCharm(dark))` in `tuiForm`. Do not add it speculatively.
- `startTUISpinner`, `spinnerTransport`, quips: zero changes (hand-rolled ANSI).

### 7.2 `tui_picker.go`, `tui_querybuilder.go`, `self_update.go`
- Import swap only. `huh.ErrUserAborted`, `Filtering(true)`, `Filterable(true)`, `Height(14)`, `NewConfirm` all carry over. The `huh` degradation contract the picker relies on (renderer failure → cancel path) is unchanged.

### 7.3 `tui_viewer.go` — the interactive table
- `runTableViewerProgram`: drop `tea.WithAltScreen()`; keep `WithOutput/WithInput`.
- `View()` returns `tea.View`: `v := tea.NewView(frame); v.AltScreen = true; return v`.
- Filtering-mode key handling: replace the `msg.Type` switch with `msg.Code` (enter/esc/backspace/ctrl+c) and `msg.Text` for typed characters — this *removes* the `KeyRunes, KeySpace` special case.
- Normal-mode handling already switches on `msg.String()` — survives modulo the §6.1 key-name audit.
- `bubbles/table` calls: verify-only.

### 7.4 `ai_tui.go` — the session program
- `runAISession`: options shrink to `tea.NewProgram(initial)`; alt screen and mouse move into `View()` (`v.AltScreen = true`, `v.MouseMode = tea.MouseModeCellMotion` when `m.mouseOn`).
- `/mouse` verb: delete the `tea.EnableMouseCellMotion`/`tea.DisableMouse` commands — flipping `m.mouseOn` is now sufficient, the next frame declares it. The settings persistence stays.
- `Update`: `tea.KeyMsg` → `tea.KeyPressMsg` (also `handleKey`, `handleKeyEntryKey`, `handlePickerKey` signatures); `tea.MouseMsg` stays an interface case forwarded to the viewport; `spinner.TickMsg`, `WindowSizeMsg`, custom messages unchanged.
- `msg.Type == tea.KeyRunes` sites (key entry, picker filter, completion trigger): `msg.Text`.
- textarea: `DefaultStyles(dark)` + clear cursor-line band/placeholder + `SetStyles`; `SetVirtualCursor(true)`; `textarea.Blink` stays in `Init`.
- viewport: constructor options; `SetWidth`/`SetHeight` in `layout()`; `HalfPageUp`/`HalfPageDown` in the PgUp/PgDn/ctrl+u/ctrl+d cases.
- `aiMarkdownStyle`: `lipgloss.HasDarkBackground(os.Stdin, os.Stderr)`, still resolved once before the program runs; rewrite the comment — the constraint is now design preference, not upstream-bug avoidance.
- `View()`: assemble the same string, return `tea.NewView` with `AltScreen`/`MouseMode`. `aiFitFrame`, `aiRule`, `statusLine` unchanged.
- `aiDoitLogo`'s `#FC3165`: inside the program, v2 downsamples per the detected profile — parity with v1. The `CLICOLOR_FORCE=1` child-env contract (`aiDispatchEnv`) is about the *children's* rendering and is unaffected here; recheck §7.6 for the chart child case.

### 7.5 `ai_picker.go`
- No Charm imports in the implementation file (the selection UI lives in ai_tui.go); only its test file constructs key messages (§8).

### 7.6 `charts.go` — renders *outside* any program
- Palette: `lipgloss.AdaptiveColor` → `compat.AdaptiveColor` (values wrapped in `lipgloss.Color`). The compat package resolves light/dark once at init from the environment — the v1 behavior this code was written against.
- `chartColorCapable`: replace `lipgloss.ColorProfile() != termenv.Ascii` with a `colorprofile.Detect(os.Stderr, os.Environ())` comparison (`> colorprofile.Ascii` — i.e. ANSI or better; verify enum ordering at impl). Drop the termenv import; `colorprofile` becomes a direct dependency.
- ⚠ **Downsampling**: the stacked chart's truecolor hex palette is printed with `fmt.Fprintln(os.Stderr, …)`. Under v1, `.Render()` degraded to the detected profile; under v2 it will not. Route the chart/legend output through a `colorprofile.Writer` on stderr (or `lipgloss.Fprintln`) so 256-color terminals keep getting legal sequences. This also covers the `dci ai` dispatch-child path (`DCI_SESSION_RENDER=1`, `CLICOLOR_FORCE=1` on a pipe): confirm during dogfooding that the child's detected profile still upgrades to truecolor for the session transcript — add `COLORTERM=truecolor` to `aiDispatchEnv` if v2's env detection needs it where termenv didn't.
- ntcharts v2 import; re-verify the partial-block background-compensation workaround (the `segments[k].Style.Background(...)` loop) against v2 — it may be fixed upstream, in which case delete it with a comment pointing at the upstream fix.
- asciigraph line chart and `augmentTableViewColumns`: untouched.

### 7.7 `go.mod` / docs
- Apply §5. Run `go mod tidy`; confirm bubbletea/bubbles/huh v1 vanish from go.sum.
- AGENTS.md: add one line to Project Context noting the Charm stack is on v2 (`charm.land` import paths) so future agents don't "fix" the imports backwards.
- TUI-SPEC.md / AI-SPEC.md are historical design docs — leave them; they describe features, not import paths.

---

## 8. Tests

All TUI behavior tests drive `Update` with hand-built messages and assert on rendered strings — they port mechanically:

- `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}` → `tea.KeyPressMsg{Code: 's', Text: "s"}`; `tea.KeyMsg{Type: tea.KeyEnter}` → `tea.KeyPressMsg{Code: tea.KeyEnter}` (≈25 sites across `tui_viewer_test.go`, `ai_tui_test.go`, `ai_picker_test.go`).
- The `aiPress(m, tea.KeyType)` helper becomes `aiPress(m, rune)`; add an `aiType(m, string)` sibling if it shortens the diff.
- `View()` assertions: add one test helper that extracts the content string from `tea.View` (accessor per the v2 docs at impl time) so the existing `strings.Contains` assertions survive unchanged.
- `tea.WindowSizeMsg` construction: unchanged.
- `chartColorCapable` is already a test-fakeable var; charts tests need no changes beyond compilation.
- huh-based flows are already tested through fakeable vars (`pickerSelectEntry`, `confirmDestructiveInteractively`, `maybeRunQueryBuilder`) — untouched.

Nothing in the suite exercises a real PTY, so CI proves compilation and model logic; rendering fidelity is on the dogfood checklist (§11).

---

## 9. Risks and watchpoints

1. **Key-name drift** (§6.1 audit). A missed rename silently kills one keybinding. Mitigation: the string-comparison audit plus the ported unit tests, which press every binding the specs promise.
2. **huh v2 theming on stderr.** Forms render on stderr while detection conventionally probes stdin/stdout; if the default theme guesses wrong where v1 guessed right, wire the explicit theme (§7.1). Low blast radius — colors, not behavior.
3. **Renderer regressions on exotic terminals.** v2's renderer is a ground-up rewrite; its failure modes will not be v1's. Mitigations: the `DCI_NO_TUI` escape hatch already bypasses every TUI path; ctrl+l is kept; dogfood across the checklist terminals before tagging.
4. **ntcharts v2.2.0 ships a `replace` of bubbletea to a fork** (`github.com/neomantra/bubbletea/v2`, "awaiting upstream merges") in its go.mod. Replace directives are ignored in dependencies, so we build it against the real charm.land bubbletea — meaning ntcharts v2 is not tested by its authors in exactly the configuration we compile. Our exposure is minimal — `barchart` used render-to-string, no tea program — but verify the stacked chart renders correctly and check whether the pending patches touch barchart before pinning. If v2.2.0 misbehaves, the fallback is staying on ntcharts v1 for one release (it needs lipgloss v1 styles, so the palette code would carry a temporary dual-import — ugly but legal) or dropping the stacked view to the asciigraph fallback until upstream settles. Neither fallback blocks the rest of the migration.
5. **Windows.** No TUI CI coverage; v2's input rewrite is a net positive long-term but a regression risk at the moment of switching. Dogfood `dci ai` and the viewer in Windows Terminal before release.
6. **Version skew during review.** Pin exact versions in the PR; do not let unrelated dependency bumps ride along.

---

## 10. Explicit non-goals

### 10.1 No incremental migration
huh v2 forms *could* move first (they only exchange strings with the rest of the code), but bubbles v1's textarea style fields take lipgloss v1 styles while ntcharts v2 takes lipgloss v2 styles — a split migration forces both lipgloss majors as direct imports of the same package with alias gymnastics. In one `package main` the honest unit is one PR, reviewed commit-by-commit (§11).

### 10.2 Deferred follow-ups (each its own issue, post-migration)
- **Real terminal cursor** in the `dci ai` input (drop `SetVirtualCursor(true)`, position `view.Cursor` from the layout) — IME and accessibility win.
- **In-program background detection** (`tea.RequestBackgroundColor`) replacing the pre-program probe, enabling live light/dark switching.
- **glamour v2** — bundled with any restish-stack move (§5).
- v2-only niceties (kitty keyboard enhancements for shift+enter multiline input, clipboard OSC52 for `/export`-to-clipboard) — note them in the ideas pile, not this migration.

---

## 11. Rollout

- **One PR** on this branch, commits ordered for review: (1) go.mod swap + mechanical import renames; (2) bubbletea program changes (`tui_viewer.go`, `ai_tui.go`); (3) charts/lipgloss compat + colorprofile; (4) tests; (5) docs line in AGENTS.md.
- **Commit prefixes**: `chore:` for the dependency/migration commits (not user-facing per the changelog conventions), `test:`/`docs:` where applicable. If dogfooding surfaces a genuine user-visible fix riding on v2 (e.g. the smear repair), it may earn a `fix:` line of its own so the changelog tells users why the release exists.
- **Versioning**: routine **patch** bump per the 2026-08-18 policy — this is not a command-surface milestone.
- **Dogfood checklist before tagging** (macOS Terminal, iTerm2/Ghostty, VS Code terminal, Windows Terminal; light *and* dark backgrounds; one 256-color TERM):
  - `dci ai`: banner, completion popup, picker, destructive y/N, /mouse on+off, /export, ctrl+l, wheel + PgUp/PgDn, narrow-pane resize
  - zero-arg picker + ambiguity select + destructive confirm (huh forms on stderr)
  - `dci query` builder end-to-end
  - `--table-mode interactive`: sort, filter, column scroll, enter-prints-id (stdout cleanliness!)
  - `--chart` line + stacked on truecolor and 256-color; the same via `dci ai` dispatch
  - `DCI_NO_TUI=1` and piped/agent-mode runs byte-identical to today (the contract tests should already prove this)
- `post-release-verify.yml` covers install-channel sanity as usual.

## 12. Questions for the maintainer

1. Timing: land now, or after the next feature release so the patch release is migration-only? (Spec assumes migration-only.)
2. Is the ntcharts fallback posture in §9.4 acceptable, or is "stacked chart blocks the migration" the bar?
3. Any known-exotic terminal among users (tmux+screen-256color, PuTTY?) to add to the dogfood list?

## 13. Implementation notes (as landed)

Where the implementation deviates from or resolves the plan above:

- **Pastes are their own message type in v2** (`tea.PasteMsg`), no longer key messages — a gap in §6.1's map. Without handling, pasting an API key into the guided setup or text into the picker filter would silently do nothing. `aiModel.handlePaste` routes pastes to whichever text sink owns the keyboard (key entry, picker filter, or the textarea, which handles `PasteMsg` itself); answer-only states ignore them.
- **`compat.AdaptiveColor` was not used** (§6.3 suggested it): the compat package probes the terminal at package *init* — an OSC round trip on every `dci` invocation, keyed on stdout rather than stderr where charts render. `charts.go` instead defines a local `chartAdaptiveColor` resolving lazily through a `sync.OnceValue` around `lipgloss.HasDarkBackground(os.Stdin, os.Stderr)`, so building a palette never queries the terminal and non-chart invocations never pay for detection.
- **Styled stderr output outside programs goes through a `colorprofile.Writer`** (`tuiStyledStderr` in tui.go): v1's `Style.Render` downsampled and stripped implicitly; in v2 that responsibility is the writer's. Routed sites: the login notice, the destructive-confirm box, the update notice, and the stacked chart. This restores NO_COLOR/non-TTY stripping and degrades the truecolor chart palette on 16/256-color terminals.
- **The §6.1 key-name audit came back clean**: only space changed (`"space"` vs `" "`), and no dci binding matched `" "` via `String()` — every existing `msg.String()` comparison stands, with `tea.KeyEsc` → `tea.KeyEscape` as the one constant rename in tests.
- **textarea's virtual cursor is on by default in v2** — the planned `SetVirtualCursor(true)` call (§6.2) is unnecessary; the custom styles moved into `textarea.Styles`/`StyleState` via `SetStyles` as planned, keeping the blinking reverse-video cursor.
- **The §9.4 ntcharts risk did not bite**: v2.2.0 compiles and passes the chart tests against upstream `charm.land/bubbletea/v2` v2.0.9. The partial-block background-compensation workaround in `renderStackedChart` is kept as-is.
- **huh v2 needed no theme wiring** (§7.1's contingency): forms detect the background themselves via `tea.BackgroundColorMsg`; confirm on light terminals during dogfooding.
- **Module graph after `go mod tidy`**: bubbletea, bubbles, huh, and ntcharts v1 are gone entirely; lipgloss v1 and termenv remain only as restish/glamour indirects, per §5.
- **Binary size** (linux/amd64, `-trimpath` dev build): 68.5 MB → 70.4 MB, **+1.9 MB (+2.7%)** — within §5's expectation.
- The §11 dogfood checklist (real terminals, light and dark, Windows Terminal) remains to be run before tagging a release.
