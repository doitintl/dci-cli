# Design spec: `dci` opens the AI session — AI as the default mode

Status: **draft for maintainer review**.
Audited at commit `d683fba` (v2.6.2 + PR #122); every claim about existing code cites the function and file it is based on.

Scope: what bare `dci` (no arguments) does, for each of the three audiences the CLI serves — humans at a terminal, scripts/CI, and AI agents. Today it prints help everywhere; this spec makes it open the interactive AI session for humans while every non-human caller keeps byte-identical behavior.

Out of scope: any change to `dci <command>` invocations, the agent output/error/exit-code contracts, the `dci ai` command itself, and free-text routing at the root (`dci "why did spend spike?"`) — the last is an explicit non-goal, argued in §5.

---

## 1. Summary and decisions

One behavior change: **bare `dci` in an interactive terminal opens the AI session** (what `dci ai` opens today). Everywhere else — pipes, CI, agents, `TERM=dumb`, `DCI_NO_TUI=1` — bare `dci` keeps printing help with exit 0, exactly as now.

| Decision | Choice | § |
|---|---|---|
| The gate | `tuiActive()` — the CLI's existing single arbiter of "a human is here" | §2 |
| Bare `dci`, human | Opens the AI session; `/quit` (or double ctrl+c) returns to the shell | §3 |
| Bare `dci`, script/CI/agent | Help, exit 0 — byte-identical to today, guaranteed by the same gate | §3 |
| `dci --help` | Help everywhere, always — cobra resolves it before the root's RunE runs | §3 |
| `dci ai` non-TTY | Unchanged: the existing "pass a question for one-shot mode" error | §4 |
| Free text at the root | **Non-goal** — `dci ai "question"` stays the only one-shot spelling | §5 |
| Escape hatches | `DCI_NO_TUI=1` (already turns the gate off) + a persisted `default: help` setting | §6 |
| Versioning | Minor bump (v2.7.0) — a "notable milestone" per AGENTS.md policy | §8 |

## 2. Why one gate suffices for three audiences

The audience split the CLI needs already exists as one function. `tuiActive()` (`tui.go:28`) is true only when **all** hold: not agent mode, stdout is a TTY, stdin is a TTY, `TERM != dumb`, `DCI_NO_TUI` unset. And agent mode itself resolves first, up front in `run()` (`main.go:276-285`), with the documented precedence (`resolveAgentMode`, `main.go:706-741`): `DCI_AGENT_MODE` → `--agent`/`--no-agent` → known agent env var → **non-TTY stdout**.

That last soft signal is what makes this spec small: a script capturing output, a CI job, a cron, an agent harness — all of them either set an agent signal or run without a TTY, so `tuiActive()` is false and bare `dci` keeps its current path with no new checks anywhere. The question the user raised — "registerAICommand doesn't check agent mode / non-TTY" — is answered by the same gate: `dci ai` already calls `tuiActive()` before opening the session (`ai_command.go:45`), and the root will go through the identical check. There is no second TTY-detection mechanism to keep in sync.

Corner cases the gate already covers correctly:

- `echo q | dci` — stdin piped, stdout TTY: stdin is not a TTY → help. A script feeding dci can never wedge an alt-screen session open.
- `dci > out.txt` from a terminal — stdout piped: agent mode auto-enables (`uaModeNonInteractive`) → help into the file.
- SSH without a PTY, git hooks, `watch dci`: no TTY → help.
- `TERM=dumb` terminals: help.

## 3. The change: root RunE routes on the gate

Today `lockToDCI()` pins the root to help: `cli.Root.RunE = cmd.Help()` with `cobra.NoArgs` (`main.go:1003-1009`). The change, in full:

```go
cli.Root.RunE = func(cmd *cobra.Command, args []string) error {
    if tuiActive() && aiDefaultEnabled(configDir) {
        return runAISession(configDir)
    }
    return cmd.Help()
}
```

with `lockToDCI(configDir)` gaining the parameter it needs (call site `main.go:367`). Properties that fall out of doing it in RunE rather than earlier in `run()`:

- **`--help` wins everywhere.** Cobra intercepts the help flag before RunE, so `dci --help` and `dci -h` print help even in a TTY — muscle memory unbroken.
- **`cobra.NoArgs` stays.** `dci nonsense` keeps failing with the unknown-command error and suggestions; nothing new is reachable by argument.
- **Everything in `run()` before command execution still happens** — config bootstrap, update check kickoff (`startUpdateCheck`, `main.go:310`), onboarding print, arg normalization, completion preflight. The session is just another command body.
- **Exit codes**: a session ended by `/quit`/ctrl+c returns nil → exit 0, same as `dci ai` today. The deferred update notice (`maybeNotifyUpdate`, `main.go:311`) prints to stderr after the alt-screen closes, which is already the shipped `dci ai` behavior.
- **Flags-only edge**: `dci -o json` (global flags, no command) currently prints help; under the gate it opens the session with the flag silently unused. Accepted as-is — the flag applies to `/`-dispatched children where relevant, and nothing worse than today's help-with-ignored-flag happens. Noted for the doc line, not engineered around.

First-run order is preserved: `ensureConfig` and `printFirstRunOnboarding` (`main.go:303, 320`) run before command execution, so a brand-new user typing `dci` sees the onboarding lines, then lands in the session. A keyless session opens into the guided key setup with `/`-commands and Esc both live (F7, AI-POLISH-SPEC) — so the no-key first run is already a designed path, not an error. A not-yet-logged-in user can run `/`-commands whose children drive OAuth exactly as they do from `dci ai` today.

## 4. Explicitly unchanged surfaces

- `dci <operation> [args]` — every existing command, flag, output format, and exit code: untouched (the root RunE only runs when no subcommand matched).
- `dci ai` and `dci ai "question"` — unchanged, including the non-TTY error pointing at one-shot mode. Rationale for the asymmetry with bare `dci`: `dci ai` in a pipe is an *explicit* request for AI that cannot be satisfied interactively, so an error with the one-shot recipe is the honest answer; bare `dci` in a pipe carries no intent, so it keeps its historical meaning (help) for every script that uses it as a probe.
- The agent contracts (output/error/destructive, exit-code taxonomy `main.go:386-400`) — no new code paths execute in agent mode.
- Shell completion (`__complete` always arrives with args) and the completion/API preflights (`main.go:376-381`) — run before command execution either way.
- The AI session itself — sessions spawned from bare `dci` and from `dci ai` are the same `runAISession(configDir)`; children always receive explicit argv, so no recursion risk.

## 5. Non-goal: free text at the root

`dci "why did spend spike?"` will **not** route to the AI. AI-SPEC §4's grammar principle — explicit routing, no heuristics, a failed command never falls through to the model — applies with more force at the root than inside the session: a typo'd command name must stay a crisp exit-2 error, not become a paid, slow, plausibly-wrong AI answer; and agents mistyping argv must get the structured error their retry logic expects. `dci ai "question"` remains the one and only one-shot spelling.

Cheap discoverability instead (P2): in a TTY only, the root's unknown-command error gains one hint line — `to ask in plain English: dci ai "…"` — alongside the existing suggestions. Agent mode keeps the bare structured error.

## 6. Escape hatches and the persisted preference

Some humans want bare `dci` to stay help — muscle memory, shared boxes, demos. Three levers, two of which already exist:

- `DCI_NO_TUI=1` — already turns the gate off wholesale (`tui.go:29`); documented as the session-wide kill switch.
- `DCI_AGENT_MODE=1` / `--agent` — already force the deterministic surface.
- New: `"default": "help"` in `ai_settings.json` (default `"session"`), read by `aiDefaultEnabled(configDir)` in the root RunE — an opt-out that survives shell restarts without disabling TUI affordances elsewhere (pickers, prompts, `dci ai` itself). Set it with `/default help` inside the session or by editing the file; the session's `/help` names it.

## 7. Risks and mitigations

- **Surprise on upgrade** — the largest real risk. Users who type `dci` expecting usage get an alt-screen session. Mitigations: the banner's `/help` line and Esc/ctrl+c affordances (shipped); the §6 opt-out; the changelog entry leading with the change; and the release notes naming `DCI_NO_TUI=1`. The fallback for every non-human context is the old behavior by construction.
- **A TTY that lies** (CI with a PTY, e.g. some pipelines allocate one): those runs would open a session that immediately blocks on input. Every major CI sets an agent env var (`agentEnvVars` heuristic) or runs with `CI=1` — worth adding `CI` to the heuristic list in this change if it is not already there — and `DCI_AGENT_MODE=1` remains the contract for exotic harnesses. This risk exists today for every `tuiActive()`-gated prompt, so the exposure is not new, only wider.
- **Session startup cost on a keyless machine**: none — the session opens without network or key (guided setup is local until a question is asked).

## 8. Delivery

- **Docs**: README leads with "run `dci`" instead of "run `dci ai`"; the usage template's first line (`brandRootCommand`/`customizeDCIUsage`, `main.go:998-1001`) says bare `dci` opens the session and `--help` prints this screen; onboarding text (`printFirstRunOnboarding`) updated the same way; help.doit.com CLI guide via the omni docs pipeline (separate PR, after release).
- **Changelog**: leads the release entry; explicitly documents the script/CI guarantee ("in pipes and CI, `dci` prints help exactly as before") and the opt-outs.
- **Versioning**: minor bump — v2.7.0.
- **Tests**: root RunE routing with the gate forced both ways (`tuiActive` is already a swappable var); `--help` in forced-TTY mode still prints help; `dci` with `DCI_AGENT_MODE=1` prints help byte-identically to today; `"default": "help"` honored; `dci nonsense` unchanged; the §5 hint appears only in TTY mode.

Phases: **P1** root routing + settings opt-out + tests; **P2** docs, onboarding, unknown-command hint, omni guide update; nothing else.

Decision asks: (a) confirm the §5 non-goal (no root free-text routing); (b) the §6 opt-out spelling — `ai_settings.json` `"default"` key plus a `/default` verb, or env-var-only; (c) should `CI` join the agent-env heuristic list in the same release (§7)?
