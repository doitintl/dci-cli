package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// aiTestModel builds a session model without touching cli.Root, the real
// config dir, or the Claude API (no key), with a fixed catalog.
func aiTestModel(t *testing.T) aiModel {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	model := newAIModel(t.TempDir())
	// A keyless session opens in the guided key setup (F7); these tests
	// exercise the normal session, so dismiss it the way a user would.
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(aiModel)
	model.catalog = aiTestCatalog()
	return model
}

// fakeAISession records inputs and lets tests feed protocol events.
type fakeAISession struct {
	events   chan aiEvent
	sent     []aiUserInput
	canceled bool
}

func newFakeAISession() *fakeAISession {
	return &fakeAISession{events: make(chan aiEvent, 8)}
}

func (f *fakeAISession) Send(input aiUserInput) error { f.sent = append(f.sent, input); return nil }
func (f *fakeAISession) Events() <-chan aiEvent       { return f.events }
func (f *fakeAISession) Cancel()                      { f.canceled = true }
func (f *fakeAISession) Close() error                 { return nil }

func aiEventUpdate(m aiModel, event aiEvent) (aiModel, tea.Cmd) {
	updated, cmd := m.Update(aiSessionEventMsg{event: event})
	return updated.(aiModel), cmd
}

func aiType(m aiModel, text string) aiModel {
	for _, r := range text {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(aiModel)
	}
	return m
}

func aiPress(m aiModel, key tea.KeyType) (aiModel, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: key})
	return updated.(aiModel), cmd
}

func TestAITypingOpensCompletionPopup(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/li")
	if len(m.completions) != 2 {
		t.Fatalf("completions = %+v, want list-anomalies and list-budgets", m.completions)
	}
	// Tab accepts the selected candidate and closes the popup.
	m, _ = aiPress(m, tea.KeyTab)
	if got := m.input.Value(); got != "/list-anomalies " {
		t.Fatalf("input after tab = %q", got)
	}
	if len(m.completions) != 0 {
		t.Fatalf("popup still open after tab: %+v", m.completions)
	}
}

func TestAICompletionArrowNavigation(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/li")
	m, _ = aiPress(m, tea.KeyDown)
	if m.completionIndex != 1 {
		t.Fatalf("completionIndex after down = %d", m.completionIndex)
	}
	m, _ = aiPress(m, tea.KeyDown) // wraps
	if m.completionIndex != 0 {
		t.Fatalf("completionIndex after wrap = %d", m.completionIndex)
	}
	m, _ = aiPress(m, tea.KeyUp)
	if m.completionIndex != 1 {
		t.Fatalf("completionIndex after up = %d", m.completionIndex)
	}
}

func TestAIDispatchSetsRunningAndDoneClearsIt(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/status")
	updated, cmd := aiPress(m, tea.KeyEnter)
	m = updated
	if m.running == nil {
		t.Fatal("running state not set after dispatching /status")
	}
	if cmd == nil {
		t.Fatal("dispatch returned no command")
	}
	if got := strings.Join(m.running.argv, " "); got != "status" {
		t.Fatalf("running argv = %q", got)
	}
	if m.input.Value() != "" {
		t.Fatalf("input not cleared: %q", m.input.Value())
	}

	updated2, _ := m.Update(aiCmdDoneMsg{argv: []string{"status"}, output: "ok", elapsed: time.Second})
	m = updated2.(aiModel)
	if m.running != nil {
		t.Fatal("running state not cleared by aiCmdDoneMsg")
	}
	if !strings.Contains(aiTranscriptText(m), "dci status") {
		t.Fatal("result card missing from the transcript")
	}
}

func TestAIEscCancelsRunningCommand(t *testing.T) {
	m := aiTestModel(t)
	canceled := false
	m.running = &aiRunState{argv: []string{"status"}, cancel: func() { canceled = true }, started: time.Now()}
	m, _ = aiPress(m, tea.KeyEsc)
	if !canceled {
		t.Fatal("esc did not cancel the running command")
	}
	// Other keys are ignored while running.
	m = aiType(m, "x")
	if m.input.Value() != "" {
		t.Fatalf("input accepted while running: %q", m.input.Value())
	}
}

func TestAIPlainTextRoutesToChatNotice(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "why did spend spike?")
	updated, _ := aiPress(m, tea.KeyEnter)
	m = updated
	if m.running != nil {
		t.Fatal("chat input must not dispatch a subprocess")
	}
	if !m.keyEntry {
		t.Fatal("keyless chat must open the guided key setup")
	}
	if len(m.history) != 1 || m.history[0] != "why did spend spike?" {
		t.Fatalf("history = %v", m.history)
	}
}

func TestAICtrlCTwiceQuits(t *testing.T) {
	m := aiTestModel(t)
	m, cmd := aiPress(m, tea.KeyCtrlC)
	if cmd != nil || !m.ctrlCArmed {
		t.Fatalf("first ctrl+c: cmd=%v armed=%v", cmd, m.ctrlCArmed)
	}
	_, cmd = aiPress(m, tea.KeyCtrlC)
	if cmd == nil {
		t.Fatal("second ctrl+c returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("second ctrl+c = %T, want tea.QuitMsg", cmd())
	}
}

func TestAICtrlCClearsNonEmptyInput(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/sta")
	m, cmd := aiPress(m, tea.KeyCtrlC)
	if cmd != nil {
		t.Fatal("ctrl+c on non-empty input must not quit")
	}
	if m.input.Value() != "" || m.ctrlCArmed {
		t.Fatalf("input=%q armed=%v after ctrl+c", m.input.Value(), m.ctrlCArmed)
	}
}

func TestAIHistoryNavigation(t *testing.T) {
	m := aiTestModel(t)
	m.history = []string{"/status", "/list-budgets"}
	m.historyPos = len(m.history)

	m = aiType(m, "dra")
	m, _ = aiPress(m, tea.KeyUp)
	if m.input.Value() != "/list-budgets" {
		t.Fatalf("first up = %q", m.input.Value())
	}
	m, _ = aiPress(m, tea.KeyUp)
	if m.input.Value() != "/status" {
		t.Fatalf("second up = %q", m.input.Value())
	}
	m, _ = aiPress(m, tea.KeyUp) // already at oldest
	if m.input.Value() != "/status" {
		t.Fatalf("third up = %q", m.input.Value())
	}
	m, _ = aiPress(m, tea.KeyDown)
	m, _ = aiPress(m, tea.KeyDown)
	if m.input.Value() != "dra" {
		t.Fatalf("down past newest must restore the draft, got %q", m.input.Value())
	}
}

func TestAIQuitVerb(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/quit")
	_, cmd := aiPress(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("/quit returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("/quit = %T, want tea.QuitMsg", cmd())
	}
}

func TestAICustomerVerbUpdatesIdentity(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/customer acme.com")
	updated, _ := aiPress(m, tea.KeyEnter)
	m = updated
	if !strings.Contains(aiTranscriptText(m), "Customer context set") {
		t.Fatal("/customer confirmation missing from the transcript")
	}
	if !strings.Contains(m.identity, "acme.com") {
		t.Fatalf("identity = %q after /customer", m.identity)
	}
	if got := readCustomerContext(m.configDir); got != "acme.com" {
		t.Fatalf("persisted context = %q", got)
	}
}

func TestRenderAIRunCard(t *testing.T) {
	success := renderAIRunCard(aiCmdDoneMsg{
		argv: []string{"list-budgets"}, output: "NAME\nprod\n", elapsed: 1200 * time.Millisecond,
	})
	if !strings.Contains(success, "dci list-budgets") || !strings.Contains(success, "prod") || !strings.Contains(success, "1.2s") {
		t.Fatalf("success card = %q", success)
	}

	failed := renderAIRunCard(aiCmdDoneMsg{argv: []string{"status"}, exitCode: 3, elapsed: time.Second})
	if !strings.Contains(failed, "exit 3") {
		t.Fatalf("failure card = %q", failed)
	}

	canceled := renderAIRunCard(aiCmdDoneMsg{argv: []string{"status"}, canceled: true, elapsed: time.Second})
	if !strings.Contains(canceled, "canceled") {
		t.Fatalf("canceled card = %q", canceled)
	}

	broken := renderAIRunCard(aiCmdDoneMsg{argv: []string{"status"}, runErr: "exec format error"})
	if !strings.Contains(broken, "failed to run: exec format error") {
		t.Fatalf("runErr card = %q", broken)
	}
}

func TestAIStatusLineStates(t *testing.T) {
	m := aiTestModel(t)
	m.identity = "doer · acme.com"
	idle := m.statusLine()
	if !strings.Contains(idle, "doer · acme.com") || !strings.Contains(idle, "/ for commands") {
		t.Fatalf("idle status = %q", idle)
	}
	m.running = &aiRunState{argv: []string{"status"}, cancel: func() {}, started: time.Now()}
	running := m.statusLine()
	if !strings.Contains(running, "running /status") || !strings.Contains(running, "esc to cancel") {
		t.Fatalf("running status = %q", running)
	}
}

func TestAIChatSendsToSession(t *testing.T) {
	m := aiTestModel(t)
	session := newFakeAISession()
	m.session = session
	m = aiType(m, "why did spend spike?")
	updated, _ := aiPress(m, tea.KeyEnter)
	m = updated
	if len(session.sent) != 1 || session.sent[0].Kind != aiInputChat || session.sent[0].Text != "why did spend spike?" {
		t.Fatalf("session received %+v", session.sent)
	}
}

func TestAIDispatchIsDestructiveDisambiguatesExit30(t *testing.T) {
	// Exit 30 is shared by the destructive contract and API VALIDATION_ERROR
	// (error_contract.go). The dispatch approval must consult the destructive
	// set, not the exit code alone.
	prevSet, prevRead, prevErr := destructiveCommandSet, destructiveMetadataRead, destructiveMetadataErr
	t.Cleanup(func() {
		destructiveCommandSet, destructiveMetadataRead, destructiveMetadataErr = prevSet, prevRead, prevErr
	})
	destructiveCommandSet = map[string]bool{"delete-budget": true}
	destructiveMetadataRead = true
	destructiveMetadataErr = nil

	if !aiDispatchIsDestructive([]string{"delete-budget", "prod"}) {
		t.Fatal("destructive command not recognized")
	}
	if aiDispatchIsDestructive([]string{"list-anomalies", "--sort-order", "descending"}) {
		t.Fatal("read-only command misclassified as destructive")
	}
	// Metadata failure fails toward asking — never run destructive work
	// unconfirmed because the spec cache was cold.
	destructiveMetadataErr = errors.New("metadata unavailable")
	if !aiDispatchIsDestructive([]string{"delete-budget", "prod"}) {
		t.Fatal("metadata failure must fail toward approval")
	}
}

func TestAISessionCustomerOverrideLifecycle(t *testing.T) {
	m := aiTestModel(t)
	m.session = newFakeAISession()

	m, _ = aiEventUpdate(m, aiEvent{ContextSwitched: &aiContextSwitched{From: "", To: "csp.doit.com", By: "agent"}})
	if m.sessionCustomer != "csp.doit.com" {
		t.Fatalf("sessionCustomer = %q after agent switch", m.sessionCustomer)
	}
	if !strings.Contains(m.contextLabel(), "session-scoped") {
		t.Fatalf("context label hides the session scope: %q", m.contextLabel())
	}
	if got := m.effectiveCustomer(); got != "csp.doit.com" {
		t.Fatalf("effectiveCustomer = %q", got)
	}
	m.fetchedNames["reports"] = []nameCacheEntry{{ID: "AAAAAAAAAAAAAAAAAAA1", Name: "Old tenant report"}}

	// Another agent switch invalidates the previous tenant's fetched names.
	m, _ = aiEventUpdate(m, aiEvent{ContextSwitched: &aiContextSwitched{From: "csp.doit.com", To: "acme.com", By: "agent"}})
	if len(m.fetchedNames) != 0 {
		t.Fatal("fetched names survived a tenant switch")
	}

	// /clear starts a fresh conversation: the rebuilt session has no
	// override, so the TUI mirror must reset with it.
	m.fetchedNames["reports"] = []nameCacheEntry{{ID: "AAAAAAAAAAAAAAAAAAA1", Name: "Old tenant report"}}
	m = aiType(m, "/clear")
	m, _ = aiPress(m, tea.KeyEnter)
	if m.sessionCustomer != "" {
		t.Fatalf("sessionCustomer = %q after /clear, want cleared", m.sessionCustomer)
	}
	if len(m.fetchedNames) != 0 {
		t.Fatal("fetched names survived /clear")
	}
	if strings.Contains(m.contextLabel(), "session-scoped") {
		t.Fatalf("stale session-scoped label after /clear: %q", m.contextLabel())
	}
}

func TestAITurnLifecycleEvents(t *testing.T) {
	m := aiTestModel(t)
	m.session = newFakeAISession()

	m, _ = aiEventUpdate(m, aiEvent{TurnStarted: &aiTurnStarted{TurnID: "t1"}})
	if !m.turnActive {
		t.Fatal("TurnStarted did not activate the turn")
	}
	// Thinking deltas drive the status line (with a marker) but never join
	// the answer stream or the transcript — an analytical pause must show
	// live progress instead of a frozen spinner.
	m, _ = aiEventUpdate(m, aiEvent{ThinkingDelta: &aiThinkingDelta{TurnID: "t1", Text: "comparing months"}})
	if !strings.Contains(m.statusLine(), "thinking · comparing months") {
		t.Fatalf("status line missing the thinking snippet: %q", m.statusLine())
	}
	if m.stream != "" {
		t.Fatalf("thinking leaked into the answer stream: %q", m.stream)
	}
	m, _ = aiEventUpdate(m, aiEvent{TextDelta: &aiTextDelta{TurnID: "t1", Text: "Spend rose because "}})
	m, _ = aiEventUpdate(m, aiEvent{TextDelta: &aiTextDelta{TurnID: "t1", Text: "of GKE."}})
	if m.stream != "Spend rose because of GKE." {
		t.Fatalf("stream buffer = %q", m.stream)
	}
	// The quiet turn: in-flight text drives the status line, not the
	// transcript (Claude Code UX) — only the final answer is committed.
	if !strings.Contains(m.statusLine(), "Spend rose because of GKE.") {
		t.Fatalf("status line missing the activity snippet: %q", m.statusLine())
	}
	if strings.Contains(aiTranscriptText(m), "Spend rose") {
		t.Fatal("in-flight narration leaked into the transcript")
	}
	m, cmd := aiEventUpdate(m, aiEvent{TurnDone: &aiTurnDone{TurnID: "t1", InputTokens: 10}})
	if m.turnActive {
		t.Fatal("TurnDone left the turn active")
	}
	if cmd == nil {
		t.Fatal("TurnDone did not re-arm the listener")
	}
	if !strings.Contains(stripANSI(aiTranscriptText(m)), "Spend rose because of GKE.") {
		t.Fatal("final answer not committed to the transcript")
	}
	if m.stream != "" {
		t.Fatal("stream buffer not reset after commit")
	}
	if m.lastUsage == nil || m.lastUsage.InputTokens != 10 {
		t.Fatalf("usage not recorded: %+v", m.lastUsage)
	}
}
func TestAIRenderMarkdownFixedStyleAndEscapes(t *testing.T) {
	rendered := stripANSI(renderAIMarkdown(`\*August is partial (through today).`, 80, "dark"))
	if strings.Contains(rendered, `\*`) {
		t.Fatalf("backslash escape reached the screen: %q", rendered)
	}
	if !strings.Contains(rendered, "*August is partial") {
		t.Fatalf("literal asterisk lost: %q", rendered)
	}
}

func TestAIQuietTurnToolActivity(t *testing.T) {
	m := aiTestModel(t)
	m.session = newFakeAISession()

	m, _ = aiEventUpdate(m, aiEvent{TurnStarted: &aiTurnStarted{TurnID: "t1"}})
	m, _ = aiEventUpdate(m, aiEvent{TextDelta: &aiTextDelta{TurnID: "t1", Text: "Let me check the reports."}})
	m, _ = aiEventUpdate(m, aiEvent{ToolCallStarted: &aiToolCallStarted{
		TurnID: "t1", CallID: "c1", Tool: aiToolRunCommand, Argv: []string{"list-reports"}, By: "agent",
	}})
	if m.stream != "" {
		t.Fatal("interim narration not discarded on a tool call")
	}
	if status := m.statusLine(); !strings.Contains(status, "running dci list-reports") {
		t.Fatalf("status line = %q", status)
	}
	m, _ = aiEventUpdate(m, aiEvent{ToolResult: &aiToolResult{
		TurnID: "t1", CallID: "c1", OK: true, Data: "big table", Elapsed: 1200 * time.Millisecond,
	}})
	if status := m.statusLine(); !strings.Contains(status, "✓ dci list-reports") || !strings.Contains(status, "1 command") {
		t.Fatalf("status line = %q", status)
	}
	transcript := aiTranscriptText(m)
	if strings.Contains(transcript, "Let me check") || strings.Contains(transcript, "big table") {
		t.Fatal("tool traffic leaked into the transcript")
	}
	m, _ = aiEventUpdate(m, aiEvent{TextDelta: &aiTextDelta{TurnID: "t1", Text: "GKE grew 40%."}})
	m, _ = aiEventUpdate(m, aiEvent{TurnDone: &aiTurnDone{TurnID: "t1"}})
	transcript = stripANSI(aiTranscriptText(m))
	if !strings.Contains(transcript, "GKE grew 40%.") || strings.Contains(transcript, "Let me check") {
		t.Fatalf("final answer commit wrong: %q", transcript)
	}
	if m.turnActivity != "" {
		t.Fatalf("activity not cleared: %q", m.turnActivity)
	}
}

func TestAIQuietTurnInterleavedToolActivity(t *testing.T) {
	// The session runs batched calls concurrently, so starts and results
	// interleave: each result line must carry its own command's label, keyed
	// by call ID, not whichever call started last.
	m := aiTestModel(t)
	m.session = newFakeAISession()
	// A pane wide enough for both labels: the status line trims the activity
	// snippet to the terminal width, and this assertion is about which label
	// each event carries, not about narrow-pane trimming.
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = sized.(aiModel)

	m, _ = aiEventUpdate(m, aiEvent{TurnStarted: &aiTurnStarted{TurnID: "t1"}})
	m, _ = aiEventUpdate(m, aiEvent{ToolCallStarted: &aiToolCallStarted{
		TurnID: "t1", CallID: "c1", Tool: aiToolRunCommand, Argv: []string{"list-budgets"}, By: "agent",
	}})
	m, _ = aiEventUpdate(m, aiEvent{ToolCallStarted: &aiToolCallStarted{
		TurnID: "t1", CallID: "c2", Tool: aiToolRunCommand, Argv: []string{"list-reports"}, By: "agent",
	}})
	if status := m.statusLine(); !strings.Contains(status, "running 2 commands") {
		t.Fatalf("status line = %q", status)
	}
	m, _ = aiEventUpdate(m, aiEvent{ToolResult: &aiToolResult{
		TurnID: "t1", CallID: "c1", OK: true, Elapsed: time.Second,
	}})
	status := m.statusLine()
	if !strings.Contains(status, "✓ dci list-budgets") {
		t.Fatalf("result captioned with the wrong label: %q", status)
	}
	if !strings.Contains(status, "running dci list-reports") {
		t.Fatalf("in-flight sibling missing from the status line: %q", status)
	}
}

func TestAIApprovalKeys(t *testing.T) {
	m := aiTestModel(t)
	session := newFakeAISession()
	m.session = session
	m, _ = aiEventUpdate(m, aiEvent{ApprovalRequest: &aiApprovalRequest{CallID: "c1", Kind: "destructive", Argv: []string{"delete-budget", "prod"}}})
	if m.approval == nil {
		t.Fatal("approval request not held")
	}
	if !strings.Contains(m.statusLine(), "destructive") {
		t.Fatalf("status line = %q", m.statusLine())
	}
	// Random keys must not answer.
	m = aiType(m, "x")
	if m.approval == nil || m.input.Value() != "" {
		t.Fatal("stray key answered or typed during approval")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(aiModel)
	if m.approval != nil {
		t.Fatal("y did not clear the approval")
	}
	if len(session.sent) != 1 || session.sent[0].Kind != aiInputApproval || !session.sent[0].Approved || session.sent[0].CallID != "c1" {
		t.Fatalf("approval answer = %+v", session.sent)
	}
}

func TestAIApprovalDeclineOnEsc(t *testing.T) {
	m := aiTestModel(t)
	session := newFakeAISession()
	m.session = session
	m, _ = aiEventUpdate(m, aiEvent{ApprovalRequest: &aiApprovalRequest{CallID: "c2", Kind: "destructive"}})
	m, _ = aiPress(m, tea.KeyEsc)
	if m.approval != nil {
		t.Fatal("esc did not clear the approval")
	}
	if len(session.sent) != 1 || session.sent[0].Approved {
		t.Fatalf("esc answer = %+v", session.sent)
	}
}

func TestAIEscCancelsActiveTurn(t *testing.T) {
	m := aiTestModel(t)
	session := newFakeAISession()
	m.session = session
	m.turnActive = true
	m, _ = aiPress(m, tea.KeyEsc)
	if !session.canceled {
		t.Fatal("esc did not cancel the turn")
	}
}

func TestAISlashResultJoinsConversation(t *testing.T) {
	m := aiTestModel(t)
	session := newFakeAISession()
	m.session = session
	updated, _ := m.Update(aiCmdDoneMsg{argv: []string{"list-budgets"}, output: "prod 42", customer: "acme.com", elapsed: time.Second})
	m = updated.(aiModel)
	if len(session.sent) != 1 || session.sent[0].Kind != aiInputCommandResult {
		t.Fatalf("session received %+v", session.sent)
	}
	if session.sent[0].Output != "prod 42" || session.sent[0].Customer != "acme.com" {
		t.Fatalf("command result = %+v", session.sent[0])
	}
	// Canceled dispatches stay out of the conversation.
	session.sent = nil
	updated, _ = m.Update(aiCmdDoneMsg{argv: []string{"status"}, canceled: true})
	if _, ok := updated.(aiModel); !ok || len(session.sent) != 0 {
		t.Fatalf("canceled dispatch injected: %+v", session.sent)
	}
}

func TestAIContextSwitchEventRefreshesIdentity(t *testing.T) {
	m := aiTestModel(t)
	m.session = newFakeAISession()
	if err := os.WriteFile(customerContextPath(m.configDir), []byte("globex.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, cmd := aiEventUpdate(m, aiEvent{ContextSwitched: &aiContextSwitched{From: "acme.com", To: "globex.com", By: "agent"}})
	if cmd == nil {
		t.Fatal("context switch printed nothing")
	}
	if !strings.Contains(m.identity, "globex.com") {
		t.Fatalf("identity = %q", m.identity)
	}
}

func TestAIModelVerb(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/model claude-sonnet-5")
	updated, _ := aiPress(m, tea.KeyEnter)
	m = updated
	if !strings.Contains(aiTranscriptText(m), "Model set to claude-sonnet-5") {
		t.Fatal("/model confirmation missing from the transcript")
	}
	if m.modelName != "claude-sonnet-5" {
		t.Fatalf("modelName = %q", m.modelName)
	}
	if got := loadAISettings(m.configDir).Model; got != "claude-sonnet-5" {
		t.Fatalf("persisted model = %q", got)
	}
	m = aiType(m, "/model gpt-4")
	updated, _ = aiPress(m, tea.KeyEnter)
	m = updated
	if m.modelName != "claude-sonnet-5" {
		t.Fatalf("invalid model applied: %q", m.modelName)
	}
}

func TestAIChatWithoutKeyExplains(t *testing.T) {
	m := aiTestModel(t)
	if m.session != nil {
		t.Fatal("keyless model built a session")
	}
	if !strings.Contains(m.sessionNote, "ANTHROPIC_API_KEY") {
		t.Fatalf("session note = %q", m.sessionNote)
	}
}

func TestAIKeyOnboardingFlow(t *testing.T) {
	m := aiTestModel(t)
	fake := newFakeAISession()
	oldFactory := newAIConversationSession
	newAIConversationSession = func(configDir, apiKey, model string, catalog []aiCatalogEntry) conversationSession {
		if apiKey != "sk-ant-test-0123456789" {
			t.Fatalf("factory received key %q", apiKey)
		}
		return fake
	}
	t.Cleanup(func() { newAIConversationSession = oldFactory })

	// A question without a key opens the guided setup, stashing the question.
	m = aiType(m, "why is spend up?")
	updated, _ := aiPress(m, tea.KeyEnter)
	m = updated
	if !m.keyEntry {
		t.Fatal("keyless chat did not open key entry")
	}
	if !strings.Contains(aiTranscriptText(m), "sent to Anthropic's API under this key") {
		t.Fatal("disclosure missing from the transcript")
	}
	if m.pendingQuestion != "why is spend up?" {
		t.Fatalf("pending question = %q", m.pendingQuestion)
	}
	if view := m.View(); !strings.Contains(view, "API key:") {
		t.Fatalf("key entry view = %q", view)
	}

	// A bad paste is rejected and the mode stays open.
	m = aiType(m, "not-a-key")
	updated, _ = aiPress(m, tea.KeyEnter)
	m = updated
	if !m.keyEntry {
		t.Fatal("invalid key closed the setup")
	}
	if masked := m.View(); strings.Contains(masked, "not-a-key") {
		t.Fatal("key text rendered unmasked")
	}

	// Clear it and paste a valid key: saved, session built, question sent.
	for range "not-a-key" {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = updated.(aiModel)
	}
	m = aiType(m, "sk-ant-test-0123456789")
	updated, cmd := aiPress(m, tea.KeyEnter)
	m = updated
	if m.keyEntry || m.session == nil || cmd == nil {
		t.Fatalf("valid key not accepted (keyEntry=%v session=%v)", m.keyEntry, m.session != nil)
	}
	if got := loadAISettings(m.configDir).APIKey; got != "sk-ant-test-0123456789" {
		t.Fatalf("persisted key = %q", got)
	}
	if len(fake.sent) != 1 || fake.sent[0].Kind != aiInputChat || fake.sent[0].Text != "why is spend up?" {
		t.Fatalf("pending question not sent: %+v", fake.sent)
	}
}

func TestAIKeyOnboardingEscCancels(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "hello")
	updated, _ := aiPress(m, tea.KeyEnter)
	m = updated
	m = aiType(m, "sk-partial")
	updated2, _ := aiPress(m, tea.KeyEsc)
	m = updated2
	if m.keyEntry || m.keyBuf != "" || m.pendingQuestion != "" {
		t.Fatalf("esc did not fully cancel: %+v", m)
	}
	if !strings.Contains(aiTranscriptText(m), "key setup canceled") {
		t.Fatal("cancel note missing from the transcript")
	}
	if loadAISettings(m.configDir).APIKey != "" {
		t.Fatal("canceled setup persisted a key")
	}
}

func TestAIValidateAPIKey(t *testing.T) {
	for _, bad := range []string{"", "sk-short", "notakey-0123456789012345", "sk-ant with space 12345"} {
		if err := aiValidateAPIKey(bad); err == nil {
			t.Fatalf("key %q accepted", bad)
		}
	}
	if err := aiValidateAPIKey("sk-ant-api03-0123456789"); err != nil {
		t.Fatal(err)
	}
}

func TestAIFriendlyAPIError(t *testing.T) {
	dir := t.TempDir()
	raw401 := `POST "https://api.anthropic.com/v1/messages": 401 Unauthorized {"type":"error","error":{"type":"authentication_error","message":"API key is invalid."}}`

	t.Setenv("ANTHROPIC_API_KEY", "sk-bogus")
	if got := aiFriendlyAPIError(dir, raw401); !strings.Contains(got, "ANTHROPIC_API_KEY environment variable") {
		t.Fatalf("env-key 401 hint = %q", got)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := aiFriendlyAPIError(dir, raw401); !strings.Contains(got, aiSettingsFileName) {
		t.Fatalf("saved-key 401 hint = %q", got)
	}
	if got := aiFriendlyAPIError(dir, "429 rate_limit_error"); !strings.Contains(got, "rate limit") {
		t.Fatalf("429 hint = %q", got)
	}
	if got := aiFriendlyAPIError(dir, "something novel"); got != "something novel" {
		t.Fatalf("unknown error rewritten: %q", got)
	}
}

func TestAIUnknownSlashNeverDispatches(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/lst-budgets")
	updated, _ := aiPress(m, tea.KeyEnter)
	m = updated
	if m.running != nil {
		t.Fatal("unknown slash command dispatched a subprocess")
	}
	if !strings.Contains(aiTranscriptText(m), "unknown command: /lst-budgets") {
		t.Fatal("unknown-command note missing from the transcript")
	}
}

// aiTranscriptText flattens the transcript for content assertions.
func aiTranscriptText(m aiModel) string {
	return strings.Join(m.transcript, "\n")
}

func TestAITranscriptCapAndExport(t *testing.T) {
	m := aiTestModel(t)
	for i := 0; i < aiTranscriptBlocks+25; i++ {
		m.append("block")
	}
	if len(m.transcript) != aiTranscriptBlocks {
		t.Fatalf("transcript = %d blocks, want cap %d", len(m.transcript), aiTranscriptBlocks)
	}

	dir := t.TempDir()
	path, err := aiExportTranscript([]string{aiEchoStyle.Render("› /status"), "plain"}, []string{dir + "/out.txt"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "› /status\n\nplain\n" {
		t.Fatalf("export = %q, want ANSI stripped and blank-line separated", data)
	}
	if _, err := aiExportTranscript(nil, []string{"a", "b"}, time.Now()); err == nil {
		t.Fatal("two-arg export accepted")
	}
}

func TestAIScrollKeysAndFollow(t *testing.T) {
	m := aiTestModel(t)
	m.height = 10
	m.layout()
	for i := 0; i < 50; i++ {
		m.append("line")
	}
	if !m.follow || !m.view.AtBottom() {
		t.Fatal("viewport not following the bottom after appends")
	}
	m, _ = aiPress(m, tea.KeyPgUp)
	if m.follow {
		t.Fatal("PgUp did not release follow mode")
	}
	if !strings.Contains(m.topRule(), "scrolled up") {
		t.Fatalf("top rule = %q, want scroll hint", m.topRule())
	}
	for i := 0; i < 20; i++ {
		m, _ = aiPress(m, tea.KeyPgDown)
	}
	if !m.follow {
		t.Fatal("PgDn to the bottom did not restore follow mode")
	}
}

func TestAIInputLineHasNoBackgroundBand(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/sta")
	rendered := m.input.View()
	if strings.Contains(rendered, "\x1b[48;") || strings.Contains(rendered, "\x1b[47m") || strings.Contains(rendered, "\x1b[107m") {
		t.Fatalf("input line paints a background band: %q", rendered)
	}
}

func TestAIBannerAndFrame(t *testing.T) {
	m := aiTestModel(t)
	banner := aiTranscriptText(m)
	for _, want := range []string{"Cloud Intelligence™ CLI", "dev build", "AI off", "commands"} {
		if !strings.Contains(banner, want) {
			t.Fatalf("banner missing %q: %s", want, banner)
		}
	}
	view := m.View()
	if got := strings.Count(view, strings.Repeat("─", 10)); got < 2 {
		t.Fatalf("view has %d rule lines, want the two around the input", got)
	}
	if strings.Contains(m.input.View(), "Ask about") {
		t.Fatal("placeholder text still renders — the input should show a bare cursor")
	}
}

func TestParseTokenClaims(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"DoitEmployee":true,"email":"vadim@doit.com"}`))
	claims, ok := parseTokenClaims("h." + payload + ".s")
	if !ok || !claims.DoitEmployee || claims.Email != "vadim@doit.com" {
		t.Fatalf("claims = %+v ok=%v", claims, ok)
	}
	if _, ok := parseTokenClaims("not-a-jwt"); ok {
		t.Fatal("malformed token parsed")
	}
	if _, ok := parseTokenClaims(""); ok {
		t.Fatal("empty token parsed")
	}
}

func TestAIContextLabelWithResolvedName(t *testing.T) {
	m := aiTestModel(t)
	if err := os.WriteFile(customerContextPath(m.configDir), []byte("RSTDkHhaoGWwOEvlYlHyBUhm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := m.contextLabel(); got != "RSTDkHhaoGWwOEvlYlHyBUhm" {
		t.Fatalf("label before lookup = %q", got)
	}
	updated, _ := m.Update(aiCustomerNameMsg{context: "RSTDkHhaoGWwOEvlYlHyBUhm", name: "Acme Corp"})
	m = updated.(aiModel)
	if got := m.identity; got != "Acme Corp (RSTDkHhaoGWwOEvlYlHyBUhm)" {
		t.Fatalf("label after lookup = %q", got)
	}
	if !strings.Contains(m.transcript[0], "Acme Corp") {
		t.Fatal("banner not refreshed with the resolved name")
	}
	// A stale lookup for a different context is dropped.
	updated, _ = m.Update(aiCustomerNameMsg{context: "other", name: "Wrong"})
	m = updated.(aiModel)
	if strings.Contains(m.identity, "Wrong") {
		t.Fatal("stale lookup applied")
	}
}

func TestAICustomerNameFromJSON(t *testing.T) {
	if got := aiCustomerNameFromJSON([]byte(`{"name":"Acme Corp","id":"x"}`)); got != "Acme Corp" {
		t.Fatalf("name = %q", got)
	}
	if got := aiCustomerNameFromJSON([]byte(`{"primaryDomain":"acme.com"}`)); got != "acme.com" {
		t.Fatalf("domain fallback = %q", got)
	}
	if got := aiCustomerNameFromJSON([]byte(`not json`)); got != "" {
		t.Fatalf("garbage = %q", got)
	}
}

func TestAIMouseToggle(t *testing.T) {
	m := aiTestModel(t)
	if m.mouseOn {
		t.Fatal("mouse capture must default off so terminal selection/copy works")
	}
	m = aiType(m, "/mouse")
	updated, cmd := aiPress(m, tea.KeyEnter)
	m = updated
	if !m.mouseOn || cmd == nil {
		t.Fatalf("first /mouse: on=%v cmd=%v", m.mouseOn, cmd)
	}
	m = aiType(m, "/mouse")
	updated, cmd = aiPress(m, tea.KeyEnter)
	m = updated
	if m.mouseOn || cmd == nil {
		t.Fatalf("second /mouse: on=%v cmd=%v", m.mouseOn, cmd)
	}
}

func TestAIPickerFetchesWhenCacheEmpty(t *testing.T) {
	m := aiTestModel(t)
	oldIndex := resolutionIndex
	resolutionIndex = map[string]resolutionListTarget{
		"get-report": {resource: "reports", listPath: "/reports", listOperation: "list-reports"},
	}
	oldFetch := resolverListFetch
	fetched := false
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		fetched = true
		return resolverListResult{entries: []nameCacheEntry{
			{ID: "AAAAAAAAAAAAAAAAAAA1", Name: "BigQuery Storage costs"},
			{ID: "AAAAAAAAAAAAAAAAAAA2", Name: "BigQuery Storage type"},
		}}, nil
	}
	t.Cleanup(func() { resolutionIndex = oldIndex; resolverListFetch = oldFetch })
	m.catalog = append(m.catalog, aiCatalogEntry{Path: "get-report", Summary: "Get a report"})

	// No cache: submitting arms the fetch instead of dispatching.
	m = aiType(m, "/get-report")
	updated, cmd := aiPress(m, tea.KeyEnter)
	m = updated
	if m.fetchIntent == nil || m.running != nil || cmd == nil {
		t.Fatalf("fetch not armed (intent=%v running=%v)", m.fetchIntent != nil, m.running != nil)
	}
	if !strings.Contains(m.statusLine(), "fetching report names") {
		t.Fatalf("status = %q", m.statusLine())
	}

	// Deliver the fetch result: the picker opens over the fetched names.
	intent := m.fetchIntent
	result, err := resolverListFetch("", "", 0)
	if err != nil || !fetched {
		t.Fatal("stub fetch broken")
	}
	updated2, _ := m.Update(aiNamesFetchedMsg{intent: intent, entries: result.entries})
	m = updated2.(aiModel)
	if m.picker == nil || len(m.picker.candidates) != 2 {
		t.Fatalf("picker not opened from fetched names: %+v", m.picker)
	}

	// Esc during a fetch abandons it and the late result is dropped.
	m.closePicker()
	m = aiType(m, "/get-report")
	m, _ = aiPress(m, tea.KeyEnter)
	stale := m.fetchIntent
	m, _ = aiPress(m, tea.KeyEsc)
	if m.fetchIntent != nil {
		t.Fatal("esc did not abandon the fetch")
	}
	updated3, _ := m.Update(aiNamesFetchedMsg{intent: stale, entries: result.entries})
	m = updated3.(aiModel)
	if m.picker != nil {
		t.Fatal("late fetch result opened a picker after cancel")
	}
}

func TestAIPickerFetchErrorDispatchesOriginal(t *testing.T) {
	m := aiTestModel(t)
	oldIndex := resolutionIndex
	resolutionIndex = map[string]resolutionListTarget{
		"get-report": {resource: "reports", listPath: "/reports", listOperation: "list-reports"},
	}
	t.Cleanup(func() { resolutionIndex = oldIndex })
	m.catalog = append(m.catalog, aiCatalogEntry{Path: "get-report", Summary: "Get a report"})

	m = aiType(m, "/get-report bigquery")
	m, _ = aiPress(m, tea.KeyEnter)
	intent := m.fetchIntent
	if intent == nil {
		t.Fatal("fetch not armed")
	}
	updated, _ := m.Update(aiNamesFetchedMsg{intent: intent, err: os.ErrDeadlineExceeded})
	m = updated.(aiModel)
	if m.running == nil {
		t.Fatal("fetch error did not dispatch the original command")
	}
	if got := strings.Join(m.running.argv, " "); got != "get-report bigquery" {
		t.Fatalf("dispatched argv = %q", got)
	}
	m.running.cancel()
}

func TestRenderAIMarkdownUsesFullTerminalWidth(t *testing.T) {
	// Wide terminals get wide answers: the old hard 100-column cap squeezed
	// markdown tables into wrapped cells no matter how much room there was.
	table := "| Source | Savings |\n|---|---|\n| Flexsave (DoiT-managed AWS commitments) on a very long descriptive label for coverage | $1,806.62 across the whole of July 2026 as measured |\n"
	wide := renderAIMarkdown(table, 220, "dark")
	maxLine := 0
	for _, line := range strings.Split(stripANSI(wide), "\n") {
		if n := len([]rune(line)); n > maxLine {
			maxLine = n
		}
	}
	if maxLine <= 100 {
		t.Fatalf("table still wrapped at the old 100-col cap: widest line %d", maxLine)
	}
	// glamour v0.6's tablewriter hard-capped every cell at 30 characters —
	// a 36-char label must now survive on one line when the width allows.
	if !strings.Contains(stripANSI(wide), "Flexsave (AWS commitment automation)") {
		wide = renderAIMarkdown("| Source | Savings |\n|---|---|\n| Flexsave (AWS commitment automation) | $1,807 |\n", 200, "dark")
		if !strings.Contains(stripANSI(wide), "Flexsave (AWS commitment automation)") {
			t.Fatal("36-char table cell still wraps (tablewriter 30-col cap)")
		}
	}
	// Unknown width (first render, tests) keeps a sane default.
	if got := renderAIMarkdown("plain text", 0, "dark"); got == "" {
		t.Fatal("zero width must fall back, not fail")
	}
}

func TestAITranscriptBlocksSeparatedByBlankLine(t *testing.T) {
	// A question echo and the previous answer were rendered back-to-back with
	// no gap; blocks now join with a blank line so turns are scannable.
	m := aiTestModel(t)
	m.append("answer block one")
	m.append("› next question")
	if !strings.Contains(m.view.View(), "answer block one") {
		t.Fatal("viewport missing first block")
	}
	content := strings.Join(m.transcript, "\n\n")
	if !strings.Contains(content, "answer block one\n\n› next question") {
		t.Fatalf("blocks not blank-line separated: %q", content)
	}
}

// aiFrameSized renders the session at a fixed terminal size.
func aiFrameSized(t *testing.T, m aiModel, width, height int) (aiModel, []string) {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	sized := next.(aiModel)
	return sized, strings.Split(sized.View(), "\n")
}

// The frame must be exactly the terminal's cell grid in every state. Bubble
// Tea's renderer rewrites only the lines whose content changed, so a frame
// that leaves cells unclaimed cannot be restored by a repaint — and a line
// running past the right edge is a cut nobody chose.
func TestAIFrameMatchesTerminalGrid(t *testing.T) {
	sizes := [][2]int{{200, 50}, {120, 30}, {100, 30}, {80, 24}, {60, 20}, {40, 12}, {30, 8}, {24, 6}, {20, 3}}
	states := map[string]func(m *aiModel){
		"idle": func(m *aiModel) {},
		"turn with long narration": func(m *aiModel) {
			m.turnActive = true
			m.turnStarted = time.Now()
			m.turnCommands = 3
			m.turnActivity = "thinking · " + strings.Repeat("narration ", 12)
		},
		"long identity": func(m *aiModel) {
			m.identity = "A Very Long Customer Display Name Ltd (RSTDkHhaoGWwOEvlYlHyBUhm)"
		},
		"completion popup": func(m *aiModel) {
			m.completions = []aiCompletion{
				{Value: "cloud-analytics-reports-list", Summary: strings.Repeat("a long command summary ", 4)},
				{Value: "b", Summary: "short"},
			}
		},
		"scrolled up": func(m *aiModel) { m.follow = false },
		"picker": func(m *aiModel) {
			m.picker = &aiNameSelection{resource: "customer", candidates: []nameCacheEntry{
				{Name: strings.Repeat("Long Customer Name ", 4), ID: "RSTDkHhaoGWwOEvlYlHyBUhm"},
			}}
		},
		"wide transcript block": func(m *aiModel) { m.append(strings.Repeat("x", 400)) },
	}
	for name, mutate := range states {
		for _, size := range sizes {
			width, height := size[0], size[1]
			model := aiTestModel(t)
			mutate(&model)
			model.layout()
			_, lines := aiFrameSized(t, model, width, height)
			if len(lines) != height {
				t.Errorf("%s at %dx%d: frame has %d lines, want %d", name, width, height, len(lines), height)
			}
			for index, line := range lines {
				if got := lipgloss.Width(line); got > width {
					t.Errorf("%s at %dx%d: line %d is %d cells wide: %q",
						name, width, height, index, got, stripANSI(line))
				}
			}
		}
	}
}

// A pane too short for the chrome keeps what the user acts through: the input
// and the status line. Transcript rows are what give way.
func TestAIFrameTooShortKeepsInputAndStatus(t *testing.T) {
	m := aiTestModel(t)
	m.session = newFakeAISession()
	_, lines := aiFrameSized(t, m, 60, 4)
	if len(lines) != 4 {
		t.Fatalf("frame has %d lines, want 4", len(lines))
	}
	frame := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(frame, "›") {
		t.Errorf("input line dropped from a short frame:\n%s", frame)
	}
	if !strings.Contains(frame, "Ask \"how much") {
		t.Errorf("status line dropped from a short frame:\n%s", frame)
	}
}

// ctrl+l is the repair gesture: it must reach the renderer from every state,
// including the ones that otherwise own the keyboard.
func TestAICtrlLRedrawsFromEveryState(t *testing.T) {
	states := map[string]func(m *aiModel){
		"idle":     func(m *aiModel) {},
		"keyEntry": func(m *aiModel) { m.keyEntry = true },
		"picker": func(m *aiModel) {
			m.picker = &aiNameSelection{resource: "customer", candidates: []nameCacheEntry{{Name: "Acme", ID: "abc"}}}
		},
		"fetching":         func(m *aiModel) { m.fetchIntent = &aiPickerIntent{resource: "customer"} },
		"dispatchApproval": func(m *aiModel) { m.dispatchApproval = &aiDispatchApproval{argv: []string{"delete"}} },
		"approval":         func(m *aiModel) { m.approval = &aiApprovalRequest{} },
	}
	for name, mutate := range states {
		m := aiTestModel(t)
		mutate(&m)
		_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlL})
		if cmd == nil {
			t.Fatalf("%s: ctrl+l produced no command", name)
		}
		if got := cmd(); got != tea.ClearScreen() {
			t.Fatalf("%s: ctrl+l sent %T, want a full repaint", name, got)
		}
	}
}

// The status line trims the narration, not the affordances: on a narrow pane
// the elapsed time and "esc to cancel" survive a long snippet.
func TestAIStatusLineTrimsNarrationNotAffordances(t *testing.T) {
	m := aiTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	m = next.(aiModel)
	m.turnActive = true
	m.turnStarted = time.Now().Add(-90 * time.Second)
	m.turnCommands = 2
	m.turnActivity = "thinking · " + strings.Repeat("a long reasoning snippet ", 6)

	status := stripANSI(m.statusLine())
	if lipgloss.Width(status) > 70 {
		t.Fatalf("status line is %d cells wide on a 70-column pane: %q", lipgloss.Width(status), status)
	}
	for _, want := range []string{"1m30s", "2 commands", "esc to cancel", "…"} {
		if !strings.Contains(status, want) {
			t.Errorf("status line lost %q: %q", want, status)
		}
	}
}

func TestAITrimTo(t *testing.T) {
	cases := []struct {
		text  string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"truncate me here", 10, "truncate…"},
		{"trailing space cut", 6, "trail…"},
		{"no room", 3, ""},
		{"no room", 0, ""},
	}
	for _, c := range cases {
		if got := aiTrimTo(c.text, c.width); got != c.want {
			t.Errorf("aiTrimTo(%q, %d) = %q, want %q", c.text, c.width, got, c.want)
		}
	}
}

// layout() sizes the viewport from pickerLines(), so that count must be
// exactly what pickerView() draws. An empty filter result is the case that
// disagreed: the "no matches" note takes a row of its own.
func TestAIPickerLinesMatchesPickerView(t *testing.T) {
	candidates := make([]nameCacheEntry, 12)
	for i := range candidates {
		candidates[i] = nameCacheEntry{Name: fmt.Sprintf("Customer %02d", i), ID: fmt.Sprintf("id%02d", i)}
	}
	cases := []struct {
		name   string
		filter string
		index  int
	}{
		{"no matches", "zzz-nothing-matches", 0},
		{"one match", "Customer 07", 0},
		{"full window", "", 0},
		{"window scrolled to the end", "", 11},
		{"stale index past a narrowed filter", "Customer 0", 11},
	}
	for _, c := range cases {
		m := aiTestModel(t)
		m.picker = &aiNameSelection{resource: "customer", candidates: candidates}
		m.pickerFilter = c.filter
		m.pickerIndex = c.index
		want := len(strings.Split(m.pickerView(), "\n"))
		if got := m.pickerLines(); got != want {
			t.Errorf("%s: pickerLines() = %d, pickerView() draws %d lines", c.name, got, want)
		}
	}
}

// With the picker open the frame must still be exactly the terminal grid, and
// the status line must still be its last row — no transcript row sliced off
// the top, no padding below the status.
func TestAIPickerFrameKeepsGrid(t *testing.T) {
	for _, filter := range []string{"", "Acme", "zzz-nothing-matches"} {
		m := aiTestModel(t)
		m.append("a transcript line that must not be sliced away")
		m.picker = &aiNameSelection{resource: "customer", candidates: []nameCacheEntry{
			{Name: "Acme", ID: "abc"}, {Name: "Globex", ID: "def"},
		}}
		m.pickerFilter = filter
		m, lines := aiFrameSized(t, m, 80, 20)
		if len(lines) != 20 {
			t.Errorf("filter %q: frame has %d lines, want 20", filter, len(lines))
		}
		if last := stripANSI(lines[len(lines)-1]); !strings.Contains(last, "esc cancel") {
			t.Errorf("filter %q: last frame row is %q, want the status line", filter, last)
		}
		if !strings.Contains(stripANSI(strings.Join(lines, "\n")), "must not be sliced away") {
			t.Errorf("filter %q: transcript row dropped from the frame", filter)
		}
	}
}

// Every status-line state keeps the affordance that gets the user out of it,
// however narrow the pane.
func TestAIStatusLineKeepsAffordanceWhenNarrow(t *testing.T) {
	states := []struct {
		name   string
		want   string
		mutate func(m *aiModel)
	}{
		{"picker", "esc cancel", func(m *aiModel) {
			m.picker = &aiNameSelection{resource: "customer", candidates: []nameCacheEntry{{Name: "Acme", ID: "abc"}}}
		}},
		{"approval", "n decline", func(m *aiModel) { m.approval = &aiApprovalRequest{} }},
		{"dispatch approval", "n decline", func(m *aiModel) {
			m.dispatchApproval = &aiDispatchApproval{argv: []string{"delete-budget"}}
		}},
		{"fetching names", "esc to cancel", func(m *aiModel) {
			m.fetchIntent = &aiPickerIntent{resource: "customer"}
		}},
		{"key entry", "esc cancel", func(m *aiModel) { m.keyEntry = true }},
		{"running a command", "esc to cancel", func(m *aiModel) {
			m.running = &aiRunState{argv: []string{"list-cloud-analytics-reports", "--limit", "50"}, started: time.Now()}
		}},
		{"turn in flight", "esc to cancel", func(m *aiModel) {
			m.turnActive = true
			m.turnStarted = time.Now()
			m.turnActivity = strings.Repeat("narration ", 10)
		}},
	}
	for _, width := range []int{80, 48, 30, 24} {
		for _, state := range states {
			m := aiTestModel(t)
			state.mutate(&m)
			sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
			m = sized.(aiModel)
			status := stripANSI(m.statusLine())
			if !strings.Contains(status, state.want) {
				t.Errorf("%s at %d columns: status %q lost %q", state.name, width, status, state.want)
			}
			if got := lipgloss.Width(status); got > width {
				t.Errorf("%s at %d columns: status is %d cells wide: %q", state.name, width, got, status)
			}
		}
	}
}

func TestAIEnterAcceptsCompletionLikeTab(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/li")
	m, _ = aiPress(m, tea.KeyEnter)
	if got := m.input.Value(); got != "/list-anomalies " {
		t.Fatalf("input after enter = %q, want the accepted completion", got)
	}
	if len(m.completions) != 0 {
		t.Fatalf("popup still open after enter: %+v", m.completions)
	}
	if len(m.history) != 0 {
		t.Fatalf("enter submitted the partial token: history = %v", m.history)
	}
}

func TestAIEnterSubmitsExactCompletion(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/help")
	if len(m.completions) == 0 {
		t.Fatal("expected the popup for /help")
	}
	m, _ = aiPress(m, tea.KeyEnter)
	if got := m.input.Value(); got != "" {
		t.Fatalf("input after enter = %q, want submitted (empty)", got)
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != "/help" {
		t.Fatalf("exact command not submitted: history = %v", m.history)
	}
}

func TestAIBellWorthy(t *testing.T) {
	if aiBellWorthy(aiTurnDone{Wall: time.Second}) {
		t.Fatal("a fast toolless turn must not ring")
	}
	if !aiBellWorthy(aiTurnDone{ToolCalls: 1, Wall: time.Second}) {
		t.Fatal("a turn that ran commands rings")
	}
	if !aiBellWorthy(aiTurnDone{Wall: 5 * time.Second}) {
		t.Fatal("a long thinking turn rings")
	}
}

func TestAIBellToggleAndPersist(t *testing.T) {
	m := aiTestModel(t)
	if !m.bellOn {
		t.Fatal("bell must default on")
	}
	m = aiType(m, "/bell")
	m, _ = aiPress(m, tea.KeyEnter)
	if m.bellOn {
		t.Fatal("first /bell should turn the bell off")
	}
	if resolveAIBell(loadAISettings(m.configDir)) {
		t.Fatal("bell off not persisted")
	}
	m = aiType(m, "/bell")
	m, _ = aiPress(m, tea.KeyEnter)
	if !m.bellOn || !resolveAIBell(loadAISettings(m.configDir)) {
		t.Fatal("second /bell should restore and persist on")
	}
}

func TestAIMouseTogglePersists(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/mouse")
	m, _ = aiPress(m, tea.KeyEnter)
	if !resolveAIMouse(loadAISettings(m.configDir)) {
		t.Fatal("mouse on not persisted")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if again := newAIModel(m.configDir); !again.mouseOn {
		t.Fatal("a new session should restore the persisted mouse choice")
	}
}

func TestAIKeylessSessionOpensKeySetup(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	m := newAIModel(t.TempDir())
	if !m.keyEntry {
		t.Fatal("a keyless session should open in the guided key setup")
	}
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "Esc skips this") {
		t.Fatalf("startup setup lacks the skip hint: %q", joined)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(aiModel)
	if m.keyEntry {
		t.Fatal("esc should drop to a normal session")
	}
}

func TestAISessionWithKeySkipsStartupKeySetup(t *testing.T) {
	old := newAIConversationSession
	newAIConversationSession = func(configDir, apiKey, model string, catalog []aiCatalogEntry) conversationSession {
		return newFakeAISession()
	}
	defer func() { newAIConversationSession = old }()
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	if m := newAIModel(t.TempDir()); m.keyEntry {
		t.Fatal("a keyed session must not open in key setup")
	}
}
