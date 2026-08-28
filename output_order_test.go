package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// outputOrderConfigDir points dciConfigDir() at a fresh temp dir for the
// test's persisted-settings reads and writes.
func outputOrderConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DCI_CONFIG_DIR", dir)
	resetDCIConfigDirCache()
	t.Cleanup(resetDCIConfigDirCache)
	return dir
}

func TestParseOutputOrder(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"terminal", outputOrderTerminal, true},
		{"CLASSIC", outputOrderClassic, true},
		{"  terminal  ", outputOrderTerminal, true},
		{"", "", false},
		{"auto", "", false}, // reserved, not accepted (OUTPUT-ORDER-SPEC §5)
		{"bogus", "", false},
	}
	for _, tc := range cases {
		got, ok := parseOutputOrder(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseOutputOrder(%q) = %q/%v, want %q/%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestResolveOutputOrderPrecedence(t *testing.T) {
	dir := outputOrderConfigDir(t)
	t.Setenv(outputOrderEnvName, "")

	// Nothing configured: the built-in default.
	order, source, err := resolveOutputOrder("")
	if err != nil || order != outputOrderDefault || source != "default" {
		t.Fatalf("unconfigured resolve = %q/%q/%v, want %q/default/nil", order, source, err, outputOrderDefault)
	}

	// Persisted file wins over the default.
	if err := saveCLISettings(dir, cliSettings{OutputOrder: outputOrderClassic}); err != nil {
		t.Fatal(err)
	}
	order, source, _ = resolveOutputOrder("")
	if order != outputOrderClassic || source != cliSettingsFileName {
		t.Fatalf("file resolve = %q/%q, want classic/%s", order, source, cliSettingsFileName)
	}

	// Env wins over the file.
	t.Setenv(outputOrderEnvName, "terminal")
	order, source, _ = resolveOutputOrder("")
	if order != outputOrderTerminal || source != outputOrderEnvName {
		t.Fatalf("env resolve = %q/%q, want terminal/%s", order, source, outputOrderEnvName)
	}

	// The flag wins over everything.
	order, source, _ = resolveOutputOrder("classic")
	if order != outputOrderClassic || source != "--output-order" {
		t.Fatalf("flag resolve = %q/%q, want classic/--output-order", order, source)
	}
}

func TestResolveOutputOrderInvalidFlagErrors(t *testing.T) {
	outputOrderConfigDir(t)
	if _, _, err := resolveOutputOrder("bogus"); err == nil {
		t.Fatal("invalid --output-order accepted, want an error")
	}
	_, _, err := resolveOutputOrder("auto")
	if err == nil {
		t.Fatal("--output-order auto accepted, want the reserved-value error")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("auto error = %q, want it to say the value is reserved", err)
	}
}

func TestResolveOutputOrderInvalidEnvWarnsAndFallsThrough(t *testing.T) {
	dir := outputOrderConfigDir(t)
	if err := saveCLISettings(dir, cliSettings{OutputOrder: outputOrderClassic}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(outputOrderEnvName, "bogus")

	var order, source string
	stderr := captureStderr(t, func() {
		order, source, _ = resolveOutputOrder("")
	})
	if order != outputOrderClassic || source != cliSettingsFileName {
		t.Fatalf("resolve = %q/%q, want the file's classic after the env typo", order, source)
	}
	if !strings.Contains(stderr, outputOrderEnvName) {
		t.Errorf("stderr = %q, want a warning naming %s", stderr, outputOrderEnvName)
	}
}

func TestResolveOutputOrderInvalidFileWarnsAndFallsThrough(t *testing.T) {
	dir := outputOrderConfigDir(t)
	t.Setenv(outputOrderEnvName, "")
	if err := os.WriteFile(cliSettingsPath(dir), []byte(`{"output_order":"sideways"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var order, source string
	stderr := captureStderr(t, func() {
		order, source, _ = resolveOutputOrder("")
	})
	if order != outputOrderDefault || source != "default" {
		t.Fatalf("resolve = %q/%q, want the default after the file typo", order, source)
	}
	if !strings.Contains(stderr, cliSettingsFileName) {
		t.Errorf("stderr = %q, want a warning naming %s", stderr, cliSettingsFileName)
	}
}

func TestTerminalOrderActiveGate(t *testing.T) {
	oldAgent := agentMode
	t.Cleanup(func() {
		agentMode = oldAgent
		viper.Set("output-order", nil)
		viper.Set("rsh-output-format", nil)
	})
	t.Setenv("DCI_SESSION_RENDER", "")

	cases := []struct {
		name    string
		order   string
		format  string
		agent   bool
		tui     bool
		session bool
		want    bool
	}{
		{"terminal table tty", outputOrderTerminal, "table", false, true, false, true},
		{"terminal auto tty", outputOrderTerminal, "auto", false, true, false, true},
		{"terminal table session render", outputOrderTerminal, "table", false, false, true, true},
		{"classic table tty", outputOrderClassic, "table", false, true, false, false},
		{"terminal json tty", outputOrderTerminal, "json", false, true, false, false},
		{"terminal toon tty", outputOrderTerminal, "toon", false, true, false, false},
		{"terminal table agent", outputOrderTerminal, "table", true, true, false, false},
		{"terminal table piped", outputOrderTerminal, "table", false, false, false, false},
		{"unset key (outside a command run)", "", "table", false, true, false, false},
	}
	for _, tc := range cases {
		viper.Set("output-order", tc.order)
		viper.Set("rsh-output-format", tc.format)
		agentMode = tc.agent
		forceTUI(t, tc.tui)
		if tc.session {
			t.Setenv("DCI_SESSION_RENDER", "1")
		} else {
			t.Setenv("DCI_SESSION_RENDER", "")
		}
		if got := terminalOrderActive(); got != tc.want {
			t.Errorf("%s: terminalOrderActive() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCLISettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := saveCLISettings(dir, cliSettings{OutputOrder: outputOrderClassic}); err != nil {
		t.Fatal(err)
	}
	if got := loadCLISettings(dir).OutputOrder; got != outputOrderClassic {
		t.Fatalf("round-tripped output_order = %q, want classic", got)
	}
	// An absent file is defaults, not an error.
	if got := loadCLISettings(t.TempDir()); got != (cliSettings{}) {
		t.Fatalf("absent file settings = %+v, want zero value", got)
	}
}

// TestCLISettingsSurviveAPIBaseOverride pins cli_settings.json's membership
// in applyAPIBaseOverride's surviving-local-state registry: the persisted
// ordering must be readable during an override session (carried into the
// temp dir), and a `dci config output-order` run under the override — e.g.
// from a `dci ai` subprocess inheriting the temp DCI_CONFIG_DIR — must be
// written back to the real config dir before the temp dir is deleted.
func TestCLISettingsSurviveAPIBaseOverride(t *testing.T) {
	realDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realDir, "apis.json"), []byte(`{"dci":{"base":"https://api.doit.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveCLISettings(realDir, cliSettings{OutputOrder: outputOrderClassic}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DCI_API_BASE_URL", "https://dev.example.com")
	t.Setenv(outputOrderEnvName, "")
	resetDCIConfigDirCache()
	t.Cleanup(resetDCIConfigDirCache)

	cleanup := applyAPIBaseOverride(realDir, []string{"dci", "list-budgets"})
	tempConfigDir := os.Getenv("DCI_CONFIG_DIR")
	resetDCIConfigDirCache()

	// Carried in: the persisted choice resolves during the override session.
	order, source, _ := resolveOutputOrder("")
	if order != outputOrderClassic || source != cliSettingsFileName {
		t.Fatalf("override-session resolve = %q/%q, want the carried-in classic/%s", order, source, cliSettingsFileName)
	}

	// Changed under the override: written back to the real dir on cleanup.
	if err := saveCLISettings(tempConfigDir, cliSettings{OutputOrder: outputOrderTerminal}); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got := loadCLISettings(realDir).OutputOrder; got != outputOrderTerminal {
		t.Fatalf("real cli_settings.json output_order = %q after cleanup, want terminal written back", got)
	}
}

func TestConfigOutputOrderCommandPersists(t *testing.T) {
	dir := outputOrderConfigDir(t)
	oldRoot := cli.Root
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() { cli.Root = oldRoot })
	registerConfigCommand(dir)

	var configCmd *cobra.Command
	for _, cmd := range cli.Root.Commands() {
		if cmd.Name() == "config" {
			configCmd = cmd
			break
		}
	}
	if configCmd == nil {
		t.Fatal("config command not registered")
	}
	var setCmd *cobra.Command
	for _, cmd := range configCmd.Commands() {
		if cmd.Name() == "output-order" {
			setCmd = cmd
			break
		}
	}
	if setCmd == nil {
		t.Fatal("config output-order subcommand not registered")
	}

	if err := setCmd.RunE(setCmd, []string{"classic"}); err != nil {
		t.Fatal(err)
	}
	if got := loadCLISettings(dir).OutputOrder; got != outputOrderClassic {
		t.Fatalf("persisted output_order = %q, want classic", got)
	}
	if err := setCmd.RunE(setCmd, []string{"auto"}); err == nil {
		t.Fatal("config output-order auto accepted, want the reserved-value error")
	}
	if got := loadCLISettings(dir).OutputOrder; got != outputOrderClassic {
		t.Fatalf("rejected value overwrote the setting: %q", got)
	}
}
