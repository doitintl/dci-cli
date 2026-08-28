# Design spec: argument-placeholder overlays in the `dci ai` completion

Status: **phase 1 implemented** (same PR as this spec: `ai_placeholder.go` + tests, the `refreshGhost`/`ghostedInputRow` hooks in ai_tui.go, and the sketch-keeping schema parse in body_validation.go). Phase 2 remains design-only, gated on P1 dogfood. As-landed deviations from the draft are recorded at the end (§10).

Source: Alfredo's dogfood feedback (Slack, 2026-08-28), deferred out of [PR #133](https://github.com/doitintl/dci-cli/pull/133) ("worth its own spec if we want it"):

> What if we take the interactive session auto-completion to state-of-the-art level, and overlay argument placeholders for each operation? For example, when we write or select "/add-ticket-tags" it already shows a faint "ticketid tags: [string]" and let us just write "XYZ" for ticketId, auto-inserts the fixed "tags:" and let us write each string of the array? It's a complex UX and should be dynamic according to the API Spec, but should be doable. Maybe a full epic on itself.

Every claim about existing code cites the function and file it is based on. Claims about PR #133 code (`argvUsageTrailer`, `requestSchemaTopLevelFieldList`) cite that PR's diff; **this spec's phase 1 depends on #133 landing first** (§5.2).

Scope: the `dci ai` interactive session only — the completion popup and input line in `ai_tui.go`/`ai_slash.go`. Non-interactive agent mode, one-shot `dci ai "question"`, the shell CLI, and shell completions are untouched.

---

## 1. Summary and target experience

Two phases, deliberately split so the high-value half ships without the hard half:

- **Phase 1 — ghost signature (passive).** The moment the input names a runnable command, the rest of the input line shows the command's remaining arguments as faint ghost text, consumed left-to-right as the user types real values. No new key semantics, no editing states — purely presentational, derived per keystroke from what is already typed.
- **Phase 2 — guided field entry (active).** Tab in argument position accepts the next ghost token: fixed tokens (`tags:`) are inserted literally, value placeholders (`<ticket-id>`) put the cursor there for typing. Array fields repeat on comma. Phase 2 is gated on phase 1 dogfood (§8).

The worked example, phase 1 (`·` marks the cursor; `▒text▒` marks faint ghost text):

```
› /add-ticket-tags ·▒ticket-id tags*: [string]▒        command accepted, nothing typed
› /add-ticket-tags 318240 ·▒tags*: [string]▒           ticket-id consumed
› /add-ticket-tags 318240 tags: prod, billing·         all placeholders consumed, ghost gone
```

And phase 2, the same command:

```
› /add-ticket-tags ·▒ticket-id tags*: [string]▒        Tab → cursor stays, user types the ID
› /add-ticket-tags 318240 ·▒tags*: [string]▒           Tab → "tags: " inserted literally
› /add-ticket-tags 318240 tags: ·▒string, …▒           type a value; comma continues the array
```

The vocabulary is the usage trailer's (PR #133, `argvUsageTrailer`, error_contract.go): path parameters spelled as cobra's `Use` spells them, body fields with their `*` required markers. A user who ignores the ghost and gets the arity error sees the same words in the trailer — the overlay is the *before* view of the same contract the trailer shows *after* failure.

## 2. What exists today (inventory)

The feature composes from machinery that already exists; nothing needs the network and nothing new is parsed from the OpenAPI spec directly.

| Piece | Where | What it gives the overlay |
|---|---|---|
| Completion popup | `aiCompletionsFor`/`aiGroupCompletionsFor` (ai_slash.go), window + accept in ai_tui.go (`aiCompletionWindow`, `acceptCompletion`) | The moment a command is accepted (`acceptCompletion` inserts `/name ` with a trailing space) — the natural trigger to build the overlay. Popup key semantics (Tab/Enter/↑/↓, AI-POLISH-SPEC F1) that phase 1 must not disturb. |
| Input line | `bubbles/textarea` v2, single row, prompt `› ` (`newAIModel`, ai_tui.go); faint `Placeholder` style already defined in `inputState` | The row the ghost renders into. The textarea's own placeholder only shows on an *empty* input — ghost text after typed content is not a textarea feature and must be composited in `View()` (§6.2). |
| Path-parameter metadata | `operationPathParameters` (path_validation.go), populated offline from the cached spec via `aiEnsureResolutionMetadata` (ai_picker.go) → `ensureDestructiveOperations` → `setOperationPathParameters` (destructive_contract.go) | Per-operation `[]*cli.Param`: `OptionName()`, `Type`, `Description`, `Example`. Same load policy as the picker: cached spec only, never OAuth, absent before first login. |
| Body-field metadata | `requestSchemaTopLevelFieldList(command.Long)` (PR #133, body_validation.go): top-level fields **in schema order**, each keeping its `*` required marker, parsed from the `## Request Schema` block restish renders into the command's Long help | The `tags*` part of the ghost. The same parse that body validation and the usage trailer trust. |
| Spec examples | `command.Example` (restish renders one `dci <use> <body>` line per spec example); `firstUsageExample` (PR #133, error_contract.go) respells it for the session | Optional richer hint for phase 2's value entry. |
| Usage vocabulary | `argvUsageTrailer(argv, true)` (PR #133): descends the live cobra tree via `findChildCommand`, leaf commands only, `Use` + `[body fields]` + field list | The wording contract §1 commits to. Also the proof that a cobra-tree walk covers GA and beta uniformly. |
| Beta parity | `hydrateBetaCommands` (called from `aiSessionCatalog`, ai_slash.go) builds beta cobra commands with the same shapes: `Use` carries slugged path params, `Long` carries the same `## Request Schema` block (`betaOperationLong`), spec examples included (beta_commands.go) | A cobra-tree-driven overlay gets beta for free (§5.3). |
| Trim/degrade helpers | `aiTrimTo`, `aiFitFrame` (ai_tui.go) | Narrow-terminal behavior identical to the status line's (§3.4). |

## 3. Phase 1 — ghost signature

### 3.1 When the ghost shows

The ghost renders iff **all** of:

1. The input starts with `/` and its first token(s) resolve to a **leaf** command in the live cobra tree — the same descent `argvUsageTrailer` uses (`findChildCommand`, leaf = no subcommands, non-empty `Use`). Groups (`/beta`, `/customer-context`) show nothing: their next word is a *command* word, and that is the popup's job, not the ghost's.
2. The command has remaining unconsumed placeholders (§3.2). A fully-satisfied line shows no ghost — silence is the "you can press Enter" signal.
3. The line is not a session verb (`aiLookupVerb`) and not a user-defined command — their argument surfaces are one-line usages (`aiSessionVerb.usage`) and free-form expansions respectively, not spec-derived signatures. Out of scope (§8 non-goals).

The ghost and the popup can coexist (e.g. `/beta run-report ` keeps a one-row exact-match popup open, `aiGroupCompletionsFor`): the popup owns its rows above the input, the ghost owns the input row's tail, and they never compete for a key in phase 1 because the ghost consumes no keys at all.

### 3.2 The placeholder model and consumption

Per command, the signature is an ordered list built once and cached (§5.1):

1. One placeholder per **path parameter**, spelled as `Use` spells it (`get-report report-id` → `report-id`; beta's slugged `ticketid` stays `ticketid` — vocabulary parity with the trailer beats prettiness).
2. One placeholder per **top-level body field**, in schema order, spelled `name*: <sketch>` where `*` is the schema's required marker and `<sketch>` is the field's one-line value sketch from the schema block (`[string]`, `object`, `string`) — a small extension of `requestSchemaTopLevelFieldList` that keeps the text after the colon (§5.2). Body placeholders render only the **required** fields by default, with a trailing `…` when optional fields exist — the full list is the trailer's and `--help`'s job; the ghost is a prompt, not a reference.

Consumption is positional and pure — a function of the typed argument tokens, recomputed per keystroke (no hidden state to desync from paste, backspace, or history recall):

- Tokenize the input after the command words with `splitCommandLine` (ai_slash.go); a trailing unterminated quote or backslash freezes the ghost as-is rather than erroring (the user is mid-token).
- Flags and their values are skipped, using the command's flag set to know which flags take values (`aiOperationFlagSet`, ai_picker.go — the same positional/flag split `aiPositionalIndexes` already performs for the picker).
- Each positional token consumes one path-param placeholder, left to right.
- The first token that *names a body field* (`shorthandBodyFieldPattern`, body_validation.go — `tags:`, `config.currency:`) or opens a JSON body (`{`) or references stdin/file (`@`, `<`) consumes that placeholder **and** switches consumption to name-based: from here on, placeholders disappear as their field names appear anywhere in the remaining input. Restish body shorthand is order-free; the ghost must not insist on schema order once the user is writing shorthand.
- A name-resolvable command (in `resolutionIndex`) consumes its single path-param placeholder on *any* first positional token, ID-shaped or not — the picker/resolver (ai_picker.go) accepts names there, and the ghost must not nag "report-id" at someone typing a report name. The ghost for that slot renders as the resource noun instead (`report-name-or-id`, from `singularResourceName`), and when submitting the empty line would open the zero-argument name picker, the ghost says so: `report-name-or-id (enter to pick from a list)`. The cue re-applies the picker's own gates (`aiPickerIntentFor`: body-less operation, no `--id` on the line, no `DCI_NO_RESOLVE`), so it never promises a picker a gate would suppress; on narrow panes it trims away before the argument name does.

Edge: multi-word names for resolvable commands (`/get-report monthly spend`) keep consuming only the one placeholder — every further positional on a single-path-param resolvable command is a name word, per `resolvePathArguments`' joinable rule (mirrored in `aiPickerIntentFor`, ai_picker.go:97).

### 3.3 Keys, cursor, and what phase 1 does NOT change

Phase 1 changes **zero** key handling. Explicitly:

- **Tab / Enter with the popup open**: exactly today's semantics (accept highlighted / submit-when-exact — `aiCompletionExact`, AI-POLISH-SPEC F1).
- **Tab with the popup closed**: today it no-ops (`acceptCompletion` on empty `m.completions` returns immediately); it stays a no-op in phase 1. Phase 2 claims it (§4).
- **Enter**: submits whenever it submits today. The ghost never blocks submission — required-but-missing arguments still fail in the child, and the failure still gets the usage trailer. The ghost reduces that failure's frequency; it does not become a validator (non-goal, §8).
- **Esc, ↑/↓, ctrl+c**: untouched (history recall via `setInput` closes the popup and shows the recalled line's remaining ghost, which falls out of the pure recompute for free).
- **Cursor**: stays wherever the textarea puts it. The ghost renders only when the cursor is at the end of the line (the overwhelmingly common state while composing); mid-line editing hides it rather than rendering ghost text *under* the cursor block or splitting it around the edit point. This single rule dodges the entire cursor-compositing problem (§6.2).

### 3.4 Degradation

- **No metadata for the command** (local commands like `login`, `update`; anything with an empty signature): no ghost. The feature is additive; absence looks exactly like today.
- **Logged-out session / no spec cache**: `aiEnsureResolutionMetadata` skips when `dci.cbor` is absent (ai_picker.go:41), `operationPathParameters` stays empty, the catalog has no API half (`aiCatalogMissingAPIOperations`, ai_slash.go) — no ghost anywhere, no error, no network fetch. Same policy as the picker and completion: the banner already explains the state ("API commands appear after /login").
- **Narrow terminals**: the ghost is trimmed with `aiTrimTo` to the input row's remaining width — never wrapped (the textarea is one row; a wrapped ghost would corrupt the frame grid, §3.1 of AI-SPEC). Under ~4 remaining cells it disappears entirely, matching `aiTrimTo`'s own floor. `aiFitFrame` still clamps the composed row as a backstop.
- **Terminals without faint support**: lipgloss degrades `Faint(true)` the same way it already does for `aiEchoStyle` everywhere else — no special handling.

## 4. Phase 2 — guided field entry

Phase 2 makes the ghost interactive. It ships only after phase 1 has dogfooded (§8), and its details are re-reviewable then; this section records the intended shape so phase 1's model doesn't paint it out.

- **Tab in argument position** (popup closed, ghost showing): accept the next placeholder.
  - A **fixed token** — a body field's `name:` prefix — is inserted literally with a trailing space, cursor after it. This is Alfredo's "auto-inserts the fixed `tags:`".
  - A **value placeholder** (path param, or a field's value) inserts nothing: the cursor is already where the value goes, and the ghost re-renders showing the *value* hint (the parameter's `Example` from the spec when present, else its type). Inserting example text as real input was considered and rejected: ghost-to-real-text promotion of a value the user didn't choose is how wrong IDs get submitted.
- **Array-of-strings entry**: while the cursor is inside an array field's value (`tags: prod`), the ghost shows `▒, next-string▒` — a typed comma continues the array (restish shorthand already parses `tags: a, b`), and the ghost drops once any element exists, since the continuation is now self-evident.
- **Popup precedence is absolute**: any state where the popup is open routes Tab/Enter to the popup exactly as today. Phase 2's Tab only fires when `len(m.completions) == 0`.
- **Esc** keeps today's meaning (clear input / cancel turn) — it does not "exit placeholder mode" because there is no mode: phase 2 remains stateless in the same way phase 1 is; Tab is just an insertion convenience computed from the same pure model.

Not in phase 2 either: a huh-style per-field form over the input, validation-on-Tab, or flag placeholders — see non-goals (§8).

## 5. Data: where placeholders come from and how they stay honest

### 5.1 One source: the live cobra tree, resolved lazily, memoized per session

The signature builder takes the typed argv words and walks the live cobra tree (the `argvUsageTrailer` descent), reading:

- `command.Use` → path-parameter names (and arity),
- `operationPathParameters[name]` → types/examples for those parameters (GA operations only, keyed by the plain operation name, `setOperationPathParameters`, path_validation.go — beta ops never populate this map, so their placeholders fall back to the `Use` word, §5.3),
- `command.Long` → body field list with markers and value sketches (§5.2),
- `command.Example` → the first spec example (phase 2 value hints),
- `resolutionIndex[name]` → whether the path slot accepts names (§3.2).

Signatures are memoized in an `aiModel` map keyed by the command path, invalidated wherever the catalog is rebuilt today: `refreshAuthState` (login/logout, ai_tui.go) — the only events that change the tree mid-session. This mirrors how `m.catalog` itself lives.

### 5.2 Dynamic against the API spec, with the same staleness window as everything else

Everything above derives from restish's cached spec (`dci.cbor`) — the same artifact that feeds completion, the picker, body validation, and the trailer. The overlay is therefore exactly as fresh (and as stale) as the rest of the session: a spec change picked up on the next cache refresh updates ghost, validation, and trailer together, and they can never disagree with each other. No new fetch, no new cache, no version pinning.

Dependency called out for sequencing: the body-field half needs PR #133's `requestSchemaTopLevelFieldList`, extended to keep the value sketch after the colon (return `name*` and sketch, e.g. as a small struct list, with the existing string-list behavior preserved for the trailer). If #133 stalls, phase 1 can ship path-params-only, but the spec's example (`tags*: [string]`) — and most of the feedback's value — is in the body half, so the intended order is #133 → this.

### 5.3 Beta commands

No special path. Beta cobra commands are built with the same `Use`/`Long`/`Example` shapes as GA (`betaOperationCommand`, `betaOperationLong`, beta_commands.go) and are hydrated in the session by `aiSessionCatalog` (ai_slash.go:267). The cobra-tree walk covers them; the one asymmetry is `operationPathParameters`, which beta ops don't populate — for them the path-param placeholder falls back to the `Use` word alone (no type/example), which is all phase 1 renders anyway. The `aiUsageLineFor` test vocabulary (`/beta get-report-results operation-id`) is the parity check to copy.

## 6. Implementation sketch

### 6.1 New sibling file: `ai_placeholder.go` (+ `ai_placeholder_test.go`)

Per the AGENTS.md chapter-per-file convention, one new file in `package main`, all pure functions with no Bubble Tea imports (the ai_slash.go pattern — the TUI stays a thin renderer of testable logic):

```go
// aiPlaceholder is one unconsumed argument slot.
type aiPlaceholder struct {
    label    string // "report-id", "tags*: [string]", "report-name-or-id"
    fixed    string // phase 2: literal insertion ("tags: "); "" for value slots
    required bool
    body     bool   // path param vs body field
}

// aiPlaceholderSignature: the memoized per-command model (§5.1).
func aiPlaceholderSignatureFor(argv []string) []aiPlaceholder

// aiPlaceholdersRemaining: the pure consumption function (§3.2) —
// signature + typed line in, unconsumed tail out.
func aiPlaceholdersRemaining(sig []aiPlaceholder, line string) []aiPlaceholder

// aiGhostText: remaining placeholders → the trimmed, styled ghost string
// for the given cell budget ("" hides the ghost).
func aiGhostText(remaining []aiPlaceholder, width int) string
```

### 6.2 TUI hooks (ai_tui.go), all small

- **Recompute**: wherever `setCompletions(aiCompletionsFor(...))` runs today (`handleKey`'s fall-through, `handlePaste`), also recompute the ghost string into a new `m.ghost` field. Same lifecycle, same purity.
- **Render**: in `View()`'s default branch, composite the ghost after `m.input.View()`. Mechanics: the input is a single row whose content width is known (`lipgloss.Width` of prompt + value); when the cursor is at end-of-line (§3.3), append the faint ghost to the row and let the existing width clamp in `aiFitFrame` back-stop it. The textarea pads its row to full width, so the splice must cut the padding first — a small pure helper (`aiSpliceGhost(row, ghost, col, width) string`) that the tests can hammer, including against the reverse-video cursor cell at the splice point. This helper is the riskiest 30 lines of the feature (§9 R1).
- **Layout**: none. The ghost adds no rows; `layout()` is untouched.
- **Phase 2 key hook**: one new arm in `handleKey`'s `"tab"` case, guarded by `len(m.completions) == 0`, that asks the placeholder model for a fixed-token insertion.

### 6.3 Testing plan

In the style of the existing `_test.go` files:

- **Unit (`ai_placeholder_test.go`)**: signature building against a fake cobra tree (reuse the `usageTrailerTestTree` shape from PR #133's error_contract_test.go — a bodied op with schema+example, schema-only, no-body, a beta-style slugged op, a group); consumption tables (flags skipped, quotes, mid-token freeze, shorthand switching to name-based, resolvable-command name slots, multi-word names); ghost text trimming at widths including the <4-cell floor; the splice helper against padded rows and cursor cells.
- **TUI unit (`ai_tui_test.go`)**: ghost recomputed on input change and paste; hidden on history recall mid-line edit; popup-open states unchanged byte-for-byte where no leaf command is typed (regression guard for F1 semantics).
- **E2E (`ai_tui_e2e_test.go`)**: per the file's harness rules, keystroke replays on the real pty — type `/add-ticket-tags` via the popup, assert the frame carries the faint signature; type the ID, assert the consumed placeholder left the frame; a logged-out (no forged spec cache) session asserts no ghost. When phase 2 lands, Tab-insertion replays join them.

## 7. Phasing

| Phase | Ships | Gate |
|---|---|---|
| **P1** | Ghost signature: `ai_placeholder.go` model + render, path params + required body fields, consumption, degradation (§3). Zero key-handling changes. **Implemented** (same PR; #133 merged first, as required by §5.2). | Maintainer review |
| **P2** | Tab acceptance: fixed-token insertion, value-hint ghosts, array continuation (§4) | P1 dogfood verdict; re-review of §4's choices |

Versioning: both phases are session UX polish — `feat:` commits, routine **patch** releases per AGENTS.md (not the "new command group" minor-bump case).

## 8. Non-goals (explicit)

- **No validation or submit-blocking.** The ghost informs; the child and the usage trailer still own rejection. A ghost that blocks Enter would have to be right about flag/body interactions in every edge the CLI supports, and being wrong would block a valid command.
- **No flag placeholders.** Required operation flags exist (`requiredOperationFlags`, command_catalog.go) but are three commands' idempotency keys — the trailer and `--help` cover them; ghosting flags would triple the signature length for near-zero routine value. Revisit only on direct feedback.
- **No session-verb or user-defined-command signatures** (§3.1): different metadata, marginal value, and user commands are free-form by design.
- **No shell-side twin.** The interactive shell's missing-argument story is PR #133's trailer; a readline-style ghost in the user's own shell is not this program's to draw.
- **No huh-style form takeover of the input.** The input stays a free-text line the user can always just type into; every phase-2 affordance is an optional accelerator on top of unrestricted typing.
- **No new OpenAPI parsing.** If the schema sketch in `command.Long` is ever too coarse (nested objects), the answer is upstream in what restish renders, not a second parser here.

## 9. Risks and open questions for the maintainer

- **R1 — the splice is the fragile part.** Compositing ghost text into a textarea-rendered row that ends in a styled virtual cursor cell is exactly the kind of terminal-detail work that regresses across bubbles versions. Mitigations already in the design: end-of-line-only rendering (§3.3), a pure splice helper with adversarial tests (§6.2), and `aiFitFrame` as the outer clamp. If it still proves flaky in review, the fallback that keeps most of the value is rendering the signature *in the popup's footer row* instead of inline — worse fidelity to Alfredo's ask, but zero compositing risk.
- **R2 — ghost/popup coexistence** on exact-match rows (`/beta run-report ` keeps a one-row popup, §3.1). Proposed: ship as specced (they don't conflict), watch dogfood. Alternative: suppress the popup once the input exactly names a leaf and args have begun — a one-line change in `aiCompletionsFor` if wanted.
- **Q1 — required-only body fields in the ghost** (§3.2), with `…` marking the rest: is that the right cut, or should the ghost show every field the way the trailer's capped list does? Required-only is proposed because the ghost's job is "what must I still type", not "what could I type".
- **Q2 — phase 2's Tab on a value slot** inserts nothing and shows the example as a hint (§4). The alternative — inserting the spec example as editable text — is faster for exploration but risks submitted example IDs. Proposed: hint-only; revisit with dogfood evidence.
- **Q3 — sequencing with PR #133**: this spec assumes #133 merges substantially as-is. If review reshapes `requestSchemaTopLevelFieldList` or the trailer vocabulary, this spec follows that outcome, not the other way around.

## 10. As-landed notes (P1)

Decisions taken at implementation, where the code deviates from or sharpens the draft above:

- **One schema parser, extended in place.** `requestSchemaTopLevelFieldSketches` (body_validation.go) is the sketch-keeping parse §5.2 asked for; `requestSchemaTopLevelFieldList` (#133, merged) is now derived from it, so validation, the trailer, and the ghost share one parse. Sketches are normalized for the one-row ghost (`aiNormalizeSchemaSketch`): a nested-object opener renders as `object`, an array-of-objects opener as `[…]`.
- **No memoization.** §5.1's per-command cache was dropped: building a signature is one cobra-child walk plus one schema-block parse, cheaper than the full-catalog scan `aiCompletionsFor` already runs on the same keystroke. This also removes the invalidation surface (`refreshAuthState` has nothing extra to clear).
- **Flag skipping uses the leaf's own flag set** (`command.Flags()`), not the picker's `aiOperationFlagSet` lookup — the builder holds the command anyway. Same accepted limitation as the picker: inherited persistent flags (`--output`) are invisible, so an unknown value-taking flag's value transiently reads as a positional and costs one ghost slot until the line is corrected — never a wrong dispatch.
- **Positional order wins over body shape for open path slots** (refining §3.2's "first body-shaped token switches"): `add-ticket-tags tags: a` consumes the *path* slot with `tags: a`'s first token, exactly as the CLI parses that argv. Body-name consumption starts only once the path slots are filled.
- **The ghost shows as soon as the input names a leaf** — no trailing space required: `/get-report` ghosts immediately, and extending the token (`/get-report-x`) clears it on the next recompute.
- **R1 held.** The splice (`aiSpliceGhost`) clamps the rendered row to prompt + text + one cursor cell with a cell-based `MaxWidth`, so both blink phases of the virtual cursor survive; covered by unit tests against a padded ANSI row and by an E2E keystroke replay (`TestE2EGhostSignatureAfterAcceptedCompletion`) on the real pty. The popup-footer fallback was not needed.
- **R2 shipped as specced**: ghost and popup coexist; no suppression added.
- **Picker cue (post-merge, maintainer feedback 2026-08-28).** The zero-argument name picker was invisible until someone stumbled into it, so a pickable slot's ghost now appends `(enter to pick from a list)` — gated by `aiPickerCueApplies` on the picker's own `aiPickerIntentFor` conditions, and placed at the ghost's tail so narrow panes drop the cue before the argument name (§3.2).
