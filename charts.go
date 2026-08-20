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

	"github.com/guptarohit/asciigraph"
	"github.com/spf13/viper"
)

// chartSeriesData carries the pivot's per-period totals from the response
// transform to the table renderer, which prints the chart under the table.
type chartSeriesData struct {
	metric  string
	periods []string
	values  []float64
}

var chartSeries *chartSeriesData

func resetChartState() { chartSeries = nil }

func chartRequested() bool { return viper.GetBool("chart-requested") }

// setChartSeries stashes the pivot's period totals for the requested chart.
// The pivot is the authority on which columns are time periods, so the chart
// consumes its totals rather than re-deriving time columns from raw rows.
func setChartSeries(metric string, periods []string, totals map[string]float64) {
	if !chartRequested() || len(periods) < 2 {
		return
	}
	values := make([]float64, len(periods))
	for i, period := range periods {
		values[i] = totals[period]
	}
	chartSeries = &chartSeriesData{metric: metric, periods: periods, values: values}
}

// maybeRenderChart prints the requested chart to stderr under the table, or
// a one-line note when the rendered response had no chartable time series.
// Chatter-free in agent mode by the CLI's decoration contract.
func maybeRenderChart() {
	if !chartRequested() || agentMode {
		return
	}
	series := chartSeries
	chartSeries = nil
	if series == nil || len(series.values) < 2 {
		fmt.Fprintln(os.Stderr, "note: --chart needs a report result with at least two time periods (the pivot view); no chart rendered")
		return
	}
	width := detectTerminalWidth(0) - 12
	if width > 90 {
		width = 90
	}
	if width < 20 {
		width = 20
	}
	graph := asciigraph.Plot(series.values,
		asciigraph.Height(10),
		asciigraph.Width(width),
		asciigraph.Caption(fmt.Sprintf("%s by period — %s → %s",
			series.metric, series.periods[0], series.periods[len(series.periods)-1])),
	)
	fmt.Fprintln(os.Stderr, "\n"+graph)
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
