package main

import (
	"fmt"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

const defaultAgentTruncationLength = 2000

func shapeResponseBody(body interface{}) interface{} {
	fields := commaSeparatedValues(viper.GetString("agent-fields"))
	excluded := commaSeparatedValues(viper.GetString("agent-exclude"))
	if len(fields) > 0 {
		matchedFields := make(map[string]bool, len(fields))
		var comparable bool
		body, comparable = projectResponseValue(body, fields, matchedFields)
		missingFields := make([]string, 0, len(fields))
		for _, field := range fields {
			if !matchedFields[field] {
				missingFields = append(missingFields, field)
			}
		}
		if comparable && len(missingFields) > 0 && cli.Stderr != nil {
			fmt.Fprintf(cli.Stderr, "warning: requested fields not present in the response: %s\n", strings.Join(missingFields, ", "))
		}
	}
	if len(excluded) > 0 {
		body = excludeResponseValue(body, excluded)
	}
	if agentMode && !viper.GetBool("agent-full") && !viper.GetBool("agent-no-truncate") {
		body = truncateResponseValue(body, defaultAgentTruncationLength)
	}
	if agentMode {
		body = makeEmptyStateDefinitive(body)
	}
	return body
}

type dciOutputGuard struct {
	next cli.ResponseFormatter
}

func (guard dciOutputGuard) Format(response cli.Response) error {
	if response.Status >= 200 && response.Status < 300 {
		response.Body = shapeResponseBody(response.Body)
	}
	return guard.next.Format(response)
}

func installOutputGuard() {
	if cli.Formatter != nil {
		cli.Formatter = dciOutputGuard{next: cli.Formatter}
	}
}

func commaSeparatedValues(value string) []string {
	seen := map[string]bool{}
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

func projectResponseValue(value interface{}, fields []string, matchedFields map[string]bool) (interface{}, bool) {
	switch item := value.(type) {
	case []interface{}:
		return projectRows(item, fields, matchedFields)
	case map[string]interface{}:
		if result, comparable, ok := projectNestedRows(item, fields, matchedFields); ok {
			return result, comparable
		}
		if key, rows, ok := listWrapperRows(item); ok {
			result := copyObject(item)
			projectedRows, comparable := projectRows(rows, fields, matchedFields)
			result[key] = projectedRows
			return result, comparable
		}
		return projectObject(item, fields, matchedFields)
	default:
		return value, false
	}
}

func projectRows(rows []interface{}, fields []string, matchedFields map[string]bool) ([]interface{}, bool) {
	result := make([]interface{}, len(rows))
	comparable := false
	for index, row := range rows {
		var rowComparable bool
		result[index], rowComparable = projectObject(row, fields, matchedFields)
		comparable = comparable || rowComparable
	}
	return result, comparable
}

func projectNestedRows(root map[string]interface{}, fields []string, matchedFields map[string]bool) (map[string]interface{}, bool, bool) {
	for _, key := range []string{"result", "results"} {
		container, ok := root[key].(map[string]interface{})
		if !ok {
			continue
		}
		rows, ok := container["rows"].([]interface{})
		if !ok {
			continue
		}
		result := copyObject(root)
		projectedContainer := copyObject(container)
		projectedRows, comparable := projectSchemaRows(rows, readReportSchemaColumnNames(container["schema"]), fields, matchedFields)
		projectedContainer["rows"] = projectedRows
		result[key] = projectedContainer
		return result, comparable, true
	}
	return nil, false, false
}

func projectSchemaRows(rows []interface{}, schema []string, fields []string, matchedFields map[string]bool) ([]interface{}, bool) {
	result := make([]interface{}, len(rows))
	comparable := false
	for index, row := range rows {
		if cells, ok := row.([]interface{}); ok {
			object := make(map[string]interface{}, len(cells))
			for cellIndex, cell := range cells {
				object[reportColumnName(schema, cellIndex)] = cell
			}
			result[index], _ = projectObject(object, fields, matchedFields)
			comparable = true
			continue
		}
		var rowComparable bool
		result[index], rowComparable = projectObject(row, fields, matchedFields)
		comparable = comparable || rowComparable
	}
	return result, comparable
}

func listWrapperRows(object map[string]interface{}) (string, []interface{}, bool) {
	for _, key := range []string{"results", "items"} {
		if rows, ok := object[key].([]interface{}); ok && isObjectArray(rows) {
			return key, rows, true
		}
	}

	type candidate struct {
		key  string
		rows []interface{}
	}
	candidates := make([]candidate, 0)
	for key, value := range object {
		if rows, ok := value.([]interface{}); ok && isObjectArray(rows) {
			candidates = append(candidates, candidate{key: key, rows: rows})
		}
	}
	if len(candidates) != 1 {
		return "", nil, false
	}
	if len(object) == 1 || hasListMetadata(object) {
		return candidates[0].key, candidates[0].rows, true
	}
	return "", nil, false
}

func hasListMetadata(object map[string]interface{}) bool {
	for _, key := range []string{"count", "rowCount", "total", "totalCount", "pageToken", "nextPageToken", "cursor", "nextCursor", "hasMore"} {
		if _, ok := object[key]; ok {
			return true
		}
	}
	return false
}

func copyObject(object map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(object))
	for key, value := range object {
		result[key] = value
	}
	return result
}

func projectObject(value interface{}, fields []string, matchedFields map[string]bool) (interface{}, bool) {
	object, ok := value.(map[string]interface{})
	if !ok {
		return value, false
	}
	result := make(map[string]interface{}, len(fields))
	for _, field := range fields {
		if child, exists := object[field]; exists {
			result[field] = child
			matchedFields[field] = true
		}
	}
	return result, len(object) > 0
}

func excludeResponseValue(value interface{}, excluded []string) interface{} {
	excludedSet := make(map[string]bool, len(excluded))
	for _, field := range excluded {
		excludedSet[field] = true
	}
	switch item := value.(type) {
	case []interface{}:
		return excludeRows(item, excludedSet)
	case map[string]interface{}:
		if result, ok := excludeNestedRows(item, excludedSet); ok {
			return result
		}
		if key, rows, ok := listWrapperRows(item); ok {
			result := excludeObject(item, excludedSet).(map[string]interface{})
			if !excludedSet[key] {
				result[key] = excludeRows(rows, excludedSet)
			}
			return result
		}
		return excludeObject(item, excludedSet)
	default:
		return value
	}
}

func excludeNestedRows(root map[string]interface{}, excluded map[string]bool) (map[string]interface{}, bool) {
	for _, key := range []string{"result", "results"} {
		container, ok := root[key].(map[string]interface{})
		if !ok {
			continue
		}
		rows, ok := container["rows"].([]interface{})
		if !ok {
			continue
		}
		result := excludeObject(root, excluded).(map[string]interface{})
		if excluded[key] {
			return result, true
		}
		filteredContainer := excludeObject(container, excluded).(map[string]interface{})
		if !excluded["rows"] {
			filteredRows, filteredSchema := excludeReportRows(rows, container["schema"], excluded)
			filteredContainer["rows"] = filteredRows
			if _, hasSchema := container["schema"]; hasSchema && !excluded["schema"] {
				filteredContainer["schema"] = filteredSchema
			}
		}
		result[key] = filteredContainer
		return result, true
	}
	return nil, false
}

func excludeReportRows(rows []interface{}, schemaValue interface{}, excluded map[string]bool) ([]interface{}, interface{}) {
	schema, ok := schemaValue.([]interface{})
	columnNames := readReportSchemaColumnNames(schemaValue)
	if !ok || len(columnNames) == 0 {
		return excludeRows(rows, excluded), schemaValue
	}

	keptIndexes := make([]int, 0, len(columnNames))
	filteredSchema := make([]interface{}, 0, len(schema))
	for index, columnName := range columnNames {
		if excluded[columnName] {
			continue
		}
		keptIndexes = append(keptIndexes, index)
		if index < len(schema) {
			filteredSchema = append(filteredSchema, schema[index])
		}
	}

	filteredRows := make([]interface{}, len(rows))
	for rowIndex, row := range rows {
		cells, ok := row.([]interface{})
		if !ok {
			filteredRows[rowIndex] = excludeObject(row, excluded)
			continue
		}
		filteredCells := make([]interface{}, 0, len(keptIndexes))
		for _, cellIndex := range keptIndexes {
			if cellIndex < len(cells) {
				filteredCells = append(filteredCells, cells[cellIndex])
			}
		}
		filteredRows[rowIndex] = filteredCells
	}
	return filteredRows, filteredSchema
}

func excludeRows(rows []interface{}, excluded map[string]bool) []interface{} {
	result := make([]interface{}, len(rows))
	for index, row := range rows {
		result[index] = excludeObject(row, excluded)
	}
	return result
}

func excludeObject(value interface{}, excluded map[string]bool) interface{} {
	object, ok := value.(map[string]interface{})
	if !ok {
		return value
	}
	result := make(map[string]interface{}, len(object))
	for key, child := range object {
		if !excluded[key] {
			result[key] = child
		}
	}
	return result
}

func truncateResponseValue(value interface{}, limit int) interface{} {
	switch item := value.(type) {
	case string:
		runes := []rune(item)
		if len(runes) <= limit {
			return item
		}
		return map[string]interface{}{
			"value":      string(runes[:limit]),
			"_truncated": len(runes) - limit,
		}
	case []interface{}:
		result := make([]interface{}, len(item))
		for index, child := range item {
			result[index] = truncateResponseValue(child, limit)
		}
		return result
	case map[string]interface{}:
		result := make(map[string]interface{}, len(item))
		for key, child := range item {
			result[key] = truncateResponseValue(child, limit)
		}
		return result
	default:
		return value
	}
}

func makeEmptyStateDefinitive(value interface{}) interface{} {
	items, ok := value.([]interface{})
	if ok && len(items) == 0 {
		return map[string]interface{}{"count": 0, "results": []interface{}{}}
	}
	return value
}
