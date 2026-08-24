package main

import (
	"context"
	"strings"
	"testing"
)

// scriptedRunner returns canned (output, exit) pairs in order and records the
// argv of every call.
type scriptedRunner struct {
	calls   [][]string
	outputs []string
	exits   []int
	errs    []error
}

func (r *scriptedRunner) run(_ context.Context, argv []string) ([]byte, int, error) {
	index := len(r.calls)
	r.calls = append(r.calls, append([]string{}, argv...))
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
	executor := newAIToolExecutor(t.TempDir())
	from, to, outcome := executor.SetCustomer(aiSetCustomerInput{Customer: "acme.com"})
	if outcome.IsError || from != "" || to != "acme.com" {
		t.Fatalf("set customer = from %q to %q outcome %+v", from, to, outcome)
	}
	if got := readCustomerContext(executor.configDir); got != "acme.com" {
		t.Fatalf("persisted context = %q", got)
	}
	_, _, outcome = executor.SetCustomer(aiSetCustomerInput{Customer: "  "})
	if !outcome.IsError {
		t.Fatal("blank customer accepted")
	}
}
