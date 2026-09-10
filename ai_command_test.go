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
