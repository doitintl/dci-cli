package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// aiTestModel builds a session model without touching cli.Root or the real
// config dir, with a fixed catalog.
func aiTestModel(t *testing.T) aiModel {
	t.Helper()
	model := newAIModel(t.TempDir())
	model.catalog = aiTestCatalog()
	return model
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

	updated2, cmd := m.Update(aiCmdDoneMsg{argv: []string{"status"}, output: "ok", elapsed: time.Second})
	m = updated2.(aiModel)
	if m.running != nil {
		t.Fatal("running state not cleared by aiCmdDoneMsg")
	}
	if cmd == nil {
		t.Fatal("result card not printed")
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
	updated, cmd := aiPress(m, tea.KeyEnter)
	m = updated
	if m.running != nil {
		t.Fatal("chat input must not dispatch a subprocess")
	}
	if cmd == nil {
		t.Fatal("chat notice not printed")
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
	updated, cmd := aiPress(m, tea.KeyEnter)
	m = updated
	if cmd == nil {
		t.Fatal("/customer returned no command")
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

func TestAIUnknownSlashNeverDispatches(t *testing.T) {
	m := aiTestModel(t)
	m = aiType(m, "/lst-budgets")
	updated, cmd := aiPress(m, tea.KeyEnter)
	m = updated
	if m.running != nil {
		t.Fatal("unknown slash command dispatched a subprocess")
	}
	if cmd == nil {
		t.Fatal("unknown slash printed nothing")
	}
}
