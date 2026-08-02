package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var destructiveActionName string

func resetDestructiveContractState() {
	destructiveActionName = ""
}

type destructiveConfirmationError struct {
	Command string
}

func (e destructiveConfirmationError) Error() string {
	return fmt.Sprintf("%s is destructive; re-run with --yes or set DCI_CONFIRM_DESTRUCTIVE=1", e.Command)
}

func (e destructiveConfirmationError) ExitCode() int {
	return 30
}

func (e destructiveConfirmationError) AgentErrorCode() string {
	return "DESTRUCTIVE_REQUIRES_CONFIRMATION"
}

func (e destructiveConfirmationError) AgentErrorHint() string {
	return "Re-run with --yes after reviewing the operation, or use --dry-run first"
}

func (e destructiveConfirmationError) AgentErrorRetryable() bool {
	return false
}

type dryRunResult struct {
	DryRun    bool     `json:"dry_run"`
	Command   string   `json:"command"`
	Arguments []string `json:"arguments,omitempty"`
}

type actionSummary struct {
	Command string `json:"command"`
	Status  string `json:"status"`
}

type actionResult struct {
	Action actionSummary `json:"action"`
	Result interface{}   `json:"result,omitempty"`
}

type destructiveActionSummaryGuard struct {
	next cli.ResponseFormatter
}

func (guard destructiveActionSummaryGuard) Format(response cli.Response) error {
	if destructiveActionName != "" && response.Status >= 200 && response.Status < 300 {
		response.Body = actionResult{
			Action: actionSummary{Command: destructiveActionName, Status: "completed"},
			Result: response.Body,
		}
	}
	return guard.next.Format(response)
}

func installDestructiveActionSummaryGuard() {
	if cli.Formatter != nil {
		cli.Formatter = destructiveActionSummaryGuard{next: cli.Formatter}
	}
}

func isDestructiveCommand(command *cobra.Command) bool {
	name := command.Name()
	for _, prefix := range []string{"archive-", "delete-", "remove-", "revoke-", "purge-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return strings.HasSuffix(name, "-remove")
}

func enforceDestructiveConfirmation(command *cobra.Command, args []string) error {
	if !isDestructiveCommand(command) {
		return nil
	}
	destructiveActionName = command.Name()
	if viper.GetBool("agent-dry-run") {
		result := dryRunResult{DryRun: true, Command: command.Name(), Arguments: args}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
		destructiveActionName = ""
		command.Run = nil
		command.RunE = func(command *cobra.Command, args []string) error { return nil }
		return nil
	}
	confirmed := viper.GetBool("agent-confirm-destructive")
	if !confirmed {
		confirmed, _ = parseBoolish(os.Getenv("DCI_CONFIRM_DESTRUCTIVE"))
	}
	if !confirmed {
		destructiveActionName = ""
		return destructiveConfirmationError{Command: command.Name()}
	}
	return nil
}
