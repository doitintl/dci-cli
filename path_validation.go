package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

// operationPathParameters maps an operation name to its declared path
// parameters. restish carries the type from the OpenAPI spec on every Param but
// never consults it — Param.Parse is a stub that returns its input unchanged —
// so a malformed identifier is substituted into the URI and sent as-is.
var operationPathParameters = map[string][]*cli.Param{}

type pathParameterCheck struct {
	singular string
	plural   string
	valid    func(string) bool
}

// Types absent from this table accept any value. restish's loader defaults a
// parameter with no declared schema to "string", so type validation can never
// start rejecting values for a parameter the spec leaves untyped.
var pathParameterChecks = map[string]pathParameterCheck{
	"integer": {"an integer", "integers", func(value string) bool {
		// ParseInt bounds to 64 bits, matching the spec's format: int64. The
		// argument reaches the API exactly as written, so a form ParseInt
		// normalizes away — a leading + or 0 — has to be rejected here too:
		// it parses cleanly and then 404s as an unknown identifier.
		parsed, err := strconv.ParseInt(value, 10, 64)
		return err == nil && strconv.FormatInt(parsed, 10) == value
	}},
	"number": {"a number", "numbers", func(value string) bool {
		_, err := strconv.ParseFloat(value, 64)
		return err == nil
	}},
	"boolean": {"a boolean", "booleans", func(value string) bool {
		_, err := strconv.ParseBool(value)
		return err == nil
	}},
}

// embeddedIntegerPattern recovers the identifier from a value that carries the
// argument name alongside it, e.g. `ticket-id: 318240`. Digits only: a hyphen
// here belongs to the label, not to the number, and a suggestion starting with
// one would be parsed as a flag and fail all over again.
var embeddedIntegerPattern = regexp.MustCompile(`\d+`)

type pathParameterValidationError struct {
	argumentName string
	value        string
	expectation  string
	// example is a corrected invocation, empty when no value could be recovered.
	example string
}

func (validationError pathParameterValidationError) Error() string {
	return fmt.Sprintf(
		"invalid value for path argument %q: %q is not %s",
		validationError.argumentName,
		validationError.value,
		validationError.expectation,
	)
}

func (validationError pathParameterValidationError) ExitCode() int {
	return exitUsage
}

func (validationError pathParameterValidationError) AgentErrorCode() string {
	return "USAGE_ERROR"
}

func (validationError pathParameterValidationError) AgentErrorHint() string {
	if validationError.example != "" {
		return "Pass only the value, not the argument name — e.g. " + validationError.example
	}
	return fmt.Sprintf(
		"Pass %s as the %q argument; run the command with --help to inspect its arguments",
		validationError.expectation,
		validationError.argumentName,
	)
}

func (validationError pathParameterValidationError) AgentErrorRetryable() bool {
	return false
}

func resetPathValidationState() {
	operationPathParameters = map[string][]*cli.Param{}
}

func setOperationPathParameters(operations []cli.Operation) {
	operationPathParameters = make(map[string][]*cli.Param, len(operations))
	for _, operation := range operations {
		if len(operation.PathParams) > 0 {
			operationPathParameters[operation.Name] = operation.PathParams
		}
	}
}

// validatePathParameters rejects a positional argument that cannot be the type
// its path parameter declares, before any request is built. It fails open: when
// operation metadata is unavailable the map is empty and every value passes,
// since a valid command must never fail because the spec could not be loaded.
func validatePathParameters(command *cobra.Command, args []string) error {
	for index, parameter := range operationPathParameters[command.Name()] {
		if index >= len(args) {
			break
		}
		elementType, isList := pathParameterElementType(parameter.Type)
		check, checkable := pathParameterChecks[elementType]
		if !checkable {
			continue
		}
		value := args[index]
		expectation := check.singular
		elements := []string{value}
		if isList {
			expectation = "a comma-separated list of " + check.plural
			elements = strings.Split(value, ",")
		}
		if allPathParameterElementsValid(elements, check) {
			continue
		}
		return pathParameterValidationError{
			argumentName: parameter.OptionName(),
			value:        value,
			expectation:  expectation,
			example:      correctedInvocationExample(command.Name(), args, index, recoveredIntegerValue(elementType, isList, value)),
		}
	}
	return nil
}

// pathParameterElementType splits restish's array[...] type notation into the
// element type and whether the parameter is a list.
func pathParameterElementType(declaredType string) (string, bool) {
	if inner, isList := strings.CutPrefix(declaredType, "array["); isList {
		return strings.TrimSuffix(inner, "]"), true
	}
	return declaredType, false
}

func allPathParameterElementsValid(elements []string, check pathParameterCheck) bool {
	for _, element := range elements {
		if !check.valid(element) {
			return false
		}
	}
	return true
}

// recoveredIntegerValue returns the identifier embedded in a rejected value when
// exactly one candidate is present, so the error can suggest the fix.
func recoveredIntegerValue(elementType string, isList bool, value string) string {
	if elementType != "integer" || isList {
		return ""
	}
	candidates := embeddedIntegerPattern.FindAllString(value, 2)
	if len(candidates) != 1 {
		return ""
	}
	parsed, err := strconv.ParseInt(candidates[0], 10, 64)
	if err != nil {
		return ""
	}
	// Canonical form, so the suggestion cannot be rejected a second time.
	return strconv.FormatInt(parsed, 10)
}

func correctedInvocationExample(commandName string, args []string, index int, recovered string) string {
	if recovered == "" {
		return ""
	}
	tokens := make([]string, 0, len(args)+2)
	tokens = append(tokens, "dci", commandName)
	for position, argument := range args {
		if position == index {
			argument = recovered
		}
		tokens = append(tokens, shellQuoteArgument(argument))
	}
	return strings.Join(tokens, " ")
}

// shellQuoteArgument quotes a value the shell would otherwise interpret, so the
// suggested invocation can be run as printed.
func shellQuoteArgument(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n\"'`$&|;<>()[]{}*?!#~\\") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
