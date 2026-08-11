package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/pflag"
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
	return !agentMode && term.IsTerminal(int(os.Stdout.Fd()))
}

func preflightAPIInvocation(args []string) error {
	if len(args) < 3 || args[1] != "dci" || invocationRequestsHelp(args) {
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

	operation := invocationOperation(api, commandName)
	if operation == nil {
		return unknownAPICommandPreflightError(api, commandName)
	}
	if authenticated || interactive || invocationHasFlag(args, "--dry-run") {
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
	for index := 2; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			if index+1 < len(args) {
				return args[index+1]
			}
			return ""
		}
		if strings.HasPrefix(argument, "-") && argument != "-" {
			known, takesValue := invocationFlag(argument)
			if !known {
				return ""
			}
			if takesValue && !strings.Contains(argument, "=") {
				index++
			}
			continue
		}
		return argument
	}
	return ""
}

func invocationFlag(argument string) (bool, bool) {
	rootFlags := cli.Root.PersistentFlags()
	dciCommand := findDCICommand()
	var dciFlags *pflag.FlagSet
	if dciCommand != nil {
		dciFlags = dciCommand.PersistentFlags()
	}

	if strings.HasPrefix(argument, "--") {
		name := strings.TrimPrefix(argument, "--")
		name, _, _ = strings.Cut(name, "=")
		flag := rootFlags.Lookup(name)
		if flag == nil && dciFlags != nil {
			flag = dciFlags.Lookup(name)
		}
		if flag == nil {
			return false, false
		}
		return true, !isBoolFlag(flag)
	}

	shortNames := strings.TrimPrefix(argument, "-")
	for index := 0; index < len(shortNames); index++ {
		shortName := string(shortNames[index])
		flag := rootFlags.ShorthandLookup(shortName)
		if flag == nil && dciFlags != nil {
			flag = dciFlags.ShorthandLookup(shortName)
		}
		if flag == nil {
			return false, false
		}
		if !isBoolFlag(flag) {
			return true, index == len(shortNames)-1
		}
	}
	return true, false
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
	for _, operation := range api.Operations {
		if strings.HasPrefix(operation.Name, name) {
			return operation.Name
		}
		distance := editDistance(name, operation.Name)
		if distance < closestDistance {
			closestDistance = distance
			closest = operation.Name
		}
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
			current[rightIndex] = minimumInteger(
				previous[rightIndex]+1,
				current[rightIndex-1]+1,
				previous[rightIndex-1]+changeCost,
			)
		}
		previous, current = current, previous
	}
	return previous[len(rightRunes)]
}

func minimumInteger(values ...int) int {
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

func credentialsAvailableForInvocation() bool {
	if os.Getenv("DCI_API_KEY") != "" {
		return true
	}
	return cli.Cache != nil && cli.Cache.GetString("dci:default.token") != ""
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
