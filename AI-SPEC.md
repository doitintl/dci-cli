# Design spec: `dci ai` — interactive agent mode

Status: **draft for maintainer review**.
Audited at commit `94268b6`; every claim about existing code cites the function and file it is based on.

Scope: the interactive agent mode agreed in conversation — a Claude Code-style terminal session where typed `/` commands run the existing CLI deterministically and plain-text questions go to an AI agent that drives the same CLI behind the scenes, rendering tables and charts natively. Covers the TUI, the input grammar, the agent loop, the doer/customer tenant model, and the renderer-agnostic event protocol that keeps other renderers (a future Console client, §13) possible. The open questions from the first draft are settled — see the decision log (§14).

Out of scope: a hosted DoiT agent service and server-side transcript storage — **decided against** (D1, D4, §14); a Console web client remains conceivable over the same event protocol (§13) but is not planned. Also out of scope: any change to non-interactive behavior — agent mode, pipes, and CI are byte-for-byte unchanged.

---

## 1. Summary

One new command group, `dci ai`:

- `dci ai` — opens an interactive session in the terminal: inline (not alt-screen), input pinned at the bottom, output flowing into native scrollback.
- `dci ai "why did acme's spend spike in July?"` — one-shot: runs the same agent loop non-interactively, prints the answer, exits. The scriptable/CI surface.

Inside the session, one input grammar with no heuristics:

| Input | Routing |
|---|---|
| `/anomalies list --filter ...` | Deterministic: the existing CLI command, verbatim after the slash |
| `/customer acme.com`, `/clear`, `/help`, `/quit`, ... | Deterministic: session-level (app) commands |
| plain text | The AI agent |

Decision table (rationale in the cited sections):

| Decision | Choice | § |
|---|---|---|
| Command name | `dci ai` (not `shell`/`chat`); never ships without a working AI path | §2 |
| TUI mode | Stable frame: alt-screen + internal scrolling viewport (D9 — supersedes the draft's inline mode after dogfood) | §3 |
| Input routing | Explicit `/` prefix; plain text is always AI; a failed `/` never falls through to the model | §4 |
| Agent loop | Headless core emitting a versioned event stream; TUI is one renderer of it | §7, §8 |
| Loop location | In-process, permanently: the user's machine holds the key and runs the loop (D1) | §7.2, §12 |
| Model | `claude-opus-5` default, user-selectable via `/model` + config (D2); adaptive thinking, streaming, prompt-cached catalog prefix | §7.5 |
| Rendering | Model proposes declarative view specs; our renderers draw them; numbers on screen never come from model text | §9 |
| Tenancy | Session owns the customer context; the model switches it only through an explicit, visible tool | §6 |
| New dependency | `anthropic-sdk-go` only — the Charm stack is already in the tree (go.mod) | §10 |

## 2. Naming and invocation

`ai` names the promise (answers), not the plumbing (a REPL); it matches ecosystem convention (`docker ai`, `kubectl-ai`, `gh copilot`, `q chat`) and self-explains in `dci --help`. Typed commands still working inside it does not make it a shell — that is a feature *of* the AI session, exactly as `!` shell lines are in Claude Code.

The hazard of the name is shipping it AI-less. Therefore the phases (§12): P1 builds the session shell but ships **hidden**; the command unhides only when the natural-language path works with a user-supplied key (the permanent mechanism, D1). When AI credentials are absent, `dci ai` still opens and says so plainly: `/` commands work, plain text explains what is missing.

Reserved now, in the same grammar decision: `dci ai <question>` one-shot mode. Args → one-shot, no args → session. One-shot honors agent-mode conventions when stdout is not a TTY (structured output, exit codes per the error contract), so it composes with pipes and CI from day one.

Versioning: a new command group is the "notable milestone" case — the release that unhides `dci ai` is a **minor** bump per AGENTS.md policy.

## 3. Terminal UX

### 3.1 Stable frame (D9 — supersedes the first draft's inline mode)

The session runs in the **alternate screen** with a stable frame: a one-line header, the transcript in a `bubbles/viewport`, and the input + status pinned at the bottom. The frame never scrolls; the transcript scrolls *inside* the viewport (mouse wheel, PgUp/PgDn, ctrl+u/ctrl+d), auto-following the bottom until the user scrolls up. The first draft specified inline scrollback here; dogfood preferred the stable frame (D9), accepting the trade: native terminal scrollback/selection is exchanged for a frame that never jumps, with `/export` covering transcript retention and `/clear` resetting it.

The existing full-screen viewer (`tui_viewer.go`, F5 in TUI-SPEC.md) remains a separate per-command program; the two do not nest.

### 3.2 Layout

```
  dci ai · Cloud Intelligence™                                   header (fixed)
  ┌──────────────────────────────────────────────────────────┐
  │ ... transcript: chat, tool cards, tables, charts ...     │  viewport (scrolls inside;
  │                                                          │  wheel / PgUp / PgDn)
  └──────────────────────────────────────────────────────────┘
  › _                                                            input (fixed)
    doer · acme.com                     esc interrupt · /help    status line (fixed)
```

- **Input**: `bubbles/textarea`, Enter submits, Shift+Enter (where the terminal reports it) or trailing `\` continues the line. History on ↑/↓ (persisted per user under the config dir, chmod 0600).
- **Status line**: identity mode + active customer context (§6), a spinner + elapsed time while a turn is running, and Esc-to-interrupt. For a plain single-tenant customer the tenant segment is omitted entirely (§6.1).
- **Streaming**: agent text streams into the viewport tail as it arrives and is committed as a rendered transcript block when it completes; the viewport auto-follows the bottom unless the user has scrolled up.
- **Interrupt**: Esc cancels the in-flight turn (context cancellation ends the API stream and any running command subprocess). Ctrl+C on an idle prompt behaves like Claude Code: first press clears input, second exits.

### 3.3 Gate

Session mode requires a real TTY on stdin and stdout. `dci ai` in a pipe or with `TERM=dumb` degrades: with args, one-shot mode; without args, a usage error. The existing `tuiActive()` gate (tui.go) already encodes exactly this definition of "a human is present" — not agent mode, stdout **and** stdin are TTYs, `TERM != "dumb"`, with the `DCI_NO_TUI` escape hatch — so the session reuses it verbatim rather than defining its own check.

## 4. Input grammar

### 4.1 The rule

`/` is deterministic; plain text is AI. There is **no** bare-text command detection — an earlier design routed bare `anomalies list` via catalog lookup, rejected because its failure modes are bad in both directions (a typo'd command silently becomes a model call; NL that starts with a command word executes something). With the prefix, intent is explicit and `/help` states the whole grammar in one line.

Corollary: a `/` line that matches nothing **never** falls through to the model. It prints the closest catalog entries (reusing the suggestion machinery cobra already gives the CLI) and stops.

### 4.2 The `/` namespace

One namespace, two populations, resolved in this order:

1. **Session commands** (reserved verbs): `/customer` (§6.2), `/clear` (new conversation), `/compact` (summarize history in place), `/export` (write transcript to a file), `/model` (switch the session model; D2), `/help`, `/quit`. A build-time check asserts no API command group shadows a reserved verb.
2. **The CLI catalog, verbatim**: `/anomalies list --filter x` ≡ `dci anomalies list --filter x`. The parity rule is absolute — same argv semantics after the slash, so every existing doc and shell-history snippet transfers by prepending `/`. Implementation is slash-strip → the same argv the outer CLI would see → dispatch (§7.4).

**User-defined slash commands** — saved prompts/queries in a config file, the `dci` analog of Claude Code custom commands — ship in P2 (D5), occupying the resolution slot between 1 and 2: app verbs → user-defined → catalog. A user-defined name expands to either a parameterized NL prompt (sent to the agent) or a fixed command line (dispatched deterministically); definitions live in a config file under the config dir, with the same completion popup treatment. Detailed format is P2 implementation scope.

### 4.3 Completion

Typing `/` opens a filterable popup listing every command with its one-line summary — all served from the existing command catalog (`buildCommandCatalog`, command_catalog.go, including `localCatalogEntries` for config-plane commands). Flag completion and — the differentiator — **value** completion (customer names, report names) come from the same hidden `__complete` machinery the shell completions use (main.go's completion normalization) and the name cache (name_completion.go). Session commands appear in the same popup, listed first.

### 4.4 Slash results join the conversation

When the user runs a `/` command, its structured result (§7.4's contract-shaped JSON, size-capped per §7.6) is appended to the model's conversation history as if it were a tool result, tagged with the tenant it ran against. This is the interaction that fuses the two modes: run `/reports run monthly-spend`, see the table, then ask *"why is the third row so high?"* — the model already has the data. Oversized results are truncated for the model with an explicit marker; the on-screen render is never truncated.

## 5. Precedents (context, not requirements)

The converged pattern this follows: Claude Code and Gemini CLI (Ink, inline scrollback), Codex CLI (ratatui inline viewport + insert-before — proof the pattern is framework-independent), Crush (the Bubble Tea reference for structure), and the domain-specific agents — Amazon Q Developer CLI, kubectl-ai, Docker Gordon — which are precisely "vendor CLI + NL agent driving it".

What none of them have is a CLI that was already agent-ready: a machine-readable command catalog with parameter shapes (command_catalog.go), deterministic agent-mode output with field projection (`agentMode`, output_contract.go), a destructive-operation classifier (destructive_contract.go), structured errors with hints (error_contract.go), and shipped agent skills (skill_management.go). `dci ai` is a harness and a UI over contracts that already exist; that is the build-vs-buy argument in one sentence.

## 6. Identity and tenant model

Three effective modes, all derived from state the CLI already has — no new concept is introduced:

| Mode | Who | Tenants reachable | Source of truth |
|---|---|---|---|
| Customer | most users | their own | OAuth token; API enforces |
| Partner / distributor | resellers | their customers' | same mechanism — just more contexts authorized |
| Doer | @doit.com | all | `cachedTokenIsDoer()` (main.go) |

The active tenant is the existing persisted `customerContext` (`customerContextPath`/`readCustomerContext`, main.go) with the `-D/--customer-context` per-invocation override. **Enforcement stays server-side** — the API 403s anything the token can't reach, exactly as today. Everything below is UX and prompt-shaping, not a security boundary.

### 6.1 In the session

- **Status line** always shows the mode and active context for doers/partners (`doer · acme.com`). A single-tenant customer sees no tenant vocabulary anywhere — status line, prompts, or `/help`.
- **`/customer <name|id>`** switches context, validated via `validateCustomerContextValue` (main.go) and persisted through the same write path as `dci customer-context set`; the API authorizes whatever the token allows (which handles partners with zero special-casing). `/customer` with no argument shows the current context. *(Implementation note: a fuzzy picker needs a customer list source, and none exists — the name cache in name_resolution.go is resource-scoped per tenant, not a tenant list. A picker follows in P2 only if the API offers a listable customers source.)*
- A doer with no context set sees it called out in the status line, with `/customer` as the fix — the session upgrade of `maybeHintDoerContext` (main.go).

### 6.2 Agent rules (the important part)

The doer threat model is not authorization — everything a doer queries is authorized. It is **silent data mixing**: an answer that quietly blends two customers' numbers is an incident even when every call was legal. Hence:

1. The active context is **session state owned by the TUI**, injected into every command execution (§7.4). It is not a free parameter the model fills in.
2. Cross-tenant work (doers/partners only) goes through an explicit `set_customer_context` tool (§7.3) that renders as a visible, labeled event in the transcript — same treatment class as destructive operations.
3. Every tool result and slash result carries a tenant tag, in the protocol (§8) and on the rendered card. The system prompt requires per-tenant attribution in any answer spanning more than one.
4. On a mid-conversation context switch, prior results from the other tenant remain in model context by design (comparisons are the point) — mitigated by rules 3's tagging. If mixing proves to be a real problem in dogfooding, escalate to prompting for a fresh conversation on switch; not built preemptively.
5. Customer mode omits all of this: no tenant tools, no tenant prompt sections. Fewer concepts, fewer hallucinated flags.

Structured errors close the loop: a 403 or missing-context error reaches the model via the error contract (`structuredErrorForExecution`, error_contract.go — which already includes `activeCustomerContext()` and hints), so the agent explains or asks rather than blindly retrying.

## 7. Agent architecture

### 7.1 Headless core

The loop, prompts, tool definitions, and tenant rules form a core with **zero Bubble Tea/Lipgloss imports**, communicating only through the §8 event stream. In Bubble Tea terms this is nearly free — events are the `tea.Msg` types the TUI would need anyway — and it is the entire difference between the web client consuming a protocol and reverse-engineering a terminal app.

### 7.2 The session interface

```go
// ConversationSession is the seam between renderers and the agent.
type ConversationSession interface {
    Send(ctx context.Context, input UserInput) error // NL turn, slash-result injection, approval response
    Events() <-chan AgentEvent                       // the §8 stream
    Close() error
}
```

One planned implementation: **local** — an in-process loop against the Claude API using the user's own key (D1; there is no DoiT agent service in this plan). The interface still earns its keep: it enforces the core/renderer split that the TUI and one-shot runner share, and it keeps a remote implementation *possible* should a hosted service ever be revisited — but none is planned, and no code should anticipate one beyond honoring this seam.

### 7.3 Tool surface

Small and closed:

| Tool | Does | Notes |
|---|---|---|
| `run_dci_command(argv)` | Executes one CLI command (§7.4), returns contract-shaped JSON | The workhorse; destructive commands gate on approval (§7.6) |
| `search_commands(query)` | Searches the catalog | Only if the full catalog proves too large for the cached prefix (§7.5); measure first |
| `set_customer_context(customer)` | Switches the session tenant | Doer/partner sessions only; emits a visible event (§6.2) |
| `render_view(spec)` | Proposes a table/chart view over a prior result | Spec schema in §9; data is referenced, never restated |

Tool schemas for `run_dci_command` are grounded by embedding the catalog (paths, summaries, parameter shapes — already emitted by command_catalog.go) in the system prompt, so definitions stay in lockstep with the API with no hand-maintained list.

### 7.4 Command execution

Both slash dispatch and `run_dci_command` execute via **subprocess re-exec** of this binary with the target argv. The child has no TTY, so the CLI's own interactive prompts (the F1 zero-argument picker and F2 ambiguity selection, TUI-SPEC) can never fire in it; the session provides them itself (ai_picker.go): before dispatching a resolvable command it detects the two cases against the name cache using the CLI's own matcher and gates (`--id`, `DCI_NO_RESOLVE`, ID-shaped input) and opens a filter-as-you-type selection in the frame, then dispatches with the chosen ID. An absent cache degrades to the child's own error. The model-facing path needs no picker: agent mode already returns the structured `NAME_AMBIGUOUS` error with candidates, which the model self-corrects from. Output mode differs by consumer: `run_dci_command` (P2) runs with `DCI_AGENT_MODE=1` (honored first in `resolveAgentMode`, main.go) because its output feeds the model; a user's slash dispatch renders for the human, so it runs without agent mode (the child's piped, `DCI_NO_TUI` stdio already keeps it deterministic) — when P2 injects slash results into model context (§4.4), that injection applies the agent-mode shaping, not the on-screen render. Both inherit the session's customer context via the shared config dir (the `-D` flag covers a per-call override). Rationale over in-process cobra re-execution: pflag/viper/restish package-level state does not persist across invocations, `os.Exit` in error paths cannot kill the session, stdout capture is trivial, and per-command cost is invisible against network-bound API calls. The child inherits the parent's config dir and token cache, so auth is shared. The existing agent-mode output contract (`shapeResponseBody` and the output guard, output_contract.go) is exactly the model-facing shape; exit codes and stderr JSON map to structured tool errors per error_contract.go.

### 7.5 Model and API usage

- **Model**: `claude-opus-5` by default, adaptive thinking, streamed responses; user-selectable per session via `/model` and persistently via config (D2). The system prompt is written model-agnostically and validated against the selectable set. SDK: `anthropic-sdk-go` (the only new dependency).
- **Prompt layout for caching**: stable prefix first — system prompt (mode-specific, §6.2, but fixed for the session) + serialized catalog — with a cache breakpoint after it; volatile content (active tenant, date, conversation) after the breakpoint. The catalog's token size must be **measured** at implementation time (`count_tokens`); if it blows the budget, `search_commands` + a summarized catalog is the fallback. Verified via `cache_read_input_tokens` in dogfooding.
- **Loop**: a manual streaming loop (ai_session.go) — the approval pause, per-delta event emission, and cancellation made the SDK tool runner's hooks a poor fit; the loop itself is ~100 lines and fully tested against a scripted transport.
- **Ceilings**: max turns per question and a max-token guard, with an event (§8) when a ceiling is hit, so a runaway loop is visible and bounded. Exact numbers are implementation-time tunables.
- **Credentials**: a user-supplied Anthropic API key — `ANTHROPIC_API_KEY` env var or a config-file equivalent under the config dir (0600) — is the **permanent** mechanism (D1), not a dogfood stopgap. `dci ai` without a key opens, runs `/` commands, and explains how to configure one; there is no DoiT-billed path.

### 7.6 Approvals and limits

- A `run_dci_command` whose argv resolves to a destructive operation (`isDestructiveCommand`/`ensureDestructiveOperations`, destructive_contract.go) pauses the loop and emits `approval_request`; the TUI renders a styled confirm (the F3 pattern, tui.go). Decline returns the existing structured refusal (`destructiveConfirmationError`) as the tool result — the model sees a first-class "user declined", not a generic error.
- Tool results entering model context are size-capped (tunable, on the order of tens of KB) with explicit truncation markers; the local render of the same result is never truncated.
- One-shot mode auto-declines destructive operations unless `--yes` was passed, mirroring the CLI's existing contract.

## 8. Event protocol

The single artifact the TUI, the one-shot runner, the future agent service, and the future web client all program against. Versioned JSON (`"v": 1`); written down and reviewed **before** P2 code exists.

| Event | Payload (sketch) | Notes |
|---|---|---|
| `turn_started` | `{turn_id}` | |
| `text_delta` | `{text}` | Streamed narration; markdown |
| `tool_call_started` | `{call_id, tool, argv?, customer}` | Renders as an in-progress card |
| `tool_result` | `{call_id, ok, data?, error?, view?, customer, truncated}` | `data` is contract-shaped JSON; `view` per §9; `error` per error_contract.go's `structuredError` |
| `approval_request` | `{call_id, kind: destructive\|context_switch, summary, argv}` | Loop is paused; answered via `Send` |
| `context_switched` | `{from, to, by: user\|agent}` | Also emitted for `/customer` |
| `limit_reached` | `{kind: turns\|tokens}` | §7.5 ceilings |
| `error` | `structuredError` | Session-level failures |
| `turn_done` | `{turn_id, usage}` | Token/cost accounting when available |

Rules: **no ANSI or terminal formatting anywhere in the protocol** — the moment escapes leak in, web is a rewrite. Client-executed tools (e.g. a future "save CSV locally") are representable — `approval_request`/`tool_result` flow works in both directions — even though v1 has none. Slash commands surface as `tool_call_started`/`tool_result` with `by: user` provenance, so a transcript replays identically on any renderer.

## 9. Rendering

The invariant: **numbers on screen never come from model prose.** Commands return structured JSON; renderers draw it; the model narrates and proposes views.

- **Tool cards**: one per call — command line, tenant badge, status, elapsed — collapsed to a summary line once complete, expandable. The result table renders with the existing table renderer; charts with the existing charts chapter (charts.go — ntcharts/asciigraph are already direct dependencies, go.mod).
- **View specs** (`render_view` and `tool_result.view`): declarative — `{type: table, columns, sort}` or `{type: bar|line, x, series}` — referencing result data by `call_id`. The terminal draws them with the stack above; the web client draws the identical spec with a real charting library (where a cost product will genuinely look better).
- **Narration**: model text renders as markdown via glamour (in the tree at v0.6.0 via restish; the F8 version-skew caveat in TUI-SPEC.md §2.3 applies unchanged).
- **`open in viewer`**: a card affordance to open that result in the F5 interactive table (tui_viewer.go) and return to the session.

## 10. Files and dependencies

Per the chapter-per-file convention (AGENTS.md), new siblings in `package main`, each with a matching `_test.go`:

| File | Chapter |
|---|---|
| `ai_session.go` | `ConversationSession`, the local loop, ceilings, approval plumbing |
| `ai_events.go` | Event types + JSON schema (the §8 contract), version constant |
| `ai_tools.go` | Tool definitions from the catalog, subprocess executor, result shaping |
| `ai_prompt.go` | System prompt assembly (mode-specific), catalog serialization, cache layout |
| `ai_tui.go` | The inline Bubble Tea program: input, status line, cards, streaming |
| `ai_slash.go` | `/` parsing, reserved verbs, completion popup, history |
| `ai_command.go` | `dci ai` cobra wiring: session vs one-shot, gates, hidden flag |

Dependencies: **add `anthropic-sdk-go`; add nothing else.** bubbletea/bubbles/lipgloss/huh/ntcharts/asciigraph are already direct dependencies (go.mod).

## 11. Security and privacy

- Authorization is the API's, via the user's OAuth token — unchanged, including for every agent-initiated call (§6). The session never widens access; it only spends it.
- **Data flow to the model is the new surface**: tool results (customer cost data) enter model context. Under D1, model traffic flows directly from the user's machine to the Anthropic API under the user's **own** key and agreement — DoiT is not the processor for it. DoiT's posture is to rely on its existing AI terms for the feature itself (D3); legal verifies that coverage before customer-facing GA (§12 P3 gate). The docs must state plainly that question data and result data are sent to the model provider under the user's key.
- Tokens and secrets never enter model context: the executor passes credentials via the child's environment/config exactly as the CLI does today, and transcripts/`/export` contain events, not env.
- The User-Agent `mode=` token (uaMode, main.go) gains an `ai` value so agent-session traffic is distinguishable in analytics from both `interactive` and external-`agent` traffic.
- Local persistence (history, exports) lives under the config dir with 0600, like the token cache and customerContext file today — and it is the **only** persistence: transcripts are never stored server-side (D4).

## 12. Phases

| Phase | Ships | Gate |
|---|---|---|
| **P1** | The session shell, hidden: inline TUI, `/` grammar + catalog completion, slash dispatch, status line, `/customer` set/show, history. Zero AI dependency. **Implemented** in `ai_command.go` / `ai_tui.go` / `ai_slash.go`. | Hidden command; doer dogfood |
| **P2** | The loop: NL path, tool cards, approvals, slash-results-into-context, one-shot mode (D7), user-defined slash commands (D5), `/model` + model config (D2), catalog token measurement (D6, surfaced by `/model`). Requires a user-supplied key (the permanent mechanism, D1). **Implemented** in `ai_events.go` / `ai_session.go` / `ai_tools.go` / `ai_prompt.go` + the TUI wiring; narration renders via glamour, agent tool cards show the model-facing output — richer view specs (§9's `render_view`) follow with P3 polish. | Still hidden; doer + friendly-partner dogfood |
| **P3** | GA hardening — **implemented**: `dci ai` unhidden (help + first-run onboarding list it; excluded from the machine catalog so external agents don't nest agents), guided in-session key setup with the D3 disclosure shown before the user commits (the triggering question resumes after saving), disclosure repeated in `--help`, `/help`, and the README's AI Assistant section. Remaining before release: legal verification of AI-terms coverage (D3) and the release tag itself (**minor version bump** per AGENTS.md). | Maintainer sign-off + legal verification of existing AI-terms coverage (D3) |

Each phase is independently shippable to its audience; P1 is pure UI with no external dependencies and immediately useful to doers as a context-aware shell.

## 13. Web replication path (informative)

With D1 (no hosted service) and D4 (local-only transcripts), the web story narrows deliberately. What stays portable: the versioned event protocol (§8) and the ANSI-free view specs (§9) — a future Console client would render the same events, and the slash catalog would become a ⌘K command palette from the same catalog JSON. What a web client would have to solve for itself: a credential story (it cannot use a key on the user's machine) and history (it starts with none — cross-device resume and sharing are out of scope by D4). In practice, revisiting web means revisiting D1 and D4 first. The CLI-side commitment is unchanged and cheap: keep the protocol clean.

## 14. Decision log

The seven open questions from the first draft were decided by the maintainer on 2026-08-24 (Q# → D#):

| # | Decision | Consequences in this spec |
|---|---|---|
| D1 | **Token billing: user-supplied Anthropic keys, permanently.** No DoiT agent service or proxy. | The local loop is the final architecture (§7.2); P3 becomes GA hardening, not a backend migration (§12); a web client would need its own credential story (§13). |
| D2 | **Model: `claude-opus-5` default, user-selectable** via `/model` and config. | `/model` is a real session command from P2 (§4.2); system prompt written model-agnostically and validated against the selectable set (§7.5). |
| D3 | **Governance: rely on existing DoiT AI terms.** | With D1, model traffic is a direct user↔Anthropic relationship under the user's own key and agreement; legal verifies existing coverage before customer-facing GA, and the docs disclose the data flow (§11, §12 P3 gate). |
| D4 | **Transcripts: local-only, indefinitely.** No server-side persistence, resume, or sharing. | History and `/export` live under the config dir only (§11); web continuity is out of scope (§13). |
| D5 | **User-defined slash commands ship in P2.** | Config-file saved prompts/commands with the reserved resolution slot and completion treatment (§4.2); detailed format is P2 implementation scope. |
| D6 | **Catalog: full catalog in the cached prefix, measured in P2**; `search_commands` is the ready fallback if it proves too large. | §7.5 unchanged — now a plan rather than a question. |
| D7 | **One-shot ships in P2** alongside the interactive loop, same key requirement. | §12 P2 scope; under D1 there is no separate customer-CI story — the key requirement *is* the permanent model. |
| D8 | **Workload Identity Federation: recorded, not implemented** (researched 2026-08-24 at the maintainer's request). Anthropic WIF (GA) exchanges short-lived OIDC JWTs for API tokens via federation rules — viable in two tiers: **(a)** cheap — relax the key gate so ambient SDK credentials (the `ANTHROPIC_FEDERATION_RULE_ID`/`ANTHROPIC_ORGANIZATION_ID`/`ANTHROPIC_SERVICE_ACCOUNT_ID`/`ANTHROPIC_IDENTITY_TOKEN[_FILE]` env vars) work in CI without a static key; **(b)** a doer identity bridge — register DoiT's IdP as an issuer on DoiT's Anthropic org so `dci ai` could exchange the DoiT OAuth token doers already hold: key-less doers with **no proxy service**, but it makes DoiT's org the billed party for doer usage (a deliberate partial reopen of D1) and needs Console admin setup (rule, workspace spend limits) plus an issuer/claims check with whoever owns DoiT's IdP. Not viable for the general customer-laptop case — no ambient OIDC issuer there; BYO key stands. | No code change yet. Tier (a) is a one-conditional change in `ai_session.go` when wanted; tier (b) is an org/IdP decision outside this repo. |
| D9 | **Stable frame TUI** (2026-08-24, from dogfood): the session runs in the alternate screen with fixed chrome — header, input, status — and the transcript scrolling *inside* a viewport (mouse wheel / PgUp / PgDn), superseding the first draft's inline-scrollback mode (§3). | Trade-off accepted knowingly: native terminal scrollback/selection is exchanged for a stable frame; `/export` covers transcript retention. §3 updated. |
