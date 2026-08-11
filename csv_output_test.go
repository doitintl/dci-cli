package main

import (
	"encoding/csv"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestCSVMarshalListWrapper(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	body := map[string]interface{}{
		"rowCount": float64(2),
		"reports": []interface{}{
			map[string]interface{}{"id": "r1", "reportName": "Costs, monthly"},
			map[string]interface{}{"id": "r2", "reportName": "Spend"},
		},
	}
	out, err := dciCSVContentType{}.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\n%s", err, out)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want header + 2 rows", len(records))
	}
	header := strings.Join(records[0], ",")
	if !strings.Contains(header, "id") || !strings.Contains(header, "reportName") {
		t.Errorf("header = %q, want id and reportName", header)
	}
	// The comma inside "Costs, monthly" must be quoted, not split.
	foundQuoted := false
	for _, record := range records[1:] {
		for _, cell := range record {
			if cell == "Costs, monthly" {
				foundQuoted = true
			}
		}
	}
	if !foundQuoted {
		t.Error("comma-containing cell was not preserved")
	}
}

func TestCSVMarshalReportRowsUseSchemaHeader(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	body := map[string]interface{}{
		"result": map[string]interface{}{
			"rows": []interface{}{
				[]interface{}{"svc", "2026", "07", 12.5, float64(1782864000)},
			},
			"schema": []interface{}{
				map[string]interface{}{"name": "service_description", "type": "string"},
				map[string]interface{}{"name": "year", "type": "string"},
				map[string]interface{}{"name": "month", "type": "string"},
				map[string]interface{}{"name": "cost", "type": "float"},
				map[string]interface{}{"name": "timestamp", "type": "timestamp"},
			},
		},
	}
	out, err := dciCSVContentType{}.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\n%s", err, out)
	}
	header := strings.Join(records[0], ",")
	if !strings.Contains(header, "cost") || !strings.Contains(header, "service_description") {
		t.Errorf("header = %q, want schema column names", header)
	}
}

func TestCSVMarshalEmptyReportUsesSchemaHeader(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	body := map[string]interface{}{
		"result": map[string]interface{}{
			"rows": []interface{}{},
			"schema": []interface{}{
				map[string]interface{}{"name": "service_description", "type": "string"},
				map[string]interface{}{"name": "cost", "type": "float"},
			},
		},
	}
	out, err := dciCSVContentType{}.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\n%s", err, out)
	}
	if len(records) != 1 || strings.Join(records[0], ",") != "service_description,cost" {
		t.Fatalf("records = %#v, want schema header", records)
	}
}
