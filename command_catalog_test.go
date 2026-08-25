package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func TestBuildCommandCatalog(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	cli.Root.AddCommand(&cobra.Command{Use: "status", Short: "Show status", Run: func(*cobra.Command, []string) {}})
	t.Cleanup(func() { cli.Root = oldRoot })

	api := cli.API{Operations: []cli.Operation{
		{
			Name:   "delete-budget",
			Short:  "Delete a budget",
			Method: "DELETE",
			PathParams: []*cli.Param{
				{Name: "id", Type: "string", Description: "Budget ID", Example: "budget-1"},
			},
			QueryParams: []*cli.Param{
				{Name: "force", Type: "boolean", Description: "Force deletion"},
			},
		},
		{Name: "create-budget", Short: "Create a budget", Method: "POST", BodyMediaType: "application/json"},
		{Name: "delete-datahub-events-by-filter", Method: "POST", BodyMediaType: "application/json"},
		{
			Name:   "cancel-invite",
			Method: "POST",
			QueryParams: []*cli.Param{
				{Name: "dryRun", Type: "boolean", Description: "Use the API simulation"},
			},
			HeaderParams: []*cli.Param{
				{Name: "Idempotency-Key", Type: "string", Description: "Idempotency key"},
			},
		},
		{
			Name:          "import-cloudflow-flow",
			Method:        "POST",
			BodyMediaType: "application/json",
			QueryParams: []*cli.Param{
				{Name: "dryRun", Type: "boolean", Description: "Use the API simulation"},
			},
			HeaderParams: []*cli.Param{
				{Name: "Idempotency-Key", Type: "string", Description: "Idempotency key"},
			},
		},
		{Name: "list-budgets", Short: "List budgets", Method: "GET"},
	}}
	catalog := buildCommandCatalog(api)
	if catalog.Version != catalogSchemaVersion || len(catalog.Commands) != 7 {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	var deleteEntry commandCatalogEntry
	for _, entry := range catalog.Commands {
		if strings.Join(entry.Path, " ") == "delete-budget" {
			deleteEntry = entry
		}
	}
	if !deleteEntry.Destructive || !deleteEntry.RequiresAuth {
		t.Fatalf("delete entry metadata = %+v", deleteEntry)
	}
	foundConfirmation := false
	for _, flag := range deleteEntry.Flags {
		if flag.Name == "--yes" {
			foundConfirmation = true
			if flag.SafetyRole != "destructive_confirmation" {
				t.Fatalf("yes safety role = %q", flag.SafetyRole)
			}
		}
	}
	if !foundConfirmation {
		t.Fatal("catalog omitted --yes")
	}
	if len(deleteEntry.Arguments) != 1 || deleteEntry.Arguments[0].Name != "id" || !deleteEntry.Arguments[0].Required {
		t.Fatalf("delete arguments = %+v", deleteEntry.Arguments)
	}

	var createEntry commandCatalogEntry
	for _, entry := range catalog.Commands {
		if strings.Join(entry.Path, " ") == "create-budget" {
			createEntry = entry
		}
	}
	if len(createEntry.Arguments) != 1 || createEntry.Arguments[0].Location != "body" || createEntry.Arguments[0].MediaType != "application/json" {
		t.Fatalf("create arguments = %+v", createEntry.Arguments)
	}

	var cancelInviteEntry commandCatalogEntry
	for _, entry := range catalog.Commands {
		if strings.Join(entry.Path, " ") == "cancel-invite" {
			cancelInviteEntry = entry
		}
	}
	dryRunFlags := 0
	foundRequiredIdempotencyKey := false
	for _, flag := range cancelInviteEntry.Flags {
		if flag.Name == "--dry-run" {
			dryRunFlags++
			if flag.Description != "Use the API simulation" {
				t.Fatalf("dry-run description = %q", flag.Description)
			}
			if flag.SafetyRole != "preview_before_execution" {
				t.Fatalf("dry-run safety role = %q", flag.SafetyRole)
			}
		}
		if flag.Name == "--idempotency-key" && flag.Required {
			foundRequiredIdempotencyKey = true
		}
	}
	if dryRunFlags != 1 {
		t.Fatalf("dry-run flags = %d", dryRunFlags)
	}
	if !foundRequiredIdempotencyKey {
		t.Fatal("catalog did not mark --idempotency-key as required")
	}

	var importEntry commandCatalogEntry
	for _, entry := range catalog.Commands {
		if strings.Join(entry.Path, " ") == "import-cloudflow-flow" {
			importEntry = entry
		}
	}
	foundRequiredImportIdempotencyKey := false
	for _, flag := range importEntry.Flags {
		if flag.Name == "--idempotency-key" && flag.Required {
			foundRequiredImportIdempotencyKey = true
		}
	}
	if !foundRequiredImportIdempotencyKey {
		t.Fatal("catalog did not mark --idempotency-key as required for import-cloudflow-flow")
	}
}

func TestCatalogAndRuntimeDestructiveClassificationMatch(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = oldRoot })

	operations := []cli.Operation{
		{Name: "delete-alert", Method: "DELETE"},
		{Name: "delete-datahub-events-by-filter", Method: "POST"},
		{Name: "cancel-contract", Method: "POST"},
		{Name: "list-alerts", Method: "GET"},
	}
	setDestructiveOperations(operations)
	t.Cleanup(resetDestructiveContractState)
	catalog := buildCommandCatalog(cli.API{Operations: operations})
	entries := make(map[string]commandCatalogEntry, len(catalog.Commands))
	for _, entry := range catalog.Commands {
		entries[strings.Join(entry.Path, " ")] = entry
	}
	for _, operation := range operations {
		command := &cobra.Command{Use: operation.Name}
		if got, want := isDestructiveCommand(command), entries[operation.Name].Destructive; got != want {
			t.Errorf("%s runtime destructive = %t, catalog = %t", operation.Name, got, want)
		}
	}
}

// TestFetchOpenAPISpecToleratesNilCommandContext guards against a real bug
// found while manually verifying help_context.go against the live DCI API:
// cobra.Command.Context() is nil until Execute()/ExecuteContext() runs, and
// http.NewRequestWithContext(nil, ...) fails with an opaque "nil Context"
// error instead of panicking — which loadHelpContext's error handling then
// silently swallowed, making tag descriptions and flag examples vanish with
// no visible failure. fetchOpenAPISpec must fall back to context.Background().
func TestFetchOpenAPISpecToleratesNilCommandContext(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("openapi: 3.0.1\n"))
	}))
	t.Cleanup(server.Close)

	previousClient := specHTTPClient
	specHTTPClient = server.Client()
	t.Cleanup(func() { specHTTPClient = previousClient })

	// A bare *cobra.Command, as loadHelpContext(&cobra.Command{}) or any
	// caller invoked before cobra's Execute() dispatch would pass — ctx is
	// nil, not context.Background().
	response, err := fetchOpenAPISpec(&cobra.Command{}, server.URL)
	if err != nil {
		t.Fatalf("fetchOpenAPISpec with nil command context: %v", err)
	}
	response.Body.Close()
}

// TestFetchOpenAPISpecHasATimeout guards against a regression a Claude Code
// review caught on the PR: this call now runs from --help (main.go's
// setupCompletion HelpFunc / help_context.go), a path that used to be purely
// local and instant. http.DefaultClient has no timeout, so an unreachable
// API host would hang --help on the OS-level DNS/TCP timeout (30-120+s)
// instead of rendering help immediately, unlike every other network client
// in this codebase (name_resolution.go, open_command.go, update.go).
func TestFetchOpenAPISpecHasATimeout(t *testing.T) {
	if specHTTPClient.Timeout <= 0 {
		t.Fatalf("specHTTPClient has no timeout (%v); --help must not be able to hang on an unreachable API host", specHTTPClient.Timeout)
	}
}
