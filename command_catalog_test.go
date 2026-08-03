package main

import (
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
		},
		{Name: "list-budgets", Short: "List budgets", Method: "GET"},
	}}
	catalog := buildCommandCatalog(api)
	if catalog.Version != catalogSchemaVersion || len(catalog.Commands) != 6 {
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
	for _, flag := range cancelInviteEntry.Flags {
		if flag.Name == "--dry-run" {
			dryRunFlags++
			if flag.Description != "Use the API simulation" {
				t.Fatalf("dry-run description = %q", flag.Description)
			}
		}
	}
	if dryRunFlags != 1 {
		t.Fatalf("dry-run flags = %d", dryRunFlags)
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
