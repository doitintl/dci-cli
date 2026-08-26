package main

import (
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func TestLoadBetaAPIHydratesManifestOperations(t *testing.T) {
	t.Setenv("DCI_API_BASE_URL", "https://api.doit.com")
	api, err := loadBetaAPI()
	if err != nil {
		t.Fatalf("loadBetaAPI: %v", err)
	}

	byName := map[string]cli.Operation{}
	for _, operation := range api.Operations {
		byName[operation.Name] = operation
	}

	expected := []string{
		"run-report-config",
		"run-report",
		"get-report-operation",
		"get-report-results",
		"cancel-report-operation",
	}
	if len(byName) != len(expected) {
		t.Fatalf("expected %d operations, got %d: %v", len(expected), len(byName), byName)
	}
	for _, name := range expected {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing beta operation %q", name)
		}
	}

	runReport := byName["run-report"]
	if !strings.HasPrefix(runReport.URITemplate, "https://api.doit.com/analytics/v1/reports/") {
		t.Errorf("run-report URI %q does not target the configured API base", runReport.URITemplate)
	}
	if len(runReport.PathParams) != 1 || runReport.PathParams[0].Name != "id" {
		t.Errorf("run-report path params = %+v, want single id", runReport.PathParams)
	}
	foundIdempotency := false
	for _, parameter := range runReport.HeaderParams {
		if isIdempotencyKeyParam(parameter.Name) {
			foundIdempotency = true
		}
	}
	if !foundIdempotency {
		t.Errorf("run-report is missing the Idempotency-Key header param: %+v", runReport.HeaderParams)
	}
}

func TestLoadBetaAPIRespectsAPIBaseOverride(t *testing.T) {
	t.Setenv("DCI_API_BASE_URL", "https://api-dev.doit.com")
	api, err := loadBetaAPI()
	if err != nil {
		t.Fatalf("loadBetaAPI: %v", err)
	}
	for _, operation := range api.Operations {
		if !strings.HasPrefix(operation.URITemplate, "https://api-dev.doit.com/") {
			t.Fatalf("operation %s URI %q does not follow DCI_API_BASE_URL", operation.Name, operation.URITemplate)
		}
	}
}

func TestBetaEarlyAccessByCommand(t *testing.T) {
	for _, name := range []string{"run-report", "run-report-config", "get-report-operation", "get-report-results", "cancel-report-operation"} {
		if betaEarlyAccessByCommand[name] != "CMP-44423" {
			t.Errorf("betaEarlyAccessByCommand[%q] = %q, want CMP-44423", name, betaEarlyAccessByCommand[name])
		}
	}
}

func TestBetaInvocationRequested(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"dci", "beta", "run-report", "abc"}, true},
		{[]string{"dci", "dci", "beta", "run-report"}, true},
		{[]string{"dci", "__complete", "dci", "beta", ""}, true},
		{[]string{"dci", "list-budgets"}, false},
		{[]string{"dci"}, false},
		// "beta" after the operand separator is a positional value, not a command.
		{[]string{"dci", "get-report", "--", "beta"}, false},
	}
	for _, testCase := range cases {
		if got := betaInvocationRequested(testCase.args); got != testCase.want {
			t.Errorf("betaInvocationRequested(%v) = %v, want %v", testCase.args, got, testCase.want)
		}
	}
}

func TestBetaOperationCommandShape(t *testing.T) {
	t.Setenv("DCI_API_BASE_URL", "https://api.doit.com")
	api, err := loadBetaAPI()
	if err != nil {
		t.Fatalf("loadBetaAPI: %v", err)
	}
	for _, operation := range api.Operations {
		command := betaOperationCommand(operation)
		if !strings.HasPrefix(command.Short, "(beta) ") {
			t.Errorf("%s: Short %q lacks the (beta) prefix", operation.Name, command.Short)
		}
		if !strings.Contains(command.Long, "BETA:") {
			t.Errorf("%s: Long lacks the BETA notice", operation.Name)
		}
		if betaEarlyAccessByCommand[operation.Name] != "" && !strings.Contains(command.Long, betaEarlyAccessByCommand[operation.Name]) {
			t.Errorf("%s: Long does not name the early-access flag", operation.Name)
		}
		for _, parameter := range operation.HeaderParams {
			if command.Flags().Lookup(parameter.OptionName()) == nil {
				t.Errorf("%s: missing flag --%s for header param", operation.Name, parameter.OptionName())
			}
		}
		for _, parameter := range operation.QueryParams {
			if command.Flags().Lookup(parameter.OptionName()) == nil {
				t.Errorf("%s: missing flag --%s for query param", operation.Name, parameter.OptionName())
			}
		}
	}
}

func TestGenerateBetaIdempotencyKey(t *testing.T) {
	first, err := generateBetaIdempotencyKey()
	if err != nil {
		t.Fatalf("generateBetaIdempotencyKey: %v", err)
	}
	second, err := generateBetaIdempotencyKey()
	if err != nil {
		t.Fatalf("generateBetaIdempotencyKey: %v", err)
	}
	if !strings.HasPrefix(first, "dci-") || len(first) != len("dci-")+32 {
		t.Errorf("unexpected key shape %q", first)
	}
	if first == second {
		t.Errorf("keys are not unique: %q", first)
	}
}

func TestPreflightBetaInvocationUnknownCommand(t *testing.T) {
	t.Setenv("DCI_API_BASE_URL", "https://api.doit.com")
	err := preflightBetaInvocation([]string{"dci", "dci", "beta", "run-repot"})
	if err == nil {
		t.Fatal("expected an unknown-beta-command error")
	}
	preflightError, ok := err.(invocationPreflightError)
	if !ok {
		t.Fatalf("expected invocationPreflightError, got %T: %v", err, err)
	}
	if preflightError.ExitCode() != exitUsage {
		t.Errorf("exit code = %d, want %d", preflightError.ExitCode(), exitUsage)
	}
	if !strings.Contains(preflightError.Error(), `did you mean "run-report"`) {
		t.Errorf("error %q lacks the did-you-mean suggestion", preflightError.Error())
	}
}

func TestPreflightBetaInvocationHelpAndBareBeta(t *testing.T) {
	t.Setenv("DCI_API_BASE_URL", "https://api.doit.com")
	if err := preflightBetaInvocation([]string{"dci", "dci", "beta"}); err != nil {
		t.Errorf("bare beta: unexpected error %v", err)
	}
	if err := preflightBetaInvocation([]string{"dci", "dci", "beta", "run-report", "--help"}); err != nil {
		t.Errorf("help: unexpected error %v", err)
	}
}

func TestPreflightBetaInvocationKnownCommandRequiresCredentials(t *testing.T) {
	t.Setenv("DCI_API_BASE_URL", "https://api.doit.com")
	restoreCredentials := invocationCredentialsAvailable
	restoreInteractive := invocationInteractive
	defer func() {
		invocationCredentialsAvailable = restoreCredentials
		invocationInteractive = restoreInteractive
	}()
	invocationCredentialsAvailable = func() bool { return false }
	invocationInteractive = func() bool { return false }

	err := preflightBetaInvocation([]string{"dci", "dci", "beta", "run-report", "abc123"})
	if err == nil {
		t.Fatal("expected an authentication-required error")
	}
	preflightError, ok := err.(invocationPreflightError)
	if !ok || preflightError.StructuredError().Code != "AUTHENTICATION_REQUIRED" {
		t.Fatalf("expected AUTHENTICATION_REQUIRED, got %v", err)
	}

	invocationCredentialsAvailable = func() bool { return true }
	if err := preflightBetaInvocation([]string{"dci", "dci", "beta", "run-report", "abc123"}); err != nil {
		t.Errorf("authenticated: unexpected error %v", err)
	}
}

func TestBetaCatalogEntries(t *testing.T) {
	t.Setenv("DCI_API_BASE_URL", "https://api.doit.com")
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = oldRoot })
	api, err := loadBetaAPI()
	if err != nil {
		t.Fatalf("loadBetaAPI: %v", err)
	}
	entries := betaCatalogEntries(api)
	if len(entries) != 5 {
		t.Fatalf("expected 5 beta catalog entries, got %d", len(entries))
	}
	for _, entry := range entries {
		if len(entry.Path) != 2 || entry.Path[0] != "beta" {
			t.Errorf("entry path %v does not start with beta", entry.Path)
		}
		if entry.Stage != "beta" {
			t.Errorf("entry %v stage = %q, want beta", entry.Path, entry.Stage)
		}
		if entry.EarlyAccess != "CMP-44423" {
			t.Errorf("entry %v early_access = %q, want CMP-44423", entry.Path, entry.EarlyAccess)
		}
		for _, flag := range entry.Flags {
			if flag.Name == "--idempotency-key" && flag.Required {
				t.Errorf("entry %v marks --idempotency-key required; the CLI auto-generates it", entry.Path)
			}
		}
	}
}

func TestBetaResponseInspectorCapturesTypedNotFoundCode(t *testing.T) {
	next := &recordingFormatter{}
	inspector := betaResponseInspector{next: next}

	lastBetaResponseErrorCode = "stale"
	if err := inspector.Format(cli.Response{Status: 404, Body: map[string]interface{}{"code": "operation_not_found"}}); err != nil {
		t.Fatalf("Format: %v", err)
	}
	if lastBetaResponseErrorCode != "operation_not_found" {
		t.Errorf("typed 404: code = %q, want operation_not_found", lastBetaResponseErrorCode)
	}

	if err := inspector.Format(cli.Response{Status: 404, Body: "Not Found"}); err != nil {
		t.Fatalf("Format: %v", err)
	}
	if lastBetaResponseErrorCode != "" {
		t.Errorf("codeless 404: code = %q, want empty", lastBetaResponseErrorCode)
	}

	lastBetaResponseErrorCode = "stale"
	if err := inspector.Format(cli.Response{Status: 200, Body: map[string]interface{}{"code": "x"}}); err != nil {
		t.Fatalf("Format: %v", err)
	}
	if lastBetaResponseErrorCode != "" {
		t.Errorf("success response: code = %q, want empty (reset per response)", lastBetaResponseErrorCode)
	}
	if !next.called {
		t.Error("inspector did not delegate to the next formatter")
	}
}

func TestInstallBetaResponseInspectorIsIdempotent(t *testing.T) {
	oldFormatter := cli.Formatter
	t.Cleanup(func() { cli.Formatter = oldFormatter })

	cli.Formatter = &recordingFormatter{}
	installBetaResponseInspector()
	first := cli.Formatter
	installBetaResponseInspector()
	if cli.Formatter != first {
		t.Error("installBetaResponseInspector wrapped twice")
	}
}

func TestUnknownBetaCommandPreflightErrorWithoutSuggestion(t *testing.T) {
	err := unknownBetaCommandPreflightError(cli.API{}, "zzz")
	preflightError, ok := err.(invocationPreflightError)
	if !ok {
		t.Fatalf("expected invocationPreflightError, got %T", err)
	}
	if strings.Contains(preflightError.Error(), "did you mean") {
		t.Errorf("unexpected suggestion in %q", preflightError.Error())
	}
	if !strings.Contains(preflightError.StructuredError().Hint, "dci beta --help") {
		t.Errorf("hint %q does not point at dci beta --help", preflightError.StructuredError().Hint)
	}
}

func TestRegisterBetaResolutionMetadata(t *testing.T) {
	oldIndex := resolutionIndex
	t.Cleanup(func() { resolutionIndex = oldIndex })

	// Without a GA reports target there is nothing to derive from.
	resolutionIndex = map[string]resolutionListTarget{}
	registerBetaResolutionMetadata()
	if len(resolutionIndex) != 0 {
		t.Fatalf("index grew without a get-report target: %v", resolutionIndex)
	}

	target := resolutionListTarget{resource: "reports", listPath: "/analytics/v1/reports", listOperation: "list-reports"}
	resolutionIndex = map[string]resolutionListTarget{"get-report": target}
	registerBetaResolutionMetadata()
	if got := resolutionIndex["beta run-report"]; got != target {
		t.Fatalf("session key target = %+v", got)
	}
	if got := resolutionIndex["run-report"]; got != target {
		t.Fatalf("cobra key target = %+v", got)
	}

	// A GA operation claiming run-report keeps its own target.
	claimed := resolutionListTarget{resource: "runs", listPath: "/runs", listOperation: "list-runs"}
	resolutionIndex = map[string]resolutionListTarget{"get-report": target, "run-report": claimed}
	registerBetaResolutionMetadata()
	if got := resolutionIndex["run-report"]; got != claimed {
		t.Fatalf("GA claim overwritten: %+v", got)
	}
}

func TestBetaResolvableArgsRelaxation(t *testing.T) {
	oldRead, oldErr := destructiveMetadataRead, destructiveMetadataErr
	oldIndex := resolutionIndex
	oldTUI := tuiActive
	t.Cleanup(func() {
		destructiveMetadataRead, destructiveMetadataErr = oldRead, oldErr
		resolutionIndex = oldIndex
		tuiActive = oldTUI
	})
	destructiveMetadataRead, destructiveMetadataErr = true, nil
	resolutionIndex = map[string]resolutionListTarget{
		"get-report": {resource: "reports", listPath: "/analytics/v1/reports", listOperation: "list-reports"},
	}
	registerBetaResolutionMetadata()
	t.Setenv("DCI_NO_RESOLVE", "")

	runReport := betaOperationCommand(cli.Operation{
		Name:        "run-report",
		Method:      "POST",
		URITemplate: "https://api.doit.com/analytics/v1/reports/{id}/actions/run",
		PathParams:  []*cli.Param{{Name: "id", Type: "string"}},
	})
	tuiActive = func() bool { return true }
	if err := runReport.Args(runReport, nil); err != nil {
		t.Fatalf("zero args rejected although the picker applies: %v", err)
	}
	if err := runReport.Args(runReport, []string{"Monthly", "GCP", "Spend"}); err != nil {
		t.Fatalf("unquoted multi-word name rejected: %v", err)
	}
	tuiActive = func() bool { return false }
	if err := runReport.Args(runReport, nil); err == nil || !strings.Contains(err.Error(), "accepts") {
		t.Fatalf("zero args without a terminal = %v, want the arity error", err)
	}

	// Beta commands without a resolution target keep the exact arity check.
	results := betaOperationCommand(cli.Operation{
		Name:        "get-report-results",
		Method:      "GET",
		URITemplate: "https://api.doit.com/analytics/v1/reports/operations/{operationId}/results",
		PathParams:  []*cli.Param{{Name: "operationId", Type: "string"}},
	})
	tuiActive = func() bool { return true }
	if err := results.Args(results, nil); err == nil || !strings.Contains(err.Error(), "accepts") {
		t.Fatalf("non-resolvable zero args = %v, want the arity error", err)
	}
}
