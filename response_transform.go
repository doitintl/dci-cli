package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

// transformSuccessBody applies the response-shaping pipeline to successful
// (2xx, JSON) bodies before any output format renders them:
//
//  1. integral float64 values become int64 so no format prints scientific
//     notation for IDs and epoch timestamps
//  2. null list wrappers become [] so consumers never need a null guard
//  3. report result rows are stable-sorted (the API's order is
//     nondeterministic run-to-run)
//  4. all-empty report rows (null group, zero metrics) are dropped unless
//     --include-empty-rows is set
//  5. report rows are capped in agent mode (default 500; --max-rows overrides,
//     0 = unlimited) with explicit omission markers
//  6. --pivot reshapes report rows into a time-as-columns pivot view
//  7. --rows keyed re-keys positional report rows into schema-named objects
func transformSuccessBody(body interface{}) interface{} {
	body = normalizeIntegralNumbers(body)
	body = nullListsToEmpty(body)

	container, rows, schema, ok := reportResultContainer(body)
	if !ok {
		return body
	}

	sortReportRows(rows, schema)

	if !viper.GetBool("include-empty-rows") {
		kept, dropped := dropEmptyReportRows(rows, schema)
		if dropped > 0 {
			rows = kept
			container["rows"] = rows
			container["emptyRowsDropped"] = int64(dropped)
		}
	}

	if viper.GetBool("pivot-rows") {
		if pivoted, ok := pivotReportBody(rows, schema); ok {
			return pivoted
		}
	}

	if limit := effectiveMaxRows(); limit > 0 && len(rows) > limit {
		total := len(rows)
		rows = rows[:limit]
		container["rows"] = rows
		container["rowsTotal"] = int64(total)
		container["rowsOmitted"] = int64(total - limit)
		if cli.Stderr != nil {
			_, _ = fmt.Fprintf(cli.Stderr, "note: %d of %d rows omitted; narrow the query (group limit, metricFilter) or pass --max-rows to adjust\n", total-limit, total)
		}
	}

	if strings.EqualFold(viper.GetString("rows-mode"), "keyed") {
		container["rows"] = keyReportRows(rows, schema)
	}

	return body
}

// effectiveMaxRows resolves the report-row cap: an explicit --max-rows wins
// (0 disables), otherwise agent mode defaults to 500 and human mode to
// unlimited.
const defaultAgentMaxRows = 500

func effectiveMaxRows() int {
	// -1 (or unset) means auto: capped in agent mode, unlimited otherwise.
	if viper.IsSet("max-rows") {
		if v := viper.GetInt("max-rows"); v >= 0 {
			return v
		}
	}
	if agentMode {
		return defaultAgentMaxRows
	}
	return 0
}

// reportResultContainer locates the Cloud Analytics result container
// (result/results object holding a rows array), returning the container, its
// positional rows, and the parsed schema columns.
func reportResultContainer(body interface{}) (map[string]interface{}, []interface{}, []reportColumn, bool) {
	root, ok := body.(map[string]interface{})
	if !ok {
		return nil, nil, nil, false
	}
	for _, key := range []string{"result", "results"} {
		container, ok := root[key].(map[string]interface{})
		if !ok {
			continue
		}
		rows, ok := container["rows"].([]interface{})
		if !ok {
			continue
		}
		return container, rows, readReportSchema(container["schema"]), true
	}
	return nil, nil, nil, false
}

// reportColumn is a parsed result-schema column.
type reportColumn struct {
	Name string
	Type string
}

func readReportSchema(rawSchema interface{}) []reportColumn {
	schema, ok := rawSchema.([]interface{})
	if !ok {
		return nil
	}
	columns := make([]reportColumn, 0, len(schema))
	for _, col := range schema {
		m, ok := col.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		colType, _ := m["type"].(string)
		if strings.TrimSpace(name) != "" {
			columns = append(columns, reportColumn{Name: name, Type: colType})
		}
	}
	return columns
}

// normalizeIntegralNumbers converts whole float64 values (the product of JSON
// decoding int64 fields) back to int64 in place, so JSON/YAML/table output
// prints 1786353374392 instead of 1.786353374392e+12.
func normalizeIntegralNumbers(value interface{}) interface{} {
	const maxSafeInteger = 1 << 53
	switch v := value.(type) {
	case map[string]interface{}:
		for k, child := range v {
			v[k] = normalizeIntegralNumbers(child)
		}
		return v
	case []interface{}:
		for i, child := range v {
			v[i] = normalizeIntegralNumbers(child)
		}
		return v
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < maxSafeInteger {
			return int64(v)
		}
		return v
	default:
		return value
	}
}

// nullListsToEmpty replaces null top-level fields with [] on list-shaped
// responses (identified by their pagination/count metadata), so empty
// collections are always arrays.
func nullListsToEmpty(body interface{}) interface{} {
	root, ok := body.(map[string]interface{})
	if !ok || !hasListMetadata(root) {
		return body
	}
	collectionKey := ""
	for k, v := range root {
		if v != nil || isListMetadataKey(k) {
			continue
		}
		if collectionKey != "" {
			return body
		}
		collectionKey = k
	}
	if collectionKey != "" {
		root[collectionKey] = []interface{}{}
	}
	return body
}

func isListMetadataKey(key string) bool {
	switch key {
	case "count", "rowCount", "total", "totalCount", "pageToken", "nextPageToken", "cursor", "nextCursor", "hasMore":
		return true
	default:
		return false
	}
}

// sortReportRows stable-sorts positional report rows cell-by-cell (numbers
// numerically, everything else as strings, nulls first) so identical queries
// render identically across runs.
func sortReportRows(rows []interface{}, schema []reportColumn) {
	sort.SliceStable(rows, func(i, j int) bool {
		return compareReportRows(rows[i], rows[j]) < 0
	})
}

func compareReportRows(a, b interface{}) int {
	cellsA, okA := a.([]interface{})
	cellsB, okB := b.([]interface{})
	if !okA || !okB {
		return strings.Compare(fmt.Sprintf("%v", a), fmt.Sprintf("%v", b))
	}
	for i := 0; i < len(cellsA) && i < len(cellsB); i++ {
		if c := compareCells(cellsA[i], cellsB[i]); c != 0 {
			return c
		}
	}
	return len(cellsA) - len(cellsB)
}

func compareCells(a, b interface{}) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	na, aNum := numericCell(a)
	nb, bNum := numericCell(b)
	if aNum && bNum {
		switch {
		case na < nb:
			return -1
		case na > nb:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(fmt.Sprintf("%v", a), fmt.Sprintf("%v", b))
}

func numericCell(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}

// dropEmptyReportRows removes noise rows: a row is empty when at least one
// string-typed (dimension) cell is null and every numeric (metric) cell is
// zero or null. Rows that identify a real group with a genuine zero cost are
// kept — their dimensions are non-null.
func dropEmptyReportRows(rows []interface{}, schema []reportColumn) ([]interface{}, int) {
	if len(schema) == 0 {
		return rows, 0
	}
	kept := make([]interface{}, 0, len(rows))
	dropped := 0
	for _, row := range rows {
		cells, ok := row.([]interface{})
		if !ok || !isEmptyReportRow(cells, schema) {
			kept = append(kept, row)
			continue
		}
		dropped++
	}
	if dropped == 0 {
		return rows, 0
	}
	return kept, dropped
}

func isEmptyReportRow(cells []interface{}, schema []reportColumn) bool {
	hasNullDimension := false
	for i, col := range schema {
		if i >= len(cells) {
			break
		}
		switch col.Type {
		case "string":
			if cells[i] == nil {
				hasNullDimension = true
			}
		case "float", "integer", "number":
			if cells[i] == nil {
				continue
			}
			if n, ok := numericCell(cells[i]); !ok || n != 0 {
				return false
			}
		}
	}
	return hasNullDimension
}

// keyReportRows converts positional rows into schema-named objects so JSON
// consumers can address fields by name instead of zipping with the schema.
func keyReportRows(rows []interface{}, schema []reportColumn) []interface{} {
	names := make([]string, len(schema))
	for i, col := range schema {
		names[i] = col.Name
	}
	keyed := make([]interface{}, len(rows))
	for rowIndex, row := range rows {
		cells, ok := row.([]interface{})
		if !ok {
			keyed[rowIndex] = row
			continue
		}
		obj := make(map[string]interface{}, len(cells))
		for i, cell := range cells {
			obj[reportColumnName(names, i)] = cell
		}
		keyed[rowIndex] = obj
	}
	return keyed
}
