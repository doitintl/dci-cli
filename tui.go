package main

// Terminal UI layer for human interactive mode: the shared gate, prompt
// helpers, spinner, and styles behind the F1–F8 features in TUI-SPEC.md.
// Everything here is gated on tuiActive() — agent mode, piped output, CI,
// and TERM=dumb keep the plain deterministic behavior — and every prompt
// renders to stderr so stdout stays data-only. Kept in a sibling file per
// the AGENTS.md chapter-split guidance.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// tuiActive reports whether interactive TUI enhancements may render. A var so
// tests can force either mode. DCI_NO_TUI follows the existing negative-toggle
// convention (DCI_NO_RESOLVE, DCI_NO_UPDATE_CHECK).
var tuiActive = func() bool {
	if on, valid := parseBoolish(os.Getenv("DCI_NO_TUI")); valid && on {
		return false
	}
	return !agentMode && stdoutIsTTY() &&
		term.IsTerminal(int(os.Stdin.Fd())) &&
		os.Getenv("TERM") != "dumb"
}

var (
	tuiSuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	tuiDimStyle     = lipgloss.NewStyle().Faint(true)
	tuiDangerBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("1")).
			Padding(0, 1)
	tuiDangerTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	tuiNoticeBox   = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("3")).
			Padding(0, 1)
)

// tuiWidth is the render width for prompts: the terminal width, capped so
// wide terminals don't stretch labels, with a safe floor when detection fails
// (an unset width makes huh wrap titles into nothing).
func tuiWidth() int {
	width, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil || width <= 0 {
		width = 80
	}
	if width > 100 {
		width = 100
	}
	return width
}

// tuiForm wraps a single huh field in a form with the house settings: output
// on stderr, input from the terminal.
func tuiForm(field huh.Field) *huh.Form {
	return huh.NewForm(huh.NewGroup(field)).
		WithOutput(os.Stderr).
		WithInput(os.Stdin).
		WithWidth(tuiWidth()).
		WithShowHelp(true)
}

// tuiSelectEntry runs a filter-as-you-type select over name cache entries on
// stderr. The returned error is huh.ErrUserAborted on Esc/Ctrl-C.
func tuiSelectEntry(title string, entries []nameCacheEntry) (nameCacheEntry, error) {
	options := make([]huh.Option[int], len(entries))
	for i, entry := range entries {
		label := entry.Name
		if entry.ID != "" {
			label = fmt.Sprintf("%s  %s", entry.Name, tuiDimStyle.Render("("+entry.ID+")"))
		}
		options[i] = huh.NewOption(label, i)
	}
	selected := 0
	// Filtering(true) starts with the filter input active — type-to-filter
	// from the first keystroke — which replaces the title line, so the
	// context lives in the description (rendered in both states).
	field := huh.NewSelect[int]().
		Title(title).
		Description(title).
		Options(options...).
		Filtering(true).
		Height(14).
		Value(&selected)
	if err := tuiForm(field).Run(); err != nil {
		return nameCacheEntry{}, err
	}
	return entries[selected], nil
}

// tuiNameSelection is the interactive-terminal upgrade of the ambiguity
// prompt (F2). handled=false means the caller should fall back to the plain
// numbered prompt (gate off, or the renderer failed on an exotic terminal).
func tuiNameSelection(input, resource string, candidates []nameCacheEntry) (entry nameCacheEntry, err error, handled bool) {
	if !tuiActive() {
		return nameCacheEntry{}, nil, false
	}
	title := fmt.Sprintf("%q matches %d %ss — select one", input, len(candidates), resource)
	entry, selectErr := tuiSelectEntry(title, candidates)
	if selectErr != nil {
		if errors.Is(selectErr, huh.ErrUserAborted) {
			return nameCacheEntry{}, nameSelectionCancelledError(resource), true
		}
		return nameCacheEntry{}, nil, false
	}
	return entry, nil, true
}

// confirmDestructiveInteractively is the F3 prompt: a styled default-Cancel
// confirm shown instead of the --yes usage error when a human is present.
// Returning false keeps today's destructiveConfirmationError path. A var so
// tests can force either answer.
var confirmDestructiveInteractively = tuiConfirmDestructive

func tuiConfirmDestructive(commandName string) bool {
	if !tuiActive() {
		return false
	}
	lines := []string{tuiDangerTitle.Render(commandName)}
	if resolved := commandResolvedTarget(commandName); resolved != nil {
		resource := resolved.resource
		if resource == "" {
			resource = "resource"
		}
		lines = append(lines, fmt.Sprintf("%s%s: %s  (%s)",
			strings.ToUpper(resource[:1]), resource[1:], resolved.name, resolved.id))
	}
	lines = append(lines, "This action cannot be undone.")
	fmt.Fprintln(os.Stderr, tuiDangerBorder.Render(strings.Join(lines, "\n")))

	confirmed := false // default answer is Cancel
	field := huh.NewConfirm().
		Title("Proceed?").
		Affirmative("Proceed").
		Negative("Cancel").
		Value(&confirmed)
	if err := tuiForm(field).Run(); err != nil {
		return false // Esc/Ctrl-C/renderer failure all mean "not confirmed"
	}
	return confirmed
}

// startTUISpinner renders a single-line braille spinner on stderr and returns
// a stop function that clears the line. A no-op outside tuiActive().
func startTUISpinner(message string) func() {
	if !tuiActive() {
		return func() {}
	}
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(90 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			select {
			case <-stop:
				fmt.Fprintf(os.Stderr, "\r\033[2K")
				return
			case <-ticker.C:
				fmt.Fprintf(os.Stderr, "\r%c %s", frames[frame%len(frames)], message)
				frame++
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}
