package main

// The `dci ai` session program (AI-SPEC §3, D9): a stable frame in the
// alternate screen — one-line header, the transcript in a scrolling
// viewport, input and status pinned at the bottom. The frame never moves;
// content scrolls inside the viewport (mouse wheel, PgUp/PgDn), following
// the bottom until the user scrolls up. Slash commands dispatch to the CLI
// itself as a subprocess (§7.4) and their results join the conversation
// (§4.4); plain text goes to the conversation session (ai_session.go),
// whose protocol events this program renders quietly (D10): in-flight
// narration and tool traffic drive the status line, the final answer commits
// through glamour, and approvals and tenant switches surface immediately.
// Routing, completion, and history logic live in ai_slash.go. Kept in a
// sibling file per the AGENTS.md chapter-split guidance.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

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
	width    int
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

	running      *aiRunState
	ctrlCArmed   bool
	identity     string // the tenant line, cached (rebuilt on switches/lookups)
	userLine     string // "email · role", from the cached token
	customerName string // resolved display name for an ID-shaped context
	mouseOn      bool

	// Async name fetch backing the picker when the cache is empty.
	fetchedNames map[string][]nameCacheEntry
	fetchIntent  *aiPickerIntent

	// Conversation state. session is nil when no API key is configured;
	// sessionNote then explains what is missing.
	session     conversationSession
	sessionNote string
	modelName   string
	turnActive  bool
	// stream is the assistant text accumulated since the last commit or
	// discard. A plain string on purpose: a strings.Builder must never live
	// in a Bubble Tea model — the model is copied by value on every Update,
	// and writing to a copied non-empty Builder panics ("illegal use of
	// non-zero Builder copied by value") as soon as a copy lands at a new
	// address (stack growth, reallocation) — a mid-turn crash.
	stream        string
	turnActivity  string    // status line: what the turn is doing right now
	turnStarted   time.Time // when the active turn began, for the elapsed display
	thinking      string    // tail of the model's thinking stream (status snippet only)
	turnCommands  int       // agent tool calls completed this turn
	toolLabel     string    // label of the tool call in flight, for its result line
	markdownStyle string    // glamour style, resolved once before the program runs
	approval      *aiApprovalRequest
	lastUsage     *aiTurnDone

	// Guided key setup (P3): entering a key inline instead of editing files.
	keyEntry        bool
	keyBuf          string
	pendingQuestion string

	// Name selection for resolvable commands (ai_picker.go): the session-side
	// F1/F2 picker.
	picker       *aiNameSelection
	pickerFilter string
	pickerIndex  int

	// F3 parity for user slash dispatches: a child blocked by the destructive
	// contract (exit 30) waits here for the y/N answer; y re-runs with --yes.
	dispatchApproval *aiDispatchApproval
}

type aiDispatchApproval struct {
	argv    []string
	summary string
}

// runAISession is the entry point behind `dci ai` (ai_command.go).
func runAISession(configDir string) error {
	// Mouse capture starts OFF so the terminal's own selection/copy works out
	// of the box (dogfood: capture broke copy/paste even with modifier keys in
	// some terminals); /mouse opts into wheel scrolling. Keyboard scrolling
	// (PgUp/PgDn) always works.
	program := tea.NewProgram(newAIModel(configDir), tea.WithAltScreen())
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
		userLine:     aiUserLine(),
		modelName:    modelName,
		mouseOn:      false,
		fetchedNames: map[string][]nameCacheEntry{},
		// Resolved here — before the program owns stdin — so no render ever
		// queries the terminal mid-session (see aiMarkdownStyle).
		markdownStyle: aiMarkdownStyle(),
	}
	m.identity = m.contextLabel()
	if key := resolveAIKey(settings); key != "" {
		m.session = newAIConversationSession(configDir, key, modelName, catalog)
	} else {
		m.sessionNote = "AI needs an Anthropic API key — ask a question to set one up, or export ANTHROPIC_API_KEY"
	}
	m.append(aiBannerBlock(&m))
	m.layout()
	return m
}

// aiDoitLogo is the DoiT "d" mark — the lowercase d with its floating dot —
// in the brand accent, the session's answer to Claude Code's robot.
var aiDoitLogo = []string{
	"        ██    ",
	"  ▄▄▄▄▄▄██  ▄▄",
	"  ██    ██  ▀▀",
	"  ▀██████▀    ",
}

// aiLogoStyle carries the DoiT accent (#FC3165); lipgloss degrades the hex
// to the terminal's nearest color when truecolor is unavailable.
var aiLogoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FC3165"))

// aiBannerBlock is the transcript's opening block, Claude Code-style: the
// mark beside the product name, version, and the session facts that matter —
// model and key source, tenant identity, catalog size.
func aiBannerBlock(m *aiModel) string {
	logo := aiLogoStyle.Render(strings.Join(aiDoitLogo, "\n"))

	versionLabel := "v" + version
	if version == "dev" {
		versionLabel = "dev build"
	}

	modelLine := m.modelName
	switch {
	case m.session == nil:
		modelLine = "AI off — ask a question to set up a key"
	case strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "":
		modelLine += " · API key from env"
	default:
		modelLine += " · API key from " + aiSettingsFileName
	}

	info := []string{
		aiCardHeadStyle.Render("Cloud Intelligence™ CLI") + aiEchoStyle.Render(" "+versionLabel),
		aiEchoStyle.Render(modelLine),
	}
	if m.userLine != "" {
		info = append(info, aiEchoStyle.Render(m.userLine))
	}
	if m.identity != "" {
		info = append(info, aiEchoStyle.Render(m.identity))
	}
	info = append(info, aiEchoStyle.Render(fmt.Sprintf("%d commands · /help for how this works", len(m.catalog))))

	return lipgloss.JoinHorizontal(lipgloss.Center, logo, "  ", strings.Join(info, "\n")) + "\n"
}

// refreshBanner re-renders the opening block in place after an identity
// change (a resolved customer name, a tenant switch).
func (m *aiModel) refreshBanner() {
	if len(m.transcript) > 0 && strings.Contains(m.transcript[0], "Cloud Intelligence™ CLI") {
		m.transcript[0] = aiBannerBlock(m)
		m.refreshTranscript()
	}
}

// aiCustomerNameMsg reports the opportunistic display-name lookup.
type aiCustomerNameMsg struct {
	context string
	name    string
}

// aiLookupCustomerName resolves an ID-shaped customer context to its display
// name — but only when the API actually exposes a get-customer operation;
// otherwise the raw context stands. Runs as a subprocess in agent mode so a
// hung lookup can never wedge the session.
func aiLookupCustomerName(customerContext string) tea.Cmd {
	if customerContext == "" || strings.Contains(customerContext, ".") || aiOperationFlagSet("get-customer") == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		output, exitCode, err := aiAgentModeRunner(ctx, []string{"get-customer", customerContext, "--output", "json"})
		if err != nil || exitCode != 0 {
			return aiCustomerNameMsg{context: customerContext}
		}
		return aiCustomerNameMsg{context: customerContext, name: aiCustomerNameFromJSON(output)}
	}
}

// aiCustomerNameFromJSON pulls a display name out of the customer payload,
// using the same field priority the name resolver trusts.
func aiCustomerNameFromJSON(data []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	for _, field := range append(resourceNameFieldPriority, "primaryDomain", "domain") {
		if value, ok := payload[field].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// aiUserLine is the signed-in identity: email and role from the cached
// token. Partner detection needs a claim the token does not carry yet, so
// non-doers read as Customer.
func aiUserLine() string {
	claims, ok := cachedTokenClaims()
	if !ok {
		return ""
	}
	role := "Customer"
	if claims.DoitEmployee {
		role = "Doer"
	}
	if claims.Email == "" {
		return role
	}
	return claims.Email + " · " + role
}

// contextLabel is the tenant line: the customer display name when resolved,
// with the raw context in parentheses; doers with no context get the fix
// spelled out; single-tenant customers see no tenant vocabulary (§6.1).
func (m *aiModel) contextLabel() string {
	context := readCustomerContext(m.configDir)
	if context == "" {
		if cachedTokenIsDoer() {
			return "no customer context (/customer <name>)"
		}
		return ""
	}
	if m.customerName != "" {
		return m.customerName + " (" + context + ")"
	}
	return context
}

// newAIConversationSession builds the session behind a var so tests can
// substitute a fake without touching the Claude API.
var newAIConversationSession = func(configDir, apiKey, model string, catalog []aiCatalogEntry) conversationSession {
	return newLocalAISession(configDir, apiKey, model, catalog)
}

func (m aiModel) Init() tea.Cmd {
	commands := []tea.Cmd{textarea.Blink}
	if m.session != nil {
		commands = append(commands, aiListen(m.session))
	}
	if lookup := aiLookupCustomerName(readCustomerContext(m.configDir)); lookup != nil {
		commands = append(commands, lookup)
	}
	return tea.Batch(commands...)
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

// refreshTranscript rebuilds the viewport content from the committed blocks.
// In-flight turn activity (narration, tool traffic) lives in the status line,
// not here — the transcript only ever gains finished blocks.
func (m *aiModel) refreshTranscript() {
	m.view.SetContent(strings.Join(m.transcript, "\n"))
	if m.follow {
		m.view.GotoBottom()
	}
}

// layout recomputes the frame: the viewport takes everything the fixed
// chrome (header, completion popup, input, status) does not.
func (m *aiModel) layout() {
	m.input.SetWidth(m.width - 2)
	m.view.Width = m.width
	middle := len(m.completions) + 1 /*input*/
	if m.picker != nil {
		rows := len(m.picker.filtered(m.pickerFilter))
		if rows > aiPickerVisibleRows {
			rows = aiPickerVisibleRows
		}
		middle = rows + 2 // selection header + filter line
	}
	chrome := 2 /*rules around the input*/ + middle + 1 /*status*/
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
		if m.running == nil && !m.turnActive && m.fetchIntent == nil {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case aiCmdDoneMsg:
		m.running = nil
		// The destructive contract blocked the command (F3): ask here — the
		// child had no terminal to ask on — and re-run with --yes on approval.
		// Exit 30 alone is ambiguous (API VALIDATION_ERROR shares the code,
		// error_contract.go), and the human-mode child prints no structured
		// envelope — so also require the command to be in the destructive set.
		if msg.exitCode == aiDestructiveExitCode && !msg.canceled && msg.runErr == "" && !aiArgvHasYes(msg.argv) && aiDispatchIsDestructive(msg.argv) {
			m.dispatchApproval = &aiDispatchApproval{argv: msg.argv, summary: aiDispatchSummary(msg.output, msg.argv)}
			m.append(renderAIDispatchApproval(*m.dispatchApproval))
			return m, nil
		}
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

	case aiCustomerNameMsg:
		if msg.name != "" && msg.context == readCustomerContext(m.configDir) {
			m.customerName = msg.name
			m.identity = m.contextLabel()
			m.refreshBanner()
		}
		return m, nil

	case aiNamesFetchedMsg:
		return m.handleNamesFetched(msg)

	case aiSessionClosedMsg:
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleSessionEvent renders one protocol event and re-arms the listener.
// The turn runs quietly (Claude Code UX): interim narration and tool traffic
// only drive the status line; the transcript gains the final answer, plus
// anything the user must see mid-turn — approvals, context switches, errors.
func (m aiModel) handleSessionEvent(event aiEvent) (tea.Model, tea.Cmd) {
	commands := []tea.Cmd{aiListen(m.session)}
	switch {
	case event.TurnStarted != nil:
		m.turnActive = true
		m.stream = ""
		m.turnActivity = ""
		m.turnStarted = time.Now()
		m.thinking = ""
		m.turnCommands = 0
		m.lastUsage = nil
		commands = append(commands, m.spin.Tick)

	case event.TextDelta != nil:
		m.stream += event.TextDelta.Text
		m.turnActivity = aiActivitySnippet(m.stream)

	case event.ThinkingDelta != nil:
		// Thinking never reaches the transcript; it drives the status line so
		// an analytical pause (often the longest part of a turn) shows live
		// progress instead of a frozen spinner. Keep only a tail: the snippet
		// wants the last line, not the whole reasoning stream.
		m.thinking += event.ThinkingDelta.Text
		if len(m.thinking) > 4096 {
			cut := len(m.thinking) - 2048
			// Advance to a rune boundary: a raw byte cut can split a
			// multi-byte rune and garble the status snippet.
			for cut < len(m.thinking) && !utf8.RuneStart(m.thinking[cut]) {
				cut++
			}
			m.thinking = m.thinking[cut:]
		}
		if snippet := aiActivitySnippet(m.thinking); snippet != "" {
			m.turnActivity = "thinking · " + snippet
		}

	case event.ToolCallStarted != nil:
		// Text so far was working narration, not the answer: it becomes the
		// activity snippet and never reaches the transcript.
		m.stream = ""
		m.toolLabel = aiToolCallLabel(*event.ToolCallStarted)
		m.turnActivity = "running " + m.toolLabel

	case event.ToolResult != nil:
		m.turnCommands++
		mark := "✓"
		if !event.ToolResult.OK {
			mark = "✗"
		}
		m.turnActivity = fmt.Sprintf("%s %s · %s", mark, m.toolLabel,
			event.ToolResult.Elapsed.Round(100*time.Millisecond))

	case event.ApprovalRequest != nil:
		request := *event.ApprovalRequest
		m.approval = &request
		m.append(renderAIApprovalRequest(request))

	case event.ContextSwitched != nil:
		m.customerName = ""
		m.identity = m.contextLabel()
		m.refreshBanner()
		m.append(aiNoticeStyle.Render(fmt.Sprintf(
			"customer context switched: %s → %s (by %s)",
			aiDisplayContext(event.ContextSwitched.From), event.ContextSwitched.To, event.ContextSwitched.By)))
		if lookup := aiLookupCustomerName(event.ContextSwitched.To); lookup != nil {
			commands = append(commands, lookup)
		}

	case event.LimitReached != nil:
		m.commitStream()
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
		m.turnActivity = ""
		m.approval = nil
	}
	return m, tea.Batch(commands...)
}

// commitStream moves the accumulated assistant text into the transcript,
// rendered as markdown. During a turn this only happens when the text is
// final: the turn ended, errored, or hit a ceiling.
func (m *aiModel) commitStream() {
	text := strings.TrimSpace(m.stream)
	m.stream = ""
	if text == "" {
		return
	}
	m.append(renderAIMarkdown(text, m.view.Width, m.markdownStyle))
}

// aiActivitySnippet condenses in-flight narration to one status-line phrase:
// the last non-empty line, whitespace collapsed, capped for the frame.
func aiActivitySnippet(stream string) string {
	lines := strings.Split(strings.TrimSpace(stream), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.Join(strings.Fields(lines[i]), " ")
		if line == "" {
			continue
		}
		if runes := []rune(line); len(runes) > 72 {
			line = string(runes[:71]) + "…"
		}
		return line
	}
	return ""
}

// aiToolCallLabel names one agent tool call for the status line.
func aiToolCallLabel(call aiToolCallStarted) string {
	switch call.Tool {
	case aiToolRunCommand:
		label := "dci " + strings.Join(call.Argv, " ")
		if runes := []rune(label); len(runes) > 60 {
			label = string(runes[:59]) + "…"
		}
		return label
	case aiToolSetCustomer:
		return "customer switch"
	default:
		return call.Tool
	}
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

	// A pending name selection owns it next (ai_picker.go).
	if m.picker != nil {
		return m.handlePickerKey(msg)
	}

	// While the picker's name fetch runs, only esc acts (it abandons the
	// fetch; the result is dropped when it lands).
	if m.fetchIntent != nil {
		if key == "esc" || key == "ctrl+c" {
			m.fetchIntent = nil
			m.append(aiEchoStyle.Render("selection canceled"))
		}
		return m, nil
	}

	// A user command blocked by the destructive contract waits for y/N (F3).
	if m.dispatchApproval != nil {
		switch key {
		case "y", "Y":
			approval := m.dispatchApproval
			m.dispatchApproval = nil
			m.append(aiEchoStyle.Render("↳ approved"))
			return m, m.startDispatch(append(append([]string{}, approval.argv...), "--yes"))
		case "n", "N", "esc", "ctrl+c":
			m.dispatchApproval = nil
			m.append(aiEchoStyle.Render("↳ declined"))
			return m, nil
		}
		return m, nil
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
		// The interactive query builder (TUI-SPEC F4) needs a real terminal
		// the child does not have; a bare /query would just die on empty
		// stdin, so say what works instead.
		if len(route.argv) == 1 && route.argv[0] == "query" {
			m.append(aiNoticeStyle.Render("The interactive query builder needs a regular terminal — run dci query in your shell.") + "\n" +
				aiEchoStyle.Render("In here: ask the AI to build and run the query for you, or pass shorthand body arguments to /query."))
			return m, nil
		}
		if intent := aiPickerIntentFor(route.argv); intent != nil {
			entries := intent.cachedEntries(m.configDir)
			if len(entries) == 0 {
				entries = m.fetchedNames[intent.target.resource]
			}
			if len(entries) == 0 {
				// No names on hand: fetch them (what the child's own F1/F2
				// would do) instead of degrading to a usage error.
				m.fetchIntent = intent
				return m, tea.Batch(m.spin.Tick, aiFetchNames(intent, readCustomerContext(m.configDir)))
			}
			if selection := intent.selection(entries); selection != nil {
				m.openPicker(selection)
				return m, nil
			}
		}
		return m, m.startDispatch(route.argv)
	}
	return m, nil
}

func (m *aiModel) openPicker(selection *aiNameSelection) {
	m.picker = selection
	m.pickerFilter = ""
	m.pickerIndex = 0
	m.layout()
}

// aiNamesFetchedMsg reports the async name fetch backing the picker.
type aiNamesFetchedMsg struct {
	intent  *aiPickerIntent
	entries []nameCacheEntry
	err     error
}

// aiFetchNames pages the collection like the CLI's own picker does
// (resolverListFetch), off the UI goroutine.
func aiFetchNames(intent *aiPickerIntent, customerContext string) tea.Cmd {
	return func() tea.Msg {
		result, err := resolverListFetch(intent.target.listPath, customerContext, resolverMaxPages)
		return aiNamesFetchedMsg{intent: intent, entries: result.entries, err: err}
	}
}

func (m aiModel) handleNamesFetched(msg aiNamesFetchedMsg) (tea.Model, tea.Cmd) {
	if m.fetchIntent == nil || m.fetchIntent != msg.intent {
		return m, nil // canceled or superseded — drop the result
	}
	m.fetchIntent = nil
	if msg.err != nil {
		// The child will surface the real error (auth, network) on dispatch.
		m.append(aiEchoStyle.Render("could not fetch " + msg.intent.resource + " names — running the command as typed"))
		return m, m.startDispatch(msg.intent.argv)
	}
	m.fetchedNames[msg.intent.target.resource] = msg.entries
	if len(msg.entries) == 0 {
		m.append(aiNoticeStyle.Render("no " + msg.intent.resource + "s available in this customer context"))
		return m, nil
	}
	if selection := msg.intent.selection(msg.entries); selection != nil {
		m.openPicker(selection)
		return m, nil
	}
	return m, m.startDispatch(msg.intent.argv)
}

// startDispatch spawns one slash command subprocess and arms the spinner.
func (m *aiModel) startDispatch(argv []string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.running = &aiRunState{
		argv: argv, cancel: cancel, started: time.Now(),
		customer: readCustomerContext(m.configDir),
		width:    m.width,
	}
	return tea.Batch(m.spin.Tick, aiDispatchCommand(ctx, m.running))
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

// handlePickerKey drives the name selection: type to filter with the CLI's
// own matcher, ↑/↓ to move, enter dispatches with the chosen ID, esc cancels.
func (m aiModel) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.picker.filtered(m.pickerFilter)
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closePicker()
		m.append(aiEchoStyle.Render("selection canceled"))
		return m, nil
	case "enter":
		if m.pickerIndex >= len(rows) {
			return m, nil
		}
		entry := rows[m.pickerIndex]
		argv := m.picker.apply(entry)
		m.closePicker()
		m.append(aiEchoStyle.Render("picked " + entry.Name + " (" + entry.ID + ")"))
		return m, m.startDispatch(argv)
	case "up":
		if len(rows) > 0 {
			m.pickerIndex = (m.pickerIndex + len(rows) - 1) % len(rows)
		}
		return m, nil
	case "down", "tab":
		if len(rows) > 0 {
			m.pickerIndex = (m.pickerIndex + 1) % len(rows)
		}
		return m, nil
	case "backspace":
		if runes := []rune(m.pickerFilter); len(runes) > 0 {
			m.pickerFilter = string(runes[:len(runes)-1])
		}
		m.clampPickerIndex()
		m.layout()
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		m.pickerFilter += string(msg.Runes)
		m.clampPickerIndex()
		m.layout()
	}
	return m, nil
}

func (m *aiModel) closePicker() {
	m.picker = nil
	m.pickerFilter = ""
	m.pickerIndex = 0
	m.layout()
}

func (m *aiModel) clampPickerIndex() {
	if rows := m.picker.filtered(m.pickerFilter); m.pickerIndex >= len(rows) {
		m.pickerIndex = 0
	}
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
		m.stream = ""
		m.append(aiBannerBlock(&m))
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
		m.customerName = ""
		m.identity = m.contextLabel()
		m.refreshBanner()
		m.append(tuiSuccessStyle.Render(result))
		if lookup := aiLookupCustomerName(readCustomerContext(m.configDir)); lookup != nil {
			return m, lookup
		}
		return m, nil
	case "mouse":
		m.mouseOn = !m.mouseOn
		if m.mouseOn {
			m.append(aiEchoStyle.Render("mouse capture on — wheel scrolls the transcript; /mouse to select text"))
			return m, tea.EnableMouseCellMotion
		}
		m.append(aiEchoStyle.Render("mouse capture off — select and copy text normally; /mouse to re-enable wheel scrolling"))
		return m, tea.DisableMouse
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
	isDoer := cachedTokenIsDoer()
	stable := aiSystemPrompt(m.catalog, isDoer || readCustomerContext(m.configDir) != "", isDoer)
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
// aiDispatchEnv is the slash child's environment, shaped so a piped child
// renders exactly what the same command shows a human at a terminal:
// DCI_AGENT_MODE=0 (the non-TTY soft signal would flip it to TOON output and
// agent error envelopes), DCI_SESSION_RENDER=1 (human-shaped hints, colored
// tables, stacked charts despite the pipe), CLICOLOR_FORCE=1 + COLOR=1
// (lipgloss and restish emit color on the pipe), COLUMNS (the child cannot
// measure the terminal, so it inherits the session's width), and DCI_NO_TUI
// (no interactive prompt from a child that has no terminal to ask on).
func aiDispatchEnv(width int) []string {
	return append(os.Environ(),
		"DCI_NO_TUI=1", "DCI_AGENT_MODE=0", "DCI_SESSION_RENDER=1",
		"COLOR=1", "CLICOLOR_FORCE=1",
		fmt.Sprintf("COLUMNS=%d", width),
	)
}

func aiDispatchCommand(ctx context.Context, run *aiRunState) tea.Cmd {
	argv, started, customer, width := run.argv, run.started, run.customer, run.width
	return func() tea.Msg {
		command := exec.CommandContext(ctx, aiExecutablePath(), argv...)
		command.Env = aiDispatchEnv(width)
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

// aiMarkdownStyle picks the glamour style for this terminal, once, BEFORE
// the Bubble Tea program starts. It must never be probed mid-session:
// glamour's WithAutoStyle queries the terminal background (OSC 11) on every
// render, and inside the running program Bubble Tea's input reader swallows
// the terminal's reply — the printable tail (";rgb:0000/0000/0000\") then
// lands in the textarea as if the user typed it.
func aiMarkdownStyle() string {
	if lipgloss.HasDarkBackground() {
		return "dark"
	}
	return "light"
}

// renderAIMarkdown renders committed answer text as markdown, falling back
// to the raw text when the renderer objects (exotic terminals).
func renderAIMarkdown(text string, width int, style string) string {
	if width <= 0 || width > 100 {
		width = 100
	}
	if style == "" {
		style = "dark"
	}
	renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width))
	if err != nil {
		return text
	}
	rendered, err := renderer.Render(aiUnescapeMarkdown(text))
	if err != nil {
		return text
	}
	return strings.Trim(rendered, "\n")
}

// aiUnescapeMarkdown resolves backslash-escaped markdown punctuation before
// rendering: the glamour pinned in the tree (v0.6.0 via restish, the F8
// version-skew caveat) prints CommonMark escapes literally — a model writing
// "\*August is partial" to suppress emphasis reaches the screen with the
// backslash. Fenced code blocks pass through untouched.
func aiUnescapeMarkdown(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	inFence := false
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			b.WriteString(line)
			continue
		}
		if inFence {
			b.WriteString(line)
			continue
		}
		for j := 0; j < len(line); j++ {
			if line[j] == '\\' && j+1 < len(line) && strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", line[j+1]) >= 0 {
				continue
			}
			b.WriteByte(line[j])
		}
	}
	return b.String()
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

// renderAIToolStart is the agent-initiated tool call header. The session's
// quiet turn keeps this off the transcript; one-shot mode still prints it to
// stderr when a human is watching.
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
// model sees, with an error/duration footer. One-shot stderr only — the
// session's quiet turn summarizes results in the status line instead.
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

// aiDispatchSummary pulls the child's own confirmation line out of its
// output for the approval prompt; the raw argv is the fallback.
// aiDispatchIsDestructive reports whether a dispatched command is in the
// destructive set, disambiguating exit 30 (VALIDATION_ERROR shares the code).
// A metadata failure fails toward asking: a destructive child must never run
// unconfirmed just because the operation metadata could not be loaded.
func aiDispatchIsDestructive(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if err := ensureDestructiveOperations(); err != nil {
		return true
	}
	return destructiveCommandSet[argv[0]]
}

func aiDispatchSummary(output string, argv []string) string {
	for _, line := range strings.Split(strings.TrimSpace(stripANSI(output)), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Error:"))
		if line != "" {
			return line
		}
	}
	return "dci " + strings.Join(argv, " ")
}

func renderAIDispatchApproval(approval aiDispatchApproval) string {
	return strings.Join([]string{
		aiApproveStyle.Render("This command is destructive:"),
		"  dci " + strings.Join(approval.argv, " "),
		aiEchoStyle.Render("  " + approval.summary),
		aiApproveStyle.Render("y") + " run it · " + aiApproveStyle.Render("n") + " decline",
	}, "\n")
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
		aiEchoStyle.Render("Copying text: select and copy normally (mouse capture is off by default); /mouse enables wheel scrolling instead; /export saves the whole transcript."),
		"",
		"  tab completes · ↑/↓ history or popup · PgUp/PgDn scroll (wheel via /mouse) · esc clears or cancels")
	return strings.Join(lines, "\n")
}

func (m aiModel) View() string {
	var b strings.Builder
	b.WriteString(m.view.View())
	b.WriteString("\n")
	b.WriteString(m.topRule())
	b.WriteString("\n")
	switch {
	case m.keyEntry:
		masked := strings.Repeat("•", len([]rune(m.keyBuf)))
		b.WriteString("API key: " + masked)
	case m.picker != nil:
		b.WriteString(m.pickerView())
	default:
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
	}
	b.WriteString("\n")
	b.WriteString(aiRule(m.width, ""))
	b.WriteString("\n")
	b.WriteString(m.statusLine())
	return b.String()
}

// aiRule draws one horizontal frame line, with an optional right-aligned
// hint embedded in it.
func aiRule(width int, hint string) string {
	if width < 4 {
		width = 4
	}
	if hint == "" {
		return aiEchoStyle.Render(strings.Repeat("─", width))
	}
	label := " " + hint + " "
	dashes := width - lipgloss.Width(label) - 2
	if dashes < 1 {
		dashes = 1
	}
	return aiEchoStyle.Render(strings.Repeat("─", dashes)) + aiNoticeStyle.Render(label) + aiEchoStyle.Render("──")
}

// topRule is the line above the input; it carries the scrolled-up hint when
// the viewport is not following the bottom.
func (m aiModel) topRule() string {
	if !m.follow {
		return aiRule(m.width, "↓ scrolled up — PgDn for latest")
	}
	return aiRule(m.width, "")
}

const aiPickerVisibleRows = 8

// pickerView renders the name selection: a header with the count, a window
// of candidates around the highlighted row, and the live filter line.
func (m aiModel) pickerView() string {
	rows := m.picker.filtered(m.pickerFilter)
	var b strings.Builder
	b.WriteString(aiCardHeadStyle.Render(fmt.Sprintf("Select a %s", m.picker.resource)) +
		aiEchoStyle.Render(fmt.Sprintf("  %d match(es) — type to filter", len(rows))))
	b.WriteString("\n")
	start := 0
	if m.pickerIndex >= aiPickerVisibleRows {
		start = m.pickerIndex - aiPickerVisibleRows + 1
	}
	end := start + aiPickerVisibleRows
	if end > len(rows) {
		end = len(rows)
	}
	for index := start; index < end; index++ {
		entry := rows[index]
		line := " " + entry.Name + "  " + aiEchoStyle.Render("("+entry.ID+")")
		if index == m.pickerIndex {
			line = aiSelectedStyle.Render(" "+entry.Name+" ") + " " + aiEchoStyle.Render("("+entry.ID+")")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(rows) == 0 {
		b.WriteString(aiEchoStyle.Render(" (no matches — backspace to widen)"))
		b.WriteString("\n")
	}
	b.WriteString("filter: " + m.pickerFilter)
	return b.String()
}

func (m aiModel) statusLine() string {
	if m.picker != nil {
		return aiEchoStyle.Render("↑/↓ select · enter run · esc cancel")
	}
	if m.approval != nil || m.dispatchApproval != nil {
		return aiApproveStyle.Render("destructive command pending — y run · n decline")
	}
	if m.fetchIntent != nil {
		return m.spin.View() + aiEchoStyle.Render(" fetching "+m.fetchIntent.resource+" names… · esc to cancel")
	}
	if m.running != nil {
		return m.spin.View() + aiEchoStyle.Render(fmt.Sprintf(" running /%s · %s · esc to cancel",
			strings.Join(m.running.argv, " "),
			time.Since(m.running.started).Round(time.Second)))
	}
	if m.turnActive {
		activity := m.turnActivity
		if activity == "" {
			activity = "thinking (" + m.modelName + ")"
		}
		line := " " + activity
		if !m.turnStarted.IsZero() {
			line += " · " + time.Since(m.turnStarted).Round(time.Second).String()
		}
		if m.turnCommands > 0 {
			line += fmt.Sprintf(" · %d command", m.turnCommands)
			if m.turnCommands > 1 {
				line += "s"
			}
		}
		return m.spin.View() + aiEchoStyle.Render(line+" · esc to cancel")
	}
	if m.keyEntry {
		return aiEchoStyle.Render("paste your key · enter save · esc cancel")
	}
	segments := make([]string, 0, 3)
	if m.identity != "" {
		segments = append(segments, m.identity)
	}
	switch {
	case m.ctrlCArmed:
		segments = append(segments, "ctrl+c again to quit")
	case m.session != nil:
		segments = append(segments, "Ask \"how much do we spend on tokens per AI model?\" · / for commands")
	default:
		segments = append(segments, "ask a question to set up AI · / for commands")
	}
	return aiEchoStyle.Render(strings.Join(segments, " · "))
}
