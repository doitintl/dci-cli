package main

import (
	"fmt"
	"strings"
	"testing"

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

func TestPivotReportBodyTimeAsColumns(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	rows := []interface{}{
		[]interface{}{"svc-a", "2026", "06", 10.0, float64(1780272000)},
		[]interface{}{"svc-a", "2026", "07", 20.0, float64(1782864000)},
		[]interface{}{"svc-b", "2026", "06", 100.0, float64(1780272000)},
		[]interface{}{"svc-b", "2026", "07", 200.0, float64(1782864000)},
	}
	result, ok := pivotReportBody(rows, pivotSchema(), true)
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
	result, ok := pivotReportBody(rows, pivotSchema(), true)
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
	result, ok := pivotReportBody(rows, schema, true)
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
		viper.Set("pivot-columns-auto", nil)
	})
	rows := []interface{}{
		[]interface{}{"svc-a", "2026", "06", 100.0, float64(1780272000)},
		[]interface{}{"svc-a", "2026", "07", 200.0, float64(1782864000)},
	}
	result, ok := pivotReportBody(rows, pivotSchema(), true)
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

func TestPivotDefaultFallsBackOnManyPeriods(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() {
		viper.Set("table-columns", nil)
		viper.Set("pivot-columns-auto", nil)
	})

	rows := []interface{}{}
	for day := 1; day <= maxDefaultPivotPeriods+3; day++ {
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
	if _, ok := pivotReportBody(rows, schema, false); ok {
		t.Error("default pivot applied despite too many periods")
	}
	if _, ok := pivotReportBody(rows, schema, true); !ok {
		t.Error("forced pivot refused")
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
	reset(false, "table", "cost,month", false, false)
	if shouldPivotReportRows() {
		t.Error("-C column selection must keep the flat layout")
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
	if _, ok := pivotReportBody([]interface{}{"not-a-row"}, pivotSchema(), true); ok {
		t.Error("pivot applied to malformed rows")
	}
	if _, ok := pivotReportBody(nil, nil, true); ok {
		t.Error("pivot applied to empty input")
	}
	// No time columns → nothing to pivot.
	schema := []reportColumn{{Name: "service_description", Type: "string"}, {Name: "cost", Type: "float"}}
	if _, ok := pivotReportBody([]interface{}{[]interface{}{"svc", 1.0}}, schema, true); ok {
		t.Error("pivot applied without time columns")
	}
}
