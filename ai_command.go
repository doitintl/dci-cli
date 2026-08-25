package main

// P3 of AI-SPEC: the `dci ai` cobra wiring, unhidden for GA. No arguments
// opens the interactive session (ai_tui.go); arguments run the one-shot form
// (D7): the same conversation session, consumed without a TUI — narration
// streams to stdout, tool traffic to stderr when a human is watching, and
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
		Use:   "ai [question]",
		Short: "Ask questions in plain English, or run commands, in an interactive session",
		Long: "Open an interactive session where plain text asks the AI about your cloud\n" +
			"costs (it runs dci commands for you) and /commands run dci directly.\n" +
			"With a question as the argument, answers once and exits.\n\n" +
			"AI features need an Anthropic API key (yours): export ANTHROPIC_API_KEY or\n" +
			"save it in the session's guided setup. Questions and command results are\n" +
			"sent to Anthropic's API under your key.",
		Args: cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			quiet, _ := command.Flags().GetBool("quiet")
			verbose, _ := command.Flags().GetBool("verbose")
			if quiet && verbose {
				return errors.New("--quiet and --verbose are mutually exclusive")
			}
			if len(args) > 0 {
				yes, _ := command.Flags().GetBool("yes")
				return runAIOneShot(configDir, strings.Join(args, " "), yes, quiet, verbose)
			}
			if quiet || verbose {
				return errors.New("--quiet/--verbose apply to one-shot mode (dci ai \"question\"); the interactive session always shows the investigation")
			}
			if !tuiActive() {
				return errors.New("dci ai needs an interactive terminal; pass a question for one-shot mode: dci ai \"why did spend spike?\"")
			}
			return runAISession(configDir)
		},
	}
	command.Flags().Bool("yes", false, "Approve destructive commands the AI proposes (one-shot mode)")
	// No -q shorthand: restish's global --rsh-query owns it, and pflag only
	// detects the redefinition when the command parses at runtime (broke
	// every dci ai invocation in v2.6.1).
	command.Flags().Bool("quiet", false, "One-shot: only the answer — no thinking or tool narration on stderr")
	command.Flags().Bool("verbose", false, "One-shot: stream the investigation narration even when stderr is piped")
	cli.Root.AddCommand(command)
}

// aiStatsLine formats one turn's telemetry as a machine-parseable key=value
// line for DCI_AI_STATS=1 consumers — the eval harness greps it out of pty
// transcripts to track token cost, not just wall clock. ttft is omitted when
// the turn produced no answer text.
func aiStatsLine(done aiTurnDone) string {
	line := fmt.Sprintf("[ai-stats] turn=%s rounds=%d tools=%d in=%d out=%d cache_read=%d wall=%.1fs",
		strings.TrimPrefix(done.TurnID, "t"), done.Rounds, done.ToolCalls,
		done.InputTokens, done.OutputTokens, done.CacheRead, done.Wall.Seconds())
	if done.FirstText > 0 {
		line += fmt.Sprintf(" ttft=%.1fs", done.FirstText.Seconds())
	}
	return line
}

// aiOneShotVerbosity resolves the one-shot output gates (F3): narrate covers
// the investigation narration (thinking, tool traffic, context switches) —
// on for a watching human (tty), forced by --verbose, silenced by --quiet;
// verdict covers the destructive-approval line, which is contract rather
// than narration, so --quiet keeps it while pipes hide it as before.
func aiOneShotVerbosity(tty, quiet, forceVerbose bool) (narrate, verdict bool) {
	return (tty || forceVerbose) && !quiet, tty || forceVerbose
}

// runAIOneShot drives one question through the conversation session without
// a TUI: the answer streams to stdout; the investigation narration and the
// destructive-approval verdict go to stderr per aiOneShotVerbosity.
func runAIOneShot(configDir, question string, approveDestructive, quiet, forceVerbose bool) error {
	settings := loadAISettings(configDir)
	key := resolveAIKey(settings)
	if key == "" {
		return errors.New("AI needs an Anthropic API key: export ANTHROPIC_API_KEY, run dci ai interactively to save one, or add {\"api_key\": \"…\"} to " + aiSettingsPath(configDir))
	}
	verbose, verdictShown := aiOneShotVerbosity(term.IsTerminal(int(os.Stderr.Fd())), quiet, forceVerbose)
	stats := os.Getenv("DCI_AI_STATS") == "1"
	session := newLocalAISession(configDir, key, resolveAIModel(settings), aiSessionCatalog())
	defer session.Close()
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: question}); err != nil {
		return err
	}

	var failure error
	printedText := false
	thinkingOpen := false // a thinking stream is mid-line on stderr
	closeThinking := func() {
		if thinkingOpen {
			fmt.Fprint(os.Stderr, "\n")
			thinkingOpen = false
		}
	}
	for event := range session.Events() {
		switch {
		case event.TextDelta != nil:
			closeThinking()
			fmt.Print(event.TextDelta.Text)
			printedText = true

		case event.ThinkingDelta != nil && verbose:
			// The model's reasoning, dimmed on stderr: analytical questions
			// can think for a minute before the first answer token, and a
			// silent terminal reads as a hang. Piped/agent callers (verbose
			// off) keep clean streams.
			fmt.Fprint(os.Stderr, "\x1b[2m"+event.ThinkingDelta.Text+"\x1b[0m")
			thinkingOpen = true

		case event.ToolCallStarted != nil && verbose:
			closeThinking()
			fmt.Fprintln(os.Stderr, renderAIToolStart(*event.ToolCallStarted))

		case event.ToolResult != nil && verbose:
			closeThinking()
			fmt.Fprintln(os.Stderr, renderAIToolResult(*event.ToolResult))

		case event.ApprovalRequest != nil:
			closeThinking()
			answer := approveDestructive
			if verdictShown {
				verdict := "declined (pass --yes to approve)"
				if answer {
					verdict = "approved via --yes"
				}
				fmt.Fprintln(os.Stderr, "destructive command "+verdict+": dci "+strings.Join(event.ApprovalRequest.Argv, " "))
			}
			_ = session.Send(aiUserInput{Kind: aiInputApproval, CallID: event.ApprovalRequest.CallID, Approved: answer})

		case event.ContextSwitched != nil && verbose:
			closeThinking()
			fmt.Fprintf(os.Stderr, "customer context switched: %s → %s\n",
				aiDisplayContext(event.ContextSwitched.From), event.ContextSwitched.To)

		case event.LimitReached != nil:
			closeThinking()
			failure = fmt.Errorf("stopped: the turn hit the %s ceiling", event.LimitReached.Kind)

		case event.Error != nil:
			closeThinking()
			failure = errors.New(aiFriendlyAPIError(configDir, event.Error.Message))

		case event.TurnDone != nil:
			closeThinking()
			if printedText {
				fmt.Println()
			}
			if stats {
				fmt.Fprintln(os.Stderr, aiStatsLine(*event.TurnDone))
			}
			return failure
		}
	}
	return failure
}
