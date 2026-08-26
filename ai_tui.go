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

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/picture"
	"github.com/charmbracelet/glamour"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
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
	aiCompletionLimit = 6 // visible popup rows: a window over the match list
	// aiCompletionMatchCap bounds the candidates behind the popup window. The
	// window scrolls with ↑/↓, so this is a sanity cap, not the reachable set
	// (v2.6.2 dogfood: matches past the visible rows were unreachable).
	aiCompletionMatchCap = 500
	aiTranscriptBlocks   = 500 // frame memory cap; /export preserves everything typed before it
)

// aiRunState tracks the one in-flight user slash dispatch; the session runs
// at most one command at a time, matching the one-conversation mental model.
type aiRunState struct {
	argv     []string
	cancel   context.CancelFunc
	started  time.Time
	customer string
	// sessionCustomer is the agent's session-scoped context override, passed
	// to the child as DCI_CUSTOMER_CONTEXT so user dispatches run against the
	// same tenant the status line shows ("" = no override).
	sessionCustomer string
	width           int
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

// aiLoginDoneMsg reports the /login child that ran with the real terminal
// (tea.ExecProcess); err carries its failure, nil on success.
type aiLoginDoneMsg struct{ err error }

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
	// Status-line flair (ai_flair.go): the shimmer phase advances per
	// spinner tick; a quiet turn shows rotating quips; turnDoneMark drives
	// the window title's checkmark until the user presses a key.
	spinPhase    int
	turnQuip     string
	quipAt       time.Time
	turnDoneMark bool

	// The banner's Kitty-graphics logo (ai_flair.go): on terminals that
	// answer the Kitty probe, the half-block mark upgrades to a real raster.
	// logoGrid caches the placeholder grid the banner embeds; everywhere
	// else it stays empty and the half-blocks stand.
	logo      picture.Model
	logoKitty bool
	logoGrid  string
	// sessionCustomer mirrors the agent's session-scoped context override
	// ("" = none): the identity line and user dispatches follow it, while the
	// persisted context file stays untouched until the user runs /customer.
	sessionCustomer string
	identity        string // the tenant line, cached (rebuilt on switches/lookups)
	userLine        string // "role · email", from the cached token
	customerName    string // resolved display name for an ID-shaped context
	mouseOn         bool
	bellOn          bool // ring the terminal bell when a turn finishes (F2)
	// focused mirrors the terminal's focus reports (view.ReportFocus): a turn
	// finishing while the terminal is unfocused escalates the bell to a
	// desktop notification. Terminals that never report focus leave it true,
	// which degrades to the plain bell.
	focused bool

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
	stream       string
	turnActivity string    // status line: what the turn is doing right now
	turnStarted  time.Time // when the active turn began, for the elapsed display
	thinking     string    // tail of the model's thinking stream (status snippet only)
	turnCommands int       // agent tool calls completed this turn
	// toolLabels holds the label of every tool call in flight, keyed by call
	// ID: the session runs batched calls concurrently, so starts and results
	// interleave and a single "latest label" field would caption results with
	// the wrong command.
	toolLabels    map[string]string
	markdownStyle string // glamour style, resolved once before the program runs
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

// launchAISession is what bare `dci` calls through the root RunE
// (AI-DEFAULT-SPEC §3) — a var so tests can verify the routing without a
// terminal.
var launchAISession = runAISession

// runAISession is the entry point behind `dci ai` (ai_command.go) and, via
// launchAISession, behind bare `dci` at a human terminal.
func runAISession(configDir string) error {
	// Mouse capture defaults OFF so the terminal's own selection/copy works out
	// of the box (dogfood: capture broke copy/paste even with modifier keys in
	// some terminals); /mouse opts into wheel scrolling, and the choice
	// persists in the settings file (F5). Keyboard scrolling (PgUp/PgDn)
	// always works. The alt screen and mouse mode are declared per-frame in
	// View (v2's declarative model), so the program takes no options.
	initial := newAIModel(configDir)
	program := tea.NewProgram(initial)
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
	// band, bright text, faint placeholder. The blinking virtual cursor keeps
	// its reverse-video default.
	inputState := textarea.StyleState{
		CursorLine:  lipgloss.NewStyle(),
		Text:        lipgloss.NewStyle(),
		Prompt:      lipgloss.NewStyle().Bold(true),
		Placeholder: lipgloss.NewStyle().Faint(true),
	}
	input.SetStyles(textarea.Styles{
		Focused: inputState,
		Blurred: inputState,
		// The brand-pink block cursor: the virtual cursor renders in reverse
		// video, so the color becomes the block and the character under it
		// keeps the terminal's background color — legible on light and dark.
		Cursor: textarea.CursorStyle{Color: lipgloss.Color(aiBrandHex), Shape: tea.CursorBlock, Blink: true},
	})
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
		view:         viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		spin:         spinner.New(spinner.WithSpinner(aiDoitSpinner)),
		width:        80,
		height:       24,
		follow:       true,
		history:      history,
		historyPos:   len(history),
		userLine:     aiUserLine(),
		modelName:    modelName,
		mouseOn:      resolveAIMouse(settings),
		bellOn:       resolveAIBell(settings),
		focused:      true,
		fetchedNames: map[string][]nameCacheEntry{},
		// Resolved here — before the program owns stdin — so no render ever
		// queries the terminal mid-session (see aiMarkdownStyle).
		markdownStyle: aiMarkdownStyle(),
	}
	m.logo = picture.NewWithConfig(picture.Config{KittyID: aiLogoKittyID})
	m.logo.SetSize(aiLogoKittyCols, aiLogoKittyRows)
	m.identity = m.contextLabel()
	if key := resolveAIKey(settings); key != "" {
		m.session = newAIConversationSession(configDir, key, modelName, catalog)
	} else {
		m.sessionNote = "AI needs an Anthropic API key — ask a question to set one up, or export ANTHROPIC_API_KEY"
	}
	m.append(aiBannerBlock(&m))
	if len(history) == 0 {
		// First session ever (nothing in the persisted input history): show
		// the /help block up front. The banner's "/help for how this works"
		// pointer assumes the user knows slash commands exist — first-timers
		// don't, so give them the map instead of a reference to it.
		m.append(aiHelpText(len(m.catalog), m.session != nil))
	}
	if m.session == nil {
		// A keyless session offers the guided key setup up front instead of
		// waiting for the first question (F7 — dogfood tested one-shot first
		// and never met the type-a-question trigger). Esc drops to a normal
		// session; / commands work either way.
		m.keyEntry = true
		m.append(renderAIKeyOnboarding() + "\n" +
			aiEchoStyle.Render("(/ commands work without a key — type one, or press Esc to skip)"))
	}
	m.layout()
	return m
}

// aiDoitLogo is the DoiT "d" mark — the lowercase d with its floating dot —
// in the brand accent, the session's answer to Claude Code's robot. Rendered
// in half-blocks (two pixel rows per text row, roughly square pixels on 1:2
// terminal cells), rasterized from the mark's real proportions: the round
// bowl with its circular counter, the stem flush with the bowl's right edge,
// and the detached dot at mid-height.
var aiDoitLogo = []string{
	"       ███",
	"       ███",
	"  ▄▄██████",
	" ▄██▀▀▀███   ██",
	" ██▀   ▀██",
	" ███▄▄▄███",
	"  ▀█████▀",
}

// aiLogoStyle carries the DoiT accent; lipgloss degrades the hex to the
// terminal's nearest color when truecolor is unavailable.
var aiLogoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(aiBrandHex))

// aiBannerBlock is the transcript's opening block, Claude Code-style: the
// mark beside the product name, version, and the session facts that matter —
// model and key source, tenant identity, catalog size.
func aiBannerBlock(m *aiModel) string {
	logo := aiLogoStyle.Render(strings.Join(aiDoitLogo, "\n"))
	if m.logoGrid != "" {
		// A Kitty-graphics terminal answered the probe: the placeholder grid
		// resolves to the real raster of the mark (ai_flair.go).
		logo = m.logoGrid
	}

	versionLabel := "v" + version
	if version == "dev" {
		versionLabel = "(dev build)"
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
	// The banner spells out what the identity values are; the status line
	// keeps the compact unlabeled forms (statusLine).
	if m.userLine != "" {
		info = append(info, aiEchoStyle.Render("Role: "+m.userLine))
	}
	if m.identity != "" {
		info = append(info, aiEchoStyle.Render("Tenant: "+m.identity))
	}
	commandsLine := fmt.Sprintf("%d commands · /help for how this works", len(m.catalog))
	if aiCatalogMissingAPIOperations() {
		// Only the custom root commands are present: the spec cache is absent
		// (never logged in, or cleared). Say so — a bare small count reads as
		// the API commands having vanished.
		commandsLine = fmt.Sprintf("%d commands · API commands appear after /login", len(m.catalog))
	}
	info = append(info, aiEchoStyle.Render(commandsLine))

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
		output, exitCode, err := aiAgentModeRunner(ctx, []string{"get-customer", customerContext, "--output", "json"}, nil)
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

// aiUserLine is the signed-in identity: role and email from the cached
// token, role first so the banner's "Role:" label reads truthfully. Partner
// detection needs a claim the token does not carry yet, so non-doers read as
// Customer.
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
	return role + " · " + claims.Email
}

// contextLabel is the tenant line: the customer display name when resolved,
// with the raw context in parentheses; doers with no context get the fix
// spelled out; single-tenant customers see no tenant vocabulary (§6.1).
// effectiveCustomer is the tenant this session actually operates against:
// the agent's session-scoped override when set, otherwise the persisted (or
// env) context. Name resolution, dispatches, and the fetch-backed picker all
// key off this — never off the persisted context alone.
func (m *aiModel) effectiveCustomer() string {
	if m.sessionCustomer != "" {
		return m.sessionCustomer
	}
	return readCustomerContext(m.configDir)
}

func (m *aiModel) contextLabel() string {
	if m.sessionCustomer != "" {
		label := m.sessionCustomer
		if m.customerName != "" {
			label = m.customerName + " (" + m.sessionCustomer + ")"
		}
		return label + " · session-scoped"
	}
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
	// The logo's Init probes for Kitty graphics support (env-gated: it sends
	// nothing to terminals with no Kitty signal) and asks for the cell size.
	commands := []tea.Cmd{textarea.Blink, m.logo.Init()}
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
	m.view.SetContent(strings.Join(m.transcript, "\n\n"))
	if m.follow {
		m.view.GotoBottom()
	}
}

// layout recomputes the frame: the viewport takes everything the fixed
// chrome (header, completion popup, input, status) does not.
func (m *aiModel) layout() {
	m.input.SetWidth(m.width - 2)
	m.view.SetWidth(m.width)
	popup := len(m.completions)
	if popup > aiCompletionLimit {
		popup = aiCompletionLimit // the popup is a window; extra matches scroll
	}
	middle := popup + 1 /*input*/
	if m.picker != nil {
		middle = m.pickerLines()
	}
	chrome := 2 /*rules around the input*/ + middle + 1 /*status*/
	height := m.height - chrome
	if height < 1 {
		// A pane too short for the chrome itself: keep the viewport legal (a
		// zero height renders unbounded) and let aiFitFrame drop the overflow
		// from the top, so the frame still ends exactly at the last row.
		height = 1
	}
	m.view.SetHeight(height)
	m.refreshTranscript()
}

func (m aiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The banner logo's picture model routes its own protocol traffic — the
	// Kitty capability probe reply, its timeout tick, cell-size reports, and
	// built frames; it ignores everything else, so the blanket forward is
	// cheap. Once the terminal affirms Kitty support, the half-block mark
	// upgrades to the real raster.
	logoCmd := m.logo.Update(msg)
	if !m.logoKitty && picture.KittySupported() == picture.KittyCapabilitySupported &&
		aiKittyPlaceholderTerminal() {
		m.logoKitty = true
		logoCmd = tea.Batch(logoCmd, m.logo.SetImage(aiLogoImage()), m.logo.Toggle())
	}
	if m.logoKitty {
		if grid := m.logo.View().Content; grid != "" && grid != m.logoGrid {
			m.logoGrid = grid
			m.refreshBanner()
		}
	}
	model, cmd := m.dispatch(msg)
	if logoCmd == nil {
		return model, cmd
	}
	return model, tea.Batch(cmd, logoCmd)
}

// dispatch is the session's message handler proper; Update wraps it with the
// banner logo's plumbing.
func (m aiModel) dispatch(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.spinPhase++
		// A still-quiet turn rotates its waiting quip so a long analytical
		// pause reads as alive, not stuck.
		if m.turnActive && m.turnActivity == "" && time.Since(m.quipAt) >= aiQuipRotateEvery {
			m.turnQuip = spinnerQuip()
			m.quipAt = time.Now()
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
		card := renderAIRunCard(msg)
		if msg.exitCode == exitUsage && !msg.canceled && msg.runErr == "" && aiOutputIsArgvError(msg.output) {
			// A bare argument/flag rejection says what went wrong ("accepts
			// 1 arg(s), received 0") but not what the command wanted — append
			// the one-line usage, spelled the session's way.
			if usage := aiUsageLineFor(msg.argv); usage != "" {
				card += "\n" + aiEchoStyle.Render(usage)
			}
		}
		m.append(card)
		// A successful /logout invalidates everything the banner and identity
		// lines derived from the (now cleared) credential cache.
		if len(msg.argv) > 0 && msg.argv[0] == "logout" && msg.exitCode == 0 && !msg.canceled && msg.runErr == "" {
			m.refreshAuthState()
		}
		// A finished user command joins the conversation so follow-up
		// questions can reference what is on screen (§4.4).
		if m.session != nil && !msg.canceled && msg.runErr == "" {
			_ = m.session.Send(aiUserInput{
				Kind: aiInputCommandResult, Argv: msg.argv, Output: msg.output, Customer: msg.customer,
			})
		}
		return m, nil

	case aiLoginDoneMsg:
		return m.handleLoginDone(msg)

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

	case tea.FocusMsg:
		m.focused = true
		return m, nil

	case tea.BlurMsg:
		m.focused = false
		return m, nil

	case tea.PasteMsg:
		return m.handlePaste(msg)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handlePaste routes a bracketed paste to whichever text sink owns the
// keyboard. v1 delivered pastes as key messages, so the key handlers covered
// this; v2 makes them their own message type.
func (m aiModel) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.keyEntry:
		m.keyBuf += msg.Content
		return m, nil
	case m.picker != nil:
		m.pickerFilter += msg.Content
		m.clampPickerIndex()
		m.layout()
		return m, nil
	case m.fetchIntent != nil, m.dispatchApproval != nil, m.approval != nil, m.running != nil:
		return m, nil // states that only accept their answer keys
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.setCompletions(aiCompletionsFor(strings.TrimSpace(m.input.Value()), m.catalog, m.userCommands, aiCompletionMatchCap))
	return m, cmd
}

// refreshAuthState re-reads the credential cache a login/logout child just
// rewrote (the parent's in-memory copy predates the child) and rebuilds
// everything derived from it: the command catalog, the identity lines, and
// the banner. The identity change also invalidates every piece of
// tenant-scoped state — the agent's session-scoped customer override and the
// fetched picker names must not survive into another account's session
// (§6.2), same policy as /clear and /customer.
func (m *aiModel) refreshAuthState() {
	reloadCredentialCache()
	m.catalog = aiSessionCatalog()
	m.userLine = aiUserLine()
	m.sessionCustomer = ""
	m.fetchedNames = map[string][]nameCacheEntry{}
	if session, ok := m.session.(interface{ ClearCustomerOverride() }); ok {
		session.ClearCustomerOverride()
	}
	m.customerName = ""
	m.identity = m.contextLabel()
	m.refreshBanner()
}

// handleLoginDone resumes the session after the /login child ran on the real
// terminal. The child re-fetched and cached the API spec, so the rebuilt
// catalog picks up the API operations that were missing before login. The
// conversation session (if any) keeps its history: its system prompt's
// catalog snapshot is stale, but the model rediscovers commands via --help,
// which isn't worth dropping the conversation over.
func (m aiModel) handleLoginDone(msg aiLoginDoneMsg) (tea.Model, tea.Cmd) {
	m.refreshAuthState()
	if msg.err != nil {
		// The child already printed its own error to the terminal while the
		// session was suspended; this line keeps a record in the transcript.
		m.append(aiErrorStyle.Render("login failed: " + msg.err.Error()))
		return m, nil
	}
	m.append(tuiSuccessStyle.Render(fmt.Sprintf("Logged in — %d commands available.", len(m.catalog))))
	// refreshAuthState blanked the resolved display name; the fresh
	// credentials (and the just-hydrated get-customer operation) can resolve
	// it again. Logout skips this: without credentials the lookup can't run.
	if lookup := aiLookupCustomerName(readCustomerContext(m.configDir)); lookup != nil {
		return m, lookup
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
		m.turnQuip = spinnerQuip()
		m.quipAt = time.Now()
		m.turnStarted = time.Now()
		m.thinking = ""
		m.turnCommands = 0
		m.toolLabels = map[string]string{}
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
		if m.toolLabels == nil {
			m.toolLabels = map[string]string{}
		}
		m.toolLabels[event.ToolCallStarted.CallID] = aiToolCallLabel(*event.ToolCallStarted)
		m.turnActivity = "running " + m.inFlightLabel()

	case event.ToolResult != nil:
		m.turnCommands++
		label := m.toolLabels[event.ToolResult.CallID]
		delete(m.toolLabels, event.ToolResult.CallID)
		if label == "" {
			label = "command"
		}
		mark := "✓"
		if !event.ToolResult.OK {
			mark = "✗"
		}
		m.turnActivity = fmt.Sprintf("%s %s · %s", mark, label,
			event.ToolResult.Elapsed.Round(100*time.Millisecond))
		if len(m.toolLabels) > 0 {
			m.turnActivity += " · running " + m.inFlightLabel()
		}

	case event.ApprovalRequest != nil:
		request := *event.ApprovalRequest
		m.approval = &request
		m.append(renderAIApprovalRequest(request))

	case event.ContextSwitched != nil:
		// Agent switches are session-scoped (never persisted): track the
		// override locally so the identity line and user dispatches follow it.
		if event.ContextSwitched.By == "agent" {
			m.sessionCustomer = event.ContextSwitched.To
		}
		// Fetched picker names are tenant-scoped: entries from the previous
		// tenant must not resolve names (and dispatch IDs) in the new one.
		m.fetchedNames = map[string][]nameCacheEntry{}
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
		m.turnDoneMark = true // window-title checkmark until the next keypress
		m.approval = nil
		if m.bellOn && aiBellWorthy(usage) {
			commands = append(commands, aiTurnDoneSignal(m.focused))
		}
	}
	return m, tea.Batch(commands...)
}

// aiBellWorthy gates the end-of-turn bell (F2) to turns worth signaling: any
// turn that ran commands, or thought long enough for the user to look away.
// Without the gate, instant echo turns would beep too.
func aiBellWorthy(done aiTurnDone) bool {
	return done.ToolCalls > 0 || done.Wall >= 3*time.Second
}

// aiTurnDoneSignal announces a finished turn. The terminal bell always rings
// (the terminal maps BEL to its own sound/badge — the app cannot choose the
// waveform); when the terminal reported itself unfocused, the user looked
// away, so the bell escalates to an OSC 9 desktop notification — the harder-
// to-miss signal Claude Code-style tools send. Terminals without OSC 9
// support consume and ignore the sequence. Routed through tea.Raw so the
// bytes serialize with the renderer's own writes instead of racing a frame
// flush. Never in one-shot mode: a piped stream must stay byte-clean
// (ai_command.go).
func aiTurnDoneSignal(focused bool) tea.Cmd {
	signal := "\a"
	if !focused {
		signal += "\x1b]9;dci ai — response ready\x07"
	}
	return tea.Raw(signal)
}

// resetTurnState drops the in-flight turn UI when its session is being
// closed or replaced (/clear, /key set, /key clear): the closed session's
// terminal event can be lost (emit races the closed done channel against the
// buffered events channel), and without this the status line would show the
// spinner forever.
func (m *aiModel) resetTurnState() {
	m.turnActive = false
	m.turnActivity = ""
	m.thinking = ""
	m.stream = ""
	m.toolLabels = nil
	m.approval = nil
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
	m.append(renderAIMarkdown(text, m.view.Width(), m.markdownStyle))
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

// aiTrimTo shortens plain text to fit `width` cells, marking the cut. Under
// four cells there is no room for a legible fragment, so nothing is kept.
func aiTrimTo(text string, width int) string {
	if lipgloss.Width(text) <= width {
		return text
	}
	if width < 4 {
		return ""
	}
	runes := []rune(text)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return strings.TrimRight(string(runes), " ") + "…"
}

// inFlightLabel names what the agent is running right now: the single call's
// label, or a count when a concurrent batch has several in flight.
func (m *aiModel) inFlightLabel() string {
	if len(m.toolLabels) == 1 {
		for _, label := range m.toolLabels {
			return label
		}
	}
	return fmt.Sprintf("%d commands", len(m.toolLabels))
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

func (m aiModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	m.turnDoneMark = false // the user is back — drop the title checkmark

	// ctrl+l redraws, from every state (the repair gesture it is everywhere
	// else). The renderer only rewrites lines whose content changed, so a
	// frame the terminal disturbed on its own — scrolling the alt screen out
	// from under it with mouse capture off, another process writing to the
	// tty — stays smeared until every cell is repainted. Nothing else in the
	// session forces that, and the banner (static by definition) is exactly
	// what stays broken.
	if key == "ctrl+l" {
		// The repair gesture returns a known-good live view: the frame is
		// rebuilt (banner included, whatever state the terminal left the
		// cell grid in), the viewport snaps back to the latest content, and
		// the erase-and-repaint below rewrites every cell.
		m.follow = true
		m.layout()
		return m, tea.ClearScreen
	}

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
		m.view.HalfPageUp()
		m.follow = m.view.AtBottom()
		return m, nil
	case "pgdown", "ctrl+d":
		if key == "ctrl+d" && strings.TrimSpace(m.input.Value()) == "" && m.running == nil && m.approval == nil {
			return m, tea.Quit
		}
		m.view.HalfPageDown()
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
		// With the popup open, Enter accepts the highlighted completion —
		// parity with Claude Code's picker (F1; dogfood: Enter submitting the
		// still-partial token reads as the picker ignoring the selection). An
		// input that already names a completion exactly submits, so /quit⏎
		// never needs a double-Enter.
		if len(m.completions) > 0 && !aiCompletionExact(m.input.Value(), m.completions) {
			m.acceptCompletion()
			return m, nil
		}
		return m.submit()

	case "tab":
		m.acceptCompletion()
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
	m.setCompletions(aiCompletionsFor(strings.TrimSpace(m.input.Value()), m.catalog, m.userCommands, aiCompletionMatchCap))
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

// aiCompletionWindow returns the first visible row of the completion popup:
// a size-row window over total candidates that keeps the highlighted row
// visible — pinned to the bottom row once the selection moves past the first
// page (v2.6.2 dogfood: the popup used to truncate at the cap, leaving
// matches past it unreachable). Stateless on purpose: derived from the index
// alone, so wrap-around navigation needs no offset bookkeeping.
func aiCompletionWindow(index, total, size int) int {
	start := index - size + 1
	if start < 0 {
		start = 0
	}
	if max := total - size; start > max && max >= 0 {
		start = max
	}
	return start
}

// acceptCompletion replaces the input with the highlighted completion, ready
// for arguments. Tab always accepts; Enter accepts unless the input already
// names a completion exactly (see handleKey).
func (m *aiModel) acceptCompletion() {
	if len(m.completions) == 0 {
		return
	}
	selected := m.completions[m.completionIndex]
	m.input.Reset()
	m.input.SetValue("/" + selected.Value + " ")
	m.setCompletions(nil)
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
		// A bare /beta would dispatch a child whose help is shell-shaped
		// ("dci beta <command>") — confusing inside the session, where the
		// same commands run as /beta <command>. List them the session's way.
		if len(route.argv) == 1 && route.argv[0] == "beta" {
			m.append(aiBetaListText(m.catalog))
			return m, nil
		}
		// /login needs the real terminal (the browser OAuth flow refuses to
		// run headless), which a piped dispatch child never has: suspend the
		// session and hand the child the tty instead of degrading to the
		// headless error. The child also re-fetches and caches the API spec,
		// so aiLoginDoneMsg can rebuild the catalog.
		if route.argv[0] == "login" {
			return m, tea.ExecProcess(exec.Command(aiExecutablePath(), route.argv...), func(err error) tea.Msg {
				return aiLoginDoneMsg{err: err}
			})
		}
		if intent := aiPickerIntentFor(route.argv); intent != nil {
			entries := intent.cachedEntries(m.configDir, m.effectiveCustomer())
			if len(entries) == 0 {
				entries = m.fetchedNames[intent.target.resource]
			}
			if len(entries) == 0 {
				// No names on hand: fetch them (what the child's own F1/F2
				// would do) instead of degrading to a usage error.
				m.fetchIntent = intent
				return m, tea.Batch(m.spin.Tick, aiFetchNames(intent, m.effectiveCustomer()))
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
		customer:        m.effectiveCustomer(),
		sessionCustomer: m.sessionCustomer,
		width:           m.width,
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
func (m aiModel) handleKeyEntryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		if m.session != nil {
			// /key set over a live session: the replacement key takes over,
			// and the old session (built on the old key) goes with it.
			_ = m.session.Close()
			m.resetTurnState()
		}
		// The session runs on the resolved key, not the typed one verbatim:
		// ANTHROPIC_API_KEY wins over the file everywhere else (resolveAIKey),
		// and building on the typed key here would leave /key reporting a
		// source the live session is not actually using.
		activeKey := resolveAIKey(settings)
		m.session = newAIConversationSession(m.configDir, activeKey, m.modelName, m.catalog)
		m.sessionNote = ""
		m.append(tuiSuccessStyle.Render("Key saved — AI is ready."))
		if activeKey != key {
			m.append(aiNoticeStyle.Render("ANTHROPIC_API_KEY is set and overrides the saved key — the session keeps using the environment's key."))
		}
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
	if msg.Text != "" && m.keyBuf == "" && m.pendingQuestion == "" &&
		strings.HasPrefix(msg.Text, "/") {
		// The startup hint promises / commands work without a key: a slash on
		// an empty buffer is a command being typed, not a key — drop to the
		// normal input with the popup open. A slash into a non-empty buffer
		// (mid-key paste) or after a question queued the setup stays key input.
		m.keyEntry = false
		m.input.SetValue(msg.Text)
		m.setCompletions(aiCompletionsFor(strings.TrimSpace(m.input.Value()), m.catalog, m.userCommands, aiCompletionMatchCap))
		return m, nil
	}
	if msg.Text != "" {
		m.keyBuf += msg.Text
	}
	return m, nil
}

// handlePickerKey drives the name selection: type to filter with the CLI's
// own matcher, ↑/↓ to move, enter dispatches with the chosen ID, esc cancels.
func (m aiModel) handlePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
	if msg.Text != "" {
		m.pickerFilter += msg.Text
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
		// so stale tenant data can't leak into the next question (§6.2). The
		// rebuilt session starts with no customer override, so the TUI's
		// mirror of it — and the tenant-scoped fetched names — reset with it.
		m.transcript = nil
		m.stream = ""
		m.sessionCustomer = ""
		m.fetchedNames = map[string][]nameCacheEntry{}
		m.customerName = ""
		m.identity = m.contextLabel()
		m.refreshBanner()
		m.append(aiBannerBlock(&m))
		if m.session != nil {
			_ = m.session.Close()
			m.resetTurnState()
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
		if len(route.args) == 0 && m.sessionCustomer != "" {
			result += " (AI session override active: " + m.sessionCustomer + ")"
		}
		if len(route.args) > 0 {
			// The user persisted a context: that wins over any earlier agent
			// session-scoped switch, in the session and in its children —
			// and the previous tenant's fetched names go with it.
			m.sessionCustomer = ""
			m.fetchedNames = map[string][]nameCacheEntry{}
			if session, ok := m.session.(interface{ ClearCustomerOverride() }); ok {
				session.ClearCustomerOverride()
			}
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
		// Flipping the flag is the whole toggle: the next frame declares the
		// new mouse mode on the view (v2's declarative model).
		m.mouseOn = !m.mouseOn
		m.persistToggle(func(settings *aiSettings) { settings.Mouse = boolPointer(m.mouseOn) })
		if m.mouseOn {
			m.append(aiEchoStyle.Render("mouse capture on — wheel scrolls the transcript; /mouse to select text (saved for future sessions)"))
			return m, nil
		}
		m.append(aiEchoStyle.Render("mouse capture off — select and copy text normally; /mouse to re-enable wheel scrolling"))
		return m, nil
	case "default":
		return m.runDefaultVerb(route.args)
	case "bell":
		m.bellOn = !m.bellOn
		m.persistToggle(func(settings *aiSettings) { settings.Bell = boolPointer(m.bellOn) })
		if m.bellOn {
			m.append(aiEchoStyle.Render("bell on — the terminal bell rings when a turn finishes (plus a desktop notification if you've switched away); /bell to disable"))
		} else {
			m.append(aiEchoStyle.Render("bell off — turns finish silently; /bell to re-enable"))
		}
		return m, nil
	case "model":
		return m.runModelVerb(route.args)
	case "key":
		return m.runKeyVerb(route.args)
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

// runDefaultVerb shows or sets what bare `dci` opens at a human terminal
// (AI-DEFAULT-SPEC §6): "session" (the default) or "help" (the persisted
// opt-out).
func (m aiModel) runDefaultVerb(args []string) (tea.Model, tea.Cmd) {
	switch len(args) {
	case 0:
		current := "session"
		if !aiDefaultEnabled(m.configDir) {
			current = "help"
		}
		m.append(aiEchoStyle.Render("bare dci opens: " + current + " — /default session|help to change"))
		return m, nil
	case 1:
		choice := strings.ToLower(strings.TrimSpace(args[0]))
		if choice != "session" && choice != "help" {
			m.append(aiErrorStyle.Render("usage: /default [session|help]"))
			return m, nil
		}
		m.persistToggle(func(settings *aiSettings) { settings.Default = choice })
		if choice == "help" {
			m.append(tuiSuccessStyle.Render("Saved — bare dci shows the help screen; dci ai still opens this session."))
		} else {
			m.append(tuiSuccessStyle.Render("Saved — bare dci opens this session."))
		}
		return m, nil
	default:
		m.append(aiErrorStyle.Render("usage: /default [session|help]"))
		return m, nil
	}
}

// persistToggle saves one settings mutation (F2/F5: the /bell and /mouse
// choices; the /default opt-out). The in-session toggle stands even when the
// save fails — the error is reported, not fatal.
func (m *aiModel) persistToggle(mutate func(*aiSettings)) {
	settings := loadAISettings(m.configDir)
	mutate(&settings)
	if err := saveAISettings(m.configDir, settings); err != nil {
		m.append(aiErrorStyle.Render("could not save the setting: " + err.Error()))
	}
}

func boolPointer(value bool) *bool { return &value }

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
	content := stripANSI(strings.Join(transcript, "\n\n")) + "\n"
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

// runKeyVerb implements /key: no argument shows where the session's Anthropic
// API key comes from, "set" reopens the guided entry, "clear" removes the
// saved key. Before this verb, changing or clearing a saved key meant finding
// and editing ai_settings.json by hand.
func (m aiModel) runKeyVerb(args []string) (tea.Model, tea.Cmd) {
	switch {
	case len(args) == 0:
		m.append(m.keyInfoText())
		return m, nil
	case len(args) == 1 && args[0] == "set":
		m.keyEntry = true
		m.keyBuf = ""
		m.pendingQuestion = ""
		m.append(renderAIKeyOnboarding())
		return m, nil
	case len(args) == 1 && args[0] == "clear":
		settings := loadAISettings(m.configDir)
		if settings.APIKey == "" {
			m.append(aiEchoStyle.Render("no key saved in " + aiSettingsFileName + " — nothing to clear"))
			return m, nil
		}
		settings.APIKey = ""
		if err := saveAISettings(m.configDir, settings); err != nil {
			m.append(aiErrorStyle.Render("could not save: " + err.Error()))
			return m, nil
		}
		if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
			// The env var was already winning over the cleared key, so the
			// live session keeps working — but say which key that leaves.
			m.append(tuiSuccessStyle.Render("Saved key cleared.") + "\n" +
				aiEchoStyle.Render("ANTHROPIC_API_KEY is set, so AI keeps using the environment's key."))
			return m, nil
		}
		if m.session != nil {
			_ = m.session.Close()
			m.session = nil
			m.resetTurnState()
		}
		m.sessionNote = "AI needs an Anthropic API key — ask a question to set one up, or export ANTHROPIC_API_KEY"
		m.append(tuiSuccessStyle.Render("Saved key cleared — AI is off.") + "\n" +
			aiEchoStyle.Render("/key set adds a new one; / commands keep working without a key."))
		return m, nil
	default:
		m.append(aiErrorStyle.Render("usage: /key [set|clear]"))
		return m, nil
	}
}

// keyInfoText reports which key the session resolved and where it came from —
// the environment silently overriding a saved key is exactly what a user
// cannot see from a 401 alone.
func (m aiModel) keyInfoText() string {
	settings := loadAISettings(m.configDir)
	envKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	savedKey := strings.TrimSpace(settings.APIKey)
	lines := make([]string, 0, 4)
	switch {
	case envKey != "":
		lines = append(lines, aiCardHeadStyle.Render("API key: ")+aiMaskAPIKey(envKey)+aiEchoStyle.Render("  (from ANTHROPIC_API_KEY)"))
		if savedKey != "" {
			lines = append(lines, aiEchoStyle.Render("A key is also saved in "+aiSettingsFileName+" ("+aiMaskAPIKey(savedKey)+") — the environment wins."))
		}
	case savedKey != "":
		lines = append(lines, aiCardHeadStyle.Render("API key: ")+aiMaskAPIKey(savedKey)+aiEchoStyle.Render("  (saved in "+aiSettingsFileName+")"))
	default:
		lines = append(lines, aiNoticeStyle.Render("No API key configured — AI is off."))
	}
	lines = append(lines, aiEchoStyle.Render("/key set replaces the saved key · /key clear removes it"))
	return strings.Join(lines, "\n")
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
		usage := m.lastUsage
		line := fmt.Sprintf("Last turn: %d in / %d out / %d cache-read tokens; %d rounds, %d tool calls, %.1fs",
			usage.InputTokens, usage.OutputTokens, usage.CacheRead, usage.Rounds, usage.ToolCalls, usage.Wall.Seconds())
		if usage.FirstText > 0 {
			line += fmt.Sprintf(" (first text at %.1fs)", usage.FirstText.Seconds())
		}
		lines = append(lines, aiEchoStyle.Render(line))
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
func aiDispatchEnv(width int, customer string) []string {
	extras := append([]string{
		"DCI_NO_TUI=1", "DCI_AGENT_MODE=0", "DCI_SESSION_RENDER=1",
		"COLOR=1", "CLICOLOR_FORCE=1",
		fmt.Sprintf("COLUMNS=%d", width),
	}, aiCustomerEnv(customer)...)
	return aiChildEnv(extras)
}

func aiDispatchCommand(ctx context.Context, run *aiRunState) tea.Cmd {
	argv, started, customer, width := run.argv, run.started, run.customer, run.width
	sessionCustomer := run.sessionCustomer
	return func() tea.Msg {
		command := exec.CommandContext(ctx, aiExecutablePath(), argv...)
		command.Env = aiDispatchEnv(width, sessionCustomer)
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
// the Bubble Tea program starts, using lipgloss v2's explicit standalone
// detection against the session's own streams. Mid-session probing stays
// off-limits by design: glamour's WithAutoStyle would run its own OSC 11
// query on every render, racing the program's input reader for the reply
// (v2 has a first-class tea.RequestBackgroundColor for in-program detection;
// adopting it is a possible follow-up, not part of the mechanical port).
// Under v1, bubbletea's package init() ran the terminal query before main()
// — which is where a mute terminal (a pty harness that never answers OSC 11)
// paid termenv's 5s OSCTimeout. v2 removes that init, so this explicit call
// is now the session's one background query and the only place a mute
// terminal waits — bounded by lipgloss's 2s query timeout, down from
// termenv's 5s. Real terminals answer in ~1ms.
func aiMarkdownStyle() string {
	if lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
		return "dark"
	}
	return "light"
}

// renderAIMarkdown renders committed answer text as markdown, falling back
// to the raw text when the renderer objects (exotic terminals).
func renderAIMarkdown(text string, width int, style string) string {
	// Use the terminal width we've got: answers, and especially markdown
	// tables, wrap at the viewport's real width instead of an arbitrary cap
	// that squeezed tables into wrapped cells on wide terminals.
	if width <= 0 {
		width = 100 // size unknown (first render, tests)
	} else if width < 20 {
		width = 20 // keep glamour sane on absurdly narrow panes
	}
	if style == "" {
		style = "dark"
	}
	renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width))
	if err != nil {
		return text
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return strings.Trim(rendered, "\n")
}

// aiOutputIsArgvError reports whether a failed dispatch's output is cobra's
// own argument/flag rejection — the only exit-2 failures where the one-line
// usage answers the error. exitUsage is shared by richer domain errors (an
// ambiguous name listing its candidates, a body-validation message naming
// the field) that already explain themselves; a usage line after those would
// misdirect the reader toward the argument count.
func aiOutputIsArgvError(output string) bool {
	lower := strings.ToLower(output)
	for _, fragment := range []string{
		"accepts ",
		"requires at least",
		"requires exactly",
		"unknown flag",
		"unknown shorthand flag",
	} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// aiUsageLineFor reconstructs a failed dispatch's one-line usage from the
// live cobra tree, spelled the session's way ("/beta run-report id"). Only
// leaf commands get one: a group's own help already lists its subcommands.
func aiUsageLineFor(argv []string) string {
	if len(argv) == 0 || cli.Root == nil {
		return ""
	}
	command := findChildCommand(findDCICommand(), argv[0])
	if command == nil {
		command = findChildCommand(cli.Root, argv[0])
	}
	if command == nil {
		return ""
	}
	// Descend while the following words name subcommands ("beta run-report",
	// "customer-context set"); the matched words become the usage's prefix.
	matched := 1
	for matched < len(argv) {
		child := findChildCommand(command, argv[matched])
		if child == nil {
			break
		}
		command = child
		matched++
	}
	if len(command.Commands()) > 0 || strings.TrimSpace(command.Use) == "" {
		return ""
	}
	prefix := "/"
	if matched > 1 {
		prefix = "/" + strings.Join(argv[:matched-1], " ") + " "
	}
	return "usage: " + prefix + command.Use
}

// findChildCommand returns parent's direct subcommand matching name (or one
// of its aliases); nil parent or no match return nil.
func findChildCommand(parent *cobra.Command, name string) *cobra.Command {
	if parent == nil {
		return nil
	}
	for _, child := range parent.Commands() {
		if child.Name() == name || child.HasAlias(name) {
			return child
		}
	}
	return nil
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

// aiBetaListText renders the beta subcommands spelled the way the session
// runs them (/beta <command>) — the child's own help spells them shell-style
// ("dci beta <command>"), which reads like it belongs outside the session.
func aiBetaListText(catalog []aiCatalogEntry) string {
	lines := []string{aiCardHeadStyle.Render("Beta commands") + aiEchoStyle.Render("  early access; may change or be removed")}
	found := false
	for _, entry := range catalog {
		if strings.HasPrefix(entry.Path, "beta ") {
			found = true
			lines = append(lines, "  /"+entry.Path+"  "+aiEchoStyle.Render(strings.TrimPrefix(entry.Summary, "(beta) ")))
		}
	}
	if !found {
		return aiNoticeStyle.Render("No beta commands are available in this build.")
	}
	lines = append(lines, aiEchoStyle.Render("Run one with /beta <command> — add --help to see its flags."))
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
		aiEchoStyle.Render("Scrolling: PgUp/PgDn move the transcript. With mouse capture off the wheel scrolls the terminal itself, which slides this frame out of view and can leave it smeared — ctrl+l redraws it and jumps back to the latest content."),
		"",
		"  tab/enter complete · ↑/↓ history or popup · PgUp/PgDn scroll (wheel via /mouse) · ctrl+l redraw+latest · esc clears or cancels")
	return strings.Join(lines, "\n")
}

func (m aiModel) View() tea.View {
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
		start := aiCompletionWindow(m.completionIndex, len(m.completions), aiCompletionLimit)
		end := start + aiCompletionLimit
		if end > len(m.completions) {
			end = len(m.completions)
		}
		for index := start; index < end; index++ {
			completion := m.completions[index]
			line := fmt.Sprintf(" /%s  %s", completion.Value, aiEchoStyle.Render(completion.Summary))
			if index == m.completionIndex {
				line = aiSelectedStyle.Render(" /" + completion.Value + " ")
				if completion.Summary != "" {
					line += " " + aiEchoStyle.Render(completion.Summary)
				}
				if len(m.completions) > aiCompletionLimit {
					line += aiEchoStyle.Render(fmt.Sprintf("  %d/%d", index+1, len(m.completions)))
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
	view := tea.NewView(aiFitFrame(b.String(), m.width, m.height))
	view.AltScreen = true
	view.ReportFocus = true // focus gates the end-of-turn desktop notification
	view.WindowTitle = aiWindowTitle(&m)
	view.ProgressBar = aiDockProgress(&m)
	if m.mouseOn {
		view.MouseMode = tea.MouseModeCellMotion
	}
	return view
}

// aiFitFrame pins the frame to the terminal's cell grid exactly: every line
// clamped to the width, exactly `height` lines.
//
// Two reasons, both about the alternate screen. First, only the viewport
// bounds itself — the chrome around it does not, so a long narration snippet,
// a wide completion summary, or a picker row can run past the right edge and
// leave the cut to Bubble Tea's own truncation. Second, and the one that
// shows: the standard renderer skips any line whose text matches what it last
// wrote, so rows the terminal disturbed behind our back — the wheel scrolling
// the alt screen out from under the frame and compositing scrollback into it,
// another process writing to the tty — are never rewritten while their
// content is unchanged. The banner is the frame's most static block, so it
// keeps the damage longest. A frame that owns every cell is a frame one
// repaint (ctrl+l) restores completely.
//
// Overflow is dropped from the top: the input and status line are the live
// chrome, so a pane too short to hold everything keeps those and loses
// transcript rows.
func aiFitFrame(frame string, width, height int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	lines := strings.Split(frame, "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	clamp := lipgloss.NewStyle().MaxWidth(width)
	for index, line := range lines {
		if lipgloss.Width(line) > width {
			lines[index] = clamp.Render(line)
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// aiRule draws one horizontal frame line, with an optional right-aligned
// hint embedded in it.
func aiRule(width int, hint string) string {
	if width < 4 {
		width = 4
	}
	// The rules open with a short brand fade (ai_flair.go) before settling
	// into the usual dim line.
	fade := len(aiRuleRamp)
	if fade > width/3 {
		fade = width / 3
	}
	lead := aiRuleGradient(fade)
	if hint == "" {
		return lead + aiEchoStyle.Render(strings.Repeat("─", width-fade))
	}
	label := " " + hint + " "
	dashes := width - fade - lipgloss.Width(label) - 2
	if dashes < 1 {
		dashes = 1
	}
	return lead + aiEchoStyle.Render(strings.Repeat("─", dashes)) + aiNoticeStyle.Render(label) + aiEchoStyle.Render("──")
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

// pickerWindow is the slice of candidates pickerView draws: a window of
// aiPickerVisibleRows that follows the highlighted row.
func (m aiModel) pickerWindow() (rows []nameCacheEntry, start, end int) {
	rows = m.picker.filtered(m.pickerFilter)
	if m.pickerIndex >= aiPickerVisibleRows {
		start = m.pickerIndex - aiPickerVisibleRows + 1
	}
	end = start + aiPickerVisibleRows
	if end > len(rows) {
		end = len(rows)
	}
	if start > end {
		start = end
	}
	return rows, start, end
}

// pickerLines is how many rows pickerView occupies. layout() must size the
// viewport from the same count the view renders: a frame that comes out a row
// long loses a transcript row off the top (aiFitFrame trims the overflow),
// one that comes out short pads below the status line. An empty result is the
// case that used to disagree — it draws the "no matches" note in place of the
// candidate window, not alongside a window of zero rows.
func (m aiModel) pickerLines() int {
	rows, start, end := m.pickerWindow()
	const headerAndFilter = 2
	if len(rows) == 0 {
		return headerAndFilter + 1
	}
	return headerAndFilter + end - start
}

// pickerView renders the name selection: a header with the count, a window
// of candidates around the highlighted row, and the live filter line.
func (m aiModel) pickerView() string {
	rows, start, end := m.pickerWindow()
	var b strings.Builder
	b.WriteString(aiCardHeadStyle.Render(fmt.Sprintf("Select a %s", m.picker.resource)) +
		aiEchoStyle.Render(fmt.Sprintf("  %d match(es) — type to filter", len(rows))))
	b.WriteString("\n")
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

// statusWidth is the room on the status row, less any text already committed
// to it (the spinner). It falls back to 80 columns before the first
// WindowSizeMsg, so a status line is never trimmed to nothing at startup.
func (m aiModel) statusWidth(committed string) int {
	width := m.width
	if width <= 0 {
		width = 80
	}
	return width - lipgloss.Width(committed)
}

// aiStatusRow assembles the status line from parts of decreasing importance,
// so a narrow pane loses the least useful one instead of whatever happens to
// sit at the right edge:
//
//	label   introduces the subject ("running", "fetching") — dropped with it
//	subject the variable part: a command, a narration snippet, a resource —
//	        trimmed before anything is dropped, since its tail words carry
//	        the least, then dropped if not even a fragment fits
//	detail  extras such as elapsed time or a command count
//	afford  how to leave the state ("esc to cancel") — never dropped
//
// The affordance survives because these are the states where not knowing the
// way out costs the most: a pending destructive command, an in-flight turn.
func aiStatusRow(width int, label, subject, detail, afford string) string {
	build := func(subject, detail string) string {
		parts := make([]string, 0, 3)
		if subject != "" {
			if label != "" {
				subject = label + " " + subject
			}
			parts = append(parts, subject)
		}
		if detail != "" {
			parts = append(parts, detail)
		}
		if afford != "" {
			parts = append(parts, afford)
		}
		return strings.Join(parts, " · ")
	}
	if full := build(subject, detail); lipgloss.Width(full) <= width {
		return full
	}
	// What a one-cell subject costs beyond the rest of the row: the label and
	// the separator it brings with it, whichever form build() chose.
	withoutSubject := lipgloss.Width(build("", detail))
	overhead := lipgloss.Width(build("X", detail)) - withoutSubject - 1
	if trimmed := aiTrimTo(subject, width-withoutSubject-overhead); trimmed != "" {
		return build(trimmed, detail)
	}
	if row := build("", detail); lipgloss.Width(row) <= width {
		return row
	}
	return build("", "")
}

func (m aiModel) statusLine() string {
	if m.picker != nil {
		return aiEchoStyle.Render(aiStatusRow(m.statusWidth(""),
			"", "↑/↓ select · enter run", "", "esc cancel"))
	}
	if m.approval != nil || m.dispatchApproval != nil {
		return aiApproveStyle.Render(aiStatusRow(m.statusWidth(""),
			"", "destructive command pending — y run", "", "n decline"))
	}
	dark := m.markdownStyle != "light"
	if m.fetchIntent != nil {
		spin := aiSpinnerStyle(0).Render(m.spin.View())
		return spin + " " + aiEchoStyle.Render(aiStatusRow(m.statusWidth(m.spin.View()+" "),
			"fetching", m.fetchIntent.resource+" names…", "", "esc to cancel"))
	}
	if m.running != nil {
		subject := "/" + strings.Join(m.running.argv, " ")
		spin := aiSpinnerStyle(time.Since(m.running.started)).Render(m.spin.View())
		row := aiStatusRow(m.statusWidth(m.spin.View()+" "),
			"running", subject, time.Since(m.running.started).Round(time.Second).String(), "esc to cancel")
		return spin + " " + aiEchoStyle.Render(aiShimmerRow(row, subject, m.spinPhase, dark))
	}
	if m.turnActive {
		activity := m.turnActivity
		if activity == "" {
			// A quiet turn shows the rotating waiting quip (ai_flair.go); the
			// literal fallback covers states created outside a TurnStarted.
			activity = m.turnQuip
		}
		if activity == "" {
			activity = "thinking (" + m.modelName + ")"
		}
		detail := make([]string, 0, 2)
		elapsed := time.Duration(0)
		if !m.turnStarted.IsZero() {
			elapsed = time.Since(m.turnStarted)
			detail = append(detail, elapsed.Round(time.Second).String())
		}
		if m.turnCommands > 0 {
			commands := fmt.Sprintf("%d command", m.turnCommands)
			if m.turnCommands > 1 {
				commands += "s"
			}
			detail = append(detail, commands)
		}
		spin := aiSpinnerStyle(elapsed).Render(m.spin.View())
		row := aiStatusRow(m.statusWidth(m.spin.View()+" "),
			"", activity, strings.Join(detail, " · "), "esc to cancel")
		return spin + " " + aiEchoStyle.Render(aiShimmerRow(row, activity, m.spinPhase, dark))
	}
	if m.keyEntry {
		return aiEchoStyle.Render(aiStatusRow(m.statusWidth(""),
			"", "paste your key · enter save", "", "esc cancel"))
	}
	var hint string
	switch {
	case m.ctrlCArmed:
		hint = "ctrl+c again to quit"
	case m.session != nil:
		hint = "Ask \"how much do we spend on tokens per AI model?\" · / for commands"
	default:
		hint = "ask a question to set up AI · / for commands"
	}
	// Idle inverts the priority: which tenant the session is pointed at
	// outranks the prompt hint, so a narrow pane loses the hint rather than
	// half the customer's name.
	if m.identity == "" {
		return aiEchoStyle.Render(aiTrimTo(hint, m.statusWidth("")))
	}
	identity := aiTrimTo(m.identity, m.statusWidth(""))
	hint = aiTrimTo(hint, m.statusWidth(identity+" · "))
	if hint == "" {
		return aiEchoStyle.Render(identity)
	}
	return aiEchoStyle.Render(identity + " · " + hint)
}
