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
	loadOperationAPI        = func(base string, root *cobra.Command) (cli.API, error) {
		// cli.Load dereferences cli.Cache, which only exists after cli.Init.
		if cli.Cache == nil {
			return cli.API{}, errors.New("restish CLI is not initialized")
		}
		return cli.Load(base, root)
	}
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
	Command  string
	Resolved *resolvedTarget
}

func (e destructiveConfirmationError) Error() string {
	if e.Resolved != nil {
		return fmt.Sprintf(
			"%s targets %s %q (%s); re-run with --yes or set DCI_CONFIRM_DESTRUCTIVE=1",
			e.Command, e.Resolved.resource, e.Resolved.name, e.Resolved.id,
		)
	}
	return fmt.Sprintf("%s requires confirmation; re-run with --yes or set DCI_CONFIRM_DESTRUCTIVE=1", e.Command)
}

func (e destructiveConfirmationError) ExitCode() int {
	return 30
}

func (e destructiveConfirmationError) AgentErrorCode() string {
	return "DESTRUCTIVE_REQUIRES_CONFIRMATION"
}

func (e destructiveConfirmationError) AgentErrorHint() string {
	if e.Resolved != nil {
		// The suggested re-run uses the resolved ID, not the fuzzy input, so
		// the retry stays deterministic even if names change in between.
		return fmt.Sprintf("Re-run dci %s %s --yes after reviewing the operation, or use --dry-run first", e.Command, e.Resolved.id)
	}
	return "Re-run with --yes after reviewing the operation, or use --dry-run first"
}

func (e destructiveConfirmationError) AgentErrorRetryable() bool {
	return false
}

func (e destructiveConfirmationError) StructuredError() structuredError {
	detail := structuredError{
		Code:      e.AgentErrorCode(),
		Message:   e.Error(),
		Hint:      e.AgentErrorHint(),
		Retryable: e.AgentErrorRetryable(),
	}
	if e.Resolved != nil {
		detail.Resolved = &resolvedTargetPayload{Input: e.Resolved.input, Name: e.Resolved.name, ID: e.Resolved.id}
	}
	return detail
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
	setResolutionIndex(operations)
	registerBetaResolutionMetadata()
	relaxResolvableArgsValidation()
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
		// Interactive humans get a default-Cancel confirm prompt instead of
		// the --yes usage error (TUI-SPEC F3). Declining (or any non-TTY /
		// agent context) keeps the exit-30 error path below byte-identical.
		ensureConfirmTargetDetails(command.Name(), args)
		confirmed = confirmDestructiveInteractively(command.Name())
	}
	if !confirmed {
		destructiveActionName = ""
		return destructiveConfirmationError{Command: command.Name(), Resolved: commandResolvedTarget(command.Name())}
	}
	return nil
}

// ensureConfirmTargetDetails backfills the confirmation display for a
// destructive command invoked with a raw resource id: name resolution never
// ran, so the prompt would otherwise show only the command name. Best-effort
// and interactive-only — the id is looked up in the resource's collection
// (fresh name cache first, then a live list fetch behind a spinner), and any
// failure or miss simply leaves the prompt without the target line. The
// backfilled target also reaches the exit-30 error when the human declines.
func ensureConfirmTargetDetails(commandName string, args []string) {
	if !tuiActive() || commandResolvedTarget(commandName) != nil || len(args) == 0 {
		return
	}
	target, ok := resolutionIndex[commandName]
	if !ok {
		return
	}
	id := strings.TrimSpace(args[0])
	if id == "" {
		return
	}
	context := activeCustomerContext()
	entries, cached := resolverCachedEntries(target.resource, context)
	if !cached {
		stopSpinner := startTUISpinner(fmt.Sprintf("Looking up %s %s…", singularResourceName(target.resource), id))
		result, err := resolverListFetch(target.listPath, context, resolverMaxPages)
		stopSpinner()
		if err != nil {
			return
		}
		entries = result.entries
	}
	for _, entry := range entries {
		if entry.ID == id {
			resolvedTargets[commandName] = resolvedFromEntry(id, singularResourceName(target.resource), entry)
			return
		}
	}
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
