package main

import (
	"reflect"
	"strings"
	"testing"

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
