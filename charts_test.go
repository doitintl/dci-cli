package main

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestUtilizationCell(t *testing.T) {
	cases := []struct {
		ratio float64
		want  string
	}{
		{0.63, "▓▓▓▓▓▓░░░░ 63%"},
		{0, "░░░░░░░░░░ 0%"},
		{1.0, "▓▓▓▓▓▓▓▓▓▓ 100%"},
		{5.72, "▓▓▓▓▓▓▓▓▓▓ 572%"},
		{-0.5, "░░░░░░░░░░ 0%"},
	}
	for _, c := range cases {
		if got := utilizationCell(c.ratio); got != c.want {
			t.Errorf("utilizationCell(%v) = %q, want %q", c.ratio, got, c.want)
		}
	}
}

func TestAugmentTableViewColumns(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	originalCommand := invokedCommandName
	t.Cleanup(func() { invokedCommandName = originalCommand })

	rows := []map[string]interface{}{
		{"budget name": "A", "amount": 1000.0, "spend to date": 660.0},
		{"budget name": "B", "amount": 1000.0, "spend to date": 950.0},
	}
	keys := []string{"budget name", "amount", "spend to date", "risk"}

	invokedCommandName = "list-reports"
	if got := augmentTableViewColumns(rows, keys); len(got) != len(keys) {
		t.Fatalf("other commands must be untouched, got %v", got)
	}

	invokedCommandName = "list-budgets"
	got := augmentTableViewColumns(rows, keys)
	want := []string{"budget name", "amount", "spend to date", "utilization", "risk"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("columns = %v, want %v", got, want)
	}
	if cell, _ := rows[0]["utilization"].(string); cell != "▓▓▓▓▓▓▓░░░ 66%" {
		t.Fatalf("row 0 utilization = %q", cell)
	}
	if _, flagged := rows[0]["utilizationRisk"]; flagged {
		t.Fatal("66% must not carry the risk accent")
	}
	if flag, _ := rows[1]["utilizationRisk"].(string); flag != "accent-red" {
		t.Fatalf("95%% must carry the red accent, got %q", flag)
	}
	if viper.GetString("table-accent-column") != "utilization" {
		t.Fatal("accent column must be registered for the renderer")
	}

	// A listing without the fields must be left exactly as-is.
	viper.Reset()
	bare := []map[string]interface{}{{"budget name": "A"}}
	if got := augmentTableViewColumns(bare, keys); len(got) != len(keys) {
		t.Fatalf("missing fields must not add a column, got %v", got)
	}
	if viper.GetString("table-accent-column") != "" {
		t.Fatal("missing fields must not register an accent")
	}
}

func captureStderr(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = writer
	run()
	writer.Close()
	os.Stderr = original
	buffer := make([]byte, 4096)
	n, _ := reader.Read(buffer)
	return string(buffer[:n])
}

func TestChartSeriesAndRendering(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Cleanup(resetChartState)

	// Without the flag, stashing is a no-op.
	resetChartState()
	setChartSeries("cost", []string{"W1", "W2"}, map[string]float64{"W1": 1, "W2": 2})
	if chartSeries != nil {
		t.Fatal("chart series must not be stashed without --chart")
	}

	viper.Set("chart-requested", true)
	setChartSeries("cost", []string{"W1", "W2", "W3"}, map[string]float64{"W1": 1, "W2": 2, "W3": 3})
	if chartSeries == nil || len(chartSeries.values) != 3 {
		t.Fatalf("chart series not stashed: %+v", chartSeries)
	}

	out := captureStderr(t, maybeRenderChart)
	if !strings.Contains(out, "cost by period — W1 → W3") {
		t.Fatalf("chart caption missing from output: %q", out)
	}
	if chartSeries != nil {
		t.Fatal("rendering must consume the series")
	}

	// No qualifying series: the flag is a no-op with a one-line stderr note.
	out = captureStderr(t, maybeRenderChart)
	if !strings.Contains(out, "no chart rendered") {
		t.Fatalf("expected the no-op note, got %q", out)
	}

	// Agent mode: accepted and ignored with no output at all.
	originalAgent := agentMode
	agentMode = true
	t.Cleanup(func() { agentMode = originalAgent })
	if out := captureStderr(t, maybeRenderChart); out != "" {
		t.Fatalf("agent mode must be silent, got %q", out)
	}
}

func TestStyleUpdateNoticePassthrough(t *testing.T) {
	forceTUI(t, false)
	notice := "A new version of dci is available: 1.0.0 → 1.1.0\n"
	if got := styleUpdateNotice(notice); got != notice {
		t.Fatalf("non-TUI notice must be byte-identical, got %q", got)
	}
}

func TestAnnounceLoginSuccessPlainOutput(t *testing.T) {
	forceTUI(t, false)
	dir := t.TempDir()

	out := captureStderr(t, func() { announceLoginSuccess(dir, false) })
	if out != "Authenticated successfully.\n" {
		t.Fatalf("plain login output changed: %q", out)
	}

	out = captureStderr(t, func() { announceLoginSuccess(dir, true) })
	want := "Detected DoiT account. Set default customer context to 'doit.com'.\n" +
		"To use a different context: dci customer-context set <CONTEXT>\n" +
		"Authenticated successfully.\n"
	if out != want {
		t.Fatalf("doer login output changed: %q", out)
	}
}
