package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedRunner returns canned (output, exit) pairs in order and records the
// argv and extra env of every call. Mutex-guarded: the session runs batched
// tool calls concurrently.
type scriptedRunner struct {
	mu      sync.Mutex
	calls   [][]string
	envs    [][]string
	outputs []string
	exits   []int
	errs    []error
}

func (r *scriptedRunner) run(_ context.Context, argv, extraEnv []string) ([]byte, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := len(r.calls)
	r.calls = append(r.calls, append([]string{}, argv...))
	r.envs = append(r.envs, append([]string{}, extraEnv...))
	if index >= len(r.outputs) {
		return nil, 0, nil
	}
	var err error
	if index < len(r.errs) {
		err = r.errs[index]
	}
	return []byte(r.outputs[index]), r.exits[index], err
}

func newScriptedExecutor(t *testing.T, runner *scriptedRunner) *aiToolExecutor {
	t.Helper()
	executor := newAIToolExecutor(t.TempDir())
	executor.runner = runner.run
	return executor
}

func TestAIRunCommandDenyList(t *testing.T) {
	runner := &scriptedRunner{}
	executor := newScriptedExecutor(t, runner)
	for _, denied := range []string{"ai", "login", "logout", "update", "completion"} {
		outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{denied}}, false)
		if !outcome.IsError || !strings.Contains(outcome.Data, "COMMAND_NOT_ALLOWED") {
			t.Fatalf("%s not denied: %+v", denied, outcome)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("denied commands reached the runner: %v", runner.calls)
	}
	if outcome := executor.RunCommand(context.Background(), aiRunCommandInput{}, false); !outcome.IsError {
		t.Fatalf("empty argv accepted: %+v", outcome)
	}
}

func TestAIRunCommandDestructiveApprovalProtocol(t *testing.T) {
	envelope := `{"error":{"code":"DESTRUCTIVE_REQUIRES_CONFIRMATION","message":"delete-budget targets budget \"prod\"; re-run with --yes","retryable":false}}`
	runner := &scriptedRunner{
		outputs: []string{envelope, "deleted"},
		exits:   []int{aiDestructiveExitCode, 0},
	}
	executor := newScriptedExecutor(t, runner)

	outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"delete-budget", "prod"}}, false)
	if !outcome.NeedsApproval {
		t.Fatalf("exit 30 did not request approval: %+v", outcome)
	}
	if !strings.Contains(outcome.Summary, "delete-budget targets budget") {
		t.Fatalf("summary not extracted from the structured error: %q", outcome.Summary)
	}

	approved := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"delete-budget", "prod"}}, true)
	if approved.IsError || approved.Data != "deleted" {
		t.Fatalf("approved retry = %+v", approved)
	}
	retry := runner.calls[1]
	if retry[len(retry)-1] != "--yes" {
		t.Fatalf("approved retry argv = %v, want trailing --yes", retry)
	}
}

func TestAIRunCommandValidationExit30IsNotDestructive(t *testing.T) {
	// The error contract maps API VALIDATION_ERROR to exit 30 — the same code
	// the destructive contract uses. Only the destructive envelope may open
	// an approval round; a validation error must come back as a plain error
	// the model can self-correct from (observed live: list-anomalies with
	// --sort-order descending was auto-declined as "destructive").
	envelope := `{"error":{"code":"VALIDATION_ERROR","message":"Error In Param: sortOrder, accepted values: asc, desc","retryable":false}}`
	runner := &scriptedRunner{outputs: []string{envelope}, exits: []int{aiDestructiveExitCode}}
	executor := newScriptedExecutor(t, runner)

	outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"list-anomalies", "--sort-order", "descending"}}, false)
	if outcome.NeedsApproval {
		t.Fatalf("validation error misread as destructive: %+v", outcome)
	}
	if !outcome.IsError || !strings.Contains(outcome.Data, "VALIDATION_ERROR") {
		t.Fatalf("expected the validation envelope as a plain error result, got %+v", outcome)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("validation error must not trigger a retry, calls = %v", runner.calls)
	}
}

func TestAIRunCommandApprovedDoesNotDoubleYes(t *testing.T) {
	runner := &scriptedRunner{outputs: []string{"ok"}, exits: []int{0}}
	executor := newScriptedExecutor(t, runner)
	executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"delete-budget", "--yes"}}, true)
	call := runner.calls[0]
	count := 0
	for _, arg := range call {
		if arg == "--yes" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("argv = %v, want exactly one --yes", call)
	}
}

func TestAIRunCommandExitAndExecErrors(t *testing.T) {
	runner := &scriptedRunner{outputs: []string{`{"error":{"code":"NOT_FOUND"}}`}, exits: []int{4}}
	executor := newScriptedExecutor(t, runner)
	outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"get-report", "x"}}, false)
	if !outcome.IsError || outcome.NeedsApproval {
		t.Fatalf("non-zero exit outcome = %+v", outcome)
	}

	// Approved calls that still exit 30 (e.g. --yes rejected) must not loop.
	runner30 := &scriptedRunner{outputs: []string{"blocked"}, exits: []int{aiDestructiveExitCode}}
	executor30 := newScriptedExecutor(t, runner30)
	outcome = executor30.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"delete-budget"}}, true)
	if outcome.NeedsApproval || !outcome.IsError {
		t.Fatalf("approved exit-30 outcome = %+v, want plain error", outcome)
	}
}

func TestAIRunCommandTimesOutWedgedChild(t *testing.T) {
	previousTimeout := aiToolCommandTimeout
	aiToolCommandTimeout = 30 * time.Millisecond
	t.Cleanup(func() { aiToolCommandTimeout = previousTimeout })

	executor := newAIToolExecutor(t.TempDir())
	executor.runner = func(ctx context.Context, argv, _ []string) ([]byte, int, error) {
		<-ctx.Done() // a wedged child: only dies when the executor kills it
		return nil, -1, ctx.Err()
	}
	outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"list-budgets"}}, false)
	if !outcome.IsError || !strings.Contains(outcome.Data, "COMMAND_TIMED_OUT") {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestAIRunCommandUserCancelIsNotATimeout(t *testing.T) {
	// Esc cancels the turn context; that must surface as the plain execution
	// error, not as COMMAND_TIMED_OUT telling the model to narrow the query.
	ctx, cancel := context.WithCancel(context.Background())
	executor := newAIToolExecutor(t.TempDir())
	executor.runner = func(runCtx context.Context, argv, _ []string) ([]byte, int, error) {
		cancel()
		<-runCtx.Done()
		return nil, -1, runCtx.Err()
	}
	outcome := executor.RunCommand(ctx, aiRunCommandInput{Argv: []string{"list-budgets"}}, false)
	if !outcome.IsError || strings.Contains(outcome.Data, "COMMAND_TIMED_OUT") {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestAICapToolResult(t *testing.T) {
	small, truncated := aiCapToolResult("hello")
	if small != "hello" || truncated {
		t.Fatalf("small result mangled: %q %v", small, truncated)
	}
	big, truncated := aiCapToolResult(strings.Repeat("x", aiToolResultByteLimit+100))
	if !truncated || !strings.Contains(big, "[truncated") {
		t.Fatalf("large result not truncated")
	}
	if len(big) > aiToolResultByteLimit+200 {
		t.Fatalf("truncated result still huge: %d bytes", len(big))
	}
}

func TestAISetCustomerTool(t *testing.T) {
	// Agent switches are session-scoped: the override reaches children as
	// DCI_CUSTOMER_CONTEXT, and the persisted context file is never written —
	// a crashed or forgetful session must not change what the user's next
	// plain dci invocation runs against.
	executor := newAIToolExecutor(t.TempDir())
	from, to, outcome := executor.SetCustomer(aiSetCustomerInput{Customer: "acme.com"})
	if outcome.IsError || from != "" || to != "acme.com" {
		t.Fatalf("set customer = from %q to %q outcome %+v", from, to, outcome)
	}
	if got := readCustomerContext(executor.configDir); got != "" {
		t.Fatalf("agent switch persisted to disk: %q", got)
	}
	if got := executor.CustomerOverride(); got != "acme.com" {
		t.Fatalf("session override = %q", got)
	}
	if got := executor.EffectiveCustomer(); got != "acme.com" {
		t.Fatalf("effective customer = %q", got)
	}

	// Children inherit the override as env; a later switch reports the
	// current override as its from.
	runner := &scriptedRunner{outputs: []string{"ok"}, exits: []int{0}}
	executor.runner = func(ctx context.Context, argv, extraEnv []string) ([]byte, int, error) {
		if len(extraEnv) != 1 || extraEnv[0] != "DCI_CUSTOMER_CONTEXT=acme.com" {
			t.Fatalf("child env = %v", extraEnv)
		}
		return runner.run(ctx, argv, extraEnv)
	}
	executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"list-budgets"}}, false)
	from, to, outcome = executor.SetCustomer(aiSetCustomerInput{Customer: "globex.com"})
	if outcome.IsError || from != "acme.com" || to != "globex.com" {
		t.Fatalf("second switch = from %q to %q outcome %+v", from, to, outcome)
	}

	// The user persisting a context clears the override.
	executor.ClearCustomerOverride()
	if got := executor.CustomerOverride(); got != "" {
		t.Fatalf("override survived clear: %q", got)
	}

	_, _, outcome = executor.SetCustomer(aiSetCustomerInput{Customer: "  "})
	if !outcome.IsError {
		t.Fatal("blank customer accepted")
	}
}
