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
		done:         make(chan struct{}),
	}
	session.streamer = func(ctx context.Context, params anthropic.MessageNewParams, onDelta, onThinking func(string)) (anthropic.Message, error) {
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
		case event.ThinkingDelta != nil:
			kinds = append(kinds, "thinking")
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

func TestAISessionForwardsThinkingDeltas(t *testing.T) {
	// Models with adaptive thinking reason before answering — often the
	// longest stretch of a turn. The session must forward those deltas so
	// renderers can show live progress instead of a silent gap.
	session := newTestSession(t, nil, &scriptedRunner{})
	session.streamer = func(ctx context.Context, params anthropic.MessageNewParams, onDelta, onThinking func(string)) (anthropic.Message, error) {
		onThinking("normalizing model-name variants")
		message := aiTestMessage(t, aiTestTextTurn)
		for _, block := range message.Content {
			if text, ok := block.AsAny().(anthropic.TextBlock); ok {
				onDelta(text.Text)
			}
		}
		return message, nil
	}
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "token spend per model?"}); err != nil {
		t.Fatal(err)
	}
	events := collectTurnEvents(t, session)
	if got := eventKinds(events); got != "start thinking delta done" {
		t.Fatalf("events = %s", got)
	}
	for _, event := range events {
		if event.ThinkingDelta != nil && event.ThinkingDelta.Text != "normalizing model-name variants" {
			t.Fatalf("thinking delta text = %q", event.ThinkingDelta.Text)
		}
	}
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
	if done.Rounds != 1 || done.ToolCalls != 0 {
		t.Fatalf("rounds/tools = %d/%d, want 1/0", done.Rounds, done.ToolCalls)
	}
	if done.Wall <= 0 {
		t.Fatalf("wall = %v, want > 0", done.Wall)
	}
	if done.FirstText <= 0 || done.FirstText > done.Wall {
		t.Fatalf("first text = %v (wall %v), want within (0, wall]", done.FirstText, done.Wall)
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
	done := events[len(events)-1].TurnDone
	if done.Rounds != 2 || done.ToolCalls != 1 {
		t.Fatalf("rounds/tools = %d/%d, want 2/1", done.Rounds, done.ToolCalls)
	}
	// user, assistant(tool_use), user(tool_result), assistant(text)
	if len(session.history) != 4 {
		t.Fatalf("history = %d messages", len(session.history))
	}
}

// aiTestCutTurn is a message the output-token limit cut mid-generation: text
// followed by a (possibly half-generated) tool_use.
const aiTestCutTurn = `{"role":"assistant","content":[{"type":"text","text":"Partial answer"},{"type":"tool_use","id":"call-9","name":"run_dci_command","input":{"argv":["list-budgets"]}}],"stop_reason":"max_tokens","usage":{"input_tokens":5,"output_tokens":5}}`

func TestAISessionOutputTokenCeiling(t *testing.T) {
	runner := &scriptedRunner{}
	session := newTestSession(t, []string{aiTestCutTurn}, runner)
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "big question"}); err != nil {
		t.Fatal(err)
	}
	events := collectTurnEvents(t, session)
	if got := eventKinds(events); got != "start delta limit done" {
		t.Fatalf("events = %s", got)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("truncated tool call executed: %v", runner.calls)
	}
	// user + assistant (text only): the half-generated tool_use must not be
	// replayed, or the next question 400s with a dangling tool_use.
	if len(session.history) != 2 {
		t.Fatalf("history = %d messages", len(session.history))
	}
	for _, block := range session.history[1].Content {
		if block.OfToolUse != nil {
			t.Fatal("truncated tool_use survived into history")
		}
	}
}

func TestAIHistoryParam(t *testing.T) {
	cut := aiTestMessage(t, aiTestCutTurn)
	param, keep := aiHistoryParam(cut)
	if !keep {
		t.Fatal("cut message with text must be kept")
	}
	for _, block := range param.Content {
		if block.OfToolUse != nil {
			t.Fatal("cut tool_use kept")
		}
	}
	toolOnly := aiTestMessage(t, `{"role":"assistant","content":[{"type":"tool_use","id":"call-9","name":"run_dci_command","input":{}}],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`)
	if _, keep := aiHistoryParam(toolOnly); keep {
		t.Fatal("text-less cut message must be dropped")
	}
	full := aiTestMessage(t, aiTestToolTurn("list-budgets"))
	param, keep = aiHistoryParam(full)
	if !keep || len(param.Content) != 1 || param.Content[0].OfToolUse == nil {
		t.Fatalf("tool_use stop must round-trip unchanged: %+v", param.Content)
	}
}

func TestAISessionCancelKeepsHistoryValid(t *testing.T) {
	session := newTestSession(t, []string{aiTestToolTurn("list-budgets")}, &scriptedRunner{})
	session.executor.runner = func(ctx context.Context, argv, _ []string) ([]byte, int, error) {
		session.Cancel() // the user pressed esc while the command ran
		return []byte("partial"), 0, nil
	}
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: "budgets?"}); err != nil {
		t.Fatal(err)
	}
	events := collectTurnEvents(t, session)
	if got := eventKinds(events); got != "start tool error done" {
		t.Fatalf("events = %s", got)
	}
	// user, assistant(tool_use), user(canceled tool_result): the backfilled
	// result keeps the history valid for the next question.
	if len(session.history) != 3 {
		t.Fatalf("history = %d messages", len(session.history))
	}
	last := session.history[2]
	if len(last.Content) != 1 || last.Content[0].OfToolResult == nil {
		t.Fatalf("canceled turn did not backfill the tool_result: %+v", last.Content)
	}
}

func TestAISessionEmitAfterCloseDoesNotBlock(t *testing.T) {
	session := newTestSession(t, nil, &scriptedRunner{})
	for i := 0; i < cap(session.events); i++ {
		session.events <- aiEvent{}
	}
	_ = session.Close()
	finished := make(chan struct{})
	go func() {
		session.emit(aiEvent{TurnDone: &aiTurnDone{}})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("emit blocked after Close on a full events buffer")
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
	session.streamer = func(ctx context.Context, params anthropic.MessageNewParams, onDelta, onThinking func(string)) (anthropic.Message, error) {
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
	session.streamer = func(ctx context.Context, params anthropic.MessageNewParams, onDelta, onThinking func(string)) (anthropic.Message, error) {
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

func TestResolveAIEffortAndModelOverrides(t *testing.T) {
	t.Setenv("DCI_AI_EFFORT", "")
	t.Setenv("DCI_AI_MODEL", "")
	if got := resolveAIEffort(aiSettings{}); got != "" {
		t.Fatalf("no effort configured, got %q", got)
	}
	if got := resolveAIEffort(aiSettings{Effort: " Medium "}); got != "medium" {
		t.Fatalf("settings effort = %q", got)
	}
	// A config typo must degrade to the API default, not break the session.
	if got := resolveAIEffort(aiSettings{Effort: "max"}); got != "" {
		t.Fatalf("invalid effort must resolve empty, got %q", got)
	}
	t.Setenv("DCI_AI_EFFORT", "low")
	if got := resolveAIEffort(aiSettings{Effort: "high"}); got != "low" {
		t.Fatalf("env must win, got %q", got)
	}
	t.Setenv("DCI_AI_MODEL", "claude-sonnet-5")
	if got := resolveAIModel(aiSettings{Model: "claude-opus-5"}); got != "claude-sonnet-5" {
		t.Fatalf("model env must win, got %q", got)
	}
}
