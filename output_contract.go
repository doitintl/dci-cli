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
		result := make([]interface{}, len(item))
		for index, row := range item {
			result[index] = projectObject(row, fields)
		}
		return result
	case map[string]interface{}:
		hasList := false
		result := make(map[string]interface{}, len(item))
		for key, child := range item {
			if rows, ok := child.([]interface{}); ok {
				hasList = true
				result[key] = projectResponseValue(rows, fields)
			} else {
				result[key] = child
			}
		}
		if hasList {
			return result
		}
		return projectObject(item, fields)
	default:
		return value
	}
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
