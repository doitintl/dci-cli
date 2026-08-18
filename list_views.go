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
// sorting is declared only where the API's order is plain creation order;
// it is deliberately absent where the server order is semantic.
//
// Keep user-facing behavior changes here in sync with the help-center CLI
// docs (generated from omni's generate-cli-docs action; command notes live in
// omni .github/workflows/actions/generate-cli-docs/command-notes/).

import (
	"sort"
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
			{title: "folder", source: "folderId", derive: folderNameCell},
			{title: "labels", derive: labelNamesCell},
		},
		linkURLKey:   "urlUI",
		sortField:    "updateTime",
		needsFolders: true,
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
		if id, _ := row["folderId"].(string); id != "" && id != rootFolderID {
			needed = true
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

// folderNameCell renders the folder column: top-level rows get a blank cell
// rather than a column of "root", resolved ids show the folder name, and
// unresolved ids stay visible as-is.
func folderNameCell(row map[string]interface{}, ctx *viewContext) interface{} {
	id, _ := row["folderId"].(string)
	if id == "" || id == rootFolderID {
		return ""
	}
	if name, ok := ctx.folderNames[id]; ok && name != "" {
		return name
	}
	return id
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
