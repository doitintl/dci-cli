package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
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
	if len(args) < 3 || args[1] != "dci" || invocationSkipsPreflight(args) {
		return nil
	}
	commandName := apiInvocationCommandName(args)
	if commandName == "" {
		return nil
	}

	authenticated := invocationCredentialsAvailable()
	interactive := invocationInteractive()
	if !authenticated && !interactive && !invocationCachedSpecAvailable() {
		return authenticationRequiredPreflightError()
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
	if authenticated || interactive || invocationHasFlag(args, "--dry-run") {
		return nil
	}
	return authenticationRequiredPreflightError()
}

func invocationSkipsPreflight(args []string) bool {
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

func cachedSpecAvailableForInvocation() bool {
	cacheDir := os.Getenv("DCI_CACHE_DIR")
	if cacheDir == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			return false
		}
		cacheDir = filepath.Join(userCacheDir, "dci")
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "dci.cbor")); err != nil || cli.Cache == nil {
		return false
	}
	expiresAt := cli.Cache.GetTime("dci.expires")
	return !expiresAt.IsZero() && expiresAt.After(time.Now())
}

func authenticationRequiredPreflightError() error {
	return invocationPreflightError{
		detail: structuredError{
			Code:      "AUTHENTICATION_REQUIRED",
			Message:   "no credentials available and this session cannot open a browser to log in",
			Hint:      "Set DCI_API_KEY to a DoiT API token, or run dci login from an interactive terminal",
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
