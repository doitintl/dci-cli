# Design spec: `dci ai` polish — first dogfood feedback round

Status: **implemented** (same PR as this spec). F6's upstream issue: [doiteng/omni#62101](https://github.com/doiteng/omni/issues/62101), covering both the `list-contracts` 403 and the missing currency/unit metadata.

Source: Alfredo's feedback DM (Slack, 2026-08-25, thread `p1787676307172329` + the API-key thread `p1787676758296559`), tested against the shipped `dci ai` session. Every claim about existing code cites the function and file it is based on.

Scope: six small, independent changes to the `dci ai` surface plus one upstream issue to file. No change to non-interactive agent mode, pipes, or CI behavior except the new one-shot verbosity flags (F3), which only *restrict* output.

Not an issue (no action): the agentic customer-context switch ("What's driving this posthog jump?" → `customer context switched: csp.doit.com → posthog.com`) was called out as working well. The 403 handling in F6 is also evidence the harness degrades gracefully — both stay as-is.

---

## F1. Slash picker: Enter accepts the highlighted completion

**Feedback:** "when I look for a slash command, hitting enter won't select it. just with tab. (this behavior is different from other CLIs, like claude)"

**Today:** in the main input key handler, `enter` always submits the raw input (`ai_tui.go:824` → `m.submit()`), while `tab` is the only key that accepts the highlighted completion (`ai_tui.go:827-834`). With the popup open and `/anom` typed, Enter submits the literal `/anom`, which fails, instead of selecting `/anomalies`.

**Change:** when the completion popup is visible and the trimmed input is **not already an exact command name**, `enter` behaves like `tab`: accept the highlighted completion into the input (with trailing space) and keep editing. A second Enter submits. When the input exactly matches a command (with or without args typed), Enter submits as today — so `/quit⏎` and `/anomalies list⏎` never need a double-Enter.

This matches Claude Code's picker: Enter and Tab both accept; Enter never submits a prefix that the popup is actively correcting. Up/Down keep moving the highlight (`ai_tui.go:836-863`), Esc keeps dismissing.

**Tests** (`ai_tui_test.go`): popup open + partial input + Enter → input becomes selected command, no submit; exact command + Enter → submits; no popup + Enter → submits (unchanged).

## F2. Terminal bell when a turn completes

**Feedback:** "a terminal 'beep' when it returns the result would be helpful."

Analytical turns run 1–2 minutes (opus thinking dominates); users tab away and want a completion signal.

**Change:** on `TurnDone` (`ai_tui.go:628`), the TUI writes BEL (`\a`) — the terminal maps it to its own notification (sound, dock bounce, tab highlight), which is exactly the Claude Code convention. Details:

- Only for turns that actually did work: skip the bell when the turn produced no tool calls and finished in under ~3s (echo-fast turns would make `/help` beep).
- `/bell` toggles it; the choice persists in `ai_settings.json` next to the key and model (same read/write path as `loadAISettings`, `ai_command.go:65`). Default **on**.
- One-shot mode: no bell ever — stdout may be piped, and BEL in a captured stream is corruption, not UX.

**Tests:** TurnDone with tools → frame output contains `\a`; fast toolless turn → no bell; `/bell` persists across a settings round-trip.

## F3. One-shot verbosity flags: `--quiet` / `--verbose`

**Feedback:** "dci ai 'query' could have a flag to be verbose/non-verbose (not outputting the whole investigation)"

**Today:** one-shot verbosity is auto-detected — thinking, tool starts/results, and context switches stream to stderr iff stderr is a TTY (`runAIOneShot`, `ai_command.go:70`). A human watching always gets the whole investigation; there is no way to opt out short of `2>/dev/null` (which also eats real errors).

**Change:** two flags on the `ai` command, honored only in one-shot mode:

- `--quiet` / `-q`: suppress the investigation narration (thinking deltas, tool start/result lines, context-switch notes) even on a TTY. Errors, the destructive-approval verdict line, and `DCI_AI_STATS` output still print — those are contract, not narration.
- `--verbose`: force narration on even when stderr is piped (useful when capturing a full investigation transcript to a file).

Both flags set, or either combined with interactive mode → usage error. The default stays the current TTY auto-detection, so nothing changes for existing callers.

**Tests** (`ai_command_test.go`): flag plumbing → the `verbose` bool passed to the event loop; `--quiet` still prints `event.Error` and the approval verdict.

## F4. Currency and units in AI answer tables

**Feedback:** "Not showing unit symbol or indicating the currency on tables" — the screenshot shows an AI-composed markdown table (`Service | Jun | Jul`) with bare numbers in inconsistent scales (`936,800` next to `1,278.4K`), no currency, no unit.

**Where the numbers come from:** that table was written by the model, not by our renderer — the deterministic table path already renders money correctly when it can (`renderCellText`/`cellCurrency`/`formatMoney`, `main.go:2827-2875`; pivot money-column marking, `pivot.go:187-191`). The gaps are:

1. **The model has no rule about units.** The system prompt (`aiSystemPrompt`, `ai_prompt.go`) tells it to be concise and table-sparing, but says nothing about labeling numbers. Add a prompt rule to the stable prefix: *every numeric table or figure states its unit — currency code/symbol for cost (taken from the query result's `currency` field, never guessed), and the metric's unit for usage; use one consistent scale per column (raw, K, or M — never mixed) and say which.* Prefix change invalidates the prompt cache once per release — acceptable.
2. **Cost results usually carry currency already** — the response transform injects `currency` into the result container the model reads (`response_transform.go:53-62`) — but only when the request body declared it. When the model composes a query without an explicit currency, the API applies its default and the result carries no marker; the prompt rule above then has nothing to cite. Cheap fix in the same transform: when the request declared no currency **and the result has money-typed metric columns**, inject the API's documented default (`USD`). Usage-only results stay unstamped — a currency on unit metrics would mislabel them (review finding, first cut gated only on the request having a `config` field).
3. **Usage metrics have no unit anywhere in the query response** — a known API gap (noted at `response_transform.go:56`). The prompt rule's fallback: when the unit is genuinely unavailable, the model must say so in the header ("usage, unit as reported") rather than print bare numbers. The real fix is upstream — fold into the omni issue in F6.

**Tests:** prompt prefix contains the units rule (`ai_prompt_test.go`); transform injects `currency: USD` when the request body had none (`response_transform_test.go`).

## F5. `/mouse` — confirm the design, make the choice stick

**Feedback:** "/mouse just for wheel scrolling is the correct behavior?"

Yes — by design (AI-SPEC §3.1, D9): mouse capture defaults off because capture broke text selection/copy in some terminals; `/mouse` opts into wheel scrolling for the transcript, PgUp/PgDn always work (`ai_tui.go:166-168`, `1231-1237`). This spec keeps the default.

Two small improvements:

- **Persist the toggle:** `/mouse` currently resets every session (`mouseOn: false`, `ai_tui.go:215`). Store it in `ai_settings.json` like `/bell` (F2) and `/model`, so wheel-scrolling users choose once.
- **Reply in the thread** confirming it's intentional and why (copy/paste survival), pointing at the `/help` scrolling paragraph (`ai_tui.go:1591-1594`), which already explains it.

**Tests:** settings round-trip restores `mouseOn`.

## F6. `list-contracts` 403 — upstream issue, not a CLI change

**Feedback:** "There's an issue with the list-contracts api (not the harness)" — the screenshot shows the model's own graceful summary: `list-contracts posthog.com` returned **403 Forbidden** ("Check the active customer context and your DoiT permissions"), reading contracts needs `contractsReadOnly` / `contractsViewer` or a write-capable role.

Alfredo's doer session should plausibly hold contract read permission for a customer context it can otherwise query — this looks like a permission-model gap (or an over-strict check) in the DCI API, owned by omni, not by this repo.

**Action:** filed as [doiteng/omni#62101](https://github.com/doiteng/omni/issues/62101), together with the usage-units API gap from F4.3. CLI-side: nothing — the harness already surfaced the failure with the exact roles required, which is the behavior we want.

## F7. API-key onboarding: offer the guided setup up front

**Feedback (second thread):** "Can we store the API Key in the settings? When running `dci ai` for the first time, we could ask for it if there's no env var." Storage and the guided prompt already exist (`ai_settings.json`, guided key setup `ai_tui.go:1043-1078`), but the prompt only appears **after the user types their first question** — Alfredo tested one-shot and never saw it; maintainer verdict: "a bit clunky today."

**Change:** when the session opens with no resolvable key (`resolveAIKey` empty), start the guided key setup **immediately** — banner explaining that `/` commands work now and AI needs a key, input pre-focused in key-entry mode, Esc dismisses to a normal session (the current type-a-question trigger stays as the fallback). Typing `/` on an empty key buffer also drops straight into command entry (review finding: the setup owns the keyboard, so the "/ commands work" hint must be honored, not just printed); a slash mid-key or after a question queued the setup stays key input. One-shot keeps the current hard error (`ai_command.go:68`) — no interactive prompts on a scriptable surface — but the message should mention both `ANTHROPIC_API_KEY` and that running `dci ai` interactively offers to save the key.

**Tests:** session start with empty key → key-entry mode active, banner rendered; Esc → normal session; start with key present → unchanged.

---

## Phasing and release

All CLI changes are patch-level (`fix:`/`feat:` per commit; released in the next batched tag per the usual cadence). Suggested order — each lands independently:

| Order | Item | Files | Effort |
|---|---|---|---|
| 1 | F1 Enter accepts completion | `ai_tui.go` | XS |
| 2 | F3 `--quiet`/`--verbose` | `ai_command.go` | XS |
| 3 | F2 bell + `/bell` | `ai_tui.go`, settings | S |
| 4 | F7 key setup on open | `ai_tui.go`, `ai_command.go` | S |
| 5 | F4 units prompt rule + USD default | `ai_prompt.go`, `response_transform.go` | S |
| 6 | F5 persist `/mouse` + thread reply | `ai_tui.go`, settings | XS |
| — | F6 file omni issue (+ units API gap) | omni repo | issue only |

Decisions taken at implementation: (a) F1 Enter accepts the highlighted completion and never auto-runs it; only an exactly-typed command submits (case-sensitive, so `/QUIT⏎` corrects to `/quit ` instead of failing). (b) F2 bell defaults on, gated to turns that ran commands or lasted ≥3s. (c) F4's prompt-prefix edit accepted — one cache invalidation per release. (d) F6 and the units gap share one omni issue ([doiteng/omni#62101](https://github.com/doiteng/omni/issues/62101)).
