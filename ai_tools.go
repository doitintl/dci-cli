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
	runner    func(ctx context.Context, argv []string) (output []byte, exitCode int, err error)
}

func newAIToolExecutor(configDir string) *aiToolExecutor {
	return &aiToolExecutor{configDir: configDir, runner: aiAgentModeRunner}
}

// aiAgentModeRunner re-execs this binary with DCI_AGENT_MODE=1: compact
// deterministic output, structured errors on stderr, the documented exit
// taxonomy. Combined output keeps the structured error envelope adjacent to
// any partial stdout, which is exactly what the model needs to self-correct.
func aiAgentModeRunner(ctx context.Context, argv []string) ([]byte, int, error) {
	command := exec.CommandContext(ctx, aiExecutablePath(), argv...)
	command.Env = append(os.Environ(), "DCI_AGENT_MODE=1", "DCI_NO_TUI=1")
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
	runCtx, cancel := context.WithTimeout(ctx, aiToolCommandTimeout)
	defer cancel()
	output, exitCode, err := e.runner(runCtx, argv)
	if runCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		return aiToolOutcome{Data: aiToolError(
			"COMMAND_TIMED_OUT",
			fmt.Sprintf("dci %s was stopped after %s", strings.Join(argv, " "), aiToolCommandTimeout),
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

// aiDestructiveSummary extracts the human-facing confirmation line from the
// child's structured error, falling back to the command line itself.
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
	from = readCustomerContext(e.configDir)
	token := strings.TrimSpace(input.Customer)
	if err := validateCustomerContextValue(token); err != nil {
		return from, "", aiToolOutcome{Data: aiToolError("INVALID_CUSTOMER_CONTEXT", err.Error(), ""), IsError: true}
	}
	if err := os.WriteFile(customerContextPath(e.configDir), []byte(token+"\n"), 0o600); err != nil {
		return from, "", aiToolOutcome{Data: aiToolError("WRITE_FAILED", err.Error(), ""), IsError: true}
	}
	return from, token, aiToolOutcome{Data: fmt.Sprintf("customer context switched from %q to %q", from, token)}
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
