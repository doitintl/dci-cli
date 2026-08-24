package main

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// aiTestModel builds a session model without touching cli.Root, the real
// config dir, or the Claude API (no key), with a fixed catalog.
func aiTestModel(t *testing.T) aiModel {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	model := newAIModel(t.TempDir())
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
	if !strings.Contains(idle, "doer · acme.com") || !strings.Contains(idle, "/help") {
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

func TestAITurnLifecycleEvents(t *testing.T) {
	m := aiTestModel(t)
	m.session = newFakeAISession()

	m, _ = aiEventUpdate(m, aiEvent{TurnStarted: &aiTurnStarted{TurnID: "t1"}})
	if !m.turnActive {
		t.Fatal("TurnStarted did not activate the turn")
	}
	m, _ = aiEventUpdate(m, aiEvent{TextDelta: &aiTextDelta{TurnID: "t1", Text: "Spend rose because "}})
	m, _ = aiEventUpdate(m, aiEvent{TextDelta: &aiTextDelta{TurnID: "t1", Text: "of GKE."}})
	if got := m.streamBuf.String(); got != "Spend rose because of GKE." {
		t.Fatalf("stream buffer = %q", got)
	}
	if view := m.View(); !strings.Contains(view, "Spend rose because of GKE.") {
		t.Fatal("streamed text not visible in the managed region")
	}
	m, cmd := aiEventUpdate(m, aiEvent{TurnDone: &aiTurnDone{TurnID: "t1", InputTokens: 10}})
	if m.turnActive {
		t.Fatal("TurnDone left the turn active")
	}
	if cmd == nil {
		t.Fatal("TurnDone did not commit the narration")
	}
	if m.streamBuf.Len() != 0 {
		t.Fatal("stream buffer not reset after commit")
	}
	if m.lastUsage == nil || m.lastUsage.InputTokens != 10 {
		t.Fatalf("usage not recorded: %+v", m.lastUsage)
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
	if string(data) != "› /status\nplain\n" {
		t.Fatalf("export = %q, want ANSI stripped", data)
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
	if !strings.Contains(m.headerLine(), "scrolled up") {
		t.Fatalf("header = %q, want scroll hint", m.headerLine())
	}
	for i := 0; i < 20; i++ {
		m, _ = aiPress(m, tea.KeyPgDown)
	}
	if !m.follow {
		t.Fatal("PgDn to the bottom did not restore follow mode")
	}
}
