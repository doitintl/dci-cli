package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

type unknownCommandError struct {
	Command           string
	Suggestions       []string
	HasCommandCatalog bool
}

var suppressedUnknownCommandStderr io.Writer

func (e unknownCommandError) Error() string {
	var message strings.Builder
	fmt.Fprintf(&message, "unknown command %q", e.Command)
	if len(e.Suggestions) == 1 {
		fmt.Fprintf(&message, "\n\nDid you mean this?\n  %s", e.Suggestions[0])
	} else if len(e.Suggestions) > 1 {
		message.WriteString("\n\nDid you mean one of these?")
		for _, suggestion := range e.Suggestions {
			fmt.Fprintf(&message, "\n  %s", suggestion)
		}
	}
	message.WriteString("\n\nRun \"dci --help\" for common commands.")
	if e.HasCommandCatalog {
		message.WriteString("\nRun \"dci commands\" for the complete command catalog.")
	}
	return message.String()
}

func (e unknownCommandError) ExitCode() int {
	return 2
}

func (e unknownCommandError) AgentErrorCode() string {
	return "UNKNOWN_COMMAND"
}

func (e unknownCommandError) AgentErrorHint() string {
	hint := "Run: dci --help"
	if e.HasCommandCatalog {
		hint += ". Full catalog: dci commands --json"
	}
	if len(e.Suggestions) > 0 {
		hint = fmt.Sprintf("Did you mean %q? %s", e.Suggestions[0], hint)
	}
	return hint
}

func (e unknownCommandError) AgentErrorRetryable() bool {
	return false
}

type unknownCommandEnvelope struct {
	Error unknownCommandEnvelopeDetail `json:"error"`
}

type unknownCommandEnvelopeDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Hint      string `json:"hint"`
	Retryable bool   `json:"retryable"`
}

func handleUnknownCommandExecutionError(err error) (int, bool) {
	var unknown unknownCommandError
	if !errors.As(err, &unknown) {
		return 0, false
	}
	writer := suppressedUnknownCommandStderr
	if writer == nil {
		writer = os.Stderr
	}
	cli.Stderr = writer
	if agentMode {
		_ = json.NewEncoder(writer).Encode(unknownCommandEnvelope{Error: unknownCommandEnvelopeDetail{
			Code:      unknown.AgentErrorCode(),
			Message:   fmt.Sprintf("unknown command %q", unknown.Command),
			Hint:      unknown.AgentErrorHint(),
			Retryable: unknown.AgentErrorRetryable(),
		}})
	} else {
		fmt.Fprintln(writer, unknown.Error())
	}
	return unknown.ExitCode(), true
}

func installUnknownCommandHandler() {
	apiCommand := findDCICommand()
	if apiCommand == nil {
		return
	}
	previousArgs := apiCommand.Args
	apiCommand.Args = func(command *cobra.Command, args []string) error {
		if len(args) == 0 {
			if previousArgs != nil {
				return previousArgs(command, args)
			}
			return nil
		}
		command.Root().SilenceErrors = true
		command.Root().SilenceUsage = true
		suppressedUnknownCommandStderr = cli.Stderr
		cli.Stderr = io.Discard
		if command.SuggestionsMinimumDistance <= 0 {
			command.SuggestionsMinimumDistance = 2
		}
		suggestions := command.SuggestionsFor(args[0])
		if len(suggestions) > 3 {
			suggestions = suggestions[:3]
		}
		return unknownCommandError{Command: args[0], Suggestions: suggestions, HasCommandCatalog: isRootCommand("commands")}
	}
}
