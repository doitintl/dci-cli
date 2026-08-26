package main

// F6 (TUI-SPEC): inline charts for human mode — the list-budgets utilization
// bar column and the --chart period-totals graph rendered under report
// tables. Both are table-output-only by construction (they hook the table
// renderer), keeping machine formats and agent output byte-identical. Kept in
// a sibling file per the AGENTS.md chapter-split guidance.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/barchart"
	"github.com/charmbracelet/colorprofile"
	"github.com/guptarohit/asciigraph"
	"github.com/spf13/viper"
)

// chartGroupSeries is one pivot group's per-period values, in period order.
type chartGroupSeries struct {
	name   string
	values []float64
}

// chartSeriesData carries the pivot's per-period totals (and, for the stacked
// view, the top groups' series) from the response transform to the table
// renderer, which prints the chart under the table.
type chartSeriesData struct {
	metric  string
	periods []string
	values  []float64
	groups  []chartGroupSeries
}

var chartSeries *chartSeriesData

func resetChartState() { chartSeries = nil }

func chartRequested() bool { return viper.GetBool("chart-requested") }

// chartMaxGroups bounds the stacked chart's segment count: the palette runs
// out of distinguishable terminal colors long before the report runs out of
// groups, so the smallest groups fold into a gray "other" segment.
const chartMaxGroups = 5

// setChartSeries stashes the pivot's period totals and ranked group series
// for the requested chart. The pivot is the authority on which columns are
// time periods and how groups rank, so the chart consumes its output rather
// than re-deriving either from raw rows. Groups beyond chartMaxGroups fold
// into "other".
func setChartSeries(metric string, periods []string, totals map[string]float64, groups []chartGroupSeries) {
	if !chartRequested() || len(periods) < 2 {
		return
	}
	values := make([]float64, len(periods))
	for i, period := range periods {
		values[i] = totals[period]
	}
	kept := groups
	if len(groups) > chartMaxGroups {
		kept = make([]chartGroupSeries, 0, chartMaxGroups+1)
		kept = append(kept, groups[:chartMaxGroups]...)
		other := chartGroupSeries{
			name:   fmt.Sprintf("other (%d groups)", len(groups)-chartMaxGroups),
			values: make([]float64, len(periods)),
		}
		for _, group := range groups[chartMaxGroups:] {
			for i, v := range group.values {
				other.values[i] += v
			}
		}
		kept = append(kept, other)
	}
	chartSeries = &chartSeriesData{metric: metric, periods: periods, values: values, groups: kept}
}

// maybeRenderChart prints the requested chart to stderr under the table, or
// a one-line note when the rendered response had no chartable time series.
// tableWidth is the rendered table's width so the chart lines up with it
// (0 when unknown — the chart falls back to a terminal-derived width).
// Chatter-free in agent mode by the CLI's decoration contract.
func maybeRenderChart(tableWidth int) {
	if !chartRequested() || agentMode {
		return
	}
	series := chartSeries
	chartSeries = nil
	if series == nil || len(series.values) < 2 {
		fmt.Fprintln(os.Stderr, "note: --chart needs a report result with at least two time periods (the pivot view); no chart rendered")
		return
	}
	// The styled writer owns color degradation for every styled mode:
	// lipgloss v2 styles emit truecolor as-is, and 256/16-color terminals
	// need the writer to downsample it (tui.go). Modes whose gate fails
	// (colorless terminal, too few groups) fall back to the line of period
	// totals rather than erroring.
	switch viper.GetString("chart-mode") {
	case "stacked":
		if stackedChartRenderable(series) {
			fmt.Fprintln(tuiStyledStderr(), "\n"+renderStackedChart(series, chartRenderWidth(tableWidth)))
			return
		}
	case "sparkline":
		fmt.Fprintln(tuiStyledStderr(), "\n"+renderSparklineChart(series, chartRenderWidth(tableWidth)))
		return
	case "heatmap":
		if heatmapChartRenderable(series) {
			fmt.Fprintln(tuiStyledStderr(), "\n"+renderHeatmapChart(series, chartRenderWidth(tableWidth)))
			return
		}
	}
	// asciigraph prepends a y-axis value margin, so the graph area is
	// narrowed to keep the total line length at the table width.
	graph := asciigraph.Plot(series.values,
		asciigraph.Height(10),
		asciigraph.Width(max(chartRenderWidth(tableWidth)-12, 20)),
		asciigraph.Caption(chartCaption(series)),
	)
	fmt.Fprintln(os.Stderr, "\n"+graph)
}

// renderSparklineChart draws the period totals as a one-line sparkline in
// the brand accent, caption underneath — the lightest chart, for a glance at
// the shape. Negative totals (credit-dominated periods) clamp to the
// baseline. Wider series than the width keep the most recent periods.
func renderSparklineChart(series *chartSeriesData, width int) string {
	values, periods := series.values, series.periods
	if len(values) > width {
		values = values[len(values)-width:]
		periods = periods[len(periods)-width:]
	}
	peak := 0.0
	for _, value := range values {
		if value > peak {
			peak = value
		}
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	var bar strings.Builder
	for _, value := range values {
		index := 0
		if peak > 0 && value > 0 {
			index = int(value/peak*float64(len(levels)-1) + 0.5)
		}
		bar.WriteRune(levels[index])
	}
	spark := lipgloss.NewStyle().Foreground(lipgloss.Color(aiBrandHex)).Render(bar.String())
	return spark + "\n" + chartCaption(&chartSeriesData{metric: series.metric, periods: periods, values: values})
}

// heatmapChartRenderable gates the heatmap on what makes it legible: at
// least two group rows (one row is just a sparkline) and a terminal where
// color renders — intensity is told by color alone.
func heatmapChartRenderable(series *chartSeriesData) bool {
	return len(series.groups) >= 2 && viper.GetBool("table-color") && chartColorCapable()
}

// heatmapLabelWidth bounds the group labels on heatmap rows.
const heatmapLabelWidth = 24

// renderHeatmapChart draws one row per group and one cell per period,
// colored by the value's share of the grid maximum — the console's heatmap
// reports, in terminal cells. The intensity ramp runs from near the terminal
// background to the brand accent, picked per background so "hot" reads hot
// on light and dark alike. Negative cells (credits) render as the low end.
func renderHeatmapChart(series *chartSeriesData, width int) string {
	low, high := "#FFE3EC", "#3A0714"
	if chartDarkBackground() {
		low = "#3A0714"
		high = "#FF7295"
	}
	ramp := lipgloss.Blend1D(8, lipgloss.Color(low), lipgloss.Color(high))

	peak := 0.0
	for _, group := range series.groups {
		for _, value := range group.values {
			if value > peak {
				peak = value
			}
		}
	}
	cellWidth := 1
	if columns := len(series.periods); columns > 0 {
		if fit := (width - heatmapLabelWidth - 1) / columns; fit > cellWidth {
			cellWidth = fit
		}
		if cellWidth > 3 {
			cellWidth = 3
		}
	}

	lines := make([]string, 0, len(series.groups)+2)
	for _, group := range series.groups {
		var row strings.Builder
		row.WriteString(padHeatmapLabel(group.name))
		row.WriteString(" ")
		for _, value := range group.values {
			index := 0
			if peak > 0 && value > 0 {
				index = int(value/peak*float64(len(ramp)-1) + 0.5)
			}
			row.WriteString(lipgloss.NewStyle().Foreground(ramp[index]).Render(strings.Repeat("█", cellWidth)))
		}
		lines = append(lines, row.String())
	}
	var scale strings.Builder
	for _, tone := range ramp {
		scale.WriteString(lipgloss.NewStyle().Foreground(tone).Render("█"))
	}
	lines = append(lines, "low "+scale.String()+" high — "+chartCaption(series))
	return strings.Join(lines, "\n")
}

// padHeatmapLabel bounds a group label to the heatmap's label column.
func padHeatmapLabel(name string) string {
	runes := []rune(name)
	if len(runes) > heatmapLabelWidth {
		return string(runes[:heatmapLabelWidth-1]) + "…"
	}
	return string(runes) + strings.Repeat(" ", heatmapLabelWidth-len(runes))
}

// chartRenderWidth aligns the chart with the rendered table when its width is
// known; otherwise it derives a width from the terminal.
func chartRenderWidth(tableWidth int) int {
	if tableWidth > 20 {
		return tableWidth
	}
	width := detectTerminalWidth(0) - 12
	if width > 90 {
		width = 90
	}
	if width < 20 {
		width = 20
	}
	return width
}

func chartCaption(series *chartSeriesData) string {
	return fmt.Sprintf("%s by period — %s → %s",
		series.metric, series.periods[0], series.periods[len(series.periods)-1])
}

// stackedChartRenderable gates the stacked view on what makes it legible:
// at least two group segments to stack, and a terminal where color renders —
// the segments are told apart by color alone, so a colorless stacked bar
// falls back to the line of period totals. table-color covers the policy
// gates (NO_COLOR, agent mode, piped stdout); the profile check covers
// terminals lipgloss would strip anyway (unset or unknown TERM).
func stackedChartRenderable(series *chartSeriesData) bool {
	return len(series.groups) >= 2 && viper.GetBool("table-color") && chartColorCapable()
}

// chartColorCapable reports whether the chart's stderr stream renders color
// at all. A var so tests can force it: under go test the detected profile is
// always NoTTY.
var chartColorCapable = func() bool {
	return colorprofile.Detect(os.Stderr, os.Environ()) >= colorprofile.ANSI
}

// chartDarkBackground resolves the terminal background once, lazily, against
// stderr — where charts render. lipgloss v2 made background detection
// explicit (v1 probed implicitly on first use); on any failure (piped stderr,
// exotic terminal) it defaults to dark, matching upstream.
var chartDarkBackground = sync.OnceValue(func() bool {
	return lipgloss.HasDarkBackground(os.Stdin, os.Stderr)
})

// chartAdaptiveColor is a theme color with light- and dark-background
// variants — lipgloss v1's AdaptiveColor, rebuilt on v2's explicit detection.
// The variant is picked lazily at render time, so building a palette never
// queries the terminal.
type chartAdaptiveColor struct {
	light, dark string
}

func (c chartAdaptiveColor) RGBA() (r, g, b, a uint32) {
	if chartDarkBackground() {
		return lipgloss.Color(c.dark).RGBA()
	}
	return lipgloss.Color(c.light).RGBA()
}

// chartThemeColors is a report color theme's series palette: parallel arrays
// for light and dark terminal backgrounds, index = series rank.
type chartThemeColors struct {
	light []string
	dark  []string
}

// presetChartThemes are the DoiT console's built-in report themes, copied
// from the console's preset-themes seed (omni: server/services/scheduled-tasks/
// scripts/cloud-analytics/definitions/preset-themes.json). The API's reserved
// themeId "default" maps to "doit"; the other presets are keyed by their seed
// ids in case the active-theme endpoint names one directly.
var presetChartThemes = map[string]chartThemeColors{
	"doit": {
		light: []string{"#E4A3F5", "#461254", "#C8D148", "#EE7798", "#0090D6", "#94E1EB", "#997366", "#3B40B5", "#00CCB7", "#CC6D00", "#FFDE4C", "#931310"},
		dark:  []string{"#E4A3F5", "#8D24A8", "#D4DB70", "#EE7798", "#1AB4FF", "#B2E9F0", "#A97E6F", "#5C62FF", "#00E5CE", "#FFA533", "#FFF6CC", "#EB4B47"},
	},
	"soft-focus": {
		light: []string{"#4868B8", "#C88040", "#7858A8", "#388878", "#A89038", "#C46070", "#5878A0", "#904878", "#3090B0", "#B86848", "#7068A8", "#508850"},
		dark:  []string{"#6888E0", "#E8A060", "#9878D0", "#58B0A0", "#C8B050", "#E08090", "#7898C0", "#B06898", "#50B0D0", "#D88868", "#9088C8", "#70A870"},
	},
	"vivid-edge": {
		light: []string{"#1A6ED8", "#B07808", "#A82870", "#0C7E96", "#D86828", "#6840C0", "#D84050", "#3868A8", "#188860", "#9838A8", "#C87830", "#1A7468"},
		dark:  []string{"#4090F0", "#E8A820", "#D858A0", "#18A4BE", "#F08848", "#8E68E8", "#F06070", "#5888CC", "#30AC80", "#BE58D0", "#E89850", "#2C9C8E"},
	},
	"ocean-night": {
		light: []string{"#1B4D8A", "#E86325", "#087E90", "#8A32A2", "#D42660", "#1478A8", "#9A7E08", "#24725A", "#D94040", "#9448CC", "#525A62", "#14785E"},
		dark:  []string{"#5A8EC7", "#F0833E", "#0B9BB2", "#B568CE", "#E04478", "#2A96C8", "#B59A18", "#3D9B72", "#E06B6B", "#A96EDB", "#9BA0A6", "#2E9E84"},
	},
}

// chartThemePalette resolves the stacked segment styles, largest group first;
// the final gray entry is reserved for the folded "other" segment. Colors
// come from the user's active report theme (fetched from the API when it is
// a custom theme), falling back to the DoiT preset. chartAdaptiveColor picks
// the theme's light or dark variant by terminal background — the same split
// the console makes — and the styled stderr writer degrades truecolor to the
// nearest supported color. A custom theme shorter than the segment count is
// padded from the DoiT preset.
func chartThemePalette() []lipgloss.Style {
	theme := activeChartTheme()
	doit := presetChartThemes["doit"]
	styles := make([]lipgloss.Style, 0, chartMaxGroups+1)
	for len(styles) < chartMaxGroups {
		i := len(styles)
		light, dark := doit.light[i], doit.dark[i]
		if i < len(theme.light) && i < len(theme.dark) {
			light, dark = theme.light[i], theme.dark[i]
		}
		styles = append(styles, lipgloss.NewStyle().Foreground(chartAdaptiveColor{light: light, dark: dark}))
	}
	return append(styles, lipgloss.NewStyle().Foreground(lipgloss.Color("8"))) // gray — "other"
}

// activeChartTheme returns the palette of the user's active report theme,
// falling back to the DoiT preset whenever the active theme cannot be
// resolved — the chart is decoration, so lookup failures stay silent.
func activeChartTheme() chartThemeColors {
	if theme, ok := fetchActiveChartTheme(); ok {
		return theme
	}
	return presetChartThemes["doit"]
}

// fetchActiveChartTheme resolves the active theme via the API: the
// active-theme setting names a theme id, and custom themes carry their own
// light/dark palettes. A var so tests can fake the API.
var fetchActiveChartTheme = fetchActiveChartThemeLive

func fetchActiveChartThemeLive() (chartThemeColors, bool) {
	var active struct {
		ThemeID string `json:"themeId"`
	}
	if !fetchSettingsJSON("/analytics/v1/settings/active-theme", &active) {
		return chartThemeColors{}, false
	}
	id := strings.TrimSpace(active.ThemeID)
	if id == "" || id == "default" {
		return chartThemeColors{}, false
	}
	if preset, ok := presetChartThemes[id]; ok {
		return preset, true
	}
	var custom struct {
		Colors struct {
			Light []string `json:"light"`
			Dark  []string `json:"dark"`
		} `json:"colors"`
	}
	if !fetchSettingsJSON("/analytics/v1/settings/themes/"+url.PathEscape(id), &custom) {
		return chartThemeColors{}, false
	}
	if len(custom.Colors.Light) == 0 || len(custom.Colors.Dark) == 0 {
		return chartThemeColors{}, false
	}
	return chartThemeColors{light: custom.Colors.Light, dark: custom.Colors.Dark}, true
}

// fetchSettingsJSON is a best-effort authenticated GET of a small settings
// payload, mirroring the resolver's programmatic-call pattern (bearer token,
// 10s client, tenant context on both transports). false on any failure.
func fetchSettingsJSON(path string, out interface{}) bool {
	token := authenticationToken()
	if token == "" {
		return false
	}
	base, err := apiBase()
	if err != nil {
		return false
	}
	requestURL, err := url.Parse(base + path)
	if err != nil {
		return false
	}
	context := activeCustomerContext()
	if context != "" {
		query := requestURL.Query()
		query.Set("customerContext", context)
		requestURL.RawQuery = query.Encode()
	}
	request, err := http.NewRequest(http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", buildUserAgent(agentUAMode))
	if context != "" {
		request.Header.Set("X-Tenant-Id", context)
	}
	response, err := resolverHTTPClient.Do(request)
	if err != nil {
		return false
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return false
	}
	return json.Unmarshal(body, out) == nil
}

// renderStackedChart draws the per-group stacked columns (one bar per time
// period) with a legend of group shares. Negative cells (credits) are omitted
// from the drawing — a stacked column cannot dip below its own baseline —
// which the legend's exact shares do not hide: shares are computed from the
// true sums.
func renderStackedChart(series *chartSeriesData, width int) string {
	const height = 12
	palette := chartThemePalette()
	chart := barchart.New(width, height, barchart.WithNoAxis())
	bars := make([]barchart.BarData, len(series.periods))
	for i := range series.periods {
		// ntcharts draws Values[0] at the bottom of the bar; the console
		// stacks the largest group on top, so segments go in reverse group
		// rank ("other" at the bottom, biggest spender on top). Zero and
		// negative cells are skipped: they draw nothing, and skipping them
		// keeps each remaining segment adjacent to its true visual neighbor.
		segments := make([]barchart.BarValue, 0, len(series.groups))
		for j := len(series.groups) - 1; j >= 0; j-- {
			value := series.groups[j].values[i]
			if value <= 0 {
				continue
			}
			segments = append(segments, barchart.BarValue{Name: series.groups[j].name, Value: value, Style: palette[j%len(palette)]})
		}
		// A segment boundary that falls mid-cell renders as a partial block
		// rune: its foreground is this segment, and the cell's remainder
		// shows the *background* — by default the terminal's, which reads as
		// a black gap in the bar. ntcharts' own compensation discards the
		// lipgloss copy it configures (Style setters return, not mutate), so
		// set each segment's background to the color stacked above it here.
		// The topmost segment keeps the default background: above it really
		// is the terminal.
		for k := 0; k < len(segments)-1; k++ {
			segments[k].Style = segments[k].Style.Background(segments[k+1].Style.GetForeground())
		}
		bars[i] = barchart.BarData{Values: segments}
	}
	chart.PushAll(bars)
	chart.Draw()

	lines := []string{strings.TrimRight(chart.View(), "\n"), chartCaption(series)}
	grand := 0.0
	sums := make([]float64, len(series.groups))
	for j, group := range series.groups {
		for _, v := range group.values {
			sums[j] += v
		}
		grand += sums[j]
	}
	for j, group := range series.groups {
		share := ""
		if grand != 0 {
			share = fmt.Sprintf("  %.0f%%", sums[j]/grand*100)
		}
		lines = append(lines, palette[j%len(palette)].Render("█")+" "+chartGroupLabel(group.name)+share)
	}
	return strings.Join(lines, "\n")
}

// chartGroupLabel bounds a legend entry to one readable line.
func chartGroupLabel(name string) string {
	if runes := []rune(name); len(runes) > 48 {
		return string(runes[:47]) + "…"
	}
	return name
}

// augmentTableViewColumns derives table-only columns (TUI-SPEC F6.1).
// list-budgets gains a utilization bar computed from the curated view's
// amount and spend-to-date columns. Availability is detected from the rows
// before the column is added, so a listing without the fields renders
// exactly as before.
func augmentTableViewColumns(rows []map[string]interface{}, keys []string) []string {
	if invokedCommandName != "list-budgets" {
		return keys
	}
	const amountKey, spendKey, utilizationKey = "amount", "spend to date", "utilization"
	available := false
	for _, row := range rows {
		if _, ok := numericCell(row[amountKey]); !ok {
			continue
		}
		if _, ok := numericCell(row[spendKey]); !ok {
			continue
		}
		available = true
		break
	}
	if !available {
		return keys
	}
	for _, row := range rows {
		amount, okAmount := numericCell(row[amountKey])
		spend, okSpend := numericCell(row[spendKey])
		if !okAmount || !okSpend || amount <= 0 {
			continue
		}
		ratio := spend / amount
		row[utilizationKey] = utilizationCell(ratio)
		// The band flag drives accentCell's colored bar after width
		// formatting — embedding ANSI in the cell value would skew column
		// widths. Every row gets a band; red keeps the 90% risk threshold.
		band := "green"
		switch {
		case ratio >= 0.9:
			band = "red"
		case ratio >= 0.7:
			band = "amber"
		}
		row["utilizationRisk"] = "utilization-" + band
	}
	if strings.TrimSpace(viper.GetString("table-accent-column")) == "" {
		viper.Set("table-accent-column", utilizationKey)
		viper.Set("table-accent-flag-key", "utilizationRisk")
	}
	out := make([]string, 0, len(keys))
	inserted := false
	for _, key := range keys {
		out = append(out, key)
		if key == spendKey {
			out = append(out, utilizationKey)
			inserted = true
		}
	}
	if !inserted {
		out = append(out, utilizationKey)
	}
	return out
}

// utilizationCell renders a ten-cell magnitude bar: ▓▓▓▓▓▓░░░░ 63%.
func utilizationCell(ratio float64) string {
	if ratio < 0 {
		ratio = 0
	}
	percent := int(ratio*100 + 0.5)
	filled := int(ratio*10 + 0.5)
	if filled > 10 {
		filled = 10
	}
	return fmt.Sprintf("%s %d%%", strings.Repeat("▓", filled)+strings.Repeat("░", 10-filled), percent)
}
