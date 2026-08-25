package main

import (
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
