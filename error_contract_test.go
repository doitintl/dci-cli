package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type describedAgentError struct{}

func (describedAgentError) Error() string             { return "described failure" }
func (describedAgentError) AgentErrorCode() string    { return "DESCRIBED_FAILURE" }
func (describedAgentError) AgentErrorHint() string    { return "review the operation" }
func (describedAgentError) AgentErrorRetryable() bool { return false }

func TestExitCodeForHTTPStatus(t *testing.T) {
	tests := []struct {
		status int
		want   int
	}{
		{status: 200, want: exitSuccess},
		{status: 400, want: exitValidation},
		{status: 401, want: exitAuthentication},
		{status: 403, want: exitAuthorization},
		{status: 404, want: exitNotFound},
		{status: 409, want: exitConflict},
		{status: 429, want: exitRateLimited},
		{status: 503, want: exitServer},
	}
	for _, test := range tests {
		if got := exitCodeForHTTPStatus(test.status); got != test.want {
			t.Errorf("exitCodeForHTTPStatus(%d) = %d, want %d", test.status, got, test.want)
		}
	}
}

func TestStructuredErrorForResponse(t *testing.T) {
	response := cli.Response{
		Status: 429,
		Headers: map[string]string{
			"Retry-After":  "30",
			"X-Request-Id": "request-123",
		},
		Body: map[string]interface{}{"message": "too many requests"},
	}
	detail := structuredErrorForResponse(response)
	if detail.Code != "RATE_LIMITED" || !detail.Retryable {
		t.Fatalf("unexpected error classification: %+v", detail)
	}
	if detail.RetryAfter != "30" || detail.RequestID != "request-123" {
		t.Fatalf("unexpected retry metadata: %+v", detail)
	}
	if detail.Message != "too many requests" {
		t.Fatalf("message = %q", detail.Message)
	}
}

func TestStructuredErrorForExecutionUsesPortableDescriptor(t *testing.T) {
	detail := structuredErrorForExecution(describedAgentError{}, 0)
	if detail.Code != "DESCRIBED_FAILURE" || detail.Message != "described failure" {
		t.Fatalf("unexpected error detail: %+v", detail)
	}
	if detail.Hint != "review the operation" || detail.Retryable {
		t.Fatalf("unexpected descriptor metadata: %+v", detail)
	}
}

func TestUnknownShorthandFlagIsUsageError(t *testing.T) {
	err := errors.New("unknown shorthand flag: 'z' in -z")
	if got := exitCodeForExecutionError(err, 0); got != exitUsage {
		t.Fatalf("exit code = %d, want %d", got, exitUsage)
	}
	if detail := structuredErrorForExecution(err, 0); detail.Code != "USAGE_ERROR" {
		t.Fatalf("error code = %q, want USAGE_ERROR", detail.Code)
	}
}

func TestAcceptedDoerLoginClearsValidationFailure(t *testing.T) {
	responseExitCode = exitAuthorization
	agentErrorWritten = true
	viper.Set("rsh-ignore-status-code", false)
	t.Cleanup(func() {
		resetErrorContractState()
		viper.Set("rsh-ignore-status-code", false)
	})

	acceptDoerLoginValidation()

	if responseExitCode != exitSuccess {
		t.Fatalf("response exit code = %d, want %d", responseExitCode, exitSuccess)
	}
	if agentErrorWritten {
		t.Fatal("agent error remains marked as written")
	}
	if got := exitCodeForProcessStatus(403); got != exitSuccess {
		t.Fatalf("process exit code = %d, want %d", got, exitSuccess)
	}
}

func TestAgentResponseGuardWritesOneStructuredError(t *testing.T) {
	oldAgentMode := agentMode
	oldAgentUAMode := agentUAMode
	oldStderr := cli.Stderr
	agentMode = true
	agentUAMode = uaModeAgent
	agentErrorWritten = false
	responseExitCode = 0
	t.Cleanup(func() {
		agentMode = oldAgentMode
		agentUAMode = oldAgentUAMode
		cli.Stderr = oldStderr
		agentErrorWritten = false
		responseExitCode = 0
	})

	var stderr bytes.Buffer
	cli.Stderr = &stderr
	next := &recordingFormatter{}
	guard := dciResponseGuard{next: next}
	err := guard.Format(cli.Response{
		Status:  403,
		Headers: map[string]string{"X-Request-Id": "request-403"},
		Body:    map[string]interface{}{"message": "access denied"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !next.called {
		t.Fatal("formatter did not receive the original API error response")
	}
	body, ok := next.got.Body.(map[string]interface{})
	if !ok || body["message"] != "access denied" {
		t.Fatalf("formatted body = %#v", next.got.Body)
	}
	if responseExitCode != exitAuthorization {
		t.Fatalf("responseExitCode = %d", responseExitCode)
	}
	var envelope structuredErrorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON error %q: %v", stderr.String(), err)
	}
	if envelope.Error.Code != "PERMISSION_DENIED" || envelope.Error.RequestID != "request-403" {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
}

func TestAuthenticationFailureHintMatchesCredentialSource(t *testing.T) {
	t.Setenv(apiKeyEnvName, "")
	loginError := structuredErrorForStatus(401, "unauthorized", nil)
	if loginError.Hint != "Run: dci login" {
		t.Fatalf("OAuth hint = %q", loginError.Hint)
	}

	t.Setenv(apiKeyEnvName, "invalid-token")
	apiKeyError := structuredErrorForStatus(401, "unauthorized", nil)
	if !strings.Contains(apiKeyError.Hint, "DCI_API_KEY") {
		t.Fatalf("API key hint = %q", apiKeyError.Hint)
	}
}

func TestExecuteCLISuppressesFrameworkErrorsInAgentMode(t *testing.T) {
	oldAgentMode := agentMode
	oldAgentUAMode := agentUAMode
	oldRoot := cli.Root
	oldStderr := cli.Stderr
	agentMode = true
	agentUAMode = uaModeAgent
	agentErrorWritten = false
	cli.Root = &cobra.Command{}
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		agentMode = oldAgentMode
		agentUAMode = oldAgentUAMode
		cli.Root = oldRoot
		cli.Stderr = oldStderr
		agentErrorWritten = false
	})

	wantErr := errors.New("blocked")
	err := executeCLIWith(func() error {
		_, _ = io.WriteString(cli.Stderr, "framework noise")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !cli.Root.SilenceErrors || !cli.Root.SilenceUsage {
		t.Fatal("cobra errors and usage remain enabled")
	}
}

func TestInteractiveExecutionPrintsErrorOnce(t *testing.T) {
	oldAgentMode := agentMode
	oldAgentUAMode := agentUAMode
	oldStderr := cli.Stderr
	oldRoot := cli.Root
	agentMode = false
	agentUAMode = uaModeInteractive
	cli.Root = &cobra.Command{}
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		agentMode = oldAgentMode
		agentUAMode = oldAgentUAMode
		cli.Stderr = oldStderr
		cli.Root = oldRoot
	})

	bootErr := errors.New(`customerContext "foo" does not look like a customer domain`)
	err := executeCLIWith(func() error {
		// restish logs the error itself (with color codes) before returning it.
		_, _ = fmt.Fprintf(cli.Stderr, "\x1b[48;5;204mERROR:\x1b[0m Error: %v\n", bootErr)
		_, _ = fmt.Fprintln(cli.Stderr, "warning: something unrelated")
		return bootErr
	})
	if err != bootErr {
		t.Fatalf("err = %v, want the original error", err)
	}
	output := stderr.String()
	if strings.Contains(output, "customerContext") {
		t.Errorf("duplicate error line re-emitted: %q", output)
	}
	if !strings.Contains(output, "warning: something unrelated") {
		t.Errorf("unrelated stderr content dropped: %q", output)
	}
	if !cli.Root.SilenceErrors || !cli.Root.SilenceUsage {
		t.Error("cobra error/usage output not silenced in interactive mode")
	}
}

func TestNonInteractiveResponsePreservesFormatterOutput(t *testing.T) {
	oldAgentMode := agentMode
	oldAgentUAMode := agentUAMode
	oldStderr := cli.Stderr
	agentMode = true
	agentUAMode = uaModeNonInteractive
	agentErrorWritten = false
	responseExitCode = 0
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		agentMode = oldAgentMode
		agentUAMode = oldAgentUAMode
		cli.Stderr = oldStderr
		resetErrorContractState()
	})

	next := &recordingFormatter{}
	guard := dciResponseGuard{next: next}
	if err := guard.Format(cli.Response{
		Status: 403,
		Body:   map[string]interface{}{"message": "access denied"},
	}); err != nil {
		t.Fatal(err)
	}
	if !next.called {
		t.Fatal("non-interactive response body was not sent to the formatter")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if responseExitCode != 0 || agentErrorWritten {
		t.Fatal("agent error contract changed non-interactive response state")
	}
}

func TestNonInteractiveExecutionKeepsFrameworkOutput(t *testing.T) {
	oldAgentMode := agentMode
	oldAgentUAMode := agentUAMode
	oldStderr := cli.Stderr
	agentMode = true
	agentUAMode = uaModeNonInteractive
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		agentMode = oldAgentMode
		agentUAMode = oldAgentUAMode
		cli.Stderr = oldStderr
	})

	wantErr := errors.New("blocked")
	err := executeCLIWith(func() error {
		_, _ = io.WriteString(cli.Stderr, "framework error")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if stderr.String() != "framework error" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
