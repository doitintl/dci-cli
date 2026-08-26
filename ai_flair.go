package main

// The `dci ai` session's decorative layer: the branded spinner, the
// shimmering status subject, the waiting quips, the aging tint on long
// waits, the gradient frame rules, and the OS-level state the frame
// declares (window title, dock progress). Pure presentation — every helper
// here is a function of session state, so the session logic in ai_tui.go
// stays testable without it. Kept in a sibling file per the AGENTS.md
// chapter-split guidance.

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// aiBrandHex is the DoiT accent — the single source for the logo, spinner,
// input cursor, shimmer, and rule gradients. lipgloss degrades it to the
// terminal's nearest color when truecolor is unavailable.
const aiBrandHex = "#FC3165"

// aiDoitSpinner is the mark as a spinner: the floating dot orbiting the
// "d". Braille dots on purpose — they are single-cell everywhere, where the
// geometric circles (U+25CF and friends) are ambiguous-width and render
// two cells wide in some terminals.
var aiDoitSpinner = spinner.Spinner{
	Frames: []string{"d⠁", "d⠈", "d⠐", "d⠠", "d⠄", "d⠂"},
	FPS:    time.Second / 8,
}

// aiSpinnerStyle tints the spinner by how long the wait has run: brand pink,
// then amber once a turn runs long — the "this one's chewing" signal. Zero
// elapsed (states without a start time) stays pink.
const aiSpinnerAgingAfter = 30 * time.Second

func aiSpinnerStyle(elapsed time.Duration) lipgloss.Style {
	if elapsed >= aiSpinnerAgingAfter {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(aiBrandHex))
}

// The shimmer ramps: brand pink through a highlight and back, one cell per
// step. The highlight is light on dark backgrounds and deep on light ones,
// so the moving band reads on both without washing out.
var (
	aiShimmerDark = lipgloss.Blend1D(12,
		lipgloss.Color(aiBrandHex), lipgloss.Color("#FFD6E2"), lipgloss.Color(aiBrandHex))
	aiShimmerLight = lipgloss.Blend1D(12,
		lipgloss.Color(aiBrandHex), lipgloss.Color("#5A0E24"), lipgloss.Color(aiBrandHex))
)

// aiShimmer paints text with the moving brand gradient; phase advances one
// cell per spinner tick, sliding the highlight along the text. The styled
// result keeps the plain text's cell width, but carries escape codes — never
// hand it to anything that slices runes (aiTrimTo), only to width-aware
// rendering.
func aiShimmer(text string, phase int, dark bool) string {
	ramp := aiShimmerDark
	if !dark {
		ramp = aiShimmerLight
	}
	if phase < 0 {
		phase = -phase
	}
	var b strings.Builder
	i := 0
	for _, r := range text {
		b.WriteString(lipgloss.NewStyle().Foreground(ramp[(i+phase)%len(ramp)]).Render(string(r)))
		i++
	}
	return b.String()
}

// aiShimmerRow decorates the subject inside an assembled status row. Only a
// subject that survived aiStatusRow's trimming intact is shimmered — a
// trimmed one was rune-sliced, and recoloring a fragment of it would slice
// through nothing today but keeps the invariant simple: shimmer whole
// subjects or none.
func aiShimmerRow(row, subject string, phase int, dark bool) string {
	if subject == "" || !strings.Contains(row, subject) {
		return row
	}
	return strings.Replace(row, subject, aiShimmer(subject, phase, dark), 1)
}

// aiQuipRotateEvery is how long one waiting quip stands before the next
// takes over on a still-quiet turn.
const aiQuipRotateEvery = 10 * time.Second

// aiRule colors: the frame rules open with a short brand-pink fade into the
// usual dim line — decoration at the frame's edges, not across it.
var aiRuleRamp = lipgloss.Blend1D(16,
	lipgloss.Color(aiBrandHex), lipgloss.Color("#6B6B6B"))

// aiRuleGradient renders the leading `cells` of a rule with the brand fade.
func aiRuleGradient(cells int) string {
	if cells > len(aiRuleRamp) {
		cells = len(aiRuleRamp)
	}
	var b strings.Builder
	for i := 0; i < cells; i++ {
		b.WriteString(lipgloss.NewStyle().Foreground(aiRuleRamp[i]).Render("─"))
	}
	return b.String()
}

// aiWindowTitle names the terminal tab after the session and its state:
// what the session is pointed at, a running marker while something is in
// flight, and a checkmark once a turn finishes — until the user comes back
// and presses a key.
func aiWindowTitle(m *aiModel) string {
	title := "dci ai"
	tenant := m.customerName
	if tenant == "" {
		tenant = m.effectiveCustomer()
	}
	if tenant != "" {
		title += " — " + tenant
	}
	switch {
	case m.running != nil || m.turnActive || m.fetchIntent != nil:
		return title + " ⋯"
	case m.turnDoneMark:
		return "✓ " + title
	}
	return title
}

// aiDockProgress declares the native OS progress state (dock, taskbar) for
// the frame: indeterminate while anything is in flight, cleared otherwise.
// Terminals without OSC 9;4 support ignore it.
func aiDockProgress(m *aiModel) *tea.ProgressBar {
	if m.running != nil || m.turnActive || m.fetchIntent != nil {
		return &tea.ProgressBar{State: tea.ProgressBarIndeterminate}
	}
	return nil
}
