package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

// pivotReportBody reshapes flat report rows (one row per group × time period)
// into a report-style pivot: groups as rows, time periods as columns, with a
// row total column and a per-period totals row — the way the DoiT console
// presents a report table. Period count never disables the pivot: the
// terminal-width fit keeps the group, the leading periods, and the
// fit-priority total/trend columns, reporting the rest through the
// hidden-columns hint.
func pivotReportBody(rows []interface{}, schema []reportColumn) (interface{}, bool) {
	if len(schema) == 0 || len(rows) == 0 {
		return nil, false
	}

	timeIdx, groupIdx, metricIdx := classifyPivotColumns(schema)
	if len(timeIdx) == 0 || len(metricIdx) == 0 {
		return nil, false
	}

	type pivotKey struct {
		group  string
		metric string
	}
	values := map[pivotKey]map[string]float64{}
	rowTotals := map[pivotKey]float64{}
	periodTotals := map[string]map[string]float64{}
	metricTotals := map[string]float64{}
	periodSet := map[string]bool{}
	groupOrder := []pivotKey{}
	multiMetric := len(metricIdx) > 1

	for _, raw := range rows {
		cells, ok := raw.([]interface{})
		if !ok {
			return nil, false
		}
		period := pivotPeriod(cells, timeIdx, schema)
		if period == "" {
			continue
		}
		group := pivotGroup(cells, groupIdx)
		for _, mi := range metricIdx {
			if mi >= len(cells) {
				continue
			}
			n, ok := numericCell(cells[mi])
			if !ok {
				continue
			}
			metric := schema[mi].Name
			key := pivotKey{group: group}
			if multiMetric {
				key.metric = metric
			}
			if values[key] == nil {
				values[key] = map[string]float64{}
				groupOrder = append(groupOrder, key)
			}
			values[key][period] += n
			rowTotals[key] += n
			if periodTotals[metric] == nil {
				periodTotals[metric] = map[string]float64{}
			}
			periodTotals[metric][period] += n
			metricTotals[metric] += n
			periodSet[period] = true
		}
	}

	if len(periodSet) == 0 {
		return nil, false
	}

	periods := make([]string, 0, len(periodSet))
	for p := range periodSet {
		periods = append(periods, p)
	}
	sort.Strings(periods)

	viper.Set("pivot-active", true)
	viper.Set("pivot-total-rows", len(metricIdx))

	// Drop groups whose every period cell is zero: the API emits a row for
	// every group in the report's scope, so a wide report is mostly dead
	// rows. Cell-level zero, not total-level — credits cancelling real spend
	// to a zero *sum* leave nonzero cells and must stay visible. The dropped
	// rows still contribute (nothing) to the totals row, and
	// --include-empty-rows restores them, same as the flat view's dropper.
	if !viper.GetBool("include-empty-rows") {
		kept := make([]pivotKey, 0, len(groupOrder))
		for _, key := range groupOrder {
			if allPeriodCellsZero(values[key], periods) {
				continue
			}
			kept = append(kept, key)
		}
		if dropped := len(groupOrder) - len(kept); dropped > 0 && len(kept) > 0 {
			groupOrder = kept
			if cli.Stderr != nil {
				_, _ = fmt.Fprintf(cli.Stderr, "note: %d all-zero rows hidden (--include-empty-rows to show)\n", dropped)
			}
		}
	}

	// Highest row total first, matching how report tables rank groups.
	sort.SliceStable(groupOrder, func(i, j int) bool {
		return rowTotals[groupOrder[i]] > rowTotals[groupOrder[j]]
	})

	groupHeader := pivotGroupHeader(groupIdx, schema)
	out := make([]interface{}, 0, len(groupOrder)+len(metricIdx))
	for _, key := range groupOrder {
		row := map[string]interface{}{groupHeader: key.group}
		if multiMetric {
			row["metric"] = key.metric
		}
		for _, p := range periods {
			row[p] = values[key][p]
		}
		row["total"] = rowTotals[key]
		row["trend"] = trendLabel(values[key][periods[0]], values[key][periods[len(periods)-1]])
		out = append(out, row)
	}

	for _, mi := range metricIdx {
		metric := schema[mi].Name
		totals := map[string]interface{}{groupHeader: "TOTAL"}
		if multiMetric {
			totals["metric"] = metric
		}
		for _, p := range periods {
			totals[p] = periodTotals[metric][p]
		}
		totals["total"] = metricTotals[metric]
		totals["trend"] = trendLabel(periodTotals[metric][periods[0]], periodTotals[metric][periods[len(periods)-1]])
		out = append(out, totals)
	}

	// The pivot already identified the time periods, their totals, and the
	// group ranking — hand them to the --chart renderer (a no-op unless the
	// flag was passed). Group series follow groupOrder, so the stacked chart
	// stacks largest-first and inherits the all-zero-row drop above; a
	// multi-metric pivot charts its first metric, same as the line view.
	if chartRequested() && len(metricIdx) > 0 {
		firstMetric := schema[metricIdx[0]].Name
		groups := make([]chartGroupSeries, 0, len(groupOrder))
		for _, key := range groupOrder {
			if multiMetric && key.metric != firstMetric {
				continue
			}
			series := make([]float64, len(periods))
			for i, p := range periods {
				series[i] = values[key][p]
			}
			groups = append(groups, chartGroupSeries{name: key.group, values: series})
		}
		setChartSeries(firstMetric, periods, periodTotals[firstMetric], groups)
	}

	// Give the renderer an explicit column order (group, periods, total) —
	// alphabetical ordering would sort the group column after the periods.
	// Marked as auto-set so the width fit still applies (unlike a user's -C,
	// this is not an explicit selection): a pivot over many periods keeps
	// the group, the leading periods, and the total, with the rest reported
	// through the hidden-columns hint.
	if strings.TrimSpace(viper.GetString("table-columns")) == "" {
		order := []string{groupHeader}
		if multiMetric {
			order = append(order, "metric")
		}
		order = append(order, periods...)
		order = append(order, "total", "trend")
		viper.Set("table-columns", strings.Join(order, ","))
		viper.Set("table-columns-auto", true)
	}

	// When the pivoted metric is monetary and the currency is known, the
	// period and total cells are money for the renderer.
	if requestCurrencyContext() != "" && allMetricsMoney(metricIdx, schema) {
		viper.Set("money-columns", strings.Join(append(append([]string{}, periods...), "total"), ","))
	}

	return out, true
}

// allPeriodCellsZero reports whether a pivot row is dead weight: every period
// cell exactly zero (absent periods count as zero). Kept deliberately strict —
// any nonzero cell, positive or negative, keeps the row.
func allPeriodCellsZero(cells map[string]float64, periods []string) bool {
	for _, period := range periods {
		if cells[period] != 0 {
			return false
		}
	}
	return true
}

// trendLabel summarizes first→last period movement per row — the textual
// counterpart of the heatmap: humans scan it, and agents (which never receive
// color) get the derived signal without recomputing it.
func trendLabel(first, last float64) string {
	if first == 0 {
		if last == 0 {
			return ""
		}
		return "new"
	}
	change := (last - first) / math.Abs(first) * 100
	if math.Abs(change) < 0.5 {
		return "flat"
	}
	return fmt.Sprintf("%+.0f%%", change)
}

func allMetricsMoney(metricIdx []int, schema []reportColumn) bool {
	for _, i := range metricIdx {
		if !moneyNamedColumn(schema[i].Name) {
			return false
		}
	}
	return len(metricIdx) > 0
}

// pivotTimeParts orders the recognized time dimensions from coarse to fine so
// period keys compose correctly (e.g. 2026-05-04).
var pivotTimeParts = []string{"year", "quarter", "month", "week", "day", "hour"}

func classifyPivotColumns(schema []reportColumn) (timeIdx map[string]int, groupIdx []int, metricIdx []int) {
	timeIdx = map[string]int{}
	timeNames := map[string]bool{}
	for _, part := range pivotTimeParts {
		timeNames[part] = true
	}
	for i, col := range schema {
		name := strings.ToLower(col.Name)
		switch {
		case timeNames[name]:
			timeIdx[name] = i
		case col.Type == "timestamp" || col.Type == "datetime":
			// Redundant with the year/month/day columns; not pivoted.
		case col.Type == "float" || col.Type == "number" || col.Type == "integer":
			metricIdx = append(metricIdx, i)
		default:
			groupIdx = append(groupIdx, i)
		}
	}
	return timeIdx, groupIdx, metricIdx
}

func pivotPeriod(cells []interface{}, timeIdx map[string]int, schema []reportColumn) string {
	parts := []string{}
	hourPart := ""
	for _, name := range pivotTimeParts {
		i, ok := timeIdx[name]
		if !ok || i >= len(cells) || cells[i] == nil {
			continue
		}
		value := fmt.Sprintf("%v", cells[i])
		if name == "hour" {
			// The API delivers hours as zero-padded "HH:MM" strings; a space
			// separator reads as a time, a dash would read as another date part.
			if n, ok := numericCell(cells[i]); ok {
				value = fmt.Sprintf("%02d:00", int(n))
			}
			hourPart = value
			continue
		}
		parts = append(parts, value)
	}
	period := strings.Join(parts, "-")
	if hourPart != "" {
		period += " " + hourPart
	}
	return period
}

func pivotGroup(cells []interface{}, groupIdx []int) string {
	if len(groupIdx) == 0 {
		return "all"
	}
	parts := make([]string, 0, len(groupIdx))
	for _, i := range groupIdx {
		if i >= len(cells) || cells[i] == nil {
			parts = append(parts, "(none)")
			continue
		}
		parts = append(parts, fmt.Sprintf("%v", cells[i]))
	}
	return strings.Join(parts, " / ")
}

func pivotGroupHeader(groupIdx []int, schema []reportColumn) string {
	if len(groupIdx) == 0 {
		return "group"
	}
	names := make([]string, 0, len(groupIdx))
	for _, i := range groupIdx {
		names = append(names, schema[i].Name)
	}
	return strings.Join(names, " / ")
}
