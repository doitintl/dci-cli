package main

import (
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func TestUnknownCommandError(t *testing.T) {
	errorDetail := unknownCommandError{Command: "list-bugets", Suggestions: []string{"list-budgets"}}
	if errorDetail.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", errorDetail.ExitCode())
	}
	if !strings.Contains(errorDetail.Error(), "Did you mean this?\n  list-budgets") {
		t.Fatalf("human error omitted suggestion: %s", errorDetail.Error())
	}
	if errorDetail.AgentErrorCode() != "UNKNOWN_COMMAND" || !strings.Contains(errorDetail.AgentErrorHint(), "list-budgets") {
		t.Fatalf("agent error = %+v", errorDetail)
	}
}

func TestInstallUnknownCommandHandler(t *testing.T) {
	oldRoot := cli.Root
	root := &cobra.Command{Use: "dci"}
	apiCommand := &cobra.Command{Use: "dci"}
	apiCommand.AddCommand(&cobra.Command{Use: "list-budgets", Run: func(*cobra.Command, []string) {}})
	root.AddCommand(apiCommand)
	cli.Root = root
	t.Cleanup(func() { cli.Root = oldRoot })

	installUnknownCommandHandler()
	err := apiCommand.Args(apiCommand, []string{"list-bugets"})
	unknown, ok := err.(unknownCommandError)
	if !ok {
		t.Fatalf("error = %T, want unknownCommandError", err)
	}
	if len(unknown.Suggestions) != 1 || unknown.Suggestions[0] != "list-budgets" {
		t.Fatalf("suggestions = %v", unknown.Suggestions)
	}
	if !root.SilenceErrors || !root.SilenceUsage {
		t.Fatal("framework error and usage output remain enabled")
	}
}
