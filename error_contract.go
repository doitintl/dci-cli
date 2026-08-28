package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/rest-sh/restish/cli"
)

// The help-center CLI reference documents this exit-code contract and the
// HTTP-status mapping below (omni: .github/workflows/actions/generate-cli-docs,
// EXIT_CODE_REFERENCE / cliErrorForStatus). Update that generator when
// changing either.
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
	Code       string                 `json:"code"`
	Message    string                 `json:"message"`
	Hint       string                 `json:"hint,omitempty"`
	Retryable  bool                   `json:"retryable"`
	HTTPStatus int                    `json:"http_status,omitempty"`
	RequestID  string                 `json:"request_id,omitempty"`
	RetryAfter string                 `json:"retry_after,omitempty"`
	Resolved   *resolvedTargetPayload `json:"resolved,omitempty"`
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
		"unknown shorthand flag",
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
		result.Hint = authenticationFailureHint()
	case 403:
		result.Code = "PERMISSION_DENIED"
		result.Hint = authFailureHint(status)
	case 404:
		result.Code = "RESOURCE_NOT_FOUND"
		result.Hint = idShapedNotFoundHint()
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

func authenticationFailureHint() string {
	if strings.TrimSpace(os.Getenv(apiKeyEnvName)) != "" {
		return fmt.Sprintf("Check %s: the token may be malformed, expired, or revoked", apiKeyEnvName)
	}
	return loginHint()
}

// loginHint names a non-default API base alongside the login remedy: a 401
// with valid-looking local credentials usually means the CLI is pointed at an
// API the token is not valid for, and "run dci login" alone hides that.
func loginHint() string {
	if base, err := apiBase(); err == nil && base != defaultAPIBase {
		return fmt.Sprintf("Run: dci login (note: the API base is %s, not the default %s)", base, defaultAPIBase)
	}
	return "Run: dci login"
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

// resolvedCustomerContext holds the customer context applied to this run from
// the env var or config file (the -D flag override lives in
// customerContextFlagValue). Set by applyCustomerContext, reset per run().
var resolvedCustomerContext string

// activeCustomerContext returns the customer context in effect for the
// current invocation, preferring an explicit -D flag override.
func activeCustomerContext() string {
	if customerContextFlagValue != "" {
		return customerContextFlagValue
	}
	return resolvedCustomerContext
}

// authFailureHint returns a remediation hint for 401/403 responses that
// matches how the user actually authenticated: pointing an API-key user at
// `dci login` sends them to the wrong fix.
func authFailureHint(status int) string {
	if status == 403 {
		hint := "Check the active customer context and your DoiT permissions"
		if ctx := activeCustomerContext(); ctx != "" {
			hint = fmt.Sprintf("Check the active customer context (%q) and your DoiT permissions", ctx)
		}
		return hint
	}
	if os.Getenv("DCI_API_KEY") != "" {
		return "Check DCI_API_KEY: the token may be malformed, expired, or revoked (new tokens can take up to a minute to activate)"
	}
	return loginHint()
}

func writeStructuredError(writer io.Writer, detail structuredError) {
	agentErrorWritten = true
	_ = json.NewEncoder(writer).Encode(structuredErrorEnvelope{Error: detail})
}

func agentErrorContractEnabled() bool {
	return agentMode && agentUAMode != uaModeNonInteractive
}

func executeCLI() error {
	return executeCLIWith(cli.Run)
}

func executeCLIWith(run func() error) error {
	if agentErrorContractEnabled() {
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

	if agentUAMode == uaModeInteractive {
		// Human at a terminal: without this, a failed command prints the same
		// error three times (cobra's "Error:" + usage dump, restish's
		// "ERROR:" log line, and the final reporter). Silence cobra, capture
		// restish's stderr, and drop lines duplicating the returned error —
		// reportExecutionError then prints it exactly once.
		cli.Root.SilenceErrors = true
		cli.Root.SilenceUsage = true

		originalStderr := cli.Stderr
		var capturedStderr bytes.Buffer
		cli.Stderr = &capturedStderr
		err := run()
		cli.Stderr = originalStderr

		emitStderrWithoutDuplicateError(originalStderr, capturedStderr.String(), err)
		return err
	}

	// Non-interactive without the agent contract (pipes, CI): preserve the
	// framework output untouched.
	return run()
}

// emitStderrWithoutDuplicateError re-emits captured stderr, skipping lines
// that merely repeat the returned error (restish logs "ERROR: Error: <msg>"
// for every failed run). Other stderr content — warnings, API error details —
// passes through unchanged.
func emitStderrWithoutDuplicateError(writer io.Writer, captured string, err error) {
	if captured == "" {
		return
	}
	if err == nil {
		_, _ = io.WriteString(writer, captured)
		return
	}
	message := err.Error()
	for _, line := range strings.Split(strings.TrimRight(captured, "\n"), "\n") {
		trimmed := strings.TrimSpace(stripANSI(line))
		if trimmed != "" && strings.HasSuffix(trimmed, message) {
			if rest := strings.TrimSuffix(trimmed, message); strings.TrimSpace(rest) == "" || isErrorPrefix(rest) {
				continue
			}
		}
		_, _ = io.WriteString(writer, line+"\n")
	}
}

func isErrorPrefix(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, prefix := range []string{"error:", "error: error:", "!"} {
		if s == prefix {
			return true
		}
	}
	return false
}

// ansiPattern matches terminal escape sequences (restish colors its log tags).
var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// looksLikeArgvRejection reports whether an error (or a failed dispatch's
// output) is cobra's own argument/flag rejection — the only usage failures
// where a reconstructed usage line answers the error. exitUsage is shared by
// richer domain errors (an ambiguous name listing its candidates, a
// body-validation message naming the field) that already explain themselves;
// a usage line after those would misdirect the reader toward the argument
// count.
func looksLikeArgvRejection(text string) bool {
	lower := strings.ToLower(text)
	for _, fragment := range []string{
		"accepts ",
		"requires at least",
		"requires exactly",
		"unknown flag",
		"unknown shorthand flag",
	} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// argvUsageTrailer reconstructs a rejected invocation's usage from the live
// cobra tree, as the lines to print after the rejection itself. argv is the
// invocation after the `dci` word (the same shape the session dispatches).
// sessionSpelling picks the surface: "/add-ticket-tags …" for the `dci ai`
// session, "dci add-ticket-tags …" for the shell — the shell previously
// printed nothing here while the session printed its usage line, which is
// exactly the inconsistency this trailer removes.
//
// Cobra's Use names only the path parameters, so for an operation that takes
// a request body the one-liner looked complete while the required body was
// missing entirely. When the command's help declares a request schema (or
// carries a spec example), the trailer says so: a [body fields] marker on
// the usage line, then the spec's own example when there is one, or the
// schema's top-level field list with its required markers.
func argvUsageTrailer(argv []string, sessionSpelling bool) string {
	if len(argv) == 0 || cli.Root == nil {
		return ""
	}
	command := findChildCommand(findDCICommand(), argv[0])
	if command == nil {
		command = findChildCommand(cli.Root, argv[0])
	}
	if command == nil {
		return ""
	}
	// Descend while the following words name subcommands ("beta run-report",
	// "customer-context set"); the matched words become the usage's prefix.
	matched := 1
	for matched < len(argv) {
		child := findChildCommand(command, argv[matched])
		if child == nil {
			break
		}
		command = child
		matched++
	}
	if len(command.Commands()) > 0 || strings.TrimSpace(command.Use) == "" {
		return ""
	}
	prefix := "dci "
	if sessionSpelling {
		prefix = "/"
	}
	if matched > 1 {
		prefix += strings.Join(argv[:matched-1], " ") + " "
	}
	usage := "usage: " + prefix + command.Use

	bodyFields := requestSchemaTopLevelFieldList(command.Long)
	example := firstUsageExample(command.Example, sessionSpelling)
	if len(bodyFields) == 0 && example == "" {
		return usage
	}
	lines := []string{usage + " [body fields]"}
	if example != "" {
		lines = append(lines, "example: "+example)
	} else if len(bodyFields) > 0 {
		lines = append(lines, "body fields (* = required): "+bodyFieldSummary(bodyFields))
	}
	return strings.Join(lines, "\n")
}

// firstUsageExample extracts the first spec-provided example invocation from
// a command's cobra Example block (restish renders one "  dci <use> <body>"
// line per spec example), respelled for the session when asked.
func firstUsageExample(example string, sessionSpelling bool) string {
	for _, line := range strings.Split(example, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if sessionSpelling {
			if rest, ok := strings.CutPrefix(line, "dci "); ok {
				line = "/" + rest
			}
		}
		return line
	}
	return ""
}

// bodyFieldSummary joins the schema's top-level fields for the trailer,
// capped so a wide schema doesn't turn the trailer into a second help page.
func bodyFieldSummary(fields []string) string {
	const maxListedBodyFields = 10
	if len(fields) > maxListedBodyFields {
		fields = append(append([]string{}, fields[:maxListedBodyFields]...), "…")
	}
	return strings.Join(fields, ", ")
}

// shellInvocationArgv strips the process word — and the "dci" group word
// normalizeArgs inserts before API operations — off os.Args, yielding the
// argv shape argvUsageTrailer expects. Local root commands
// (customer-context, login, …) arrive without the inserted group word.
func shellInvocationArgv(args []string) []string {
	if len(args) < 2 {
		return nil
	}
	if args[1] == "dci" {
		return args[2:]
	}
	return args[1:]
}
