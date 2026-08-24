package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// aiTestMessage builds an anthropic.Message by unmarshaling JSON, which is
// the only way the SDK's raw-JSON accessors (toolUse.JSON.Input.Raw) get
// populated — hand-built structs leave them empty.
func aiTestMessage(t *testing.T, body string) anthropic.Message {
	t.Helper()
	var message anthropic.Message
	if err := json.Unmarshal([]byte(body), &message); err != nil {
		t.Fatalf("test message does not parse: %v", err)
	}
	return message
}

const aiTestTextTurn = `{"role":"assistant","content":[{"type":"text","text":"All good."}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":50}}`

func aiTestToolTurn(argv ...string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = `"` + arg + `"`
	}
	return `{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"run_dci_command","input":{"argv":[` +
		strings.Join(quoted, ",") + `]}}],"stop_reason":"tool_use","usage":{"input_tokens":20,"output_tokens":5}}`
}

// newTestSession wires a localAISession with a scripted streamer and runner.
func newTestSession(t *testing.T, turns []string, runner *scriptedRunner) *localAISession {
	t.Helper()
	dir := t.TempDir()
	turnIndex := 0
	session := &localAISession{
		configDir:    dir,
		model:        aiDefaultModel,
		stablePrompt: "test prompt",
		executor:     &aiToolExecutor{configDir: dir, runner: runner.run},
		events:       make(chan aiEvent, 64),
		approvals:    make(chan aiUserInput, 1),
	}
	session.streamer = func(ctx context.Context, params anthropic.MessageNewParams, onDelta func(string)) (anthropic.Message, error) {
		if turnIndex >= len(turns) {
			t.Errorf("streamer called %d times, scripted %d", turnIndex+1, len(turns))
			return aiTestMessage(t, aiTestTextTurn), nil
		}
		message := aiTestMessage(t, turns[turnIndex])
		turnIndex++
		for _, block := range message.Content {
			if text, ok := block.AsAny().(anthropic.TextBlock); ok {
				onDelta(text.Text)
			}
		}
		return message, nil
	}
	return session
}

// collectTurnEvents drains events until TurnDone (or times out).
func collectTurnEvents(t *testing.T, session *localAISession) []aiEvent {
	t.Helper()
	var events []aiEvent
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-session.Events():
			events = append(events, event)
			if event.TurnDone != nil {
				return events
			}
		case <-timeout:
			t.Fatalf("timed out waiting for TurnDone; got %d events", len(events))
		}
	}
}

func eventKinds(events []aiEvent) string {
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		switch {
		case event.TurnStarted != nil:
			kinds = append(kinds, "start")
		case event.TextDelta != nil:
			kinds = append(kinds, "delta")
		case event.ToolCallStarted != nil:
			kinds = append(kinds, "tool")
		case event.ToolResult != nil:
			kinds = append(kinds, "result")
		case event.ApprovalRequest != nil:
			kinds = append(kinds, "approval")
		case event.ContextSwitched != nil:
			kinds = append(kinds, "switch")
		case event.LimitReached != nil:
			kinds = append(kinds, "limit")
		case event.Error != nil:
			kinds = append(kinds, "error")
		case event.TurnDone != nil:
			kinds = append(kinds, "done")
		}
	}
	return strings.Join(kinds, " ")
}

func TestAISessionPlainTextTurn(t *testing.T) {
	session := newTestSession(t, []string{aiTestTextTurn}, &scriptedRunner{})
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	events := collectTurnEvents(t, session)
	if got := eventKinds(events); got != "start delta done" {
		t.Fatalf("events = %s", got)
	}
	done := events[len(events)-1].TurnDone
	if done.InputTokens != 100 || done.OutputTokens != 10 || done.CacheRead != 50 {
		t.Fatalf("usage = %+v", done)
	}
	if len(session.history) != 2 {
		t.Fatalf("history = %d messages, want user+assistant", len(session.history))
	}
}

func TestAISessionToolRoundTrip(t *testing.T) {
	runner := &scriptedRunner{outputs: []string{"NAME spend\nprod 42"}, exits: []int{0}}
	session := newTestSession(t, []string{aiTestToolTurn("list-budgets"), aiTestTextTurn}, runner)
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "budgets?"}); err != nil {
		t.Fatal(err)
	}
	events := collectTurnEvents(t, session)
	if got := eventKinds(events); got != "start tool result delta done" {
		t.Fatalf("events = %s", got)
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != "list-budgets" {
		t.Fatalf("runner calls = %v", runner.calls)
	}
	// user, assistant(tool_use), user(tool_result), assistant(text)
	if len(session.history) != 4 {
		t.Fatalf("history = %d messages", len(session.history))
	}
}

func TestAISessionApprovalApproved(t *testing.T) {
	envelope := `{"error":{"code":"DESTRUCTIVE_REQUIRES_CONFIRMATION","message":"delete-budget requires confirmation","retryable":false}}`
	runner := &scriptedRunner{outputs: []string{envelope, "deleted"}, exits: []int{aiDestructiveExitCode, 0}}
	session := newTestSession(t, []string{aiTestToolTurn("delete-budget", "prod"), aiTestTextTurn}, runner)
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "delete prod"}); err != nil {
		t.Fatal(err)
	}

	timeout := time.After(5 * time.Second)
	var sawApproval bool
	var events []aiEvent
	for {
		select {
		case event := <-session.Events():
			events = append(events, event)
			if event.ApprovalRequest != nil {
				sawApproval = true
				_ = session.Send(aiUserInput{Kind: aiInputApproval, CallID: event.ApprovalRequest.CallID, Approved: true})
			}
			if event.TurnDone != nil {
				if !sawApproval {
					t.Fatal("turn finished without an approval request")
				}
				if got := eventKinds(events); got != "start tool approval result delta done" {
					t.Fatalf("events = %s", got)
				}
				if len(runner.calls) != 2 {
					t.Fatalf("runner calls = %v", runner.calls)
				}
				retry := runner.calls[1]
				if retry[len(retry)-1] != "--yes" {
					t.Fatalf("approved retry = %v", retry)
				}
				return
			}
		case <-timeout:
			t.Fatal("timed out")
		}
	}
}

func TestAISessionApprovalDeclined(t *testing.T) {
	envelope := `{"error":{"code":"DESTRUCTIVE_REQUIRES_CONFIRMATION","message":"delete-budget requires confirmation","retryable":false}}`
	runner := &scriptedRunner{outputs: []string{envelope}, exits: []int{aiDestructiveExitCode}}
	session := newTestSession(t, []string{aiTestToolTurn("delete-budget", "prod"), aiTestTextTurn}, runner)
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "delete prod"}); err != nil {
		t.Fatal(err)
	}

	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-session.Events():
			if event.ApprovalRequest != nil {
				_ = session.Send(aiUserInput{Kind: aiInputApproval, CallID: event.ApprovalRequest.CallID, Approved: false})
			}
			if event.ToolResult != nil {
				if event.ToolResult.OK || !strings.Contains(event.ToolResult.Data, "DESTRUCTIVE_DECLINED") {
					t.Fatalf("declined tool result = %+v", event.ToolResult)
				}
			}
			if event.TurnDone != nil {
				if len(runner.calls) != 1 {
					t.Fatalf("declined command re-ran: %v", runner.calls)
				}
				return
			}
		case <-timeout:
			t.Fatal("timed out")
		}
	}
}

func TestAISessionRejectsConcurrentTurns(t *testing.T) {
	release := make(chan struct{})
	session := newTestSession(t, nil, &scriptedRunner{})
	session.streamer = func(ctx context.Context, params anthropic.MessageNewParams, onDelta func(string)) (anthropic.Message, error) {
		<-release
		return aiTestMessage(t, aiTestTextTurn), nil
	}
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "two"}); err != errAITurnRunning {
		t.Fatalf("second Send = %v, want errAITurnRunning", err)
	}
	close(release)
	collectTurnEvents(t, session)
}

func TestAISessionInjectsCommandResults(t *testing.T) {
	var captured anthropic.MessageNewParams
	session := newTestSession(t, nil, &scriptedRunner{})
	session.streamer = func(ctx context.Context, params anthropic.MessageNewParams, onDelta func(string)) (anthropic.Message, error) {
		captured = params
		return aiTestMessage(t, aiTestTextTurn), nil
	}
	_ = session.Send(aiUserInput{Kind: aiInputCommandResult, Argv: []string{"list-budgets"}, Output: "prod 42", Customer: "acme.com"})
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "why is prod high?"}); err != nil {
		t.Fatal(err)
	}
	collectTurnEvents(t, session)

	if len(captured.Messages) != 1 {
		t.Fatalf("captured %d messages", len(captured.Messages))
	}
	encoded, err := json.Marshal(captured.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{"list-budgets", "prod 42", "acme.com", "why is prod high?"} {
		if !strings.Contains(body, want) {
			t.Fatalf("user message missing %q: %s", want, body)
		}
	}
	// Injections are consumed: a second question must not repeat them.
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "and dev?"}); err != nil {
		t.Fatal(err)
	}
	collectTurnEvents(t, session)
	encoded, _ = json.Marshal(captured.Messages[len(captured.Messages)-1])
	if strings.Contains(string(encoded), "prod 42") {
		t.Fatal("injected result repeated on the next question")
	}
}

func TestAISessionParamsLayout(t *testing.T) {
	session := newTestSession(t, nil, &scriptedRunner{})
	session.tenantAware = true
	params := session.params()
	if len(params.System) != 2 {
		t.Fatalf("system blocks = %d, want stable+volatile", len(params.System))
	}
	if params.System[0].CacheControl.Type == "" {
		t.Fatal("stable system block carries no cache breakpoint")
	}
	if len(params.Tools) != 2 {
		t.Fatalf("tenant-aware tools = %d, want run+switch", len(params.Tools))
	}
	session.tenantAware = false
	if params := session.params(); len(params.Tools) != 1 {
		t.Fatalf("customer-mode tools = %d, want run only", len(params.Tools))
	}
}

func TestAISettingsRoundTripAndResolution(t *testing.T) {
	dir := t.TempDir()
	if err := saveAISettings(dir, aiSettings{APIKey: "sk-test", Model: "claude-sonnet-5"}); err != nil {
		t.Fatal(err)
	}
	loaded := loadAISettings(dir)
	if loaded.APIKey != "sk-test" || loaded.Model != "claude-sonnet-5" {
		t.Fatalf("loaded = %+v", loaded)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	if got := resolveAIKey(loaded); got != "sk-env" {
		t.Fatalf("env must win, got %q", got)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := resolveAIKey(loaded); got != "sk-test" {
		t.Fatalf("file fallback, got %q", got)
	}
	if got := resolveAIModel(aiSettings{}); got != aiDefaultModel {
		t.Fatalf("default model = %q", got)
	}
	if err := aiValidateModel("gpt-4"); err == nil {
		t.Fatal("non-claude model accepted")
	}
	if err := aiValidateModel("claude-opus-5"); err != nil {
		t.Fatal(err)
	}
}
