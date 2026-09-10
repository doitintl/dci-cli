package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func TestRegisterAICommandVisibleAtGA(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = oldRoot })

	registerAICommand(t.TempDir())
	var aiCommand *cobra.Command
	for _, command := range cli.Root.Commands() {
		if command.Name() == "ai" {
			aiCommand = command
		}
	}
	if aiCommand == nil {
		t.Fatal("ai command not registered")
	}
	if aiCommand.Hidden {
		t.Fatal("ai command still hidden — P3 unhides it")
	}
	if !strings.Contains(aiCommand.Long, "Anthropic") {
		t.Fatal("ai --help must carry the data-flow disclosure (D3)")
	}
	if flag := aiCommand.Flags().Lookup("yes"); flag == nil {
		t.Fatal("one-shot --yes flag missing")
	}
}

func TestAIStatsLine(t *testing.T) {
	done := aiTurnDone{
		TurnID: "t1", Rounds: 3, ToolCalls: 2,
		InputTokens: 48213, OutputTokens: 2911, CacheRead: 45102,
		Wall: 88200 * time.Millisecond, FirstText: 6100 * time.Millisecond,
	}
	want := "[ai-stats] turn=1 rounds=3 tools=2 in=48213 out=2911 cache_read=45102 wall=88.2s ttft=6.1s"
	if got := aiStatsLine(done); got != want {
		t.Fatalf("stats line = %q, want %q", got, want)
	}

	// A turn with no answer text (error, cancel) has no first-text time; the
	// key is omitted rather than printing a misleading ttft=0.0s.
	done.FirstText = 0
	if got := aiStatsLine(done); strings.Contains(got, "ttft") {
		t.Fatalf("ttft printed for a textless turn: %q", got)
	}
}

func TestAICommandExcludedFromMachineCatalog(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = oldRoot })

	registerAICommand(t.TempDir())
	cli.Root.AddCommand(&cobra.Command{Use: "status", Short: "Show status", Run: func(*cobra.Command, []string) {}})

	catalog := buildCommandCatalog(cli.API{})
	sawStatus := false
	for _, entry := range catalog.Commands {
		if entry.Path[0] == "ai" {
			t.Fatalf("ai leaked into the machine catalog: %v", entry.Path)
		}
		if entry.Path[0] == "status" {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatal("catalog walk broke: status missing")
	}
}

func TestAIOneShotVerbosity(t *testing.T) {
	cases := []struct {
		tty, quiet, force bool
		narrate, verdict  bool
	}{
		{tty: true, narrate: true, verdict: true},                 // watching human: full narration
		{tty: false, narrate: false, verdict: false},              // piped: clean streams
		{tty: true, quiet: true, narrate: false, verdict: true},   // --quiet keeps the verdict contract
		{tty: false, force: true, narrate: true, verdict: true},   // --verbose captures the investigation
		{tty: true, force: true, narrate: true, verdict: true},    // --verbose on a tty is the default anyway
		{tty: false, quiet: true, narrate: false, verdict: false}, // --quiet piped stays clean
	}
	for _, c := range cases {
		narrate, verdict := aiOneShotVerbosity(c.tty, c.quiet, c.force)
		if narrate != c.narrate || verdict != c.verdict {
			t.Errorf("aiOneShotVerbosity(tty=%v quiet=%v force=%v) = %v,%v want %v,%v",
				c.tty, c.quiet, c.force, narrate, verdict, c.narrate, c.verdict)
		}
	}
}

func TestAIFlagsDoNotCollideWithGlobalShorthands(t *testing.T) {
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci", SilenceUsage: true, SilenceErrors: true}
	t.Cleanup(func() { cli.Root = oldRoot })
	// restish's global --rsh-query owns the -q shorthand (AddGlobalFlag in
	// its cli.go); a local ai flag reusing any global shorthand only blows up
	// when the command parses at runtime — pflag panics, which broke every
	// dci ai invocation in v2.6.1. Execute through a root carrying the global
	// so the collision is caught here, not by users.
	cli.Root.PersistentFlags().StringSliceP("rsh-query", "q", nil, "Add custom query param")
	registerAICommand(t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("ai flag parsing panicked: %v", recovered)
		}
	}()
	cli.Root.SetArgs([]string{"ai", "--quiet", "hello"})
	err := cli.Root.Execute()
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("err = %v, want the missing-key error (proof flag parsing succeeded)", err)
	}
}

// TestAIOneShotSeparatesInterimNarrationFromAnswer: the model narrates before
// a tool call and answers in the next round. One-shot keeps the interim text
// (scripts read the stream) but a tool-call boundary ends its paragraph, so
// the two rounds print as two paragraphs rather than one run-on line.
func TestAIOneShotSeparatesInterimNarrationFromAnswer(t *testing.T) {
	session := newFakeAISession()
	session.events <- aiEvent{TextDelta: &aiTextDelta{Text: "I'll pull this month's anomalies for you."}}
	session.events <- aiEvent{ToolCallStarted: &aiToolCallStarted{CallID: "c1", Tool: aiToolRunCommand, Argv: []string{"list-anomalies"}, By: "agent"}}
	session.events <- aiEvent{ToolResult: &aiToolResult{CallID: "c1", OK: true, Data: "[]", Elapsed: time.Second}}
	session.events <- aiEvent{TextDelta: &aiTextDelta{Text: "Three anomalies were detected"}}
	session.events <- aiEvent{TextDelta: &aiTextDelta{Text: " since Sept 1."}}
	session.events <- aiEvent{TurnDone: &aiTurnDone{}}

	var stdout, stderr strings.Builder
	if err := renderAIOneShot(session, &stdout, &stderr, aiOneShotOptions{}); err != nil {
		t.Fatalf("renderAIOneShot: %v", err)
	}
	want := "I'll pull this month's anomalies for you.\n\nThree anomalies were detected since Sept 1.\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Errorf("--quiet one-shot wrote narration to stderr: %q", stderr.String())
	}
}

// A tool call before any text prints no separator, and interim text that
// already ends its line gets padded to exactly one blank line.
func TestAIOneShotParagraphBreakEdges(t *testing.T) {
	cases := []struct {
		name   string
		events []aiEvent
		want   string
	}{
		{
			name: "tool call before any text",
			events: []aiEvent{
				{ToolCallStarted: &aiToolCallStarted{CallID: "c1", Tool: aiToolRunCommand, By: "agent"}},
				{ToolResult: &aiToolResult{CallID: "c1", OK: true}},
				{TextDelta: &aiTextDelta{Text: "Answer."}},
				{TurnDone: &aiTurnDone{}},
			},
			want: "Answer.\n",
		},
		{
			name: "interim text ends with a newline",
			events: []aiEvent{
				{TextDelta: &aiTextDelta{Text: "Looking.\n"}},
				{ToolCallStarted: &aiToolCallStarted{CallID: "c1", Tool: aiToolRunCommand, By: "agent"}},
				{ToolResult: &aiToolResult{CallID: "c1", OK: true}},
				{TextDelta: &aiTextDelta{Text: "Answer."}},
				{TurnDone: &aiTurnDone{}},
			},
			want: "Looking.\n\nAnswer.\n",
		},
		{
			name: "two tool calls between narration and answer",
			events: []aiEvent{
				{TextDelta: &aiTextDelta{Text: "Looking."}},
				{ToolCallStarted: &aiToolCallStarted{CallID: "c1", Tool: aiToolRunCommand, By: "agent"}},
				{ToolResult: &aiToolResult{CallID: "c1", OK: true}},
				{ToolCallStarted: &aiToolCallStarted{CallID: "c2", Tool: aiToolRunCommand, By: "agent"}},
				{ToolResult: &aiToolResult{CallID: "c2", OK: true}},
				{TextDelta: &aiTextDelta{Text: "Answer."}},
				{TurnDone: &aiTurnDone{}},
			},
			want: "Looking.\n\nAnswer.\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			session := newFakeAISession()
			for _, event := range c.events {
				session.events <- event
			}
			var stdout strings.Builder
			if err := renderAIOneShot(session, &stdout, io.Discard, aiOneShotOptions{}); err != nil {
				t.Fatalf("renderAIOneShot: %v", err)
			}
			if stdout.String() != c.want {
				t.Errorf("stdout = %q, want %q", stdout.String(), c.want)
			}
		})
	}
}

// aiOneShotTableAnswer is a markdown answer with the shapes that read badly
// raw at a terminal: a pipe table, bold, and inline code.
const aiOneShotTableAnswer = "Top services:\n\n| Service | Cost |\n|---|---|\n| EC2 | $1,200 |\n| S3 | $300 |\n\n**Total** is `$1,500`."

func withAIStdoutTTY(t *testing.T, tty bool) {
	t.Helper()
	previous := aiStdoutIsTTY
	aiStdoutIsTTY = func() bool { return tty }
	t.Cleanup(func() { aiStdoutIsTTY = previous })
}

func aiOneShotRun(t *testing.T, opts aiOneShotOptions, events ...aiEvent) (stdout, stderr string) {
	t.Helper()
	session := newFakeAISession()
	for _, event := range events {
		session.events <- event
	}
	var out, errs strings.Builder
	if err := renderAIOneShot(session, &out, &errs, opts); err != nil {
		t.Fatalf("renderAIOneShot: %v", err)
	}
	return out.String(), errs.String()
}

// A human at a terminal gets the answer rendered like the interactive
// session: glamour draws the table with rules, and the raw |---| separator
// row, the ** markers, and the backticks are gone.
func TestAIOneShotRendersMarkdownAtTTY(t *testing.T) {
	withAIStdoutTTY(t, true)
	if !aiOneShotRendersMarkdown(false, false, false) {
		t.Fatal("tty, no agent mode, no NO_COLOR, no --output must render")
	}
	stdout, stderr := aiOneShotRun(t, aiOneShotOptions{render: true, width: 80, style: "dark"},
		aiEvent{TextDelta: &aiTextDelta{Text: aiOneShotTableAnswer[:20]}},
		aiEvent{TextDelta: &aiTextDelta{Text: aiOneShotTableAnswer[20:]}},
		aiEvent{TurnDone: &aiTurnDone{}},
	)
	plain := stripANSI(stdout)
	if !strings.Contains(plain, "│") || !strings.Contains(plain, "┼") {
		t.Errorf("rendered table has no glamour borders:\n%s", plain)
	}
	for _, raw := range []string{"|---|", "| EC2 |", "**Total**", "`$1,500`"} {
		if strings.Contains(plain, raw) {
			t.Errorf("rendered output still carries raw markdown %q:\n%s", raw, plain)
		}
	}
	for _, text := range []string{"EC2", "$1,200", "Total", "$1,500"} {
		if !strings.Contains(plain, text) {
			t.Errorf("rendered output lost %q:\n%s", text, plain)
		}
	}
	if !strings.HasSuffix(stdout, "\n") || strings.HasSuffix(stdout, "\n\n") {
		t.Errorf("rendered answer must end with exactly one newline: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("no status line was requested, stderr = %q", stderr)
	}
}

// Interim narration before a tool call renders as its own block, separated
// from the answer by a blank line — the rendered counterpart of the raw
// mode's paragraph break — and it prints at the tool boundary, before the
// answer round starts.
func TestAIOneShotRenderedInterimNarrationIsItsOwnBlock(t *testing.T) {
	stdout, _ := aiOneShotRun(t, aiOneShotOptions{render: true, width: 80, style: "dark"},
		aiEvent{TextDelta: &aiTextDelta{Text: "I'll pull this month's anomalies for you."}},
		aiEvent{ToolCallStarted: &aiToolCallStarted{CallID: "c1", Tool: aiToolRunCommand, Argv: []string{"list-anomalies"}, By: "agent"}},
		aiEvent{ToolResult: &aiToolResult{CallID: "c1", OK: true, Data: "[]", Elapsed: time.Second}},
		aiEvent{TextDelta: &aiTextDelta{Text: "Three anomalies were detected since Sept 1."}},
		aiEvent{TurnDone: &aiTurnDone{}},
	)
	plain := stripANSI(stdout)
	interim := strings.Index(plain, "I'll pull this month's anomalies")
	answer := strings.Index(plain, "Three anomalies were detected")
	if interim < 0 || answer < 0 || interim > answer {
		t.Fatalf("interim narration then answer expected, got:\n%s", plain)
	}
	between := plain[interim:answer]
	if !strings.Contains(between, "\n\n") {
		t.Errorf("interim narration and answer are not separated by a blank line:\n%q", between)
	}
}

// Piped stdout keeps the raw stream byte for byte: scripts parse it.
func TestAIOneShotStreamsRawMarkdownWhenPiped(t *testing.T) {
	withAIStdoutTTY(t, false)
	if aiOneShotRendersMarkdown(false, false, false) {
		t.Fatal("a pipe must not render")
	}
	stdout, _ := aiOneShotRun(t, aiOneShotOptions{},
		aiEvent{TextDelta: &aiTextDelta{Text: aiOneShotTableAnswer}},
		aiEvent{TurnDone: &aiTurnDone{}},
	)
	if stdout != aiOneShotTableAnswer+"\n" {
		t.Errorf("piped stdout = %q, want the raw markdown unchanged", stdout)
	}
}

// Agent mode, NO_COLOR, and an explicit --output keep the raw stream even at
// a terminal.
func TestAIOneShotAgentModeStreamsRaw(t *testing.T) {
	withAIStdoutTTY(t, true)
	cases := []struct {
		name                            string
		agent, noColor, outputRequested bool
	}{
		{"agent mode", true, false, false},
		{"NO_COLOR", false, true, false},
		{"--output requested", false, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if aiOneShotRendersMarkdown(c.agent, c.noColor, c.outputRequested) {
				t.Fatalf("%s must not render", c.name)
			}
		})
	}
	stdout, _ := aiOneShotRun(t, aiOneShotOptions{},
		aiEvent{TextDelta: &aiTextDelta{Text: aiOneShotTableAnswer}},
		aiEvent{TurnDone: &aiTurnDone{}},
	)
	if stdout != aiOneShotTableAnswer+"\n" {
		t.Errorf("agent-mode stdout = %q, want the raw markdown unchanged", stdout)
	}
}

// With --quiet at a terminal the answer is buffered, so a status line on
// stderr says it is coming — and is erased before the answer prints.
func TestAIOneShotStatusLineIsErasedBeforeAnswer(t *testing.T) {
	stdout, stderr := aiOneShotRun(t, aiOneShotOptions{render: true, width: 80, style: "dark", status: true},
		aiEvent{TextDelta: &aiTextDelta{Text: "Answer."}},
		aiEvent{TurnDone: &aiTurnDone{}},
	)
	if !strings.Contains(stderr, "thinking…") {
		t.Errorf("status line missing from stderr: %q", stderr)
	}
	if !strings.HasSuffix(stderr, "\r\x1b[2K") {
		t.Errorf("status line not erased at the end: %q", stderr)
	}
	if !strings.Contains(stripANSI(stdout), "Answer.") {
		t.Errorf("stdout = %q", stdout)
	}
}
