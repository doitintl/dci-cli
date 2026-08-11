package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"CustomerID":"AbCdEfGhIjKlMnOpQrSt","sub":"user@example.com"}`))
	t.Setenv("DCI_API_KEY", "header."+payload+".signature")
	if got := tokenCustomerID(); got != "AbCdEfGhIjKlMnOpQrSt" {
		t.Errorf("tokenCustomerID = %q, want claim value", got)
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

func TestResolveConsoleCustomerID(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer oauth-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-Tenant-Id") != "acme.com" {
			t.Errorf("tenant header = %q", request.Header.Get("X-Tenant-Id"))
		}
		if request.URL.Query().Get("customerContext") != "acme.com" || request.URL.Query().Get("maxResults") != "1" {
			t.Errorf("query = %v", request.URL.Query())
		}
		_, _ = fmt.Fprint(writer, `{"reports":[{"urlUI":"https://console.doit.com/customers/ResolvedCustomerID123/analyze/reports/report-id"}]}`)
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
