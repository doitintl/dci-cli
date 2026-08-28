package main

// Output ordering (OUTPUT-ORDER-SPEC): the direction of the row rankings the
// CLI imposes on human table output. Terminals fill bottom-up into scrollback,
// so "terminal" ordering lands the row the reader came for nearest the prompt
// (newest last, largest last); "classic" keeps the web ordering the DoiT
// console uses. The choice is configurable — flag > env > persisted file >
// default — and flips nothing outside human table rendering: machine formats,
// agent mode, and piped output keep their bytes. Kept in a sibling file per
// the AGENTS.md chapter-split guidance.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

const (
	outputOrderTerminal = "terminal"
	outputOrderClassic  = "classic"
	// outputOrderReserved is the height-contextual mode weighed and deferred
	// in OUTPUT-ORDER-SPEC §5: not accepted, but named in errors so a user
	// who read the spec learns it is reserved rather than a typo.
	outputOrderReserved = "auto"
	outputOrderDefault  = outputOrderTerminal

	outputOrderEnvName = "DCI_OUTPUT_ORDER"
)

// cliSettingsFileName persists CLI-wide preferences in the config dir. A
// sibling of ai_settings.json (which stays the AI session's chapter): same
// read-tolerant loading, same 0600 mode, absent file means defaults.
const cliSettingsFileName = "cli_settings.json"

type cliSettings struct {
	// OutputOrder is the persisted row-ordering choice ("terminal" or
	// "classic"; empty = unset, fall through to the default).
	OutputOrder string `json:"output_order,omitempty"`
}

func cliSettingsPath(configDir string) string {
	return filepath.Join(configDir, cliSettingsFileName)
}

func loadCLISettings(configDir string) cliSettings {
	var settings cliSettings
	data, err := os.ReadFile(cliSettingsPath(configDir))
	if err != nil {
		return settings
	}
	_ = json.Unmarshal(data, &settings)
	return settings
}

func saveCLISettings(configDir string, settings cliSettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cliSettingsPath(configDir), append(data, '\n'), 0o600)
}

// parseOutputOrder validates an ordering spelling: "terminal" or "classic"
// (case-insensitive, trimmed). Everything else — typos and the reserved
// "auto" alike — is not accepted; the caller decides the severity per
// OUTPUT-ORDER-SPEC §6.1 (usage error on the flag, warn-and-fall-through
// for the env var and the persisted file).
func parseOutputOrder(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case outputOrderTerminal:
		return outputOrderTerminal, true
	case outputOrderClassic:
		return outputOrderClassic, true
	}
	return "", false
}

// outputOrderValueDetail words the rejection: the reserved "auto" is named
// as such so it does not read as a typo.
func outputOrderValueDetail(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), outputOrderReserved) {
		return `"auto" is reserved and not supported yet; use terminal or classic`
	}
	return "supported: terminal, classic"
}

// resolveOutputOrder applies the precedence flag > DCI_OUTPUT_ORDER >
// cli_settings.json > default, once per invocation. The returned source names
// the winning layer for `dci status` attribution. An invalid flag value is a
// hard error (an explicit per-invocation ask fails loudly, like invalid
// --output); an invalid env or file value warns once on stderr and falls
// through (ambient configuration must not break every invocation over a typo
// — the DCI_AGENT_MODE and DCI_TZ rule).
func resolveOutputOrder(flagValue string) (order, source string, err error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		if order, ok := parseOutputOrder(v); ok {
			return order, "--output-order", nil
		}
		return "", "", fmt.Errorf("invalid --output-order %q (%s)", v, outputOrderValueDetail(v))
	}
	if v := strings.TrimSpace(os.Getenv(outputOrderEnvName)); v != "" {
		if order, ok := parseOutputOrder(v); ok {
			return order, outputOrderEnvName, nil
		}
		fmt.Fprintf(os.Stderr, "warning: ignoring %s=%q (%s)\n", outputOrderEnvName, v, outputOrderValueDetail(v))
	}
	if v := strings.TrimSpace(loadCLISettings(dciConfigDir()).OutputOrder); v != "" {
		if order, ok := parseOutputOrder(v); ok {
			return order, cliSettingsFileName, nil
		}
		fmt.Fprintf(os.Stderr, "warning: ignoring output_order=%q in %s (%s)\n", v, cliSettingsFileName, outputOrderValueDetail(v))
	}
	return outputOrderDefault, "default", nil
}

// terminalOrderActive is the single question the sort sites ask: does this
// invocation render rows terminal-ordered? True only for human table output —
// the resolved order is "terminal", the format is table/auto (TOON is a
// machine format even at a TTY), not agent mode, and a human is watching
// (a TTY, or the dci ai session's transcript via DCI_SESSION_RENDER). Pipes
// and machine formats keep classic ordering regardless of configuration:
// the setting chooses what humans see, never what scripts parse.
func terminalOrderActive() bool {
	if viper.GetString("output-order") != outputOrderTerminal {
		return false
	}
	if agentMode {
		return false
	}
	switch strings.TrimSpace(viper.GetString("rsh-output-format")) {
	case "table", "auto":
	default:
		return false
	}
	return tuiActive() || sessionRenderActive()
}

// registerConfigCommand adds `dci config` (print resolved preferences with
// their sources) and `dci config output-order <terminal|classic>` (persist
// the ordering to cli_settings.json). Editing JSON by hand must never be the
// only path to a persisted preference.
func registerConfigCommand(configDir string) {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Show or set persisted CLI preferences",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			order, source, err := resolveOutputOrder("")
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "output-order: %s (%s)\n", order, source)
			return nil
		},
	}
	setCmd := &cobra.Command{
		Use:   "output-order <terminal|classic>",
		Short: "Persist the row ordering for human table output (terminal: key rows nearest the prompt; classic: web ordering)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			order, ok := parseOutputOrder(args[0])
			if !ok {
				return fmt.Errorf("invalid output-order %q (%s)", args[0], outputOrderValueDetail(args[0]))
			}
			settings := loadCLISettings(configDir)
			settings.OutputOrder = order
			if err := saveCLISettings(configDir, settings); err != nil {
				return fmt.Errorf("failed to persist output-order: %w", err)
			}
			fmt.Fprintf(os.Stdout, "output-order set to %s\n", order)
			return nil
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return []string{outputOrderTerminal, outputOrderClassic}, cobra.ShellCompDirectiveNoFileComp
		},
	}
	configCmd.AddCommand(setCmd)
	cli.Root.AddCommand(configCmd)
}

// --- Scroll-overflow hint ---------------------------------------------------
//
// Terminal ordering lands the key rows nearest the prompt, which cuts both
// ways: when a command's output is taller than the screen, the reader may not
// notice there is more above — in the worst case a --chart fills the screen
// exactly and the table goes entirely unseen (Alfredo's dogfood follow-up,
// 2026-08-28). The hint is one dim stderr line next to the prompt, printed
// only when the rendered output actually overflows the terminal height, in
// human rendering only — agent mode, machine consumers, and pipes never see
// it, per the decoration contract.

// renderedLineCount tallies the lines this invocation put on the terminal:
// stdout data lines via the counting writer below, chart lines via
// noteRenderedText. Reset per run().
var renderedLineCount int

func resetRenderedLineCount() { renderedLineCount = 0 }

// noteRenderedText counts a block printed outside the stdout path (the
// --chart render on stderr), as Fprintln prints it: its newlines plus the
// trailing one.
func noteRenderedText(s string) { renderedLineCount += strings.Count(s, "\n") + 1 }

// lineCountingWriter tallies newlines on their way to restish's stdout writer,
// so the overflow hint knows how tall the rendered response was.
type lineCountingWriter struct{ next io.Writer }

func (w lineCountingWriter) Write(p []byte) (int, error) {
	renderedLineCount += bytes.Count(p, []byte{'\n'})
	return w.next.Write(p)
}

// installRenderedLineCounter wraps cli.Stdout — the writer restish's
// formatter hands every rendered body to. cli.Init resets cli.Stdout each
// run, so this is installed right after it; the type check keeps a stray
// double install from counting lines twice.
func installRenderedLineCounter() {
	if _, installed := cli.Stdout.(lineCountingWriter); installed {
		return
	}
	cli.Stdout = lineCountingWriter{next: cli.Stdout}
}

// detectTerminalHeight mirrors detectTerminalWidth: the stdout terminal's
// height, the LINES variable as the fallback (the dci ai session exports it
// to dispatch children the way it already exports COLUMNS), and 0 when
// neither is known — unknown height renders no hint rather than a guess.
func detectTerminalHeight() int {
	if _, height, err := term.GetSize(int(os.Stdout.Fd())); err == nil && height > 0 {
		return height
	}
	if lines := os.Getenv("LINES"); lines != "" {
		if n, err := strconv.Atoi(lines); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// maybeHintScrollOverflow prints the one-line "more above" hint when this
// invocation's rendered output overflows the terminal height. Human rendering
// only; a height of 0 (undetectable) stays silent.
func maybeHintScrollOverflow() {
	if agentMode || (!tuiActive() && !sessionRenderActive()) {
		return
	}
	height := detectTerminalHeight()
	if height <= 0 || renderedLineCount <= height {
		return
	}
	above := renderedLineCount - height
	fmt.Fprintln(tuiStyledStderr(), tuiDimStyle.Render(fmt.Sprintf("↑ %d more lines above — scroll up", above)))
}
