package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func resolutionTestOperations() []cli.Operation {
	return []cli.Operation{
		{Name: "list-reports", Method: "GET", URITemplate: "https://api.doit.com/analytics/v1/reports"},
		{Name: "get-report", Method: "GET", URITemplate: "https://api.doit.com/analytics/v1/reports/{id}",
			PathParams: []*cli.Param{{Name: "id", Type: "string"}}},
		{Name: "delete-report", Method: "DELETE", URITemplate: "https://api.doit.com/analytics/v1/reports/{id}",
			PathParams: []*cli.Param{{Name: "id", Type: "string"}}},
		{Name: "update-report", Method: "PATCH", URITemplate: "https://api.doit.com/analytics/v1/reports/{id}",
			PathParams: []*cli.Param{{Name: "id", Type: "string"}}, BodyMediaType: "application/json"},
		{Name: "list-budgets", Method: "GET", URITemplate: "https://api.doit.com/analytics/v1/budgets"},
		{Name: "get-budget", Method: "GET", URITemplate: "https://api.doit.com/analytics/v1/budgets/{id}",
			PathParams: []*cli.Param{{Name: "id", Type: "string"}}},
		{Name: "list-attributions", Method: "GET", URITemplate: "https://api.doit.com/analytics/v1/attributions"},
		{Name: "get-attribution", Method: "GET", URITemplate: "https://api.doit.com/analytics/v1/attributions/{id}",
			PathParams: []*cli.Param{{Name: "id", Type: "string"}}},
		{Name: "get-label-assignment", Method: "GET", URITemplate: "https://api.doit.com/labels/{resourceType}/{resourceId}",
			PathParams: []*cli.Param{{Name: "resourceType", Type: "string"}, {Name: "resourceId", Type: "string"}}},
		{Name: "get-orphan", Method: "GET", URITemplate: "https://api.doit.com/analytics/v1/orphans/{id}",
			PathParams: []*cli.Param{{Name: "id", Type: "string"}}},
		{Name: "get-ticket-tag", Method: "GET", URITemplate: "https://api.doit.com/support/v1/tickets/{id}/tags",
			PathParams: []*cli.Param{{Name: "id", Type: "integer"}}},
	}
}

func TestBuildResolutionIndex(t *testing.T) {
	index := buildResolutionIndex(resolutionTestOperations())

	for operationName, expected := range map[string]resolutionListTarget{
		"get-report":    {listPath: "/analytics/v1/reports", resource: "reports", listOperation: "list-reports"},
		"delete-report": {listPath: "/analytics/v1/reports", resource: "reports", listOperation: "list-reports"},
		"update-report": {listPath: "/analytics/v1/reports", resource: "reports", listOperation: "list-reports", hasBody: true},
		"get-budget":    {listPath: "/analytics/v1/budgets", resource: "budgets", listOperation: "list-budgets"},
	} {
		if got := index[operationName]; got != expected {
			t.Errorf("index[%q] = %+v, want %+v", operationName, got, expected)
		}
	}
	for _, excluded := range []string{
		"get-attribution",      // legacy collection override table
		"get-label-assignment", // multiple path params
		"get-orphan",           // no parent list operation
		"get-ticket-tag",       // template does not end in the path param
		"list-reports",         // zero path params
	} {
		if _, ok := index[excluded]; ok {
			t.Errorf("index unexpectedly contains %q", excluded)
		}
	}
}

func TestResourceIDPatternBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		value string
		isID  bool
	}{
		{"AbCdEfGhIjKlMnOpQrSt", true},
		{"12345678901234567890", true},
		{"AbCdEfGhIjKlMnOpQrS", false},   // 19 chars
		{"AbCdEfGhIjKlMnOpQrStU", false}, // 21 chars
		{"AbCdEfGhIj-lMnOpQrSt", false},  // hyphen
		{"AbCdEfGhIj_lMnOpQrSt", false},  // underscore
		{"monthly aws spend", false},
		{"", false},
	} {
		if got := resourceIDPattern.MatchString(testCase.value); got != testCase.isID {
			t.Errorf("resourceIDPattern(%q) = %t, want %t", testCase.value, got, testCase.isID)
		}
	}
}

func namedEntries(names ...string) []nameCacheEntry {
	entries := make([]nameCacheEntry, 0, len(names))
	for index, name := range names {
		entries = append(entries, nameCacheEntry{ID: fmt.Sprintf("id-%d", index), Name: name})
	}
	return entries
}

func TestMatchNameCandidatesLadder(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		input   string
		entries []nameCacheEntry
		want    []string
	}{
		{
			name:    "exact wins over case-insensitive and substring",
			input:   "Monthly Spend",
			entries: namedEntries("Monthly Spend", "monthly spend", "Monthly Spend by SKU"),
			want:    []string{"Monthly Spend"},
		},
		{
			name:    "exact tie is ambiguous",
			input:   "Monthly Spend",
			entries: namedEntries("Monthly Spend", "Monthly Spend"),
			want:    []string{"Monthly Spend", "Monthly Spend"},
		},
		{
			name:    "input is whitespace-trimmed",
			input:   "  Monthly Spend  ",
			entries: namedEntries("Monthly Spend"),
			want:    []string{"Monthly Spend"},
		},
		{
			name:    "case-insensitive exact beats substring",
			input:   "monthly spend",
			entries: namedEntries("Monthly Spend", "Monthly Spend by SKU"),
			want:    []string{"Monthly Spend"},
		},
		{
			name:    "case-insensitive exact tie is ambiguous",
			input:   "monthly spend",
			entries: namedEntries("Monthly Spend", "MONTHLY SPEND"),
			want:    []string{"Monthly Spend", "MONTHLY SPEND"},
		},
		{
			name:    "unique substring resolves",
			input:   "aws",
			entries: namedEntries("Monthly AWS Spend", "GCP Spend"),
			want:    []string{"Monthly AWS Spend"},
		},
		{
			name:    "substring tie is ambiguous",
			input:   "spend",
			entries: namedEntries("Monthly AWS Spend", "GCP Spend"),
			want:    []string{"Monthly AWS Spend", "GCP Spend"},
		},
		{
			name:    "fuzzy unique minimum resolves",
			input:   "budgetz",
			entries: namedEntries("budget", "budgets!!", "unrelated"),
			want:    []string{"budget"},
		},
		{
			name:    "fuzzy tie at the minimum is ambiguous",
			input:   "budgex",
			entries: namedEntries("budget", "budges"),
			want:    []string{"budget", "budges"},
		},
		{
			name:    "beyond the fuzzy threshold finds nothing",
			input:   "zzzz",
			entries: namedEntries("Monthly Spend"),
			want:    nil,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			matches := matchNameCandidates(testCase.input, testCase.entries)
			got := make([]string, 0, len(matches))
			for _, match := range matches {
				got = append(got, match.Name)
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("matches = %v, want %v", got, testCase.want)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("matches = %v, want %v", got, testCase.want)
				}
			}
		})
	}
}

type resolverFetchCall struct {
	listPath string
	context  string
	maxPages int
}

func stubNameResolution(t *testing.T, result resolverListResult, fetchErr error) *[]resolverFetchCall {
	t.Helper()
	previousFetch := resolverListFetch
	previousCached := resolverCachedEntries
	previousInteractive := nameSelectionInteractive
	previousIndex := resolutionIndex
	previousTargets := resolvedTargets
	previousIDShaped := idShapedPathArgument
	previousAgentMode := agentMode
	calls := &[]resolverFetchCall{}
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		*calls = append(*calls, resolverFetchCall{listPath: listPath, context: context, maxPages: maxPages})
		return result, fetchErr
	}
	resolverCachedEntries = func(resource, context string) ([]nameCacheEntry, bool) { return nil, false }
	nameSelectionInteractive = func() bool { return false }
	agentMode = true
	resolvedTargets = map[string]resolvedTarget{}
	idShapedPathArgument = ""
	t.Cleanup(func() {
		resolverListFetch = previousFetch
		resolverCachedEntries = previousCached
		nameSelectionInteractive = previousInteractive
		resolutionIndex = previousIndex
		resolvedTargets = previousTargets
		idShapedPathArgument = previousIDShaped
		agentMode = previousAgentMode
	})
	return calls
}

func resolutionTestCommand(name string) *cobra.Command {
	command := &cobra.Command{Use: name}
	command.Flags().Bool("id", false, "")
	command.Flags().Bool("name", false, "")
	command.Flags().String("customer-context", "", "")
	return command
}

func TestResolvePathArgumentsResolvesNameInPlace(t *testing.T) {
	t.Setenv("DCI_NO_RESOLVE", "")
	calls := stubNameResolution(t, resolverListResult{entries: namedEntries("Monthly Spend")}, nil)
	setResolutionIndex(resolutionTestOperations())
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(resolutionTestOperations())

	command := resolutionTestCommand("get-report")
	args := []string{"monthly spend"}
	if err := resolvePathArguments(command, args); err != nil {
		t.Fatal(err)
	}
	if args[0] != "id-0" {
		t.Fatalf("args[0] = %q, want resolved id", args[0])
	}
	if len(*calls) != 1 || (*calls)[0].listPath != "/analytics/v1/reports" || (*calls)[0].maxPages != resolverMaxPages {
		t.Fatalf("fetch calls = %+v", *calls)
	}
	recorded, ok := resolvedTargets["get-report"]
	if !ok || recorded.name != "Monthly Spend" || recorded.id != "id-0" || recorded.resource != "report" {
		t.Fatalf("resolved target = %+v", recorded)
	}
}

func TestResolvePathArgumentsGating(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		argument    string
		configure   func(t *testing.T, command *cobra.Command)
		wantFetches int
		wantIDHint  bool
	}{
		{
			name:        "id flag skips resolution",
			argument:    "monthly spend",
			configure:   func(t *testing.T, command *cobra.Command) { _ = command.Flags().Set("id", "true") },
			wantFetches: 0,
		},
		{
			name:        "DCI_NO_RESOLVE skips resolution",
			argument:    "monthly spend",
			configure:   func(t *testing.T, command *cobra.Command) { t.Setenv("DCI_NO_RESOLVE", "1") },
			wantFetches: 0,
		},
		{
			name:        "id-shaped argument passes through",
			argument:    "AbCdEfGhIjKlMnOpQrSt",
			configure:   func(t *testing.T, command *cobra.Command) {},
			wantFetches: 0,
			wantIDHint:  true,
		},
		{
			name:        "name flag forces resolution of an id-shaped argument",
			argument:    "MonthlySpendReport99",
			configure:   func(t *testing.T, command *cobra.Command) { _ = command.Flags().Set("name", "true") },
			wantFetches: 1,
		},
		{
			name:        "name-shaped argument resolves",
			argument:    "monthly spend",
			configure:   func(t *testing.T, command *cobra.Command) {},
			wantFetches: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DCI_NO_RESOLVE", "")
			calls := stubNameResolution(t, resolverListResult{entries: namedEntries("Monthly Spend", "MonthlySpendReport99")}, nil)
			setResolutionIndex(resolutionTestOperations())
			t.Cleanup(resetPathValidationState)
			setOperationPathParameters(resolutionTestOperations())

			command := resolutionTestCommand("get-report")
			testCase.configure(t, command)
			if err := resolvePathArguments(command, []string{testCase.argument}); err != nil {
				t.Fatal(err)
			}
			if len(*calls) != testCase.wantFetches {
				t.Fatalf("fetch calls = %d, want %d", len(*calls), testCase.wantFetches)
			}
			if testCase.wantIDHint && idShapedPathArgument != testCase.argument {
				t.Fatalf("idShapedPathArgument = %q", idShapedPathArgument)
			}
			if !testCase.wantIDHint && idShapedPathArgument != "" {
				t.Fatalf("idShapedPathArgument unexpectedly %q", idShapedPathArgument)
			}
		})
	}
}

func TestResolvePathArgumentsJoinsSpaceSplitName(t *testing.T) {
	t.Setenv("DCI_NO_RESOLVE", "")
	calls := stubNameResolution(t, resolverListResult{entries: namedEntries("Tom Playground1 only Full details")}, nil)
	setResolutionIndex(resolutionTestOperations())
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(resolutionTestOperations())

	command := resolutionTestCommand("get-report")
	args := []string{"Tom", "Playground1", "only", "Full", "details"}
	if err := resolvePathArguments(command, args); err != nil {
		t.Fatal(err)
	}
	if args[0] != "id-0" {
		t.Fatalf("args[0] = %q, want resolved id", args[0])
	}
	if len(*calls) != 1 {
		t.Fatalf("fetch calls = %d, want 1", len(*calls))
	}
	recorded := resolvedTargets["get-report"]
	if recorded.input != "Tom Playground1 only Full details" || recorded.name != "Tom Playground1 only Full details" {
		t.Fatalf("resolved target = %+v, want the rejoined name", recorded)
	}
}

func TestJoinableNameArgumentsGating(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		command   string
		args      []string
		configure func(t *testing.T, command *cobra.Command)
		wantName  string // resolved name; distinguishes joined vs first-arg-only input
	}{
		{
			name:     "no-body operation joins the words",
			command:  "get-report",
			args:     []string{"Tom", "Playground1"},
			wantName: "Tom Playground1",
		},
		{
			name:     "body operation resolves only the first word",
			command:  "update-report",
			args:     []string{"Tom", "body: value"},
			wantName: "Tom",
		},
		{
			name:     "flag-shaped surplus word blocks the join",
			command:  "get-report",
			args:     []string{"Tom", "-x"},
			wantName: "Tom",
		},
		{
			name:    "id-shaped first word still resolves once joined",
			command: "get-report",
			args:    []string{"AbCdEfGhIjKlMnOpQrSt", "Tom"},
			// The joined input contains a space, so the ID passthrough cannot
			// trigger; the lookup runs and misses — asserted below.
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DCI_NO_RESOLVE", "")
			calls := stubNameResolution(t, resolverListResult{entries: namedEntries("Tom", "Tom Playground1")}, nil)
			setResolutionIndex(resolutionTestOperations())
			t.Cleanup(resetPathValidationState)
			setOperationPathParameters(resolutionTestOperations())

			command := resolutionTestCommand(testCase.command)
			if testCase.configure != nil {
				testCase.configure(t, command)
			}
			err := resolvePathArguments(command, testCase.args)
			if testCase.wantName == "" {
				if err == nil || !strings.Contains(err.Error(), "no report found matching") {
					t.Fatalf("err = %v, want a lookup miss on the joined input", err)
				}
				if idShapedPathArgument != "" {
					t.Fatalf("idShapedPathArgument = %q, want ID passthrough not to trigger", idShapedPathArgument)
				}
				if len(*calls) != 1 {
					t.Fatalf("fetch calls = %d, want 1", len(*calls))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(*calls) != 1 {
				t.Fatalf("fetch calls = %d, want 1", len(*calls))
			}
			if recorded := resolvedTargets[testCase.command]; recorded.name != testCase.wantName {
				t.Fatalf("resolved name = %q, want %q", recorded.name, testCase.wantName)
			}
		})
	}
}

func TestRelaxResolvableArgsValidation(t *testing.T) {
	t.Setenv("DCI_NO_RESOLVE", "")
	stubNameResolution(t, resolverListResult{}, nil)
	setResolutionIndex(resolutionTestOperations())
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(resolutionTestOperations())

	previousRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = previousRoot })
	dciCommand := &cobra.Command{Use: "dci"}
	cli.Root.AddCommand(dciCommand)
	getReport := resolutionTestCommand("get-report")
	getReport.Args = cobra.ExactArgs(1)
	updateReport := resolutionTestCommand("update-report")
	updateReport.Args = cobra.MinimumNArgs(1)
	listReports := resolutionTestCommand("list-reports")
	listReports.Args = cobra.NoArgs
	dciCommand.AddCommand(getReport, updateReport, listReports)

	relaxResolvableArgsValidation()
	relaxResolvableArgsValidation() // idempotent: the annotation guards re-wraps

	if err := getReport.Args(getReport, []string{"Tom", "Playground1"}); err != nil {
		t.Fatalf("space-split name rejected: %v", err)
	}
	if err := getReport.Args(getReport, []string{"Tom"}); err != nil {
		t.Fatalf("single argument rejected: %v", err)
	}
	if err := getReport.Args(getReport, []string{"Tom", "Playground1", "extra"}); err != nil {
		t.Fatalf("longer space-split name rejected: %v", err)
	}

	// When the join cannot apply, the original arity errors must survive.
	if err := getReport.Args(getReport, []string{}); err == nil {
		t.Fatal("zero arguments unexpectedly accepted")
	}
	_ = getReport.Flags().Set("id", "true")
	if err := getReport.Args(getReport, []string{"Tom", "Playground1"}); err == nil {
		t.Fatal("--id with surplus words unexpectedly accepted")
	}
	_ = getReport.Flags().Set("id", "false")
	t.Setenv("DCI_NO_RESOLVE", "1")
	if err := getReport.Args(getReport, []string{"Tom", "Playground1"}); err == nil {
		t.Fatal("DCI_NO_RESOLVE with surplus words unexpectedly accepted")
	}
	t.Setenv("DCI_NO_RESOLVE", "")

	// Body and unresolvable commands keep their validators untouched.
	if updateReport.Annotations[joinableArgsAnnotation] != "" {
		t.Fatal("body operation was wrapped")
	}
	if listReports.Annotations[joinableArgsAnnotation] != "" {
		t.Fatal("unresolvable operation was wrapped")
	}
}

func TestResolvePathArgumentsSkipsNonStringAndUnindexedOperations(t *testing.T) {
	t.Setenv("DCI_NO_RESOLVE", "")
	calls := stubNameResolution(t, resolverListResult{}, nil)
	operations := append(resolutionTestOperations(), cli.Operation{
		Name: "get-typed", Method: "GET", URITemplate: "https://api.doit.com/analytics/v1/reports/{id}",
		PathParams: []*cli.Param{{Name: "id", Type: "integer"}},
	})
	setResolutionIndex(operations)
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(operations)

	if err := resolvePathArguments(resolutionTestCommand("get-typed"), []string{"monthly spend"}); err != nil {
		t.Fatal(err)
	}
	if err := resolvePathArguments(resolutionTestCommand("list-reports"), []string{"anything"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("fetch calls = %d, want 0", len(*calls))
	}
}

func TestResolvePathArgumentsUsesCustomerContextFlagOverride(t *testing.T) {
	t.Setenv("DCI_NO_RESOLVE", "")
	calls := stubNameResolution(t, resolverListResult{entries: namedEntries("Monthly Spend")}, nil)
	setResolutionIndex(resolutionTestOperations())
	t.Cleanup(resetPathValidationState)
	setOperationPathParameters(resolutionTestOperations())
	previousContext := resolvedCustomerContext
	resolvedCustomerContext = "fallback.com"
	t.Cleanup(func() { resolvedCustomerContext = previousContext })

	command := resolutionTestCommand("get-report")
	if err := command.Flags().Set("customer-context", "acme.com"); err != nil {
		t.Fatal(err)
	}
	if err := resolvePathArguments(command, []string{"monthly spend"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].context != "acme.com" {
		t.Fatalf("fetch calls = %+v, want -D override context", *calls)
	}
}

func TestResolvePathArgumentsAmbiguousAndNotFoundErrors(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		entries   []nameCacheEntry
		truncated bool
		wantCode  string
		wantExit  int
		wantHint  string
	}{
		{
			name:     "ambiguous",
			entries:  namedEntries("Monthly Spend", "monthly spend by sku"),
			wantCode: "NAME_AMBIGUOUS",
			wantExit: exitUsage,
			wantHint: "id-0",
		},
		{
			name:     "not found",
			entries:  namedEntries("Unrelated"),
			wantCode: "NAME_NOT_FOUND",
			wantExit: exitNotFound,
			wantHint: "list-reports",
		},
		{
			name:      "not found notes the page cap",
			entries:   namedEntries("Unrelated"),
			truncated: true,
			wantCode:  "NAME_NOT_FOUND",
			wantExit:  exitNotFound,
			wantHint:  "capped at the first 3 pages",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DCI_NO_RESOLVE", "")
			stubNameResolution(t, resolverListResult{entries: testCase.entries, truncated: testCase.truncated}, nil)
			setResolutionIndex(resolutionTestOperations())
			t.Cleanup(resetPathValidationState)
			setOperationPathParameters(resolutionTestOperations())

			err := resolvePathArguments(resolutionTestCommand("get-report"), []string{"monthly"})
			if err == nil {
				t.Fatal("resolution unexpectedly succeeded")
			}
			resolutionError, ok := err.(nameResolutionError)
			if !ok {
				t.Fatalf("error type = %T", err)
			}
			if resolutionError.StructuredError().Code != testCase.wantCode || resolutionError.ExitCode() != testCase.wantExit {
				t.Fatalf("error = %+v", resolutionError)
			}
			if resolutionError.StructuredError().Retryable {
				t.Fatal("resolution errors must not be retryable")
			}
			if !strings.Contains(resolutionError.StructuredError().Hint, testCase.wantHint) {
				t.Fatalf("hint = %q, want it to contain %q", resolutionError.StructuredError().Hint, testCase.wantHint)
			}
		})
	}
}

func TestNameAmbiguousErrorCapsCandidatesAtTen(t *testing.T) {
	names := make([]string, 0, 12)
	for index := 0; index < 12; index++ {
		names = append(names, fmt.Sprintf("Report %02d", index))
	}
	err := nameAmbiguousError("report", "report", namedEntries(names...))
	detail := err.(nameResolutionError).StructuredError()
	if strings.Contains(detail.Message, "Report 10") || !strings.Contains(detail.Message, "Report 09") {
		t.Fatalf("message = %q", detail.Message)
	}
	if !strings.Contains(detail.Message, "matches 12 reports") {
		t.Fatalf("message = %q", detail.Message)
	}
}

func TestResolvePathArgumentsPropagatesLookupFailures(t *testing.T) {
	t.Run("401 becomes authentication required", func(t *testing.T) {
		t.Setenv("DCI_NO_RESOLVE", "")
		stubNameResolution(t, resolverListResult{}, authenticationRequiredPreflightError())
		setResolutionIndex(resolutionTestOperations())
		t.Cleanup(resetPathValidationState)
		setOperationPathParameters(resolutionTestOperations())

		err := resolvePathArguments(resolutionTestCommand("get-report"), []string{"monthly"})
		preflightError, ok := err.(invocationPreflightError)
		if !ok || preflightError.ExitCode() != exitAuthentication {
			t.Fatalf("error = %#v", err)
		}
	})

	t.Run("network failure classifies as NETWORK_ERROR", func(t *testing.T) {
		t.Setenv("DCI_NO_RESOLVE", "")
		stubNameResolution(t, resolverListResult{}, nameResolutionNetworkError{err: errors.New("dial tcp: timeout")})
		setResolutionIndex(resolutionTestOperations())
		t.Cleanup(resetPathValidationState)
		setOperationPathParameters(resolutionTestOperations())

		err := resolvePathArguments(resolutionTestCommand("get-report"), []string{"monthly"})
		if got := exitCodeForExecutionError(err, 0); got != exitNetwork {
			t.Fatalf("exit code = %d, want %d", got, exitNetwork)
		}
		detail := structuredErrorForExecution(err, 0)
		if detail.Code != "NETWORK_ERROR" || !detail.Retryable || !strings.Contains(detail.Hint, "resource id directly") {
			t.Fatalf("structured error = %+v", detail)
		}
	})
}

func TestResolveResourceNamePrefersFreshCacheExactMatch(t *testing.T) {
	calls := stubNameResolution(t, resolverListResult{entries: namedEntries("Live Copy")}, nil)
	previousCached := resolverCachedEntries
	resolverCachedEntries = func(resource, context string) ([]nameCacheEntry, bool) {
		return []nameCacheEntry{{ID: "cached-1", Name: "Monthly Spend"}}, true
	}
	t.Cleanup(func() { resolverCachedEntries = previousCached })

	target := resolutionListTarget{listPath: "/analytics/v1/reports", resource: "reports", listOperation: "list-reports"}
	resolved, err := resolveResourceName("Monthly Spend", target, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.id != "cached-1" || len(*calls) != 0 {
		t.Fatalf("resolved = %+v, fetches = %d", resolved, len(*calls))
	}

	// A substring input is not confident enough for the advisory cache path.
	resolved, err = resolveResourceName("Live", target, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.id != "id-0" || len(*calls) != 1 {
		t.Fatalf("resolved = %+v, fetches = %d", resolved, len(*calls))
	}
}

func TestPromptNameSelection(t *testing.T) {
	previousInput := nameSelectionInput
	t.Cleanup(func() { nameSelectionInput = previousInput })
	candidates := namedEntries("First", "Second")

	nameSelectionInput = strings.NewReader("2\n")
	chosen, err := promptNameSelection("fir", "report", candidates)
	if err != nil || chosen.ID != "id-1" {
		t.Fatalf("chosen = %+v, err = %v", chosen, err)
	}

	for _, input := range []string{"0\n", "3\n", "abc\n", ""} {
		nameSelectionInput = strings.NewReader(input)
		_, err := promptNameSelection("fir", "report", candidates)
		var coded interface{ ExitCode() int }
		if err == nil || !errors.As(err, &coded) || coded.ExitCode() != exitUsage {
			t.Fatalf("input %q: err = %#v", input, err)
		}
	}
}

func TestResolveResourceNameUsesPickerWhenInteractive(t *testing.T) {
	stubNameResolution(t, resolverListResult{entries: namedEntries("Monthly AWS", "Monthly GCP")}, nil)
	previousInteractive := nameSelectionInteractive
	previousPrompt := nameSelectionPrompt
	nameSelectionInteractive = func() bool { return true }
	nameSelectionPrompt = func(input, resource string, candidates []nameCacheEntry) (nameCacheEntry, error) {
		if len(candidates) != 2 {
			t.Fatalf("candidates = %+v", candidates)
		}
		return candidates[1], nil
	}
	t.Cleanup(func() {
		nameSelectionInteractive = previousInteractive
		nameSelectionPrompt = previousPrompt
	})

	target := resolutionListTarget{listPath: "/analytics/v1/reports", resource: "reports", listOperation: "list-reports"}
	resolved, err := resolveResourceName("monthly", target, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.id != "id-1" || resolved.name != "Monthly GCP" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestFetchResourceNamesPagingHeadersAndBudgetCap(t *testing.T) {
	requests := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.RequestURI())
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-Tenant-Id") != "acme.com" {
			t.Errorf("tenant header = %q", request.Header.Get("X-Tenant-Id"))
		}
		if request.URL.Query().Get("customerContext") != "acme.com" {
			t.Errorf("customerContext query = %q", request.URL.Query().Get("customerContext"))
		}
		if !strings.HasPrefix(request.Header.Get("User-Agent"), "dci-cli/") {
			t.Errorf("user agent = %q", request.Header.Get("User-Agent"))
		}
		page := request.URL.Query().Get("pageToken")
		fmt.Fprintf(writer, `{"pageToken":"next-%s","rowCount":1,"reports":[{"id":"ReportIdentifier%s000","reportName":"Report %s"}]}`, page, page, page)
	}))
	t.Cleanup(server.Close)
	t.Setenv("DCI_API_BASE_URL", server.URL)
	t.Setenv("DCI_API_KEY", "test-token")
	previousClient := resolverHTTPClient
	resolverHTTPClient = server.Client()
	t.Cleanup(func() { resolverHTTPClient = previousClient })

	result, err := fetchResourceNames("/analytics/v1/reports", "acme.com", resolverMaxPages)
	if err != nil {
		t.Fatal(err)
	}
	if !result.truncated || len(result.entries) != 3 {
		t.Fatalf("result = %+v", result)
	}
	if len(requests) != 3 || !strings.Contains(requests[0], "maxResults=500") {
		t.Fatalf("requests = %v", requests)
	}
	if !strings.Contains(requests[1], "pageToken=next-") {
		t.Fatalf("requests = %v", requests)
	}

	requests = nil
	if _, err := fetchResourceNames("/analytics/v1/budgets", "acme.com", 1); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || !strings.Contains(requests[0], "maxResults=250") {
		t.Fatalf("budget requests = %v", requests)
	}
}

func TestFetchResourceNamesStatusHandling(t *testing.T) {
	status := http.StatusUnauthorized
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	t.Setenv("DCI_API_BASE_URL", server.URL)
	t.Setenv("DCI_API_KEY", "test-token")
	previousClient := resolverHTTPClient
	resolverHTTPClient = server.Client()
	t.Cleanup(func() { resolverHTTPClient = previousClient })

	_, err := fetchResourceNames("/analytics/v1/reports", "", 1)
	preflightError, ok := err.(invocationPreflightError)
	if !ok || preflightError.ExitCode() != exitAuthentication {
		t.Fatalf("401 error = %#v", err)
	}

	status = http.StatusForbidden
	_, err = fetchResourceNames("/analytics/v1/reports", "", 1)
	if got := exitCodeForExecutionError(err, 0); got != exitAuthorization {
		t.Fatalf("403 exit code = %d, want %d", got, exitAuthorization)
	}
}

func TestParseResourceNamePageDiscoversNameFieldByPriority(t *testing.T) {
	entries, nextToken := parseResourceNamePage([]byte(`{
		"pageToken": "abc",
		"budgets": [
			{"id": "b-1", "budgetName": "Budget One"},
			{"id": "b-2", "name": "Named", "budgetName": "Shadowed"},
			{"id": "b-3", "content": "Annotation text"},
			{"id": "b-4"},
			{"budgetName": "No ID"}
		]
	}`))
	if nextToken != "abc" {
		t.Fatalf("pageToken = %q", nextToken)
	}
	want := map[string]string{"b-1": "Budget One", "b-2": "Named", "b-3": "Annotation text"}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v", entries)
	}
	for _, entry := range entries {
		if want[entry.ID] != entry.Name {
			t.Fatalf("entry = %+v", entry)
		}
	}
}

func TestDestructiveConfirmationErrorCarriesResolvedTarget(t *testing.T) {
	err := destructiveConfirmationError{
		Command:  "delete-report",
		Resolved: &resolvedTarget{input: "monthly", resource: "report", name: "Monthly Spend", id: "ReportIdentifier1234"},
	}
	if !strings.Contains(err.Error(), `targets report "Monthly Spend" (ReportIdentifier1234)`) {
		t.Fatalf("message = %q", err.Error())
	}
	if !strings.Contains(err.AgentErrorHint(), "delete-report ReportIdentifier1234 --yes") {
		t.Fatalf("hint = %q", err.AgentErrorHint())
	}
	detail := err.StructuredError()
	if detail.Resolved == nil || detail.Resolved.ID != "ReportIdentifier1234" || detail.Resolved.Input != "monthly" {
		t.Fatalf("structured error = %+v", detail)
	}
	if detail.Code != "DESTRUCTIVE_REQUIRES_CONFIRMATION" || detail.Retryable {
		t.Fatalf("structured error = %+v", detail)
	}

	plain := destructiveConfirmationError{Command: "delete-report"}
	if plain.StructuredError().Resolved != nil || !strings.Contains(plain.Error(), "requires confirmation") {
		t.Fatalf("plain error = %+v", plain.StructuredError())
	}
}

func TestStructuredError404HintForIDShapedArgument(t *testing.T) {
	previous := idShapedPathArgument
	t.Cleanup(func() { idShapedPathArgument = previous })

	idShapedPathArgument = ""
	if hint := structuredErrorForStatus(404, "not found", nil).Hint; hint != "" {
		t.Fatalf("hint = %q, want empty", hint)
	}
	idShapedPathArgument = "AbCdEfGhIjKlMnOpQrSt"
	hint := structuredErrorForStatus(404, "not found", nil).Hint
	if !strings.Contains(hint, `"AbCdEfGhIjKlMnOpQrSt"`) || !strings.Contains(hint, "--name") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestResolveOpenResourceID(t *testing.T) {
	t.Setenv("DCI_NO_RESOLVE", "")
	calls := stubNameResolution(t, resolverListResult{entries: namedEntries("Monthly Spend")}, nil)

	resolved, err := resolveOpenResourceID("report", "monthly spend", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "id-0" || len(*calls) != 1 || (*calls)[0].listPath != "/analytics/v1/reports" {
		t.Fatalf("resolved = %q, calls = %+v", resolved, *calls)
	}

	passthrough, err := resolveOpenResourceID("report", "AbCdEfGhIjKlMnOpQrSt", t.TempDir())
	if err != nil || passthrough != "AbCdEfGhIjKlMnOpQrSt" {
		t.Fatalf("passthrough = %q, err = %v", passthrough, err)
	}

	t.Setenv("DCI_NO_RESOLVE", "1")
	skipped, err := resolveOpenResourceID("budget", "monthly spend", t.TempDir())
	if err != nil || skipped != "monthly spend" || len(*calls) != 1 {
		t.Fatalf("skipped = %q, err = %v, calls = %d", skipped, err, len(*calls))
	}
}

func TestBuildCommandCatalogMarksResolvableOperations(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = oldRoot })

	catalog := buildCommandCatalog(cli.API{Operations: resolutionTestOperations()})
	resolvable := map[string]bool{}
	for _, entry := range catalog.Commands {
		resolvable[strings.Join(entry.Path, " ")] = entry.ResolvesNames
	}
	if !resolvable["get-report"] || !resolvable["delete-report"] || !resolvable["get-budget"] {
		t.Fatalf("resolvable = %+v", resolvable)
	}
	if resolvable["list-reports"] || resolvable["get-attribution"] || resolvable["get-label-assignment"] {
		t.Fatalf("resolvable = %+v", resolvable)
	}
	if catalog.Version != "1" {
		t.Fatalf("catalog version = %q", catalog.Version)
	}
}
