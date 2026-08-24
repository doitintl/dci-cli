package main

// P2 of AI-SPEC: the `dci ai` cobra wiring. Hidden while the mode is doer
// dogfood (it unhides in P3 per the spec's phase plan). No arguments opens
// the interactive session (ai_tui.go); arguments run the one-shot form (D7):
// the same conversation session, consumed without a TUI — narration streams
// to stdout, tool traffic to stderr when a human is watching, and
// destructive commands are auto-declined unless --yes was passed (§7.6).
// Kept in a sibling file per the AGENTS.md chapter-split guidance.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func registerAICommand(configDir string) {
	command := &cobra.Command{
		Use:    "ai [question]",
		Short:  "Interactive session: run commands and ask questions (preview)",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) > 0 {
				yes, _ := command.Flags().GetBool("yes")
				return runAIOneShot(configDir, strings.Join(args, " "), yes)
			}
			if !tuiActive() {
				return errors.New("dci ai needs an interactive terminal; pass a question for one-shot mode: dci ai \"why did spend spike?\"")
			}
			return runAISession(configDir)
		},
	}
	command.Flags().Bool("yes", false, "Approve destructive commands the AI proposes (one-shot mode)")
	cli.Root.AddCommand(command)
}

// runAIOneShot drives one question through the conversation session without
// a TUI: the answer streams to stdout; tool activity goes to stderr only when
// stderr is a terminal (piped/agent callers get clean streams).
func runAIOneShot(configDir, question string, approveDestructive bool) error {
	settings := loadAISettings(configDir)
	key := resolveAIKey(settings)
	if key == "" {
		return errors.New("AI needs an Anthropic API key: export ANTHROPIC_API_KEY, or add {\"api_key\": \"…\"} to " + aiSettingsPath(configDir))
	}
	verbose := term.IsTerminal(int(os.Stderr.Fd()))
	session := newLocalAISession(configDir, key, resolveAIModel(settings), aiSessionCatalog())
	defer session.Close()
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: question}); err != nil {
		return err
	}

	var failure error
	printedText := false
	for event := range session.Events() {
		switch {
		case event.TextDelta != nil:
			fmt.Print(event.TextDelta.Text)
			printedText = true

		case event.ToolCallStarted != nil && verbose:
			fmt.Fprintln(os.Stderr, renderAIToolStart(*event.ToolCallStarted))

		case event.ToolResult != nil && verbose:
			fmt.Fprintln(os.Stderr, renderAIToolResult(*event.ToolResult))

		case event.ApprovalRequest != nil:
			answer := approveDestructive
			if verbose {
				verdict := "declined (pass --yes to approve)"
				if answer {
					verdict = "approved via --yes"
				}
				fmt.Fprintln(os.Stderr, "destructive command "+verdict+": dci "+strings.Join(event.ApprovalRequest.Argv, " "))
			}
			_ = session.Send(aiUserInput{Kind: aiInputApproval, CallID: event.ApprovalRequest.CallID, Approved: answer})

		case event.ContextSwitched != nil && verbose:
			fmt.Fprintf(os.Stderr, "customer context switched: %s → %s\n",
				aiDisplayContext(event.ContextSwitched.From), event.ContextSwitched.To)

		case event.LimitReached != nil:
			failure = fmt.Errorf("stopped: the turn hit the %s ceiling", event.LimitReached.Kind)

		case event.Error != nil:
			failure = errors.New(event.Error.Message)

		case event.TurnDone != nil:
			if printedText {
				fmt.Println()
			}
			return failure
		}
	}
	return failure
}
