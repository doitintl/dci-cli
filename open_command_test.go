package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func TestLooksLikeCustomerID(t *testing.T) {
	if !looksLikeCustomerID("RSTDkHhaoGWwOEvlYlHyBUhm") {
		t.Error("customer-ID shaped context rejected")
	}
	if looksLikeCustomerID("acme.com") {
		t.Error("domain context accepted as customer ID")
	}
	if looksLikeCustomerID("foo") {
		t.Error("short token accepted as customer ID")
	}
}

func TestTokenCustomerID(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"customerId":"AbCdEfGhIjKlMnOpQrSt","sub":"user@example.com"}`))
	t.Setenv("DCI_API_KEY", "header."+payload+".signature")
	if got := tokenCustomerID(); got != "AbCdEfGhIjKlMnOpQrSt" {
		t.Errorf("tokenCustomerID = %q, want claim value", got)
	}

	payload = base64.RawURLEncoding.EncodeToString([]byte(`{"CustomerID":"LegacyCustomerID123"}`))
	t.Setenv("DCI_API_KEY", "header."+payload+".signature")
	if got := tokenCustomerID(); got != "LegacyCustomerID123" {
		t.Errorf("legacy tokenCustomerID = %q, want claim value", got)
	}

	t.Setenv("DCI_API_KEY", "not-a-jwt")
	if got := tokenCustomerID(); got != "" {
		t.Errorf("malformed token produced %q, want empty", got)
	}
}

func TestConsoleCustomerIDResolvesOAuthSession(t *testing.T) {
	oldContext := resolvedCustomerContext
	oldFlag := customerContextFlagValue
	oldResolver := consoleCustomerIDResolver
	resolvedCustomerContext = ""
	customerContextFlagValue = ""
	t.Cleanup(func() {
		resolvedCustomerContext = oldContext
		customerContextFlagValue = oldFlag
		consoleCustomerIDResolver = oldResolver
	})

	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"Key":"access-key","UserID":"user-id","DoitOwner":true,"DoitEmployee":false}`))
	t.Setenv("DCI_API_KEY", "header."+payload+".signature")
	consoleCustomerIDResolver = func(context string) (string, error) {
		if context != "" {
			t.Fatalf("resolver context = %q, want empty", context)
		}
		return "ResolvedCustomerID123", nil
	}
	customerID, err := consoleCustomerID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if customerID != "ResolvedCustomerID123" {
		t.Errorf("consoleCustomerID = %q, want resolved customer", customerID)
	}
}

func TestConsoleCustomerIDResolvesDomainContext(t *testing.T) {
	oldContext := resolvedCustomerContext
	oldFlag := customerContextFlagValue
	oldResolver := consoleCustomerIDResolver
	resolvedCustomerContext = "acme.com"
	customerContextFlagValue = ""
	t.Cleanup(func() {
		resolvedCustomerContext = oldContext
		customerContextFlagValue = oldFlag
		consoleCustomerIDResolver = oldResolver
	})

	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"CustomerID":"DifferentCustomerID123"}`))
	t.Setenv("DCI_API_KEY", "header."+payload+".signature")
	consoleCustomerIDResolver = func(context string) (string, error) {
		if context != "acme.com" {
			t.Fatalf("resolver context = %q, want acme.com", context)
		}
		return "ContextCustomerID123", nil
	}
	got, err := consoleCustomerID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != "ContextCustomerID123" {
		t.Errorf("consoleCustomerID = %q, want context customer", got)
	}
}

func TestConsoleResourcePaths(t *testing.T) {
	want := map[string]string{
		"report":     "https://console.doit.com/customers/CustomerIdentifier123/analyze/reports/resource-id",
		"budget":     "https://console.doit.com/customers/CustomerIdentifier123/monitor/budgets/resource-id",
		"allocation": "https://console.doit.com/customers/CustomerIdentifier123/operate/allocations/resource-id",
	}
	for resource, expectedURL := range want {
		actualURL, ok := consoleResourceURL("CustomerIdentifier123", resource, "resource-id")
		if !ok {
			t.Fatalf("resource %q not supported", resource)
		}
		if actualURL != expectedURL {
			t.Errorf("%s URL = %q, want %q", resource, actualURL, expectedURL)
		}
	}
}

func TestConsoleURLForArgsDoesNotResolveHome(t *testing.T) {
	oldResolver := consoleCustomerIDResolver
	consoleCustomerIDResolver = func(context string) (string, error) {
		t.Fatalf("customer resolver called with context %q", context)
		return "", nil
	}
	t.Cleanup(func() { consoleCustomerIDResolver = oldResolver })

	consoleURL, err := consoleURLForArgs(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if consoleURL != consoleBaseURL {
		t.Errorf("console URL = %q, want %q", consoleURL, consoleBaseURL)
	}
}

func TestOpenJoinableArgs(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
		env  string
		want bool
	}{
		{name: "unquoted multi-word name", args: []string{"report", "New", "SKU", "Changes"}, want: true},
		{name: "two args keep range validation", args: []string{"report", "Monthly Spend"}, want: false},
		{name: "unknown resource", args: []string{"widget", "a", "b"}, want: false},
		{name: "flag-shaped word", args: []string{"report", "a", "--json"}, want: false},
		{name: "resolution off", args: []string{"report", "a", "b"}, env: "1", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DCI_NO_RESOLVE", testCase.env)
			if got := openJoinableArgs(testCase.args); got != testCase.want {
				t.Errorf("openJoinableArgs(%v) = %v, want %v", testCase.args, got, testCase.want)
			}
		})
	}
}

func TestOpenArgsAcceptUnquotedMultiWordName(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = oldRoot })
	registerOpenCommand(t.TempDir())

	var openCommand *cobra.Command
	for _, command := range cli.Root.Commands() {
		if command.Name() == "open" {
			openCommand = command
		}
	}
	if openCommand == nil {
		t.Fatal("open command not registered")
	}

	t.Setenv("DCI_NO_RESOLVE", "")
	if err := openCommand.Args(openCommand, []string{"report", "New", "SKU", "Changes"}); err != nil {
		t.Errorf("multi-word name rejected: %v", err)
	}
	if err := openCommand.Args(openCommand, []string{"report", "a", "b", "--flag"}); err == nil {
		t.Error("flag-shaped surplus accepted")
	}
}

func TestConsoleURLForArgsJoinsSpaceSplitName(t *testing.T) {
	oldContext := resolvedCustomerContext
	oldFlag := customerContextFlagValue
	resolvedCustomerContext = "CustomerIdentifier123"
	customerContextFlagValue = ""
	t.Cleanup(func() {
		resolvedCustomerContext = oldContext
		customerContextFlagValue = oldFlag
	})
	t.Setenv("DCI_NO_RESOLVE", "")
	stubNameResolution(t, resolverListResult{entries: namedEntries("Monthly Spend")}, nil)

	consoleURL, err := consoleURLForArgs(t.TempDir(), []string{"report", "monthly", "spend"})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://console.doit.com/customers/CustomerIdentifier123/analyze/reports/id-0"
	if consoleURL != want {
		t.Errorf("console URL = %q, want %q", consoleURL, want)
	}
}

func TestResolveConsoleCustomerID(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/auth/v1/validate" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer oauth-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-Tenant-Id") != "acme.com" {
			t.Errorf("tenant header = %q", request.Header.Get("X-Tenant-Id"))
		}
		if request.URL.Query().Get("customerContext") != "acme.com" {
			t.Errorf("query = %v", request.URL.Query())
		}
		writer.Header().Set("X-DoiT-Customer-ID", "ResolvedCustomerID123")
		_, _ = fmt.Fprint(writer, `{}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("DCI_API_BASE_URL", server.URL)
	t.Setenv("DCI_API_KEY", "oauth-token")

	oldClient := consoleHTTPClient
	consoleHTTPClient = server.Client()
	t.Cleanup(func() { consoleHTTPClient = oldClient })

	customerID, err := resolveConsoleCustomerID("acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if customerID != "ResolvedCustomerID123" {
		t.Errorf("customer ID = %q", customerID)
	}
}

func TestResolveConsoleCustomerIDPreservesHTTPClassification(t *testing.T) {
	tests := []struct {
		status    int
		exitCode  int
		errorCode string
	}{
		{status: http.StatusUnauthorized, exitCode: exitAuthentication, errorCode: "AUTHENTICATION_FAILED"},
		{status: http.StatusForbidden, exitCode: exitAuthorization, errorCode: "PERMISSION_DENIED"},
		{status: http.StatusInternalServerError, exitCode: exitServer, errorCode: "API_SERVER_ERROR"},
	}

	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(test.status)
			}))
			t.Cleanup(server.Close)
			t.Setenv("DCI_API_BASE_URL", server.URL)
			t.Setenv("DCI_API_KEY", "oauth-token")

			oldClient := consoleHTTPClient
			consoleHTTPClient = server.Client()
			t.Cleanup(func() { consoleHTTPClient = oldClient })

			_, err := resolveConsoleCustomerID("")
			if err == nil {
				t.Fatal("expected HTTP error")
			}
			if got := exitCodeForExecutionError(err, 0); got != test.exitCode {
				t.Errorf("exit code = %d, want %d", got, test.exitCode)
			}
			if got := structuredErrorForExecution(err, 0).Code; got != test.errorCode {
				t.Errorf("error code = %q, want %q", got, test.errorCode)
			}
		})
	}
}
