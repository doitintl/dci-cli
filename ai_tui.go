package main

// The `dci ai` session program (AI-SPEC §3, D9): a stable frame in the
// alternate screen — one-line header, the transcript in a scrolling
// viewport, input and status pinned at the bottom. The frame never moves;
// content scrolls inside the viewport (mouse wheel, PgUp/PgDn), following
// the bottom until the user scrolls up. Slash commands dispatch to the CLI
// itself as a subprocess (§7.4) and their results join the conversation
// (§4.4); plain text goes to the conversation session (ai_session.go),
// whose protocol events this program renders: streamed narration committed
// through glamour, tool cards, destructive approvals, tenant switches.
// Routing, completion, and history logic live in ai_slash.go. Kept in a
// sibling file per the AGENTS.md chapter-split guidance.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var (
	aiEchoStyle     = lipgloss.NewStyle().Faint(true)
	aiCardHeadStyle = lipgloss.NewStyle().Bold(true)
	aiErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	aiSelectedStyle = lipgloss.NewStyle().Reverse(true)
	aiNoticeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	aiAgentStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	aiApproveStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	aiHeaderStyle   = lipgloss.NewStyle().Bold(true)
)

const (
	aiCompletionLimit  = 6
	aiTranscriptBlocks = 500 // frame memory cap; /export preserves everything typed before it
)

// aiRunState tracks the one in-flight user slash dispatch; the session runs
// at most one command at a time, matching the one-conversation mental model.
type aiRunState struct {
	argv     []string
	cancel   context.CancelFunc
	started  time.Time
	customer string
}

// aiCmdDoneMsg reports a finished user dispatch back into the Update loop.
type aiCmdDoneMsg struct {
	argv     []string
	output   string
	exitCode int
	runErr   string
	canceled bool
	elapsed  time.Duration
	customer string
}

// aiSessionEventMsg wraps one protocol event for the Update loop.
type aiSessionEventMsg struct{ event aiEvent }

// aiSessionClosedMsg reports the events channel closing.
type aiSessionClosedMsg struct{}

type aiModel struct {
	configDir    string
	catalog      []aiCatalogEntry
	userCommands map[string]aiUserCommand

	input      textarea.Model
	view       viewport.Model
	spin       spinner.Model
	width      int
	height     int
	follow     bool // stick the viewport to the bottom on new content
	transcript []string

	completions     []aiCompletion
	completionIndex int

	history    []string
	historyPos int
	draft      string

	running    *aiRunState
	ctrlCArmed bool
	identity   string

	// Conversation state. session is nil when no API key is configured;
	// sessionNote then explains what is missing.
	session     conversationSession
	sessionNote string
	modelName   string
	turnActive  bool
	streamBuf   strings.Builder
	approval    *aiApprovalRequest
	lastUsage   *aiTurnDone

	// Guided key setup (P3): entering a key inline instead of editing files.
	keyEntry        bool
	keyBuf          string
	pendingQuestion string
}

// runAISession is the entry point behind `dci ai` (ai_command.go).
func runAISession(configDir string) error {
	program := tea.NewProgram(newAIModel(configDir), tea.WithAltScreen(), tea.WithMouseCellMotion())
	model, err := program.Run()
	if m, ok := model.(aiModel); ok && m.session != nil {
		_ = m.session.Close()
	}
	return err
}

func newAIModel(configDir string) aiModel {
	input := textarea.New()
	input.Prompt = "› "
	input.ShowLineNumbers = false
	input.CharLimit = 0
	input.SetHeight(1)
	input.KeyMap.InsertNewline.SetEnabled(false)
	// The default textarea styles paint a background band across the cursor
	// line (adaptive, and often guessed wrong inside the alt screen), leaving
	// gray-on-gray input. The session wants the terminal's own colors: no
	// band, bright text, faint placeholder.
	input.FocusedStyle.CursorLine = lipgloss.NewStyle()
	input.FocusedStyle.Text = lipgloss.NewStyle()
	input.FocusedStyle.Prompt = lipgloss.NewStyle().Bold(true)
	input.FocusedStyle.Placeholder = lipgloss.NewStyle().Faint(true)
	input.BlurredStyle = input.FocusedStyle
	input.Focus()

	history := loadAIHistory(configDir)
	catalog := aiSessionCatalog()
	settings := loadAISettings(configDir)
	modelName := resolveAIModel(settings)

	m := aiModel{
		configDir:    configDir,
		catalog:      catalog,
		userCommands: loadAIUserCommands(configDir),
		input:        input,
		view:         viewport.New(80, 20),
		spin:         spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		width:        80,
		height:       24,
		follow:       true,
		history:      history,
		historyPos:   len(history),
		identity:     aiIdentitySegment(configDir),
		modelName:    modelName,
	}
	if key := resolveAIKey(settings); key != "" {
		m.session = newAIConversationSession(configDir, key, modelName, catalog)
		m.input.Placeholder = "Ask about your cloud costs, or type / for commands"
	} else {
		m.sessionNote = "AI needs an Anthropic API key — ask a question to set one up, or export ANTHROPIC_API_KEY"
		m.input.Placeholder = "Ask a question to set up AI, or type / for commands"
	}
	m.append(aiNoticeStyle.Render("Cloud Intelligence™ interactive session") +
		aiEchoStyle.Render("  —  /help for commands, /quit to leave"))
	m.layout()
	return m
}

// aiIdentitySegment is the tenant half of the status line: doers always see
// their mode and context, non-doers see the context only when one is set —
// a single-tenant customer sees no tenant vocabulary at all (AI-SPEC §6.1).
func aiIdentitySegment(configDir string) string {
	context := readCustomerContext(configDir)
	if cachedTokenIsDoer() {
		if context == "" {
			return "doer · no customer context (/customer <name>)"
		}
		return "doer · " + context
	}
	return context
}

// newAIConversationSession builds the session behind a var so tests can
// substitute a fake without touching the Claude API.
var newAIConversationSession = func(configDir, apiKey, model string, catalog []aiCatalogEntry) conversationSession {
	return newLocalAISession(configDir, apiKey, model, catalog)
}

func (m aiModel) Init() tea.Cmd {
	if m.session != nil {
		return tea.Batch(textarea.Blink, aiListen(m.session))
	}
	return textarea.Blink
}

// aiListen delivers the next protocol event as a tea.Msg; the handler re-arms
// it, so exactly one listener is outstanding at a time.
func aiListen(session conversationSession) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-session.Events()
		if !ok {
			return aiSessionClosedMsg{}
		}
		return aiSessionEventMsg{event: event}
	}
}

// --- Transcript ---------------------------------------------------------------

// append adds one block to the transcript and refreshes the viewport,
// following the bottom unless the user has scrolled away.
func (m *aiModel) append(block string) {
	m.transcript = append(m.transcript, block)
	if len(m.transcript) > aiTranscriptBlocks {
		m.transcript = m.transcript[len(m.transcript)-aiTranscriptBlocks:]
	}
	m.refreshTranscript()
}

// refreshTranscript rebuilds the viewport content: committed blocks plus the
// live streaming tail.
func (m *aiModel) refreshTranscript() {
	content := strings.Join(m.transcript, "\n")
	if streaming := strings.TrimRight(m.streamBuf.String(), "\n"); streaming != "" {
		if content != "" {
			content += "\n"
		}
		content += aiAgentStyle.Render(streaming)
	}
	m.view.SetContent(content)
	if m.follow {
		m.view.GotoBottom()
	}
}

// layout recomputes the frame: the viewport takes everything the fixed
// chrome (header, completion popup, input, status) does not.
func (m *aiModel) layout() {
	m.input.SetWidth(m.width - 2)
	m.view.Width = m.width
	chrome := 1 /*header*/ + len(m.completions) + 1 /*input*/ + 1 /*status*/
	height := m.height - chrome
	if height < 3 {
		height = 3
	}
	m.view.Height = height
	m.refreshTranscript()
}

func (m aiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.view, cmd = m.view.Update(msg)
		m.follow = m.view.AtBottom()
		return m, cmd

	case spinner.TickMsg:
		if m.running == nil && !m.turnActive {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case aiCmdDoneMsg:
		m.running = nil
		m.append(renderAIRunCard(msg))
		// A finished user command joins the conversation so follow-up
		// questions can reference what is on screen (§4.4).
		if m.session != nil && !msg.canceled && msg.runErr == "" {
			_ = m.session.Send(aiUserInput{
				Kind: aiInputCommandResult, Argv: msg.argv, Output: msg.output, Customer: msg.customer,
			})
		}
		return m, nil

	case aiSessionEventMsg:
		return m.handleSessionEvent(msg.event)

	case aiSessionClosedMsg:
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleSessionEvent renders one protocol event and re-arms the listener.
func (m aiModel) handleSessionEvent(event aiEvent) (tea.Model, tea.Cmd) {
	commands := []tea.Cmd{aiListen(m.session)}
	switch {
	case event.TurnStarted != nil:
		m.turnActive = true
		m.streamBuf.Reset()
		m.lastUsage = nil
		commands = append(commands, m.spin.Tick)

	case event.TextDelta != nil:
		m.streamBuf.WriteString(event.TextDelta.Text)
		m.refreshTranscript()

	case event.ToolCallStarted != nil:
		m.commitStream()
		m.append(renderAIToolStart(*event.ToolCallStarted))

	case event.ToolResult != nil:
		m.append(renderAIToolResult(*event.ToolResult))

	case event.ApprovalRequest != nil:
		request := *event.ApprovalRequest
		m.approval = &request
		m.append(renderAIApprovalRequest(request))

	case event.ContextSwitched != nil:
		m.identity = aiIdentitySegment(m.configDir)
		m.append(aiNoticeStyle.Render(fmt.Sprintf(
			"customer context switched: %s → %s (by %s)",
			aiDisplayContext(event.ContextSwitched.From), event.ContextSwitched.To, event.ContextSwitched.By)))

	case event.LimitReached != nil:
		m.append(aiErrorStyle.Render(
			"stopped: the turn hit the " + event.LimitReached.Kind + " ceiling — ask a narrower question to continue"))

	case event.Error != nil:
		m.commitStream()
		message := event.Error.Message
		if message == "turn canceled" {
			m.append(aiEchoStyle.Render("canceled"))
		} else {
			m.append(aiErrorStyle.Render("AI error: " + aiFriendlyAPIError(m.configDir, message)))
		}

	case event.TurnDone != nil:
		m.commitStream()
		usage := *event.TurnDone
		m.lastUsage = &usage
		m.turnActive = false
		m.approval = nil
	}
	return m, tea.Batch(commands...)
}

// commitStream moves accumulated narration from the live tail into the
// transcript, rendered as markdown.
func (m *aiModel) commitStream() {
	text := strings.TrimSpace(m.streamBuf.String())
	m.streamBuf.Reset()
	if text == "" {
		m.refreshTranscript()
		return
	}
	m.append(renderAIMarkdown(text, m.view.Width))
}

func aiDisplayContext(context string) string {
	if context == "" {
		return "(unset)"
	}
	return context
}

func (m aiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// The guided key setup owns the keyboard while active (P3).
	if m.keyEntry {
		return m.handleKeyEntryKey(msg)
	}

	// Viewport scrolling works in every other state.
	switch key {
	case "pgup", "ctrl+u":
		m.view.HalfViewUp()
		m.follow = m.view.AtBottom()
		return m, nil
	case "pgdown", "ctrl+d":
		if key == "ctrl+d" && strings.TrimSpace(m.input.Value()) == "" && m.running == nil && m.approval == nil {
			return m, tea.Quit
		}
		m.view.HalfViewDown()
		m.follow = m.view.AtBottom()
		return m, nil
	}

	// A pending destructive approval owns the keyboard: y runs it, n or esc
	// declines. Nothing else falls through, so a queued keystroke can't
	// answer a question the user hasn't read.
	if m.approval != nil {
		switch key {
		case "y", "Y":
			return m.answerApproval(true)
		case "n", "N", "esc", "ctrl+c":
			return m.answerApproval(false)
		}
		return m, nil
	}

	// While a user slash command runs, the only inputs are the cancel keys;
	// everything else waits so a queued keystroke can't interleave with the
	// result card.
	if m.running != nil {
		if key == "esc" || key == "ctrl+c" {
			m.running.cancel()
		}
		return m, nil
	}

	if key != "ctrl+c" {
		m.ctrlCArmed = false
	}

	switch key {
	case "ctrl+c":
		if strings.TrimSpace(m.input.Value()) != "" {
			m.input.Reset()
			m.setCompletions(nil)
			m.ctrlCArmed = false
			return m, nil
		}
		if m.ctrlCArmed {
			return m, tea.Quit
		}
		m.ctrlCArmed = true
		return m, nil

	case "enter":
		return m.submit()

	case "tab":
		if len(m.completions) > 0 {
			selected := m.completions[m.completionIndex]
			m.input.Reset()
			m.input.SetValue("/" + selected.Value + " ")
			m.setCompletions(nil)
		}
		return m, nil

	case "up":
		if len(m.completions) > 0 {
			m.completionIndex = (m.completionIndex + len(m.completions) - 1) % len(m.completions)
			return m, nil
		}
		if m.historyPos > 0 {
			if m.historyPos == len(m.history) {
				m.draft = m.input.Value()
			}
			m.historyPos--
			m.setInput(m.history[m.historyPos])
		}
		return m, nil

	case "down":
		if len(m.completions) > 0 {
			m.completionIndex = (m.completionIndex + 1) % len(m.completions)
			return m, nil
		}
		if m.historyPos < len(m.history) {
			m.historyPos++
			if m.historyPos == len(m.history) {
				m.setInput(m.draft)
			} else {
				m.setInput(m.history[m.historyPos])
			}
		}
		return m, nil

	case "esc":
		if m.turnActive && m.session != nil {
			m.session.Cancel()
			return m, nil
		}
		m.input.Reset()
		m.setCompletions(nil)
		m.historyPos = len(m.history)
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.setCompletions(aiCompletionsFor(strings.TrimSpace(m.input.Value()), m.catalog, m.userCommands, aiCompletionLimit))
	return m, cmd
}

// setCompletions swaps the popup and relayouts, since the popup borrows rows
// from the viewport.
func (m *aiModel) setCompletions(completions []aiCompletion) {
	if len(completions) != len(m.completions) {
		m.completions = completions
		m.layout()
	} else {
		m.completions = completions
	}
	if m.completionIndex >= len(m.completions) {
		m.completionIndex = 0
	}
}

func (m aiModel) answerApproval(approved bool) (tea.Model, tea.Cmd) {
	request := m.approval
	m.approval = nil
	if m.session == nil || request == nil {
		return m, nil
	}
	_ = m.session.Send(aiUserInput{Kind: aiInputApproval, CallID: request.CallID, Approved: approved})
	answer := "declined"
	if approved {
		answer = "approved"
	}
	m.append(aiEchoStyle.Render("↳ " + answer))
	return m, nil
}

// setInput replaces the input content (history navigation), leaving the
// completion popup closed: recalled lines are complete, not being composed.
func (m *aiModel) setInput(value string) {
	m.input.Reset()
	m.input.SetValue(value)
	m.setCompletions(nil)
}

func (m aiModel) submit() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	route := aiRouteLine(line, m.catalog, m.userCommands)
	if route.kind == aiRouteEmpty {
		m.input.Reset()
		m.setCompletions(nil)
		return m, nil
	}

	m.history = appendAIHistory(m.configDir, m.history, line)
	m.historyPos = len(m.history)
	m.draft = ""
	m.input.Reset()
	m.setCompletions(nil)
	m.follow = true

	m.append(aiEchoStyle.Render("› " + line))

	switch route.kind {
	case aiRouteChat:
		return m.submitChat(route.text)

	case aiRouteInvalid:
		m.append(aiErrorStyle.Render("could not parse: " + route.text))
		return m, nil

	case aiRouteUnknown:
		lines := []string{aiErrorStyle.Render("unknown command: /" + route.text)}
		if len(route.suggestions) > 0 {
			lines = append(lines, aiEchoStyle.Render("did you mean: /"+strings.Join(route.suggestions, ", /")))
		} else {
			lines = append(lines, aiEchoStyle.Render("type / to browse available commands"))
		}
		m.append(strings.Join(lines, "\n"))
		return m, nil

	case aiRouteVerb:
		return m.runVerb(route)

	case aiRouteDispatch:
		ctx, cancel := context.WithCancel(context.Background())
		m.running = &aiRunState{
			argv: route.argv, cancel: cancel, started: time.Now(),
			customer: readCustomerContext(m.configDir),
		}
		return m, tea.Batch(m.spin.Tick, aiDispatchCommand(ctx, m.running))
	}
	return m, nil
}

func (m aiModel) submitChat(text string) (tea.Model, tea.Cmd) {
	if m.session == nil {
		// Guided setup (P3): capture a key inline, then answer the question
		// that triggered it. The data-flow disclosure (D3) renders before the
		// user commits anything.
		m.keyEntry = true
		m.keyBuf = ""
		m.pendingQuestion = text
		m.append(renderAIKeyOnboarding())
		return m, nil
	}
	if err := m.session.Send(aiUserInput{Kind: aiInputChat, Text: text}); err != nil {
		m.append(aiEchoStyle.Render(err.Error()))
	}
	return m, nil
}

func renderAIKeyOnboarding() string {
	return strings.Join([]string{
		aiNoticeStyle.Render("AI questions need an Anthropic API key (yours)."),
		aiEchoStyle.Render("Get one at console.anthropic.com → API keys, then paste it below."),
		aiEchoStyle.Render("It is stored only in " + aiSettingsFileName + " under your dci config directory (0600)."),
		aiNoticeStyle.Render("Note: your questions and dci command results are sent to Anthropic's API under this key."),
	}, "\n")
}

// handleKeyEntryKey owns the keyboard while the key setup is active.
func (m aiModel) handleKeyEntryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.keyEntry = false
		m.keyBuf = ""
		m.pendingQuestion = ""
		m.append(aiEchoStyle.Render("key setup canceled — export ANTHROPIC_API_KEY works too"))
		return m, nil
	case "enter":
		key := strings.TrimSpace(m.keyBuf)
		if err := aiValidateAPIKey(key); err != nil {
			m.append(aiErrorStyle.Render(err.Error()))
			return m, nil
		}
		settings := loadAISettings(m.configDir)
		settings.APIKey = key
		if err := saveAISettings(m.configDir, settings); err != nil {
			m.append(aiErrorStyle.Render("could not save: " + err.Error()))
			return m, nil
		}
		m.keyEntry = false
		m.keyBuf = ""
		m.session = newAIConversationSession(m.configDir, key, m.modelName, m.catalog)
		m.sessionNote = ""
		m.input.Placeholder = "Ask about your cloud costs, or type / for commands"
		m.append(tuiSuccessStyle.Render("Key saved — AI is ready."))
		commands := []tea.Cmd{aiListen(m.session)}
		if question := m.pendingQuestion; question != "" {
			m.pendingQuestion = ""
			if err := m.session.Send(aiUserInput{Kind: aiInputChat, Text: question}); err == nil {
				m.append(aiEchoStyle.Render("› " + question))
			}
		}
		return m, tea.Batch(commands...)
	case "backspace":
		if runes := []rune(m.keyBuf); len(runes) > 0 {
			m.keyBuf = string(runes[:len(runes)-1])
		}
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		m.keyBuf += string(msg.Runes)
	}
	return m, nil
}

func (m aiModel) runVerb(route aiRoute) (tea.Model, tea.Cmd) {
	switch route.verb {
	case "help":
		m.append(aiHelpText(len(m.catalog), m.session != nil))
		return m, nil
	case "clear":
		// A new screen is a new conversation: drop the session history too,
		// so stale tenant data can't leak into the next question (§6.2).
		m.transcript = nil
		m.streamBuf.Reset()
		m.refreshTranscript()
		if m.session != nil {
			_ = m.session.Close()
			settings := loadAISettings(m.configDir)
			if key := resolveAIKey(settings); key != "" {
				m.session = newAIConversationSession(m.configDir, key, m.modelName, m.catalog)
				return m, aiListen(m.session)
			}
			m.session = nil
		}
		return m, nil
	case "quit":
		return m, tea.Quit
	case "customer":
		result, err := aiHandleCustomer(m.configDir, route.args)
		if err != nil {
			m.append(aiErrorStyle.Render(err.Error()))
			return m, nil
		}
		m.identity = aiIdentitySegment(m.configDir)
		m.append(tuiSuccessStyle.Render(result))
		return m, nil
	case "model":
		return m.runModelVerb(route.args)
	case "export":
		path, err := aiExportTranscript(m.transcript, route.args, time.Now())
		if err != nil {
			m.append(aiErrorStyle.Render(err.Error()))
			return m, nil
		}
		m.append(tuiSuccessStyle.Render("Transcript saved to " + path))
		return m, nil
	}
	return m, nil
}

// aiExportTranscript writes the transcript, ANSI styling stripped, to the
// given path (or a timestamped default in the working directory). With the
// stable frame there is no terminal scrollback to fall back on, so this is
// the transcript's way out of the session (D4/D9).
func aiExportTranscript(transcript []string, args []string, now time.Time) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("usage: /export [file]")
	}
	path := "dci-ai-" + now.Format("20060102-150405") + ".txt"
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		path = args[0]
	}
	// stripANSI is the error contract chapter's helper; the transcript export
	// wants the same plain text.
	content := stripANSI(strings.Join(transcript, "\n")) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// runModelVerb implements /model (D2): no argument shows the current model
// and session info; one argument validates, persists, and applies it live.
func (m aiModel) runModelVerb(args []string) (tea.Model, tea.Cmd) {
	switch len(args) {
	case 0:
		m.append(m.modelInfoText())
		return m, nil
	case 1:
		model := strings.TrimSpace(args[0])
		if err := aiValidateModel(model); err != nil {
			m.append(aiErrorStyle.Render(err.Error()))
			return m, nil
		}
		settings := loadAISettings(m.configDir)
		settings.Model = model
		if err := saveAISettings(m.configDir, settings); err != nil {
			m.append(aiErrorStyle.Render("could not save: " + err.Error()))
			return m, nil
		}
		m.modelName = model
		if local, ok := m.session.(*localAISession); ok && local != nil {
			local.SetModel(model)
		}
		note := "Model set to " + model
		if !aiModelIsKnown(model) {
			note += aiEchoStyle.Render("  (not in the known list — the API validates it on the next question)")
		}
		m.append(tuiSuccessStyle.Render(note))
		return m, nil
	default:
		m.append(aiErrorStyle.Render("usage: /model [id]"))
		return m, nil
	}
}

func (m aiModel) modelInfoText() string {
	lines := []string{
		aiCardHeadStyle.Render("Model: ") + m.modelName,
		aiEchoStyle.Render("Available: " + strings.Join(aiKnownModels, ", ")),
	}
	if m.session == nil {
		lines = append(lines, aiNoticeStyle.Render(m.sessionNote))
	}
	stable := aiSystemPrompt(m.catalog, cachedTokenIsDoer() || readCustomerContext(m.configDir) != "")
	lines = append(lines, aiEchoStyle.Render(fmt.Sprintf("System prompt (cached prefix): ~%d tokens, %d commands in catalog", aiEstimateTokens(stable), len(m.catalog))))
	if m.lastUsage != nil {
		lines = append(lines, aiEchoStyle.Render(fmt.Sprintf("Last turn: %d in / %d out / %d cache-read tokens",
			m.lastUsage.InputTokens, m.lastUsage.OutputTokens, m.lastUsage.CacheRead)))
	}
	return strings.Join(lines, "\n")
}

// aiDispatchCommand runs one user slash command as a subprocess of this
// binary — the same argv the user would type after `dci `, isolated per
// AI-SPEC §7.4: package-level state can't leak between commands and an
// os.Exit in the child can't kill the session. It renders for the human, so
// no agent mode (the model-facing path in ai_tools.go uses it instead);
// DCI_NO_TUI keeps the child deterministic even if a PTY-ish stdio ever
// leaks through. The child shares this process's config dir, so auth and
// customer context apply unchanged.
func aiDispatchCommand(ctx context.Context, run *aiRunState) tea.Cmd {
	argv, started, customer := run.argv, run.started, run.customer
	return func() tea.Msg {
		command := exec.CommandContext(ctx, aiExecutablePath(), argv...)
		command.Env = append(os.Environ(), "DCI_NO_TUI=1")
		output, err := command.CombinedOutput()
		msg := aiCmdDoneMsg{argv: argv, output: string(output), elapsed: time.Since(started), customer: customer}
		if ctx.Err() == context.Canceled {
			msg.canceled = true
			return msg
		}
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				msg.exitCode = exitErr.ExitCode()
			} else {
				msg.runErr = err.Error()
			}
		}
		return msg
	}
}

// aiFriendlyAPIError translates the common Claude API failures into
// actionable hints — a raw 401 doesn't say which key source is wrong, and
// the env var silently overriding the saved key is exactly the case a user
// can't see. Unrecognized errors pass through verbatim.
func aiFriendlyAPIError(configDir, message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "authentication_error") || strings.Contains(lower, "401"):
		source := "the key saved in " + aiSettingsPath(configDir)
		if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
			source = "the ANTHROPIC_API_KEY environment variable — it overrides any saved key"
		}
		return "Anthropic rejected the API key. Check " + source + "."
	case strings.Contains(lower, "rate_limit") || strings.Contains(lower, "429"):
		return "Anthropic rate limit reached — wait a moment and ask again."
	case strings.Contains(lower, "overloaded") || strings.Contains(lower, "529"):
		return "Anthropic is overloaded right now — try again shortly."
	case strings.Contains(lower, "credit balance") || strings.Contains(lower, "billing"):
		return "The Anthropic account behind this key has a billing problem: " + message
	case strings.Contains(lower, "not_found_error") && strings.Contains(lower, "model"):
		return "The selected model was not accepted — check /model. (" + message + ")"
	}
	return message
}

// aiExecutablePath resolves the binary to re-exec: the running executable,
// symlinks resolved (same policy as the update chapter), with os.Args[0] as
// the last resort.
func aiExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// renderAIMarkdown renders committed narration as markdown, falling back to
// the raw text when the renderer objects (exotic terminals).
func renderAIMarkdown(text string, width int) string {
	if width <= 0 || width > 100 {
		width = 100
	}
	renderer, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(width))
	if err != nil {
		return text
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return strings.Trim(rendered, "\n")
}

// renderAIRunCard renders one finished user dispatch for the transcript:
// header, verbatim output, one-line footer. Pure so tests can cover every
// outcome.
func renderAIRunCard(msg aiCmdDoneMsg) string {
	var b strings.Builder
	header := "dci " + strings.Join(msg.argv, " ")
	if msg.customer != "" {
		header += aiEchoStyle.Render("  [" + msg.customer + "]")
	}
	b.WriteString(aiCardHeadStyle.Render(header))
	b.WriteString("\n")
	if output := strings.TrimRight(msg.output, "\n"); output != "" {
		b.WriteString(output)
		b.WriteString("\n")
	}
	elapsed := msg.elapsed.Round(100 * time.Millisecond)
	switch {
	case msg.canceled:
		b.WriteString(aiErrorStyle.Render(fmt.Sprintf("canceled after %s", elapsed)))
	case msg.runErr != "":
		b.WriteString(aiErrorStyle.Render("failed to run: " + msg.runErr))
	case msg.exitCode != 0:
		b.WriteString(aiErrorStyle.Render(fmt.Sprintf("exit %d · %s", msg.exitCode, elapsed)))
	default:
		b.WriteString(aiEchoStyle.Render(elapsed.String()))
	}
	return b.String()
}

// renderAIToolStart is the agent-initiated tool call header (§9 tool cards):
// provenance-badged so agent calls read differently from the user's own.
func renderAIToolStart(call aiToolCallStarted) string {
	header := "⚙ "
	switch call.Tool {
	case aiToolRunCommand:
		header += "dci " + strings.Join(call.Argv, " ")
	case aiToolSetCustomer:
		header += "switching customer context"
	default:
		header += call.Tool
	}
	if call.Customer != "" {
		header += aiEchoStyle.Render("  [" + call.Customer + "]")
	}
	return aiAgentStyle.Render(header)
}

// renderAIToolResult renders one agent tool result: the same output the
// model sees, tenant-badged, with an error/duration footer.
func renderAIToolResult(result aiToolResult) string {
	var b strings.Builder
	if output := strings.TrimRight(result.Data, "\n"); output != "" {
		b.WriteString(output)
		b.WriteString("\n")
	}
	footer := result.Elapsed.Round(100 * time.Millisecond).String()
	if result.Truncated {
		footer += " · truncated for the model"
	}
	if result.OK {
		b.WriteString(aiEchoStyle.Render(footer))
	} else {
		b.WriteString(aiErrorStyle.Render("tool failed · " + footer))
	}
	return b.String()
}

func renderAIApprovalRequest(request aiApprovalRequest) string {
	lines := []string{
		aiApproveStyle.Render("The agent wants to run a destructive command:"),
		"  dci " + strings.Join(request.Argv, " "),
	}
	if request.Summary != "" {
		lines = append(lines, aiEchoStyle.Render("  "+request.Summary))
	}
	lines = append(lines, aiApproveStyle.Render("y")+" run it · "+aiApproveStyle.Render("n")+" decline")
	return strings.Join(lines, "\n")
}

func aiHelpText(catalogSize int, sessionReady bool) string {
	aiLine := "  plain text   ask the AI about your cloud costs — it runs dci commands for you"
	if !sessionReady {
		aiLine = "  plain text   ask the AI (needs an API key: export ANTHROPIC_API_KEY or ask a question to set one up)"
	}
	lines := []string{
		aiCardHeadStyle.Render("How this session works"),
		"  /<command>   run any dci command — same syntax as the shell (" + fmt.Sprint(catalogSize) + " available, type / to browse)",
		aiLine,
		"",
		aiCardHeadStyle.Render("Session commands"),
	}
	for _, verb := range aiSessionVerbs {
		lines = append(lines, fmt.Sprintf("  %-22s %s", verb.usage, verb.summary))
	}
	lines = append(lines, "",
		aiEchoStyle.Render("Saved commands: define your own in "+aiUserCommandsFileName+" — {\"top5\": {\"command\": \"list-reports --limit 5\"}, \"review\": {\"prompt\": \"Review last month's spend\"}}"),
		aiEchoStyle.Render("Privacy: AI questions and dci command results are sent to Anthropic's API under your key."),
		"",
		"  tab completes · ↑/↓ history or popup · wheel/PgUp/PgDn scroll · esc clears or cancels · /export saves the transcript")
	return strings.Join(lines, "\n")
}

func (m aiModel) View() string {
	var b strings.Builder
	b.WriteString(m.headerLine())
	b.WriteString("\n")
	b.WriteString(m.view.View())
	b.WriteString("\n")
	if m.keyEntry {
		masked := strings.Repeat("•", len([]rune(m.keyBuf)))
		b.WriteString("API key: " + masked + "\n")
		b.WriteString(aiEchoStyle.Render("paste your key · enter save · esc cancel"))
		return b.String()
	}
	for index, completion := range m.completions {
		line := fmt.Sprintf(" /%s  %s", completion.Value, aiEchoStyle.Render(completion.Summary))
		if index == m.completionIndex {
			line = aiSelectedStyle.Render(" /" + completion.Value + " ")
			if completion.Summary != "" {
				line += " " + aiEchoStyle.Render(completion.Summary)
			}
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(m.input.View())
	b.WriteString("\n")
	b.WriteString(m.statusLine())
	return b.String()
}

// headerLine is the fixed top of the frame; a scroll hint appears when the
// viewport is not following the bottom.
func (m aiModel) headerLine() string {
	title := aiHeaderStyle.Render("dci ai") + aiEchoStyle.Render(" · Cloud Intelligence™")
	if !m.follow {
		title += aiNoticeStyle.Render("  ↓ scrolled up — PgDn for latest")
	}
	return title
}

func (m aiModel) statusLine() string {
	if m.approval != nil {
		return aiApproveStyle.Render("destructive command pending — y run · n decline")
	}
	if m.running != nil {
		return m.spin.View() + aiEchoStyle.Render(fmt.Sprintf(" running /%s · %s · esc to cancel",
			strings.Join(m.running.argv, " "),
			time.Since(m.running.started).Round(time.Second)))
	}
	if m.turnActive {
		return m.spin.View() + aiEchoStyle.Render(" thinking ("+m.modelName+") · esc to cancel")
	}
	segments := make([]string, 0, 3)
	if m.identity != "" {
		segments = append(segments, m.identity)
	}
	if m.ctrlCArmed {
		segments = append(segments, "ctrl+c again to quit")
	} else {
		segments = append(segments, "/help for commands")
	}
	return aiEchoStyle.Render(strings.Join(segments, " · "))
}
