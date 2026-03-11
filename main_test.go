package main

import (
	"context"
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
		&cobra.Command{Use: "completion"},
		&cobra.Command{Use: "generic-cmd", GroupID: "generic"},
		&cobra.Command{Use: "other-api", GroupID: "api"},
	)

	lockToDCI()

	if !cli.Root.CompletionOptions.DisableDefaultCmd {
		t.Fatalf("expected default completion command to be disabled")
	}

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
	if strings.Contains(out, "\n  completion       ") {
		t.Fatalf("unexpected completion command in root help:\n%s", out)
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
	)

	cli.Root = root
	t.Cleanup(func() {
		cli.Root = oldRoot
	})
}
