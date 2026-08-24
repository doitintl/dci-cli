package main

// P1 of AI-SPEC: the `dci ai` inline session program. Runs Bubble Tea in its
// default inline mode — no alternate screen — so completed output lands in
// the terminal's native scrollback via tea.Println while only the bottom
// region (completion popup, input, status line) is managed. Slash commands
// dispatch to the CLI itself as a subprocess (AI-SPEC §7.4); plain text is
// the AI path, which in P1 prints the not-yet-available notice (AI-SPEC §2).
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
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	aiEchoStyle     = lipgloss.NewStyle().Faint(true)
	aiCardHeadStyle = lipgloss.NewStyle().Bold(true)
	aiErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	aiSelectedStyle = lipgloss.NewStyle().Reverse(true)
	aiNoticeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

const aiCompletionLimit = 6

// aiRunState tracks the one in-flight subprocess; the session runs at most
// one command at a time, matching the one-conversation mental model.
type aiRunState struct {
	argv    []string
	cancel  context.CancelFunc
	started time.Time
}

// aiCmdDoneMsg reports a finished dispatch back into the Update loop.
type aiCmdDoneMsg struct {
	argv     []string
	output   string
	exitCode int
	runErr   string
	canceled bool
	elapsed  time.Duration
}

type aiModel struct {
	configDir string
	catalog   []aiCatalogEntry

	input textarea.Model
	spin  spinner.Model
	width int

	completions     []aiCompletion
	completionIndex int

	history    []string
	historyPos int
	draft      string

	running    *aiRunState
	ctrlCArmed bool
	identity   string
}

// runAISession is the entry point behind `dci ai` (ai_command.go).
func runAISession(configDir string) error {
	program := tea.NewProgram(newAIModel(configDir))
	_, err := program.Run()
	return err
}

func newAIModel(configDir string) aiModel {
	input := textarea.New()
	input.Prompt = "› "
	input.Placeholder = "Ask about your cloud costs, or type / for commands"
	input.ShowLineNumbers = false
	input.CharLimit = 0
	input.SetHeight(1)
	input.SetWidth(78)
	input.KeyMap.InsertNewline.SetEnabled(false)
	input.Focus()

	history := loadAIHistory(configDir)
	return aiModel{
		configDir:  configDir,
		catalog:    aiSessionCatalog(),
		input:      input,
		spin:       spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		width:      80,
		history:    history,
		historyPos: len(history),
		identity:   aiIdentitySegment(configDir),
	}
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

func (m aiModel) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		tea.Println(aiNoticeStyle.Render("Cloud Intelligence™ interactive session (preview)")+
			aiEchoStyle.Render("  —  /help for commands, /quit to leave")),
	)
}

func (m aiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.input.SetWidth(msg.Width - 2)
		return m, nil

	case spinner.TickMsg:
		if m.running == nil {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case aiCmdDoneMsg:
		m.running = nil
		return m, tea.Println(renderAIRunCard(msg))

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m aiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// While a command runs, the only inputs are the cancel keys; everything
	// else waits so a queued keystroke can't interleave with the result card.
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
			m.completions = nil
			m.ctrlCArmed = false
			return m, nil
		}
		if m.ctrlCArmed {
			return m, tea.Quit
		}
		m.ctrlCArmed = true
		return m, nil

	case "ctrl+d":
		if strings.TrimSpace(m.input.Value()) == "" {
			return m, tea.Quit
		}
		return m, nil

	case "enter":
		return m.submit()

	case "tab":
		if len(m.completions) > 0 {
			selected := m.completions[m.completionIndex]
			m.input.Reset()
			m.input.SetValue("/" + selected.Value + " ")
			m.completions = nil
			m.completionIndex = 0
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
		m.input.Reset()
		m.completions = nil
		m.completionIndex = 0
		m.historyPos = len(m.history)
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.completions = aiCompletionsFor(strings.TrimSpace(m.input.Value()), m.catalog, aiCompletionLimit)
	if m.completionIndex >= len(m.completions) {
		m.completionIndex = 0
	}
	return m, cmd
}

// setInput replaces the input content (history navigation), leaving the
// completion popup closed: recalled lines are complete, not being composed.
func (m *aiModel) setInput(value string) {
	m.input.Reset()
	m.input.SetValue(value)
	m.completions = nil
	m.completionIndex = 0
}

func (m aiModel) submit() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	route := aiRouteLine(line, m.catalog)
	if route.kind == aiRouteEmpty {
		m.input.Reset()
		m.completions = nil
		return m, nil
	}

	m.history = appendAIHistory(m.configDir, m.history, line)
	m.historyPos = len(m.history)
	m.draft = ""
	m.input.Reset()
	m.completions = nil
	m.completionIndex = 0

	echo := tea.Println(aiEchoStyle.Render("› " + line))

	switch route.kind {
	case aiRouteChat:
		return m, tea.Sequence(echo, tea.Println(aiNoticeStyle.Render(
			"Natural-language questions aren't available in this build yet.")+
			aiEchoStyle.Render("  Commands work now — type / to browse them.")))

	case aiRouteInvalid:
		return m, tea.Sequence(echo, tea.Println(aiErrorStyle.Render("could not parse: "+route.text)))

	case aiRouteUnknown:
		lines := []string{aiErrorStyle.Render("unknown command: /" + route.text)}
		if len(route.suggestions) > 0 {
			lines = append(lines, aiEchoStyle.Render("did you mean: /"+strings.Join(route.suggestions, ", /")))
		} else {
			lines = append(lines, aiEchoStyle.Render("type / to browse available commands"))
		}
		return m, tea.Sequence(echo, tea.Println(strings.Join(lines, "\n")))

	case aiRouteVerb:
		return m.runVerb(route, echo)

	case aiRouteDispatch:
		ctx, cancel := context.WithCancel(context.Background())
		m.running = &aiRunState{argv: route.argv, cancel: cancel, started: time.Now()}
		return m, tea.Batch(echo, m.spin.Tick, aiRunCommand(ctx, route.argv, m.running.started))
	}
	return m, nil
}

func (m aiModel) runVerb(route aiRoute, echo tea.Cmd) (tea.Model, tea.Cmd) {
	switch route.verb {
	case "help":
		return m, tea.Sequence(echo, tea.Println(aiHelpText(len(m.catalog))))
	case "clear":
		return m, tea.ClearScreen
	case "quit":
		return m, tea.Quit
	case "customer":
		result, err := aiHandleCustomer(m.configDir, route.args)
		if err != nil {
			return m, tea.Sequence(echo, tea.Println(aiErrorStyle.Render(err.Error())))
		}
		m.identity = aiIdentitySegment(m.configDir)
		return m, tea.Sequence(echo, tea.Println(tuiSuccessStyle.Render(result)))
	}
	return m, echo
}

// aiRunCommand dispatches one CLI command as a subprocess of this binary —
// the same argv the user would type after `dci `, isolated per AI-SPEC §7.4:
// package-level state can't leak between commands and an os.Exit in the child
// can't kill the session. DCI_NO_TUI keeps the child deterministic even if a
// PTY-ish stdio ever leaks through; the child shares this process's config
// dir, so auth and customer context apply unchanged.
func aiRunCommand(ctx context.Context, argv []string, started time.Time) tea.Cmd {
	return func() tea.Msg {
		command := exec.CommandContext(ctx, aiExecutablePath(), argv...)
		command.Env = append(os.Environ(), "DCI_NO_TUI=1")
		output, err := command.CombinedOutput()
		msg := aiCmdDoneMsg{argv: argv, output: string(output), elapsed: time.Since(started)}
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

// renderAIRunCard renders one finished dispatch for the scrollback: header,
// verbatim output, one-line footer. Pure so tests can cover every outcome.
func renderAIRunCard(msg aiCmdDoneMsg) string {
	var b strings.Builder
	b.WriteString(aiCardHeadStyle.Render("dci " + strings.Join(msg.argv, " ")))
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

func aiHelpText(catalogSize int) string {
	lines := []string{
		aiCardHeadStyle.Render("How this session works"),
		"  /<command>   run any dci command — same syntax as the shell (" + fmt.Sprint(catalogSize) + " available, type / to browse)",
		"  plain text   ask the AI (not available in this build yet)",
		"",
		aiCardHeadStyle.Render("Session commands"),
	}
	for _, verb := range aiSessionVerbs {
		lines = append(lines, fmt.Sprintf("  %-22s %s", verb.usage, verb.summary))
	}
	lines = append(lines, "",
		"  tab completes · ↑/↓ history or popup · esc clears · esc cancels a running command")
	return strings.Join(lines, "\n")
}

func (m aiModel) View() string {
	var b strings.Builder
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

func (m aiModel) statusLine() string {
	if m.running != nil {
		return m.spin.View() + aiEchoStyle.Render(fmt.Sprintf(" running /%s · %s · esc to cancel",
			strings.Join(m.running.argv, " "),
			time.Since(m.running.started).Round(time.Second)))
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
