package main

// F6 (TUI-SPEC): inline charts for human mode — the list-budgets utilization
// bar column and the --chart period-totals graph rendered under report
// tables. Both are table-output-only by construction (they hook the table
// renderer), keeping machine formats and agent output byte-identical. Kept in
// a sibling file per the AGENTS.md chapter-split guidance.

import (
	"fmt"
	"os"
	"strings"

	"github.com/NimbleMarkets/ntcharts/barchart"
	"github.com/charmbracelet/lipgloss"
	"github.com/guptarohit/asciigraph"
	"github.com/muesli/termenv"
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
	if viper.GetString("chart-mode") == "stacked" && stackedChartRenderable(series) {
		fmt.Fprintln(os.Stderr, "\n"+renderStackedChart(series, chartRenderWidth(tableWidth)))
		return
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

// chartColorCapable reports whether lipgloss will emit color at all. A var so
// tests can force it: under go test the detected profile is always Ascii.
var chartColorCapable = func() bool {
	return lipgloss.ColorProfile() != termenv.Ascii
}

// chartPalette are the stacked segment styles, largest group first; the last
// entry is reserved for the folded "other" segment when present. The colors
// are the DoiT console's default report theme ("DoiT" in the console's
// preset-themes seed), with AdaptiveColor picking the theme's light or dark
// variant by terminal background — the same split the console makes.
// Terminals without truecolor degrade to the nearest supported color.
var chartPalette = []lipgloss.Style{
	lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#E4A3F5", Dark: "#E4A3F5"}),
	lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#461254", Dark: "#8D24A8"}),
	lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C8D148", Dark: "#D4DB70"}),
	lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#EE7798", Dark: "#EE7798"}),
	lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0090D6", Dark: "#1AB4FF"}),
	lipgloss.NewStyle().Foreground(lipgloss.Color("8")), // gray — "other"
}

// renderStackedChart draws the per-group stacked columns (one bar per time
// period) with a legend of group shares. Negative cells (credits) are clamped
// to zero — a stacked column cannot draw below its own baseline — which the
// legend's exact shares do not hide: shares are computed from the true sums.
func renderStackedChart(series *chartSeriesData, width int) string {
	const height = 12
	chart := barchart.New(width, height, barchart.WithNoAxis())
	bars := make([]barchart.BarData, len(series.periods))
	for i := range series.periods {
		segments := make([]barchart.BarValue, len(series.groups))
		for j, group := range series.groups {
			value := group.values[i]
			if value < 0 {
				value = 0
			}
			segments[j] = barchart.BarValue{Name: group.name, Value: value, Style: chartPalette[j%len(chartPalette)]}
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
		lines = append(lines, chartPalette[j%len(chartPalette)].Render("█")+" "+chartGroupLabel(group.name)+share)
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
		if ratio >= 0.9 {
			// Red accent applied by accentCell after width formatting —
			// embedding ANSI in the cell value would skew column widths.
			row["utilizationRisk"] = "accent-red"
		}
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
