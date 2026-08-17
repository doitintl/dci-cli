package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

// consoleBaseURL is where deep links land. The API host is configurable via
// DCI_API_BASE_URL for testing, but console links always target production.
const consoleBaseURL = "https://console.doit.com"

var consoleResourcePaths = map[string]string{
	"report":     "analyze/reports",
	"budget":     "monitor/budgets",
	"allocation": "operate/allocations",
}

var consoleCustomerIDResolver = resolveConsoleCustomerID
var consoleHTTPClient = &http.Client{Timeout: 10 * time.Second}

func registerOpenCommand(configDir string) {
	cmd := &cobra.Command{
		Use:   "open [resource] [id]",
		Short: "Open the DoiT console (optionally a specific report, budget, or allocation)",
		Long: "Deep-links into the DoiT console for the active customer: `dci open` lands on the console home, " +
			"`dci open report <id>` (also: budget, allocation) opens the resource. " +
			"Opens a browser in interactive use; prints the URL in agent or non-interactive mode.",
		Args: func(cmd *cobra.Command, args []string) error {
			// An unquoted multi-word resource name arrives word-split by the
			// shell; accept the surplus words when they can only be a name.
			if openJoinableArgs(args) {
				return nil
			}
			return cobra.RangeArgs(0, 2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			consoleURL, err := consoleURLForArgs(configDir, args)
			if err != nil {
				return err
			}

			if agentMode || !term.IsTerminal(int(os.Stdout.Fd())) {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), consoleURL)
				return err
			}
			if err := openInBrowser(consoleURL); err != nil {
				_, writeErr := fmt.Fprintln(cmd.OutOrStdout(), consoleURL)
				return writeErr
			}
			return nil
		},
	}
	cli.Root.AddCommand(cmd)
}

func consoleURLForArgs(configDir string, args []string) (string, error) {
	if len(args) == 0 {
		return consoleBaseURL, nil
	}
	resources := make([]string, 0, len(consoleResourcePaths))
	for resource := range consoleResourcePaths {
		resources = append(resources, resource)
	}
	sort.Strings(resources)
	if len(args) == 1 {
		return "", fmt.Errorf("usage: dci open <%s> <id>", strings.Join(resources, "|"))
	}
	customerID, err := consoleCustomerID(configDir)
	if err != nil {
		return "", err
	}
	resourceID, err := resolveOpenResourceID(args[0], openResourceArgument(args), configDir)
	if err != nil {
		return "", err
	}
	resourceURL, ok := consoleResourceURL(customerID, args[0], resourceID)
	if !ok {
		return "", fmt.Errorf("unknown resource %q (supported: %s)", args[0], strings.Join(resources, ", "))
	}
	return resourceURL, nil
}

func consoleResourceURL(customerID, resource, resourceID string) (string, bool) {
	path, ok := consoleResourcePaths[strings.ToLower(resource)]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s/customers/%s/%s/%s", consoleBaseURL, customerID, path, resourceID), true
}

func consoleCustomerID(configDir string) (string, error) {
	context := activeCustomerContext()
	if context == "" {
		context = readCustomerContext(configDir)
	}
	if context != "" {
		if looksLikeCustomerID(context) {
			return context, nil
		}
		return consoleCustomerIDResolver(context)
	}
	if customerID := tokenCustomerID(); customerID != "" {
		return customerID, nil
	}
	return consoleCustomerIDResolver("")
}

func looksLikeCustomerID(s string) bool {
	return len(s) >= 16 && !strings.Contains(s, ".")
}

func tokenCustomerID() string {
	token := authenticationToken()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		CustomerID       string `json:"customerId"`
		LegacyCustomerID string `json:"CustomerID"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if claims.CustomerID == "" {
		return claims.LegacyCustomerID
	}
	return claims.CustomerID
}

func authenticationToken() string {
	if token := os.Getenv("DCI_API_KEY"); token != "" {
		return token
	}
	if cli.Cache == nil {
		return ""
	}
	profile := viper.GetString("rsh-profile")
	if profile == "" {
		profile = "default"
	}
	return cli.Cache.GetString("dci:" + profile + ".token")
}

func resolveConsoleCustomerID(context string) (string, error) {
	token := authenticationToken()
	if token == "" {
		return "", fmt.Errorf("cannot determine the customer for console links: authenticate first")
	}
	base, err := apiBase()
	if err != nil {
		return "", err
	}
	requestURL, err := url.Parse(base + "/auth/v1/validate")
	if err != nil {
		return "", err
	}
	query := requestURL.Query()
	if context != "" {
		query.Set("customerContext", context)
	}
	requestURL.RawQuery = query.Encode()

	request, err := http.NewRequest(http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", buildUserAgent(agentUAMode))
	if context != "" {
		request.Header.Set("X-Tenant-Id", context)
	}
	response, err := consoleHTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("cannot resolve the active customer: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", consoleCustomerResolutionError(response)
	}
	if customerID := strings.TrimSpace(response.Header.Get("X-DoiT-Customer-ID")); looksLikeCustomerID(customerID) {
		return customerID, nil
	}
	return "", fmt.Errorf("cannot resolve the active customer: the API did not return a customer ID; set a customer-ID context with dci customer-context set <customer-id>")
}

type consoleAPIError struct {
	status  int
	message string
	headers map[string]string
}

func (err consoleAPIError) Error() string {
	return err.message
}

func (err consoleAPIError) ExitCode() int {
	return exitCodeForHTTPStatus(err.status)
}

func (err consoleAPIError) StructuredError() structuredError {
	return structuredErrorForStatus(err.status, err.message, err.headers)
}

func diagnosticResponseHeaders(response *http.Response) map[string]string {
	headers := make(map[string]string)
	for _, name := range []string{"X-Request-Id", "X-Doit-Trace", "Cf-Ray", "X-Cloud-Trace-Context", "Traceparent", "Retry-After", "X-Retry-In"} {
		if value := response.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	return headers
}

func consoleCustomerResolutionError(response *http.Response) error {
	return consoleAPIError{
		status:  response.StatusCode,
		message: fmt.Sprintf("cannot resolve the active customer: API returned %s", response.Status),
		headers: diagnosticResponseHeaders(response),
	}
}

func openInBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
