package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/rest-sh/restish/cli"
)

const (
	exitSuccess        = 0
	exitGenericFailure = 1
	exitUsage          = 2
	exitAuthentication = 10
	exitAuthorization  = 11
	exitNotFound       = 20
	exitConflict       = 21
	exitValidation     = 30
	exitServer         = 40
	exitNetwork        = 41
	exitRateLimited    = 50
)

var (
	responseExitCode  int
	agentErrorWritten bool
)

func resetErrorContractState() {
	responseExitCode = 0
	agentErrorWritten = false
}

type structuredErrorEnvelope struct {
	Error structuredError `json:"error"`
}

type structuredError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Hint       string `json:"hint,omitempty"`
	Retryable  bool   `json:"retryable"`
	HTTPStatus int    `json:"http_status,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
}

type agentErrorDescriptor interface {
	AgentErrorCode() string
	AgentErrorHint() string
	AgentErrorRetryable() bool
}

func exitCodeForHTTPStatus(status int) int {
	switch status {
	case 0:
		return exitSuccess
	case 400, 422:
		return exitValidation
	case 401:
		return exitAuthentication
	case 403:
		return exitAuthorization
	case 404:
		return exitNotFound
	case 409:
		return exitConflict
	case 429:
		return exitRateLimited
	}
	if status >= 500 {
		return exitServer
	}
	if status >= 400 {
		return exitGenericFailure
	}
	return exitSuccess
}

func exitCodeForExecutionError(err error, status int) int {
	var codedError interface{ ExitCode() int }
	if errors.As(err, &codedError) {
		return codedError.ExitCode()
	}
	if code := exitCodeForHTTPStatus(status); code != exitSuccess {
		return code
	}
	var networkError net.Error
	var urlError *url.Error
	if errors.As(err, &networkError) || errors.As(err, &urlError) {
		return exitNetwork
	}
	if isUsageError(err) {
		return exitUsage
	}
	return exitGenericFailure
}

func isSilentExecutionError(err error) bool {
	var silentError interface{ Silent() bool }
	return errors.As(err, &silentError) && silentError.Silent()
}

func isUsageError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"unknown command",
		"unknown flag",
		"invalid argument",
		"invalid --output",
		"required flag",
		"requires at least",
		"requires exactly",
		"accepts ",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func structuredErrorForExecution(err error, status int) structuredError {
	var errorProvider interface{ StructuredError() structuredError }
	if errors.As(err, &errorProvider) {
		return errorProvider.StructuredError()
	}
	var descriptor agentErrorDescriptor
	if errors.As(err, &descriptor) {
		return structuredError{
			Code:      descriptor.AgentErrorCode(),
			Message:   err.Error(),
			Hint:      descriptor.AgentErrorHint(),
			Retryable: descriptor.AgentErrorRetryable(),
		}
	}
	if status >= 400 {
		return structuredErrorForStatus(status, err.Error(), nil)
	}
	if exitCodeForExecutionError(err, status) == exitNetwork {
		return structuredError{
			Code:      "NETWORK_ERROR",
			Message:   err.Error(),
			Hint:      "Check network connectivity and retry",
			Retryable: true,
		}
	}
	if isUsageError(err) {
		return structuredError{
			Code:      "USAGE_ERROR",
			Message:   err.Error(),
			Hint:      "Run the command with --help to inspect its arguments and flags",
			Retryable: false,
		}
	}
	return structuredError{
		Code:      "CLI_ERROR",
		Message:   err.Error(),
		Retryable: false,
	}
}

func structuredErrorForResponse(resp cli.Response) structuredError {
	message := responseErrorMessage(resp.Body)
	if message == "" {
		message = fmt.Sprintf("DoiT API request failed with HTTP status %d", resp.Status)
	}
	return structuredErrorForStatus(resp.Status, message, resp.Headers)
}

func structuredErrorForStatus(status int, message string, headers map[string]string) structuredError {
	result := structuredError{
		Code:       "API_ERROR",
		Message:    message,
		HTTPStatus: status,
		RequestID:  requestID(headers),
	}
	switch status {
	case 400, 422:
		result.Code = "VALIDATION_ERROR"
		result.Hint = "Review the request arguments and payload"
	case 401:
		result.Code = "AUTHENTICATION_FAILED"
		result.Hint = "Run: dci login"
	case 403:
		result.Code = "PERMISSION_DENIED"
		result.Hint = "Check the active customer context and your DoiT permissions"
	case 404:
		result.Code = "RESOURCE_NOT_FOUND"
	case 409:
		result.Code = "RESOURCE_CONFLICT"
	case 429:
		result.Code = "RATE_LIMITED"
		result.Hint = "Retry after the server-provided delay"
		result.Retryable = true
		result.RetryAfter = firstHeaderValue(headers, "Retry-After", "X-Retry-In")
	default:
		if status >= 500 {
			result.Code = "API_SERVER_ERROR"
			result.Hint = "Retry the request; contact DoiT support if the error persists"
			result.Retryable = true
		}
	}
	return result
}

func responseErrorMessage(body interface{}) string {
	switch value := body.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	case map[string]interface{}:
		for _, key := range []string{"message", "detail", "error_description"} {
			if message, ok := value[key].(string); ok && strings.TrimSpace(message) != "" {
				return strings.TrimSpace(message)
			}
		}
		switch nested := value["error"].(type) {
		case string:
			return strings.TrimSpace(nested)
		case map[string]interface{}:
			for _, key := range []string{"message", "detail", "code"} {
				if message, ok := nested[key].(string); ok && strings.TrimSpace(message) != "" {
					return strings.TrimSpace(message)
				}
			}
		}
	}
	return ""
}

func requestID(headers map[string]string) string {
	for _, name := range []string{"X-Request-Id", "X-Doit-Trace", "Cf-Ray", "X-Cloud-Trace-Context", "Traceparent"} {
		if value := strings.TrimSpace(headerValue(headers, name)); value != "" {
			return value
		}
	}
	return ""
}

func firstHeaderValue(headers map[string]string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headerValue(headers, name)); value != "" {
			return value
		}
	}
	return ""
}

func writeStructuredError(writer io.Writer, detail structuredError) {
	agentErrorWritten = true
	_ = json.NewEncoder(writer).Encode(structuredErrorEnvelope{Error: detail})
}

func executeCLI() error {
	return executeCLIWith(cli.Run)
}

func executeCLIWith(run func() error) error {
	if !agentMode {
		return run()
	}

	cli.Root.SilenceErrors = true
	cli.Root.SilenceUsage = true

	originalStderr := cli.Stderr
	var capturedStderr bytes.Buffer
	cli.Stderr = &capturedStderr
	err := run()
	cli.Stderr = originalStderr

	if err == nil || agentErrorWritten {
		_, _ = io.Copy(originalStderr, &capturedStderr)
	}

	return err
}
