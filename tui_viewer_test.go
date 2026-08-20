package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func viewerFixture() *tableViewerModel {
	rows := []map[string]interface{}{
		{"id": "id-alpha", "name": "alpha", "cost": 30.0},
		{"id": "id-beta", "name": "Beta", "cost": 10.0},
		{"id": "id-gamma", "name": "gamma", "cost": 20.0},
	}
	return newTableViewerModel(rows, []string{"name", "cost"})
}

func TestViewerSortCycle(t *testing.T) {
	model := viewerFixture()
	model.focusCol = 1 // cost

	// First press: ascending numeric sort.
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if got := model.rows[model.visible[0]]["cost"]; got != 10.0 {
		t.Fatalf("ascending sort first row cost = %v, want 10", got)
	}
	// Second press: descending.
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if got := model.rows[model.visible[0]]["cost"]; got != 30.0 {
		t.Fatalf("descending sort first row cost = %v, want 30", got)
	}
	// Third press: original order restored.
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if got := model.rows[model.visible[0]]["cost"]; got != 30.0 || model.sortCol != -1 {
		t.Fatalf("third press must restore original order, got %v (sortCol %d)", got, model.sortCol)
	}
}

func TestViewerStringSortIsCaseInsensitive(t *testing.T) {
	model := viewerFixture()
	model.focusCol = 0 // name
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	order := make([]string, len(model.visible))
	for i, rowIndex := range model.visible {
		order[i] = model.rows[rowIndex]["name"].(string)
	}
	if strings.Join(order, ",") != "alpha,Beta,gamma" {
		t.Fatalf("case-insensitive sort order = %v", order)
	}
}

func TestViewerFilter(t *testing.T) {
	model := viewerFixture()
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !model.filtering {
		t.Fatal("/ must enter filter mode")
	}
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bet")})
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.filtering {
		t.Fatal("enter must apply the filter and leave filter mode")
	}
	if len(model.visible) != 1 || model.rows[model.visible[0]]["name"] != "Beta" {
		t.Fatalf("filter 'bet' kept %d rows", len(model.visible))
	}
	// Esc clears the filter entirely.
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(model.visible) != 3 {
		t.Fatalf("esc must clear the filter, %d rows visible", len(model.visible))
	}
}

func TestViewerSelectionPrintsID(t *testing.T) {
	model := viewerFixture()
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.selection != "id-alpha" {
		t.Fatalf("selection = %q, want the focused row's id", model.selection)
	}
}

func TestViewerColumnWindow(t *testing.T) {
	rows := []map[string]interface{}{{
		"aaaaaaaaaaaaaaaaaaaa": "x", "bbbbbbbbbbbbbbbbbbbb": "x",
		"cccccccccccccccccccc": "x", "dddddddddddddddddddd": "x",
		"eeeeeeeeeeeeeeeeeeee": "x", "ffffffffffffffffffff": "x",
	}}
	keys := []string{
		"aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccc",
		"dddddddddddddddddddd", "eeeeeeeeeeeeeeeeeeee", "ffffffffffffffffffff",
	}
	model := newTableViewerModel(rows, keys)
	model.width = 60
	model.rebuild()
	_, end := model.visibleColumnRange()
	if end >= len(keys) {
		t.Fatal("narrow terminal must window the columns")
	}
	// Focusing the last column shifts the window until it is visible.
	model.focusCol = len(keys) - 1
	model.ensureFocusVisible()
	start, end := model.visibleColumnRange()
	if model.focusCol < start || model.focusCol >= end {
		t.Fatalf("focus column %d outside window [%d,%d)", model.focusCol, start, end)
	}
}

func TestComposeQueryConfig(t *testing.T) {
	configJSON, err := composeQueryConfig(
		queryTimePreset{"Last 30 days", 30, "day", "day"},
		false, "",
		[]queryDimension{{ID: "service_description", Type: "fixed"}},
		"cost", true,
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Config struct {
			DataSource   string                   `json:"dataSource"`
			TimeInterval string                   `json:"timeInterval"`
			TimeRange    map[string]interface{}   `json:"timeRange"`
			Metrics      []map[string]interface{} `json:"metrics"`
			MetricFilter map[string]interface{}   `json:"metricFilter"`
			Group        []map[string]interface{} `json:"group"`
		} `json:"config"`
	}
	if err := json.Unmarshal(configJSON, &parsed); err != nil {
		t.Fatal(err)
	}
	c := parsed.Config
	if c.DataSource != "billing" || c.TimeInterval != "day" {
		t.Fatalf("dataSource/timeInterval = %q/%q", c.DataSource, c.TimeInterval)
	}
	if c.TimeRange["amount"].(float64) != 30 || c.TimeRange["unit"] != "day" || c.TimeRange["includeCurrent"] != false {
		t.Fatalf("timeRange = %v", c.TimeRange)
	}
	if len(c.Group) != 1 || c.Group[0]["id"] != "service_description" || c.Group[0]["type"] != "fixed" {
		t.Fatalf("group = %v", c.Group)
	}
	if c.MetricFilter["operator"] != "gt" {
		t.Fatalf("metricFilter = %v", c.MetricFilter)
	}

	// No groups, keep zero rows: both keys absent.
	minimalJSON, err := composeQueryConfig(queryTimePresets[0], true, "month", nil, "usage", false)
	if err != nil {
		t.Fatal(err)
	}
	minimal := string(minimalJSON)
	if strings.Contains(minimal, "\"group\"") || strings.Contains(minimal, "metricFilter") {
		t.Fatalf("minimal config must omit group and metricFilter: %s", minimal)
	}
	if !strings.Contains(minimal, "\"timeInterval\": \"month\"") {
		t.Fatalf("explicit interval must override the preset: %s", minimal)
	}
}

func TestSaveQueryConfigAvoidsClobbering(t *testing.T) {
	dir := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(original) })

	first, err := saveQueryConfig([]byte("{}"))
	if err != nil || first != "query.json" {
		t.Fatalf("first save = %q, %v", first, err)
	}
	second, err := saveQueryConfig([]byte("{}"))
	if err != nil || second != "query-2.json" {
		t.Fatalf("second save = %q, %v", second, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "query-2.json")); err != nil {
		t.Fatal(err)
	}
}

func TestBuilderBodyInfoReadsAsPipedInput(t *testing.T) {
	info := builderBodyInfo{size: 42}
	if info.Mode()&os.ModeCharDevice != 0 {
		t.Fatal("builder body must not stat as a terminal, or restish would ignore it")
	}
	if info.Size() != 42 {
		t.Fatalf("size = %d", info.Size())
	}
}
