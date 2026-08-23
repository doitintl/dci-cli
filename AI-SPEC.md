# Design spec: `dci ai` — interactive agent mode

Status: **draft for maintainer review**.
Audited at commit `94268b6`; every claim about existing code cites the function and file it is based on.

Scope: the interactive agent mode agreed in conversation — a Claude Code-style terminal session where typed `/` commands run the existing CLI deterministically and plain-text questions go to an AI agent that drives the same CLI behind the scenes, rendering tables and charts natively. Covers the TUI, the input grammar, the agent loop, the doer/customer tenant model, and the renderer-agnostic event protocol that keeps a future web (Console) client a port rather than a rewrite.

Out of scope, deliberately: the DoiT backend agent service, Console web client, and server-side transcript storage. Those live outside this repo; this spec's only commitment to them is the event protocol (§8) and the session interface (§7.2). Also out of scope: any change to non-interactive behavior — agent mode, pipes, and CI are byte-for-byte unchanged.

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
| TUI mode | Bubble Tea **inline** renderer + `tea.Println` scrollback; no alt-screen for the session | §3 |
| Input routing | Explicit `/` prefix; plain text is always AI; a failed `/` never falls through to the model | §4 |
| Agent loop | Headless core emitting a versioned event stream; TUI is one renderer of it | §7, §8 |
| Loop location | In-process first (P2, user-supplied key), remote DoiT service later (P3) — same interface | §7.2, §12 |
| Model | `claude-opus-5`, adaptive thinking, streaming, prompt-cached catalog prefix | §7.5 |
| Rendering | Model proposes declarative view specs; our renderers draw them; numbers on screen never come from model text | §9 |
| Tenancy | Session owns the customer context; the model switches it only through an explicit, visible tool | §6 |
| New dependency | `anthropic-sdk-go` only — the Charm stack is already in the tree (go.mod) | §10 |

## 2. Naming and invocation

`ai` names the promise (answers), not the plumbing (a REPL); it matches ecosystem convention (`docker ai`, `kubectl-ai`, `gh copilot`, `q chat`) and self-explains in `dci --help`. Typed commands still working inside it does not make it a shell — that is a feature *of* the AI session, exactly as `!` shell lines are in Claude Code.

The hazard of the name is shipping it AI-less. Therefore the phases (§12): P1 builds the session shell but ships **hidden**; the command unhides only when the natural-language path works, even in its minimal user-supplied-key form. When AI credentials are absent, `dci ai` still opens and says so plainly: `/` commands work, plain text explains what is missing.

Reserved now, in the same grammar decision: `dci ai <question>` one-shot mode. Args → one-shot, no args → session. One-shot honors agent-mode conventions when stdout is not a TTY (structured output, exit codes per the error contract), so it composes with pipes and CI from day one.

Versioning: a new command group is the "notable milestone" case — the release that unhides `dci ai` is a **minor** bump per AGENTS.md policy.

## 3. Terminal UX

### 3.1 Inline, not alt-screen

The session runs the Bubble Tea program **without** `tea.WithAltScreen()`. Completed output — command results, agent text, tool cards — is emitted via `tea.Println` and lands in the terminal's native scrollback; only the bottom region (input + status line) is managed. This is the defining feel of Claude Code and Codex: users scroll and copy previous output with their terminal, and quitting leaves the transcript in place.

This is a different regime from the existing full-screen viewer (`tui_viewer.go`, F5 in TUI-SPEC.md), which is a per-command alternate-screen view. The two coexist: the session is inline; a result card can still offer "open in viewer" to jump into the F5 table for one result and return.

### 3.2 Layout

```
  ... native scrollback: transcript, tool cards, tables, charts ...

  ┌──────────────────────────────────────────────────────────┐
  │ › _                                                      │  input (bubbles/textarea, grows to ~5 lines)
  └──────────────────────────────────────────────────────────┘
    doer · acme.com                     esc interrupt · /help    status line
```

- **Input**: `bubbles/textarea`, Enter submits, Shift+Enter (where the terminal reports it) or trailing `\` continues the line. History on ↑/↓ (persisted per user under the config dir, chmod 0600).
- **Status line**: identity mode + active customer context (§6), a spinner + elapsed time while a turn is running, and Esc-to-interrupt. For a plain single-tenant customer the tenant segment is omitted entirely (§6.1).
- **Streaming**: agent text streams into the managed region as it arrives and is committed to scrollback (one `tea.Println`) when the block completes, so resizes never corrupt history.
- **Interrupt**: Esc cancels the in-flight turn (context cancellation ends the API stream and any running command subprocess). Ctrl+C on an idle prompt behaves like Claude Code: first press clears input, second exits.

### 3.3 Gate

Session mode requires a real TTY on stdin and stdout. `dci ai` in a pipe or with `TERM=dumb` degrades: with args, one-shot mode; without args, a usage error. The existing `tuiActive()` gate (tui.go) is the reference for "a human is present"; the session adds the stricter requirement that stdout also be a TTY since it owns the whole screen bottom.

## 4. Input grammar

### 4.1 The rule

`/` is deterministic; plain text is AI. There is **no** bare-text command detection — an earlier design routed bare `anomalies list` via catalog lookup, rejected because its failure modes are bad in both directions (a typo'd command silently becomes a model call; NL that starts with a command word executes something). With the prefix, intent is explicit and `/help` states the whole grammar in one line.

Corollary: a `/` line that matches nothing **never** falls through to the model. It prints the closest catalog entries (reusing the suggestion machinery cobra already gives the CLI) and stops.

### 4.2 The `/` namespace

One namespace, two populations, resolved in this order:

1. **Session commands** (reserved verbs): `/customer` (§6.2), `/clear` (new conversation), `/compact` (summarize history in place), `/export` (write transcript to a file), `/model` (P3, if the backend offers a choice), `/help`, `/quit`. A build-time check asserts no API command group shadows a reserved verb.
2. **The CLI catalog, verbatim**: `/anomalies list --filter x` ≡ `dci anomalies list --filter x`. The parity rule is absolute — same argv semantics after the slash, so every existing doc and shell-history snippet transfers by prepending `/`. Implementation is slash-strip → the same argv the outer CLI would see → dispatch (§7.4).

Space is reserved (resolution order slot between 1 and 2) for **user-defined slash commands** — saved prompts/queries in a config file, the `dci` analog of Claude Code custom commands. Not designed here; the parser just must not preclude it.

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
- **`/customer <name|id>`** switches context: name-resolved and completed via the existing cache, validated via `validateCustomerContextValue` (main.go), scoped to whatever the API authorizes (which handles partners with zero special-casing). `/customer` with no argument opens the F1-style picker (tui_picker.go).
- A doer with no context set gets the picker on session start — the interactive upgrade of `maybeHintDoerContext` (main.go).

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

Two implementations, one per phase: **local** (P2 — in-process loop against the Claude API, user-supplied key) and **remote** (P3 — SSE/WebSocket client of the DoiT agent service). The TUI and the one-shot runner never know which they hold. Moving billing behind the DoiT backend and moving the loop server-side are the same milestone, by construction.

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

Both slash dispatch and `run_dci_command` execute via **subprocess re-exec**: `os.Args[0]` with the target argv, `DCI_AGENT_MODE=1` (honored first in `resolveAgentMode`, main.go), and the session's `-D <context>`. Rationale over in-process cobra re-execution: pflag/viper/restish package-level state does not persist across invocations, `os.Exit` in error paths cannot kill the session, stdout capture is trivial, and per-command cost is invisible against network-bound API calls. The child inherits the parent's config dir and token cache, so auth is shared. The existing agent-mode output contract (`shapeResponseBody` and the output guard, output_contract.go) is exactly the model-facing shape; exit codes and stderr JSON map to structured tool errors per error_contract.go.

### 7.5 Model and API usage

- **Model**: `claude-opus-5`, adaptive thinking, streamed responses. SDK: `anthropic-sdk-go` (the only new dependency).
- **Prompt layout for caching**: stable prefix first — system prompt (mode-specific, §6.2, but fixed for the session) + serialized catalog — with a cache breakpoint after it; volatile content (active tenant, date, conversation) after the breakpoint. The catalog's token size must be **measured** at implementation time (`count_tokens`); if it blows the budget, `search_commands` + a summarized catalog is the fallback. Verified via `cache_read_input_tokens` in dogfooding.
- **Loop**: the SDK's tool-runner helper if its per-turn hooks accommodate the approval gate (§7.6); otherwise the manual loop — it is small, and the approval/interrupt/event plumbing is the actual work either way.
- **Ceilings**: max turns per question and a max-token guard, with an event (§8) when a ceiling is hit, so a runaway loop is visible and bounded. Exact numbers are implementation-time tunables.
- **P2 credentials**: `ANTHROPIC_API_KEY` (or config-file equivalent), explicitly labeled experimental. P3 replaces this with the DoiT-authenticated backend (open question Q1).

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

Dependencies: **add `anthropic-sdk-go`; add nothing else.** bubbletea/bubbles/lipgloss/huh/ntcharts/asciigraph are already direct dependencies (go.mod). The P3 remote session adds no dependency (SSE over net/http).

## 11. Security and privacy

- Authorization is the API's, via the user's OAuth token — unchanged, including for every agent-initiated call (§6). The session never widens access; it only spends it.
- **Data flow to the model is the new surface**: tool results (customer cost data) enter model context. P2 sends them to the Anthropic API under the user's own key, which is why P2 stays experimental and doer-dogfood-first; the governance posture for customers (sub-processor, DPA, retention) is a P3/backend decision — open question Q3.
- Tokens and secrets never enter model context: the executor passes credentials via the child's environment/config exactly as the CLI does today, and transcripts/`/export` contain events, not env.
- The User-Agent `mode=` token (uaMode, main.go) gains an `ai` value so agent-session traffic is distinguishable in analytics from both `interactive` and external-`agent` traffic.
- Local persistence (history, exports) lives under the config dir with 0600, like the token cache and customerContext file today.

## 12. Phases

| Phase | Ships | Gate |
|---|---|---|
| **P1** | The session shell, hidden: inline TUI, `/` grammar + catalog completion, slash dispatch, status line, `/customer` + picker, history. Zero AI dependency. | Hidden command; doer dogfood |
| **P2** | The local loop: NL path, tool cards, approvals, view specs, slash-results-into-context, one-shot mode. Requires user-supplied key; labeled experimental. `ai_events.go` schema reviewed before this code starts. | Still hidden or key-gated; doer + friendly-partner dogfood |
| **P3** | Remote session against the DoiT agent service: DoiT-authenticated, DoiT-billed, no user key. Unhide `dci ai`, README + docs, **minor version bump**. | Maintainer + backend readiness (Q1–Q3) |

Each phase is independently shippable to its audience; P1 is pure UI with no external dependencies and immediately useful to doers as a context-aware shell.

## 13. Web replication path (informative)

What P1–P3 leave behind for a Console client: the versioned event protocol (§8), ANSI-free view specs (§9), a session interface with a remote implementation (§7.2), and — once the loop is server-side — conversations that can be persisted, resumed across surfaces, and shared (transcript access inherits tenant rules: a doer transcript containing a customer's data is governed like the data). The slash catalog becomes a ⌘K command palette from the same catalog JSON. None of that work lives in this repo beyond keeping the protocol clean.

## 14. Open questions for the maintainer

| # | Question | Blocks |
|---|---|---|
| Q1 | Token billing & endpoint: user Anthropic keys are a dogfood hack — is the P3 path a DoiT backend proxy authenticated with the existing Console OAuth? Owned by which team? | P3 |
| Q2 | Model policy: default `claude-opus-5` — confirm; is a cheaper model wanted for one-shot/CI usage, and is `/model` exposed at all? | P2 defaults |
| Q3 | Data governance: customer data in model context — sub-processor/DPA/retention posture for customer-facing GA. | P3 GA |
| Q4 | Transcript persistence server-side (resume/share/web) — in scope for the agent service v1? | P3/web |
| Q5 | User-defined slash commands (saved prompts/queries): worth a slot in the P-plan or explicitly later? | grammar only reserves space |
| Q6 | Catalog token budget: if measurement (§7.5) shows the full catalog too large for the cached prefix, accept the `search_commands` fallback or invest in a condensed catalog? | P2 |
| Q7 | One-shot in CI for customers pre-P3 (needs a key): support at all, or one-shot lands with P3? | P2 scope |
