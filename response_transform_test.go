package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

func resetTransformConfig(t *testing.T) {
	t.Helper()
	oldAgentMode := agentMode
	// Full-pipeline tests leave the PreRun-resolved display zone behind
	// (production resets it in run()); transform tests must see the
	// deterministic UTC fallback unless they opt into a zone themselves.
	oldDisplayLoc := displayTimeLocation
	displayTimeLocation = nil
	t.Cleanup(func() {
		agentMode = oldAgentMode
		displayTimeLocation = oldDisplayLoc
		viper.Set("max-rows", nil)
		viper.Set("rows-mode", nil)
		viper.Set("pivot-rows", nil)
		viper.Set("pivot-active", nil)
		viper.Set("include-empty-rows", nil)
		viper.Set("drop-unlabeled-rows", nil)
		viper.Set("table-columns", nil)
	})
	viper.Set("max-rows", -1)
	viper.Set("rows-mode", "")
	viper.Set("pivot-rows", false)
	viper.Set("pivot-active", false)
	viper.Set("include-empty-rows", false)
	viper.Set("drop-unlabeled-rows", false)
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
		viper.Set("table-link-column", nil)
		viper.Set("table-link-url-key", nil)
	})
	invokedCommandName = "list-insights"
	viper.Set("include-dismissed", false)
	viper.Set("rsh-output-format", "table")
	viper.Set("table-columns-auto", false)
	viper.Set("table-priority-column", "")
	viper.Set("table-accent-column", "")
	viper.Set("table-accent-flag-key", "")
	viper.Set("table-link-column", "")
	viper.Set("table-link-url-key", "")
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
		"summary":                map[string]interface{}{"potentialDailySavings": float64(0)},
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

func TestApplyListSearchFiltersItems(t *testing.T) {
	viper.Set("list-search", "GenAI")
	t.Cleanup(func() { viper.Set("list-search", nil) })

	body := map[string]interface{}{
		"rowCount": int64(3),
		"items": []interface{}{
			map[string]interface{}{"id": "genai/model", "label": "Model", "type": "system_label"},
			map[string]interface{}{"id": "service_description", "label": "Service", "type": "fixed"},
			map[string]interface{}{"id": "team", "label": "Team tag", "labels": map[string]interface{}{"topic": "genai"}},
		},
	}
	result := applyListSearch(body).(map[string]interface{})
	items := result["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	if result["searchDropped"] != int64(1) || result["rowCount"] != int64(2) {
		t.Fatalf("markers = %v / %v", result["searchDropped"], result["rowCount"])
	}
}

func TestApplyListSearchNoOps(t *testing.T) {
	t.Cleanup(func() { viper.Set("list-search", nil) })

	// No search term set: untouched.
	viper.Set("list-search", "")
	body := map[string]interface{}{"items": []interface{}{map[string]interface{}{"id": "a"}}}
	if result := applyListSearch(body).(map[string]interface{}); len(result["items"].([]interface{})) != 1 {
		t.Fatal("empty search term filtered items")
	}

	// Everything matches: no markers added.
	viper.Set("list-search", "a")
	result := applyListSearch(body).(map[string]interface{})
	if _, present := result["searchDropped"]; present {
		t.Fatal("all-match search added a searchDropped marker")
	}

	// Not a list wrapper (a report result): untouched.
	report := map[string]interface{}{"result": map[string]interface{}{"rows": []interface{}{}}}
	if applyListSearch(report).(map[string]interface{})["searchDropped"] != nil {
		t.Fatal("non-list body grew a searchDropped marker")
	}
}

func TestApplyListSearchRunsBeforeInsightPresentation(t *testing.T) {
	// The " (easy win)" title suffix and other derived fields are injected by
	// the insights presentation step; --search must match raw API fields only,
	// or the match set would change with the output format.
	resetInsightConfig(t)
	viper.Set("list-search", "easy win")
	t.Cleanup(func() { viper.Set("list-search", nil) })

	body := insightsBody(
		insightRow(map[string]interface{}{"easyWinDescription": "Quick fix"}),
		insightRow(nil),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	if rows := root["results"].([]interface{}); len(rows) != 0 {
		t.Fatalf("search matched presentation-derived text: %v", rows)
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
	if first["title"] != "Purchase reserved instances (easy win)" {
		t.Errorf("title = %v, want the (easy win) suffix", first["title"])
	}
	second := rows[1].(map[string]interface{})
	if second["easyWin"] != "" {
		t.Errorf("easyWin = %v, want empty for empty easyWinDescription", second["easyWin"])
	}
	if second["title"] != "Purchase reserved instances" {
		t.Errorf("title = %v, want unsuffixed for non easy wins", second["title"])
	}

	want := "title,dailySavings,provider,categories,lastUpdated,source"
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
	if got := viper.GetString("table-link-column"); got != "title" {
		t.Errorf("table-link-column = %q, want title", got)
	}
	if got := viper.GetString("table-link-url-key"); got != "reportUrl" {
		t.Errorf("table-link-url-key = %q, want reportUrl", got)
	}
}

func resetReportsListConfig(t *testing.T) {
	t.Helper()
	resetTransformConfig(t)
	oldCommand := invokedCommandName
	oldFetch := resolverListFetch
	t.Cleanup(func() {
		invokedCommandName = oldCommand
		resolverListFetch = oldFetch
		viper.Set("rsh-output-format", nil)
		viper.Set("table-columns-auto", nil)
		viper.Set("table-priority-column", nil)
		viper.Set("table-link-column", nil)
		viper.Set("table-link-url-key", nil)
	})
	invokedCommandName = "list-reports"
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		t.Fatalf("unexpected folder lookup on %s (all reports at top level)", listPath)
		return resolverListResult{}, nil
	}
	viper.Set("rsh-output-format", "table")
	viper.Set("table-columns-auto", false)
	viper.Set("table-priority-column", "")
	viper.Set("table-link-column", "")
	viper.Set("table-link-url-key", "")
}

func reportsListRow(overrides map[string]interface{}) map[string]interface{} {
	row := map[string]interface{}{
		"createTime": int64(1787065375365),
		"folderId":   "root",
		"id":         "qPA5QvltVGvhlSUiNw3O",
		"labels":     []interface{}{},
		"owner":      "someone@example.com",
		"reportName": "BQ storage data",
		"type":       "custom",
		"updateTime": int64(1787065375365),
		"urlUI":      "https://console.doit.com/customers/x/analyze/reports/q",
	}
	for k, v := range overrides {
		row[k] = v
	}
	return row
}

func reportsListBody(rows ...map[string]interface{}) map[string]interface{} {
	items := make([]interface{}, len(rows))
	for i, row := range rows {
		items[i] = row
	}
	return map[string]interface{}{
		"pageToken": "",
		"reports":   items,
	}
}

func TestTransformReportsCuratesDefaultTableView(t *testing.T) {
	resetReportsListConfig(t)
	body := reportsListBody(reportsListRow(map[string]interface{}{
		"labels": []interface{}{
			map[string]interface{}{"id": "l1", "name": "House ANA"},
			map[string]interface{}{"id": "l2", "name": "Analytics"},
		},
	}))
	root := transformSuccessBody(body).(map[string]interface{})
	row := root["reports"].([]interface{})[0].(map[string]interface{})

	if row["report name"] != "BQ storage data" {
		t.Errorf("report name = %v, want the reportName", row["report name"])
	}
	if row["updated (UTC)"] != int64(1787065375365) {
		t.Errorf("updated (UTC) = %v, want updateTime mirrored", row["updated (UTC)"])
	}
	if row["folder"] != "" {
		t.Errorf("folder = %v, want blank for root", row["folder"])
	}
	if row["labels"] != "House ANA, Analytics" {
		t.Errorf("labels = %v, want comma-joined label names", row["labels"])
	}

	if cols := viper.GetString("table-columns"); cols != "report name,owner,updated (UTC),folder,labels" {
		t.Errorf("table-columns = %q, want the curated order", cols)
	}
	if !viper.GetBool("table-columns-auto") {
		t.Error("table-columns-auto = false, want true (order stays fit-eligible)")
	}
	if got := viper.GetString("table-priority-column"); got != "report name" {
		t.Errorf("table-priority-column = %q, want report name", got)
	}
	if got := viper.GetString("table-link-column"); got != "report name" {
		t.Errorf("table-link-column = %q, want report name", got)
	}
	if got := viper.GetString("table-link-url-key"); got != "urlUI" {
		t.Errorf("table-link-url-key = %q, want urlUI", got)
	}
}

func TestTransformReportsResolvesFolderNames(t *testing.T) {
	resetReportsListConfig(t)
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		if listPath != foldersListPath {
			t.Fatalf("listPath = %q, want %q", listPath, foldersListPath)
		}
		return resolverListResult{entries: []nameCacheEntry{{ID: "T0bkYjXi5fOfFNiF5Zhf", Name: "House ANA"}}}, nil
	}
	body := reportsListBody(
		reportsListRow(map[string]interface{}{"folderId": "T0bkYjXi5fOfFNiF5Zhf"}),
		reportsListRow(map[string]interface{}{"folderId": "UnknownFolderId000000"}),
		reportsListRow(nil),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	rows := root["reports"].([]interface{})
	if folder := rows[0].(map[string]interface{})["folder"]; folder != "House ANA" {
		t.Errorf("folder = %v, want the resolved folder name", folder)
	}
	if folder := rows[1].(map[string]interface{})["folder"]; folder != "UnknownFolderId000000" {
		t.Errorf("folder = %v, want the raw id for unresolved folders", folder)
	}
	if folder := rows[2].(map[string]interface{})["folder"]; folder != "" {
		t.Errorf("folder = %v, want blank for root", folder)
	}
}

func TestTransformReportsFolderLookupFailureFallsBack(t *testing.T) {
	resetReportsListConfig(t)
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		return resolverListResult{}, fmt.Errorf("network down")
	}
	body := reportsListBody(reportsListRow(map[string]interface{}{"folderId": "T0bkYjXi5fOfFNiF5Zhf"}))
	root := transformSuccessBody(body).(map[string]interface{})
	row := root["reports"].([]interface{})[0].(map[string]interface{})
	if row["folder"] != "T0bkYjXi5fOfFNiF5Zhf" {
		t.Errorf("folder = %v, want the raw id when the lookup fails", row["folder"])
	}
}

func TestTransformReportsStarredMarker(t *testing.T) {
	resetReportsListConfig(t)
	body := reportsListBody(
		reportsListRow(map[string]interface{}{"starred": true}),
		reportsListRow(nil),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	rows := root["reports"].([]interface{})
	if name := rows[0].(map[string]interface{})["report name"]; name != "★ BQ storage data" {
		t.Errorf("report name = %v, want the ★ prefix for starred rows", name)
	}
	if name := rows[1].(map[string]interface{})["report name"]; name != "BQ storage data" {
		t.Errorf("report name = %v, want no prefix for unstarred rows", name)
	}
}

func TestTransformReportsSortsByUpdatedDescending(t *testing.T) {
	resetReportsListConfig(t)
	body := reportsListBody(
		reportsListRow(map[string]interface{}{"id": "stale0000000000000000", "updateTime": int64(1780000000000)}),
		reportsListRow(map[string]interface{}{"id": "fresh0000000000000000", "updateTime": int64(1787000000000)}),
		reportsListRow(map[string]interface{}{"id": "middle000000000000000", "updateTime": int64(1783000000000)}),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	got := []string{}
	for _, item := range root["reports"].([]interface{}) {
		got = append(got, item.(map[string]interface{})["id"].(string))
	}
	want := []string{"fresh0000000000000000", "middle000000000000000", "stale0000000000000000"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted ids = %v, want %v", got, want)
		}
	}
}

func TestTransformReportsMachineFormatsKeepRawFields(t *testing.T) {
	resetReportsListConfig(t)
	viper.Set("rsh-output-format", "json")
	body := reportsListBody(
		reportsListRow(map[string]interface{}{"id": "stale0000000000000000", "updateTime": int64(1780000000000)}),
		reportsListRow(map[string]interface{}{"id": "fresh0000000000000000", "updateTime": int64(1787000000000)}),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	rows := root["reports"].([]interface{})
	row := rows[0].(map[string]interface{})
	if _, present := row["report name"]; present {
		t.Error("report name derived for machine formats, want raw fields only")
	}
	if row["id"] != "stale0000000000000000" {
		t.Errorf("first id = %v, want the API's order kept for machine formats", row["id"])
	}
	if cols := viper.GetString("table-columns"); cols != "" {
		t.Errorf("table-columns = %q, want unset for machine formats", cols)
	}
}

func TestTransformReportsExplicitColumnsKeepRawFields(t *testing.T) {
	resetReportsListConfig(t)
	viper.Set("table-columns", "id,owner")
	body := reportsListBody(reportsListRow(nil))
	root := transformSuccessBody(body).(map[string]interface{})
	row := root["reports"].([]interface{})[0].(map[string]interface{})
	if _, present := row["report name"]; present {
		t.Error("report name derived despite explicit -C selection, want raw fields only")
	}
	if cols := viper.GetString("table-columns"); cols != "id,owner" {
		t.Errorf("table-columns = %q, want the user's selection untouched", cols)
	}
}

func TestTransformInsightsSortsBySavingsDescending(t *testing.T) {
	resetInsightConfig(t)
	viper.Set("rsh-output-format", "json") // sorting applies to machine formats too
	body := insightsBody(
		insightRow(map[string]interface{}{"key": "none"}),
		insightRow(map[string]interface{}{"key": "big", "summary": map[string]interface{}{"potentialDailySavings": 50.92}}),
		insightRow(map[string]interface{}{"key": "small", "summary": map[string]interface{}{"potentialDailySavings": 1.24}}),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	rows := root["results"].([]interface{})
	got := []string{}
	for _, item := range rows {
		got = append(got, item.(map[string]interface{})["key"].(string))
	}
	want := []string{"big", "small", "none"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted keys = %v, want %v", got, want)
		}
	}
}

func TestTransformInsightsDailySavingsColumn(t *testing.T) {
	resetInsightConfig(t)
	body := insightsBody(
		insightRow(map[string]interface{}{"summary": map[string]interface{}{"potentialDailySavings": 8.98765}}),
		insightRow(nil),
	)
	root := transformSuccessBody(body).(map[string]interface{})
	rows := root["results"].([]interface{})
	first := rows[0].(map[string]interface{})
	if first["dailySavings"] != "$8.99" {
		t.Errorf("dailySavings = %v, want $8.99 (USD, rounded to cents)", first["dailySavings"])
	}
	second := rows[1].(map[string]interface{})
	if _, present := second["dailySavings"]; present {
		t.Errorf("dailySavings = %v, want absent for zero savings (blank cell)", second["dailySavings"])
	}
}

func TestFormatUSD(t *testing.T) {
	cases := map[float64]string{
		500:     "$500.00",
		8.98765: "$8.99",
		9.999:   "$10.00",
		1234.5:  "$1,234.50",
		0.01:    "$0.01",
	}
	for amount, want := range cases {
		if got := formatUSD(amount); got != want {
			t.Errorf("formatUSD(%v) = %q, want %q", amount, got, want)
		}
	}
}

func TestMoneyTextAlignment(t *testing.T) {
	for val, want := range map[string]bool{
		"$8.99":     true,
		"$1,234.50": true,
		"-$3.20":    true,
		"plain":     false,
		"$":         false,
		"$notmoney": false,
	} {
		if got := moneyText(val); got != want {
			t.Errorf("moneyText(%q) = %v, want %v", val, got, want)
		}
	}
	if moneyText(8.99) {
		t.Error("moneyText(number) = true, want false (numbers use the numeric path)")
	}
}

func TestTableLinksMarkOnlyRowsWithURL(t *testing.T) {
	links := &tableLinks{}
	linked := map[string]interface{}{"reportUrl": "https://example.com/r/1"}
	plain := map[string]interface{}{"reportUrl": ""}
	if got := links.mark(linked, "reportUrl", "Title"); got != linkStartMarker+"Title"+linkEndMarker {
		t.Errorf("mark(linked) = %q, want marker-wrapped", got)
	}
	if got := links.mark(plain, "reportUrl", "Other"); got != "Other" {
		t.Errorf("mark(plain) = %q, want unchanged", got)
	}
	out := links.apply("| " + linkStartMarker + "Title" + linkEndMarker + " |")
	want := "| \x1b]8;;https://example.com/r/1\x1b\\Title\x1b]8;;\x1b\\ |"
	if out != want {
		t.Errorf("apply = %q, want %q", out, want)
	}
}

func TestTableLinksWrapModeMarksEachLine(t *testing.T) {
	links := &tableLinks{}
	row := map[string]interface{}{"urlUI": "https://example.com/r/2"}
	marked := links.mark(row, "urlUI", "first\nsecond")
	if len(links.urls) != 2 {
		t.Fatalf("urls queued = %d, want 2 (one per wrapped line)", len(links.urls))
	}
	out := links.apply(marked)
	if strings.Count(out, "\x1b]8;;https://example.com/r/2\x1b\\") != 2 {
		t.Errorf("apply = %q, want each wrapped line hyperlinked separately", out)
	}
	if strings.Contains(out, linkStartMarker) || strings.Contains(out, linkEndMarker) {
		t.Errorf("apply = %q, want no marker runes left", out)
	}
}

// The OSC 8 escape sequences must never pass through simpletable: its column
// sizing strips CSI color codes but not OSC sequences, so an in-cell URL
// would inflate the measured width and shear the table borders (the original
// list-reports hyperlink bug).
func TestBuildTableStringHyperlinksPreserveAlignment(t *testing.T) {
	t.Cleanup(func() {
		viper.Set("table-color", nil)
		viper.Set("table-link-column", nil)
		viper.Set("table-link-url-key", nil)
	})
	viper.Set("table-color", true)
	viper.Set("table-link-column", "name")
	viper.Set("table-link-url-key", "urlUI")
	url := "https://console.example.com/customers/RSTDkHhaoGWwOEvlYlHyBUhm/analyze/reports/qPA5QvltVGvhlSUiNw3O"
	rows := []map[string]interface{}{
		{"name": "Anomaly Detection Dev WoW", "owner": "someone@example.com", "urlUI": url},
		{"name": "Spend by User", "owner": "other@example.com", "urlUI": ""},
	}
	out, err := buildTableString(rows, []string{"name", "owner"}, []int{25, 19}, "fit")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\x1b]8;;"+url+"\x1b\\Anomaly Detection Dev WoW\x1b]8;;\x1b\\") {
		t.Errorf("output missing the OSC 8 hyperlink:\n%s", out)
	}
	if strings.Contains(out, linkStartMarker) || strings.Contains(out, linkEndMarker) {
		t.Error("marker runes leaked into the rendered table")
	}
	oscPattern := regexp.MustCompile(`\x1b]8;;[^\x1b]*\x1b\\`)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := runewidth.StringWidth(lines[0])
	for _, line := range lines {
		if got := runewidth.StringWidth(oscPattern.ReplaceAllString(line, "")); got != want {
			t.Errorf("line visible width = %d, want %d (borders sheared): %q", got, want, line)
		}
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

func TestTransformDropsUnlabeledRowsOnRequest(t *testing.T) {
	resetTransformConfig(t)
	viper.Set("drop-unlabeled-rows", true)
	oldBody := bufferedRequestBody
	bufferedRequestBody = nil
	t.Cleanup(func() { bufferedRequestBody = oldBody })
	body := reportBody(
		[]interface{}{nil, "2026", "07", float64(418722), float64(1782864000)},
		[]interface{}{"[Value N/A]", "2026", "07", 5.0, float64(1782864000)},
		[]interface{}{"labeled", "2026", "07", 12.5, float64(1782864000)},
	)
	result := transformSuccessBody(body)
	rows := transformedRows(t, result)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (null and [Value N/A] groups dropped despite nonzero cost)", len(rows))
	}
	container := result.(map[string]interface{})["result"].(map[string]interface{})
	if dropped, _ := container["unlabeledRowsDropped"].(int64); dropped != 2 {
		t.Errorf("unlabeledRowsDropped = %v, want 2", container["unlabeledRowsDropped"])
	}
}

// Providers label mutually exclusive subsets: with several labels grouped,
// only the all-null row is the unlabeled bucket. Rows carrying any one label
// must survive.
func TestTransformDropsOnlyAllNullRowsAcrossMultipleLabels(t *testing.T) {
	resetTransformConfig(t)
	viper.Set("drop-unlabeled-rows", true)
	oldBody := bufferedRequestBody
	bufferedRequestBody = []byte(`{"config":{"group":[
		{"id":"genai/model","type":"system_label"},
		{"id":"genai/cost_type","type":"system_label"}
	]}}`)
	t.Cleanup(func() { bufferedRequestBody = oldBody })

	body := map[string]interface{}{
		"result": map[string]interface{}{
			"rows": []interface{}{
				[]interface{}{nil, nil, "2026", "07", float64(418722)},
				[]interface{}{"Claude Opus 4.8", "tokens", "2026", "07", 61384.0},
				[]interface{}{"gpt-5.6-sol", nil, "2026", "07", 7487.0},
				[]interface{}{nil, "tokens", "2026", "07", 5.0},
			},
			"schema": []interface{}{
				map[string]interface{}{"name": "genai/model", "type": "string"},
				map[string]interface{}{"name": "genai/cost_type", "type": "string"},
				map[string]interface{}{"name": "year", "type": "string"},
				map[string]interface{}{"name": "month", "type": "string"},
				map[string]interface{}{"name": "cost", "type": "float"},
			},
		},
	}
	result := transformSuccessBody(body)
	rows := transformedRows(t, result)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (only the all-null bucket dropped)", len(rows))
	}
	container := result.(map[string]interface{})["result"].(map[string]interface{})
	if dropped, _ := container["unlabeledRowsDropped"].(int64); dropped != 1 {
		t.Errorf("unlabeledRowsDropped = %v, want 1", container["unlabeledRowsDropped"])
	}
}

func TestTransformKeepsUnlabeledRowsByDefault(t *testing.T) {
	resetTransformConfig(t)
	body := reportBody(
		[]interface{}{nil, "2026", "07", float64(418722), float64(1782864000)},
		[]interface{}{"labeled", "2026", "07", 12.5, float64(1782864000)},
	)
	result := transformSuccessBody(body)
	if rows := transformedRows(t, result); len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (nonzero null bucket kept without the flag)", len(rows))
	}
	container := result.(map[string]interface{})["result"].(map[string]interface{})
	if _, present := container["unlabeledRowsDropped"]; present {
		t.Error("unlabeledRowsDropped marker present without the flag")
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

func TestTransformRollupAggregatesRows(t *testing.T) {
	// --rollup: group by the requested columns, sum numeric metrics, drop
	// everything else (per-period timestamps included) — "total per X over
	// the period" without pushing row-level arithmetic onto the consumer.
	resetTransformConfig(t)
	viper.Set("report-rollup", "service_description")
	body := reportBody(
		[]interface{}{"BigQuery", "2026", "05", 10.0, float64(1778000000)},
		[]interface{}{"BigQuery", "2026", "06", 5.5, float64(1780000000)},
		[]interface{}{"GCS", "2026", "05", 2.0, float64(1778000000)},
		[]interface{}{"BigQuery", "2026", "07", 4.5, float64(1782864000)},
	)
	root := transformSuccessBody(body).(map[string]interface{})
	container := root["result"].(map[string]interface{})
	rows := container["rows"].([]interface{})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 rolled-up rows: %v", len(rows), rows)
	}
	if got := container["rolledUpFrom"]; got != int64(4) {
		t.Fatalf("rolledUpFrom = %v", got)
	}
	byService := map[string]float64{}
	for _, row := range rows {
		cells := row.([]interface{})
		if len(cells) != 2 {
			t.Fatalf("rolled row width = %d, want key+sum: %v", len(cells), cells)
		}
		value, _ := numericCell(cells[1])
		byService[cells[0].(string)] = value
	}
	if byService["BigQuery"] != 20.0 || byService["GCS"] != 2.0 {
		t.Fatalf("sums = %v", byService)
	}
	schema := container["schema"].([]interface{})
	if len(schema) != 2 {
		t.Fatalf("rolled schema = %v", schema)
	}
}

func TestTransformRollupUnknownColumnSelfCorrects(t *testing.T) {
	// A typo'd column must not silently return unaggregated data as if it
	// were rolled up: the rows pass through untouched and rollupError names
	// the valid columns so an agent can fix the call in one step.
	resetTransformConfig(t)
	viper.Set("report-rollup", "servcie")
	body := reportBody(
		[]interface{}{"BigQuery", "2026", "05", 10.0, float64(1778000000)},
		[]interface{}{"GCS", "2026", "05", 2.0, float64(1778000000)},
	)
	root := transformSuccessBody(body).(map[string]interface{})
	container := root["result"].(map[string]interface{})
	if len(container["rows"].([]interface{})) != 2 {
		t.Fatalf("rows changed despite unknown rollup column")
	}
	message, _ := container["rollupError"].(string)
	if !strings.Contains(message, "servcie") || !strings.Contains(message, "service_description") {
		t.Fatalf("rollupError = %q", message)
	}
}
