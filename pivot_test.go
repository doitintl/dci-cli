package main

import (
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
