package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
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
	if flag, _ := rows[0]["utilizationRisk"].(string); flag != "utilization-green" {
		t.Fatalf("66%% must carry the green band, got %q", flag)
	}
	if flag, _ := rows[1]["utilizationRisk"].(string); flag != "utilization-red" {
		t.Fatalf("95%% must carry the red band, got %q", flag)
	}
	// The banded accent colors the bar cell by cell: filled cells green up
	// to the amber and red deciles, empty cells dim, percent in the band.
	colored := accentUtilizationCell("▓▓▓▓▓▓▓▓░░ 84%", "amber")
	if stripANSI(colored) != "▓▓▓▓▓▓▓▓░░ 84%" {
		t.Fatalf("banding must not change the text: %q", stripANSI(colored))
	}
	if !strings.Contains(colored, "\x1b[32m▓") || !strings.Contains(colored, "\x1b[33m▓") {
		t.Fatalf("an 80%% bar must carry green and amber cells: %q", colored)
	}
	if strings.Contains(colored, "\x1b[31m▓") {
		t.Fatalf("an 80%% bar must not reach the red decile: %q", colored)
	}
	if !strings.Contains(colored, "\x1b[1;33m8") {
		t.Fatalf("the percent must take the row band: %q", colored)
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
	setChartSeries("cost", []string{"W1", "W2"}, map[string]float64{"W1": 1, "W2": 2}, nil)
	if chartSeries != nil {
		t.Fatal("chart series must not be stashed without --chart")
	}

	viper.Set("chart-requested", true)
	setChartSeries("cost", []string{"W1", "W2", "W3"}, map[string]float64{"W1": 1, "W2": 2, "W3": 3}, nil)
	if chartSeries == nil || len(chartSeries.values) != 3 {
		t.Fatalf("chart series not stashed: %+v", chartSeries)
	}

	out := captureStderr(t, func() { maybeRenderChart(0) })
	if !strings.Contains(out, "cost by period — W1 → W3") {
		t.Fatalf("chart caption missing from output: %q", out)
	}
	if chartSeries != nil {
		t.Fatal("rendering must consume the series")
	}

	// No qualifying series: the flag is a no-op with a one-line stderr note.
	out = captureStderr(t, func() { maybeRenderChart(0) })
	if !strings.Contains(out, "no chart rendered") {
		t.Fatalf("expected the no-op note, got %q", out)
	}

	// Agent mode: accepted and ignored with no output at all.
	originalAgent := agentMode
	agentMode = true
	t.Cleanup(func() { agentMode = originalAgent })
	if out := captureStderr(t, func() { maybeRenderChart(0) }); out != "" {
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

func TestSetChartSeriesFoldsGroupsIntoOther(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Cleanup(resetChartState)
	viper.Set("chart-requested", true)

	groups := []chartGroupSeries{}
	for i := 0; i < 8; i++ {
		groups = append(groups, chartGroupSeries{
			name:   string(rune('A' + i)),
			values: []float64{1, 2},
		})
	}
	setChartSeries("cost", []string{"W1", "W2"}, map[string]float64{"W1": 8, "W2": 16}, groups)
	if chartSeries == nil || len(chartSeries.groups) != chartMaxGroups+1 {
		t.Fatalf("groups = %+v, want top %d + other", chartSeries.groups, chartMaxGroups)
	}
	other := chartSeries.groups[chartMaxGroups]
	if other.name != "other (3 groups)" {
		t.Fatalf("other segment name = %q", other.name)
	}
	if other.values[0] != 3 || other.values[1] != 6 {
		t.Fatalf("other segment values = %v, want the folded sums", other.values)
	}
}

func TestSparklineChartRendering(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Cleanup(resetChartState)
	viper.Set("chart-requested", true)
	viper.Set("chart-mode", "sparkline")

	chartSeries = &chartSeriesData{
		metric:  "cost",
		periods: []string{"W1", "W2", "W3", "W4"},
		values:  []float64{0, 25, -5, 100},
	}
	out := stripANSI(captureStderr(t, func() { maybeRenderChart(0) }))
	if !strings.Contains(out, "cost by period — W1 → W4") {
		t.Fatalf("sparkline caption missing: %q", out)
	}
	spark := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	if got := len([]rune(spark)); got != 4 {
		t.Fatalf("sparkline = %q (%d cells), want one cell per period", spark, got)
	}
	// Zero and negative periods sit on the baseline; the peak tops out.
	if runes := []rune(spark); runes[0] != '▁' || runes[2] != '▁' || runes[3] != '█' {
		t.Fatalf("sparkline shape = %q, want baseline/low/baseline/peak", spark)
	}

	// A series wider than the render width keeps the most recent periods.
	wide := &chartSeriesData{metric: "cost", periods: make([]string, 300), values: make([]float64, 300)}
	for i := range wide.periods {
		wide.periods[i] = fmt.Sprintf("P%d", i)
		wide.values[i] = float64(i)
	}
	narrow := stripANSI(renderSparklineChart(wide, 50))
	if !strings.Contains(narrow, "P250 → P299") {
		t.Fatalf("wide sparkline must keep the most recent periods: %q", narrow)
	}
}

func TestHeatmapChartRenderingAndFallback(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Cleanup(resetChartState)
	viper.Set("chart-requested", true)
	viper.Set("chart-mode", "heatmap")
	viper.Set("table-color", true)
	forceChartColor(t, true)

	series := &chartSeriesData{
		metric:  "cost",
		periods: []string{"W1", "W2"},
		values:  []float64{30, 60},
		groups: []chartGroupSeries{
			{name: "svc-a", values: []float64{20, 40}},
			{name: "a-service-with-a-very-long-name-indeed", values: []float64{10, 20}},
		},
	}
	chartSeries = series
	out := stripANSI(captureStderr(t, func() { maybeRenderChart(0) }))
	if !strings.Contains(out, "svc-a") || !strings.Contains(out, "…") {
		t.Fatalf("heatmap rows missing or label unbounded: %q", out)
	}
	if !strings.Contains(out, "low ") || !strings.Contains(out, " high — cost by period") {
		t.Fatalf("heatmap scale legend missing: %q", out)
	}

	// One group or a colorless terminal falls back to the line chart.
	forceChartColor(t, false)
	chartSeries = series
	out = captureStderr(t, func() { maybeRenderChart(0) })
	if !strings.Contains(out, "┤") && !strings.Contains(out, "┼") {
		t.Fatalf("colorless heatmap must fall back to the line chart: %q", out)
	}
}

func forceChartColor(t *testing.T, capable bool) {
	t.Helper()
	original := chartColorCapable
	chartColorCapable = func() bool { return capable }
	t.Cleanup(func() { chartColorCapable = original })
}

func TestStackedChartRenderingAndFallbacks(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Cleanup(resetChartState)
	viper.Set("chart-requested", true)
	viper.Set("chart-mode", "stacked")
	viper.Set("table-color", true)
	forceChartColor(t, true)

	stash := func() {
		setChartSeries("cost", []string{"W1", "W2"}, map[string]float64{"W1": 30, "W2": 60},
			[]chartGroupSeries{
				{name: "svc-a", values: []float64{20, 40}},
				{name: "svc-b", values: []float64{10, 20}},
			})
	}

	stash()
	out := captureStderr(t, func() { maybeRenderChart(0) })
	for _, want := range []string{"cost by period — W1 → W2", "█ svc-a  67%", "█ svc-b  33%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stacked output missing %q:\n%s", want, out)
		}
	}

	// Colorless terminals cannot tell segments apart: fall back to the line.
	viper.Set("table-color", false)
	stash()
	out = captureStderr(t, func() { maybeRenderChart(0) })
	if strings.Contains(out, "svc-a") {
		t.Fatalf("colorless output must fall back to the line chart:\n%s", out)
	}
	if !strings.Contains(out, "cost by period — W1 → W2") {
		t.Fatalf("line fallback missing its caption:\n%s", out)
	}

	// Same fallback when the color policy allows but the terminal profile
	// strips color anyway (unset or unknown TERM).
	viper.Set("table-color", true)
	forceChartColor(t, false)
	stash()
	out = captureStderr(t, func() { maybeRenderChart(0) })
	if strings.Contains(out, "svc-a") {
		t.Fatalf("Ascii-profile output must fall back to the line chart:\n%s", out)
	}
	forceChartColor(t, true)

	// An ungrouped result has nothing to stack: same fallback.
	viper.Set("table-color", true)
	setChartSeries("cost", []string{"W1", "W2"}, map[string]float64{"W1": 30, "W2": 60},
		[]chartGroupSeries{{name: "all", values: []float64{30, 60}}})
	out = captureStderr(t, func() { maybeRenderChart(0) })
	if strings.Contains(out, "█ all") {
		t.Fatalf("single-group output must fall back to the line chart:\n%s", out)
	}

	// --chart line renders the line even when groups are available.
	viper.Set("chart-mode", "line")
	stash()
	out = captureStderr(t, func() { maybeRenderChart(0) })
	if strings.Contains(out, "svc-a") {
		t.Fatalf("--chart line must not render the stacked view:\n%s", out)
	}
}

func TestChartAlignsWithTableWidth(t *testing.T) {
	if got := chartRenderWidth(120); got != 120 {
		t.Fatalf("chartRenderWidth(120) = %d, want the table width", got)
	}
	if got := chartRenderWidth(0); got < 20 {
		t.Fatalf("chartRenderWidth(0) = %d, want the terminal fallback", got)
	}

	series := &chartSeriesData{
		metric:  "cost",
		periods: []string{"W1", "W2"},
		values:  []float64{30, 60},
		groups: []chartGroupSeries{
			{name: "svc-a", values: []float64{20, 40}},
			{name: "svc-b", values: []float64{10, 20}},
		},
	}
	out := renderStackedChart(series, 60)
	first := strings.Split(out, "\n")[0]
	// Cell width, not rune count: lipgloss v2 styles emit ANSI even under go
	// test (degradation moved to the writer), so the raw string carries codes.
	if got := lipgloss.Width(first); got != 60 {
		t.Fatalf("stacked chart canvas width = %d, want 60 (aligned with the table)", got)
	}
}

func TestActiveChartThemeResolution(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/analytics/v1/settings/active-theme":
			fmt.Fprint(writer, `{"themeId":"CustomTheme12345"}`)
		case "/analytics/v1/settings/themes/CustomTheme12345":
			fmt.Fprint(writer, `{"id":"CustomTheme12345","name":"Mine","colors":{"light":["#111111","#222222"],"dark":["#AAAAAA","#BBBBBB"]}}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("DCI_API_BASE_URL", server.URL)
	t.Setenv("DCI_API_KEY", "test-token")
	previousClient := resolverHTTPClient
	resolverHTTPClient = server.Client()
	t.Cleanup(func() { resolverHTTPClient = previousClient })

	theme, ok := fetchActiveChartThemeLive()
	if !ok || len(theme.light) != 2 || theme.dark[1] != "#BBBBBB" {
		t.Fatalf("custom theme = %+v ok=%v, want the fetched palette", theme, ok)
	}

	// The padded palette keeps the custom colors first and fills the rest
	// from the DoiT preset, with the gray "other" entry last.
	originalFetch := fetchActiveChartTheme
	fetchActiveChartTheme = func() (chartThemeColors, bool) { return theme, true }
	t.Cleanup(func() { fetchActiveChartTheme = originalFetch })
	palette := chartThemePalette()
	if len(palette) != chartMaxGroups+1 {
		t.Fatalf("palette size = %d, want %d", len(palette), chartMaxGroups+1)
	}
	first, ok := palette[0].GetForeground().(chartAdaptiveColor)
	if !ok || first.dark != "#AAAAAA" {
		t.Fatalf("palette[0] = %#v, want the custom theme's first color", palette[0].GetForeground())
	}
	third, ok := palette[2].GetForeground().(chartAdaptiveColor)
	if !ok || third.dark != presetChartThemes["doit"].dark[2] {
		t.Fatalf("palette[2] = %#v, want DoiT padding", palette[2].GetForeground())
	}
}

func TestActiveChartThemePresetAndDefault(t *testing.T) {
	themeID := "default"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/analytics/v1/settings/active-theme" {
			t.Errorf("unexpected request %s (presets must not be fetched)", request.URL.Path)
		}
		fmt.Fprintf(writer, `{"themeId":%q}`, themeID)
	}))
	t.Cleanup(server.Close)
	t.Setenv("DCI_API_BASE_URL", server.URL)
	t.Setenv("DCI_API_KEY", "test-token")
	previousClient := resolverHTTPClient
	resolverHTTPClient = server.Client()
	t.Cleanup(func() { resolverHTTPClient = previousClient })

	if _, ok := fetchActiveChartThemeLive(); ok {
		t.Fatal("the default sentinel must fall back to the built-in palette")
	}

	themeID = "ocean-night"
	theme, ok := fetchActiveChartThemeLive()
	if !ok || theme.dark[0] != presetChartThemes["ocean-night"].dark[0] {
		t.Fatalf("preset theme = %+v ok=%v, want the embedded ocean-night palette", theme, ok)
	}
}
