package main

import (
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func TestRegisterAICommandVisibleAtGA(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = oldRoot })

	registerAICommand(t.TempDir())
	var aiCommand *cobra.Command
	for _, command := range cli.Root.Commands() {
		if command.Name() == "ai" {
			aiCommand = command
		}
	}
	if aiCommand == nil {
		t.Fatal("ai command not registered")
	}
	if aiCommand.Hidden {
		t.Fatal("ai command still hidden — P3 unhides it")
	}
	if !strings.Contains(aiCommand.Long, "Anthropic") {
		t.Fatal("ai --help must carry the data-flow disclosure (D3)")
	}
	if flag := aiCommand.Flags().Lookup("yes"); flag == nil {
		t.Fatal("one-shot --yes flag missing")
	}
}

func TestAICommandExcludedFromMachineCatalog(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = oldRoot })

	registerAICommand(t.TempDir())
	cli.Root.AddCommand(&cobra.Command{Use: "status", Short: "Show status", Run: func(*cobra.Command, []string) {}})

	catalog := buildCommandCatalog(cli.API{})
	sawStatus := false
	for _, entry := range catalog.Commands {
		if entry.Path[0] == "ai" {
			t.Fatalf("ai leaked into the machine catalog: %v", entry.Path)
		}
		if entry.Path[0] == "status" {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatal("catalog walk broke: status missing")
	}
}
