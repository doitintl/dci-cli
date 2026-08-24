package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func configureInvocationPreflightTest(t *testing.T, api cli.API, authenticated, cached, interactive bool) *int {
	t.Helper()
	previousLoader := loadInvocationAPI
	previousCredentials := invocationCredentialsAvailable
	previousCache := invocationCachedSpecAvailable
	previousInteractive := invocationInteractive
	previousActionName := destructiveActionName
	previousActionDryRun := destructiveActionDryRun
	previousCommandSet := destructiveCommandSet
	previousMetadataRead := destructiveMetadataRead
	previousMetadataErr := destructiveMetadataErr
	resetDestructiveContractState()
	loadCount := 0
	loadInvocationAPI = func() (cli.API, error) {
		loadCount++
		return api, nil
	}
	invocationCredentialsAvailable = func() bool { return authenticated }
	invocationCachedSpecAvailable = func() bool { return cached }
	invocationInteractive = func() bool { return interactive }
	t.Cleanup(func() {
		loadInvocationAPI = previousLoader
		invocationCredentialsAvailable = previousCredentials
		invocationCachedSpecAvailable = previousCache
		invocationInteractive = previousInteractive
		destructiveActionName = previousActionName
		destructiveActionDryRun = previousActionDryRun
		destructiveCommandSet = previousCommandSet
		destructiveMetadataRead = previousMetadataRead
		destructiveMetadataErr = previousMetadataErr
	})
	return &loadCount
}

func TestPreflightAPIInvocationRejectsUnknownCommandWithSuggestion(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "list-budgets"}, {Name: "list-reports"}}}
	configureInvocationPreflightTest(t, api, true, false, false)

	err := preflightAPIInvocation([]string{"dci", "dci", "list-bugets"})
	if err == nil || !strings.Contains(err.Error(), "list-budgets") {
		t.Fatalf("error = %v", err)
	}
	if err.(invocationPreflightError).ExitCode() != exitUsage {
		t.Fatalf("exit code = %d", err.(invocationPreflightError).ExitCode())
	}
}

func TestPreflightAPIInvocationRejectsHeadlessAuthentication(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "list-budgets"}}}
	configureInvocationPreflightTest(t, api, false, true, false)

	err := preflightAPIInvocation([]string{"dci", "dci", "list-budgets"})
	if err == nil {
		t.Fatal("unauthenticated headless invocation accepted")
	}
	detail := err.(invocationPreflightError).StructuredError()
	if detail.Code != "AUTHENTICATION_REQUIRED" || err.(invocationPreflightError).ExitCode() != exitAuthentication {
		t.Fatalf("error = %#v", err)
	}
}

func TestPreflightAPIInvocationFailsBeforeLoadingWithoutCredentialsOrCache(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "list-budgets"}}}
	loadCount := configureInvocationPreflightTest(t, api, false, false, false)

	err := preflightAPIInvocation([]string{"dci", "dci", "list-budgets"})
	if err == nil || *loadCount != 0 {
		t.Fatalf("error = %v, load count = %d", err, *loadCount)
	}
}

func TestPreflightAPIInvocationRejectsColdCacheHeadlessHelp(t *testing.T) {
	// Help on an API command needs the description; loading it with no
	// credentials, no cached spec, and no interactive terminal used to
	// dead-end in the browser-login wait. It must fail fast instead.
	api := cli.API{Operations: []cli.Operation{{Name: "delete-report"}}}
	loadCount := configureInvocationPreflightTest(t, api, false, false, false)

	err := preflightAPIInvocation([]string{"dci", "dci", "delete-report", "--help"})
	if err == nil {
		t.Fatal("cold-cache headless help accepted")
	}
	detail := err.(invocationPreflightError).StructuredError()
	if detail.Code != "AUTHENTICATION_REQUIRED" || err.(invocationPreflightError).ExitCode() != exitAuthentication {
		t.Fatalf("error = %#v", err)
	}
	if *loadCount != 0 {
		t.Fatalf("help loaded operation metadata %d times", *loadCount)
	}
}

func TestPreflightAPIInvocationAllowsWarmCacheHelpWithoutCredentials(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "delete-report"}}}
	loadCount := configureInvocationPreflightTest(t, api, false, true, false)

	if err := preflightAPIInvocation([]string{"dci", "dci", "delete-report", "--help"}); err != nil {
		t.Fatalf("help rejected: %v", err)
	}
	if *loadCount != 0 {
		t.Fatalf("help loaded operation metadata %d times", *loadCount)
	}
}

func TestPreflightAPIInvocationRejectsColdCacheDryRunWithoutCredentials(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "delete-report"}}}
	loadCount := configureInvocationPreflightTest(t, api, false, false, false)

	for _, args := range [][]string{
		{"dci", "dci", "delete-report", "report-1", "--dry-run"},
		{"dci", "dci", "delete-report", "report-1", "--dry-run=true"},
	} {
		err := preflightAPIInvocation(args)
		if err == nil || err.(invocationPreflightError).ExitCode() != exitAuthentication {
			t.Fatalf("args = %v, error = %#v", args, err)
		}
	}
	if *loadCount != 0 {
		t.Fatalf("cold-cache dry runs loaded operation metadata %d times", *loadCount)
	}
}

func TestPreflightAPIInvocationAllowsWarmCacheDryRunWithoutCredentials(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "delete-report"}}}
	loadCount := configureInvocationPreflightTest(t, api, false, true, false)

	for _, args := range [][]string{
		{"dci", "dci", "delete-report", "report-1", "--dry-run"},
		{"dci", "dci", "delete-report", "report-1", "--dry-run=true"},
	} {
		if err := preflightAPIInvocation(args); err != nil {
			t.Fatalf("args = %v, error = %v", args, err)
		}
	}
	if *loadCount != 2 {
		t.Fatalf("warm-cache dry runs loaded operation metadata %d times", *loadCount)
	}
}

func TestPreflightAPIInvocationRejectsUnknownDryRunCommand(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "delete-report"}}}
	configureInvocationPreflightTest(t, api, false, true, false)

	err := preflightAPIInvocation([]string{"dci", "dci", "delete-repot", "report-1", "--dry-run"})
	if err == nil || err.(invocationPreflightError).ExitCode() != exitUsage || !strings.Contains(err.Error(), "delete-report") {
		t.Fatalf("error = %#v", err)
	}
}

func TestAPIInvocationCommandNameSkipsKnownFlags(t *testing.T) {
	previousRoot := cli.Root
	cli.Root = &cobra.Command{}
	cli.Root.PersistentFlags().Bool("agent", false, "")
	cli.Root.PersistentFlags().String("profile", "", "")
	t.Cleanup(func() {
		cli.Root = previousRoot
	})

	if name := apiInvocationCommandName([]string{"dci", "dci", "--agent", "list-budgets"}); name != "list-budgets" {
		t.Fatalf("command = %q", name)
	}
	if name := apiInvocationCommandName([]string{"dci", "dci", "--profile", "default", "list-budgets"}); name != "list-budgets" {
		t.Fatalf("command = %q", name)
	}
}

func TestAPIInvocationCommandNameDoesNotBypassPreflightForUnknownShortFlag(t *testing.T) {
	previousRoot := cli.Root
	cli.Root = &cobra.Command{}
	cli.Root.PersistentFlags().BoolP("quiet", "q", false, "")
	t.Cleanup(func() {
		cli.Root = previousRoot
	})

	args := []string{"dci", "dci", "-qz", "list-budgets"}
	if name := apiInvocationCommandName(args); name != "list-budgets" {
		t.Fatalf("command = %q", name)
	}
	api := cli.API{Operations: []cli.Operation{{Name: "list-budgets"}}}
	configureInvocationPreflightTest(t, api, false, true, false)
	err := preflightAPIInvocation(args)
	if err == nil || err.(invocationPreflightError).ExitCode() != exitAuthentication {
		t.Fatalf("error = %#v", err)
	}
}

func TestPreflightAPIInvocationWarmsDestructiveMetadata(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "delete-report", Method: "DELETE"}}}
	loadCount := configureInvocationPreflightTest(t, api, true, false, false)

	if err := preflightAPIInvocation([]string{"dci", "dci", "delete-report", "report-1"}); err != nil {
		t.Fatal(err)
	}
	if *loadCount != 1 || !destructiveMetadataRead || !destructiveCommandSet["delete-report"] {
		t.Fatalf("loads = %d, metadata read = %t, destructive = %t", *loadCount, destructiveMetadataRead, destructiveCommandSet["delete-report"])
	}
}

func TestClosestAPICommandPrefersDistanceAndIncludesAliases(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{
		{Name: "get-reports-long"},
		{Name: "get-report"},
		{Name: "get-dimension", Aliases: []string{"get-dimensions"}},
	}}
	if suggestion := closestAPICommand(api, "get-repor"); suggestion != "get-report" {
		t.Fatalf("prefix suggestion = %q", suggestion)
	}
	if suggestion := closestAPICommand(api, "get-dimensons"); suggestion != "get-dimensions" {
		t.Fatalf("alias suggestion = %q", suggestion)
	}
}

func TestInvocationCanAuthenticateInteractivelyUsesStdinAndExplicitAgentMode(t *testing.T) {
	previousMode := agentUAMode
	t.Cleanup(func() { agentUAMode = previousMode })

	agentUAMode = uaModeNonInteractive
	if !invocationCanAuthenticateInteractively(true) {
		t.Fatal("terminal stdin cannot authenticate when stdout is redirected")
	}
	agentUAMode = uaModeAgent
	if invocationCanAuthenticateInteractively(true) {
		t.Fatal("explicit agent mode accepted interactive authentication")
	}
}

func TestCredentialsAvailableForInvocationChecksExpiryAndRefresh(t *testing.T) {
	previousCache := cli.Cache
	cli.Cache = viper.New()
	t.Setenv("DCI_API_KEY", "")
	t.Cleanup(func() { cli.Cache = previousCache })

	cli.Cache.Set("dci:default.token", "token")
	cli.Cache.Set("dci:default.expires", time.Now().Add(-time.Hour).Format(time.RFC3339))
	if credentialsAvailableForInvocation() {
		t.Fatal("expired token without refresh counted as credentials")
	}
	cli.Cache.Set("dci:default.refresh", "refresh")
	if !credentialsAvailableForInvocation() {
		t.Fatal("refreshable token rejected")
	}
	cli.Cache.Set("dci:default.refresh", "")
	cli.Cache.Set("dci:default.expires", time.Now().Add(time.Hour).Format(time.RFC3339))
	if !credentialsAvailableForInvocation() {
		t.Fatal("unexpired token rejected")
	}
}

func TestReportExecutionErrorPreservesNonInteractivePreflightContract(t *testing.T) {
	previousMode := agentMode
	previousUAMode := agentUAMode
	previousStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	agentMode = true
	agentUAMode = uaModeNonInteractive
	os.Stderr = writer
	t.Cleanup(func() {
		agentMode = previousMode
		agentUAMode = previousUAMode
		os.Stderr = previousStderr
		_ = reader.Close()
		_ = writer.Close()
		resetErrorContractState()
	})

	exitCode := reportExecutionError(authenticationRequiredPreflightError(), 0, t.TempDir())
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var envelope structuredErrorEnvelope
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("output = %q: %v", output, err)
	}
	if exitCode != exitAuthentication || envelope.Error.Code != "AUTHENTICATION_REQUIRED" {
		t.Fatalf("exit = %d, error = %#v", exitCode, envelope.Error)
	}
}

func TestPreflightAPIInvocationRejectsColdCacheDestructiveWithoutCredentials(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "delete-budget", Method: "DELETE"}}}
	loadCount := configureInvocationPreflightTest(t, api, false, false, false)

	err := preflightAPIInvocation([]string{"dci", "dci", "delete-budget", "budget-1"})
	if err == nil || err.(invocationPreflightError).ExitCode() != exitAuthentication || *loadCount != 0 {
		t.Fatalf("error = %#v, loads = %d", err, *loadCount)
	}
}
