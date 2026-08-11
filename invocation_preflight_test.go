package main

import (
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func configureInvocationPreflightTest(t *testing.T, api cli.API, authenticated, cached, interactive bool) *int {
	t.Helper()
	previousLoader := loadInvocationAPI
	previousCredentials := invocationCredentialsAvailable
	previousCache := invocationCachedSpecAvailable
	previousInteractive := invocationInteractive
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

func TestPreflightAPIInvocationAllowsHelpAndDryRunWithoutCredentials(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "delete-report"}}}
	loadCount := configureInvocationPreflightTest(t, api, false, true, false)

	if err := preflightAPIInvocation([]string{"dci", "dci", "delete-report", "--help"}); err != nil {
		t.Fatalf("help rejected: %v", err)
	}
	if *loadCount != 0 {
		t.Fatalf("help loaded operation metadata %d times", *loadCount)
	}
	if err := preflightAPIInvocation([]string{"dci", "dci", "delete-report", "report-1", "--dry-run"}); err != nil {
		t.Fatalf("dry run rejected: %v", err)
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
