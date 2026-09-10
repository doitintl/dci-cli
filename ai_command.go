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
	"io"
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
			"DoiT employees need no setup: AI access is provided through their DoiT\n" +
			"sign-in, and traffic runs under DoiT's Anthropic organization. Everyone\n" +
			"else brings an Anthropic API key (yours): export ANTHROPIC_API_KEY or\n" +
			"save it in the session's guided setup — questions and command results\n" +
			"are then sent to Anthropic's API under your key.",
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
	creds := resolveAICredentials(settings)
	if !creds.available() {
		return errors.New("AI needs an Anthropic API key: export ANTHROPIC_API_KEY, run dci ai interactively to save one, or add {\"api_key\": \"…\"} to " + aiSettingsPath(configDir))
	}
	verbose, verdictShown := aiOneShotVerbosity(term.IsTerminal(int(os.Stderr.Fd())), quiet, forceVerbose)
	session := newLocalAISession(configDir, creds, resolveAIModel(settings), aiSessionCatalog())
	defer session.Close()
	if err := session.Send(aiUserInput{Kind: aiInputChat, Text: question}); err != nil {
		return err
	}
	return renderAIOneShot(session, os.Stdout, os.Stderr, aiOneShotOptions{
		configDir:          configDir,
		approveDestructive: approveDestructive,
		verbose:            verbose,
		verdictShown:       verdictShown,
		stats:              os.Getenv("DCI_AI_STATS") == "1",
	})
}

// aiOneShotOptions is what renderAIOneShot needs beyond the streams.
type aiOneShotOptions struct {
	configDir          string
	approveDestructive bool
	verbose            bool // narration (thinking, tool traffic) on stderr
	verdictShown       bool // destructive-approval verdict on stderr
	stats              bool // DCI_AI_STATS=1 footer on stderr
}

// renderAIOneShot consumes the session's events for one turn. Answer text
// goes to stdout; everything else is stderr narration gated by opts.
//
// The model often narrates before a tool call ("I'll pull this month's
// anomalies.") and answers in the next round, after the tool result. The
// session's quiet turn drops that interim text from the transcript
// (ai_tui.go, ToolCallStarted); one-shot keeps it — agents and scripts read
// the stream — but a tool-call boundary ends the paragraph, so the interim
// narration and the answer print as two paragraphs instead of running
// together on one line.
func renderAIOneShot(session conversationSession, stdout, stderr io.Writer, opts aiOneShotOptions) error {
	var failure error
	printedText := false
	paragraphOpen := false // stdout text printed since the last tool-call boundary
	breakPending := false  // a tool call interrupted an open paragraph
	tail := ""             // last two bytes written to stdout, for newline accounting
	writeText := func(text string) {
		if text == "" {
			return
		}
		fmt.Fprint(stdout, text)
		printedText = true
		paragraphOpen = true
		tail += text
		if len(tail) > 2 {
			tail = tail[len(tail)-2:]
		}
	}
	thinkingOpen := false // a thinking stream is mid-line on stderr
	closeThinking := func() {
		if thinkingOpen {
			fmt.Fprint(stderr, "\n")
			thinkingOpen = false
		}
	}
	for event := range session.Events() {
		switch {
		case event.TextDelta != nil:
			closeThinking()
			if breakPending && event.TextDelta.Text != "" {
				// Pad to a blank line whatever the interim text ended with.
				switch {
				case strings.HasSuffix(tail, "\n\n"):
				case strings.HasSuffix(tail, "\n"):
					fmt.Fprint(stdout, "\n")
				default:
					fmt.Fprint(stdout, "\n\n")
				}
				breakPending = false
			}
			writeText(event.TextDelta.Text)

		case event.ThinkingDelta != nil && opts.verbose:
			// The model's reasoning, dimmed on stderr: analytical questions
			// can think for a minute before the first answer token, and a
			// silent terminal reads as a hang. Piped/agent callers (verbose
			// off) keep clean streams.
			fmt.Fprint(stderr, "\x1b[2m"+event.ThinkingDelta.Text+"\x1b[0m")
			thinkingOpen = true

		case event.ToolCallStarted != nil:
			closeThinking()
			if paragraphOpen {
				breakPending = true
				paragraphOpen = false
			}
			if opts.verbose {
				fmt.Fprintln(stderr, renderAIToolStart(*event.ToolCallStarted))
			}

		case event.ToolResult != nil && opts.verbose:
			closeThinking()
			fmt.Fprintln(stderr, renderAIToolResult(*event.ToolResult))

		case event.ApprovalRequest != nil:
			closeThinking()
			answer := opts.approveDestructive
			if opts.verdictShown {
				verdict := "declined (pass --yes to approve)"
				if answer {
					verdict = "approved via --yes"
				}
				fmt.Fprintln(stderr, "destructive command "+verdict+": dci "+strings.Join(event.ApprovalRequest.Argv, " "))
			}
			_ = session.Send(aiUserInput{Kind: aiInputApproval, CallID: event.ApprovalRequest.CallID, Approved: answer})

		case event.ContextSwitched != nil && opts.verbose:
			closeThinking()
			fmt.Fprintf(stderr, "customer context switched: %s → %s\n",
				aiDisplayContext(event.ContextSwitched.From), event.ContextSwitched.To)

		case event.LimitReached != nil:
			closeThinking()
			failure = fmt.Errorf("stopped: the turn hit the %s ceiling", event.LimitReached.Kind)

		case event.Error != nil:
			closeThinking()
			failure = errors.New(aiFriendlyAPIError(opts.configDir, event.Error.Message))

		case event.TurnDone != nil:
			closeThinking()
			if printedText {
				fmt.Fprintln(stdout)
			}
			if opts.stats {
				fmt.Fprintln(stderr, aiStatsLine(*event.TurnDone))
			}
			return failure
		}
	}
	return failure
}
