package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

func TestShapeResponseBodyProjectsAndExcludesWithoutChangingValueTypes(t *testing.T) {
	oldAgentMode := agentMode
	agentMode = true
	viper.Set("agent-fields", "id,description,owner")
	viper.Set("agent-exclude", "owner")
	t.Cleanup(func() {
		agentMode = oldAgentMode
		viper.Reset()
	})

	previousAgentTruncationLength := 2000
	longValue := strings.Repeat("a", previousAgentTruncationLength+9)
	input := map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{
				"id":          "item-1",
				"description": longValue,
				"owner":       "owner@example.com",
				"ignored":     true,
			},
		},
		"pageToken": "next",
	}
	shaped := shapeResponseBody(input).(map[string]interface{})
	rows := shaped["results"].([]interface{})
	row := rows[0].(map[string]interface{})
	if _, exists := row["owner"]; exists {
		t.Fatal("excluded field remains")
	}
	if _, exists := row["ignored"]; exists {
		t.Fatal("unselected field remains")
	}
	if row["description"] != longValue {
		t.Fatalf("description changed type or value: %#v", row["description"])
	}
	if shaped["pageToken"] != "next" {
		t.Fatal("wrapper metadata was removed")
	}
}

func TestExcludeHonorsWrapperFieldsWithoutRecursingIntoNestedObjects(t *testing.T) {
	viper.Set("agent-exclude", "rowCount,amount")
	t.Cleanup(viper.Reset)

	input := map[string]interface{}{
		"budgets": []interface{}{
			map[string]interface{}{
				"id":     "budget-1",
				"amount": 1000,
				"alertThresholds": []interface{}{
					map[string]interface{}{"amount": 900, "percentage": 90},
				},
			},
		},
		"rowCount": 1,
	}

	shaped := shapeResponseBody(input).(map[string]interface{})
	if _, exists := shaped["rowCount"]; exists {
		t.Fatal("explicitly excluded rowCount remains")
	}
	row := shaped["budgets"].([]interface{})[0].(map[string]interface{})
	if _, exists := row["amount"]; exists {
		t.Fatal("top-level row amount remains")
	}
	threshold := row["alertThresholds"].([]interface{})[0].(map[string]interface{})
	if threshold["amount"] != 900 {
		t.Fatalf("nested amount = %#v", threshold["amount"])
	}
}

func TestExcludeHonorsListWrapperKey(t *testing.T) {
	viper.Set("agent-exclude", "budgets")
	t.Cleanup(viper.Reset)

	input := map[string]interface{}{
		"budgets":  []interface{}{map[string]interface{}{"id": "budget-1"}},
		"rowCount": 1,
	}
	shaped := shapeResponseBody(input).(map[string]interface{})
	if _, exists := shaped["budgets"]; exists {
		t.Fatal("explicitly excluded list wrapper remains")
	}
	if shaped["rowCount"] != 1 {
		t.Fatalf("rowCount = %#v", shaped["rowCount"])
	}
}

func TestExcludeFiltersReportSchemaAndPositionalRows(t *testing.T) {
	viper.Set("agent-exclude", "cost")
	t.Cleanup(viper.Reset)

	input := map[string]interface{}{
		"result": map[string]interface{}{
			"schema": []interface{}{
				map[string]interface{}{"name": "service"},
				map[string]interface{}{"name": "cost"},
			},
			"rows": []interface{}{[]interface{}{"BigQuery", 123.45}},
		},
	}
	shaped := shapeResponseBody(input).(map[string]interface{})
	result := shaped["result"].(map[string]interface{})
	wantSchema := []interface{}{map[string]interface{}{"name": "service"}}
	if !reflect.DeepEqual(result["schema"], wantSchema) {
		t.Fatalf("schema = %#v, want %#v", result["schema"], wantSchema)
	}
	wantRows := []interface{}{[]interface{}{"BigQuery"}}
	if !reflect.DeepEqual(result["rows"], wantRows) {
		t.Fatalf("rows = %#v, want %#v", result["rows"], wantRows)
	}
}

func TestExcludeHonorsReportContainerFields(t *testing.T) {
	viper.Set("agent-exclude", "schema")
	t.Cleanup(viper.Reset)

	input := map[string]interface{}{
		"result": map[string]interface{}{
			"schema": []interface{}{map[string]interface{}{"name": "service"}},
			"rows":   []interface{}{[]interface{}{"BigQuery"}},
		},
	}
	result := shapeResponseBody(input).(map[string]interface{})["result"].(map[string]interface{})
	if _, exists := result["schema"]; exists {
		t.Fatal("explicitly excluded schema remains")
	}
	if _, exists := result["rows"]; !exists {
		t.Fatal("rows were removed with schema")
	}
}

func TestUnknownProjectionFieldsReturnUsageError(t *testing.T) {
	viper.Set("agent-fields", "id,nosuchfield")
	t.Cleanup(viper.Reset)

	input := map[string]interface{}{
		"budgets":  []interface{}{map[string]interface{}{"id": "budget-1"}},
		"rowCount": 1,
	}
	guard := dciOutputGuard{next: &recordingFormatter{}}
	err := guard.Format(cli.Response{Status: 200, Body: input})
	if err == nil {
		t.Fatal("expected invalid field error")
	}
	if !strings.Contains(err.Error(), "requested fields are not projectable row fields: nosuchfield") {
		t.Fatalf("error = %q", err)
	}
	if !strings.Contains(err.Error(), "available fields: id") {
		t.Fatalf("error does not list available fields: %q", err)
	}
	if coded, ok := err.(interface{ ExitCode() int }); !ok || coded.ExitCode() != exitUsage {
		t.Fatalf("error exit code = %#v, want %d", err, exitUsage)
	}
}

func TestProjectionFieldsUseReportSchemaWhenRowsAreEmpty(t *testing.T) {
	input := map[string]interface{}{
		"result": map[string]interface{}{
			"schema": []interface{}{
				map[string]interface{}{"name": "service"},
				map[string]interface{}{"name": "cost"},
			},
			"rows": []interface{}{},
		},
	}
	if err := validateResponseFields(input, []string{"service"}); err != nil {
		t.Fatalf("valid field rejected: %v", err)
	}
	err := validateResponseFields(input, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "available fields: cost, service") {
		t.Fatalf("error = %v", err)
	}
}

func TestProjectionFieldsIncludeSchemaAndObjectRowFields(t *testing.T) {
	input := map[string]interface{}{
		"result": map[string]interface{}{
			"schema": []interface{}{map[string]interface{}{"name": "service"}},
			"rows": []interface{}{
				map[string]interface{}{"service": "BigQuery", "cost": 12.5},
			},
		},
	}
	if err := validateResponseFields(input, []string{"cost"}); err != nil {
		t.Fatalf("object row field rejected: %v", err)
	}

	viper.Set("agent-fields", "cost")
	t.Cleanup(viper.Reset)
	rows := shapeResponseBody(input).(map[string]interface{})["result"].(map[string]interface{})["rows"].([]interface{})
	want := []interface{}{map[string]interface{}{"cost": 12.5}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("projected rows = %#v, want %#v", rows, want)
	}
}

func TestProjectionErrorDistinguishesWrapperMetadata(t *testing.T) {
	input := map[string]interface{}{
		"budgets":  []interface{}{map[string]interface{}{"id": "budget-1"}},
		"rowCount": 1,
	}
	err := validateResponseFields(input, []string{"rowCount"})
	if err == nil || !strings.Contains(err.Error(), "not projectable row fields: rowCount") {
		t.Fatalf("error = %v", err)
	}
}

func TestProjectionWarningUsesObjectReportRowFields(t *testing.T) {
	viper.Set("agent-fields", "service")
	oldStderr := cli.Stderr
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		cli.Stderr = oldStderr
		viper.Reset()
	})

	input := map[string]interface{}{
		"result": map[string]interface{}{
			"schema": []interface{}{map[string]interface{}{"name": "colA"}},
			"rows":   []interface{}{map[string]interface{}{"service": "BigQuery"}},
		},
	}
	shapeResponseBody(input)
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestEmptyProjectionDoesNotWarn(t *testing.T) {
	viper.Set("agent-fields", "id")
	oldStderr := cli.Stderr
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		cli.Stderr = oldStderr
		viper.Reset()
	})

	shapeResponseBody([]interface{}{})
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestShapeResponseBodyDefinitiveEmptyState(t *testing.T) {
	oldAgentMode := agentMode
	agentMode = true
	viper.Reset()
	t.Cleanup(func() {
		agentMode = oldAgentMode
		viper.Reset()
	})

	got := shapeResponseBody([]interface{}{})
	want := map[string]interface{}{"count": 0, "results": []interface{}{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty state = %#v, want %#v", got, want)
	}
}

func TestShapeResponseBodyProjectsDetailWithArrayFields(t *testing.T) {
	viper.Set("agent-fields", "id")
	t.Cleanup(viper.Reset)

	input := map[string]interface{}{
		"id":         "budget-1",
		"name":       "Production",
		"amount":     1000,
		"alerts":     []interface{}{map[string]interface{}{"id": "alert-1"}},
		"recipients": []interface{}{"owner@example.com"},
		"scopes":     []interface{}{map[string]interface{}{"key": "service"}},
	}

	got := shapeResponseBody(input)
	want := map[string]interface{}{"id": "budget-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detail projection = %#v, want %#v", got, want)
	}
}

func TestShapeResponseBodyProjectsRowCountListWrappers(t *testing.T) {
	viper.Set("agent-fields", "id")
	t.Cleanup(viper.Reset)

	for _, resource := range []string{"budgets", "folders", "labels", "annotations", "anomalies"} {
		t.Run(resource, func(t *testing.T) {
			input := map[string]interface{}{
				resource: []interface{}{
					map[string]interface{}{"id": resource + "-1", "name": "Production"},
				},
				"rowCount": 1,
			}

			shaped := shapeResponseBody(input).(map[string]interface{})
			if shaped["rowCount"] != 1 {
				t.Fatalf("rowCount = %v", shaped["rowCount"])
			}
			rows, ok := shaped[resource].([]interface{})
			if !ok || len(rows) != 1 {
				t.Fatalf("%s = %#v", resource, shaped[resource])
			}
			want := map[string]interface{}{"id": resource + "-1"}
			if !reflect.DeepEqual(rows[0], want) {
				t.Fatalf("projected row = %#v, want %#v", rows[0], want)
			}
		})
	}
}

func TestShapeResponseBodyProjectsNestedQueryRows(t *testing.T) {
	viper.Set("agent-fields", "service")
	t.Cleanup(viper.Reset)

	input := map[string]interface{}{
		"requestId": "request-1",
		"result": map[string]interface{}{
			"rows": []interface{}{
				map[string]interface{}{"service": "compute", "cost": 12.5},
				[]interface{}{"storage", 4.25},
			},
			"schema": []interface{}{
				map[string]interface{}{"name": "service"},
				map[string]interface{}{"name": "cost"},
			},
		},
	}

	shaped := shapeResponseBody(input).(map[string]interface{})
	if shaped["requestId"] != "request-1" {
		t.Fatal("query wrapper metadata was removed")
	}
	rows := shaped["result"].(map[string]interface{})["rows"].([]interface{})
	want := []interface{}{
		map[string]interface{}{"service": "compute"},
		map[string]interface{}{"service": "storage"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("query rows = %#v, want %#v", rows, want)
	}
}

func TestResponseGuardInspectsBodyBeforeProjection(t *testing.T) {
	oldAgentMode := agentMode
	oldStderr := cli.Stderr
	agentMode = false
	nonJSONErrorResponse = false
	viper.Set("agent-fields", "answer")
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		agentMode = oldAgentMode
		nonJSONErrorResponse = false
		cli.Stderr = oldStderr
		viper.Reset()
	})

	next := &recordingFormatter{}
	guard := dciResponseGuard{next: dciOutputGuard{next: next}}
	err := guard.Format(cli.Response{
		Status: 200,
		Body:   map[string]interface{}{"error": "generation failed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !nonJSONErrorResponse {
		t.Fatal("application error was hidden by projection")
	}
	if !strings.Contains(stderr.String(), "generation failed") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// exportedFlowBundle mirrors the shape of an export-cloudflow-flow response:
// document metadata alongside the `flows` array.
func exportedFlowBundle() map[string]interface{} {
	return map[string]interface{}{
		"kind":          cloudflowBundleKind,
		"schemaVersion": 1,
		"rootFlow":      "flow-1",
		"flows": []interface{}{
			map[string]interface{}{"key": "flow-1", "name": "Nightly report", "firstNode": "n1"},
		},
		"requirements": map[string]interface{}{
			"connections": []interface{}{
				map[string]interface{}{"key": "aws-1", "provider": "amazon-web-services", "name": "Prod"},
			},
		},
	}
}

func TestCloudflowBundleCommandsDefaultToJSONOutput(t *testing.T) {
	oldAgentMode := agentMode
	t.Cleanup(func() { agentMode = oldAgentMode })

	for _, agent := range []bool{false, true} {
		agentMode = agent
		// The bundle has to be JSON in both modes to stay importable.
		if got := defaultOutputFormatForCommand("export-cloudflow-flow"); got != "json" {
			t.Fatalf("export default output (agentMode=%v) = %q, want json", agent, got)
		}
		// The import plan only loses sections to the table renderer, so agent
		// mode keeps TOON — it encodes the whole body.
		wantImport := "json"
		if agent {
			wantImport = "toon"
		}
		if got := defaultOutputFormatForCommand("import-cloudflow-flow"); got != wantImport {
			t.Fatalf("import default output (agentMode=%v) = %q, want %q", agent, got, wantImport)
		}
		// Every other command keeps the mode's default: table for humans,
		// TOON for agents.
		want := "table"
		if agent {
			want = "toon"
		}
		if got := defaultOutputFormatForCommand("list-cloudflows"); got != want {
			t.Fatalf("list default output (agentMode=%v) = %q, want %q", agent, got, want)
		}
	}
}

// The reason export defaults to JSON: the table renderer treats a bundle as a
// list wrapper around `flows` and drops the document fields that make the
// bundle importable. If this ever stops being true the JSON default can be
// revisited — until then it is load-bearing.
func TestTableRenderingOfAFlowBundleLosesDocumentFields(t *testing.T) {
	rows, err := toTableRows(exportedFlowBundle(), labelDisplay)
	if err != nil {
		t.Fatalf("toTableRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the flows array rendered as one row", len(rows))
	}
	for _, field := range []string{"kind", "schemaVersion", "rootFlow", "requirements"} {
		if _, present := rows[0][field]; present {
			t.Fatalf("table unexpectedly kept %q; re-check the export JSON default", field)
		}
	}
}

// The reason import defaults to JSON: a dry-run plan carries its verdict in
// `valid`/`errors`, and the table renderer keeps only the longest array — here
// `requirements` — so a rejected plan would render as a clean requirements
// list with the errors nowhere in sight.
func TestTableRenderingOfAnImportPlanLosesTheVerdict(t *testing.T) {
	plan := map[string]interface{}{
		"valid": false,
		"requirements": []interface{}{
			map[string]interface{}{"section": "connections", "key": "aws-1", "resolution": "unbound"},
			map[string]interface{}{"section": "globalVariables", "key": "region", "resolution": "willCreate"},
		},
		"flowsToCreate": []interface{}{
			map[string]interface{}{"key": "flow-1", "name": "Nightly report", "nodeCount": 3},
		},
		"errors": []interface{}{
			map[string]interface{}{"code": "binding_not_found", "message": "no connection matches aws-1"},
		},
	}
	rows, err := toTableRows(plan, labelDisplay)
	if err != nil {
		t.Fatalf("toTableRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the requirements array (the longest section)", len(rows))
	}
	for _, field := range []string{"valid", "errors"} {
		if _, present := rows[0][field]; present {
			t.Fatalf("table unexpectedly kept %q; re-check the import JSON default", field)
		}
	}
}
