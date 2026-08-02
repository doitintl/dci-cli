package main

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func TestDestructiveConfirmation(t *testing.T) {
	setDestructiveOperations([]cli.Operation{{Name: "delete-budget", Method: "DELETE"}})
	command := &cobra.Command{Use: "delete-budget"}

	t.Run("fails closed", func(t *testing.T) {
		viper.Reset()
		t.Setenv("DCI_CONFIRM_DESTRUCTIVE", "")
		if err := enforceDestructiveConfirmation(command, []string{"budget-1"}); err == nil {
			t.Fatal("expected confirmation error")
		}
	})

	t.Run("accepts yes flag", func(t *testing.T) {
		viper.Reset()
		viper.Set("agent-confirm-destructive", true)
		t.Cleanup(viper.Reset)
		if err := enforceDestructiveConfirmation(command, []string{"budget-1"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dry run does not execute", func(t *testing.T) {
		viper.Reset()
		viper.Set("agent-dry-run", true)
		t.Cleanup(viper.Reset)

		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		oldStdout := os.Stdout
		os.Stdout = writer
		err = enforceDestructiveConfirmation(command, []string{"budget-1"})
		writer.Close()
		os.Stdout = oldStdout
		output, readErr := io.ReadAll(reader)
		reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if command.RunE == nil {
			t.Fatal("dry run did not replace command execution")
		}
		var result dryRunResult
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		if !result.DryRun || result.Command != "delete-budget" {
			t.Fatalf("unexpected result: %+v", result)
		}
	})
}

func TestDestructiveConfirmationErrorMetadata(t *testing.T) {
	err := destructiveConfirmationError{Command: "delete-budget"}
	if err.ExitCode() != 30 || err.AgentErrorCode() != "DESTRUCTIVE_REQUIRES_CONFIRMATION" {
		t.Fatalf("unexpected error metadata: %+v", err)
	}
	if err.AgentErrorHint() == "" || err.AgentErrorRetryable() {
		t.Fatalf("unexpected agent metadata: %+v", err)
	}
}

func TestDestructiveCommandClassification(t *testing.T) {
	setDestructiveOperations([]cli.Operation{
		{Name: "delete-budget", Method: "DELETE"},
		{Name: "archive-contract-template", Method: "DELETE"},
		{Name: "id-of-ticket-tags-remove", Method: "DELETE"},
		{Name: "delete-datahub-events-by-filter", Method: "POST"},
		{Name: "list-budgets", Method: "GET"},
	})
	for _, name := range []string{"delete-budget", "archive-contract-template", "id-of-ticket-tags-remove", "delete-datahub-events-by-filter"} {
		if !isDestructiveCommand(&cobra.Command{Use: name}) {
			t.Errorf("%s was not classified as destructive", name)
		}
	}
	if isDestructiveCommand(&cobra.Command{Use: "list-budgets"}) {
		t.Fatal("read-only command was classified as destructive")
	}
}
