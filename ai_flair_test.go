package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestAIShimmerKeepsWidthAndText(t *testing.T) {
	text := "Reticulating cost splines…"
	for _, dark := range []bool{true, false} {
		shimmered := aiShimmer(text, 3, dark)
		if got := lipgloss.Width(shimmered); got != lipgloss.Width(text) {
			t.Fatalf("dark=%v: shimmer width = %d, want %d", dark, got, lipgloss.Width(text))
		}
		if stripANSI(shimmered) != text {
			t.Fatalf("dark=%v: shimmer altered the text: %q", dark, stripANSI(shimmered))
		}
		if !strings.Contains(shimmered, "\x1b[") {
			t.Fatalf("dark=%v: shimmer produced no styling", dark)
		}
	}
	// The phase moves the gradient: two phases must differ.
	if aiShimmer(text, 0, true) == aiShimmer(text, 1, true) {
		t.Fatal("advancing the phase must move the gradient")
	}
}

func TestAIShimmerRowOnlyDecoratesIntactSubjects(t *testing.T) {
	row := "running /status · 2s · esc to cancel"
	decorated := aiShimmerRow(row, "/status", 0, true)
	if stripANSI(decorated) != row {
		t.Fatalf("decorated row text changed: %q", stripANSI(decorated))
	}
	if decorated == row {
		t.Fatal("an intact subject must be shimmered")
	}
	// A trimmed (absent) subject leaves the row untouched — recoloring a
	// fragment would style the wrong cells.
	if got := aiShimmerRow(row, "/status --all --pages 99", 0, true); got != row {
		t.Fatalf("a trimmed subject must not be decorated: %q", got)
	}
}

func TestAISpinnerStyleAges(t *testing.T) {
	young := aiSpinnerStyle(5 * time.Second).GetForeground()
	old := aiSpinnerStyle(aiSpinnerAgingAfter).GetForeground()
	if young == old {
		t.Fatal("a long wait must change the spinner tint")
	}
}

func TestAIQuipRotatesOnQuietTurns(t *testing.T) {
	m := aiTestModel(t)
	m.turnActive = true
	m.running = nil
	m.turnQuip = "Warming the cache…"
	m.quipAt = time.Now().Add(-aiQuipRotateEvery - time.Second)
	updated, _ := m.Update(m.spin.Tick())
	m = updated.(aiModel)
	if m.turnQuip == "" || time.Since(m.quipAt) > time.Minute {
		t.Fatalf("stale quip must be re-chosen: quip=%q at=%v", m.turnQuip, m.quipAt)
	}
	if !strings.Contains(stripANSI(m.statusLine()), m.turnQuip) {
		t.Fatalf("quiet turn status must show the quip: %q", stripANSI(m.statusLine()))
	}
}

func TestAIWindowTitleStates(t *testing.T) {
	m := aiTestModel(t)
	m.customerName = "Omni"
	if got := aiWindowTitle(&m); got != "dci ai — Omni" {
		t.Fatalf("idle title = %q", got)
	}
	m.turnActive = true
	if got := aiWindowTitle(&m); got != "dci ai — Omni ⋯" {
		t.Fatalf("busy title = %q", got)
	}
	m.turnActive = false
	m.turnDoneMark = true
	if got := aiWindowTitle(&m); got != "✓ dci ai — Omni" {
		t.Fatalf("done title = %q", got)
	}
	// Any keypress drops the checkmark.
	updated, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(aiModel)
	if m.turnDoneMark {
		t.Fatal("a keypress must clear the title checkmark")
	}
	if !strings.Contains(m.View().WindowTitle, "dci ai") {
		t.Fatal("the view must declare the window title")
	}
}

func TestAIDockProgressDeclaredWhileBusy(t *testing.T) {
	m := aiTestModel(t)
	if m.View().ProgressBar != nil {
		t.Fatal("idle session must not declare progress")
	}
	m.turnActive = true
	bar := m.View().ProgressBar
	if bar == nil || bar.State != tea.ProgressBarIndeterminate {
		t.Fatalf("busy session must declare indeterminate progress, got %+v", bar)
	}
}

func TestAILogoImageDrawsTheMark(t *testing.T) {
	img := aiLogoImage()
	bounds := img.Bounds()
	if bounds.Dy() != 224 || bounds.Dx() <= bounds.Dy() {
		t.Fatalf("logo bounds = %v, want 224 tall and wider than tall (the dot extends right)", bounds)
	}
	opaque, tinted := 0, 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			switch {
			case a == 0xFFFF:
				opaque++
			case a > 0:
				tinted++
			}
		}
	}
	if opaque < 5000 {
		t.Fatalf("logo has %d opaque pixels, want a solid mark", opaque)
	}
	if tinted == 0 {
		t.Fatal("logo has no partially covered pixels — anti-aliasing missing")
	}
	// The counter: the bowl's center must be transparent.
	if _, _, _, a := img.At(90, 148).RGBA(); a != 0 {
		t.Fatal("the bowl's counter must be transparent")
	}
}

func TestAIKittyPlaceholderTerminalGate(t *testing.T) {
	clear := func(t *testing.T) {
		for _, name := range []string{"KITTY_WINDOW_ID", "KITTY_INSTALLATION_DIR",
			"GHOSTTY_RESOURCES_DIR", "WEZTERM_EXECUTABLE", "WEZTERM_PANE", "TERM", "TERM_PROGRAM"} {
			t.Setenv(name, "")
		}
	}
	clear(t)
	// Terminals that answer the protocol probe but draw placeholder cells
	// blank must NOT get the upgrade — the mark would vanish.
	for _, program := range []string{"iTerm.app", "WarpTerminal", "Apple_Terminal", ""} {
		t.Setenv("TERM_PROGRAM", program)
		if aiKittyPlaceholderTerminal() {
			t.Fatalf("TERM_PROGRAM=%q must not pass the placeholder gate", program)
		}
	}
	clear(t)
	t.Setenv("GHOSTTY_RESOURCES_DIR", "/opt/ghostty")
	if !aiKittyPlaceholderTerminal() {
		t.Fatal("Ghostty must pass the placeholder gate")
	}
	clear(t)
	t.Setenv("TERM", "xterm-kitty")
	if !aiKittyPlaceholderTerminal() {
		t.Fatal("Kitty must pass the placeholder gate")
	}
}

func TestAIBannerUsesKittyGridWhenResolved(t *testing.T) {
	m := aiTestModel(t)
	if !strings.Contains(aiTranscriptText(m), "███") {
		t.Fatal("without Kitty support the half-block mark stands")
	}
	m.logoGrid = "KITTY-GRID-PLACEHOLDERS"
	m.refreshBanner()
	banner := m.transcript[0]
	if !strings.Contains(banner, "KITTY-GRID-PLACEHOLDERS") {
		t.Fatalf("banner must embed the resolved grid: %q", banner)
	}
	if strings.Contains(banner, "███") {
		t.Fatal("the half-block mark must step aside for the raster")
	}
}

func TestAIRuleGradientKeepsWidth(t *testing.T) {
	for _, width := range []int{4, 24, 80} {
		rule := aiRule(width, "")
		if got := lipgloss.Width(rule); got != width {
			t.Fatalf("rule width = %d, want %d", got, width)
		}
	}
	hinted := aiRule(80, "↓ scrolled up — PgDn for latest")
	if got := lipgloss.Width(hinted); got != 80 {
		t.Fatalf("hinted rule width = %d, want 80", got)
	}
	if !strings.Contains(stripANSI(hinted), "scrolled up") {
		t.Fatal("hinted rule must keep its hint")
	}
}
