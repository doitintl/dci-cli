package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

func TestShapeResponseBodyProjectsExcludesAndTruncates(t *testing.T) {
	oldAgentMode := agentMode
	agentMode = true
	viper.Set("agent-fields", "id,description,owner")
	viper.Set("agent-exclude", "owner")
	viper.Set("agent-full", false)
	viper.Set("agent-no-truncate", false)
	t.Cleanup(func() {
		agentMode = oldAgentMode
		viper.Reset()
	})

	longValue := strings.Repeat("a", defaultAgentTruncationLength+9)
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
	truncated := row["description"].(map[string]interface{})
	if truncated["_truncated"] != 9 {
		t.Fatalf("truncation marker = %v", truncated["_truncated"])
	}
	if shaped["pageToken"] != "next" {
		t.Fatal("wrapper metadata was removed")
	}
}

func TestExcludePreservesWrapperMetadataAndNestedObjects(t *testing.T) {
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
	if shaped["rowCount"] != 1 {
		t.Fatalf("rowCount = %#v", shaped["rowCount"])
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

func TestUnknownProjectionFieldsWriteWarning(t *testing.T) {
	viper.Set("agent-fields", "nosuchfield")
	oldStderr := cli.Stderr
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		cli.Stderr = oldStderr
		viper.Reset()
	})

	input := map[string]interface{}{
		"budgets":  []interface{}{map[string]interface{}{"id": "budget-1"}},
		"rowCount": 1,
	}
	shaped := shapeResponseBody(input).(map[string]interface{})
	if !strings.Contains(stderr.String(), "none of the requested fields exist") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if shaped["rowCount"] != 1 {
		t.Fatalf("rowCount = %#v", shaped["rowCount"])
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
