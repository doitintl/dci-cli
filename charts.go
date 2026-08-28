package main

// F6 (TUI-SPEC): inline charts for human mode — the list-budgets utilization
// bar column and the --chart period-totals graph rendered under report
// tables. Both are table-output-only by construction (they hook the table
// renderer), keeping machine formats and agent output byte-identical. Kept in
// a sibling file per the AGENTS.md chapter-split guidance.

import (
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
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

// pendingChartWidth defers the chart render until after restish has written
// the table to stdout. The table marshaler records the rendered table's
// width here (noteChartWidth) instead of printing the chart inline: printing
// from inside the marshaler put the chart ABOVE the table on the terminal —
// stderr flushed before the marshaled bytes reached stdout — so on a long
// report the chart scrolled far out of view and the reader landed on the
// table's tail. run() flushes it (flushPendingChart) once the command's
// output is on screen, so the chart sits under the table, next to the
// prompt. -1 means no table was rendered.
var pendingChartWidth = -1

func resetChartState() {
	chartSeries = nil
	pendingChartWidth = -1
}

// noteChartWidth records that a table of the given width was rendered, arming
// the post-command chart flush.
func noteChartWidth(tableWidth int) { pendingChartWidth = tableWidth }

// flushPendingChart renders the requested chart under the command's output.
// A no-op unless a table was rendered this invocation (matching the old
// inline behavior: no table, no chart and no note).
func flushPendingChart() {
	if pendingChartWidth < 0 {
		return
	}
	width := pendingChartWidth
	pendingChartWidth = -1
	maybeRenderChart(width)
}

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
	if !chartRequested() || len(periods) == 0 {
		return
	}
	// Every mode but the treemap draws a time axis, so a single period has
	// nothing to chart; the treemap draws shares of a total and charts fine.
	if len(periods) < 2 && viper.GetString("chart-mode") != "treemap" {
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
	mode := viper.GetString("chart-mode")
	if series == nil || len(series.values) == 0 {
		if mode == "treemap" {
			fmt.Fprintln(os.Stderr, "note: --chart=treemap needs a report result with group and metric columns; no chart rendered")
		} else {
			fmt.Fprintln(os.Stderr, "note: --chart needs a report result with at least two time periods (the pivot view); no chart rendered")
		}
		return
	}
	// The treemap dispatches before the two-period gate: it draws shares of a
	// total, not a time axis, so a single-period (or time-less) result charts.
	if mode == "treemap" && treemapChartRenderable(series) {
		fmt.Fprintln(tuiStyledStderr(), "\n"+renderTreemapChart(series, chartRenderWidth(tableWidth)))
		return
	}
	if len(series.values) < 2 {
		if mode == "treemap" {
			fmt.Fprintln(os.Stderr, "note: --chart=treemap needs a color terminal and at least two groups; no chart rendered")
		} else {
			fmt.Fprintln(os.Stderr, "note: --chart needs a report result with at least two time periods (the pivot view); no chart rendered")
		}
		return
	}
	// Chart lines occupy the screen like table lines do, so they count
	// toward the scroll-overflow hint (output_order.go).
	printChart := func(w io.Writer, rendered string) {
		noteRenderedText(rendered)
		fmt.Fprintln(w, rendered)
	}
	// The styled writer owns color degradation for every styled mode:
	// lipgloss v2 styles emit truecolor as-is, and 256/16-color terminals
	// need the writer to downsample it (tui.go). Modes whose gate fails
	// (colorless terminal, too few groups) fall back to the line of period
	// totals rather than erroring.
	switch mode {
	case "stacked":
		if stackedChartRenderable(series) {
			printChart(tuiStyledStderr(), "\n"+renderStackedChart(series, chartRenderWidth(tableWidth)))
			return
		}
	case "sparkline":
		printChart(tuiStyledStderr(), "\n"+renderSparklineChart(series, chartRenderWidth(tableWidth)))
		return
	case "heatmap":
		if heatmapChartRenderable(series) {
			printChart(tuiStyledStderr(), "\n"+renderHeatmapChart(series, chartRenderWidth(tableWidth)))
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
	printChart(os.Stderr, "\n"+graph)
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
	sums, grand := chartGroupSums(series)
	lines = append(lines, chartLegend(series, palette, sums, grand)...)
	return strings.Join(lines, "\n")
}

// chartGroupSums returns each group's cross-period sum (index-parallel with
// series.groups) and the grand total across all groups.
func chartGroupSums(series *chartSeriesData) ([]float64, float64) {
	grand := 0.0
	sums := make([]float64, len(series.groups))
	for j, group := range series.groups {
		for _, v := range group.values {
			sums[j] += v
		}
		grand += sums[j]
	}
	return sums, grand
}

// chartLegend renders one swatch + label + share line per group. Shares come
// from the true sums, so segments a chart cannot draw (negative cells, rects
// too small for a label) still report their exact share here.
func chartLegend(series *chartSeriesData, palette []lipgloss.Style, sums []float64, grand float64) []string {
	lines := make([]string, 0, len(series.groups))
	for j, group := range series.groups {
		share := ""
		if grand != 0 {
			share = fmt.Sprintf("  %.0f%%", sums[j]/grand*100)
		}
		lines = append(lines, palette[j%len(palette)].Render("█")+" "+chartGroupLabel(group.name)+share)
	}
	return lines
}

// chartGroupLabel bounds a legend entry to one readable line.
func chartGroupLabel(name string) string {
	if runes := []rune(name); len(runes) > 48 {
		return string(runes[:47]) + "…"
	}
	return name
}

// treemapChartRenderable gates the treemap on what makes it legible: at least
// two groups whose totals are positive (rect areas are shares of positive
// spend — negatives cannot occupy area) and a terminal where color renders —
// rects are told apart by background color alone. table-color covers the
// policy gates; the profile check covers terminals lipgloss would strip
// anyway, same as the stacked view.
func treemapChartRenderable(series *chartSeriesData) bool {
	if !viper.GetBool("table-color") || !chartColorCapable() {
		return false
	}
	sums, _ := chartGroupSums(series)
	positive := 0
	for _, sum := range sums {
		if sum > 0 {
			positive++
		}
	}
	return positive >= 2
}

// treemapChartHeight matches the stacked chart's canvas so the two heavy
// views take the same vertical room under the table.
const treemapChartHeight = 12

// renderTreemapChart draws each group's share of the total as a proportional
// rectangle — the console's treemap renderer, in terminal cells. Groups with
// non-positive totals (credit-dominated) cannot occupy area and are left to
// the legend, whose shares come from the true sums. Labels render inside
// rects that fit them; the legend under the caption always carries the full
// names and exact shares.
func renderTreemapChart(series *chartSeriesData, width int) string {
	palette := chartThemePalette()
	sums, grand := chartGroupSums(series)

	// Groups arrive ranked largest-first from the pivot; the layout depends
	// on that order to keep big rects together (and re-sorts defensively for
	// series stashed by other paths).
	items := make([]int, 0, len(series.groups))
	for j := range series.groups {
		if sums[j] > 0 {
			items = append(items, j)
		}
	}
	sort.SliceStable(items, func(a, b int) bool { return sums[items[a]] > sums[items[b]] })
	weights := make([]float64, len(items))
	for i, j := range items {
		weights[i] = sums[j]
	}

	rects := make([]treemapRect, 0, len(items))
	treemapSplit(items, weights, 0, 0, width, treemapChartHeight, &rects)

	grid := make([][]int, treemapChartHeight)
	text := make([][]rune, treemapChartHeight)
	for y := range grid {
		grid[y] = make([]int, width)
		text[y] = make([]rune, width)
		for x := range grid[y] {
			grid[y][x] = -1
			text[y][x] = ' '
		}
	}
	for _, rect := range rects {
		for y := rect.y; y < rect.y+rect.h; y++ {
			for x := rect.x; x < rect.x+rect.w; x++ {
				grid[y][x] = rect.item
			}
		}
		share := ""
		if grand != 0 {
			share = fmt.Sprintf("%.0f%%", sums[rect.item]/grand*100)
		}
		label := treemapLabel(series.groups[rect.item].name, share, rect.w-2)
		if runes := []rune(label); len(runes) > 0 {
			copy(text[rect.y][rect.x+1:], runes)
			// A rect too narrow for "name share%" on one line can still
			// carry the share on its second row.
			if share != "" && !strings.HasSuffix(label, " "+share) && rect.h >= 2 && len([]rune(share)) <= rect.w-2 {
				copy(text[rect.y+1][rect.x+1:], []rune(share))
			}
		}
	}

	lines := make([]string, 0, treemapChartHeight+1+len(series.groups))
	for y := 0; y < treemapChartHeight; y++ {
		var line strings.Builder
		for x := 0; x < width; {
			item := grid[y][x]
			start := x
			for x < width && grid[y][x] == item {
				x++
			}
			segment := string(text[y][start:x])
			if item < 0 {
				line.WriteString(segment)
				continue
			}
			background := palette[item%len(palette)].GetForeground()
			line.WriteString(lipgloss.NewStyle().Background(background).Foreground(treemapTextColor(background)).Render(segment))
		}
		lines = append(lines, line.String())
	}
	lines = append(lines, treemapChartCaption(series))
	lines = append(lines, chartLegend(series, palette, sums, grand)...)
	return strings.Join(lines, "\n")
}

// treemapRect is one group's tile in cell coordinates.
type treemapRect struct {
	item       int // index into series.groups
	x, y, w, h int
}

// treemapSplit tiles the rectangle with one rect per item, area proportional
// to weight: the item list is split into two runs of roughly equal weight,
// the rectangle is split along its visually longer axis (a terminal cell is
// about twice as tall as wide, so height counts double), and both halves
// recurse. Weights are index-parallel with items and expected largest-first;
// every rect keeps at least one cell in the split dimension.
func treemapSplit(items []int, weights []float64, x, y, w, h int, out *[]treemapRect) {
	if len(items) == 0 || w < 1 || h < 1 {
		return
	}
	if len(items) == 1 {
		*out = append(*out, treemapRect{item: items[0], x: x, y: y, w: w, h: h})
		return
	}
	total := 0.0
	for _, weight := range weights {
		total += weight
	}
	splitAt, acc := 1, weights[0]
	for splitAt < len(items)-1 && acc+weights[splitAt] <= total/2 {
		acc += weights[splitAt]
		splitAt++
	}
	fraction := 0.5
	if total > 0 {
		fraction = acc / total
	}
	// A cell renders ~1:2 (width:height), so compare w against 2h to split
	// across the visually longer axis; a dimension of one cell cannot split.
	if (w >= 2*h && w > 1) || h == 1 {
		left := int(float64(w)*fraction + 0.5)
		left = min(max(left, 1), w-1)
		treemapSplit(items[:splitAt], weights[:splitAt], x, y, left, h, out)
		treemapSplit(items[splitAt:], weights[splitAt:], x+left, y, w-left, h, out)
		return
	}
	top := int(float64(h)*fraction + 0.5)
	top = min(max(top, 1), h-1)
	treemapSplit(items[:splitAt], weights[:splitAt], x, y, w, top, out)
	treemapSplit(items[splitAt:], weights[splitAt:], x, y+top, w, h-top, out)
}

// treemapLabel fits "name share%" into width cells, dropping the share and
// truncating the name as the rect narrows. Empty when nothing legible fits.
func treemapLabel(name, share string, width int) string {
	if width < 3 {
		return ""
	}
	runes := []rune(name)
	if share != "" && len(runes)+1+len(share) <= width {
		return name + " " + share
	}
	if len(runes) > width {
		return string(runes[:width-1]) + "…"
	}
	return name
}

// treemapTextColor picks black or white for label text over a tile color, by
// the tile's relative luminance — the theme palettes span pale pastels to
// near-black, so neither constant reads on both.
func treemapTextColor(background color.Color) color.Color {
	r, g, b, _ := background.RGBA()
	luminance := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
	if luminance > 0.55*65535 {
		return lipgloss.Color("#000000")
	}
	return lipgloss.Color("#FFFFFF")
}

// treemapChartCaption mirrors chartCaption for the share-of-total view: the
// empty period key marks a result with no time dimension (the flat-rows
// path), which has no period range to report.
func treemapChartCaption(series *chartSeriesData) string {
	first, last := series.periods[0], series.periods[len(series.periods)-1]
	switch {
	case first == "":
		return series.metric + " by group"
	case first == last:
		return fmt.Sprintf("%s by group — %s", series.metric, first)
	default:
		return fmt.Sprintf("%s by group — %s → %s", series.metric, first, last)
	}
}

// setTreemapSeriesFromFlatRows stashes a treemap series for report results
// the pivot cannot reshape (no time dimension, so no period columns): the
// treemap draws group shares of a total, which flat group totals carry
// already. Other chart modes need a time axis and keep their "no chart"
// note. Ranking and the top-group fold mirror the pivot path: largest total
// first, smallest groups folded into "other" by setChartSeries.
func setTreemapSeriesFromFlatRows(rows []interface{}, schema []reportColumn) {
	if !chartRequested() || viper.GetString("chart-mode") != "treemap" || chartSeries != nil {
		return
	}
	_, groupIdx, metricIdx := classifyPivotColumns(schema)
	if len(groupIdx) == 0 || len(metricIdx) == 0 {
		return
	}
	metricColumn := metricIdx[0]
	totals := map[string]float64{}
	order := []string{}
	for _, raw := range rows {
		cells, ok := raw.([]interface{})
		if !ok {
			return
		}
		if metricColumn >= len(cells) {
			continue
		}
		n, ok := numericCell(cells[metricColumn])
		if !ok {
			continue
		}
		group := pivotGroup(cells, groupIdx)
		if _, seen := totals[group]; !seen {
			order = append(order, group)
		}
		totals[group] += n
	}
	if len(order) == 0 {
		return
	}
	sort.SliceStable(order, func(i, j int) bool { return totals[order[i]] > totals[order[j]] })
	grand := 0.0
	groups := make([]chartGroupSeries, 0, len(order))
	for _, name := range order {
		groups = append(groups, chartGroupSeries{name: name, values: []float64{totals[name]}})
		grand += totals[name]
	}
	setChartSeries(schema[metricColumn].Name, []string{""}, map[string]float64{"": grand}, groups)
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
