package main

// Curated list views: the declarative registry behind the default table/TOON
// presentation of list commands. Each entry names the response's items key,
// the display columns in order (with optional source-field renames and
// derived cells), an optional URL field hyperlinking the lead column, and an
// optional client-side sort. The generic applyListView drives every entry;
// transformInsightsList stays bespoke (dismissed filtering, savings ranking,
// dynamic column discovery) but shares setListViewConfig so there is exactly
// one place that pins a curated view for the renderers.
//
// Ground rules every view follows: presentation formats only (table, auto,
// toon), explicit -C/--fields selections and machine formats (json, yaml,
// csv) always see the raw response, and the column order is marked auto-set
// so terminal-width fitting may still trim overflow columns. Client-side
// sorting is declared only where the API's order is plain creation order
// (reports, allocations, alerts); it is deliberately absent where the server
// order is semantic — anomalies sort server-side (--sort-by/--sort-order)
// and budgets reorder by projected breach date under riskStatus:atRisk.
//
// Keep user-facing behavior changes here in sync with the help-center CLI
// docs (generated from omni's generate-cli-docs action; command notes live in
// omni .github/workflows/actions/generate-cli-docs/command-notes/).

import (
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

// viewContext carries per-invocation data resolved once for all rows.
type viewContext struct {
	folderNames map[string]string
}

type viewColumn struct {
	// title is the display column name (also the row key the cell lands on).
	title string
	// source is the response field backing the column; empty means the title
	// is itself the field name (no rename).
	source string
	// derive computes the cell from the whole row; when nil the source field
	// is mirrored under the title as-is.
	derive func(row map[string]interface{}, ctx *viewContext) interface{}
}

type listView struct {
	// itemsKey is the response field holding the list rows.
	itemsKey string
	columns  []viewColumn
	// linkURLKey names the row field whose URL becomes an OSC 8 hyperlink on
	// the lead column in interactive tables; empty disables linking.
	linkURLKey string
	// sortField names an epoch-milliseconds row field to sort by, newest
	// first; empty keeps the API's order.
	sortField string
	// needsFolders resolves folderId values to folder names with a single
	// folders-list call (reports, allocations).
	needsFolders bool
}

var listViews = map[string]listView{
	"list-reports": {
		itemsKey: "reports",
		columns: []viewColumn{
			{title: "report name", source: "reportName", derive: starredNameCell},
			{title: "owner"},
			{title: "updated (UTC)", source: "updateTime"},
			{title: "folder", source: "folderId", derive: folderCellFor("folderId")},
			{title: "labels", derive: labelNamesCell},
		},
		linkURLKey:   "urlUI",
		sortField:    "updateTime",
		needsFolders: true,
	},
	"list-budgets": {
		itemsKey: "budgets",
		columns: []viewColumn{
			{title: "budget name", source: "budgetName"},
			{title: "owner"},
			{title: "amount"},
			{title: "spend to date", source: "currentUtilization"},
			{title: "risk", source: "riskStatus"},
			{title: "updated (UTC)", source: "updateTime"},
		},
		linkURLKey: "url",
	},
	"list-allocations": {
		itemsKey: "allocations",
		columns: []viewColumn{
			{title: "name"},
			{title: "owner"},
			{title: "type"},
			{title: "folder", source: "folderId", derive: folderCellFor("folderId")},
			{title: "updated (UTC)", source: "updateTime"},
		},
		linkURLKey:   "urlUI",
		sortField:    "updateTime",
		needsFolders: true,
	},
	"list-folders": {
		itemsKey: "folders",
		columns: []viewColumn{
			{title: "name"},
			{title: "description"},
			{title: "parent folder", source: "parentFolderId", derive: folderCellFor("parentFolderId")},
		},
		needsFolders: true,
	},
	"list-users": {
		itemsKey: "users",
		columns: []viewColumn{
			{title: "email"},
			{title: "status"},
			{title: "last login (UTC)", source: "lastLogin"},
			{title: "mfa enrolled", source: "mfaEnrolled"},
		},
	},
	"list-roles": {
		itemsKey: "roles",
		columns: []viewColumn{
			{title: "name"},
			{title: "type"},
			{title: "description"},
		},
	},
	"list-annotations": {
		itemsKey: "annotations",
		columns: []viewColumn{
			{title: "content"},
			{title: "labels", derive: labelNamesCell},
			{title: "annotated (UTC)", source: "timestamp"},
		},
	},
	"list-cloud-incidents": {
		itemsKey: "incidents",
		columns: []viewColumn{
			{title: "title"},
			{title: "platform"},
			{title: "product"},
			{title: "status"},
			{title: "created (UTC)", source: "createTime"},
		},
	},
	"list-commitments": {
		itemsKey: "commitments",
		columns: []viewColumn{
			{title: "name"},
			{title: "provider", source: "cloudProvider"},
			{title: "commitment", source: "totalCommitmentValue", derive: moneyCell("totalCommitmentValue")},
			{title: "attainment", source: "totalCurrentAttainment", derive: moneyCell("totalCurrentAttainment")},
			{title: "forecast", source: "totalForecastValue", derive: moneyCell("totalForecastValue")},
			{title: "start", source: "startDate"},
			{title: "end", source: "endDate"},
		},
	},
	"list-cloudflows": {
		itemsKey: "items",
		columns: []viewColumn{
			{title: "name"},
			{title: "published"},
			{title: "trigger", source: "triggerType"},
			{title: "run status", source: "lastExecutionStatus"},
			{title: "last run (UTC)", source: "lastExecutedTime"},
			{title: "next run (UTC)", source: "nextRun"},
		},
	},
	"list-budget-suggestions": {
		itemsKey: "items",
		columns: []viewColumn{
			{title: "name"},
			{title: "amount", derive: suggestionAmountCell},
			{title: "confidence"},
			{title: "interval", source: "timeInterval"},
			{title: "status"},
		},
	},
	"list-datahub-datasets": {
		itemsKey: "datasets",
		columns: []viewColumn{
			{title: "name"},
			{title: "updated (UTC)", source: "lastUpdated"},
			{title: "updated by", source: "updatedBy"},
		},
	},
	"list-anomalies": {
		itemsKey: "anomalies",
		columns: []viewColumn{
			{title: "service", source: "serviceName"},
			{title: "severity", source: "severityLevel"},
			{title: "anomaly cost", source: "costOfAnomaly"},
			{title: "platform"},
			{title: "status"},
			{title: "started (UTC)", source: "startTime"},
		},
	},
	"list-alerts": {
		itemsKey: "alerts",
		columns: []viewColumn{
			{title: "name"},
			{title: "owner"},
			{title: "last alerted (UTC)", source: "lastAlerted", derive: epochCell("lastAlerted")},
			{title: "updated (UTC)", source: "updateTime"},
		},
		sortField: "updateTime",
	},
	"list-invoices": {
		itemsKey: "invoices",
		columns: []viewColumn{
			{title: "invoice", source: "id"},
			{title: "platform"},
			{title: "issued", source: "invoiceDate", derive: epochCell("invoiceDate")},
			{title: "due", source: "dueDate", derive: epochCell("dueDate")},
			{title: "total", source: "totalAmount"},
			{title: "balance", source: "balanceAmount"},
			{title: "status"},
		},
		linkURLKey: "url",
	},
	"list-assets": {
		itemsKey: "assets",
		columns: []viewColumn{
			{title: "name"},
			{title: "type"},
			{title: "created (UTC)", source: "createTime"},
		},
		linkURLKey: "url",
	},
	"list-labels": {
		itemsKey: "labels",
		columns: []viewColumn{
			{title: "name"},
			{title: "type"},
			{title: "color"},
			{title: "updated (UTC)", source: "updateTime"},
		},
	},
	"list-tickets": {
		itemsKey: "tickets",
		columns: []viewColumn{
			{title: "subject"},
			{title: "status"},
			{title: "priority"},
			{title: "updated (UTC)", source: "updated_at"},
		},
	},
}

// applyListView applies the invoked command's registered view, if any:
// sort, per-row derived cells and renames, then the renderer config. Any row
// that is not an object leaves the whole response raw.
func applyListView(body interface{}) interface{} {
	view, ok := listViews[invokedCommandName]
	if !ok || !presentationView() {
		return body
	}
	root, ok := body.(map[string]interface{})
	if !ok {
		return body
	}
	items, ok := root[view.itemsKey].([]interface{})
	if !ok || len(items) == 0 {
		return body
	}
	rows := make([]map[string]interface{}, len(items))
	for i, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok {
			return body
		}
		rows[i] = row
	}

	if view.sortField != "" {
		sortRowsByEpochDesc(items, view.sortField)
	}

	ctx := &viewContext{}
	if view.needsFolders {
		ctx.folderNames = resolveFolderNames(items)
	}
	for _, row := range rows {
		for _, column := range view.columns {
			if column.derive != nil {
				row[column.title] = column.derive(row, ctx)
				continue
			}
			if column.source != "" && column.source != column.title {
				row[column.title] = row[column.source]
			}
		}
	}

	titles := make([]string, len(view.columns))
	for i, column := range view.columns {
		titles[i] = column.title
	}
	linkColumn := ""
	if view.linkURLKey != "" {
		linkColumn = titles[0]
	}
	setListViewConfig(titles, titles[0], linkColumn, view.linkURLKey, "", "")
	// Point the table renderer at the curated items field: discovery by array
	// size can pick a sideloaded secondary array instead (tickets vs users).
	viper.Set("table-items-key", view.itemsKey)
	return body
}

// setListViewConfig pins a curated view for the table/TOON renderers: the
// column order (marked auto-set so terminal-width fitting may trim overflow),
// the width-priority column, and the optional lead-column hyperlink and
// accent. Empty column names leave the respective feature off.
func setListViewConfig(columns []string, priorityColumn, linkColumn, linkURLKey, accentColumn, accentFlagKey string) {
	viper.Set("table-columns", strings.Join(columns, ","))
	viper.Set("table-columns-auto", true)
	viper.Set("table-priority-column", priorityColumn)
	if linkColumn != "" && linkURLKey != "" {
		viper.Set("table-link-column", linkColumn)
		viper.Set("table-link-url-key", linkURLKey)
	}
	if accentColumn != "" && accentFlagKey != "" {
		viper.Set("table-accent-column", accentColumn)
		viper.Set("table-accent-flag-key", accentFlagKey)
	}
}

// sortRowsByEpochDesc orders rows by an epoch-milliseconds field, newest
// first, so a visible time column is also the view's sort key. Stable: rows
// sharing the field value keep the API's order.
func sortRowsByEpochDesc(items []interface{}, field string) {
	sort.SliceStable(items, func(i, j int) bool {
		return epochField(items[i], field) > epochField(items[j], field)
	})
}

func epochField(item interface{}, field string) float64 {
	row, ok := item.(map[string]interface{})
	if !ok {
		return 0
	}
	value, _ := numericCell(row[field])
	return value
}

// epochCell mirrors an epoch-milliseconds field, blanking absent and
// non-positive values: the API encodes "no date" as zero or Go's zero time
// (-62135596800000 ms, seen on invoice credit memos), which would otherwise
// render as a huge negative number.
func epochCell(source string) func(map[string]interface{}, *viewContext) interface{} {
	return func(row map[string]interface{}, _ *viewContext) interface{} {
		if ms, ok := numericCell(row[source]); !ok || ms <= 0 {
			return ""
		}
		return row[source]
	}
}

// starredNameCell renders the report name, ★-prefixed when the row is
// starred. The public API exposes no starring on reports today (absent from
// the spec as of 2026-08); when a starred field appears on list rows the
// marker lights up without further changes here.
func starredNameCell(row map[string]interface{}, _ *viewContext) interface{} {
	name, _ := row["reportName"].(string)
	if reportStarred(row) {
		return "★ " + name
	}
	return name
}

func reportStarred(row map[string]interface{}) bool {
	switch v := row["starred"].(type) {
	case bool:
		return v
	case string:
		return strings.TrimSpace(v) != ""
	}
	return false
}

const foldersListPath = "/analytics/v1/folders"
const rootFolderID = "root"

// folderIDFields are the row fields that may hold a folder id needing name
// resolution (reports/allocations vs the folders list's own parent column).
var folderIDFields = []string{"folderId", "parentFolderId"}

// resolveFolderNames resolves folder ids to names with a single folders-list
// call, skipped entirely when every row sits at the top level. Lookup
// failures are non-fatal: cells fall back to the raw folder id.
func resolveFolderNames(rows []interface{}) map[string]string {
	needed := false
	for _, item := range rows {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		for _, field := range folderIDFields {
			if id, _ := row[field].(string); id != "" && id != rootFolderID {
				needed = true
			}
		}
		if needed {
			break
		}
	}
	if !needed {
		return nil
	}
	result, err := resolverListFetch(foldersListPath, activeCustomerContext(), resolverMaxPages)
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(result.entries))
	for _, entry := range result.entries {
		names[entry.ID] = entry.Name
	}
	return names
}

// folderCellFor renders a folder column from the given id field: top-level
// rows get a blank cell rather than a column of "root", resolved ids show the
// folder name, and unresolved ids stay visible as-is.
func folderCellFor(field string) func(map[string]interface{}, *viewContext) interface{} {
	return func(row map[string]interface{}, ctx *viewContext) interface{} {
		id, _ := row[field].(string)
		if id == "" || id == rootFolderID {
			return ""
		}
		if name, ok := ctx.folderNames[id]; ok && name != "" {
			return name
		}
		return id
	}
}

// moneyCell renders a monetary field with the row's currency symbol, for
// display titles the money-name heuristic doesn't cover (the commitments
// view's commitment/attainment/forecast columns). --raw-numbers keeps the
// plain value, matching renderCellText's behavior for money-named columns.
func moneyCell(source string) func(map[string]interface{}, *viewContext) interface{} {
	return func(row map[string]interface{}, _ *viewContext) interface{} {
		amount, ok := numericCell(row[source])
		currency, _ := row["currency"].(string)
		if !ok || currency == "" || viper.GetBool("raw-numbers") {
			return row[source]
		}
		return formatMoney(amount, currency)
	}
}

// suggestionAmountCell folds a budget suggestion's amount object
// ({"amount": "3120", "currency": "USD"}) into one money cell; anything
// unexpected falls back to the raw inner amount.
func suggestionAmountCell(row map[string]interface{}, _ *viewContext) interface{} {
	amount, ok := row["amount"].(map[string]interface{})
	if !ok {
		return row["amount"]
	}
	raw, _ := amount["amount"].(string)
	currency, _ := amount["currency"].(string)
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || currency == "" || viper.GetBool("raw-numbers") {
		return amount["amount"]
	}
	return formatMoney(value, currency)
}

// labelNamesCell folds the labels array ({id, name} objects) into the
// comma-joined label names shown in table and TOON cells.
func labelNamesCell(row map[string]interface{}, _ *viewContext) interface{} {
	items, ok := row["labels"].([]interface{})
	if !ok {
		return ""
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if label, ok := item.(map[string]interface{}); ok {
			if name, _ := label["name"].(string); name != "" {
				names = append(names, name)
			}
		}
	}
	return strings.Join(names, ", ")
}
