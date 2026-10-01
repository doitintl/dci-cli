package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

func pivotSchema() []reportColumn {
	return []reportColumn{
		{Name: "service_description", Type: "string"},
		{Name: "year", Type: "string"},
		{Name: "month", Type: "string"},
		{Name: "cost", Type: "float"},
		{Name: "timestamp", Type: "timestamp"},
	}
}

func TestPivotTerminalOrderMirrorsGroupsKeepsTotalsLast(t *testing.T) {
	viper.Set("table-columns", "")
	viper.Set("rsh-output-format", "table")
	viper.Set("output-order", outputOrderTerminal)
	t.Cleanup(func() {
		viper.Set("table-columns", nil)
		viper.Set("rsh-output-format", nil)
		viper.Set("output-order", nil)
	})
	forceTUI(t, true)

	rows := []interface{}{
		[]interface{}{"svc-a", "2026", "06", 10.0, float64(1780272000)},
		[]interface{}{"svc-a", "2026", "07", 20.0, float64(1782864000)},
		[]interface{}{"svc-b", "2026", "06", 100.0, float64(1780272000)},
		[]interface{}{"svc-b", "2026", "07", 200.0, float64(1782864000)},
	}
	result, ok := pivotReportBody(rows, pivotSchema())
	if !ok {
		t.Fatal("pivot not applied")
	}
	pivoted := result.([]interface{})
	if got := pivoted[0].(map[string]interface{})["service_description"]; got != "svc-a" {
		t.Errorf("first group = %v, want svc-a (smallest first under terminal ordering)", got)
	}
	if got := pivoted[1].(map[string]interface{})["service_description"]; got != "svc-b" {
		t.Errorf("second group = %v, want svc-b (largest lands nearest the TOTAL row)", got)
	}
	if got := pivoted[2].(map[string]interface{})["service_description"]; got != "TOTAL" {
		t.Errorf("last row = %v, want TOTAL kept at the bottom", got)
	}
}

func TestPivotTerminalOrderChartSeriesStayLargestFirst(t *testing.T) {
	// The chart's top-chartMaxGroups fold consumes groupOrder largest-first
	// (OUTPUT-ORDER-SPEC §7.1); only the rendered rows mirror.
	viper.Set("table-columns", "")
	viper.Set("rsh-output-format", "table")
	viper.Set("output-order", outputOrderTerminal)
	viper.Set("chart-requested", true)
	t.Cleanup(func() {
		viper.Set("table-columns", nil)
		viper.Set("rsh-output-format", nil)
		viper.Set("output-order", nil)
		viper.Set("chart-requested", nil)
		resetChartState()
	})
	forceTUI(t, true)
	resetChartState()

	rows := []interface{}{
		[]interface{}{"svc-a", "2026", "06", 10.0, float64(1780272000)},
		[]interface{}{"svc-a", "2026", "07", 20.0, float64(1782864000)},
		[]interface{}{"svc-b", "2026", "06", 100.0, float64(1780272000)},
		[]interface{}{"svc-b", "2026", "07", 200.0, float64(1782864000)},
	}
	if _, ok := pivotReportBody(rows, pivotSchema()); !ok {
		t.Fatal("pivot not applied")
	}
	if chartSeries == nil || len(chartSeries.groups) != 2 {
		t.Fatalf("chart series = %+v, want two groups", chartSeries)
	}
	if chartSeries.groups[0].name != "svc-b" {
		t.Errorf("first chart group = %q, want svc-b (largest-first ranking preserved for the fold)", chartSeries.groups[0].name)
	}
}

func TestPivotReportBodyTimeAsColumns(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	rows := []interface{}{
		[]interface{}{"svc-a", "2026", "06", 10.0, float64(1780272000)},
		[]interface{}{"svc-a", "2026", "07", 20.0, float64(1782864000)},
		[]interface{}{"svc-b", "2026", "06", 100.0, float64(1780272000)},
		[]interface{}{"svc-b", "2026", "07", 200.0, float64(1782864000)},
	}
	result, ok := pivotReportBody(rows, pivotSchema())
	if !ok {
		t.Fatal("pivot not applied")
	}
	pivoted := result.([]interface{})
	if len(pivoted) != 3 {
		t.Fatalf("pivot rows = %d, want 2 groups + totals", len(pivoted))
	}

	first := pivoted[0].(map[string]interface{})
	if first["service_description"] != "svc-b" {
		t.Errorf("first group = %v, want svc-b (largest total first)", first["service_description"])
	}
	if first["2026-06"] != 100.0 || first["2026-07"] != 200.0 {
		t.Errorf("period cells = %v / %v, want 100 / 200", first["2026-06"], first["2026-07"])
	}
	if first["total"] != 300.0 {
		t.Errorf("row total = %v, want 300", first["total"])
	}

	totals := pivoted[2].(map[string]interface{})
	if totals["service_description"] != "TOTAL" {
		t.Errorf("last row = %v, want TOTAL", totals["service_description"])
	}
	if totals["total"] != 330.0 {
		t.Errorf("grand total = %v, want 330", totals["total"])
	}

	order := viper.GetString("table-columns")
	if !strings.HasPrefix(order, "service_description,2026-06,2026-07,total") {
		t.Errorf("column order = %q, want group first, periods, then total", order)
	}
}

func TestPivotReportBodyNullGroups(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	rows := []interface{}{
		[]interface{}{nil, "2026", "06", 5.0, float64(1780272000)},
	}
	result, ok := pivotReportBody(rows, pivotSchema())
	if !ok {
		t.Fatal("pivot not applied")
	}
	first := result.([]interface{})[0].(map[string]interface{})
	if first["service_description"] != "(none)" {
		t.Errorf("null group rendered as %v, want (none)", first["service_description"])
	}
}

func TestPivotReportBodyKeepsMetricTotalsSeparate(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	schema := []reportColumn{
		{Name: "service_description", Type: "string"},
		{Name: "year", Type: "string"},
		{Name: "month", Type: "string"},
		{Name: "cost", Type: "float"},
		{Name: "usage", Type: "float"},
	}
	rows := []interface{}{
		[]interface{}{"svc", "2026", "06", 10.0, 100.0},
		[]interface{}{"svc", "2026", "07", 20.0, 200.0},
	}
	result, ok := pivotReportBody(rows, schema)
	if !ok {
		t.Fatal("pivot not applied")
	}
	pivoted := result.([]interface{})
	if len(pivoted) != 4 {
		t.Fatalf("pivot rows = %d, want 2 metric rows + 2 totals", len(pivoted))
	}
	costTotal := pivoted[2].(map[string]interface{})
	usageTotal := pivoted[3].(map[string]interface{})
	if costTotal["metric"] != "cost" || costTotal["total"] != 30.0 {
		t.Errorf("cost total = %#v, want 30", costTotal)
	}
	if usageTotal["metric"] != "usage" || usageTotal["total"] != 300.0 {
		t.Errorf("usage total = %#v, want 300", usageTotal)
	}
}

func TestTrendLabel(t *testing.T) {
	cases := []struct {
		first, last float64
		want        string
	}{
		{239927, 345927, "+44%"},
		{83219, 53925, "-35%"},
		{100, 100.2, "flat"},
		{0, 500, "new"},
		{0, 0, ""},
		{-100, -50, "+50%"},
		{-100, -200, "-100%"},
	}
	for _, c := range cases {
		if got := trendLabel(c.first, c.last); got != c.want {
			t.Errorf("trendLabel(%v, %v) = %q, want %q", c.first, c.last, got, c.want)
		}
	}
}

func TestPivotRowsCarryTrend(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() {
		viper.Set("table-columns", nil)
		viper.Set("table-columns-auto", nil)
	})
	rows := []interface{}{
		[]interface{}{"svc-a", "2026", "06", 100.0, float64(1780272000)},
		[]interface{}{"svc-a", "2026", "07", 200.0, float64(1782864000)},
	}
	result, ok := pivotReportBody(rows, pivotSchema())
	if !ok {
		t.Fatal("pivot not applied")
	}
	first := result.([]interface{})[0].(map[string]interface{})
	if first["trend"] != "+100%" {
		t.Errorf("trend = %v, want +100%%", first["trend"])
	}
	if !strings.HasSuffix(viper.GetString("table-columns"), ",total,trend") {
		t.Errorf("column order = %q, want trend last", viper.GetString("table-columns"))
	}
}

func TestPivotAppliesOnManyPeriods(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() {
		viper.Set("table-columns", nil)
		viper.Set("table-columns-auto", nil)
	})

	rows := []interface{}{}
	for day := 1; day <= 30; day++ {
		rows = append(rows, []interface{}{"svc", "2026", "07", fmt.Sprintf("%02d", day), 1.0, float64(1780272000 + day*86400)})
	}
	schema := []reportColumn{
		{Name: "service_description", Type: "string"},
		{Name: "year", Type: "string"},
		{Name: "month", Type: "string"},
		{Name: "day", Type: "string"},
		{Name: "cost", Type: "float"},
		{Name: "timestamp", Type: "timestamp"},
	}
	// A month of daily periods pivots like any other report: the terminal
	// width fit trims overflow columns, so period count never flattens the view.
	result, ok := pivotReportBody(rows, schema)
	if !ok {
		t.Fatal("pivot refused on a month of daily periods")
	}
	first := result.([]interface{})[0].(map[string]interface{})
	if first["total"] != 30.0 {
		t.Errorf("row total = %v, want 30", first["total"])
	}
	if !viper.GetBool("table-columns-auto") {
		t.Error("pivot column order must stay fit-eligible (table-columns-auto)")
	}
}

func TestPivotPeriodHourly(t *testing.T) {
	schema := append(pivotSchema()[:3], reportColumn{Name: "day", Type: "string"}, reportColumn{Name: "hour", Type: "string"}, reportColumn{Name: "cost", Type: "float"})
	timeIdx, _, _ := classifyPivotColumns(schema)
	cells := []interface{}{"svc", "2026", "08", "09", "01:00", 5.0}
	if got := pivotPeriod(cells, timeIdx, schema); got != "2026-08-09 01:00" {
		t.Errorf("hourly period = %q, want 2026-08-09 01:00", got)
	}
}

func TestShouldPivotReportRowsDefaults(t *testing.T) {
	oldAgentMode := agentMode
	t.Cleanup(func() {
		agentMode = oldAgentMode
		for _, key := range []string{"pivot-rows", "flat-rows", "rsh-output-format", "table-columns"} {
			viper.Set(key, nil)
		}
	})
	reset := func(agent bool, output, columns string, pivot, flat bool) {
		agentMode = agent
		viper.Set("rsh-output-format", output)
		viper.Set("table-columns", columns)
		viper.Set("pivot-rows", pivot)
		viper.Set("flat-rows", flat)
	}

	reset(false, "table", "", false, false)
	if !shouldPivotReportRows() {
		t.Error("human table view should pivot by default")
	}
	reset(false, "table", "", false, true)
	if shouldPivotReportRows() {
		t.Error("--flat must disable the default pivot")
	}
	reset(false, "table", "service_description,2026-06,total", false, false)
	if !shouldPivotReportRows() {
		t.Error("-C column selection must keep the pivot (it names pivot columns; pivotReportBody validates them)")
	}
	reset(false, "json", "", false, false)
	if shouldPivotReportRows() {
		t.Error("machine formats must stay flat by default")
	}
	reset(true, "toon", "", false, false)
	if shouldPivotReportRows() {
		t.Error("agent mode must stay flat by default")
	}
	reset(true, "toon", "", true, false)
	if !shouldPivotReportRows() {
		t.Error("--pivot must force the pivot even in agent mode")
	}
}

func TestPivotReportBodySkipsNonReportShapes(t *testing.T) {
	if _, ok := pivotReportBody([]interface{}{"not-a-row"}, pivotSchema()); ok {
		t.Error("pivot applied to malformed rows")
	}
	if _, ok := pivotReportBody(nil, nil); ok {
		t.Error("pivot applied to empty input")
	}
	// No time columns → nothing to pivot.
	schema := []reportColumn{{Name: "service_description", Type: "string"}, {Name: "cost", Type: "float"}}
	if _, ok := pivotReportBody([]interface{}{[]interface{}{"svc", 1.0}}, schema); ok {
		t.Error("pivot applied without time columns")
	}
}

func TestPivotDropsAllZeroRows(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() {
		viper.Set("table-columns", nil)
		viper.Set("table-columns-auto", nil)
		viper.Set("include-empty-rows", nil)
	})
	oldStderr := cli.Stderr
	var stderr strings.Builder
	cli.Stderr = &stderr
	t.Cleanup(func() { cli.Stderr = oldStderr })

	rows := []interface{}{
		[]interface{}{"svc-live", "2026", "06", 10.0, float64(1780272000)},
		[]interface{}{"svc-live", "2026", "07", 20.0, float64(1782864000)},
		// Credits cancel to a zero total, but the cells are nonzero: keep.
		[]interface{}{"svc-credited", "2026", "06", 100.0, float64(1780272000)},
		[]interface{}{"svc-credited", "2026", "07", -100.0, float64(1782864000)},
		// Every cell zero: dead weight, dropped.
		[]interface{}{"svc-idle", "2026", "06", 0.0, float64(1780272000)},
		[]interface{}{"svc-idle", "2026", "07", 0.0, float64(1782864000)},
	}
	result, ok := pivotReportBody(rows, pivotSchema())
	if !ok {
		t.Fatal("pivot not applied")
	}
	groups := []string{}
	for _, item := range result.([]interface{}) {
		groups = append(groups, item.(map[string]interface{})["service_description"].(string))
	}
	if fmt.Sprint(groups) != "[svc-credited svc-live TOTAL]" && fmt.Sprint(groups) != "[svc-live svc-credited TOTAL]" {
		t.Fatalf("groups = %v, want svc-live and svc-credited kept, svc-idle dropped", groups)
	}
	if !strings.Contains(stderr.String(), "1 all-zero rows hidden (--include-empty-rows to show)") {
		t.Fatalf("stderr = %q, want the hidden-rows note", stderr.String())
	}
	// The totals row must still reflect the full data set.
	last := result.([]interface{})[2].(map[string]interface{})
	if last["service_description"] != "TOTAL" || last["total"] != 30.0 {
		t.Fatalf("totals row = %+v", last)
	}
}

func TestPivotKeepsAllZeroRowsOnFlagOrWhenAllZero(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() {
		viper.Set("table-columns", nil)
		viper.Set("table-columns-auto", nil)
		viper.Set("include-empty-rows", nil)
	})

	rows := []interface{}{
		[]interface{}{"svc-live", "2026", "06", 10.0, float64(1780272000)},
		[]interface{}{"svc-idle", "2026", "06", 0.0, float64(1780272000)},
	}
	viper.Set("include-empty-rows", true)
	result, ok := pivotReportBody(rows, pivotSchema())
	if !ok {
		t.Fatal("pivot not applied")
	}
	if got := len(result.([]interface{})); got != 3 {
		t.Fatalf("row count with --include-empty-rows = %d, want 3 (both groups + TOTAL)", got)
	}

	// An entirely zero report keeps its rows: an empty table with a zero
	// TOTAL row explains nothing.
	viper.Set("include-empty-rows", false)
	viper.Set("table-columns", "")
	allZero := []interface{}{
		[]interface{}{"svc-a", "2026", "06", 0.0, float64(1780272000)},
		[]interface{}{"svc-b", "2026", "06", 0.0, float64(1780272000)},
	}
	result, ok = pivotReportBody(allZero, pivotSchema())
	if !ok {
		t.Fatal("pivot not applied")
	}
	if got := len(result.([]interface{})); got != 3 {
		t.Fatalf("row count when every group is zero = %d, want 3 (nothing dropped)", got)
	}
}

// pivotSelectionRows spans three months so a -C selection can hide one and
// the total/trend columns have hidden periods to cover.
func pivotSelectionRows() []interface{} {
	return []interface{}{
		[]interface{}{"svc-a", "2026", "06", 10.0, float64(1780272000)},
		[]interface{}{"svc-a", "2026", "07", 20.0, float64(1782864000)},
		[]interface{}{"svc-a", "2026", "08", 30.0, float64(1785542400)},
		[]interface{}{"svc-b", "2026", "06", 1.0, float64(1780272000)},
		[]interface{}{"svc-b", "2026", "07", 2.0, float64(1782864000)},
		[]interface{}{"svc-b", "2026", "08", 3.0, float64(1785542400)},
	}
}

func resetPivotSelectionConfig(t *testing.T) {
	t.Helper()
	viper.Set("rsh-output-format", "table")
	viper.Set("output-order", outputOrderClassic)
	t.Cleanup(func() {
		for _, key := range []string{"table-columns", "table-columns-auto", "rsh-output-format", "output-order", "chart-requested", "chart-mode", "pivot-active"} {
			viper.Set(key, nil)
		}
		pendingPivotColumnError = nil
		resetChartState()
	})
}

// -C on a report keeps the pivot without --pivot: the selection names pivot
// columns and only decides which of them render. Names resolve
// case-insensitively (TOTAL, as the column reads on screen, is the total
// column), come back canonically spelled in the requested order, and the
// selection is explicit — not fit-eligible — so nothing is auto-hidden.
func TestPivotKeepsColumnSelectionWithoutPivotFlag(t *testing.T) {
	resetPivotSelectionConfig(t)
	viper.Set("table-columns", "service_description,2026-07,2026-08,TOTAL")
	viper.Set("table-columns-auto", false)

	if !shouldPivotReportRows() {
		t.Fatal("-C must not switch a human table view to flat rows")
	}
	result, ok := pivotReportBody(pivotSelectionRows(), pivotSchema())
	if !ok {
		t.Fatalf("pivot refused under -C: %v", pendingPivotColumnError)
	}
	if got := viper.GetString("table-columns"); got != "service_description,2026-07,2026-08,total" {
		t.Errorf("table-columns = %q, want the selection resolved to pivot keys", got)
	}
	if viper.GetBool("table-columns-auto") {
		t.Error("an explicit -C selection must not be fit-eligible (no auto-hide)")
	}
	rows := result.([]interface{})
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want two groups + TOTAL", len(rows))
	}
	first := rows[0].(map[string]interface{})
	if first["service_description"] != "svc-a" || first["2026-06"] != 10.0 {
		t.Errorf("pivot row shape changed under -C: %v", first)
	}
}

// TOTAL under -C is the row total over every period, shown or hidden — the
// same choice the width fit makes when it hides trailing period columns —
// and the trend still spans first→last period. The TOTAL row carries its
// total too.
func TestPivotTotalPopulatedUnderColumnSelection(t *testing.T) {
	resetPivotSelectionConfig(t)
	viper.Set("table-columns", "service_description,2026-08,TOTAL,Trend")

	result, ok := pivotReportBody(pivotSelectionRows(), pivotSchema())
	if !ok {
		t.Fatalf("pivot refused under -C: %v", pendingPivotColumnError)
	}
	if got := viper.GetString("table-columns"); got != "service_description,2026-08,total,trend" {
		t.Errorf("table-columns = %q", got)
	}
	rows := result.([]interface{})
	first := rows[0].(map[string]interface{})
	if first["total"] != 60.0 {
		t.Errorf("svc-a total = %v, want 60 (all periods, not just the shown 2026-08)", first["total"])
	}
	if first["trend"] != "+200%" {
		t.Errorf("svc-a trend = %v, want +200%% (first→last period)", first["trend"])
	}
	totals := rows[len(rows)-1].(map[string]interface{})
	if totals["service_description"] != "TOTAL" || totals["total"] != 66.0 {
		t.Errorf("TOTAL row = %v, want total 66", totals)
	}
	if _, present := first["TOTAL"]; present {
		t.Error("rows must keep the canonical total key; the selection is what gets resolved")
	}
}

// The chart draws the whole result: -C hides columns from the table, not
// periods from the series.
func TestPivotChartUnaffectedByColumnSelection(t *testing.T) {
	resetPivotSelectionConfig(t)
	forceTUI(t, true)
	resetChartState()
	viper.Set("chart-requested", true)
	viper.Set("chart-mode", "treemap")
	viper.Set("table-columns", "service_description,2026-08,total")

	if _, ok := pivotReportBody(pivotSelectionRows(), pivotSchema()); !ok {
		t.Fatalf("pivot refused under -C: %v", pendingPivotColumnError)
	}
	if chartSeries == nil {
		t.Fatal("no chart series under -C")
	}
	if len(chartSeries.periods) != 3 {
		t.Errorf("chart periods = %v, want all three (unaffected by -C)", chartSeries.periods)
	}
	if len(chartSeries.groups) != 2 || chartSeries.groups[0].name != "svc-a" {
		t.Errorf("chart groups = %+v, want svc-a, svc-b", chartSeries.groups)
	}
	if chartSeries.values[0] != 11.0 || chartSeries.values[2] != 33.0 {
		t.Errorf("chart period totals = %v", chartSeries.values)
	}
}

// A -C name the pivot does not have (a misspelled month, or a flat column
// such as cost) is a usage error listing the pivot's columns — never an empty
// table. The pivot leaves no side effects behind (pivot-active stays off, no
// chart series) and the formatter hook returns the parked error.
func TestPivotColumnSelectionUnknownColumnIsUsageError(t *testing.T) {
	resetPivotSelectionConfig(t)
	forceTUI(t, true)
	resetChartState()
	viper.Set("chart-requested", true)
	viper.Set("chart-mode", "treemap")
	viper.Set("pivot-active", false)
	viper.Set("table-columns", "service_description,2026-13,cost")

	if _, ok := pivotReportBody(pivotSelectionRows(), pivotSchema()); ok {
		t.Fatal("pivot accepted a selection with unknown columns")
	}
	if viper.GetBool("pivot-active") || chartSeries != nil {
		t.Error("a rejected selection must leave no pivot side effects")
	}
	err := pendingPivotColumnError
	if err == nil {
		t.Fatal("no pending column error")
	}
	var selectionError pivotColumnSelectionError
	if !errors.As(err, &selectionError) || selectionError.ExitCode() != exitUsage {
		t.Fatalf("error = %T %v, want pivotColumnSelectionError with usage exit code", err, err)
	}
	message := err.Error()
	for _, want := range []string{"2026-13", "cost", "available columns: service_description, 2026-06, 2026-07, 2026-08, total, trend", "cost are flat row columns; pass --flat"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q lacks %q", message, want)
		}
	}
	if strings.Contains(message, "2026-13 are flat") {
		t.Errorf("a misspelled period is not a flat column: %q", message)
	}
	if selectionError.AgentErrorCode() != "USAGE_ERROR" || selectionError.AgentErrorRetryable() {
		t.Error("agent contract: USAGE_ERROR, not retryable")
	}

	// The formatter hook surfaces it and renders nothing.
	next := &recordingFormatter{}
	guard := dciResponseGuard{next: next}
	got := guard.Format(cli.Response{Status: 200, Body: map[string]interface{}{"ok": true}})
	if got == nil || got.Error() != err.Error() {
		t.Fatalf("Format returned %v, want the pending selection error", got)
	}
	if next.called {
		t.Error("formatter rendered a table after a rejected -C selection")
	}
	if pendingPivotColumnError != nil {
		t.Error("pending error must be consumed by the hook")
	}
}

// A multi-dimension group column is addressable by any one of its schema
// names as well as the combined header.
func TestPivotColumnSelectionResolvesGroupDimensionNames(t *testing.T) {
	schema := []reportColumn{
		{Name: "service_description", Type: "string"},
		{Name: "sku_description", Type: "string"},
		{Name: "year", Type: "string"},
		{Name: "month", Type: "string"},
		{Name: "cost", Type: "float"},
	}
	groupHeader := "service_description / sku_description"
	available := pivotColumnOrder(groupHeader, false, []string{"2026-06", "2026-07"})
	resolved, err := resolvePivotColumnSelection([]string{"sku_description", "2026-07", "TOTAL", groupHeader}, available, groupHeader, []int{0, 1}, schema)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(resolved, ",") != groupHeader+",2026-07,total" {
		t.Errorf("resolved = %v", resolved)
	}
}
