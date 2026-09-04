package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

type invocationPreflightError struct {
	detail   structuredError
	exitCode int
}

func (preflightError invocationPreflightError) Error() string {
	return preflightError.detail.Message
}

func (preflightError invocationPreflightError) ExitCode() int {
	return preflightError.exitCode
}

func (preflightError invocationPreflightError) StructuredError() structuredError {
	return preflightError.detail
}

var loadInvocationAPI = loadDCIOperationAPI
var invocationCredentialsAvailable = credentialsAvailableForInvocation
var invocationCachedSpecAvailable = cachedSpecAvailableForInvocation
var invocationInteractive = func() bool {
	return invocationCanAuthenticateInteractively(term.IsTerminal(int(os.Stdin.Fd())))
}

func invocationCanAuthenticateInteractively(stdinTTY bool) bool {
	return agentUAMode != uaModeAgent && stdinTTY
}

func preflightAPIInvocation(args []string) error {
	if len(args) < 3 || args[1] != "dci" {
		return nil
	}
	commandName := apiInvocationCommandName(args)
	if commandName == "" {
		return nil
	}
	if commandName == "beta" {
		// Beta commands validate against the embedded beta surface, not the
		// GA spec — beta_commands.go owns that preflight.
		return preflightBetaInvocation(args)
	}
	if localDCICommandRegistered(commandName) {
		// Hand-registered local commands (question_commands.go and similar)
		// are not GA-spec operations, so the unknown-command check below does
		// not apply. They still fall through to the ordinary authentication
		// check just below.
		return preflightLocalDCICommand(commandName, args)
	}

	authenticated := invocationCredentialsAvailable()
	interactive := invocationInteractive()
	if !authenticated && !interactive && !invocationCachedSpecAvailable() {
		return authenticationRequiredPreflightError()
	}
	if invocationRequestsHelp(args) {
		// Help needs a loadable API description but nothing else: with a
		// cached spec (or a way to authenticate) it renders offline, so it
		// skips operation validation. Without one, the check above already
		// failed fast — the description fetch would otherwise dead-end in the
		// interactive login wait.
		return nil
	}

	api, err := loadInvocationAPI()
	if err != nil || len(api.Operations) == 0 {
		if !authenticated && !interactive {
			return authenticationRequiredPreflightError()
		}
		return nil
	}
	setOperationMetadata(api.Operations)

	operation := invocationOperation(api, commandName)
	if operation == nil {
		return unknownAPICommandPreflightError(api, commandName)
	}
	if err := validateMaxResults(operation.Name, args[2:]); err != nil {
		return err
	}
	if err := validateAllPagesFlags(args[2:]); err != nil {
		return err
	}
	if err := validateSearchFlags(args[2:]); err != nil {
		return err
	}
	if err := validatePagingFlagsSupported(operation, args[2:]); err != nil {
		return err
	}
	if err := validateExportWindow(operation.Name, args[2:]); err != nil {
		return err
	}
	if err := validateReimportFlag(operation.Name, args[2:]); err != nil {
		return err
	}
	if err := validateSearchOnFileExport(operation.Name, args[2:]); err != nil {
		return err
	}
	if authenticated || interactive || invocationHasFlag(args, "--dry-run") {
		return nil
	}
	return authenticationRequiredPreflightError()
}

// questionCommandNames lists the hand-registered local commands under dciCmd
// that are not GA-spec operations (question_commands.go). "beta" is handled
// by its own preflightBetaInvocation branch above and does not belong here.
var questionCommandNames = map[string]bool{
	"budgets-at-risk":  true,
	"anomalies-recent": true,
}

func localDCICommandRegistered(commandName string) bool {
	return questionCommandNames[commandName]
}

// preflightLocalDCICommand validates a hand-registered local command the way
// preflightAPIInvocation validates a GA one, minus the GA spec load and
// unknown-command lookup: the command's own existence is already known by
// construction. Help is checked first, matching preflightAPIInvocation's
// order, so an out-of-range flag value never hides the help text. The flag
// validations below it still apply: --all/--search are persistent dciCmd
// flags budgets-at-risk/anomalies-recent inherit like every GA command, and
// validateMaxResults protects the same silent-clamp hazard it protects for
// list-budgets — budgets-at-risk wraps it directly and inherits its 250 cap
// (pagingCaps).
func preflightLocalDCICommand(commandName string, args []string) error {
	if invocationRequestsHelp(args) {
		return nil
	}
	if err := validateMaxResults(commandName, args[2:]); err != nil {
		return err
	}
	if err := validateAllPagesFlags(args[2:]); err != nil {
		return err
	}
	if err := validateSearchFlags(args[2:]); err != nil {
		return err
	}
	if err := validateReimportFlag(commandName, args[2:]); err != nil {
		return err
	}
	if invocationCredentialsAvailable() || invocationInteractive() || invocationHasFlag(args, "--dry-run") {
		return nil
	}
	return authenticationRequiredPreflightError()
}

func invocationRequestsHelp(args []string) bool {
	for _, argument := range args[2:] {
		if argument == "--help" || argument == "-h" {
			return true
		}
	}
	return false
}

func apiInvocationCommandName(args []string) string {
	if cli.Root == nil {
		return commandArg(args, 2)
	}
	rootFlags := cli.Root.PersistentFlags()
	dciCommand := findDCICommand()
	if dciCommand != nil {
		return commandArg(args, 2, rootFlags, dciCommand.PersistentFlags())
	}
	return commandArg(args, 2, rootFlags)
}

func invocationOperation(api cli.API, name string) *cli.Operation {
	for index := range api.Operations {
		operation := &api.Operations[index]
		if operation.Name == name {
			return operation
		}
		for _, alias := range operation.Aliases {
			if alias == name {
				return operation
			}
		}
	}
	return nil
}

func unknownAPICommandPreflightError(api cli.API, name string) error {
	suggestion := closestAPICommand(api, name)
	message := fmt.Sprintf("unknown command %q", name)
	hint := "Run dci --help to list available commands"
	if suggestion != "" {
		message = fmt.Sprintf("unknown command %q (did you mean %q?)", name, suggestion)
		hint = fmt.Sprintf("Did you mean %q? Run dci --help to list available commands", suggestion)
	}
	if tuiActive() {
		// Humans only (agents never pass the gate): a mistyped command is the
		// natural moment to learn the plain-English spelling — the error stays
		// an error, never a silent AI fallback (AI-DEFAULT-SPEC §5).
		message += "\nTo ask in plain English instead: dci ai \"<your question>\""
	}
	return invocationPreflightError{
		detail:   structuredError{Code: "USAGE_ERROR", Message: message, Hint: hint, Retryable: false},
		exitCode: exitUsage,
	}
}

func closestAPICommand(api cli.API, name string) string {
	closest := ""
	closestDistance := 3
	prefix := ""
	for _, operation := range api.Operations {
		for _, candidate := range append([]string{operation.Name}, operation.Aliases...) {
			if prefix == "" && strings.HasPrefix(candidate, name) {
				prefix = candidate
			}
			distance := editDistance(name, candidate)
			if distance < closestDistance {
				closestDistance = distance
				closest = candidate
			}
		}
	}
	if closest == "" {
		return prefix
	}
	return closest
}

func editDistance(left, right string) int {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	previous := make([]int, len(rightRunes)+1)
	current := make([]int, len(rightRunes)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex := 1; leftIndex <= len(leftRunes); leftIndex++ {
		current[0] = leftIndex
		for rightIndex := 1; rightIndex <= len(rightRunes); rightIndex++ {
			changeCost := 1
			if leftRunes[leftIndex-1] == rightRunes[rightIndex-1] {
				changeCost = 0
			}
			current[rightIndex] = min(
				previous[rightIndex]+1,
				current[rightIndex-1]+1,
				previous[rightIndex-1]+changeCost,
			)
		}
		previous, current = current, previous
	}
	return previous[len(rightRunes)]
}

func credentialsAvailableForInvocation() bool {
	if os.Getenv("DCI_API_KEY") != "" {
		return true
	}
	if cli.Cache == nil || cli.Cache.GetString("dci:default.token") == "" {
		return false
	}
	expiresAt := cli.Cache.GetTime("dci:default.expires")
	return expiresAt.IsZero() || expiresAt.After(time.Now()) || cli.Cache.GetString("dci:default.refresh") != ""
}

// cachedSpecAvailableForInvocation reports whether a warm, unexpired
// OpenAPI spec cache exists — checked against realCacheDir(), the REAL
// cache directory, not whatever DCI_CACHE_DIR currently resolves to. An
// active DCI_API_BASE_URL override points DCI_CACHE_DIR at
// applyAPIBaseOverride's throwaway temp dir, which never gets a copy of
// dci.cbor (so a stale spec from a different host can never leak in) — so
// checking the current env var here would always report "not cached" and
// permanently disable Tab completion for the whole override session, even
// with a fully warm real cache for that same host. Completion only reads
// local cache state; it makes no routed API call, so consulting the real
// directory here carries none of the cross-host-leak risk the temp dir
// exists to prevent elsewhere.
func cachedSpecAvailableForInvocation() bool {
	cacheDir := realCacheDir()
	if cacheDir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "dci.cbor")); err != nil {
		return false
	}
	expiresAt := readCacheExpiry(cacheDir, "dci.expires")
	return !expiresAt.IsZero() && expiresAt.After(time.Now())
}

// readCacheExpiry reads a viper-style dotted timestamp key directly out of
// cacheDir's cache.json, bypassing cli.Cache — which, during an active
// DCI_API_BASE_URL override, is restish's in-memory view of the isolated
// temp dir's copy, not necessarily the same file cachedSpecAvailableForInvocation
// needs to check here (the real one).
//
// Uses a scratch viper instance rather than plain json.Unmarshal into a
// flat map: viper's Set("dci.expires", ...) (restish cli/api.go's cacheAPI)
// nests the dot into a real JSON object — {"dci":{"expires":"..."}}, not a
// flat {"dci.expires":"..."} key — so a flat-map unmarshal fails outright
// whenever any other top-level key (e.g. "dci:default" holding the OAuth
// token object) is present, which it always is on a real cache.json.
func readCacheExpiry(cacheDir, key string) time.Time {
	v := viper.New()
	v.SetConfigName("cache")
	v.SetConfigType("json")
	v.AddConfigPath(cacheDir)
	if err := v.ReadInConfig(); err != nil {
		return time.Time{}
	}
	return v.GetTime(key)
}

func authenticationRequiredPreflightError() error {
	message := "no credentials available and this session cannot open a browser to log in"
	hint := "Set DCI_API_KEY to a DoiT API token, or run dci login from an interactive terminal"
	if sessionRenderActive() {
		// This child really cannot open a browser, but the `dci ai` session it
		// renders for can: /login suspends the session and hands the browser
		// flow the real terminal. Say that, not the shell-shaped remedy — the
		// human-mode error prints only the message, so it must carry the fix.
		message = "you're not signed in — run /login to sign in"
		hint = "Run /login to sign in"
	}
	return invocationPreflightError{
		detail: structuredError{
			Code:      "AUTHENTICATION_REQUIRED",
			Message:   message,
			Hint:      hint,
			Retryable: false,
		},
		exitCode: exitAuthentication,
	}
}

// apiRejectedTokenError reports a 401 from the API itself: credentials exist
// but the server refused them. Distinct from authenticationRequiredPreflightError
// (no credentials at all) — and it names the API base, because the most
// confusing cause is the CLI talking to an API the stored token is not valid
// for (a dev base left behind in apis.json or DCI_API_BASE_URL).
func apiRejectedTokenError(base string) error {
	hint := "Run dci login to re-authenticate"
	if base != defaultAPIBase {
		hint += fmt.Sprintf("; note the API base is %s, not the default %s — check DCI_API_BASE_URL and the base in apis.json", base, defaultAPIBase)
	}
	return invocationPreflightError{
		detail: structuredError{
			Code:      "AUTHENTICATION_REQUIRED",
			Message:   fmt.Sprintf("the API at %s rejected the stored credentials (HTTP 401)", base),
			Hint:      hint,
			Retryable: false,
		},
		exitCode: exitAuthentication,
	}
}

func invocationHasFlag(args []string, name string) bool {
	for _, argument := range args {
		if argument == name || strings.HasPrefix(argument, name+"=") {
			return true
		}
	}
	return false
}
