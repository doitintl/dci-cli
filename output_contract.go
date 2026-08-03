package main

import (
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

const defaultAgentTruncationLength = 2000

func shapeResponseBody(body interface{}) interface{} {
	fields := commaSeparatedValues(viper.GetString("agent-fields"))
	excluded := commaSeparatedValues(viper.GetString("agent-exclude"))
	if len(fields) > 0 {
		body = projectResponseValue(body, fields)
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

func projectResponseValue(value interface{}, fields []string) interface{} {
	switch item := value.(type) {
	case []interface{}:
		return projectRows(item, fields)
	case map[string]interface{}:
		if result, ok := projectNestedRows(item, fields); ok {
			return result
		}
		if key, rows, ok := listWrapperRows(item); ok {
			result := copyObject(item)
			result[key] = projectRows(rows, fields)
			return result
		}
		return projectObject(item, fields)
	default:
		return value
	}
}

func projectRows(rows []interface{}, fields []string) []interface{} {
	result := make([]interface{}, len(rows))
	for index, row := range rows {
		result[index] = projectObject(row, fields)
	}
	return result
}

func projectNestedRows(root map[string]interface{}, fields []string) (map[string]interface{}, bool) {
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
		projectedContainer["rows"] = projectSchemaRows(rows, readReportSchemaColumnNames(container["schema"]), fields)
		result[key] = projectedContainer
		return result, true
	}
	return nil, false
}

func projectSchemaRows(rows []interface{}, schema []string, fields []string) []interface{} {
	result := make([]interface{}, len(rows))
	for index, row := range rows {
		if cells, ok := row.([]interface{}); ok {
			object := make(map[string]interface{}, len(cells))
			for cellIndex, cell := range cells {
				object[reportColumnName(schema, cellIndex)] = cell
			}
			result[index] = projectObject(object, fields)
			continue
		}
		result[index] = projectObject(row, fields)
	}
	return result
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

func projectObject(value interface{}, fields []string) interface{} {
	object, ok := value.(map[string]interface{})
	if !ok {
		return value
	}
	result := make(map[string]interface{}, len(fields))
	for _, field := range fields {
		if child, exists := object[field]; exists {
			result[field] = child
		}
	}
	return result
}

func excludeResponseValue(value interface{}, excluded []string) interface{} {
	excludedSet := make(map[string]bool, len(excluded))
	for _, field := range excluded {
		excludedSet[field] = true
	}
	return transformResponseObjects(value, func(object map[string]interface{}) map[string]interface{} {
		result := make(map[string]interface{}, len(object))
		for key, child := range object {
			if !excludedSet[key] {
				result[key] = child
			}
		}
		return result
	})
}

func transformResponseObjects(value interface{}, transform func(map[string]interface{}) map[string]interface{}) interface{} {
	switch item := value.(type) {
	case []interface{}:
		result := make([]interface{}, len(item))
		for index, child := range item {
			result[index] = transformResponseObjects(child, transform)
		}
		return result
	case map[string]interface{}:
		result := make(map[string]interface{}, len(item))
		for key, child := range item {
			result[key] = transformResponseObjects(child, transform)
		}
		return transform(result)
	default:
		return value
	}
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
