package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func ticketOperations(parameterType string) []cli.Operation {
	return []cli.Operation{{
		Name:       "get-ticket",
		Method:     "GET",
		PathParams: []*cli.Param{{Name: "ticketId", Type: parameterType}},
	}}
}

func ticketCommand() *cobra.Command {
	return &cobra.Command{Use: "get-ticket ticketid"}
}

// The rejected values are the three shapes observed in production, plus the bare
// placeholder names the CLI itself prints for the argument.
var pathParameterIntegerCases = []struct {
	value   string
	isValid bool
}{
	{"318240", true},
	{"-1", true},
	{"0", true},
	{"ticket-id: 318240", false},
	{"ticketId:309353", false},
	{`{"ticket-id": 310201}`, false},
	{"ticket-id", false},
	{"ticketid", false},
	{"ticket-id-318240", false},
	{"", false},
	{"99999999999999999999", false},
	{"318240.0", false},
	// Parse cleanly but reach the API as written, where they 404.
	{"+318240", false},
	{"0318240", false},
}

func TestValidatePathParametersRejectsNonIntegerValues(t *testing.T) {
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(ticketOperations("integer"))

	for _, testCase := range pathParameterIntegerCases {
		err := validatePathParameters(ticketCommand(), []string{testCase.value})
		if testCase.isValid && err != nil {
			t.Errorf("%q rejected: %v", testCase.value, err)
		}
		if !testCase.isValid && err == nil {
			t.Errorf("%q accepted for an integer parameter", testCase.value)
		}
	}
}

// A parameter with no declared schema loads as "string", so the 83 string-typed
// path parameters in the spec must keep accepting anything.
func TestValidatePathParametersAcceptsEveryStringValue(t *testing.T) {
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(ticketOperations("string"))

	for _, testCase := range pathParameterIntegerCases {
		if err := validatePathParameters(ticketCommand(), []string{testCase.value}); err != nil {
			t.Errorf("%q rejected for a string parameter: %v", testCase.value, err)
		}
	}
	for _, value := range []string{"finops:squad:customers", "project.dataset", "urn:resource"} {
		if err := validatePathParameters(ticketCommand(), []string{value}); err != nil {
			t.Errorf("%q rejected for a string parameter: %v", value, err)
		}
	}
}

func TestValidatePathParametersChecksNumberAndBooleanTypes(t *testing.T) {
	t.Cleanup(resetPathValidationState)

	for _, testCase := range []struct {
		declaredType string
		accepted     string
		rejected     string
	}{
		{"number", "1.5", "amount: 1.5"},
		{"boolean", "true", "active: true"},
		{"array[integer]", "310032,318240", "ticket-id: 310032,318240"},
	} {
		setOperationPathParameters(ticketOperations(testCase.declaredType))
		if err := validatePathParameters(ticketCommand(), []string{testCase.accepted}); err != nil {
			t.Errorf("%s rejected %q: %v", testCase.declaredType, testCase.accepted, err)
		}
		err := validatePathParameters(ticketCommand(), []string{testCase.rejected})
		if err == nil {
			t.Errorf("%s accepted %q", testCase.declaredType, testCase.rejected)
			continue
		}
		if testCase.declaredType == "array[integer]" && !strings.Contains(err.Error(), "comma-separated list of integers") {
			t.Errorf("array error = %q", err)
		}
	}
}

func TestValidatePathParametersFailsOpenWithoutMetadata(t *testing.T) {
	t.Cleanup(resetPathValidationState)
	resetPathValidationState()

	if err := validatePathParameters(ticketCommand(), []string{"ticket-id: 318240"}); err != nil {
		t.Fatalf("validation ran without operation metadata: %v", err)
	}

	setOperationPathParameters(ticketOperations("integer"))
	unknown := &cobra.Command{Use: "list-budgets"}
	if err := validatePathParameters(unknown, []string{"ticket-id: 318240"}); err != nil {
		t.Fatalf("unknown command validated: %v", err)
	}
}

func TestValidatePathParametersChecksEachParameterPositionally(t *testing.T) {
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters([]cli.Operation{{
		Name:   "create-ticket-comment",
		Method: "POST",
		PathParams: []*cli.Param{
			{Name: "customerId", Type: "string"},
			{Name: "ticketId", Type: "integer"},
		},
	}})
	command := &cobra.Command{Use: "create-ticket-comment customerid ticketid"}

	// Body arguments follow the path arguments and are never type-checked.
	args := []string{"acme.com", "318240", `body: "thanks"`}
	if err := validatePathParameters(command, args); err != nil {
		t.Fatalf("valid arguments rejected: %v", err)
	}

	err := validatePathParameters(command, []string{"acme.com", "ticket-id: 318240", `body: "thanks"`})
	if err == nil {
		t.Fatal("second path argument not validated")
	}
	if !strings.Contains(err.Error(), `"ticket-id"`) {
		t.Fatalf("error names the wrong argument: %q", err)
	}
	if hint := err.(pathParameterValidationError).AgentErrorHint(); !strings.Contains(hint, `dci create-ticket-comment acme.com 318240 'body: "thanks"'`) {
		t.Fatalf("hint = %q", hint)
	}

	// Fewer arguments than parameters is cobra's error to report, not ours.
	if err := validatePathParameters(command, []string{"acme.com"}); err != nil {
		t.Fatalf("missing argument rejected here: %v", err)
	}
}

func TestPathParameterValidationErrorContract(t *testing.T) {
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(ticketOperations("integer"))

	err := validatePathParameters(ticketCommand(), []string{"ticket-id: 318240"})
	if err == nil {
		t.Fatal("malformed ticket ID accepted")
	}
	validationError, ok := err.(pathParameterValidationError)
	if !ok {
		t.Fatalf("error type = %T", err)
	}
	if got := validationError.Error(); got != `invalid value for path argument "ticket-id": "ticket-id: 318240" is not an integer` {
		t.Fatalf("message = %q", got)
	}
	if validationError.ExitCode() != exitUsage {
		t.Errorf("exit code = %d, want %d", validationError.ExitCode(), exitUsage)
	}
	if validationError.AgentErrorRetryable() {
		t.Error("error reported as retryable")
	}

	structured := structuredErrorForExecution(err, 0)
	if structured.Code != "USAGE_ERROR" || structured.Retryable {
		t.Fatalf("structured error = %+v", structured)
	}
	if !strings.Contains(structured.Hint, "dci get-ticket 318240") {
		t.Fatalf("hint = %q", structured.Hint)
	}
	if exitCodeForExecutionError(err, 0) != exitUsage {
		t.Errorf("execution exit code = %d", exitCodeForExecutionError(err, 0))
	}
}

func TestPathParameterValidationSuggestsCorrectedInvocation(t *testing.T) {
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(ticketOperations("integer"))

	for _, testCase := range []struct {
		value string
		want  string
	}{
		{"ticket-id: 318240", "dci get-ticket 318240"},
		{"ticketId:309353", "dci get-ticket 309353"},
		{`{"ticket-id": 310201}`, "dci get-ticket 310201"},
		// A hyphen before the digits belongs to the label. Suggesting
		// "-318240" would be parsed as a flag and fail a second time.
		{"ticket-id-318240", "dci get-ticket 318240"},
		{"+318240", "dci get-ticket 318240"},
		{"0318240", "dci get-ticket 318240"},
	} {
		err := validatePathParameters(ticketCommand(), []string{testCase.value})
		if err == nil {
			t.Fatalf("%q accepted", testCase.value)
		}
		hint := err.(pathParameterValidationError).AgentErrorHint()
		if !strings.Contains(hint, testCase.want) {
			t.Errorf("hint for %q = %q, want it to suggest %q", testCase.value, hint, testCase.want)
		}
		if strings.Contains(hint, "dci get-ticket -") {
			t.Errorf("hint for %q suggests a value pflag would read as a flag: %q", testCase.value, hint)
		}
	}

	// Nothing recoverable: no integer at all, or more than one candidate.
	for _, value := range []string{"ticket-id", "2026-08-11"} {
		err := validatePathParameters(ticketCommand(), []string{value})
		if err == nil {
			t.Fatalf("%q accepted", value)
		}
		hint := err.(pathParameterValidationError).AgentErrorHint()
		if strings.Contains(hint, "e.g.") {
			t.Errorf("hint for %q invented a suggestion: %q", value, hint)
		}
		if !strings.Contains(hint, "an integer") {
			t.Errorf("hint for %q = %q", value, hint)
		}
	}
}

func TestPathParameterValidationOmitsRunnableRetryWhenFlagsChanged(t *testing.T) {
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(ticketOperations("integer"))
	command := ticketCommand()
	command.Flags().Bool("dry-run", false, "")
	command.Flags().StringP("customer-context", "D", "", "")
	if err := command.Flags().Parse([]string{"--dry-run", "-D", "acme.com"}); err != nil {
		t.Fatal(err)
	}

	err := validatePathParameters(command, []string{"ticket-id: 318240"})
	if err == nil {
		t.Fatal("malformed ticket ID accepted")
	}
	hint := err.(pathParameterValidationError).AgentErrorHint()
	if strings.Contains(hint, "dci get-ticket") {
		t.Fatalf("hint dropped changed flags from a runnable retry: %q", hint)
	}
	if !strings.Contains(hint, `Pass an integer as the "ticket-id" argument`) {
		t.Fatalf("hint = %q", hint)
	}
}

// Drives the real PersistentPreRunE wired by addOutputFlag, so the validator
// cannot be silently disconnected from the command tree. The operation's RunE is
// what would build the request, so it must not be reached.
func TestPathParameterValidationBlocksExecution(t *testing.T) {
	previousRoot := cli.Root
	t.Cleanup(func() {
		cli.Root = previousRoot
		resetDestructiveContractState()
		resetPathValidationState()
		viper.Reset()
	})

	for _, testCase := range []struct {
		name        string
		args        []string
		wantBlocked bool
	}{
		{"malformed", []string{"dci", "get-ticket", "ticket-id: 318240"}, true},
		{"malformed with dry run", []string{"dci", "get-ticket", "ticket-id: 318240", "--dry-run"}, true},
		{"valid", []string{"dci", "get-ticket", "318240"}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			viper.Reset()
			executed := false
			root := &cobra.Command{Use: "dci"}
			apiCommand := &cobra.Command{Use: "dci"}
			operation := &cobra.Command{
				Use:  "get-ticket ticketid",
				Args: cobra.ExactArgs(1),
				RunE: func(*cobra.Command, []string) error {
					executed = true
					return nil
				},
			}
			apiCommand.AddCommand(operation)
			root.AddCommand(apiCommand)
			cli.Root = root
			addOutputFlag()
			setOperationMetadata(ticketOperations("integer"))

			root.SetArgs(testCase.args)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			err := root.Execute()

			if testCase.wantBlocked {
				if executed {
					t.Fatal("command ran despite an invalid path argument")
				}
				var validationError pathParameterValidationError
				if !errors.As(err, &validationError) {
					t.Fatalf("error = %v (%T)", err, err)
				}
				return
			}
			if err != nil || !executed {
				t.Fatalf("valid invocation blocked: err = %v, executed = %t", err, executed)
			}
		})
	}
}
