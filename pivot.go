package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/viper"
)

// pivotReportBody reshapes flat report rows (one row per group × time period)
// into a report-style pivot: groups as rows, time periods as columns, with a
// row total column and a per-period totals row — the way the DoiT console
// presents a report table.
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
	periodTotals := map[string]float64{}
	periodSet := map[string]bool{}
	groupOrder := []pivotKey{}
	grandTotal := 0.0
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
			key := pivotKey{group: group}
			if multiMetric {
				key.metric = schema[mi].Name
			}
			if values[key] == nil {
				values[key] = map[string]float64{}
				groupOrder = append(groupOrder, key)
			}
			values[key][period] += n
			rowTotals[key] += n
			periodTotals[period] += n
			grandTotal += n
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

	// Highest row total first, matching how report tables rank groups.
	sort.SliceStable(groupOrder, func(i, j int) bool {
		return rowTotals[groupOrder[i]] > rowTotals[groupOrder[j]]
	})

	groupHeader := pivotGroupHeader(groupIdx, schema)
	out := make([]interface{}, 0, len(groupOrder)+1)
	for _, key := range groupOrder {
		row := map[string]interface{}{groupHeader: key.group}
		if multiMetric {
			row["metric"] = key.metric
		}
		for _, p := range periods {
			row[p] = values[key][p]
		}
		row["total"] = rowTotals[key]
		out = append(out, row)
	}

	totals := map[string]interface{}{groupHeader: "TOTAL"}
	if multiMetric {
		totals["metric"] = ""
	}
	for _, p := range periods {
		totals[p] = periodTotals[p]
	}
	totals["total"] = grandTotal
	out = append(out, totals)

	// Give the renderer an explicit column order (group, periods, total) —
	// alphabetical ordering would sort the group column after the periods.
	if strings.TrimSpace(viper.GetString("table-columns")) == "" {
		order := []string{groupHeader}
		if multiMetric {
			order = append(order, "metric")
		}
		order = append(order, periods...)
		order = append(order, "total")
		viper.Set("table-columns", strings.Join(order, ","))
	}

	return out, true
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
	for _, name := range pivotTimeParts {
		i, ok := timeIdx[name]
		if !ok || i >= len(cells) || cells[i] == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%v", cells[i]))
	}
	return strings.Join(parts, "-")
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
