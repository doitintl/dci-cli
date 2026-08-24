package main

// P2 of AI-SPEC (§7.3–§7.4, §7.6): the agent's tool surface and executor.
// run_dci_command re-execs this binary in agent mode so the model reads the
// same deterministic output contract external agents get; the CLI's own
// destructive contract is the approval classifier — a blocked call surfaces
// as DESTRUCTIVE_REQUIRES_CONFIRMATION (exit 30), the session asks the
// human, and an approved retry appends --yes. set_customer_context is the
// explicit, visible tenant switch (§6.2). Kept in a sibling file per the
// AGENTS.md chapter-split guidance.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	aiToolRunCommand      = "run_dci_command"
	aiToolSetCustomer     = "set_customer_context"
	aiToolResultByteLimit = 32 * 1024 // per tool result entering model context (§7.6)
)

// aiToolCommandTimeout bounds one run_dci_command child. Without it a wedged
// child (a stuck network call, or anything that blocks waiting for input that
// will never come) hangs the whole turn until the user presses Esc. Generous
// enough for a full --all pagination sweep; a var so tests shrink it.
var aiToolCommandTimeout = 2 * time.Minute

// aiToolQueryTimeout is the ceiling for query children specifically (F4,
// AI-FINOPS-SPEC §3.3): cold CSP queries legitimately run 58–97s and the
// API's own edge timeout (a retryable 524) arrives at ~125s — both past or
// inside the 2-minute kill. Queries are the only measured >2-minute
// legitimate children; everything else keeps the fast failure.
var aiToolQueryTimeout = 5 * time.Minute

func aiToolTimeoutFor(argv []string) time.Duration {
	if len(argv) > 0 && argv[0] == "query" {
		return aiToolQueryTimeout
	}
	return aiToolCommandTimeout
}

// aiDeniedCommands are argv[0] values run_dci_command refuses: they open
// browsers, mutate the binary, or recurse into the session. The model is told
// to ask the user instead.
var aiDeniedCommands = map[string]string{
	"ai":         "the session cannot recurse into itself",
	"login":      "login opens a browser OAuth flow — ask the user to run dci login themselves",
	"logout":     "logout drops the user's credentials — ask the user to run it themselves",
	"update":     "self-update replaces the binary — ask the user to run dci update themselves",
	"completion": "shell completion scripts are not useful to you",
}

// aiToolOutcome is what one tool execution produced, before shaping into a
// protocol event and a model-facing tool result.
type aiToolOutcome struct {
	Data          string
	IsError       bool
	Truncated     bool
	NeedsApproval bool   // destructive contract blocked the call (exit 30)
	Summary       string // approval prompt text, from the CLI's own error message
}

// aiRunCommandInput is run_dci_command's input shape.
type aiRunCommandInput struct {
	Argv []string `json:"argv"`
}

type aiSetCustomerInput struct {
	Customer string `json:"customer"`
}

// aiToolExecutor runs tool calls for a session. The command runner is a var
// so tests script outcomes without spawning processes.
type aiToolExecutor struct {
	configDir string
	runner    func(ctx context.Context, argv, extraEnv []string) (output []byte, exitCode int, err error)

	mu sync.Mutex
	// customerOverride is the session-scoped customer context set by the
	// agent's set_customer_context. It never touches the persisted context
	// file: children receive it as DCI_CUSTOMER_CONTEXT (which the CLI reads
	// before the file), so a crashed or forgetful session can never leave the
	// user's saved context pointing at another tenant.
	customerOverride string
}

func newAIToolExecutor(configDir string) *aiToolExecutor {
	return &aiToolExecutor{configDir: configDir, runner: aiAgentModeRunner}
}

// CustomerOverride returns the session-scoped context ("" when unset).
func (e *aiToolExecutor) CustomerOverride() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.customerOverride
}

// ClearCustomerOverride drops the session-scoped context — called when the
// user persists a context themselves (/customer), which must win over any
// earlier agent switch.
func (e *aiToolExecutor) ClearCustomerOverride() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.customerOverride = ""
}

// EffectiveCustomer is the tenant children actually run against: the session
// override when set, otherwise the environment/persisted context.
func (e *aiToolExecutor) EffectiveCustomer() string {
	if override := e.CustomerOverride(); override != "" {
		return override
	}
	return readCustomerContext(e.configDir)
}

// aiCustomerEnv shapes a session-scoped context as child environment; the
// caller must place it after os.Environ() with any inherited duplicate
// removed (aiChildEnv).
func aiCustomerEnv(customer string) []string {
	if customer == "" {
		return nil
	}
	return []string{"DCI_CUSTOMER_CONTEXT=" + customer}
}

// aiChildEnv builds a child environment from the parent's, dropping any
// inherited DCI_CUSTOMER_CONTEXT when extras carry one — getenv semantics on
// duplicate entries are platform-defined, so the override must be the only
// occurrence.
func aiChildEnv(extras []string) []string {
	env := os.Environ()
	overriding := false
	for _, extra := range extras {
		if strings.HasPrefix(extra, "DCI_CUSTOMER_CONTEXT=") {
			overriding = true
		}
	}
	if overriding {
		kept := env[:0]
		for _, entry := range env {
			if !strings.HasPrefix(entry, "DCI_CUSTOMER_CONTEXT=") {
				kept = append(kept, entry)
			}
		}
		env = kept
	}
	return append(env, extras...)
}

// aiAgentModeRunner re-execs this binary with DCI_AGENT_MODE=1: compact
// deterministic output, structured errors on stderr, the documented exit
// taxonomy. Combined output keeps the structured error envelope adjacent to
// any partial stdout, which is exactly what the model needs to self-correct.
func aiAgentModeRunner(ctx context.Context, argv, extraEnv []string) ([]byte, int, error) {
	command := exec.CommandContext(ctx, aiExecutablePath(), argv...)
	command.Env = aiChildEnv(append([]string{"DCI_AGENT_MODE=1", "DCI_NO_TUI=1"}, extraEnv...))
	output, err := command.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return output, exitErr.ExitCode(), nil
		}
		return output, 0, err
	}
	return output, 0, nil
}

const aiDestructiveExitCode = 30 // destructiveConfirmationError.ExitCode()

// RunCommand executes one run_dci_command call. approved=true appends --yes
// (the human already confirmed via the approval round-trip).
func (e *aiToolExecutor) RunCommand(ctx context.Context, input aiRunCommandInput, approved bool) aiToolOutcome {
	if len(input.Argv) == 0 {
		return aiToolOutcome{Data: aiToolError("EMPTY_ARGV", "argv must contain at least the command name", ""), IsError: true}
	}
	if reason, denied := aiDeniedCommands[input.Argv[0]]; denied {
		return aiToolOutcome{Data: aiToolError("COMMAND_NOT_ALLOWED", "dci "+input.Argv[0]+" is not available to the agent", reason), IsError: true}
	}
	argv := input.Argv
	if approved && !aiArgvHasYes(argv) {
		argv = append(append([]string{}, argv...), "--yes")
	}
	timeout := aiToolTimeoutFor(argv)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, exitCode, err := e.runner(runCtx, argv, aiCustomerEnv(e.CustomerOverride()))
	if runCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		return aiToolOutcome{Data: aiToolError(
			"COMMAND_TIMED_OUT",
			fmt.Sprintf("dci %s was stopped after %s", strings.Join(argv, " "), timeout),
			"Narrow the request (server-side filters, --fields, a smaller time range) or ask the user to run it themselves",
		), IsError: true}
	}
	if err != nil {
		return aiToolOutcome{Data: aiToolError("EXEC_FAILED", err.Error(), ""), IsError: true}
	}
	// Exit 30 alone is ambiguous: the error contract maps API VALIDATION_ERROR
	// to the same code (error_contract.go's exitValidation). Only the
	// destructive contract's own envelope may open an approval round —
	// otherwise a mistyped flag value surfaces as a bogus y/N prompt and, in
	// one-shot mode, an auto-decline that blocks the model's self-correction.
	if exitCode == aiDestructiveExitCode && !approved && aiEnvelopeCode(output) == "DESTRUCTIVE_REQUIRES_CONFIRMATION" {
		return aiToolOutcome{
			NeedsApproval: true,
			Summary:       aiDestructiveSummary(output, argv),
		}
	}
	data, truncated := aiCapToolResult(string(output))
	return aiToolOutcome{Data: data, IsError: exitCode != 0, Truncated: truncated}
}

// aiArgvHasYes reports whether the argv already carries the confirmation
// flag, so an approved retry never doubles it.
func aiArgvHasYes(argv []string) bool {
	for _, arg := range argv {
		if arg == "--yes" || arg == "--yes=true" {
			return true
		}
	}
	return false
}

// aiEnvelopeCode extracts the structured error envelope's code from a child's
// output ("" when no envelope line parses).
func aiEnvelopeCode(output []byte) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if json.Unmarshal([]byte(line), &envelope) == nil && envelope.Error.Code != "" {
			return envelope.Error.Code
		}
	}
	return ""
}

// aiDestructiveSummary extracts the human-facing confirmation line from the
// child's structured error, falling back to the command line itself.
func aiDestructiveSummary(output []byte, argv []string) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if json.Unmarshal([]byte(line), &envelope) == nil && envelope.Error.Message != "" {
			return envelope.Error.Message
		}
	}
	return "dci " + strings.Join(argv, " ")
}

// SetCustomer executes set_customer_context: validate, persist through the
// same write path as /customer, report the from→to transition.
func (e *aiToolExecutor) SetCustomer(input aiSetCustomerInput) (from, to string, outcome aiToolOutcome) {
	from = e.EffectiveCustomer()
	token := strings.TrimSpace(input.Customer)
	if err := validateCustomerContextValue(token); err != nil {
		return from, "", aiToolOutcome{Data: aiToolError("INVALID_CUSTOMER_CONTEXT", err.Error(), ""), IsError: true}
	}
	// Session-scoped on purpose: the switch reaches children via
	// DCI_CUSTOMER_CONTEXT, never the persisted context file — an agent
	// switch (or a crash right after one) must not change what the user's
	// next plain dci invocation runs against. /customer persists.
	e.mu.Lock()
	e.customerOverride = token
	e.mu.Unlock()
	return from, token, aiToolOutcome{Data: fmt.Sprintf("customer context switched from %q to %q for this session (the saved context is unchanged)", from, token)}
}

// aiCapToolResult bounds one tool result entering model context (§7.6),
// keeping the head and marking the cut explicitly so the model knows data is
// missing rather than absent.
func aiCapToolResult(data string) (string, bool) {
	if len(data) <= aiToolResultByteLimit {
		return data, false
	}
	return data[:aiToolResultByteLimit] + "\n[truncated: output exceeded the tool result limit — refine the query with --fields/--exclude or filters]", true
}

// aiToolError shapes an executor-level failure like the CLI's own structured
// errors, so the model handles both identically.
func aiToolError(code, message, hint string) string {
	envelope := map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": false}}
	if hint != "" {
		envelope["error"].(map[string]any)["hint"] = hint
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return message
	}
	return string(encoded)
}
