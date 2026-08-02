package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

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

func TestAgentResponseGuardWritesOneStructuredError(t *testing.T) {
	oldAgentMode := agentMode
	oldStderr := cli.Stderr
	agentMode = true
	agentErrorWritten = false
	responseExitCode = 0
	t.Cleanup(func() {
		agentMode = oldAgentMode
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
	if next.called {
		t.Fatal("formatter received an agent-mode error response")
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

func TestExecuteCLISuppressesFrameworkErrorsInAgentMode(t *testing.T) {
	oldAgentMode := agentMode
	oldRoot := cli.Root
	oldStderr := cli.Stderr
	agentMode = true
	agentErrorWritten = false
	cli.Root = &cobra.Command{}
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() {
		agentMode = oldAgentMode
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
