package main

import (
	"errors"
	"os"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func forceTUI(t *testing.T, active bool) {
	t.Helper()
	original := tuiActive
	tuiActive = func() bool { return active }
	t.Cleanup(func() { tuiActive = original })
}

func TestTUIActiveKillSwitch(t *testing.T) {
	// The real predicate needs TTYs, absent under go test; assert the
	// kill-switch and agent-mode short-circuits, which fire before the TTY
	// checks.
	t.Setenv("DCI_NO_TUI", "1")
	if tuiActive() {
		t.Fatal("DCI_NO_TUI=1 must disable the TUI")
	}
	t.Setenv("DCI_NO_TUI", "")
	originalAgent := agentMode
	agentMode = true
	t.Cleanup(func() { agentMode = originalAgent })
	if tuiActive() {
		t.Fatal("agent mode must disable the TUI")
	}
}

func TestZeroArgPickerApplies(t *testing.T) {
	resetNameResolutionState()
	t.Cleanup(resetNameResolutionState)
	resolutionIndex = map[string]resolutionListTarget{
		"get-report":    {listPath: "/reports", resource: "reports", listOperation: "list-reports"},
		"update-report": {listPath: "/reports", resource: "reports", listOperation: "list-reports", hasBody: true},
	}
	getReport := &cobra.Command{Use: "get-report"}
	updateReport := &cobra.Command{Use: "update-report"}
	other := &cobra.Command{Use: "status"}

	forceTUI(t, true)
	if !zeroArgPickerApplies(getReport) {
		t.Fatal("resolvable no-body command must qualify")
	}
	if zeroArgPickerApplies(updateReport) {
		t.Fatal("hasBody command must not qualify (surplus args are body shorthand)")
	}
	if zeroArgPickerApplies(other) {
		t.Fatal("non-resolvable command must not qualify")
	}
	t.Setenv("DCI_NO_RESOLVE", "1")
	if zeroArgPickerApplies(getReport) {
		t.Fatal("DCI_NO_RESOLVE must disable the picker")
	}
	t.Setenv("DCI_NO_RESOLVE", "")

	forceTUI(t, false)
	if zeroArgPickerApplies(getReport) {
		t.Fatal("gate off must disable the picker")
	}
}

func TestPickPathArgumentSelection(t *testing.T) {
	resetNameResolutionState()
	t.Cleanup(resetNameResolutionState)
	target := resolutionListTarget{listPath: "/reports", resource: "reports", listOperation: "list-reports"}
	resolutionIndex = map[string]resolutionListTarget{"get-report": target}
	cmd := &cobra.Command{Use: "get-report"}

	forceTUI(t, true)
	originalFetch := resolverListFetch
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		return resolverListResult{entries: []nameCacheEntry{
			{ID: "abcdefghij1234567890", Name: "Monthly AWS Spend"},
			{ID: "abcdefghij0987654321", Name: "Monthly GCP Spend"},
		}}, nil
	}
	t.Cleanup(func() { resolverListFetch = originalFetch })

	originalSelect := pickerSelectEntry
	pickerSelectEntry = func(title string, entries []nameCacheEntry) (nameCacheEntry, error) {
		return entries[1], nil
	}
	t.Cleanup(func() { pickerSelectEntry = originalSelect })

	if err := pickPathArgument(cmd, target); err != nil {
		t.Fatal(err)
	}
	if pickedPathArgument != "abcdefghij0987654321" {
		t.Fatalf("pickedPathArgument = %q, want the selected ID", pickedPathArgument)
	}
	resolved := commandResolvedTarget("get-report")
	if resolved == nil || resolved.name != "Monthly GCP Spend" {
		t.Fatalf("resolvedTargets not recorded for the destructive gate: %+v", resolved)
	}
}

func TestPickPathArgumentCancel(t *testing.T) {
	resetNameResolutionState()
	t.Cleanup(resetNameResolutionState)
	target := resolutionListTarget{listPath: "/reports", resource: "reports", listOperation: "list-reports"}
	resolutionIndex = map[string]resolutionListTarget{"get-report": target}
	cmd := &cobra.Command{Use: "get-report"}

	forceTUI(t, true)
	originalFetch := resolverListFetch
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		return resolverListResult{entries: []nameCacheEntry{{ID: "abcdefghij1234567890", Name: "Only"}}}, nil
	}
	t.Cleanup(func() { resolverListFetch = originalFetch })
	originalSelect := pickerSelectEntry
	pickerSelectEntry = func(title string, entries []nameCacheEntry) (nameCacheEntry, error) {
		return nameCacheEntry{}, huh.ErrUserAborted
	}
	t.Cleanup(func() { pickerSelectEntry = originalSelect })

	err := pickPathArgument(cmd, target)
	if err == nil {
		t.Fatal("cancel must abort the command")
	}
	var resolutionErr nameResolutionError
	if !errors.As(err, &resolutionErr) || resolutionErr.ExitCode() != exitUsage {
		t.Fatalf("cancel must keep the USAGE_ERROR exit contract, got %v", err)
	}
	if pickedPathArgument != "" {
		t.Fatalf("cancel must not leave a picked argument, got %q", pickedPathArgument)
	}
}

func TestPickerArgInjection(t *testing.T) {
	var got []string
	command := &cobra.Command{Use: "get-report", RunE: func(cmd *cobra.Command, args []string) error {
		got = args
		return nil
	}}
	installPickerArgInjection(command)
	pickedPathArgument = "abcdefghij1234567890"
	t.Cleanup(func() { pickedPathArgument = "" })
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "abcdefghij1234567890" {
		t.Fatalf("RunE args = %v, want the injected selection", got)
	}
	// A typed argument must never be displaced by picker state.
	if err := command.RunE(command, []string{"typed"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "typed" {
		t.Fatalf("RunE args = %v, want the typed argument preserved", got)
	}
}

func TestDestructiveInteractiveConfirmation(t *testing.T) {
	setDestructiveOperations([]cli.Operation{{Name: "delete-budget", Method: "DELETE"}})
	t.Cleanup(resetDestructiveContractState)
	command := &cobra.Command{Use: "delete-budget"}

	original := confirmDestructiveInteractively
	t.Cleanup(func() { confirmDestructiveInteractively = original })

	viper.Reset()
	t.Setenv("DCI_CONFIRM_DESTRUCTIVE", "")
	t.Cleanup(viper.Reset)

	confirmDestructiveInteractively = func(commandName string) bool { return true }
	if err := enforceDestructiveConfirmation(command, []string{"budget-1"}); err != nil {
		t.Fatalf("interactive confirm must proceed like --yes: %v", err)
	}

	confirmDestructiveInteractively = func(commandName string) bool { return false }
	err := enforceDestructiveConfirmation(command, []string{"budget-1"})
	if err == nil {
		t.Fatal("interactive decline must keep the confirmation error")
	}
	var confirmationErr destructiveConfirmationError
	if !errors.As(err, &confirmationErr) || confirmationErr.ExitCode() != 30 {
		t.Fatalf("decline must keep the exit-30 contract, got %v", err)
	}
}

func TestPromptNameSelectionFallsBackWithoutTUI(t *testing.T) {
	forceTUI(t, false)
	originalInput := nameSelectionInput
	t.Cleanup(func() { nameSelectionInput = originalInput })
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	nameSelectionInput = reader
	go func() {
		writer.WriteString("2\n")
		writer.Close()
	}()
	candidates := []nameCacheEntry{
		{ID: "abcdefghij1234567890", Name: "First"},
		{ID: "abcdefghij0987654321", Name: "Second"},
	}
	entry, err := promptNameSelection("input", "report", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Name != "Second" {
		t.Fatalf("numbered fallback selected %q, want Second", entry.Name)
	}
}
