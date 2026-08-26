package main

// The `dci ai` session's decorative layer: the branded spinner, the
// shimmering status subject, the waiting quips, the aging tint on long
// waits, the gradient frame rules, and the OS-level state the frame
// declares (window title, dock progress). Pure presentation — every helper
// here is a function of session state, so the session logic in ai_tui.go
// stays testable without it. Kept in a sibling file per the AGENTS.md
// chapter-split guidance.

import (
	"image"
	"image/color"
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

// The banner's Kitty-graphics logo: the cell rectangle it occupies (matching
// the half-block fallback's footprint) and the image ID it registers under.
const (
	aiLogoKittyCols = 15
	aiLogoKittyRows = 7
	aiLogoKittyID   = 4443
)

// aiLogoImage draws the DoiT mark — the round bowl with its circular
// counter, the stem flush with the bowl's right edge, the detached dot at
// mid-height — as an anti-aliased raster in the brand accent on a
// transparent background, for terminals that render real pixels (the Kitty
// graphics banner). Same geometry as the half-block fallback, sixteen times
// the resolution. Built lazily: only Kitty-capable terminals pay for it.
func aiLogoImage() image.Image {
	const height = 224
	fH := float64(height)
	bowlX, bowlY, bowlR := 0.40*fH, 0.66*fH, 0.34*fH
	holeR := 0.165 * fH
	stemR := bowlX + bowlR
	stemL := stemR - 0.20*fH
	capY := 0.06*fH + (stemR-stemL)/2
	dotX, dotY, dotR := stemR+0.24*fH+dotRadiusPad*fH, 0.43*fH, 0.115*fH
	width := int(dotX + dotR + 0.03*fH)

	inside := func(x, y float64) bool {
		dxB, dyB := x-bowlX, y-bowlY
		distB := dxB*dxB + dyB*dyB
		if distB <= bowlR*bowlR && distB >= holeR*holeR {
			return true
		}
		if x >= stemL && x <= stemR && y >= capY && y <= bowlY {
			return true
		}
		dxC, dyC := x-(stemL+stemR)/2, y-capY
		if dxC*dxC+dyC*dyC <= (stemR-stemL)*(stemR-stemL)/4 { // rounded cap
			return true
		}
		dxD, dyD := x-dotX, y-dotY
		return dxD*dxD+dyD*dyD <= dotR*dotR
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for py := 0; py < height; py++ {
		for px := 0; px < width; px++ {
			hits := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					if inside(float64(px)+(float64(sx)+0.5)/4, float64(py)+(float64(sy)+0.5)/4) {
						hits++
					}
				}
			}
			if hits == 0 {
				continue
			}
			// Premultiplied alpha: the brand accent scaled by coverage.
			img.SetRGBA(px, py, color.RGBA{
				R: uint8(0xFC * hits / 16),
				G: uint8(0x31 * hits / 16),
				B: uint8(0x65 * hits / 16),
				A: uint8(0xFF * hits / 16),
			})
		}
	}
	return img
}

// dotRadiusPad nudges the dot outward so the raster's proportions match the
// half-block fallback, whose dot snaps to whole cells.
const dotRadiusPad = 0.06

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
