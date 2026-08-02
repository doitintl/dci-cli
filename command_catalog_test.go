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
			QueryParams: []*cli.Param{
				{Name: "force", Type: "boolean", Description: "Force deletion"},
			},
		},
		{Name: "list-budgets", Short: "List budgets", Method: "GET"},
	}}
	catalog := buildCommandCatalog(api)
	if catalog.Version != catalogSchemaVersion || len(catalog.Commands) != 3 {
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
}
