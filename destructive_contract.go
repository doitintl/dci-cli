package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	destructiveActionName   string
	destructiveActionDryRun bool
	destructiveCommandSet   = map[string]bool{}
	destructiveMetadataRead bool
	destructiveMetadataErr  error
	loadOperationAPI        = cli.Load
)

// Non-DELETE operations belong here only when they can revoke access, alter financial contracts,
// or disable ingestion or automation. Routine lifecycle and cosmetic updates remain ungated.
var explicitlyDestructiveOperations = map[string]bool{
	"accept-budget-suggestion":        true,
	"activate-contract":               true,
	"assign-objects-to-label":         true,
	"cancel-contract":                 true,
	"cancel-invite":                   true,
	"delete-datahub-events-by-filter": true,
	"dismiss-budget-suggestion":       true,
	"trigger-cloudflow-webhook":       true,
	"update-aws-feature":              true,
	"update-cloudflow-connection":     true,
	"update-contract":                 true,
	"update-contract-template":        true,
	"update-resource-permission":      true,
	"update-user":                     true,
}

func resetDestructiveContractState() {
	destructiveActionName = ""
	destructiveActionDryRun = false
	destructiveCommandSet = map[string]bool{}
	destructiveMetadataRead = false
	destructiveMetadataErr = nil
}

type destructiveConfirmationError struct {
	Command string
}

func (e destructiveConfirmationError) Error() string {
	return fmt.Sprintf("%s requires confirmation; re-run with --yes or set DCI_CONFIRM_DESTRUCTIVE=1", e.Command)
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
	DryRun  bool   `json:"dry_run,omitempty"`
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
		if isErrorResponseBody(response) {
			return guard.next.Format(response)
		}
		status := "completed"
		if destructiveActionDryRun {
			status = "simulated"
		}
		response.Body = actionResult{
			Action: actionSummary{Command: destructiveActionName, Status: status, DryRun: destructiveActionDryRun},
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

func isDestructiveOperation(operation cli.Operation) bool {
	return strings.EqualFold(operation.Method, "DELETE") || explicitlyDestructiveOperations[operation.Name]
}

func setDestructiveOperations(operations []cli.Operation) {
	destructiveCommandSet = make(map[string]bool, len(operations))
	for _, operation := range operations {
		destructiveCommandSet[operation.Name] = isDestructiveOperation(operation)
	}
	destructiveMetadataRead = true
	destructiveMetadataErr = nil
}

// setOperationMetadata records everything the pre-run hooks need from a loaded
// API: which commands are destructive, and the declared type of each path
// parameter. Both are keyed by operation name and read back by command name.
func setOperationMetadata(operations []cli.Operation) {
	setDestructiveOperations(operations)
	setOperationPathParameters(operations)
}

func ensureDestructiveOperations() error {
	if destructiveMetadataRead {
		return destructiveMetadataErr
	}
	api, err := loadDCIOperationAPI()
	if err != nil {
		return err
	}
	destructiveMetadataRead = true
	if len(api.Operations) == 0 {
		destructiveMetadataErr = errors.New("DCI operation metadata is unavailable")
		return destructiveMetadataErr
	}
	setOperationMetadata(api.Operations)
	return nil
}

func loadDCIOperationAPI() (cli.API, error) {
	base, err := apiBase()
	if err != nil {
		return cli.API{}, err
	}
	return loadOperationAPI(base, &cobra.Command{})
}

func isDestructiveCommand(command *cobra.Command) bool {
	return destructiveCommandSet[command.Name()]
}

func enforceDestructiveConfirmation(command *cobra.Command, args []string) error {
	if viper.GetBool("agent-dry-run") {
		if command.LocalNonPersistentFlags().Lookup("dry-run") != nil {
			if err := ensureDryRunIdempotencyKey(command); err != nil {
				return err
			}
			destructiveActionName = command.Name()
			destructiveActionDryRun = true
			return nil
		}
		result := dryRunResult{DryRun: true, Command: command.Name(), Arguments: args}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
		destructiveActionName = ""
		destructiveActionDryRun = false
		command.Run = nil
		command.RunE = func(command *cobra.Command, args []string) error { return nil }
		return nil
	}
	if err := ensureDestructiveOperations(); err != nil {
		return fmt.Errorf("load destructive operation metadata: %w", err)
	}
	if !isDestructiveCommand(command) {
		return nil
	}
	destructiveActionName = command.Name()
	destructiveActionDryRun = false
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

func ensureDryRunIdempotencyKey(command *cobra.Command) error {
	flag := command.LocalNonPersistentFlags().Lookup("idempotency-key")
	if flag == nil || flag.Changed {
		return nil
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Errorf("generate dry-run idempotency key: %w", err)
	}
	if err := command.Flags().Set("idempotency-key", "dci-dry-run-"+hex.EncodeToString(bytes)); err != nil {
		return fmt.Errorf("set dry-run idempotency key: %w", err)
	}
	return nil
}
