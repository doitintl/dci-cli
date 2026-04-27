package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func TestNormalizeArgs(t *testing.T) {
	setupTestRoot(t)

	tests := []struct {
		name string
		in   []string
		out  []string
	}{
		{
			name: "no args becomes help",
			in:   []string{"dci"},
			out:  []string{"dci", "--help"},
		},
		{
			name: "help flag stays local",
			in:   []string{"dci", "--help"},
			out:  []string{"dci", "--help"},
		},
		{
			name: "help command stays local",
			in:   []string{"dci", "help"},
			out:  []string{"dci", "help"},
		},
		{
			name: "root command stays local",
			in:   []string{"dci", "status"},
			out:  []string{"dci", "status"},
		},
		{
			name: "api command is prefixed",
			in:   []string{"dci", "list-budgets"},
			out:  []string{"dci", "dci", "list-budgets"},
		},
		{
			name: "global flag before root command stays local",
			in:   []string{"dci", "--rsh-timeout", "5s", "status"},
			out:  []string{"dci", "--rsh-timeout", "5s", "status"},
		},
		{
			name: "global flag before api command is prefixed",
			in:   []string{"dci", "--rsh-timeout", "5s", "list-budgets"},
			out:  []string{"dci", "dci", "--rsh-timeout", "5s", "list-budgets"},
		},
		{
			name: "completion stays local",
			in:   []string{"dci", "completion", "zsh"},
			out:  []string{"dci", "completion", "zsh"},
		},
		{
			name: "__complete with empty arg stays at root",
			in:   []string{"dci", "__complete", ""},
			out:  []string{"dci", "__complete", ""},
		},
		{
			name: "__complete with root command stays at root",
			in:   []string{"dci", "__complete", "status", ""},
			out:  []string{"dci", "__complete", "status", ""},
		},
		{
			name: "__complete with api command gets dci prefix",
			in:   []string{"dci", "__complete", "list-budgets", ""},
			out:  []string{"dci", "__complete", "dci", "list-budgets", ""},
		},
		{
			name: "__completeNoDesc with api command gets dci prefix",
			in:   []string{"dci", "__completeNoDesc", "list-budgets", "--"},
			out:  []string{"dci", "__completeNoDesc", "dci", "list-budgets", "--"},
		},
		{
			name: "__completeNoDesc with root command stays at root",
			in:   []string{"dci", "__completeNoDesc", "login", ""},
			out:  []string{"dci", "__completeNoDesc", "login", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeArgs(tt.in)
			if !reflect.DeepEqual(got, tt.out) {
				t.Fatalf("normalizeArgs() = %v, want %v", got, tt.out)
			}
		})
	}
}

func TestRejectProfileFlags(t *testing.T) {
	setupTestRoot(t)

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "no profile flags", args: []string{"dci", "status"}, wantErr: false},
		{name: "long profile", args: []string{"dci", "--profile", "other", "status"}, wantErr: true},
		{name: "long profile equals", args: []string{"dci", "--profile=other", "status"}, wantErr: true},
		{name: "rsh profile", args: []string{"dci", "--rsh-profile", "other", "status"}, wantErr: true},
		{name: "rsh profile equals", args: []string{"dci", "--rsh-profile=other", "status"}, wantErr: true},
		{name: "short profile", args: []string{"dci", "-p", "other", "status"}, wantErr: true},
		{name: "short profile compact", args: []string{"dci", "-pother", "status"}, wantErr: true},
		{name: "short profile equals", args: []string{"dci", "-p=other", "status"}, wantErr: true},
		{name: "profile flag later", args: []string{"dci", "status", "-p", "other"}, wantErr: true},
		{name: "operand after double dash", args: []string{"dci", "status", "--", "-p"}, wantErr: false},
		{name: "value containing p for another short flag", args: []string{"dci", "-Mprofile", "status"}, wantErr: false},
		{name: "other short flags", args: []string{"dci", "-hv", "status"}, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rejectProfileFlags(tt.args)
			if tt.wantErr && err == nil {
				t.Fatalf("rejectProfileFlags(%v) expected error", tt.args)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("rejectProfileFlags(%v) unexpected error: %v", tt.args, err)
			}
		})
	}
}

func TestLockToDCI(t *testing.T) {
	oldRoot := cli.Root
	root := &cobra.Command{Use: "dci"}
	cli.Root = root
	t.Cleanup(func() {
		cli.Root = oldRoot
	})

	root.AddCommand(
		&cobra.Command{Use: "dci", GroupID: "api"},
		&cobra.Command{Use: "help"},
		&cobra.Command{Use: "status"},
		&cobra.Command{Use: "login"},
		&cobra.Command{Use: "logout"},
		&cobra.Command{Use: "api"},
		&cobra.Command{Use: "generic-cmd", GroupID: "generic"},
		&cobra.Command{Use: "other-api", GroupID: "api"},
	)

	lockToDCI()

	got := make([]string, 0)
	for _, cmd := range cli.Root.Commands() {
		got = append(got, cmd.Name())
	}
	sort.Strings(got)

	want := []string{"dci", "help", "login", "logout", "status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining commands = %v, want %v", got, want)
	}
}

func TestBrandRootAndDCICommands(t *testing.T) {
	oldRoot := cli.Root
	root := &cobra.Command{Use: "dci"}
	dciCmd := &cobra.Command{Use: "dci"}
	root.AddCommand(dciCmd)
	cli.Root = root
	t.Cleanup(func() {
		cli.Root = oldRoot
	})

	brandRootCommand()
	brandDCIRootCommand()

	if cli.Root.Short != "DoiT Cloud Intelligence CLI" {
		t.Fatalf("root short = %q", cli.Root.Short)
	}
	if cli.Root.Long != dciLongDescription {
		t.Fatalf("root long = %q", cli.Root.Long)
	}
	if cli.Root.Example != strings.Join(rootExamples, "\n") {
		t.Fatalf("root example mismatch:\n%s", cli.Root.Example)
	}
	if cli.Root.UsageTemplate() != dciUsageTemplate {
		t.Fatalf("root usage template mismatch")
	}

	if dciCmd.Short != "DoiT Cloud Intelligence API CLI" {
		t.Fatalf("dci short = %q", dciCmd.Short)
	}
	if dciCmd.Long != dciLongDescription {
		t.Fatalf("dci long = %q", dciCmd.Long)
	}
	if dciCmd.Example != strings.Join(apiExamples, "\n") {
		t.Fatalf("dci example mismatch:\n%s", dciCmd.Example)
	}
}

func TestCustomizeDCIUsageAppliesTemplateRecursively(t *testing.T) {
	oldRoot := cli.Root
	root := &cobra.Command{Use: "dci"}
	dciCmd := &cobra.Command{Use: "dci"}
	child := &cobra.Command{Use: "list-budgets"}
	grandChild := &cobra.Command{Use: "get-report"}
	child.AddCommand(grandChild)
	dciCmd.AddCommand(child)
	root.AddCommand(dciCmd)
	cli.Root = root
	t.Cleanup(func() {
		cli.Root = oldRoot
	})

	customizeDCIUsage()

	if dciCmd.UsageTemplate() != dciUsageTemplate {
		t.Fatalf("dci command usage template mismatch")
	}
	if child.UsageTemplate() != dciUsageTemplate {
		t.Fatalf("child usage template mismatch")
	}
	if grandChild.UsageTemplate() != dciUsageTemplate {
		t.Fatalf("grandchild usage template mismatch")
	}
}

func TestEnsureConfigPermissions(t *testing.T) {
	dir := t.TempDir()
	configured, err := ensureConfig(dir)
	if err != nil {
		t.Fatalf("ensureConfig(create) error: %v", err)
	}
	if !configured {
		t.Fatalf("ensureConfig(create) configured=false, want true")
	}

	configPath := filepath.Join(dir, "apis.json")
	assertPrivateFilePerms(t, configPath)

	if err := os.Chmod(configPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	configured, err = ensureConfig(dir)
	if err != nil {
		t.Fatalf("ensureConfig(existing) error: %v", err)
	}
	if configured {
		t.Fatalf("ensureConfig(existing) configured=true, want false")
	}
	assertPrivateFilePerms(t, configPath)
}

func TestApiBase(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    string
		wantErr string
	}{
		{name: "no env var", env: "", want: defaultAPIBase},
		{name: "valid override", env: "https://dev-app.doit.com", want: "https://dev-app.doit.com"},
		{name: "trailing slash stripped", env: "https://dev-app.doit.com/", want: "https://dev-app.doit.com"},
		{name: "multiple trailing slashes", env: "https://dev-app.doit.com///", want: "https://dev-app.doit.com"},
		{name: "whitespace trimmed", env: "  https://dev-app.doit.com  ", want: "https://dev-app.doit.com"},
		{name: "empty after trim is default", env: "   ", want: defaultAPIBase},
		{name: "http rejected", env: "http://dev-app.doit.com", wantErr: "must use https://"},
		{name: "no scheme rejected", env: "dev-app.doit.com", wantErr: "must use https://"},
		{name: "ftp rejected", env: "ftp://dev-app.doit.com", wantErr: "must use https://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DCI_API_BASE_URL", tt.env)
			got, err := apiBase()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("apiBase() error = nil, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("apiBase() error = %q, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("apiBase() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("apiBase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureConfigUpdatesBaseURL(t *testing.T) {
	dir := t.TempDir()

	// First run: create config with default base.
	configured, err := ensureConfig(dir)
	if err != nil {
		t.Fatalf("ensureConfig(create) error: %v", err)
	}
	if !configured {
		t.Fatalf("expected configured=true on first run")
	}

	// Verify default base is written.
	configPath := filepath.Join(dir, "apis.json")
	assertConfigBase(t, configPath, defaultAPIBase)

	// Second run with DCI_API_BASE_URL set: should update base in existing config.
	t.Setenv("DCI_API_BASE_URL", "https://dev-app.doit.com")
	configured, err = ensureConfig(dir)
	if err != nil {
		t.Fatalf("ensureConfig(update) error: %v", err)
	}
	if configured {
		t.Fatalf("expected configured=false on second run")
	}
	assertConfigBase(t, configPath, "https://dev-app.doit.com")

	// Third run without env var: base should remain as previously written.
	t.Setenv("DCI_API_BASE_URL", "")
	configured, err = ensureConfig(dir)
	if err != nil {
		t.Fatalf("ensureConfig(no-op) error: %v", err)
	}
	if configured {
		t.Fatalf("expected configured=false on third run")
	}
	assertConfigBase(t, configPath, "https://dev-app.doit.com")
}

func assertConfigBase(t *testing.T, configPath, wantBase string) {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	dci, ok := config["dci"].(map[string]interface{})
	if !ok {
		t.Fatalf("config missing dci key")
	}
	if got := dci["base"].(string); got != wantBase {
		t.Errorf("config base = %q, want %q", got, wantBase)
	}
}

func TestWrapTextDisplayWidth(t *testing.T) {
	got := wrapText("你好a", 2)
	want := "你\n好\na"
	if got != want {
		t.Fatalf("wrapText() = %q, want %q", got, want)
	}
}

func TestTruncateTextDisplayWidth(t *testing.T) {
	got := truncateText("你好abc", 4)
	if runewidth.StringWidth(got) > 4 {
		t.Fatalf("truncateText() width = %d, want <= 4 (value %q)", runewidth.StringWidth(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncateText() = %q, want suffix ellipsis", got)
	}

	got = truncateText("你好", 1)
	if got != "…" {
		t.Fatalf("truncateText(width=1) = %q, want ellipsis", got)
	}
}

func TestCLIIntegrationBehavior(t *testing.T) {
	bin := buildBinary(t)

	t.Run("no args help stays offline", func(t *testing.T) {
		res := runCLI(t, bin)
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if res.exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; output:\n%s", res.exitCode, res.output)
		}
		assertNoOAuthOrPanic(t, res.output)
		assertRootHelpBranded(t, res.output)
	})

	t.Run("help flag stays offline", func(t *testing.T) {
		res := runCLI(t, bin, "--help")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if res.exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; output:\n%s", res.exitCode, res.output)
		}
		assertNoOAuthOrPanic(t, res.output)
		assertRootHelpBranded(t, res.output)
	})

	t.Run("help command stays offline", func(t *testing.T) {
		res := runCLI(t, bin, "help")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if res.exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; output:\n%s", res.exitCode, res.output)
		}
		assertNoOAuthOrPanic(t, res.output)
	})

	t.Run("status works", func(t *testing.T) {
		home := t.TempDir()
		res := runCLIWithHome(t, bin, home, "status")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if res.exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; output:\n%s", res.exitCode, res.output)
		}
		if !strings.Contains(res.output, "DoiT Cloud Intelligence") {
			t.Fatalf("status output missing expected text:\n%s", res.output)
		}

		configDir := extractConfigDirFromStatus(res.output)
		if configDir == "" {
			t.Fatalf("status output missing config dir:\n%s", res.output)
		}
		configPath := filepath.Join(configDir, "apis.json")
		assertPrivateFilePerms(t, configPath)
	})

	t.Run("status shows oauth by default", func(t *testing.T) {
		home := t.TempDir()
		res := runCLIWithHome(t, bin, home, "status")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if !strings.Contains(res.output, "Auth: OAuth (DoiT Console)") {
			t.Fatalf("expected OAuth auth source in status:\n%s", res.output)
		}
	})

	t.Run("status shows api key when DCI_API_KEY set", func(t *testing.T) {
		home := t.TempDir()
		res := runCLIWithEnv(t, bin, home, []string{"DCI_API_KEY=test-key"}, "status")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if !strings.Contains(res.output, "Auth: API key (DCI_API_KEY)") {
			t.Fatalf("expected API key auth source in status:\n%s", res.output)
		}
	})

	t.Run("status shows DCI_API_BASE_URL annotation when set", func(t *testing.T) {
		home := t.TempDir()
		res := runCLIWithEnv(t, bin, home, []string{"DCI_API_BASE_URL=https://dev-app.doit.com"}, "status")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if !strings.Contains(res.output, "API Base: https://dev-app.doit.com (DCI_API_BASE_URL)") {
			t.Fatalf("expected DCI_API_BASE_URL annotation in status:\n%s", res.output)
		}
	})

	t.Run("login rejected when DCI_API_KEY set", func(t *testing.T) {
		home := t.TempDir()
		res := runCLIWithEnv(t, bin, home, []string{"DCI_API_KEY=test-key"}, "login")
		if res.exitCode == 0 {
			t.Fatalf("expected non-zero exit; output:\n%s", res.output)
		}
		if !strings.Contains(res.output, "login is not needed when DCI_API_KEY is set") {
			t.Fatalf("expected login rejection message:\n%s", res.output)
		}
	})

	t.Run("logout rejected when DCI_API_KEY set", func(t *testing.T) {
		home := t.TempDir()
		res := runCLIWithEnv(t, bin, home, []string{"DCI_API_KEY=test-key"}, "logout")
		if res.exitCode == 0 {
			t.Fatalf("expected non-zero exit; output:\n%s", res.output)
		}
		if !strings.Contains(res.output, "unset the environment variable") {
			t.Fatalf("expected logout rejection message:\n%s", res.output)
		}
	})

	t.Run("profile short flag rejected cleanly", func(t *testing.T) {
		res := runCLI(t, bin, "-p", "other", "status")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if res.exitCode == 0 {
			t.Fatalf("exit code = 0, want non-zero; output:\n%s", res.output)
		}
		if !strings.Contains(res.output, "profile selection is currently disabled") {
			t.Fatalf("missing profile rejection message:\n%s", res.output)
		}
		if strings.Contains(strings.ToLower(res.output), "panic") {
			t.Fatalf("unexpected panic in output:\n%s", res.output)
		}
	})

	t.Run("completion help stays offline", func(t *testing.T) {
		res := runCLI(t, bin, "completion", "--help")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if res.exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; output:\n%s", res.exitCode, res.output)
		}
		assertNoOAuthOrPanic(t, res.output)
		for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
			if !strings.Contains(res.output, shell) {
				t.Fatalf("completion help missing %s subcommand:\n%s", shell, res.output)
			}
		}
	})

	t.Run("completion generates valid script", func(t *testing.T) {
		for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
			t.Run(shell, func(t *testing.T) {
				res := runCLI(t, bin, "completion", shell)
				if res.timedOut {
					t.Fatalf("command timed out; output:\n%s", res.output)
				}
				if res.exitCode != 0 {
					t.Fatalf("exit code = %d, want 0; output:\n%s", res.exitCode, res.output)
				}
				assertNoOAuthOrPanic(t, res.output)
				if len(res.output) < 100 {
					t.Fatalf("completion script suspiciously short (%d bytes):\n%s", len(res.output), res.output)
				}
			})
		}
	})

	t.Run("profile long flag rejected cleanly", func(t *testing.T) {
		res := runCLI(t, bin, "--profile", "other", "status")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if res.exitCode == 0 {
			t.Fatalf("exit code = 0, want non-zero; output:\n%s", res.output)
		}
		if !strings.Contains(res.output, "profile selection is currently disabled") {
			t.Fatalf("missing profile rejection message:\n%s", res.output)
		}
		if strings.Contains(strings.ToLower(res.output), "panic") {
			t.Fatalf("unexpected panic in output:\n%s", res.output)
		}
	})
}

type cliResult struct {
	exitCode int
	output   string
	timedOut bool
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dci-test")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(out))
	}
	return bin
}

func runCLI(t *testing.T, bin string, args ...string) cliResult {
	t.Helper()
	return runCLIWithHome(t, bin, t.TempDir(), args...)
}

func runCLIWithEnv(t *testing.T, bin string, home string, extraEnv []string, args ...string) cliResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	xdg := filepath.Join(home, "xdg")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+xdg,
	)
	cmd.Env = append(cmd.Env, extraEnv...)

	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return cliResult{exitCode: -1, output: string(out), timedOut: true}
	}
	if err == nil {
		return cliResult{exitCode: 0, output: string(out)}
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return cliResult{exitCode: exitErr.ExitCode(), output: string(out)}
	}

	t.Fatalf("command failed to start: %v", err)
	return cliResult{}
}

func runCLIWithHome(t *testing.T, bin string, home string, args ...string) cliResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	xdg := filepath.Join(home, "xdg")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+xdg,
	)

	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return cliResult{exitCode: -1, output: string(out), timedOut: true}
	}
	if err == nil {
		return cliResult{exitCode: 0, output: string(out)}
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return cliResult{exitCode: exitErr.ExitCode(), output: string(out)}
	}

	t.Fatalf("command failed to start: %v", err)
	return cliResult{}
}

func assertNoOAuthOrPanic(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "Open your browser to log in") {
		t.Fatalf("unexpected oauth flow output:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "panic") {
		t.Fatalf("unexpected panic output:\n%s", out)
	}
}

func assertRootHelpBranded(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "A generic client for REST-ish APIs") {
		t.Fatalf("unexpected stock restish root help:\n%s", out)
	}
	if !strings.Contains(out, "Command-line interface for the DoiT Cloud Intelligence API.") {
		t.Fatalf("missing DCI root branding in help output:\n%s", out)
	}
}

func assertPrivateFilePerms(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("permissions for %s = %o, want owner-only (0600)", path, perm)
	}
}

func extractConfigDirFromStatus(out string) string {
	for _, line := range strings.Split(out, "\n") {
		const prefix = "Config Dir: "
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func setupTestRoot(t *testing.T) {
	t.Helper()

	oldRoot := cli.Root
	root := &cobra.Command{Use: "dci"}
	root.PersistentFlags().BoolP("help", "h", false, "")
	root.PersistentFlags().Bool("version", false, "")
	root.PersistentFlags().StringP("rsh-profile", "p", "default", "")
	root.PersistentFlags().StringP("mode", "M", "", "")
	root.PersistentFlags().String("rsh-timeout", "", "")
	root.AddCommand(
		&cobra.Command{Use: "dci"},
		&cobra.Command{Use: "help"},
		&cobra.Command{Use: "status"},
		&cobra.Command{Use: "login"},
		&cobra.Command{Use: "logout"},
	)

	cli.Root = root
	t.Cleanup(func() {
		cli.Root = oldRoot
	})
}

// --- Table rendering test helpers ---

// mockAlertRows returns rows resembling a DCI list-alerts response.
// Includes a "config" column with map values that should be auto-hidden.
func mockAlertRows() ([]map[string]interface{}, []string) {
	rows := []map[string]interface{}{
		{
			"createTime":  1.709550521e+12,
			"id":          "JkKD7J8jmgcL52Lgj4uy",
			"lastAlerted": 1.710936037e+12,
			"name":        "bookreviews staging test",
			"owner":       "alice@example.com",
			"updateTime":  1.709557415e+12,
			"config":      map[string]interface{}{"threshold": 100, "period": "monthly"},
		},
		{
			"createTime":  1.667139587394e+12,
			"id":          "Ns8B2zIs07qJjDVByCIz",
			"lastAlerted": 1.736672435e+12,
			"name":        "Cloud analytics reports cost by user",
			"owner":       "bob@example.com",
			"updateTime":  1.728637824610e+12,
			"config":      map[string]interface{}{"threshold": 50},
		},
	}
	allKeys := []string{"config", "createTime", "id", "lastAlerted", "name", "owner", "updateTime"}
	return rows, allKeys
}

// mockReportRows returns rows resembling a DCI list-reports response.
// Includes a "labels" column with array-of-map values that should be auto-hidden.
func mockReportRows() ([]map[string]interface{}, []string) {
	rows := []map[string]interface{}{
		{
			"createTime": 1.774010451448e+12,
			"id":         "ApLLbhKaGNVlXqNlFh1u",
			"labels":     []interface{}{},
			"owner":      "alice@example.com",
			"reportName": "Monthly cost breakdown",
			"type":       "custom",
			"updateTime": 1.77401059984e+12,
			"urlUI":      "https://console.example.com/customers/abc123/analytics/reports/ApLLbhKaGNVlXqNlFh1u",
		},
		{
			"createTime": 1.709000000e+12,
			"id":         "kyYAeFUM3hD8moWxyz12",
			"labels":     []interface{}{map[string]interface{}{"id": "il6vOdNiBDGw", "name": "team-alpha"}},
			"owner":      "bob@example.com",
			"reportName": "Account overview Q1",
			"type":       "custom",
			"updateTime": 1.709100000e+12,
			"urlUI":      "https://console.example.com/customers/abc123/analytics/reports/kyYAeFUM3hD8moWxyz12",
		},
	}
	allKeys := []string{"createTime", "id", "labels", "owner", "reportName", "type", "updateTime", "urlUI"}
	return rows, allKeys
}

// mockSimpleRows returns rows with no object columns (nothing to hide).
func mockSimpleRows() ([]map[string]interface{}, []string) {
	rows := []map[string]interface{}{
		{"name": "budget-alpha", "amount": 1000.0, "currency": "USD"},
		{"name": "budget-beta", "amount": 5000.0, "currency": "EUR"},
	}
	return rows, []string{"amount", "currency", "name"}
}

// --- formatValue tests ---

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name string
		val  interface{}
		want string
	}{
		{"string passthrough", "hello", "hello"},
		{"int passthrough", 42, "42"},
		{"small float", 3.14, "3.14"},
		{"unix ms timestamp", 1.709550521e+12, time.UnixMilli(1709550521000).UTC().Format(time.RFC3339)},
		{"unix ms timestamp 2", 1.774010451448e+12, time.UnixMilli(1774010451448).UTC().Format(time.RFC3339)},
		{"below timestamp range", 9.99e+11, "9.99e+11"},
		{"above timestamp range", 5e+12, "5e+12"},
		{"nil value", nil, "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatValue(tt.val)
			if got != tt.want {
				t.Errorf("formatValue(%v) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}

// --- containsObject tests ---

func TestContainsObject(t *testing.T) {
	tests := []struct {
		name string
		val  interface{}
		want bool
	}{
		{"nil", nil, false},
		{"string", "hello", false},
		{"float", 3.14, false},
		{"empty slice", []interface{}{}, false},
		{"slice of strings", []interface{}{"a", "b"}, false},
		{"direct map", map[string]interface{}{"k": "v"}, true},
		{"slice containing map", []interface{}{map[string]interface{}{"k": "v"}}, true},
		{"slice with mixed types including map", []interface{}{"a", map[string]interface{}{"k": "v"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsObject(tt.val)
			if got != tt.want {
				t.Errorf("containsObject(%v) = %v, want %v", tt.val, got, tt.want)
			}
		})
	}
}

// --- filterObjectColumns tests ---

func TestFilterObjectColumns(t *testing.T) {
	t.Run("alert rows hide config", func(t *testing.T) {
		rows, keys := mockAlertRows()
		visible, hidden := filterObjectColumns(rows, keys)
		if !reflect.DeepEqual(hidden, []string{"config"}) {
			t.Errorf("hidden = %v, want [config]", hidden)
		}
		for _, k := range visible {
			if k == "config" {
				t.Errorf("config should not be in visible columns")
			}
		}
	})

	t.Run("report rows hide labels", func(t *testing.T) {
		rows, keys := mockReportRows()
		visible, hidden := filterObjectColumns(rows, keys)
		if !reflect.DeepEqual(hidden, []string{"labels"}) {
			t.Errorf("hidden = %v, want [labels]", hidden)
		}
		for _, k := range visible {
			if k == "labels" {
				t.Errorf("labels should not be in visible columns")
			}
		}
	})

	t.Run("simple rows hide nothing", func(t *testing.T) {
		rows, keys := mockSimpleRows()
		visible, hidden := filterObjectColumns(rows, keys)
		if len(hidden) != 0 {
			t.Errorf("hidden = %v, want empty", hidden)
		}
		if !reflect.DeepEqual(visible, keys) {
			t.Errorf("visible = %v, want %v", visible, keys)
		}
	})
}

// --- measureContentWidths tests ---

func TestMeasureContentWidths(t *testing.T) {
	rows, keys := mockSimpleRows()
	widths := measureContentWidths(rows, keys)
	if len(widths) != len(keys) {
		t.Fatalf("widths length = %d, want %d", len(widths), len(keys))
	}
	// "amount" column: header=6, values "1000" (4) and "5000" (4) → max 6
	if widths[0] < 4 {
		t.Errorf("amount width = %d, want >= 4", widths[0])
	}
	// "currency" column: header=8, values "USD" (3) and "EUR" (3) → max 8
	if widths[1] < 3 {
		t.Errorf("currency width = %d, want >= 3", widths[1])
	}
	// "name" column: header=4, values "budget-alpha" (12) and "budget-beta" (11) → max 12
	if widths[2] != 12 {
		t.Errorf("name width = %d, want 12", widths[2])
	}
}

func TestMeasureContentWidthsFormatsTimestamps(t *testing.T) {
	rows, _ := mockAlertRows()
	keys := []string{"createTime"}
	widths := measureContentWidths(rows, keys)
	// ISO 8601 timestamp "2024-03-04T12:28:41Z" is 20 chars
	if widths[0] != 20 {
		t.Errorf("timestamp width = %d, want 20", widths[0])
	}
}

// --- computeColumnWidths tests ---

func TestComputeColumnWidthsSumMatchesAvailable(t *testing.T) {
	tests := []struct {
		name          string
		contentWidths []int
		termWidth     int
	}{
		{"alert-like 6 cols at 214", []int{20, 20, 20, 51, 24, 20}, 214},
		{"alert-like 6 cols at 120", []int{20, 20, 20, 51, 24, 20}, 120},
		{"report-like 7 cols at 214", []int{20, 20, 24, 30, 6, 20, 100}, 214},
		{"simple 3 cols at 80", []int{12, 8, 6}, 80},
		{"all fit easily", []int{5, 5, 5}, 200},
		{"all very wide", []int{200, 200, 200}, 120},
		{"single column", []int{50}, 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			widths := computeColumnWidths(tt.contentWidths, tt.termWidth, 0)
			cols := len(tt.contentWidths)
			overhead := tableOverhead(cols)
			available := tt.termWidth - overhead

			sum := 0
			for _, w := range widths {
				sum += w
				if w < 1 {
					t.Errorf("column width %d < 1", w)
				}
			}
			if sum != available {
				t.Errorf("sum of widths = %d, want %d (termWidth=%d overhead=%d)", sum, available, tt.termWidth, overhead)
			}
		})
	}
}

func TestComputeColumnWidthsNarrowColumnsGetContentWidth(t *testing.T) {
	// When all columns fit, narrow ones should get at least their content width.
	contentWidths := []int{5, 5, 5}
	widths := computeColumnWidths(contentWidths, 200, 0)
	for i, cw := range contentWidths {
		if widths[i] < cw {
			t.Errorf("col %d: width %d < content width %d", i, widths[i], cw)
		}
	}
}

func TestComputeColumnWidthsMaxColWidth(t *testing.T) {
	contentWidths := []int{5, 5, 200}
	widths := computeColumnWidths(contentWidths, 120, 30)
	for i, w := range widths {
		if w > 30 {
			t.Errorf("col %d: width %d exceeds maxColWidth 30", i, w)
		}
	}
}

func TestComputeColumnWidthsWideColumnGetsMore(t *testing.T) {
	// One wide column, several narrow — wide column should get the surplus.
	contentWidths := []int{10, 10, 200}
	widths := computeColumnWidths(contentWidths, 120, 0)
	// The wide column should be wider than the narrow ones.
	if widths[2] <= widths[0] {
		t.Errorf("wide column (%d) should be wider than narrow column (%d)", widths[2], widths[0])
	}
}

// --- buildTableString width tests ---

func TestBuildTableStringExactWidth(t *testing.T) {
	tests := []struct {
		name      string
		termWidth int
	}{
		{"width 80", 80},
		{"width 120", 120},
		{"width 214", 214},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, keys := mockSimpleRows()
			contentW := measureContentWidths(rows, keys)
			colWidths := computeColumnWidths(contentW, tt.termWidth, 0)
			out, err := buildTableString(rows, keys, colWidths, "fit")
			if err != nil {
				t.Fatalf("buildTableString error: %v", err)
			}
			w := tableDisplayWidth(out)
			if w != tt.termWidth {
				t.Errorf("display width = %d, want %d", w, tt.termWidth)
			}
		})
	}
}

func TestBuildTableStringAlertLikeExactWidth(t *testing.T) {
	rows, allKeys := mockAlertRows()
	visible, _ := filterObjectColumns(rows, allKeys)

	for _, termWidth := range []int{80, 120, 214} {
		t.Run(fmt.Sprintf("width_%d", termWidth), func(t *testing.T) {
			contentW := measureContentWidths(rows, visible)
			colWidths := computeColumnWidths(contentW, termWidth, 0)
			out, err := buildTableString(rows, visible, colWidths, "fit")
			if err != nil {
				t.Fatalf("buildTableString error: %v", err)
			}
			w := tableDisplayWidth(out)
			if w != termWidth {
				t.Errorf("display width = %d, want %d", w, termWidth)
			}
		})
	}
}

func TestBuildTableStringReportLikeExactWidth(t *testing.T) {
	rows, allKeys := mockReportRows()
	visible, _ := filterObjectColumns(rows, allKeys)

	for _, termWidth := range []int{80, 120, 214} {
		t.Run(fmt.Sprintf("width_%d", termWidth), func(t *testing.T) {
			contentW := measureContentWidths(rows, visible)
			colWidths := computeColumnWidths(contentW, termWidth, 0)
			out, err := buildTableString(rows, visible, colWidths, "fit")
			if err != nil {
				t.Fatalf("buildTableString error: %v", err)
			}
			w := tableDisplayWidth(out)
			if w != termWidth {
				t.Errorf("display width = %d, want %d", w, termWidth)
			}
		})
	}
}

func TestBuildTableStringWithHiddenColumnsIncluded(t *testing.T) {
	// Simulate user passing -C to include all columns (including object ones).
	rows, allKeys := mockAlertRows()

	for _, termWidth := range []int{120, 214} {
		t.Run(fmt.Sprintf("all_cols_width_%d", termWidth), func(t *testing.T) {
			contentW := measureContentWidths(rows, allKeys)
			colWidths := computeColumnWidths(contentW, termWidth, 0)
			out, err := buildTableString(rows, allKeys, colWidths, "fit")
			if err != nil {
				t.Fatalf("buildTableString error: %v", err)
			}
			w := tableDisplayWidth(out)
			if w != termWidth {
				t.Errorf("display width = %d, want %d", w, termWidth)
			}
		})
	}
}

func TestBuildTableStringNoU2800InOutput(t *testing.T) {
	// Verify that U+2800 padding placeholder is replaced with spaces.
	rows, keys := mockSimpleRows()
	contentW := measureContentWidths(rows, keys)
	colWidths := computeColumnWidths(contentW, 120, 0)
	out, err := buildTableString(rows, keys, colWidths, "fit")
	if err != nil {
		t.Fatalf("buildTableString error: %v", err)
	}
	if strings.Contains(out, "\u2800") {
		t.Errorf("output contains U+2800 placeholder; should be replaced with spaces")
	}
}

func TestBuildTableStringRightAlignment(t *testing.T) {
	// Body cells should be right-aligned (content pushed to the right with leading spaces).
	rows := []map[string]interface{}{
		{"col": "short"},
	}
	keys := []string{"col"}
	// Give the column much more space than needed.
	colWidths := []int{30}
	out, err := buildTableString(rows, keys, colWidths, "fit")
	if err != nil {
		t.Fatalf("buildTableString error: %v", err)
	}
	// Find the body line (not header, not border).
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "short") && strings.Contains(line, "║") {
			// Right-aligned means spaces before "short", not after.
			inner := strings.TrimPrefix(line, "║")
			inner = strings.TrimSuffix(inner, "║")
			inner = strings.TrimSpace(inner)
			if !strings.HasSuffix(strings.TrimSpace(inner), "short") {
				t.Errorf("expected right-aligned 'short', got line: %q", line)
			}
			return
		}
	}
	t.Errorf("could not find body line with 'short' in output:\n%s", out)
}

func TestTableOverheadFormula(t *testing.T) {
	for cols := 1; cols <= 10; cols++ {
		keys := make([]string, cols)
		rows := []map[string]interface{}{{}}
		for i := 0; i < cols; i++ {
			keys[i] = fmt.Sprintf("c%d", i)
			rows[0][keys[i]] = "a"
		}
		widths := make([]int, cols)
		for i := 0; i < cols; i++ {
			widths[i] = 1
		}
		s, _ := buildTableString(rows, keys, widths, "fit")
		actual := tableDisplayWidth(s) - cols
		formula := tableOverhead(cols)
		t.Logf("cols=%d actual=%d formula=%d diff=%d", cols, actual, formula, actual-formula)
		if actual != formula {
			t.Errorf("cols=%d: overhead mismatch actual=%d formula=%d", cols, actual, formula)
		}
	}
}

func TestTableMarshalFallsBackToJSON(t *testing.T) {
	ct := dciTableContentType{}

	tests := []struct {
		name  string
		input interface{}
	}{
		{"plain string", "How are you?"},
		{"number", 42},
		{"bool", true},
		{"array of strings", []interface{}{"hello", "world"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ct.Marshal(tc.input)
			if err != nil {
				t.Fatalf("expected JSON fallback, got error: %v", err)
			}
			if len(out) == 0 {
				t.Fatal("expected non-empty output")
			}
		})
	}
}

func TestTableMarshalObjectStillWorks(t *testing.T) {
	ct := dciTableContentType{}
	input := map[string]interface{}{"name": "test", "value": 123}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected table output, got error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty output")
	}
}

// expectedSkillFiles lists every file the embedded skill should produce.
var expectedSkillFiles = []string{
	"skills/dci-cli/SKILL.md",
	"skills/dci-cli/agents/openai.yaml",
	"skills/dci-cli/references/capabilities.md",
	"skills/dci-cli/references/cost-optimization.md",
	"skills/dci-cli/references/evals.md",
	"skills/dci-cli/references/examples.md",
	"skills/dci-cli/references/query-patterns.md",
}

func TestInstallSkill(t *testing.T) {
	agents := []struct {
		name string
		dir  string
	}{
		{"claude", ".claude"},
		{"codex", ".codex"},
		{"kiro", ".kiro"},
		{"gemini", ".gemini"},
		{"opencode", ".config/opencode"},
	}

	for _, a := range agents {
		t.Run(a.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			targetDir := filepath.Join(tmpDir, a.dir)

			if err := installSkill(targetDir); err != nil {
				t.Fatalf("installSkill(%s) failed: %v", a.name, err)
			}

			for _, relPath := range expectedSkillFiles {
				fullPath := filepath.Join(targetDir, relPath)
				info, err := os.Stat(fullPath)
				if err != nil {
					t.Errorf("expected file %s to exist: %v", relPath, err)
					continue
				}
				if info.Size() == 0 {
					t.Errorf("expected file %s to be non-empty", relPath)
				}
			}
		})
	}
}

func TestInstallSkillContentMatchesEmbed(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, ".claude")

	if err := installSkill(targetDir); err != nil {
		t.Fatalf("installSkill failed: %v", err)
	}

	for _, relPath := range expectedSkillFiles {
		embedPath := relPath // embedded paths use forward slashes
		embedded, err := skillFS.ReadFile(embedPath)
		if err != nil {
			t.Fatalf("failed to read embedded %s: %v", embedPath, err)
		}

		installed, err := os.ReadFile(filepath.Join(targetDir, relPath))
		if err != nil {
			t.Fatalf("failed to read installed %s: %v", relPath, err)
		}

		if string(embedded) != string(installed) {
			t.Errorf("content mismatch for %s", relPath)
		}
	}
}

func TestInstallSkillIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, ".claude")

	if err := installSkill(targetDir); err != nil {
		t.Fatalf("first install failed: %v", err)
	}
	if err := installSkill(targetDir); err != nil {
		t.Fatalf("second install failed: %v", err)
	}

	for _, relPath := range expectedSkillFiles {
		if _, err := os.Stat(filepath.Join(targetDir, relPath)); err != nil {
			t.Errorf("expected file %s after second install: %v", relPath, err)
		}
	}
}

func TestInstallSkillCreatesDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "deep", "nested", "path")

	if err := installSkill(targetDir); err != nil {
		t.Fatalf("installSkill into nested path failed: %v", err)
	}

	expectedDirs := []string{
		"skills/dci-cli",
		"skills/dci-cli/agents",
		"skills/dci-cli/references",
	}
	for _, dir := range expectedDirs {
		info, err := os.Stat(filepath.Join(targetDir, dir))
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", dir)
		}
	}
}

func TestInstallSkillFileCount(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, ".claude")

	if err := installSkill(targetDir); err != nil {
		t.Fatalf("installSkill failed: %v", err)
	}

	var installedFiles []string
	err := filepath.WalkDir(filepath.Join(targetDir, "skills", "dci-cli"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(targetDir, path)
			installedFiles = append(installedFiles, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking installed dir: %v", err)
	}

	if len(installedFiles) != len(expectedSkillFiles) {
		t.Errorf("expected %d files, got %d: %v", len(expectedSkillFiles), len(installedFiles), installedFiles)
	}
}

const testTokenCacheKey = "dci:default.token"

// makeTestJWT builds a minimal unsigned JWT (header.payload.) with the given claims.
// claims is marshalled as-is so callers can pass any JSON-serialisable map.
func makeTestJWT(claims map[string]interface{}) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(claims)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

// doerJWT returns a test JWT with DoitEmployee: true.
func doerJWT() string {
	return makeTestJWT(map[string]interface{}{"DoitEmployee": true, "sub": "jane@doit-intl.com"})
}

// nonDoerJWT returns a test JWT with DoitEmployee: false.
func nonDoerJWT() string {
	return makeTestJWT(map[string]interface{}{"DoitEmployee": false, "sub": "user@example.com"})
}

// writeContextFile writes ctx to the customer context file in dir, fataling on error.
func writeContextFile(t *testing.T, dir, ctx string) {
	t.Helper()
	if err := os.WriteFile(customerContextPath(dir), []byte(ctx+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setupTestCache replaces cli.Cache with a fresh in-memory viper instance and
// restores the original on test cleanup.
func setupTestCache(t *testing.T) {
	t.Helper()
	old := cli.Cache
	cli.Cache = viper.New()
	t.Cleanup(func() { cli.Cache = old })
}

func TestCachedTokenIsDoer(t *testing.T) {
	setupTestCache(t)

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "DoitEmployee true", token: doerJWT(), want: true},
		{name: "DoitEmployee false", token: nonDoerJWT(), want: false},
		{name: "JWT without DoitEmployee claim", token: makeTestJWT(map[string]interface{}{"sub": "user@example.com"}), want: false},
		{name: "empty token", token: "", want: false},
		{name: "not a JWT", token: "not-a-jwt", want: false},
		{name: "invalid base64 in payload", token: "header.!!invalid!!.sig", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli.Cache.Set(testTokenCacheKey, tt.token)
			if got := cachedTokenIsDoer(); got != tt.want {
				t.Errorf("cachedTokenIsDoer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyDoerContext(t *testing.T) {
	setupTestCache(t)

	tests := []struct {
		name            string
		token           string
		existingContext string
		wantResult      bool
		wantContext     string
	}{
		{
			name:        "sets doit.com for Doer with no context",
			token:       doerJWT(),
			wantResult:  true,
			wantContext: "doit.com",
		},
		{
			name:        "no-op for non-Doer account",
			token:       nonDoerJWT(),
			wantResult:  false,
			wantContext: "",
		},
		{
			name:            "no-op when context already set",
			token:           doerJWT(),
			existingContext: "other-customer",
			wantResult:      false,
			wantContext:     "other-customer",
		},
		{
			name:        "no-op when no cached token",
			token:       "",
			wantResult:  false,
			wantContext: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.existingContext != "" {
				writeContextFile(t, dir, tt.existingContext)
			}
			cli.Cache.Set(testTokenCacheKey, tt.token)

			if got := applyDoerContext(dir); got != tt.wantResult {
				t.Errorf("applyDoerContext() = %v, want %v", got, tt.wantResult)
			}
			if ctx := readCustomerContext(dir); ctx != tt.wantContext {
				t.Errorf("customerContext = %q, want %q", ctx, tt.wantContext)
			}
		})
	}
}

func TestCustomerContextFlag(t *testing.T) {
	bin := buildBinary(t)

	t.Run("empty --customer-context errors", func(t *testing.T) {
		home := t.TempDir()
		res := runCLIWithEnv(t, bin, home, []string{"DCI_API_KEY=test-key"}, "list-budgets", "--customer-context", "")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if res.exitCode == 0 {
			t.Fatalf("expected non-zero exit; output:\n%s", res.output)
		}
		if !strings.Contains(res.output, "--customer-context requires a non-empty domain name") {
			t.Fatalf("expected error message in output:\n%s", res.output)
		}
	})

	t.Run("-D short form appears in help", func(t *testing.T) {
		home := t.TempDir()
		res := runCLIWithEnv(t, bin, home, []string{"DCI_API_KEY=test-key"}, "list-budgets", "--help")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if !strings.Contains(res.output, "-D, --customer-context") {
			t.Fatalf("expected -D/--customer-context flag in help output:\n%s", res.output)
		}
	})

	t.Run("Doer hint suppressed when customerContextFlagValue set", func(t *testing.T) {
		setupTestCache(t)
		cli.Cache.Set(testTokenCacheKey, doerJWT())

		// Simulate --customer-context flag having been set for this invocation.
		customerContextFlagValue = "acme.com"
		t.Cleanup(func() { customerContextFlagValue = "" })

		dir := t.TempDir()
		// No persistent context file — conditions that would normally trigger the hint.

		// Capture stderr.
		r, w, _ := os.Pipe()
		oldStderr := os.Stderr
		os.Stderr = w

		// Call with exitCode=1 and status=403 — would print the hint for a Doer
		// with no persistent context, unless customerContextFlagValue suppresses it.
		maybeHintDoerContext(1, 403, dir)

		w.Close()
		os.Stderr = oldStderr
		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		output := string(buf[:n])
		r.Close()

		if strings.Contains(output, "DoiT employees need a customer context") {
			t.Fatalf("expected hint to be suppressed, but got:\n%s", output)
		}
	})
}
