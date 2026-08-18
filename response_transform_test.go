package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

func resetTransformConfig(t *testing.T) {
	t.Helper()
	oldAgentMode := agentMode
	t.Cleanup(func() {
		agentMode = oldAgentMode
		viper.Set("max-rows", nil)
		viper.Set("rows-mode", nil)
		viper.Set("pivot-rows", nil)
		viper.Set("pivot-active", nil)
		viper.Set("include-empty-rows", nil)
		viper.Set("table-columns", nil)
	})
	viper.Set("max-rows", -1)
	viper.Set("rows-mode", "")
	viper.Set("pivot-rows", false)
	viper.Set("pivot-active", false)
	viper.Set("include-empty-rows", false)
	viper.Set("table-columns", "")
}

func reportBody(rows ...[]interface{}) map[string]interface{} {
	items := make([]interface{}, len(rows))
	for i, row := range rows {
		items[i] = row
	}
	return map[string]interface{}{
		"result": map[string]interface{}{
			"rows": items,
			"schema": []interface{}{
				map[string]interface{}{"name": "service_description", "type": "string"},
				map[string]interface{}{"name": "year", "type": "string"},
				map[string]interface{}{"name": "month", "type": "string"},
				map[string]interface{}{"name": "cost", "type": "float"},
				map[string]interface{}{"name": "timestamp", "type": "timestamp"},
			},
		},
	}
}

func transformedRows(t *testing.T, body interface{}) []interface{} {
	t.Helper()
	root := body.(map[string]interface{})
	container := root["result"].(map[string]interface{})
	return container["rows"].([]interface{})
}

func TestNormalizeIntegralNumbersConvertsWholeFloats(t *testing.T) {
	body := map[string]interface{}{
		"createTime": 1.786353374392e+12,
		"cost":       20881.451376860987,
		"nested":     []interface{}{float64(1786060800)},
	}
	result := normalizeIntegralNumbers(body).(map[string]interface{})
	if _, ok := result["createTime"].(int64); !ok {
		t.Errorf("createTime = %T(%v), want int64", result["createTime"], result["createTime"])
	}
	if _, ok := result["cost"].(float64); !ok {
		t.Errorf("cost = %T, want float64 preserved", result["cost"])
	}
	if _, ok := result["nested"].([]interface{})[0].(int64); !ok {
		t.Errorf("nested timestamp not converted to int64")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "e+") {
		t.Errorf("JSON still contains scientific notation: %s", encoded)
	}
}

func TestNullListsToEmptyOnListResponses(t *testing.T) {
	body := map[string]interface{}{"budgets": nil, "rowCount": float64(0), "nextPageToken": nil}
	result := nullListsToEmpty(body).(map[string]interface{})
	arr, ok := result["budgets"].([]interface{})
	if !ok || len(arr) != 0 {
		t.Errorf("budgets = %#v, want empty array", result["budgets"])
	}
	if result["nextPageToken"] != nil {
		t.Errorf("nextPageToken = %#v, want nil preserved", result["nextPageToken"])
	}
}

func TestNullFieldsOnDetailResponsesStayNull(t *testing.T) {
	body := map[string]interface{}{"description": nil, "id": "x"}
	result := nullListsToEmpty(body).(map[string]interface{})
	if result["description"] != nil {
		t.Errorf("description = %#v, want nil preserved (no list metadata)", result["description"])
	}
}

func resetInsightConfig(t *testing.T) {
	t.Helper()
	resetTransformConfig(t)
	oldCommand := invokedCommandName
	t.Cleanup(func() {
		invokedCommandName = oldCommand
		viper.Set("include-dismissed", nil)
		viper.Set("rsh-output-format", nil)
		viper.Set("table-columns-auto", nil)
		viper.Set("table-priority-column", nil)
		viper.Set("table-accent-column", nil)
		viper.Set("table-accent-flag-key", nil)
	})
	invokedCommandName = "list-insights"
	viper.Set("include-dismissed", false)
	viper.Set("rsh-output-format", "table")
	viper.Set("table-columns-auto", false)
	viper.Set("table-priority-column", "")
	viper.Set("table-accent-column", "")
	viper.Set("table-accent-flag-key", "")
}

func insightRow(overrides map[string]interface{}) map[string]interface{} {
	row := map[string]interface{}{
		"title":                  "Purchase reserved instances",
		"shortDescription":       "Savings from commitments",
		"detailedDescriptionMdx": "Long **markdown** prose",
		"cloudProvider":          "aws",
		"cloudFlowTemplateId":    "",
		"categories":             []interface{}{"FinOps"},
		"easyWinDescription":     "",
		"key":                    "reserved-instances",
		"displayStatus":          "actionable",
		"source":                 "aws-cost-optimization-hub",
		"lastUpdated":            "2026-08-18T02:30:59Z",
		"reportUrl":              "",
		"tags":                   []interface{}{},
		"lastStatusChange":       map[string]interface{}{"userId": "u"},
	}
	for k, v := range overrides {
		row[k] = v
	}
	return row
}

func insightsBody(rows ...map[string]interface{}) map[string]interface{} {
	items := make([]interface{}, len(rows))
	for i, row := range rows {
		items[i] = row
	}
	return map[string]interface{}{
		"pagination": map[string]interface{}{"pageToken": "", "rowCount": int64(len(rows))},
		"results":    items,
	}
}

func TestTransformInsightsDropsDismissedByDefault(t *testing.T) {
	resetInsightConfig(t)
	body := insightsBody(
		insightRow(map[string]interface{}{"displayStatus": "dismissed", "key": "gone"}),
		insightRow(nil),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	results := root["results"].([]interface{})
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (dismissed row dropped)", len(results))
	}
	if omitted, _ := root["dismissedOmitted"].(int64); omitted != 1 {
		t.Errorf("dismissedOmitted = %v, want 1", root["dismissedOmitted"])
	}
}

func TestTransformInsightsKeepsDismissedWhenRequested(t *testing.T) {
	resetInsightConfig(t)
	viper.Set("include-dismissed", true)
	body := insightsBody(
		insightRow(map[string]interface{}{"displayStatus": "dismissed"}),
		insightRow(nil),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	if results := root["results"].([]interface{}); len(results) != 2 {
		t.Fatalf("results = %d, want 2 with --include-dismissed", len(results))
	}
	if _, present := root["dismissedOmitted"]; present {
		t.Error("dismissedOmitted set with --include-dismissed")
	}
}

func TestTransformInsightsIgnoresOtherCommands(t *testing.T) {
	resetInsightConfig(t)
	invokedCommandName = "list-anomalies"
	body := insightsBody(insightRow(map[string]interface{}{"displayStatus": "dismissed"}))
	root := transformSuccessBody(body).(map[string]interface{})
	if results := root["results"].([]interface{}); len(results) != 1 {
		t.Fatalf("results = %d, want 1 (other commands untouched)", len(results))
	}
	if cols := viper.GetString("table-columns"); cols != "" {
		t.Errorf("table-columns = %q, want unset for other commands", cols)
	}
}

func TestTransformInsightsCuratesDefaultTableView(t *testing.T) {
	resetInsightConfig(t)
	body := insightsBody(
		insightRow(map[string]interface{}{"easyWinDescription": "Quick fix", "cloudProvider": "gcp"}),
		insightRow(nil),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	rows := root["results"].([]interface{})

	first := rows[0].(map[string]interface{})
	if first["provider"] != "gcp" {
		t.Errorf("provider = %v, want gcp", first["provider"])
	}
	if first["easyWin"] != "✓" {
		t.Errorf("easyWin = %v, want ✓ for non-empty easyWinDescription", first["easyWin"])
	}
	second := rows[1].(map[string]interface{})
	if second["easyWin"] != "" {
		t.Errorf("easyWin = %v, want empty for empty easyWinDescription", second["easyWin"])
	}

	want := "title,provider,categories,lastUpdated,source"
	if cols := viper.GetString("table-columns"); cols != want {
		t.Errorf("table-columns = %q, want %q", cols, want)
	}
	if !viper.GetBool("table-columns-auto") {
		t.Error("table-columns-auto = false, want true (order stays fit-eligible)")
	}
	if got := viper.GetString("table-priority-column"); got != "title" {
		t.Errorf("table-priority-column = %q, want title", got)
	}
	if got := viper.GetString("table-accent-column"); got != "title" {
		t.Errorf("table-accent-column = %q, want title", got)
	}
	if got := viper.GetString("table-accent-flag-key"); got != "easyWin" {
		t.Errorf("table-accent-flag-key = %q, want easyWin", got)
	}
}

func TestWidenPriorityColumnTakesSurplusFromWidestDonor(t *testing.T) {
	t.Cleanup(func() { viper.Set("table-priority-column", nil) })
	viper.Set("table-priority-column", "title")
	keys := []string{"title", "provider", "source"}
	contentWidths := []int{90, 8, 25}
	colWidths := []int{45, 8, 25}
	widenPriorityColumn(keys, contentWidths, colWidths)
	if colWidths[1] != 8 {
		t.Errorf("provider width = %d, want 8 (already at content width)", colWidths[1])
	}
	if colWidths[2] != 16 {
		t.Errorf("source width = %d, want 16 (donated down to floor)", colWidths[2])
	}
	if colWidths[0] != 54 {
		t.Errorf("title width = %d, want 54 (45 + 9 donated)", colWidths[0])
	}
}

func TestWidenPriorityColumnNoopWhenUnsetOrFitting(t *testing.T) {
	t.Cleanup(func() { viper.Set("table-priority-column", nil) })
	viper.Set("table-priority-column", "")
	widths := []int{10, 20}
	widenPriorityColumn([]string{"a", "b"}, []int{30, 20}, widths)
	if widths[0] != 10 || widths[1] != 20 {
		t.Errorf("widths = %v, want unchanged without a priority column", widths)
	}

	viper.Set("table-priority-column", "a")
	fitting := []int{30, 20}
	widenPriorityColumn([]string{"a", "b"}, []int{30, 20}, fitting)
	if fitting[0] != 30 || fitting[1] != 20 {
		t.Errorf("widths = %v, want unchanged when priority column already fits", fitting)
	}
}

func TestAccentCellColorsFlaggedRowsOnly(t *testing.T) {
	flagged := map[string]interface{}{"easyWin": "✓"}
	plain := map[string]interface{}{"easyWin": ""}
	if got := accentCell(flagged, "easyWin", "Title"); got != "\x1b[1;32mTitle\x1b[0m" {
		t.Errorf("accentCell(flagged) = %q, want green-wrapped", got)
	}
	if got := accentCell(plain, "easyWin", "Title"); got != "Title" {
		t.Errorf("accentCell(plain) = %q, want unchanged", got)
	}
}

func TestTableAccentConfigRespectsColorGate(t *testing.T) {
	t.Cleanup(func() {
		viper.Set("table-color", nil)
		viper.Set("table-accent-column", nil)
		viper.Set("table-accent-flag-key", nil)
	})
	viper.Set("table-accent-column", "title")
	viper.Set("table-accent-flag-key", "easyWin")

	viper.Set("table-color", false)
	if column, _ := tableAccentConfig(); column != "" {
		t.Errorf("accent column = %q, want disabled when color is off", column)
	}

	viper.Set("table-color", true)
	column, flagKey := tableAccentConfig()
	if column != "title" || flagKey != "easyWin" {
		t.Errorf("accent = %q/%q, want title/easyWin when color is on", column, flagKey)
	}
}

func TestTransformInsightsMachineFormatsKeepRawFields(t *testing.T) {
	resetInsightConfig(t)
	viper.Set("rsh-output-format", "json")
	body := insightsBody(insightRow(nil))
	root := transformSuccessBody(body).(map[string]interface{})
	row := root["results"].([]interface{})[0].(map[string]interface{})
	if _, present := row["provider"]; present {
		t.Error("provider derived for json output, want raw fields only")
	}
	if _, present := row["easyWin"]; present {
		t.Error("easyWin derived for json output, want raw fields only")
	}
	if cols := viper.GetString("table-columns"); cols != "" {
		t.Errorf("table-columns = %q, want unset for json output", cols)
	}
}

func TestTransformInsightsExplicitColumnsKeepRawFields(t *testing.T) {
	resetInsightConfig(t)
	viper.Set("table-columns", "key,title")
	body := insightsBody(insightRow(nil))
	root := transformSuccessBody(body).(map[string]interface{})
	row := root["results"].([]interface{})[0].(map[string]interface{})
	if _, present := row["provider"]; present {
		t.Error("provider derived despite explicit -C selection")
	}
	if cols := viper.GetString("table-columns"); cols != "key,title" {
		t.Errorf("table-columns = %q, want user selection preserved", cols)
	}
}

func TestTransformSortsReportRowsDeterministically(t *testing.T) {
	resetTransformConfig(t)
	shuffled := reportBody(
		[]interface{}{"beta", "2026", "07", 2.5, float64(1782864000)},
		[]interface{}{"alpha", "2026", "07", 1.5, float64(1782864000)},
		[]interface{}{"alpha", "2026", "06", 3.5, float64(1780272000)},
	)
	rows := transformedRows(t, transformSuccessBody(shuffled))
	got := []string{}
	for _, row := range rows {
		cells := row.([]interface{})
		got = append(got, fmt.Sprintf("%v-%v", cells[0], cells[2]))
	}
	want := []string{"alpha-06", "alpha-07", "beta-07"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted order = %v, want %v", got, want)
		}
	}
}

func TestTransformDropsNullGroupZeroMetricRows(t *testing.T) {
	resetTransformConfig(t)
	body := reportBody(
		[]interface{}{nil, "2026", "07", float64(0), float64(1782864000)},
		[]interface{}{"real", "2026", "07", 12.5, float64(1782864000)},
		[]interface{}{"zero-but-real-group", "2026", "07", float64(0), float64(1782864000)},
	)
	result := transformSuccessBody(body)
	rows := transformedRows(t, result)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (null-group zero row dropped)", len(rows))
	}
	container := result.(map[string]interface{})["result"].(map[string]interface{})
	if dropped, _ := container["emptyRowsDropped"].(int64); dropped != 1 {
		t.Errorf("emptyRowsDropped = %v, want 1", container["emptyRowsDropped"])
	}
}

func TestTransformKeepsEmptyRowsWhenRequested(t *testing.T) {
	resetTransformConfig(t)
	viper.Set("include-empty-rows", true)
	body := reportBody(
		[]interface{}{nil, "2026", "07", float64(0), float64(1782864000)},
		[]interface{}{"real", "2026", "07", 12.5, float64(1782864000)},
	)
	rows := transformedRows(t, transformSuccessBody(body))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 with --include-empty-rows", len(rows))
	}
}

func TestTransformCapsRowsInAgentMode(t *testing.T) {
	resetTransformConfig(t)
	agentMode = true
	oldStderr := cli.Stderr
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() { cli.Stderr = oldStderr })

	rows := make([][]interface{}, defaultAgentMaxRows+100)
	for i := range rows {
		rows[i] = []interface{}{fmt.Sprintf("svc-%04d", i), "2026", "07", float64(i) + 0.5, float64(1782864000)}
	}
	result := transformSuccessBody(reportBody(rows...))
	container := result.(map[string]interface{})["result"].(map[string]interface{})
	capped := container["rows"].([]interface{})
	if len(capped) != defaultAgentMaxRows {
		t.Fatalf("rows = %d, want %d", len(capped), defaultAgentMaxRows)
	}
	if omitted, _ := container["rowsOmitted"].(int64); omitted != 100 {
		t.Errorf("rowsOmitted = %v, want 100", container["rowsOmitted"])
	}
	if total, _ := container["rowsTotal"].(int64); total != int64(defaultAgentMaxRows+100) {
		t.Errorf("rowsTotal = %v, want %d", container["rowsTotal"], defaultAgentMaxRows+100)
	}
	if !strings.Contains(stderr.String(), "omitted") {
		t.Errorf("stderr = %q, want omission notice", stderr.String())
	}
}

func TestTransformPivotsCompleteRowsInAgentMode(t *testing.T) {
	resetTransformConfig(t)
	agentMode = true
	viper.Set("pivot-rows", true)

	rows := make([][]interface{}, defaultAgentMaxRows+100)
	for i := range rows {
		rows[i] = []interface{}{"svc", "2026", fmt.Sprintf("%02d", i%12+1), float64(1), float64(1782864000)}
	}
	result := transformSuccessBody(reportBody(rows...)).([]interface{})
	totals := result[len(result)-1].(map[string]interface{})
	if totals["total"] != float64(defaultAgentMaxRows+100) {
		t.Fatalf("pivot total = %v, want %d", totals["total"], defaultAgentMaxRows+100)
	}
}

func TestTransformMaxRowsZeroDisablesCap(t *testing.T) {
	resetTransformConfig(t)
	agentMode = true
	viper.Set("max-rows", 0)

	rows := make([][]interface{}, defaultAgentMaxRows+50)
	for i := range rows {
		rows[i] = []interface{}{fmt.Sprintf("svc-%04d", i), "2026", "07", float64(i) + 0.5, float64(1782864000)}
	}
	result := transformSuccessBody(reportBody(rows...))
	container := result.(map[string]interface{})["result"].(map[string]interface{})
	if got := len(container["rows"].([]interface{})); got != defaultAgentMaxRows+50 {
		t.Fatalf("rows = %d, want uncapped %d", got, defaultAgentMaxRows+50)
	}
	if _, present := container["rowsOmitted"]; present {
		t.Error("rowsOmitted present on uncapped result")
	}
}

func TestTransformHumanModeUncappedByDefault(t *testing.T) {
	resetTransformConfig(t)
	agentMode = false

	rows := make([][]interface{}, defaultAgentMaxRows+50)
	for i := range rows {
		rows[i] = []interface{}{fmt.Sprintf("svc-%04d", i), "2026", "07", float64(i) + 0.5, float64(1782864000)}
	}
	result := transformSuccessBody(reportBody(rows...))
	container := result.(map[string]interface{})["result"].(map[string]interface{})
	if got := len(container["rows"].([]interface{})); got != defaultAgentMaxRows+50 {
		t.Fatalf("rows = %d, want uncapped %d in human mode", got, defaultAgentMaxRows+50)
	}
}

func TestTransformKeyedRows(t *testing.T) {
	resetTransformConfig(t)
	viper.Set("rows-mode", "keyed")
	body := reportBody(
		[]interface{}{"svc", "2026", "07", 12.5, float64(1782864000)},
	)
	rows := transformedRows(t, transformSuccessBody(body))
	obj, ok := rows[0].(map[string]interface{})
	if !ok {
		t.Fatalf("row = %T, want keyed object", rows[0])
	}
	if obj["service_description"] != "svc" {
		t.Errorf("service_description = %v", obj["service_description"])
	}
	if cost, _ := obj["cost"].(float64); cost != 12.5 {
		t.Errorf("cost = %v, want 12.5", obj["cost"])
	}
}

func TestResponseGuardEmptyBody2xxIsSuccess(t *testing.T) {
	oldAgentMode := agentMode
	oldUAMode := agentUAMode
	agentMode = true
	agentUAMode = uaModeAgent
	t.Cleanup(func() {
		agentMode = oldAgentMode
		agentUAMode = oldUAMode
		resetErrorContractState()
		nonJSONErrorResponse = false
	})
	resetErrorContractState()
	nonJSONErrorResponse = false

	next := &recordingFormatter{}
	guard := dciResponseGuard{next: next}
	if err := guard.Format(cli.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "text/html"},
		Body:    "",
	}); err != nil {
		t.Fatal(err)
	}
	if nonJSONErrorResponse {
		t.Error("empty 200 body flagged as error")
	}
	if responseExitCode != 0 {
		t.Errorf("responseExitCode = %d, want 0", responseExitCode)
	}
	if agentErrorWritten {
		t.Error("error envelope written for a successful empty response")
	}
}

func TestResponseGuardClassifies4xxHTMLByStatus(t *testing.T) {
	oldAgentMode := agentMode
	oldUAMode := agentUAMode
	oldStderr := cli.Stderr
	agentMode = true
	agentUAMode = uaModeAgent
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		agentMode = oldAgentMode
		agentUAMode = oldUAMode
		cli.Stderr = oldStderr
		resetErrorContractState()
		nonJSONErrorResponse = false
	})
	resetErrorContractState()

	guard := dciResponseGuard{next: &recordingFormatter{}}
	if err := guard.Format(cli.Response{
		Status:  400,
		Headers: map[string]string{"Content-Type": "text/html"},
		Body:    "",
	}); err != nil {
		t.Fatal(err)
	}
	if responseExitCode != exitValidation {
		t.Errorf("responseExitCode = %d, want %d (validation)", responseExitCode, exitValidation)
	}
	var envelope structuredErrorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("stderr is not an error envelope: %q", stderr.String())
	}
	if envelope.Error.Code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", envelope.Error.Code)
	}
	if envelope.Error.Retryable {
		t.Error("4xx marked retryable")
	}
}

func TestAuthFailureHintNamesAPIKeyWhenSet(t *testing.T) {
	t.Setenv("DCI_API_KEY", "some-token")
	hint := authFailureHint(401)
	if !strings.Contains(hint, "DCI_API_KEY") {
		t.Errorf("hint = %q, want DCI_API_KEY mentioned", hint)
	}
}

func TestAuthFailureHintSuggestsLoginWithoutAPIKey(t *testing.T) {
	t.Setenv("DCI_API_KEY", "")
	hint := authFailureHint(401)
	if !strings.Contains(hint, "dci login") {
		t.Errorf("hint = %q, want dci login mentioned", hint)
	}
}

func TestAuthFailureHint403IncludesActiveContext(t *testing.T) {
	oldResolved := resolvedCustomerContext
	resolvedCustomerContext = "acme.com"
	t.Cleanup(func() { resolvedCustomerContext = oldResolved })
	hint := authFailureHint(403)
	if !strings.Contains(hint, "acme.com") {
		t.Errorf("hint = %q, want active context included", hint)
	}
}
