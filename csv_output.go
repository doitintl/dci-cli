package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
)

// dciCSVContentType renders list responses and report results as CSV for
// spreadsheet import. Report rows are keyed through the result schema so the
// header row carries column names.
type dciCSVContentType struct{}

func (t dciCSVContentType) Detect(contentType string) bool { return false }

func (t dciCSVContentType) Marshal(value interface{}) ([]byte, error) {
	jsonSafe, err := toJSONSafe(value)
	if err != nil {
		return nil, err
	}
	jsonSafe = normalizeIntegralNumbers(jsonSafe)

	rows, err := toTableRows(jsonSafe)
	if err != nil {
		return nil, fmt.Errorf("response is not table-shaped; use --output json instead: %w", err)
	}

	columns := getTableOptions().columns
	keys := collectKeys(rows, columns)
	if len(keys) == 0 && len(columns) == 0 {
		keys = reportSchemaColumnNames(jsonSafe)
	}
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write(keys); err != nil {
		return nil, err
	}
	for _, row := range rows {
		record := make([]string, len(keys))
		for i, k := range keys {
			record[i] = csvCell(row[k])
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func reportSchemaColumnNames(value interface{}) []string {
	_, _, schema, ok := reportResultContainer(value)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(schema))
	for _, column := range schema {
		names = append(names, column.Name)
	}
	return names
}

func (t dciCSVContentType) Unmarshal(data []byte, value interface{}) error {
	return fmt.Errorf("unimplemented")
}

func csvCell(v interface{}) string {
	switch value := v.(type) {
	case nil:
		return ""
	case []interface{}:
		return joinPrimitives(value)
	case map[string]interface{}:
		return jsonCell(value)
	default:
		return fmt.Sprintf("%v", value)
	}
}
