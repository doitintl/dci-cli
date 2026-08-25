package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

func shapeResponseBody(body interface{}) interface{} {
	fields := commaSeparatedValues(viper.GetString("agent-fields"))
	excluded := commaSeparatedValues(viper.GetString("agent-exclude"))
	if len(fields) > 0 {
		body = projectResponseValue(body, fields)
	}
	if len(excluded) > 0 {
		body = excludeResponseValue(body, excluded)
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
		if !isErrorResponseBody(response) {
			if err := validateResponseFields(response.Body, commaSeparatedValues(viper.GetString("agent-fields"))); err != nil {
				return err
			}
		}
		response.Body = shapeResponseBody(response.Body)
	}
	return guard.next.Format(response)
}

type responseFieldValidationError struct {
	missingFields   []string
	availableFields []string
}

func (validationError responseFieldValidationError) Error() string {
	return fmt.Sprintf(
		"requested fields are not projectable row fields: %s; available fields: %s",
		strings.Join(validationError.missingFields, ", "),
		strings.Join(validationError.availableFields, ", "),
	)
}

func (validationError responseFieldValidationError) ExitCode() int {
	return exitUsage
}

func (validationError responseFieldValidationError) AgentErrorCode() string {
	return "USAGE_ERROR"
}

func (validationError responseFieldValidationError) AgentErrorHint() string {
	return fmt.Sprintf("Choose one or more available fields: %s", strings.Join(validationError.availableFields, ", "))
}

func (validationError responseFieldValidationError) AgentErrorRetryable() bool {
	return false
}

func validateResponseFields(body interface{}, requestedFields []string) error {
	if len(requestedFields) == 0 {
		return nil
	}
	availableFields, comparable := availableResponseFields(body)
	if !comparable {
		return nil
	}
	availableSet := make(map[string]bool, len(availableFields))
	for _, field := range availableFields {
		availableSet[field] = true
	}
	missingFields := make([]string, 0)
	for _, field := range requestedFields {
		if !availableSet[field] {
			missingFields = append(missingFields, field)
		}
	}
	if len(missingFields) == 0 {
		return nil
	}
	return responseFieldValidationError{missingFields: missingFields, availableFields: availableFields}
}

func availableResponseFields(value interface{}) ([]string, bool) {
	fields := map[string]bool{}
	comparable := collectAvailableResponseFields(value, fields)
	names := make([]string, 0, len(fields))
	for field := range fields {
		names = append(names, field)
	}
	sort.Strings(names)
	return names, comparable
}

func collectAvailableResponseFields(value interface{}, fields map[string]bool) bool {
	switch item := value.(type) {
	case []interface{}:
		return collectRowFields(item, fields)
	case map[string]interface{}:
		if _, container, rows, ok := nestedReportRows(item); ok {
			for _, field := range readReportSchemaColumnNames(container["schema"]) {
				fields[field] = true
			}
			hasRows := collectRowFields(rows, fields)
			return len(fields) > 0 || hasRows
		}
		if _, rows, ok := listWrapperRows(item); ok {
			return collectRowFields(rows, fields)
		}
		for field := range item {
			fields[field] = true
		}
		return len(item) > 0
	default:
		return false
	}
}

func collectRowFields(rows []interface{}, fields map[string]bool) bool {
	comparable := false
	for _, row := range rows {
		object, ok := row.(map[string]interface{})
		if !ok {
			continue
		}
		comparable = comparable || len(object) > 0
		for field := range object {
			fields[field] = true
		}
	}
	return comparable
}

func installOutputGuard() {
	if cli.Formatter != nil {
		cli.Formatter = dciOutputGuard{next: cli.Formatter}
	}
}

// verbatimJSONResponseOperations answer with a document meant to be saved and
// fed back to the API unchanged, so the body has to reach stdout as JSON in
// every mode: TOON is not JSON, and the table renderer reduces a flow bundle
// to its `flows` array, dropping the `kind`, `schemaVersion`, `rootFlow` and
// `requirements` that make the bundle importable.
var verbatimJSONResponseOperations = map[string]bool{
	"export-cloudflow-flow": true,
}

// multiSectionResponseOperations answer with several sibling sections rather
// than a row set. The table renderer keeps only the largest array of objects
// and silently drops the rest, which for an import means losing the dry-run
// plan's `valid` and `errors` — a rejected plan then renders as a clean
// requirements list — or the result's `orphanedResources`, the side resources
// a failed import leaked for the caller to clean up by hand. TOON encodes the
// whole body, so agent mode keeps it.
var multiSectionResponseOperations = map[string]bool{
	"import-cloudflow-flow": true,
}

// defaultOutputFormatForCommand picks the output format for a command invoked
// without --output.
func defaultOutputFormatForCommand(commandName string) string {
	if verbatimJSONResponseOperations[commandName] {
		return "json"
	}
	format := defaultOutputFormat()
	if format == "table" && multiSectionResponseOperations[commandName] {
		return "json"
	}
	return format
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

func nestedReportRows(root map[string]interface{}) (string, map[string]interface{}, []interface{}, bool) {
	for _, key := range []string{"result", "results"} {
		container, ok := root[key].(map[string]interface{})
		if !ok {
			continue
		}
		rows, ok := container["rows"].([]interface{})
		if ok {
			return key, container, rows, true
		}
	}
	return "", nil, nil, false
}

func projectNestedRows(root map[string]interface{}, fields []string) (map[string]interface{}, bool) {
	key, container, rows, ok := nestedReportRows(root)
	if !ok {
		return nil, false
	}
	result := copyObject(root)
	projectedContainer := copyObject(container)
	projectedContainer["rows"] = projectSchemaRows(rows, readReportSchemaColumnNames(container["schema"]), fields)
	result[key] = projectedContainer
	return result, true
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
