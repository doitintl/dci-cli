package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

type requestBodyValidationError struct {
	unknownFields []string
	validFields   []string
}

func (validationError requestBodyValidationError) Error() string {
	return fmt.Sprintf(
		"unknown request body field(s): %s; valid top-level fields: %s",
		strings.Join(validationError.unknownFields, ", "),
		strings.Join(validationError.validFields, ", "),
	)
}

func (validationError requestBodyValidationError) ExitCode() int {
	return exitUsage
}

func (validationError requestBodyValidationError) AgentErrorCode() string {
	return "USAGE_ERROR"
}

func (validationError requestBodyValidationError) AgentErrorHint() string {
	return "Run the command with --help to inspect the request schema; use --rsh-no-cache if the API schema changed recently, or set DCI_SKIP_BODY_VALIDATION=1 to bypass validation"
}

func (validationError requestBodyValidationError) AgentErrorRetryable() bool {
	return false
}

var shorthandBodyFieldPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)\s*[.\[:{]`)
var schemaBodyFieldPattern = regexp.MustCompile(`^  ([A-Za-z_][A-Za-z0-9_-]*)\*?:`)

func validateRequestBody(command *cobra.Command, args []string) error {
	validFields := requestSchemaTopLevelFields(command.Long)
	if len(validFields) == 0 {
		return nil
	}
	if skip, _ := parseBoolish(os.Getenv("DCI_SKIP_BODY_VALIDATION")); skip {
		return nil
	}

	bodyArguments := args
	pathParameterCount := len(strings.Fields(command.Use)) - 1
	if pathParameterCount > 0 && pathParameterCount <= len(args) {
		bodyArguments = args[pathParameterCount:]
	}
	unknownFields := make([]string, 0)
	for _, argument := range bodyArguments {
		if strings.HasPrefix(argument, "@") || strings.HasPrefix(argument, "<") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(argument), "{") {
			for _, field := range jsonTopLevelFields([]byte(argument)) {
				if !validFields[field] {
					unknownFields = append(unknownFields, field)
				}
			}
			continue
		}
		match := shorthandBodyFieldPattern.FindStringSubmatch(argument)
		if match != nil && !validFields[match[1]] {
			unknownFields = append(unknownFields, match[1])
		}
	}

	if stdinFields, buffered := bufferStdinTopLevelFields(); buffered {
		for _, field := range stdinFields {
			if !validFields[field] {
				unknownFields = append(unknownFields, field)
			}
		}
	}

	if len(unknownFields) == 0 {
		return nil
	}
	unknownFields = uniqueSortedStrings(unknownFields)
	return requestBodyValidationError{
		unknownFields: unknownFields,
		validFields:   sortedFieldNames(validFields),
	}
}

func requestSchemaTopLevelFields(longHelp string) map[string]bool {
	_, afterHeading, found := strings.Cut(longHelp, "## Request Schema")
	if !found {
		return nil
	}
	_, schemaBlock, found := strings.Cut(afterHeading, "```schema")
	if !found {
		return nil
	}
	schemaBlock, _, _ = strings.Cut(schemaBlock, "```")
	fields := map[string]bool{}
	foundObject := false
	for _, line := range strings.Split(schemaBlock, "\n") {
		trimmedLine := strings.TrimSpace(line)
		if !foundObject {
			if trimmedLine == "{" {
				foundObject = true
			} else if strings.HasPrefix(trimmedLine, "[") {
				return nil
			}
			continue
		}
		if match := schemaBodyFieldPattern.FindStringSubmatch(line); match != nil {
			fields[match[1]] = true
		}
	}
	if !foundObject {
		return nil
	}
	return fields
}

func jsonTopLevelFields(data []byte) []string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil
	}
	fields := make([]string, 0, len(object))
	for field := range object {
		fields = append(fields, field)
	}
	return fields
}

func bufferStdinTopLevelFields() ([]string, bool) {
	inputInfo, err := cli.Stdin.Stat()
	if err != nil || inputInfo.Mode()&os.ModeCharDevice != 0 {
		return nil, false
	}
	data, err := io.ReadAll(cli.Stdin)
	if err != nil {
		return nil, false
	}
	cli.Stdin = &bufferedBodyInput{Reader: bytes.NewReader(data), info: inputInfo}
	trimmedData := bytes.TrimSpace(data)
	if len(trimmedData) == 0 || trimmedData[0] != '{' {
		return nil, true
	}
	return jsonTopLevelFields(trimmedData), true
}

type bufferedBodyInput struct {
	*bytes.Reader
	info fs.FileInfo
}

func (input *bufferedBodyInput) Stat() (fs.FileInfo, error) {
	return input.info, nil
}

func uniqueSortedStrings(values []string) []string {
	uniqueValues := make(map[string]bool, len(values))
	for _, value := range values {
		uniqueValues[value] = true
	}
	return sortedFieldNames(uniqueValues)
}

func sortedFieldNames(fields map[string]bool) []string {
	names := make([]string, 0, len(fields))
	for field := range fields {
		names = append(names, field)
	}
	sort.Strings(names)
	return names
}
