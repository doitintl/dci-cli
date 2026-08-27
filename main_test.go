package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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
			// Bare dci must reach the root RunE, which routes on the TUI gate
			// (AI session for humans, help everywhere else) — a --help rewrite
			// here would bypass that routing (AI-DEFAULT-SPEC §3).
			name: "no args passes through to the root RunE",
			in:   []string{"dci"},
			out:  []string{"dci"},
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
		{
			// dci st<Tab>: the partial word is the one being completed, so it
			// must not be treated as a committed API command — root-level
			// completion is where root commands (status) and API operations
			// are merged into one candidate list.
			name: "__complete with partial first word stays at root",
			in:   []string{"dci", "__complete", "st"},
			out:  []string{"dci", "__complete", "st"},
		},
		{
			name: "__complete with partial api command stays at root",
			in:   []string{"dci", "__complete", "list-bud"},
			out:  []string{"dci", "__complete", "list-bud"},
		},
		{
			name: "__completeNoDesc with partial first word stays at root",
			in:   []string{"dci", "__completeNoDesc", "st"},
			out:  []string{"dci", "__completeNoDesc", "st"},
		},
		{
			name: "__complete with global flag then partial first word stays at root",
			in:   []string{"dci", "__complete", "--rsh-timeout", "5s", "st"},
			out:  []string{"dci", "__complete", "--rsh-timeout", "5s", "st"},
		},
		{
			name: "__complete with global flag then api command gets dci prefix",
			in:   []string{"dci", "__complete", "--rsh-timeout", "5s", "list-budgets", ""},
			out:  []string{"dci", "__complete", "dci", "--rsh-timeout", "5s", "list-budgets", ""},
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

func TestRegisterVersionCommand(t *testing.T) {
	setupTestRoot(t)
	cli.Root.Use = "custom-dci"
	registerVersionCommand()

	command, _, err := cli.Root.Find([]string{"version"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Name() != "version" {
		t.Fatalf("command = %q, want version", command.Name())
	}
	if command.Short != "Print the DCI CLI version" {
		t.Fatalf("short description = %q", command.Short)
	}
	var output strings.Builder
	command.SetOut(&output)
	command.Run(command, nil)
	want := fmt.Sprintf("custom-dci version %s\n", version)
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
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

	lockToDCI(t.TempDir())

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

func TestLockToDCIDisablesGenericRootRequests(t *testing.T) {
	oldRoot := cli.Root
	requestExecuted := false
	root := &cobra.Command{
		Use:  "dci",
		Args: cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			requestExecuted = true
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return []string{"example.com"}, cobra.ShellCompDirectiveDefault
		},
	}
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	cli.Root = root
	t.Cleanup(func() {
		cli.Root = oldRoot
	})

	lockToDCI(t.TempDir())

	if root.Run != nil {
		t.Fatal("generic root request handler remains installed")
	}
	if root.RunE == nil {
		t.Fatal("safe root handler was not installed")
	}
	if root.ValidArgsFunction != nil {
		t.Fatal("generic hostname completion remains installed")
	}
	if err := root.Args(root, []string{""}); err == nil {
		t.Fatal("empty hostname argument was accepted")
	}
	if err := root.RunE(root, nil); err != nil {
		t.Fatal(err)
	}
	if requestExecuted {
		t.Fatal("generic root request handler executed")
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

	if cli.Root.Short != "Cloud Intelligence™ CLI" {
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

	if dciCmd.Short != "Cloud Intelligence™ API CLI" {
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
	previousConfiguredAPIBase := configuredAPIBase
	configuredAPIBase = ""
	t.Cleanup(func() { configuredAPIBase = previousConfiguredAPIBase })
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
	previousConfiguredAPIBase := configuredAPIBase
	configuredAPIBase = ""
	t.Cleanup(func() { configuredAPIBase = previousConfiguredAPIBase })
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

func TestEnsureConfigLoadsSavedAPIBase(t *testing.T) {
	previousConfiguredAPIBase := configuredAPIBase
	configuredAPIBase = ""
	t.Cleanup(func() { configuredAPIBase = previousConfiguredAPIBase })

	dir := t.TempDir()
	if _, err := ensureConfig(dir); err != nil {
		t.Fatalf("ensureConfig(create) error: %v", err)
	}
	configPath := filepath.Join(dir, "apis.json")
	if err := writeConfig(configPath, "https://api-dev.doit.com"); err != nil {
		t.Fatalf("writeConfig() error: %v", err)
	}
	configuredAPIBase = ""
	if _, err := ensureConfig(dir); err != nil {
		t.Fatalf("ensureConfig(existing) error: %v", err)
	}
	base, err := apiBase()
	if err != nil {
		t.Fatalf("apiBase() error: %v", err)
	}
	if base != "https://api-dev.doit.com" {
		t.Fatalf("apiBase() = %q, want saved dev base", base)
	}
}

func TestEnsureConfigNeverPersistsEnvBase(t *testing.T) {
	previousConfiguredAPIBase := configuredAPIBase
	configuredAPIBase = ""
	t.Cleanup(func() { configuredAPIBase = previousConfiguredAPIBase })
	dir := t.TempDir()

	// First run under the env override: the stored config still gets the
	// production default — the override is per-invocation, not sticky.
	t.Setenv("DCI_API_BASE_URL", "https://dev-app.doit.com")
	configured, err := ensureConfig(dir)
	if err != nil {
		t.Fatalf("ensureConfig(create) error: %v", err)
	}
	if !configured {
		t.Fatalf("expected configured=true on first run")
	}
	configPath := filepath.Join(dir, "apis.json")
	assertConfigBase(t, configPath, defaultAPIBase)

	// The override still wins at runtime for this invocation.
	base, err := apiBase()
	if err != nil {
		t.Fatalf("apiBase() error: %v", err)
	}
	if base != "https://dev-app.doit.com" {
		t.Fatalf("apiBase() = %q, want the env override", base)
	}

	// A later run with the env set must leave the stored base untouched —
	// one dev-targeted invocation must not strand every later run on dev.
	configured, err = ensureConfig(dir)
	if err != nil {
		t.Fatalf("ensureConfig(existing) error: %v", err)
	}
	if configured {
		t.Fatalf("expected configured=false on second run")
	}
	assertConfigBase(t, configPath, defaultAPIBase)

	// Without the env var, runtime resolution follows the stored base again.
	t.Setenv("DCI_API_BASE_URL", "")
	if _, err := ensureConfig(dir); err != nil {
		t.Fatalf("ensureConfig(no-op) error: %v", err)
	}
	base, err = apiBase()
	if err != nil {
		t.Fatalf("apiBase() error: %v", err)
	}
	if base != defaultAPIBase {
		t.Fatalf("apiBase() = %q, want the stored default", base)
	}
}

func TestIsDetachedRefreshInvocation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"update refresh child", []string{"dci", "__refresh-update-check"}, true},
		{"name refresh child", []string{"dci", "__refresh-names"}, true},
		{"normal data command", []string{"dci", "list-budgets"}, false},
		{"status command", []string{"dci", "status"}, false},
		{"no args", []string{"dci"}, false},
		{"empty args", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDetachedRefreshInvocation(tt.args); got != tt.want {
				t.Errorf("isDetachedRefreshInvocation(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// TestWriteOverriddenAPIsConfigPreservesEverythingButBase verifies
// writeOverriddenAPIsConfig only patches "dci.base" — auth, TLS, and any
// other field already in the real apis.json are copied through untouched
// into the isolated temp-dir copy.
func TestWriteOverriddenAPIsConfigPreservesEverythingButBase(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "apis.json")
	original := `{
		"dci": {
			"base": "https://api.doit.com",
			"profiles": {"default": {"auth": {"name": "oauth-authorization-code"}}},
			"tls": {"insecure": true}
		}
	}`
	if err := os.WriteFile(src, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "override.json")
	if err := writeOverriddenAPIsConfig(src, dst, "https://dev.example.com"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	dci := doc["dci"].(map[string]interface{})
	if dci["base"] != "https://dev.example.com" {
		t.Errorf("base = %v, want the override", dci["base"])
	}
	tls := dci["tls"].(map[string]interface{})
	if tls["insecure"] != true {
		t.Errorf("tls.insecure = %v, want true (must survive the patch)", tls["insecure"])
	}
	profiles := dci["profiles"].(map[string]interface{})
	profile := profiles["default"].(map[string]interface{})
	auth := profile["auth"].(map[string]interface{})
	if auth["name"] != "oauth-authorization-code" {
		t.Errorf("auth.name = %v, want it preserved", auth["name"])
	}

	// The real file must be untouched.
	realData, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(realData) != original {
		t.Fatalf("the real apis.json changed:\n%s", realData)
	}
}

// TestApplyAPIBaseOverrideIsolatesConfigAndCacheDirs is the core unit test
// for the redesigned fix: instead of mutating the real apis.json (and
// needing a lock, stale-lock reclamation, and cache-invalidation machinery
// to make that safe across processes), DCI_API_BASE_URL now gets its own
// private temp config+cache dir that no other invocation ever sees — so
// there is nothing left to race.
// TestDetachedRefreshEnvRestoresRealDirsDuringOverride guards a finding
// from the adversarial review of this redesign: __refresh-update-check and
// __refresh-names are spawned via exec.Command, which inherits os.Environ()
// at Start() time. If applyAPIBaseOverride's active override has already
// pointed DCI_CONFIG_DIR/DCI_CACHE_DIR at its throwaway temp dir when one
// of these children is spawned (completionPreflight's name-refresh spawn
// happens well after applyAPIBaseOverride runs), the child would otherwise
// do real network I/O and write into a directory the parent deletes on
// exit — silently breaking the persisted name/update cache it exists to
// refresh. detachedRefreshEnv must give the child the real directories.
func TestDetachedRefreshEnvRestoresRealDirsDuringOverride(t *testing.T) {
	t.Cleanup(func() { realDCIDirOverrides = nil })

	t.Run("no active override: passes environ through unchanged", func(t *testing.T) {
		realDCIDirOverrides = nil
		t.Setenv("DCI_CONFIG_DIR", "/real/config")
		t.Setenv("DCI_CACHE_DIR", "/real/cache")

		env := detachedRefreshEnv()
		if !slices.Contains(env, "DCI_CONFIG_DIR=/real/config") {
			t.Errorf("env missing DCI_CONFIG_DIR=/real/config: %v", env)
		}
		if !slices.Contains(env, "DCI_CACHE_DIR=/real/cache") {
			t.Errorf("env missing DCI_CACHE_DIR=/real/cache: %v", env)
		}
	})

	t.Run("active override: restores the real dirs, not the temp ones", func(t *testing.T) {
		t.Setenv("DCI_CONFIG_DIR", "/temp/override")
		t.Setenv("DCI_CACHE_DIR", "/temp/override")
		realDCIDirOverrides = &realDCIDirOverridesState{
			configDir: dciDirOverride{value: "/real/config", had: true},
			cacheDir:  dciDirOverride{value: "/real/cache", had: true},
		}

		env := detachedRefreshEnv()
		if slices.Contains(env, "DCI_CONFIG_DIR=/temp/override") {
			t.Errorf("env leaked the temp override dir: %v", env)
		}
		if !slices.Contains(env, "DCI_CONFIG_DIR=/real/config") {
			t.Errorf("env missing the restored real DCI_CONFIG_DIR: %v", env)
		}
		if !slices.Contains(env, "DCI_CACHE_DIR=/real/cache") {
			t.Errorf("env missing the restored real DCI_CACHE_DIR: %v", env)
		}
	})

	t.Run("active override, real dirs were unset: child gets them unset too", func(t *testing.T) {
		t.Setenv("DCI_CONFIG_DIR", "/temp/override")
		t.Setenv("DCI_CACHE_DIR", "/temp/override")
		realDCIDirOverrides = &realDCIDirOverridesState{
			configDir: dciDirOverride{had: false},
			cacheDir:  dciDirOverride{had: false},
		}

		env := detachedRefreshEnv()
		for _, e := range env {
			if strings.HasPrefix(e, "DCI_CONFIG_DIR=") || strings.HasPrefix(e, "DCI_CACHE_DIR=") {
				t.Errorf("env should not set DCI_CONFIG_DIR/DCI_CACHE_DIR when the real invocation never had them, got: %s", e)
			}
		}
	})
}

func TestApplyAPIBaseOverrideIsolatesConfigAndCacheDirs(t *testing.T) {
	realDir := t.TempDir()
	realConfig := filepath.Join(realDir, "apis.json")
	if err := os.WriteFile(realConfig, []byte(`{"dci":{"base":"https://api.doit.com","tls":{"insecure":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	realCacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realCacheDir, "cache.json"), []byte(`{"dci:default.token":"real-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// dci.cbor must never be copied into the isolated dir — a stale spec
	// from a different host must never be served under the override.
	if err := os.WriteFile(filepath.Join(realCacheDir, "dci.cbor"), []byte("stale-spec-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	// customer_context, unlike dci.cbor, must be copied in: it's user/tenant
	// selection data, not host-specific, and `dci ai` tool-call subprocesses
	// (which resolve their own dciConfigDir() to this same temp dir) need it
	// to keep the user's selected customer for the whole override session.
	if err := os.WriteFile(customerContextPath(realDir), []byte("acme.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DCI_CACHE_DIR", realCacheDir)
	t.Setenv("DCI_API_BASE_URL", "https://dev.example.com")

	cleanup := applyAPIBaseOverride(realDir, []string{"dci", "list-budgets"})
	t.Cleanup(cleanup)

	tempConfigDir := os.Getenv("DCI_CONFIG_DIR")
	tempCacheDir := os.Getenv("DCI_CACHE_DIR")
	if tempConfigDir == "" || tempConfigDir == realDir {
		t.Fatalf("DCI_CONFIG_DIR = %q, want a fresh isolated dir", tempConfigDir)
	}
	if tempCacheDir == "" || tempCacheDir == realCacheDir {
		t.Fatalf("DCI_CACHE_DIR = %q, want a fresh isolated dir", tempCacheDir)
	}
	if tempConfigDir != tempCacheDir {
		t.Fatalf("config dir %q and cache dir %q should be the same temp dir", tempConfigDir, tempCacheDir)
	}

	data, err := os.ReadFile(filepath.Join(tempConfigDir, "apis.json"))
	if err != nil {
		t.Fatalf("isolated apis.json missing: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	dci := doc["dci"].(map[string]interface{})
	if dci["base"] != "https://dev.example.com" {
		t.Errorf("isolated base = %v, want the override", dci["base"])
	}

	cacheData, err := os.ReadFile(filepath.Join(tempCacheDir, "cache.json"))
	if err != nil {
		t.Fatalf("isolated cache.json missing: %v", err)
	}
	if !strings.Contains(string(cacheData), "real-token") {
		t.Errorf("isolated cache.json = %s, want the real OAuth session carried over", cacheData)
	}

	if _, err := os.Stat(filepath.Join(tempCacheDir, "dci.cbor")); !os.IsNotExist(err) {
		t.Fatalf("dci.cbor should not exist in the isolated cache dir yet, err=%v", err)
	}

	ctxData, err := os.ReadFile(customerContextPath(tempConfigDir))
	if err != nil {
		t.Fatalf("isolated customer_context missing: %v", err)
	}
	if strings.TrimSpace(string(ctxData)) != "acme.com" {
		t.Errorf("isolated customer_context = %q, want the real customer context carried over", ctxData)
	}

	realData, err := os.ReadFile(realConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(realData), "https://api.doit.com") || strings.Contains(string(realData), "dev.example.com") {
		t.Fatalf("the real apis.json was mutated: %s", realData)
	}
	if _, err := os.Stat(filepath.Join(realCacheDir, "dci.cbor")); err != nil {
		t.Fatalf("the real cache dir's dci.cbor should be untouched, err=%v", err)
	}

	cleanup()
	if _, err := os.Stat(tempConfigDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %q should be removed after cleanup, err=%v", tempConfigDir, err)
	}
	if got := os.Getenv("DCI_CONFIG_DIR"); got != "" {
		t.Errorf("DCI_CONFIG_DIR after cleanup = %q, want unset", got)
	}
	if got := os.Getenv("DCI_CACHE_DIR"); got != realCacheDir {
		t.Errorf("DCI_CACHE_DIR after cleanup = %q, want restored to %q", got, realCacheDir)
	}
}

// TestApplyAPIBaseOverrideCleanupPersistsRefreshedSession guards a
// Claude-review finding: restish writes a refreshed/new OAuth token to
// cache.json under DCI_CACHE_DIR — the temp dir, for the whole override
// invocation — on an access-token refresh or a `dci login` run under the
// override. Without copying it back to the real cache dir before the temp
// dir is deleted, that session is silently discarded: the next invocation,
// override or not, finds the same stale real cache.json and has to
// re-authenticate despite the login/refresh having appeared to succeed.
func TestApplyAPIBaseOverrideCleanupPersistsRefreshedSession(t *testing.T) {
	realDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realDir, "apis.json"), []byte(`{"dci":{"base":"https://api.doit.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	realCacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realCacheDir, "cache.json"), []byte(`{"dci:default":{"token":"stale-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DCI_CACHE_DIR", realCacheDir)
	t.Setenv("DCI_API_BASE_URL", "https://dev.example.com")

	cleanup := applyAPIBaseOverride(realDir, []string{"dci", "list-budgets"})
	tempCacheDir := os.Getenv("DCI_CACHE_DIR")

	// Simulate restish writing a refreshed token into the temp dir's
	// cache.json during the invocation (e.g. an access-token refresh, or
	// `dci login` run under the override).
	if err := os.WriteFile(filepath.Join(tempCacheDir, "cache.json"), []byte(`{"dci:default":{"token":"refreshed-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanup()

	data, err := os.ReadFile(filepath.Join(realCacheDir, "cache.json"))
	if err != nil {
		t.Fatalf("real cache.json missing after cleanup: %v", err)
	}
	if !strings.Contains(string(data), "refreshed-token") {
		t.Fatalf("real cache.json = %s, want the refreshed token persisted back", data)
	}
}

// TestDCIConfigDirMemoizesAcrossOverride guards a finding from the
// adversarial review of this redesign: name_completion.go and
// tui_picker.go call dciConfigDir() fresh, after applyAPIBaseOverride has
// already pointed DCI_CONFIG_DIR at its throwaway per-invocation temp dir.
// Without memoizing the first (real) resolution, every completion/picker
// call during an active override would see an empty temp dir instead of
// the real, persisted name cache and customer context — a silent feature
// regression for exactly the workflow (a dev session against a staging
// host) the override exists to serve.
func TestDCIConfigDirMemoizesAcrossOverride(t *testing.T) {
	resetDCIConfigDirCache()
	t.Cleanup(resetDCIConfigDirCache)

	realDir := t.TempDir()
	t.Setenv("DCI_CONFIG_DIR", realDir)

	first := dciConfigDir()
	if first != realDir {
		t.Fatalf("dciConfigDir() = %q, want the real dir %q", first, realDir)
	}

	// Simulate what applyAPIBaseOverride does: point DCI_CONFIG_DIR at a
	// different, throwaway directory mid-invocation.
	tempDir := t.TempDir()
	t.Setenv("DCI_CONFIG_DIR", tempDir)

	if got := dciConfigDir(); got != realDir {
		t.Fatalf("dciConfigDir() after the env var changed = %q, want the memoized real dir %q (this is what protects completion/the picker from seeing the isolated override dir)", got, realDir)
	}
}

// TestApplyAPIBaseOverrideNoopsWithoutOverride guards the common case: with
// no DCI_API_BASE_URL set, applyAPIBaseOverride must not touch the env or
// create anything.
func TestApplyAPIBaseOverrideNoopsWithoutOverride(t *testing.T) {
	t.Setenv("DCI_API_BASE_URL", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "apis.json"), []byte(`{"dci":{"base":"https://api.doit.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	oldConfigDir := os.Getenv("DCI_CONFIG_DIR")
	cleanup := applyAPIBaseOverride(dir, []string{"dci", "list-budgets"})
	defer cleanup()

	if got := os.Getenv("DCI_CONFIG_DIR"); got != oldConfigDir {
		t.Errorf("DCI_CONFIG_DIR = %q, want unchanged %q", got, oldConfigDir)
	}
}

// TestApplyAPIBaseOverrideSkipsDetachedRefresh guards the parent/child
// refresher race this whole mechanism used to need a lock to avoid: the
// detached update-check/name-completion children inherit DCI_API_BASE_URL
// but never talk to the DCI API, so they must be a pure no-op here.
func TestApplyAPIBaseOverrideSkipsDetachedRefresh(t *testing.T) {
	t.Setenv("DCI_API_BASE_URL", "https://dev.example.com")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "apis.json"), []byte(`{"dci":{"base":"https://api.doit.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	oldConfigDir := os.Getenv("DCI_CONFIG_DIR")
	cleanup := applyAPIBaseOverride(dir, []string{"dci", "__refresh-update-check"})
	defer cleanup()

	if got := os.Getenv("DCI_CONFIG_DIR"); got != oldConfigDir {
		t.Errorf("DCI_CONFIG_DIR = %q, want unchanged %q for a detached refresh invocation", got, oldConfigDir)
	}
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

func assertConfigOAuthProfile(t *testing.T, configPath string) {
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
		t.Fatal("config missing dci key")
	}
	profiles, ok := dci["profiles"].(map[string]interface{})
	if !ok {
		t.Fatal("config missing profiles")
	}
	profile, ok := profiles["default"].(map[string]interface{})
	if !ok {
		t.Fatal("config missing default profile")
	}
	auth, ok := profile["auth"].(map[string]interface{})
	if !ok || auth["name"] != "oauth-authorization-code" {
		t.Fatalf("default auth = %#v", auth)
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

func TestResolveAgentMode(t *testing.T) {
	tests := []struct {
		name      string
		envMode   string
		args      []string
		agentEnv  string
		ciEnv     bool
		stdoutTTY bool
		want      bool
		wantMode  uaMode
	}{
		// 1. DCI_AGENT_MODE always wins.
		{name: "env mode 1 wins over tty", envMode: "1", args: nil, agentEnv: "", stdoutTTY: true, want: true, wantMode: uaModeAgent},
		{name: "env mode true", envMode: "true", stdoutTTY: true, want: true, wantMode: uaModeAgent},
		{name: "env mode 0 forces human", envMode: "0", agentEnv: "CLAUDECODE", stdoutTTY: false, want: false, wantMode: uaModeInteractive},
		{name: "env mode false forces human", envMode: "false", stdoutTTY: false, want: false, wantMode: uaModeInteractive},
		{name: "env mode wins over --agent", envMode: "0", args: []string{"--agent"}, stdoutTTY: false, want: false, wantMode: uaModeInteractive},
		{name: "env mode garbage ignored, falls through to tty", envMode: "2", stdoutTTY: true, want: false, wantMode: uaModeInteractive},
		{name: "env mode garbage ignored, falls through to env var", envMode: "banana", agentEnv: "KIRO_AGENT", stdoutTTY: true, want: true, wantMode: uaModeAgent},

		// 2. Flags override heuristics.
		{name: "--agent forces agent", args: []string{"dci", "--agent", "status"}, stdoutTTY: true, want: true, wantMode: uaModeAgent},
		{name: "--no-agent forces human", args: []string{"dci", "--no-agent", "list"}, agentEnv: "CLAUDECODE", stdoutTTY: false, want: false, wantMode: uaModeInteractive},
		{name: "--no-agent over tty", args: []string{"--no-agent"}, stdoutTTY: false, want: false, wantMode: uaModeInteractive},

		// 3. Agent env var heuristic.
		{name: "agent env enables", agentEnv: "CURSOR_AGENT", stdoutTTY: true, want: true, wantMode: uaModeAgent},

		// 4. CI heuristic: CI systems can allocate a PTY, which would open the
		// bare-dci AI session and hang the job (AI-DEFAULT-SPEC §7).
		{name: "CI env enables even with a tty", ciEnv: true, stdoutTTY: true, want: true, wantMode: uaModeNonInteractive},
		{name: "env mode 0 wins over CI", envMode: "0", ciEnv: true, stdoutTTY: true, want: false, wantMode: uaModeInteractive},

		// 5. Non-TTY soft signal — agent behavior, but classified noninteractive
		// (covers CI/CD and piped/redirected use).
		{name: "non-tty enables", stdoutTTY: false, want: true, wantMode: uaModeNonInteractive},
		{name: "interactive tty stays human", stdoutTTY: true, want: false, wantMode: uaModeInteractive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAgentMode(tt.envMode, tt.args, tt.agentEnv, tt.ciEnv, tt.stdoutTTY)
			if got.enabled != tt.want {
				t.Fatalf("resolveAgentMode() enabled = %v (reason %q), want %v", got.enabled, got.reason, tt.want)
			}
			if got.mode != tt.wantMode {
				t.Fatalf("resolveAgentMode() mode = %q (reason %q), want %q", got.mode, got.reason, tt.wantMode)
			}
		})
	}
}

func TestHeatmapEnabled(t *testing.T) {
	tests := []struct {
		name                           string
		requested, agent, tty, noColor bool
		want                           bool
	}{
		{name: "interactive", requested: true, tty: true, want: true},
		{name: "disabled", requested: false, tty: true},
		{name: "agent", requested: true, agent: true, tty: true},
		{name: "pipe", requested: true},
		{name: "no color", requested: true, tty: true, noColor: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := heatmapEnabled(test.requested, test.agent, test.tty, test.noColor); got != test.want {
				t.Errorf("heatmapEnabled() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAgentFlagOverride(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "none", args: []string{"dci", "status"}, want: 0},
		{name: "agent", args: []string{"dci", "--agent", "status"}, want: 1},
		{name: "agent equals true", args: []string{"--agent=true"}, want: 1},
		{name: "agent equals 1", args: []string{"--agent=1"}, want: 1},
		{name: "no-agent", args: []string{"--no-agent"}, want: -1},
		{name: "no-agent equals 1", args: []string{"--no-agent=1"}, want: -1},
		{name: "agent uppercase TRUE", args: []string{"--agent=TRUE"}, want: 1},

		// Explicit false leaves the flag unset (matching two independent pflag
		// bool flags), so it does NOT force the opposite mode — it clears to 0.
		{name: "agent equals false clears", args: []string{"--agent=false"}, want: 0},
		{name: "agent equals 0 clears", args: []string{"--agent=0"}, want: 0},
		{name: "no-agent equals false clears", args: []string{"--no-agent=false"}, want: 0},
		{name: "no-agent equals 0 clears", args: []string{"--no-agent=0"}, want: 0},
		{name: "agent then agent false clears", args: []string{"--agent", "--agent=false"}, want: 0},
		{name: "no-agent then no-agent false clears", args: []string{"--no-agent", "--no-agent=false"}, want: 0},

		// pflag uses strconv.ParseBool, which rejects yes/on/etc. (parseBoolish
		// accepts them, but that's only for the DCI_AGENT_MODE env var).
		{name: "agent yes rejected (not pflag bool)", args: []string{"--agent=yes"}, want: 0},
		{name: "agent unrecognized value ignored", args: []string{"--agent=maybe"}, want: 0},

		// Conflicting flags both true: most recently enabled wins.
		{name: "conflict no-agent last wins", args: []string{"--agent", "--no-agent"}, want: -1},
		{name: "conflict agent last wins", args: []string{"--no-agent", "--agent"}, want: 1},
		{name: "stop at terminator", args: []string{"--", "--agent"}, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentFlagOverride(tt.args); got != tt.want {
				t.Fatalf("agentFlagOverride(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

func TestBuildUserAgent(t *testing.T) {
	// The mode token is always present and self-describing so analytics can
	// segment by interface across all three classifications.
	cases := []struct {
		mode uaMode
		want string
	}{
		{uaModeInteractive, "mode=interactive"},
		{uaModeAgent, "mode=agent"},
		{uaModeNonInteractive, "mode=noninteractive"},
	}
	for _, c := range cases {
		ua := buildUserAgent(c.mode)
		if !strings.HasPrefix(ua, "dci-cli/") {
			t.Fatalf("unexpected User-Agent prefix: %q", ua)
		}
		if !strings.Contains(ua, c.want) {
			t.Fatalf("buildUserAgent(%q) = %q, want it to contain %q", c.mode, ua, c.want)
		}
		// Guard against the legacy opaque tag regressing.
		if strings.Contains(ua, "agent=1") {
			t.Fatalf("User-Agent must use mode=, not the legacy agent=1 tag: %q", ua)
		}
	}

	// An unset mode falls back to interactive rather than emitting "mode=".
	if ua := buildUserAgent(""); !strings.Contains(ua, "mode=interactive") {
		t.Fatalf("empty mode should default to interactive: %q", ua)
	}
}

func TestDefaultOutputFormat(t *testing.T) {
	t.Cleanup(func() { agentMode = false })

	agentMode = false
	if got := defaultOutputFormat(); got != "table" {
		t.Fatalf("human defaultOutputFormat() = %q, want table", got)
	}
	agentMode = true
	if got := defaultOutputFormat(); got != "toon" {
		t.Fatalf("agent defaultOutputFormat() = %q, want toon", got)
	}
}

func TestDetectedAgentEnv(t *testing.T) {
	for _, name := range agentEnvVars {
		t.Setenv(name, "")
	}
	if got := detectedAgentEnv(); got != "" {
		t.Fatalf("detectedAgentEnv() with no agent vars = %q, want empty", got)
	}

	t.Setenv("KIRO_AGENT", "1")
	if got := detectedAgentEnv(); got != "KIRO_AGENT" {
		t.Fatalf("detectedAgentEnv() = %q, want KIRO_AGENT", got)
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
		if !strings.Contains(res.output, "Cloud Intelligence™") {
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

	t.Run("env API base is per-invocation, never persisted", func(t *testing.T) {
		home := t.TempDir()
		first := runCLIWithEnv(t, bin, home, []string{"DCI_API_BASE_URL=https://api-dev.doit.com"}, "status")
		if first.exitCode != 0 {
			t.Fatalf("initial status exit code = %d; output:\n%s", first.exitCode, first.output)
		}
		if !strings.Contains(first.output, "API Base: https://api-dev.doit.com (DCI_API_BASE_URL)") {
			t.Fatalf("env override missing from status:\n%s", first.output)
		}
		// The next env-less run is back on the stored default: one
		// dev-targeted invocation must not strand later runs on dev.
		second := runCLIWithHome(t, bin, home, "status")
		if second.exitCode != 0 {
			t.Fatalf("saved-base status exit code = %d; output:\n%s", second.exitCode, second.output)
		}
		if !strings.Contains(second.output, "API Base: "+defaultAPIBase) {
			t.Fatalf("default API base missing from status:\n%s", second.output)
		}
		if strings.Contains(second.output, "api-dev.doit.com") {
			t.Fatalf("env base leaked into the stored config:\n%s", second.output)
		}
	})

	t.Run("malformed DCI_API_BASE_URL degrades gracefully instead of aborting", func(t *testing.T) {
		// Guards a Claude-review finding on PR #128: apiBase() rejecting a
		// malformed override (missing https://, here) must not hard-abort
		// commands that never depended on the override, the same way a
		// swapConfiguredAPIBase write failure already degrades gracefully.
		// No warning is expected here (unlike the write-failure case) since
		// `status` calls apiBase() itself and reports this exact error on
		// its own terms — printing it a second time from run() would be
		// redundant noise for the one command that actually cares.
		home := t.TempDir()
		res := runCLIWithEnv(t, bin, home, []string{"DCI_API_BASE_URL=http://not-https.example.com"}, "--version")
		if res.timedOut {
			t.Fatalf("command timed out; output:\n%s", res.output)
		}
		if res.exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; output:\n%s", res.exitCode, res.output)
		}
	})

	t.Run("status repairs unusable API config", func(t *testing.T) {
		cases := map[string]string{
			"missing base": `{"$schema":"x","dci":{"profiles":{}}}`,
			"malformed":    `{`,
			"invalid base": `{"dci":{"base":"http://api-dev.doit.com","profiles":{}}}`,
		}
		for name, contents := range cases {
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				first := runCLIWithHome(t, bin, home, "status")
				if first.exitCode != 0 {
					t.Fatalf("initial status exit code = %d; output:\n%s", first.exitCode, first.output)
				}
				configDir := extractConfigDirFromStatus(first.output)
				configPath := filepath.Join(configDir, "apis.json")
				if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
					t.Fatalf("write unusable config: %v", err)
				}

				second := runCLIWithHome(t, bin, home, "status")
				if second.exitCode != 0 {
					t.Fatalf("recovery status exit code = %d; output:\n%s", second.exitCode, second.output)
				}
				if !strings.Contains(second.output, "warning: unable to use API base from ") {
					t.Fatalf("recovery warning missing:\n%s", second.output)
				}
				if strings.Contains(second.output, "DCI_API_BASE_URL must") {
					t.Fatalf("warning identifies the wrong source:\n%s", second.output)
				}
				if !strings.Contains(second.output, "API Base: "+defaultAPIBase) {
					t.Fatalf("production fallback missing:\n%s", second.output)
				}
				assertConfigBase(t, configPath, defaultAPIBase)
				assertConfigOAuthProfile(t, configPath)

				third := runCLIWithHome(t, bin, home, "status")
				if third.exitCode != 0 || strings.Contains(third.output, "warning: unable to use API base") {
					t.Fatalf("repaired config was not reusable:\n%s", third.output)
				}
			})
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

	t.Run("agent profile rejection is structured", func(t *testing.T) {
		res := runCLIWithEnv(t, bin, t.TempDir(), []string{"DCI_AGENT_MODE=1"}, "--profile", "other", "status")
		assertStructuredCLIError(t, res, exitUsage, "USAGE_ERROR")
	})

	t.Run("agent config rejection is structured", func(t *testing.T) {
		res := runCLIWithEnv(t, bin, t.TempDir(), []string{"DCI_AGENT_MODE=1", "DCI_API_BASE_URL=http://example.com"}, "status")
		assertStructuredCLIError(t, res, exitGenericFailure, "CLI_ERROR")
	})

	t.Run("agent shorthand flag rejection is usage error", func(t *testing.T) {
		res := runCLIWithEnv(t, bin, t.TempDir(), []string{"DCI_AGENT_MODE=1"}, "-z")
		assertStructuredCLIError(t, res, exitUsage, "USAGE_ERROR")
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

// seedHermeticAPIConfig writes an apis.json under home that points the CLI at
// base and trusts its self-signed certificate, and returns the environment a
// hermetic binary invocation needs: the per-invocation API base, a per-test
// spec cache and config dir, and no update check.
func seedHermeticAPIConfig(t *testing.T, home, base string) []string {
	t.Helper()
	configDir := filepath.Join(home, "xdg", "dci")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]interface{}{
		"dci": map[string]interface{}{
			"base":     base,
			"profiles": map[string]interface{}{"default": map[string]interface{}{}},
			"tls":      map[string]interface{}{"insecure": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "apis.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{
		"DCI_API_BASE_URL=" + base,
		"DCI_CACHE_DIR=" + filepath.Join(home, "cache"),
		"DCI_CONFIG_DIR=" + configDir,
		"DCI_NO_UPDATE_CHECK=1",
	}
}

// hermeticSpecEnv serves the given OpenAPI document from a local TLS server
// and returns the environment that points the built binary at it. Generated
// commands (even their --help) hydrate the command tree by fetching the spec,
// so without this an invocation reaches the live API and can exceed the
// runCLI timeout under parallel test load.
func hermeticSpecEnv(t *testing.T, home, spec string) []string {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/openapi.json" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, spec)
	}))
	t.Cleanup(server.Close)
	return seedHermeticAPIConfig(t, home, server.URL)
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

func assertStructuredCLIError(t *testing.T, result cliResult, expectedExit int, expectedCode string) {
	t.Helper()
	if result.exitCode != expectedExit {
		t.Fatalf("exit code = %d, want %d; output:\n%s", result.exitCode, expectedExit, result.output)
	}
	var envelope structuredErrorEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.output)), &envelope); err != nil {
		t.Fatalf("output is not one JSON envelope: %v\n%s", err, result.output)
	}
	if envelope.Error.Code != expectedCode {
		t.Fatalf("error code = %q, want %q", envelope.Error.Code, expectedCode)
	}
}

func assertRootHelpBranded(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "A generic client for REST-ish APIs") {
		t.Fatalf("unexpected stock restish root help:\n%s", out)
	}
	if !strings.Contains(out, "Command-line interface for the Cloud Intelligence™ API.") {
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
		{"nil value", nil, ""},
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

	t.Run("long strings remain visible", func(t *testing.T) {
		longDescription := strings.Repeat("a", 3000)
		rows := []map[string]interface{}{{"id": "item-1", "description": longDescription}}
		keys := []string{"id", "description"}
		visible, hidden := filterObjectColumns(rows, keys)
		if len(hidden) != 0 {
			t.Fatalf("hidden = %v, want empty", hidden)
		}
		if !reflect.DeepEqual(visible, keys) {
			t.Fatalf("visible = %v, want %v", visible, keys)
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
	// Human-readable timestamp "2024-03-04 12:28" is 16 chars
	if widths[0] != 16 {
		t.Errorf("timestamp width = %d, want 16", widths[0])
	}
}

// --- computeColumnWidths tests ---

func TestComputeColumnWidthsNeverExceedAvailable(t *testing.T) {
	// Columns fit their content and never stretch beyond it to fill wide
	// terminals; the sum must stay within the available width.
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
			for i, w := range widths {
				sum += w
				if w < 1 {
					t.Errorf("column width %d < 1", w)
				}
				if w > tt.contentWidths[i] {
					t.Errorf("col %d: width %d stretched beyond content width %d", i, w, tt.contentWidths[i])
				}
			}
			if sum > available {
				t.Errorf("sum of widths = %d exceeds available %d (termWidth=%d overhead=%d)", sum, available, tt.termWidth, overhead)
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

// assertTableWidthFits checks the no-stretch contract: the rendered table
// never exceeds the terminal width, and when the content is wider than the
// terminal the table uses the full width.
func assertTableWidthFits(t *testing.T, out string, termWidth int, contentWidths []int) {
	t.Helper()
	w := tableDisplayWidth(out)
	if w > termWidth {
		t.Errorf("display width = %d exceeds terminal width %d", w, termWidth)
	}
	natural := tableOverhead(len(contentWidths))
	for _, cw := range contentWidths {
		natural += cw
	}
	if natural >= termWidth && w != termWidth {
		t.Errorf("display width = %d, want full terminal width %d (content is wider)", w, termWidth)
	}
}

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
			assertTableWidthFits(t, out, tt.termWidth, contentW)
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
			assertTableWidthFits(t, out, termWidth, contentW)
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
			assertTableWidthFits(t, out, termWidth, contentW)
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
			assertTableWidthFits(t, out, termWidth, contentW)
		})
	}
}

func TestTerseHelpText(t *testing.T) {
	long := "Runs a report query.\n## Input Example\n```json\n{}\n```\n## Request Schema\n..."
	terse, truncated := terseHelpText(long)
	if !truncated {
		t.Fatal("schema-bearing help not truncated")
	}
	if strings.Contains(terse, "## ") || !strings.Contains(terse, "--help-full") {
		t.Errorf("terse = %q, want schemas stripped and --help-full pointer", terse)
	}
	if _, truncated := terseHelpText("plain description"); truncated {
		t.Error("plain help truncated")
	}
}

func timeDimFixtureBody(withTimestamp bool) map[string]interface{} {
	schema := []interface{}{
		map[string]interface{}{"name": "service_description", "type": "string"},
		map[string]interface{}{"name": "year", "type": "string"},
		map[string]interface{}{"name": "month", "type": "string"},
		map[string]interface{}{"name": "cost", "type": "float"},
	}
	row := []interface{}{"svc", "2026", "07", 12.5}
	if withTimestamp {
		schema = append(schema, map[string]interface{}{"name": "timestamp", "type": "timestamp"})
		row = append(row, float64(1782864000))
	}
	return map[string]interface{}{
		"result": map[string]interface{}{
			"rows":   []interface{}{row},
			"schema": schema,
		},
	}
}

func TestRedundantTimeDimensionColumnsSuppressedInFlatRows(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	rows, handled, err := extractGetReportRows(timeDimFixtureBody(true), labelRFC3339)
	if !handled || err != nil {
		t.Fatalf("handled = %v, err = %v", handled, err)
	}
	row := rows[0]
	if _, hasYear := row["year"]; hasYear {
		t.Error("year kept despite timestamp column carrying the period")
	}
	if _, hasMonth := row["month"]; hasMonth {
		t.Error("month kept despite timestamp column carrying the period")
	}
	if _, hasTimestamp := row["timestamp"]; !hasTimestamp {
		t.Error("timestamp column missing — it is the machine-sortable period form")
	}
	if row["service_description"] != "svc" || row["cost"] != 12.5 {
		t.Errorf("non-time columns disturbed: %v", row)
	}
}

func TestTimeDimensionColumnsKeptWithoutTimestamp(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	rows, _, err := extractGetReportRows(timeDimFixtureBody(false), labelRFC3339)
	if err != nil {
		t.Fatal(err)
	}
	if _, hasYear := rows[0]["year"]; !hasYear {
		t.Error("year dropped although no timestamp column exists")
	}
}

func TestTimeDimensionColumnsKeptUnderExplicitSelection(t *testing.T) {
	viper.Set("table-columns", "month,cost")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	rows, _, err := extractGetReportRows(timeDimFixtureBody(true), labelRFC3339)
	if err != nil {
		t.Fatal(err)
	}
	if _, hasMonth := rows[0]["month"]; !hasMonth {
		t.Error("explicitly selected month column dropped")
	}
}

func TestAugmentVerifiedFlagHelp(t *testing.T) {
	newListDimensions := func() *cobra.Command {
		cmd := &cobra.Command{Use: "list-dimensions"}
		cmd.Flags().String("filter", "", "An expression for filtering the results.")
		return cmd
	}

	cmd := newListDimensions()
	augmentVerifiedFlagHelp(cmd)
	usage := cmd.Flags().Lookup("filter").Usage
	if !strings.Contains(usage, "field:value") || !strings.Contains(usage, "silently ignored") {
		t.Errorf("filter usage = %q, want verified syntax note appended", usage)
	}

	// Idempotent: help can render more than once per process.
	augmentVerifiedFlagHelp(cmd)
	if strings.Count(cmd.Flags().Lookup("filter").Usage, "field:value") != 1 {
		t.Error("augmentation applied twice")
	}

	// Other commands with a filter flag are untouched: the note documents
	// behavior verified per endpoint, not a global contract.
	other := &cobra.Command{Use: "list-reports"}
	other.Flags().String("filter", "", "An expression for filtering the results.")
	augmentVerifiedFlagHelp(other)
	if strings.Contains(other.Flags().Lookup("filter").Usage, "field:value") {
		t.Error("augmentation leaked to an unverified command")
	}
}

func TestRewriteHelpFullFlag(t *testing.T) {
	oldRequested := helpFullRequested
	helpFullRequested = false
	t.Cleanup(func() { helpFullRequested = oldRequested })

	args := rewriteHelpFullFlag([]string{"dci", "query", "--help-full"})
	if !helpFullRequested {
		t.Error("--help-full not detected")
	}
	if args[2] != "--help" {
		t.Errorf("args = %v, want --help substituted", args)
	}
}

func TestFormatValueNilIsEmptyCell(t *testing.T) {
	if got := formatValue(nil); got != "" {
		t.Errorf("formatValue(nil) = %q, want empty", got)
	}
}

func TestDisplayTimestampCellRawNumbersPreservesEpoch(t *testing.T) {
	viper.Set("raw-numbers", false)
	t.Cleanup(func() { viper.Set("raw-numbers", nil) })

	const epoch = float64(1786356000)
	if got := displayTimestampCell(epoch, "timestamp", labelRFC3339); got != "2026-08-10T10:00:00Z" {
		t.Errorf("displayTimestampCell = %v, want RFC3339 timestamp", got)
	}
	if got := displayTimestampCell(epoch, "timestamp", labelDisplay); got != "2026-08-10 10:00" {
		t.Errorf("display-style displayTimestampCell = %v, want terminal label", got)
	}
	viper.Set("raw-numbers", true)
	if got := displayTimestampCell(epoch, "timestamp", labelRFC3339); got != epoch {
		t.Errorf("raw displayTimestampCell = %v, want %v", got, epoch)
	}
	if got := displayTimestampCell(epoch, "timestamp", labelDisplay); got != epoch {
		t.Errorf("raw display-style displayTimestampCell = %v, want %v", got, epoch)
	}
}

func TestFormatTableValueRendering(t *testing.T) {
	viper.Set("raw-numbers", false)
	t.Cleanup(func() { viper.Set("raw-numbers", nil) })

	if got := formatTableValue(1786356000000.0); got != "2026-08-10 10:00" {
		t.Errorf("epoch-ms float = %q, want human-readable timestamp", got)
	}
	if got := formatTableValue("2026-06-01T00:00:00Z"); got != "2026-06-01" {
		t.Errorf("midnight timestamp = %q, want bare date", got)
	}
	if got := formatTableValue("2026-08-10T09:16:14Z"); got != "2026-08-10 09:16" {
		t.Errorf("timestamp = %q, want minute precision", got)
	}
	if got := formatTableValue("not a timestamp"); got != "not a timestamp" {
		t.Errorf("plain string = %q, want untouched", got)
	}

	// Hourly report resolution keeps the time even at midnight.
	viper.Set("report-hourly", true)
	t.Cleanup(func() { viper.Set("report-hourly", nil) })
	if got := formatTableValue("2026-06-01T00:00:00Z"); got != "2026-06-01 00:00" {
		t.Errorf("hourly midnight = %q, want time kept", got)
	}
	if got := formatTableValue(55.0); got != "55" {
		t.Errorf("integral float = %q, want 55 (no decimals)", got)
	}
	if got := formatTableValue(291018.6548470196); got != "291,018.65" {
		t.Errorf("decimal float = %q, want grouped 2dp", got)
	}
	viper.Set("raw-numbers", true)
	if got := formatTableValue(291018.6548470196); got != "291018.6548470196" {
		t.Errorf("--raw-numbers float = %q, want unformatted", got)
	}
}

func TestFitColumnsToTerminalKeepsPriorityAndHidesOverflow(t *testing.T) {
	rows := []map[string]interface{}{{
		"alpha":  strings.Repeat("a", 28),
		"beta":   strings.Repeat("b", 28),
		"gamma":  strings.Repeat("c", 28),
		"delta":  strings.Repeat("d", 28),
		"id":     "b0f3c260-3df3-4270-b946-df31ddee6a92",
		"status": "active",
		"trend":  "+25%",
	}}
	keys := []string{"alpha", "beta", "gamma", "delta", "id", "status", "trend"}
	visible, hidden := fitColumnsToTerminal(rows, keys, 80)
	if len(hidden) == 0 {
		t.Fatal("expected overflow columns to be hidden at width 80")
	}
	foundID := false
	for _, k := range visible {
		if k == "id" {
			foundID = true
		}
	}
	if !foundID {
		t.Errorf("id column not kept: visible=%v hidden=%v", visible, hidden)
	}
	foundTrend := false
	for _, key := range visible {
		if key == "trend" {
			foundTrend = true
		}
	}
	if !foundTrend {
		t.Errorf("trend column not kept: visible=%v hidden=%v", visible, hidden)
	}
	if len(visible)+len(hidden) != len(keys) {
		t.Errorf("columns lost: visible=%v hidden=%v", visible, hidden)
	}

	// Everything fits on a wide terminal.
	visible, hidden = fitColumnsToTerminal(rows, keys, 500)
	if len(hidden) != 0 {
		t.Errorf("nothing should hide at width 500: hidden=%v", hidden)
	}
	if len(visible) != len(keys) {
		t.Errorf("visible=%v, want all keys", visible)
	}
}

func TestFormatMoney(t *testing.T) {
	if got := formatMoney(239927.13841529994, "USD"); got != "$239,927" {
		t.Errorf("USD = %q, want $239,927", got)
	}
	if got := formatMoney(1400, "EUR"); got != "€1,400" {
		t.Errorf("EUR = %q, want €1,400", got)
	}
	if got := formatMoney(1234.56, "SEK"); got != "SEK 1,235" {
		t.Errorf("unknown code = %q, want SEK 1,235", got)
	}
	if got := formatMoney(-500.4, "USD"); got != "-$500" {
		t.Errorf("negative = %q, want -$500", got)
	}
}

func TestRenderCellTextCurrency(t *testing.T) {
	viper.Set("raw-numbers", false)
	viper.Set("report-currency", "USD")
	viper.Set("money-columns", "cost,total")
	t.Cleanup(func() {
		for _, key := range []string{"raw-numbers", "report-currency", "money-columns"} {
			viper.Set(key, nil)
		}
	})

	row := map[string]interface{}{"cost": 135616.704056, "usage": 42.5}
	if got := renderCellText(row, "cost"); got != "$135,617" {
		t.Errorf("marked money column = %q, want $135,617", got)
	}
	if got := renderCellText(row, "usage"); got != "42.50" {
		t.Errorf("non-money metric = %q, want plain number", got)
	}

	// Per-row currency (budgets shape) formats money-named columns.
	budget := map[string]interface{}{"amount": 4900.0, "currency": "EUR", "currentUtilization": 0.0}
	viper.Set("money-columns", "")
	viper.Set("report-currency", "")
	if got := renderCellText(budget, "amount"); got != "€4,900" {
		t.Errorf("row-currency amount = %q, want €4,900", got)
	}

	// --raw-numbers disables everything.
	viper.Set("raw-numbers", true)
	viper.Set("money-columns", "cost")
	viper.Set("report-currency", "USD")
	if got := renderCellText(row, "cost"); got != "135616.704056" {
		t.Errorf("raw mode = %q, want unformatted", got)
	}
}

func TestHeatmapColorizesPivotPeriodCells(t *testing.T) {
	viper.Set("heatmap", true)
	viper.Set("pivot-active", true)
	viper.Set("table-columns-auto", false)
	viper.Set("pivot-total-rows", 1)
	t.Cleanup(func() {
		viper.Set("heatmap", nil)
		viper.Set("pivot-active", nil)
		viper.Set("table-columns-auto", nil)
		viper.Set("pivot-total-rows", nil)
	})

	rows := []map[string]interface{}{
		{"service_description": "svc-a", "2026-06": 100.0, "2026-07": 900.0, "total": 1000.0, "trend": "+800%"},
		{"service_description": "TOTAL", "2026-06": 100.0, "2026-07": 900.0, "total": 1000.0, "trend": "+800%"},
	}
	keys := []string{"service_description", "2026-06", "2026-07", "total", "trend"}
	heat := newHeatmap(rows, keys)
	if heat == nil {
		t.Fatal("heatmap not built for pivot rows")
	}
	hot := heat.colorize(0, "2026-07", 900.0, "$900")
	if !strings.Contains(hot, "\x1b[48;5;124") || !strings.Contains(hot, "$900") {
		t.Errorf("hot cell = %q, want ANSI-shaded", hot)
	}
	cool := heat.colorize(0, "2026-06", 100.0, "$100")
	if !strings.Contains(cool, "\x1b[48;5;28") || !strings.Contains(cool, "$100") {
		t.Errorf("cool cell = %q, want lower ANSI shade", cool)
	}
	if got := heat.colorize(1, "2026-07", 900.0, "$900"); got != "$900" {
		t.Errorf("totals row shaded: %q", got)
	}
	if got := heat.colorize(0, "total", 1000.0, "$1,000"); got != "$1,000" {
		t.Errorf("total column shaded: %q", got)
	}
	if got := heat.colorize(0, "trend", nil, "+800%"); got != "+800%" {
		t.Errorf("trend column shaded: %q", got)
	}

	viper.Set("heatmap", false)
	if newHeatmap(rows, keys) != nil {
		t.Error("heatmap built while disabled")
	}
	viper.Set("heatmap", true)
	viper.Set("pivot-active", false)
	if newHeatmap(rows, keys) != nil {
		t.Error("heatmap built outside pivot view")
	}
}

func TestHeatmapUsesAbsoluteMagnitudes(t *testing.T) {
	viper.Set("heatmap", true)
	viper.Set("pivot-active", true)
	viper.Set("pivot-total-rows", 1)
	t.Cleanup(func() {
		viper.Set("heatmap", nil)
		viper.Set("pivot-active", nil)
		viper.Set("pivot-total-rows", nil)
	})

	for _, rows := range [][]map[string]interface{}{
		{
			{"group": "a", "2026-06": -100.0},
			{"group": "b", "2026-06": -900.0},
			{"group": "TOTAL", "2026-06": -1000.0},
		},
		{
			{"group": "a", "2026-06": -900.0},
			{"group": "b", "2026-06": 100.0},
			{"group": "TOTAL", "2026-06": -800.0},
		},
	} {
		heat := newHeatmap(rows, []string{"group", "2026-06"})
		if heat == nil || heat.max != 900 {
			t.Fatalf("heatmap max = %v, want 900", heat)
		}
		if shaded := heat.colorize(0, "2026-06", rows[0]["2026-06"], "-900"); !strings.Contains(shaded, "\x1b[") {
			t.Errorf("negative cell not shaded: %q", shaded)
		}
	}
}

func TestHeatmapShadesDataRowsNamedTotal(t *testing.T) {
	viper.Set("heatmap", true)
	viper.Set("pivot-active", true)
	viper.Set("pivot-total-rows", 1)
	t.Cleanup(func() {
		viper.Set("heatmap", nil)
		viper.Set("pivot-active", nil)
		viper.Set("pivot-total-rows", nil)
	})

	rows := []map[string]interface{}{
		{"group": "TOTAL", "2026-06": 900.0},
		{"group": "other", "2026-06": 100.0},
		{"group": "TOTAL", "2026-06": 1000.0},
	}
	heat := newHeatmap(rows, []string{"group", "2026-06"})
	if heat == nil || heat.max != 900 {
		t.Fatalf("heatmap max = %v, want 900", heat)
	}
	if shaded := heat.colorize(0, "2026-06", 900.0, "900"); !strings.Contains(shaded, "\x1b[") {
		t.Errorf("data row named TOTAL not shaded: %q", shaded)
	}
	if shaded := heat.colorize(2, "2026-06", 1000.0, "1,000"); shaded != "1,000" {
		t.Errorf("generated totals row shaded: %q", shaded)
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

func TestToonMarshalListProducesTabularOutput(t *testing.T) {
	ct := dciToonContentType{}
	input := map[string]interface{}{
		"reports": []interface{}{
			map[string]interface{}{"id": "r1", "name": "alpha"},
			map[string]interface{}{"id": "r2", "name": "beta"},
		},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := strings.TrimSpace(string(out))
	if len(s) == 0 {
		t.Fatal("expected non-empty output")
	}
	// The field/value substrings below also appear in JSON, so guard that we
	// actually emitted TOON and didn't silently hit the indented-JSON fallback
	// (which would start with `{`).
	if strings.HasPrefix(s, "{") {
		t.Fatalf("expected TOON output, got JSON fallback:\n%s", s)
	}
	if !strings.Contains(s, "id") || !strings.Contains(s, "name") {
		t.Fatalf("expected field names in TOON output, got:\n%s", s)
	}
	if !strings.Contains(s, "alpha") || !strings.Contains(s, "beta") {
		t.Fatalf("expected row values in TOON output, got:\n%s", s)
	}
}

func TestToonMarshalFoldsRowsWithEmptyContainerFields(t *testing.T) {
	// Real list responses carry info-free containers on every row (labels: [],
	// or empty objects). Per the TOON spec those disqualify tabular folding, so
	// they must be normalized away: empty objects pruned, empty arrays rendered
	// as blank cells (matching the table, which joins arrays into cell strings).
	ct := dciToonContentType{}
	input := map[string]interface{}{
		"pageToken": "tok123",
		"reports": []interface{}{
			map[string]interface{}{"id": "r1", "owner": "a@example.com", "labels": []interface{}{}, "meta": map[string]interface{}{}},
			map[string]interface{}{"id": "r2", "owner": "b@example.com", "labels": []interface{}{}, "meta": map[string]interface{}{}},
		},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "reports[2]{") {
		t.Fatalf("expected tabular fold for uniform rows, got:\n%s", s)
	}
	if strings.Contains(s, "meta") {
		t.Fatalf("expected empty objects pruned, got:\n%s", s)
	}
	if !strings.Contains(s, "pageToken: tok123") {
		t.Fatalf("expected wrapper fields (pagination) preserved, got:\n%s", s)
	}
}

func TestToonMarshalJoinsPrimitiveArraysLikeTable(t *testing.T) {
	// Arrays of primitives become ", "-joined cell strings (same as the table
	// renderer), so rows mixing empty and non-empty arrays still fold tabular.
	ct := dciToonContentType{}
	input := map[string]interface{}{
		"reports": []interface{}{
			map[string]interface{}{"id": "r1", "labels": []interface{}{"prod", "eu"}},
			map[string]interface{}{"id": "r2", "labels": []interface{}{}},
		},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "reports[2]{") {
		t.Fatalf("expected tabular fold with joined array cells, got:\n%s", s)
	}
	if !strings.Contains(s, "prod, eu") {
		t.Fatalf("expected table-style joined array value, got:\n%s", s)
	}
}

func TestToonMarshalGetReportRowsFoldTabular(t *testing.T) {
	// get-report returns result.rows as arrays of arrays; the table path maps
	// them to schema-named objects via extractGetReportRows. TOON must see the
	// same rows so both formats present the same information.
	ct := dciToonContentType{}
	input := map[string]interface{}{
		"result": map[string]interface{}{
			"schema": []interface{}{
				map[string]interface{}{"name": "service", "type": "string"},
				map[string]interface{}{"name": "cost", "type": "float"},
			},
			"rows": []interface{}{
				[]interface{}{"BigQuery", 12.5},
				[]interface{}{"GCS", 3.25},
			},
		},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "rows[2]{") {
		t.Fatalf("expected report rows folded tabular with schema columns, got:\n%s", s)
	}
	if !strings.Contains(s, "BigQuery") || !strings.Contains(s, "GCS") {
		t.Fatalf("expected report row values, got:\n%s", s)
	}
}

func liftTestReportInput(rowCount int) map[string]interface{} {
	rows := make([]interface{}, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		rows = append(rows, []interface{}{fmt.Sprintf("service-%d", i), float64(i) + 0.5, "2026-07-01T00:00:00Z"})
	}
	return map[string]interface{}{
		"result": map[string]interface{}{
			"schema": []interface{}{
				map[string]interface{}{"name": "service", "type": "string"},
				map[string]interface{}{"name": "cost", "type": "float"},
				map[string]interface{}{"name": "timestamp", "type": "timestamp"},
			},
			"rows": rows,
		},
	}
}

func TestToonMarshalLiftsConstantReportColumns(t *testing.T) {
	// A single-period grouped query repeats the same timestamp in every row —
	// hundreds of copies of one value is pure token cost for the consuming
	// agent. Constant string columns lift into result.constantColumns.
	ct := dciToonContentType{}
	out, err := ct.Marshal(liftTestReportInput(12))
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "constantColumns") {
		t.Fatalf("expected constantColumns marker, got:\n%s", s)
	}
	if got := strings.Count(s, "2026-07-01T00:00:00Z"); got != 1 {
		t.Fatalf("expected the constant timestamp exactly once, got %d occurrences:\n%s", got, s)
	}
	if !strings.Contains(s, "rows[12]{cost,service}") {
		t.Fatalf("expected rows folded without the lifted column, got:\n%s", s)
	}
}

func TestToonMarshalKeepsConstantColumnsInSmallResults(t *testing.T) {
	// Below the lift threshold there is nothing worth saving; rows keep the
	// column so small results stay maximally explicit.
	ct := dciToonContentType{}
	out, err := ct.Marshal(liftTestReportInput(3))
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "constantColumns") {
		t.Fatalf("expected no lift below %d rows, got:\n%s", liftConstantMinRows, s)
	}
	if got := strings.Count(s, "2026-07-01T00:00:00Z"); got != 3 {
		t.Fatalf("expected the timestamp in every row, got %d occurrences:\n%s", got, s)
	}
}

func TestToonMarshalColumnSelectionDisablesConstantLift(t *testing.T) {
	// -C is an explicit field request: requested columns are never dropped,
	// so lifting is skipped entirely.
	viper.Set("table-columns", "service,timestamp")
	t.Cleanup(viper.Reset)

	ct := dciToonContentType{}
	out, err := ct.Marshal(liftTestReportInput(12))
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "constantColumns") {
		t.Fatalf("expected no lift under -C selection, got:\n%s", s)
	}
	if got := strings.Count(s, "2026-07-01T00:00:00Z"); got != 12 {
		t.Fatalf("expected the selected timestamp in every row, got %d occurrences:\n%s", got, s)
	}
}

func TestToonMarshalDropsObjectColumnsLikeTable(t *testing.T) {
	// A row field holding objects (directly or inside an array) blocks the
	// tabular fold for the whole list, so drop the column from every row —
	// the same rule the table uses to hide object-valued columns.
	ct := dciToonContentType{}
	input := map[string]interface{}{
		"reports": []interface{}{
			map[string]interface{}{"id": "r1", "labels": []interface{}{map[string]interface{}{"name": "Cached"}}},
			map[string]interface{}{"id": "r2", "labels": []interface{}{}},
		},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "reports[2]{") {
		t.Fatalf("expected tabular fold with object column dropped, got:\n%s", s)
	}
	if strings.Contains(s, "labels") || strings.Contains(s, "Cached") {
		t.Fatalf("expected object-valued column dropped from all rows, got:\n%s", s)
	}
}

func TestToonMarshalFillsMissingRowKeysLikeTable(t *testing.T) {
	// Rows with differing key sets (e.g. budgets mixing scope/scopes) can't
	// fold per the TOON spec; the table renders the union of columns with
	// blank cells, so missing keys become "" here too.
	ct := dciToonContentType{}
	input := map[string]interface{}{
		"budgets": []interface{}{
			map[string]interface{}{"id": "b1", "scope": []interface{}{"projA"}},
			map[string]interface{}{"id": "b2", "scopes": []interface{}{"projB"}},
		},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "budgets[2]{") {
		t.Fatalf("expected tabular fold with union of row keys, got:\n%s", s)
	}
	if !strings.Contains(s, "projA") || !strings.Contains(s, "projB") {
		t.Fatalf("expected values from both key variants, got:\n%s", s)
	}
}

func TestToonMarshalColumnSelectionOverridesObjectDrop(t *testing.T) {
	// -C is the explicit opt-in: selected columns are never dropped, and
	// object-valued cells encode as compact JSON strings so the fold survives.
	// Unselected columns are excluded, matching the table's -C contract.
	viper.Set("table-columns", "id,labels")
	t.Cleanup(viper.Reset)

	ct := dciToonContentType{}
	input := map[string]interface{}{
		"reports": []interface{}{
			map[string]interface{}{"id": "r1", "owner": "a@example.com", "labels": []interface{}{map[string]interface{}{"name": "Cached"}}},
			map[string]interface{}{"id": "r2", "owner": "b@example.com", "labels": []interface{}{}},
		},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "reports[2]{") {
		t.Fatalf("expected tabular fold with selected columns, got:\n%s", s)
	}
	if !strings.Contains(s, "labels") || !strings.Contains(s, "Cached") {
		t.Fatalf("expected requested object column kept as JSON cell, got:\n%s", s)
	}
	if strings.Contains(s, "owner") {
		t.Fatalf("expected unselected columns excluded, got:\n%s", s)
	}
}

func TestToonMarshalCustomFilterKeepsObjectColumns(t *testing.T) {
	// A custom -f filter means the agent already hand-picked the fields;
	// dropping any of them would silently discard requested data. Object cells
	// encode as JSON strings to preserve the fold.
	viper.Set("rsh-filter", "body.reports[].{id: id, labels: labels}")
	t.Cleanup(viper.Reset)

	ct := dciToonContentType{}
	input := []interface{}{
		map[string]interface{}{"id": "r1", "labels": []interface{}{map[string]interface{}{"name": "Cached"}}},
		map[string]interface{}{"id": "r2", "labels": []interface{}{}},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "[2]{") {
		t.Fatalf("expected tabular fold with filtered fields kept, got:\n%s", s)
	}
	if !strings.Contains(s, "labels") || !strings.Contains(s, "Cached") {
		t.Fatalf("expected filtered object column kept as JSON cell, got:\n%s", s)
	}
}

func TestToonMarshalColumnSelectionSkipsUnrelatedLists(t *testing.T) {
	// -C applies to row lists that actually contain a selected column; other
	// lists (e.g. a schema array) keep their own columns instead of being
	// emptied by an unrelated selection.
	viper.Set("table-columns", "id,labels")
	t.Cleanup(viper.Reset)

	ct := dciToonContentType{}
	input := map[string]interface{}{
		"reports": []interface{}{
			map[string]interface{}{"id": "r1", "labels": []interface{}{}, "owner": "a@example.com"},
		},
		"schema": []interface{}{
			map[string]interface{}{"name": "service", "type": "string"},
		},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "reports[1]{id,labels}") {
		t.Fatalf("expected selection applied to matching list, got:\n%s", s)
	}
	if !strings.Contains(s, "schema[1]{name,type}") {
		t.Fatalf("expected unrelated list untouched by -C, got:\n%s", s)
	}
}

func TestToonMarshalSingleObjectKeepsNestedStructure(t *testing.T) {
	// Detail responses (single object, not a row list) have no fold to win, so
	// nested structure stays intact — and nested row lists inside them still
	// fold tabular.
	ct := dciToonContentType{}
	input := map[string]interface{}{
		"id":   "b1",
		"meta": map[string]interface{}{"x": 1},
		"alertThresholds": []interface{}{
			map[string]interface{}{"amount": 55.5, "percentage": 50},
			map[string]interface{}{"amount": 111, "percentage": 100},
		},
	}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "x: 1") {
		t.Fatalf("expected nested object kept on single-object body, got:\n%s", s)
	}
	if !strings.Contains(s, "alertThresholds[2]{") {
		t.Fatalf("expected nested row list folded tabular, got:\n%s", s)
	}
}

func TestToonMarshalKeepsEmptyListResponse(t *testing.T) {
	// A top-level empty list is meaningful ("no results") — it must stay an
	// array, not be flattened to a blank string like an empty row cell.
	ct := dciToonContentType{}
	input := map[string]interface{}{"reports": []interface{}{}}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "reports[0]") {
		t.Fatalf("expected empty list kept as array, got:\n%s", s)
	}
}

func TestToonMarshalObjectEncodesAsToon(t *testing.T) {
	ct := dciToonContentType{}
	input := map[string]interface{}{"name": "test", "value": 123}
	out, err := ct.Marshal(input)
	if err != nil {
		t.Fatalf("expected TOON output, got error: %v", err)
	}
	s := strings.TrimSpace(string(out))
	if len(s) == 0 {
		t.Fatal("expected non-empty output")
	}
	// TOON renders objects as `key: value` lines, not JSON. A leading `{` would
	// mean we silently hit the indented-JSON fallback instead of encoding TOON.
	if strings.HasPrefix(s, "{") {
		t.Fatalf("expected TOON output, got JSON fallback:\n%s", s)
	}
	if !strings.Contains(s, "name") || !strings.Contains(s, "test") {
		t.Fatalf("expected key/value in TOON output, got:\n%s", s)
	}
}

func TestToonMarshalScalarsEncode(t *testing.T) {
	ct := dciToonContentType{}
	// Scalars are valid TOON primitives; assert they encode without error and
	// without panicking (they do not exercise the JSON fallback path).
	for _, input := range []interface{}{"hello", 42, true} {
		out, err := ct.Marshal(input)
		if err != nil {
			t.Fatalf("expected output for %v, got error: %v", input, err)
		}
		if len(out) == 0 {
			t.Fatalf("expected non-empty output for %v", input)
		}
	}
}

func TestOutputFlagValidation(t *testing.T) {
	// Drive the real --output validation in addOutputFlag's PersistentPreRunE so
	// the accepted set can't silently drift from this test. "toon" must be
	// accepted (and set rsh-output-format); an unknown value must be rejected.
	oldRoot := cli.Root
	t.Cleanup(func() {
		cli.Root = oldRoot
		viper.Reset()
	})
	stubDestructiveMetadata(t)

	tests := []struct {
		value   string
		wantErr bool
	}{
		{"table", false},
		{"json", false},
		{"yaml", false},
		{"auto", false},
		{"toon", false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			viper.Reset()
			// Build a fresh command per case: addOutputFlag wires the --output
			// flag and its validating PersistentPreRunE onto the dci command, and
			// Execute() is what merges persistent flags + runs that PreRunE.
			root := &cobra.Command{Use: "dci"}
			subCmd := &cobra.Command{Use: "dci", RunE: func(*cobra.Command, []string) error { return nil }}
			root.AddCommand(subCmd)
			cli.Root = root
			addOutputFlag()
			// Execution starts at the root, so set args there and route to the
			// dci subcommand.
			root.SetArgs([]string{"dci", "--output", tt.value})
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)

			err := root.Execute()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("--output %q: expected error, got nil", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("--output %q: unexpected error: %v", tt.value, err)
			}
			if got := viper.GetString("rsh-output-format"); got != tt.value {
				t.Fatalf("--output %q: rsh-output-format = %q, want %q", tt.value, got, tt.value)
			}
		})
	}
}

// expectedSkillFiles lists every file the embedded skill should produce.
var expectedSkillFiles = []string{
	"skills/dci-cli/SKILL.md",
	"skills/dci-cli/agents/openai.yaml",
	"skills/dci-cli/references/capabilities.md",
	"skills/dci-cli/references/cost-optimization.md",
	"skills/dci-cli/references/csp-patterns.md",
	"skills/dci-cli/references/evals.md",
	"skills/dci-cli/references/examples.md",
	"skills/dci-cli/references/finops-baseline.md",
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

	wantFileCount := len(expectedSkillFiles) + 1
	if len(installedFiles) != wantFileCount {
		t.Errorf("expected %d files, got %d: %v", wantFileCount, len(installedFiles), installedFiles)
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

// stubDestructiveMetadata marks destructive-operation metadata as already
// loaded (with an empty command set), so tests that execute commands through
// addOutputFlag's PersistentPreRunE never reach the real metadata load —
// cli.Load requires cli.Init state that unit tests don't have. Previous state
// is restored on cleanup so the stub itself stays order-independent.
func stubDestructiveMetadata(t *testing.T) {
	t.Helper()
	oldSet := destructiveCommandSet
	oldRead := destructiveMetadataRead
	oldErr := destructiveMetadataErr
	destructiveCommandSet = map[string]bool{}
	destructiveMetadataRead = true
	destructiveMetadataErr = nil
	t.Cleanup(func() {
		destructiveCommandSet = oldSet
		destructiveMetadataRead = oldRead
		destructiveMetadataErr = oldErr
	})
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

func TestValidateCustomerContextValue(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{name: "customer domain", token: "acme.com", wantErr: false},
		{name: "subdomain", token: "cloud.acme.co.uk", wantErr: false},
		{name: "customer ID", token: "AbCdEf1234567890GhIj", wantErr: false},
		{name: "dot-less token of 8+ chars", token: "internal", wantErr: false},
		{name: "URL display name", token: "omni", wantErr: false},
		{name: "minimum-length slug", token: "ab1", wantErr: false},
		{name: "slug with dashes and digits", token: "acme-i2", wantErr: false},
		{name: "empty", token: "", wantErr: true},
		{name: "whitespace inside", token: "acme .com", wantErr: true},
		{name: "too short for a slug", token: "ab", wantErr: true},
		{name: "uppercase short token", token: "OMNI", wantErr: true},
		{name: "slug starting with a dash", token: "-omni", wantErr: true},
		{name: "slug ending with a dash", token: "omni-", wantErr: true},
		{name: "underscore in short token", token: "om_ni", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCustomerContextValue(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCustomerContextValue(%q) error = %v, wantErr %v", tt.token, err, tt.wantErr)
			}
		})
	}
}

func TestApplyCustomerContext(t *testing.T) {
	t.Run("adds header and query param preserving existing entries", func(t *testing.T) {
		t.Cleanup(viper.Reset)
		t.Setenv("DCI_CUSTOMER_CONTEXT", "")
		viper.Set("rsh-header", []string{"user-agent:test"})
		dir := t.TempDir()
		writeContextFile(t, dir, "acme.com")

		applyCustomerContext(dir)

		// The literals (not tenantIDHeaderPrefix / customerContextQueryPrefix)
		// are deliberate: they pin the wire names so the constants can't
		// silently drift.
		wantHeader := []string{"user-agent:test", "X-Tenant-Id:acme.com"}
		if got := viper.GetStringSlice("rsh-header"); !reflect.DeepEqual(got, wantHeader) {
			t.Errorf("rsh-header = %v, want %v", got, wantHeader)
		}
		wantQuery := []string{"customerContext=acme.com"}
		if got := viper.GetStringSlice("rsh-query"); !reflect.DeepEqual(got, wantQuery) {
			t.Errorf("rsh-query = %v, want %v", got, wantQuery)
		}
	})

	t.Run("no-op without a context", func(t *testing.T) {
		t.Cleanup(viper.Reset)
		t.Setenv("DCI_CUSTOMER_CONTEXT", "")

		applyCustomerContext(t.TempDir())

		if got := viper.GetStringSlice("rsh-header"); len(got) != 0 {
			t.Errorf("rsh-header = %v, want empty", got)
		}
		if got := viper.GetStringSlice("rsh-query"); len(got) != 0 {
			t.Errorf("rsh-query = %v, want empty", got)
		}
	})

	t.Run("does not duplicate existing header or query param", func(t *testing.T) {
		t.Cleanup(viper.Reset)
		t.Setenv("DCI_CUSTOMER_CONTEXT", "")
		viper.Set("rsh-header", []string{tenantIDHeaderPrefix + "already.set"})
		viper.Set("rsh-query", []string{customerContextQueryPrefix + "already.set"})
		dir := t.TempDir()
		writeContextFile(t, dir, "acme.com")

		applyCustomerContext(dir)

		wantHeader := []string{tenantIDHeaderPrefix + "already.set"}
		if got := viper.GetStringSlice("rsh-header"); !reflect.DeepEqual(got, wantHeader) {
			t.Errorf("rsh-header = %v, want %v", got, wantHeader)
		}
		wantQuery := []string{customerContextQueryPrefix + "already.set"}
		if got := viper.GetStringSlice("rsh-query"); !reflect.DeepEqual(got, wantQuery) {
			t.Errorf("rsh-query = %v, want %v", got, wantQuery)
		}
	})

	// If one transport already carries a context, neither may be filled from
	// the file/env — a partial fill could send two different tenants.
	t.Run("pre-existing header leaves query param untouched", func(t *testing.T) {
		t.Cleanup(viper.Reset)
		t.Setenv("DCI_CUSTOMER_CONTEXT", "")
		viper.Set("rsh-header", []string{tenantIDHeaderPrefix + "already.set"})
		dir := t.TempDir()
		writeContextFile(t, dir, "acme.com")

		applyCustomerContext(dir)

		if got := viper.GetStringSlice("rsh-query"); len(got) != 0 {
			t.Errorf("rsh-query = %v, want empty", got)
		}
	})

	t.Run("pre-existing query param leaves header untouched", func(t *testing.T) {
		t.Cleanup(viper.Reset)
		t.Setenv("DCI_CUSTOMER_CONTEXT", "")
		viper.Set("rsh-query", []string{customerContextQueryPrefix + "already.set"})
		dir := t.TempDir()
		writeContextFile(t, dir, "acme.com")

		applyCustomerContext(dir)

		if got := viper.GetStringSlice("rsh-header"); len(got) != 0 {
			t.Errorf("rsh-header = %v, want empty", got)
		}
	})
}

func TestCustomerContextFlagOverride(t *testing.T) {
	// Drives the real --customer-context override in addOutputFlag's
	// PersistentPreRunE, mirroring TestOutputFlagValidation's setup.
	run := func(t *testing.T, presetHeaders, presetQuery []string) ([]string, []string) {
		t.Helper()
		viper.Reset()
		t.Cleanup(viper.Reset)
		t.Cleanup(func() { customerContextFlagValue = "" })
		stubDestructiveMetadata(t)

		oldRoot := cli.Root
		root := &cobra.Command{Use: "dci"}
		subCmd := &cobra.Command{Use: "dci", RunE: func(*cobra.Command, []string) error { return nil }}
		root.AddCommand(subCmd)
		cli.Root = root
		t.Cleanup(func() { cli.Root = oldRoot })
		addOutputFlag()

		viper.Set("rsh-header", presetHeaders)
		viper.Set("rsh-query", presetQuery)
		root.SetArgs([]string{"dci", "--customer-context", "acme.com"})
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		if err := root.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		return viper.GetStringSlice("rsh-header"), viper.GetStringSlice("rsh-query")
	}

	t.Run("replaces existing tenant header and query param, preserves others", func(t *testing.T) {
		gotHeader, gotQuery := run(t,
			[]string{"user-agent:test", tenantIDHeaderPrefix + "old.example"},
			[]string{"maxResults=5", customerContextQueryPrefix + "old.example"})
		wantHeader := []string{"user-agent:test", tenantIDHeaderPrefix + "acme.com"}
		if !reflect.DeepEqual(gotHeader, wantHeader) {
			t.Errorf("rsh-header = %v, want %v", gotHeader, wantHeader)
		}
		wantQuery := []string{"maxResults=5", customerContextQueryPrefix + "acme.com"}
		if !reflect.DeepEqual(gotQuery, wantQuery) {
			t.Errorf("rsh-query = %v, want %v", gotQuery, wantQuery)
		}
	})

	t.Run("replaces tenant header regardless of name casing", func(t *testing.T) {
		// Users can inject headers via the hidden -H/--rsh-header flag, and
		// HTTP header names are case-insensitive — a lowercase variant must
		// still be replaced, not doubled.
		gotHeader, gotQuery := run(t, []string{"x-tenant-id:evil.example"}, nil)
		wantHeader := []string{tenantIDHeaderPrefix + "acme.com"}
		if !reflect.DeepEqual(gotHeader, wantHeader) {
			t.Errorf("rsh-header = %v, want %v", gotHeader, wantHeader)
		}
		wantQuery := []string{customerContextQueryPrefix + "acme.com"}
		if !reflect.DeepEqual(gotQuery, wantQuery) {
			t.Errorf("rsh-query = %v, want %v", gotQuery, wantQuery)
		}
	})
}

func TestCustomerContextFlag(t *testing.T) {
	bin := buildBinary(t)

	// list-budgets is spec-generated: even --help hydrates the command tree
	// from the OpenAPI spec, so these invocations must stay off the live API.
	home := t.TempDir()
	environment := append(hermeticSpecEnv(t, home, `{
		"openapi": "3.0.0",
		"info": {"title": "DCI test", "version": "1.0.0"},
		"paths": {
			"/budgets": {
				"get": {
					"operationId": "list-budgets",
					"responses": {"200": {"description": "OK"}}
				}
			}
		}
	}`), "DCI_API_KEY=test-key")

	t.Run("empty --customer-context errors", func(t *testing.T) {
		res := runCLIWithEnv(t, bin, home, environment, "list-budgets", "--customer-context", "")
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
		res := runCLIWithEnv(t, bin, home, environment, "list-budgets", "--help")
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

// TestDataCommandHonorsAPIBaseOverride guards against CMP-50503: with
// apis.json's persisted base pinned to one host (as it always is — the
// env override is deliberately never persisted, see ensureConfig) and
// DCI_API_BASE_URL pointing at a different host, a real data command must
// still route its request to the override, not the persisted base.
func TestDataCommandHonorsAPIBaseOverride(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()

	prodHit := false
	prod := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		prodHit = true
		http.NotFound(writer, request)
	}))
	t.Cleanup(prod.Close)

	devRequestPath := ""
	dev := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/openapi.json" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{
				"openapi": "3.0.0",
				"info": {"title": "DCI test", "version": "1.0.0"},
				"paths": {
					"/budgets": {
						"get": {
							"operationId": "list-budgets",
							"responses": {"200": {"description": "OK"}}
						}
					}
				}
			}`)
			return
		}
		devRequestPath = request.URL.Path
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[]`)
	}))
	t.Cleanup(dev.Close)

	// dciConfigDir() resolves against HOME via os.UserConfigDir() (macOS:
	// $HOME/Library/Application Support/dci; Linux: $XDG_CONFIG_HOME/dci or
	// $HOME/.config/dci). Compute the same path runCLIWithEnv's HOME (and
	// XDG_CONFIG_HOME on Linux) will make dciConfigDir() resolve to, so
	// restish's cli.Init reads the same file dci-cli's own ensureConfig()/
	// apiBase() use. apis.json is pinned to prod, as it always is on a real
	// machine — the override must never need to be persisted to take effect.
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	// dciConfigDir() memoizes its resolution per process; reset it so an
	// earlier test's HOME doesn't leak in, and so this test's own resolution
	// (this call, in-process, unlike the built binary's own separate process
	// below) doesn't leak into a later one.
	resetDCIConfigDirCache()
	t.Cleanup(resetDCIConfigDirCache)
	configDir := dciConfigDir()
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]interface{}{
		"dci": map[string]interface{}{
			"base":     prod.URL,
			"profiles": map[string]interface{}{"default": map[string]interface{}{}},
			"tls":      map[string]interface{}{"insecure": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "apis.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := []string{
		"DCI_API_BASE_URL=" + dev.URL,
		"DCI_API_KEY=test-key",
		"DCI_CACHE_DIR=" + filepath.Join(home, "cache"),
		"DCI_NO_UPDATE_CHECK=1",
	}

	res := runCLIWithEnv(t, bin, home, environment, "list-budgets")
	if res.timedOut {
		t.Fatalf("command timed out; output:\n%s", res.output)
	}
	if prodHit {
		t.Fatalf("list-budgets hit the persisted prod base instead of DCI_API_BASE_URL; output:\n%s", res.output)
	}
	if devRequestPath != "/budgets" {
		t.Fatalf("dev server saw path %q, want /budgets; output:\n%s", devRequestPath, res.output)
	}
}

// TestLogoutClearsRealCredentialsDuringAPIBaseOverride guards a Claude-review
// finding on PR #128: `dci logout` clears credentials via the global
// cli.Cache, which — under an active DCI_API_BASE_URL override —
// applyAPIBaseOverride has pointed at its throwaway per-invocation temp dir
// by the time logout's RunE runs. Logout must still clear the REAL,
// persisted cache.json (the credential that matters for every future
// invocation, override or not), not just the temp dir's copy that gets
// deleted when the command returns — otherwise logout reports success while
// silently leaving the real session intact.
func TestLogoutClearsRealCredentialsDuringAPIBaseOverride(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()

	dev := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{}}`)
	}))
	t.Cleanup(dev.Close)

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	resetDCIConfigDirCache()
	t.Cleanup(resetDCIConfigDirCache)
	configDir := dciConfigDir()
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]interface{}{
		"dci": map[string]interface{}{
			"base":     "https://api.doit.com",
			"profiles": map[string]interface{}{"default": map[string]interface{}{}},
			"tls":      map[string]interface{}{"insecure": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "apis.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}

	realCacheDir := filepath.Join(home, "cache")
	if err := os.MkdirAll(realCacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cacheJSON, err := json.Marshal(map[string]interface{}{
		"dci:default": map[string]interface{}{
			"token":   "real-oauth-token",
			"refresh": "real-refresh-token",
			"type":    "Bearer",
			"expires": "9999-12-31T23:59:59Z",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realCacheDir, "cache.json"), cacheJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	environment := []string{
		"DCI_API_BASE_URL=" + dev.URL,
		"DCI_CACHE_DIR=" + realCacheDir,
		"DCI_NO_UPDATE_CHECK=1",
	}

	res := runCLIWithEnv(t, bin, home, environment, "logout")
	if res.timedOut {
		t.Fatalf("command timed out; output:\n%s", res.output)
	}
	if res.exitCode != 0 {
		t.Fatalf("logout exit code = %d; output:\n%s", res.exitCode, res.output)
	}

	data, err := os.ReadFile(filepath.Join(realCacheDir, "cache.json"))
	if err != nil {
		t.Fatalf("real cache.json missing after logout: %v", err)
	}
	if strings.Contains(string(data), "real-oauth-token") || strings.Contains(string(data), "real-refresh-token") {
		t.Fatalf("logout under DCI_API_BASE_URL left the real credentials intact: %s", data)
	}
}

func TestIsHTMLErrorPage(t *testing.T) {
	tests := []struct {
		name string
		resp cli.Response
		want bool
	}{
		{
			name: "text/html content type",
			resp: cli.Response{Headers: map[string]string{"Content-Type": "text/html; charset=utf-8"}, Body: "anything"},
			want: true,
		},
		{
			name: "doctype body without content type",
			resp: cli.Response{Body: "<!DOCTYPE html><html><head><title>DoiT Console - Maintenance</title></head></html>"},
			want: true,
		},
		{
			name: "html body with leading whitespace",
			resp: cli.Response{Body: "\n  <html><body>oops</body></html>"},
			want: true,
		},
		{
			name: "html body as bytes",
			resp: cli.Response{Body: []byte("<head>...</head>")},
			want: true,
		},
		{
			name: "xml declaration edge page without content type",
			resp: cli.Response{Body: "<?xml version=\"1.0\"?><error>upstream</error>"},
			want: true,
		},
		{
			name: "html comment edge page without content type",
			resp: cli.Response{Body: "<!-- error --><html></html>"},
			want: true,
		},
		{
			name: "utf-8 bom before doctype",
			resp: cli.Response{Body: "\ufeff<!DOCTYPE html><html></html>"},
			want: true,
		},
		{
			name: "json object body is not html",
			resp: cli.Response{Headers: map[string]string{"Content-Type": "application/json"}, Body: map[string]interface{}{"answer": "hi"}},
			want: false,
		},
		{
			name: "json string body is not html",
			resp: cli.Response{Body: `{"answer":"hi"}`},
			want: false,
		},
		{
			name: "streaming SSE body is not html",
			resp: cli.Response{Headers: map[string]string{"Content-Type": "text/event-stream"}, Body: "data: {\"chunk\":1}"},
			want: false,
		},
		{
			name: "empty response is not html",
			resp: cli.Response{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHTMLErrorPage(tt.resp); got != tt.want {
				t.Errorf("isHTMLErrorPage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTraceID(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{name: "cf-ray preferred", headers: map[string]string{"Cf-Ray": "a023e448ca101c99", "X-Request-Id": "abc"}, want: "Cf-Ray=a023e448ca101c99"},
		{name: "case insensitive lookup", headers: map[string]string{"cf-ray": "deadbeef"}, want: "Cf-Ray=deadbeef"},
		{name: "falls back to request id", headers: map[string]string{"X-Request-Id": "req-123"}, want: "X-Request-Id=req-123"},
		{name: "none present", headers: map[string]string{"Content-Type": "text/html"}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := traceID(tt.headers); got != tt.want {
				t.Errorf("traceID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// recordingFormatter is a test double capturing whether the wrapped formatter
// was delegated to.
type recordingFormatter struct {
	called bool
	got    cli.Response
}

func (r *recordingFormatter) Format(resp cli.Response) error {
	r.called = true
	r.got = resp
	return nil
}

func TestResponseGuardFormat(t *testing.T) {
	t.Run("client error HTML is classified by status in agent mode", func(t *testing.T) {
		previousAgentMode := agentMode
		previousAgentUAMode := agentUAMode
		previousStderr := cli.Stderr
		agentMode = true
		agentUAMode = uaModeAgent
		resetErrorContractState()
		var stderr bytes.Buffer
		cli.Stderr = &stderr
		t.Cleanup(func() {
			agentMode = previousAgentMode
			agentUAMode = previousAgentUAMode
			cli.Stderr = previousStderr
			resetErrorContractState()
		})

		next := &recordingFormatter{}
		guard := dciResponseGuard{next: next}
		if err := guard.Format(cli.Response{
			Status:  400,
			Headers: map[string]string{"Content-Type": "text/html"},
			Body:    "<html>invalid maxResults</html>",
		}); err != nil {
			t.Fatal(err)
		}
		if next.called {
			t.Fatal("HTML client error delegated to formatter")
		}
		if responseExitCode != exitValidation {
			t.Fatalf("exit code = %d", responseExitCode)
		}
		var envelope structuredErrorEnvelope
		if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error.Code != "VALIDATION_ERROR" || envelope.Error.Retryable {
			t.Fatalf("error = %#v", envelope.Error)
		}
	})

	t.Run("empty successful response does not invoke the formatter", func(t *testing.T) {
		bodies := []interface{}{nil, "", " \n\t", []byte{}, []byte(" \n\t")}
		for _, body := range bodies {
			next := &recordingFormatter{}
			guard := dciResponseGuard{next: next}
			if err := guard.Format(cli.Response{Status: 200, Body: body}); err != nil {
				t.Fatalf("unexpected error for %#v: %v", body, err)
			}
			if next.called {
				t.Fatalf("formatter invoked for empty body %#v", body)
			}
		}
	})

	t.Run("empty error response still invokes the formatter", func(t *testing.T) {
		next := &recordingFormatter{}
		guard := dciResponseGuard{next: next}
		if err := guard.Format(cli.Response{Status: 500, Body: ""}); err != nil {
			t.Fatal(err)
		}
		if !next.called {
			t.Fatal("formatter was not invoked for an empty error response")
		}
	})

	t.Run("html page prints message and sets flag without delegating", func(t *testing.T) {
		nonJSONErrorResponse = false
		t.Cleanup(func() { nonJSONErrorResponse = false })

		next := &recordingFormatter{}
		guard := dciResponseGuard{next: next}

		r, w, _ := os.Pipe()
		oldStderr := cli.Stderr
		cli.Stderr = w

		err := guard.Format(cli.Response{
			Status:  524,
			Headers: map[string]string{"Content-Type": "text/html", "Cf-Ray": "a023e448ca101c99"},
			Body:    "<!DOCTYPE html><html><head><title>DoiT Console - Maintenance</title></head></html>",
		})

		w.Close()
		cli.Stderr = oldStderr
		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		output := string(buf[:n])
		r.Close()

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if next.called {
			t.Fatal("expected guard not to delegate to the wrapped formatter for HTML")
		}
		if !nonJSONErrorResponse {
			t.Fatal("expected nonJSONErrorResponse to be set")
		}
		if !strings.Contains(output, "non-JSON") {
			t.Errorf("expected message to mention non-JSON, got:\n%s", output)
		}
		if !strings.Contains(output, "HTTP status: 524") {
			t.Errorf("expected message to include HTTP status, got:\n%s", output)
		}
		if !strings.Contains(output, "Cf-Ray=a023e448ca101c99") {
			t.Errorf("expected message to include trace, got:\n%s", output)
		}
	})

	t.Run("json response delegates to wrapped formatter", func(t *testing.T) {
		nonJSONErrorResponse = false
		t.Cleanup(func() { nonJSONErrorResponse = false })

		next := &recordingFormatter{}
		guard := dciResponseGuard{next: next}

		resp := cli.Response{
			Status:  200,
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    map[string]interface{}{"answer": "hello"},
		}
		if err := guard.Format(resp); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !next.called {
			t.Fatal("expected guard to delegate to the wrapped formatter for JSON")
		}
		if nonJSONErrorResponse {
			t.Fatal("expected nonJSONErrorResponse to stay false for JSON")
		}
	})

	t.Run("2xx json error body prints body and flags non-zero exit", func(t *testing.T) {
		nonJSONErrorResponse = false
		t.Cleanup(func() { nonJSONErrorResponse = false })

		next := &recordingFormatter{}
		guard := dciResponseGuard{next: next}

		r, w, _ := os.Pipe()
		oldStderr := cli.Stderr
		cli.Stderr = w

		err := guard.Format(cli.Response{
			Status:  200,
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    map[string]interface{}{"error": "generation failed midway"},
		})

		w.Close()
		cli.Stderr = oldStderr
		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		output := string(buf[:n])
		r.Close()

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !next.called {
			t.Fatal("expected guard to still print the structured error body via the wrapped formatter")
		}
		if !nonJSONErrorResponse {
			t.Fatal("expected nonJSONErrorResponse to be set for a 2xx JSON error body")
		}
		if !strings.Contains(output, "application error") || !strings.Contains(output, "generation failed midway") {
			t.Errorf("expected stderr to frame the application error, got:\n%s", output)
		}
	})
}

func TestJSONApplicationError(t *testing.T) {
	tests := []struct {
		name    string
		resp    cli.Response
		wantMsg string
		wantOK  bool
	}{
		{
			name:    "2xx with string error",
			resp:    cli.Response{Status: 200, Body: map[string]interface{}{"error": "boom"}},
			wantMsg: "boom",
			wantOK:  true,
		},
		{
			name:    "2xx with object error and message",
			resp:    cli.Response{Status: 200, Body: map[string]interface{}{"error": map[string]interface{}{"message": "nested boom"}}},
			wantMsg: "nested boom",
			wantOK:  true,
		},
		{
			name:    "2xx with object error without message",
			resp:    cli.Response{Status: 200, Body: map[string]interface{}{"error": map[string]interface{}{"code": 42}}},
			wantMsg: "application error",
			wantOK:  true,
		},
		{
			name:   "2xx success body is ignored",
			resp:   cli.Response{Status: 200, Body: map[string]interface{}{"answer": "hi"}},
			wantOK: false,
		},
		{
			name:   "2xx empty string error is ignored",
			resp:   cli.Response{Status: 200, Body: map[string]interface{}{"error": ""}},
			wantOK: false,
		},
		{
			name:   "2xx null error is ignored",
			resp:   cli.Response{Status: 200, Body: map[string]interface{}{"error": nil}},
			wantOK: false,
		},
		{
			name:   "non-2xx error is left to restish",
			resp:   cli.Response{Status: 400, Body: map[string]interface{}{"error": "bad request"}},
			wantOK: false,
		},
		{
			name:   "non-object body is ignored",
			resp:   cli.Response{Status: 200, Body: "just a string"},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := jsonApplicationError(tt.resp)
			if ok != tt.wantOK {
				t.Fatalf("jsonApplicationError() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && msg != tt.wantMsg {
				t.Errorf("jsonApplicationError() msg = %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}

// --- local-timezone display tests ---

// withDisplayZone points the instant-display resolver at a fixed IANA zone
// with table output active, restoring the prior state on cleanup. Tests use
// this instead of mutating TZ: time.Local is fixed at process init, so env
// changes cannot leak in, and the resolver var is the injection point the
// production PreRun uses anyway.
func withDisplayZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	prevLoc := displayTimeLocation
	prevFormat := viper.Get("rsh-output-format")
	prevShown := localizedInstantShown
	displayTimeLocation = loc
	viper.Set("rsh-output-format", "table")
	t.Cleanup(func() {
		displayTimeLocation = prevLoc
		viper.Set("rsh-output-format", prevFormat)
		localizedInstantShown = prevShown
	})
	return loc
}

func TestDisplayLocationDefaultsToUTCWhenUnresolved(t *testing.T) {
	prev := displayTimeLocation
	displayTimeLocation = nil
	t.Cleanup(func() { displayTimeLocation = prev })
	viper.Set("rsh-output-format", "table")
	t.Cleanup(func() { viper.Set("rsh-output-format", nil) })

	if got := displayLocation(); got != time.UTC {
		t.Errorf("unresolved displayLocation() = %v, want UTC", got)
	}
}

func TestDisplayLocationMachineContextsStayUTC(t *testing.T) {
	withDisplayZone(t, "Asia/Jerusalem")

	for _, format := range []string{"json", "yaml", "csv", "toon", ""} {
		viper.Set("rsh-output-format", format)
		if got := displayLocation(); got != time.UTC {
			t.Errorf("displayLocation() with format %q = %v, want UTC", format, got)
		}
	}

	viper.Set("rsh-output-format", "table")
	if got := displayLocation(); got == time.UTC {
		t.Error("displayLocation() with table format = UTC, want Asia/Jerusalem")
	}

	viper.Set("display-utc", true)
	t.Cleanup(func() { viper.Set("display-utc", nil) })
	if got := displayLocation(); got != time.UTC {
		t.Errorf("displayLocation() with --utc = %v, want UTC", got)
	}
	viper.Set("display-utc", false)

	prevAgent := agentMode
	agentMode = true
	t.Cleanup(func() { agentMode = prevAgent })
	if got := displayLocation(); got != time.UTC {
		t.Errorf("displayLocation() in agent mode = %v, want UTC", got)
	}
}

func TestPrettifyTimestampLocalizesInstants(t *testing.T) {
	tests := []struct {
		zone string
		in   string
		want string
	}{
		{"Asia/Jerusalem", "2026-08-18T02:30:59Z", "2026-08-18 05:30"},
		{"Asia/Kolkata", "2026-08-18T02:30:59Z", "2026-08-18 08:00"},
		{"Pacific/Chatham", "2026-08-18T02:30:59Z", "2026-08-18 15:15"},
		// Late-evening UTC instants cross into the next local day — correct
		// for instants, which is exactly why labels never take this path.
		{"Pacific/Chatham", "2026-08-18T23:30:00Z", "2026-08-19 12:15"},
		// US spring-forward (2026-03-08 07:00Z): EST before, EDT after.
		{"America/New_York", "2026-03-08T06:59:00Z", "2026-03-08 01:59"},
		{"America/New_York", "2026-03-08T07:30:00Z", "2026-03-08 03:30"},
		// US fall-back (2026-11-01): two UTC instants share a wall time.
		{"America/New_York", "2026-11-01T05:30:00Z", "2026-11-01 01:30"},
		{"America/New_York", "2026-11-01T06:30:00Z", "2026-11-01 01:30"},
	}
	for _, tt := range tests {
		t.Run(tt.zone+"/"+tt.in, func(t *testing.T) {
			withDisplayZone(t, tt.zone)
			if got := prettifyTimestamp(tt.in); got != tt.want {
				t.Errorf("prettifyTimestamp(%q) in %s = %q, want %q", tt.in, tt.zone, got, tt.want)
			}
		})
	}
}

func TestPrettifyTimestampMidnightStaysBareUTCDate(t *testing.T) {
	// Midnight UTC is date-valued (contract terms, invoice dates, budget
	// periods): bare UTC date in every zone, never shifted.
	for _, zone := range []string{"Asia/Jerusalem", "Pacific/Chatham", "America/New_York"} {
		withDisplayZone(t, zone)
		if got := prettifyTimestamp("2026-06-01T00:00:00Z"); got != "2026-06-01" {
			t.Errorf("midnight in %s = %q, want bare UTC date", zone, got)
		}
	}
}

func TestPrettifyTimestampSetsZoneNoteFlagOnlyWhenShifted(t *testing.T) {
	withDisplayZone(t, "Asia/Jerusalem")
	localizedInstantShown = false
	prettifyTimestamp("2026-06-01T00:00:00Z") // midnight: date-valued, no shift
	if localizedInstantShown {
		t.Error("midnight collapse should not flag a localized instant")
	}
	prettifyTimestamp("2026-08-18T02:30:59Z")
	if !localizedInstantShown {
		t.Error("localized instant did not set the zone-note flag")
	}
}

func TestReportLabelsImmuneToDisplayZone(t *testing.T) {
	withDisplayZone(t, "Pacific/Chatham")
	viper.Set("raw-numbers", false)
	t.Cleanup(func() { viper.Set("raw-numbers", nil) })

	daily := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).Unix()
	hourly := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC).Unix()

	if got := displayTimestampCell(float64(daily), "timestamp", labelDisplay); got != "2026-05-01" {
		t.Errorf("daily label under Chatham = %v, want bare UTC date", got)
	}
	viper.Set("report-hourly", true)
	t.Cleanup(func() { viper.Set("report-hourly", nil) })
	if got := displayTimestampCell(float64(hourly), "timestamp", labelDisplay); got != "2026-08-09 01:00" {
		t.Errorf("hourly label under Chatham = %v, want UTC hour label", got)
	}
	viper.Set("report-hourly", false)

	// Machine style keeps full RFC3339 UTC regardless of zone.
	if got := displayTimestampCell(float64(daily), "timestamp", labelRFC3339); got != "2026-05-01T00:00:00Z" {
		t.Errorf("machine label under Chatham = %v, want RFC3339 Z", got)
	}
	// Terminal label text is immune to the instant path by construction.
	if got := prettifyTimestamp("2026-08-09 01:00"); got != "2026-08-09 01:00" {
		t.Errorf("terminal label re-entered the instant path: %q", got)
	}
}

func TestCSVReportTimestampsStayRFC3339UnderZone(t *testing.T) {
	withDisplayZone(t, "Pacific/Chatham")
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	body := map[string]interface{}{
		"result": map[string]interface{}{
			"rows": []interface{}{
				[]interface{}{"svc", 12.5, float64(1782864000)},
			},
			"schema": []interface{}{
				map[string]interface{}{"name": "service_description", "type": "string"},
				map[string]interface{}{"name": "cost", "type": "float"},
				map[string]interface{}{"name": "timestamp", "type": "timestamp"},
			},
		},
	}
	out, err := dciCSVContentType{}.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1782864000, 0).UTC().Format(time.RFC3339)
	if !strings.Contains(string(out), want) {
		t.Errorf("CSV under Chatham = %q, want RFC3339 UTC %q", out, want)
	}
}

func TestAnomalyWindowColumnsStayUTCWhileInstantsLocalize(t *testing.T) {
	withDisplayZone(t, "Asia/Jerusalem")
	viper.Set("raw-numbers", false)
	viper.Set("utc-label-columns", "startTime,endTime")
	t.Cleanup(func() {
		viper.Set("raw-numbers", nil)
		viper.Set("utc-label-columns", "")
	})

	hourlyStart := float64(time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC).UnixMilli())
	if got := tableCellText("startTime", hourlyStart); got != "2026-08-12 01:00" {
		t.Errorf("hourly anomaly startTime = %q, want UTC label (no +03:00 shift)", got)
	}
	dailyStart := float64(time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC).UnixMilli())
	if got := tableCellText("startTime", dailyStart); got != "2026-08-12" {
		t.Errorf("daily anomaly startTime = %q, want bare UTC date", got)
	}
	// Sibling instants on the same row keep localizing.
	if got := tableCellText("acknowledgedAt", "2026-08-12T09:15:30Z"); got != "2026-08-12 12:15" {
		t.Errorf("acknowledgedAt = %q, want localized instant", got)
	}
}

func TestMarkAnomalyWindowColumns(t *testing.T) {
	prev := invokedCommandName
	t.Cleanup(func() {
		invokedCommandName = prev
		viper.Set("utc-label-columns", "")
	})

	viper.Set("utc-label-columns", "")
	invokedCommandName = "list-anomalies"
	markAnomalyWindowColumns()
	if got := viper.GetString("utc-label-columns"); got != "startTime,endTime,started (UTC)" {
		t.Errorf("list-anomalies utc-label-columns = %q", got)
	}

	viper.Set("utc-label-columns", "")
	invokedCommandName = "list-budgets"
	markAnomalyWindowColumns()
	if got := viper.GetString("utc-label-columns"); got != "" {
		t.Errorf("list-budgets utc-label-columns = %q, want empty", got)
	}
}

func TestMidnightEpochMsCalendarDatesStayBareUnderZone(t *testing.T) {
	// Budget startPeriod / invoice dueDate style: epoch ms at midnight UTC.
	withDisplayZone(t, "Pacific/Chatham")
	viper.Set("raw-numbers", false)
	t.Cleanup(func() { viper.Set("raw-numbers", nil) })

	if got := formatTableValue(1785542400000.0); got != "2026-08-01" {
		t.Errorf("midnight epoch-ms under Chatham = %q, want bare UTC date", got)
	}
}

func TestResolveDisplayLocation(t *testing.T) {
	if got := resolveDisplayLocation(""); got != time.Local {
		t.Errorf("empty DCI_TZ = %v, want time.Local", got)
	}
	if got := resolveDisplayLocation("Europe/Berlin"); got.String() != "Europe/Berlin" {
		t.Errorf("DCI_TZ=Europe/Berlin = %v", got)
	}
	if got := resolveDisplayLocation("Not/AZone"); got != time.Local {
		t.Errorf("invalid DCI_TZ = %v, want time.Local fallback", got)
	}
}

func TestMaybeNoteDisplayZone(t *testing.T) {
	withDisplayZone(t, "Asia/Kolkata")
	oldStderr := cli.Stderr
	var stderr strings.Builder
	cli.Stderr = &stderr
	t.Cleanup(func() { cli.Stderr = oldStderr })

	localizedInstantShown = false
	maybeNoteDisplayZone()
	if stderr.Len() != 0 {
		t.Errorf("note emitted without a localized instant: %q", stderr.String())
	}

	localizedInstantShown = true
	maybeNoteDisplayZone()
	want := "note: times shown in Asia/Kolkata (UTC+05:30); pass --utc for UTC\n"
	if stderr.String() != want {
		t.Errorf("zone note = %q, want %q", stderr.String(), want)
	}

	// Flag is consumed: a second call stays silent.
	maybeNoteDisplayZone()
	if stderr.String() != want {
		t.Errorf("zone note repeated: %q", stderr.String())
	}
}

func TestRenderHiddenColumnsHintCompactOnTTY(t *testing.T) {
	keys := []string{"sku", "2026-08-01", "total", "trend"}
	hidden := []string{"2026-08-04", "2026-08-05", "2026-08-19"}

	forceTUI(t, false)
	verbose := renderHiddenColumnsHint(keys, hidden)
	if !strings.Contains(verbose, "Hidden columns (nested objects, or too many to fit): 2026-08-04, 2026-08-05, 2026-08-19") ||
		!strings.Contains(verbose, "Use -C to choose columns") {
		t.Fatalf("non-TTY hint must keep the verbose form, got %q", verbose)
	}

	forceTUI(t, true)
	compact := renderHiddenColumnsHint(keys, hidden)
	if !strings.Contains(compact, "+3 hidden: 2026-08-04 … 2026-08-19") ||
		!strings.Contains(compact, "-C to choose · -M wrap · -W widen") {
		t.Fatalf("TTY hint must be the compact one-liner, got %q", compact)
	}
	if strings.Contains(compact, "2026-08-05") {
		t.Fatalf("compact hint must span, not list, got %q", compact)
	}

	// One or two hidden columns are shorter listed than spanned.
	pair := renderHiddenColumnsHint(keys, hidden[:2])
	if !strings.Contains(pair, "+2 hidden: 2026-08-04, 2026-08-05") {
		t.Fatalf("short hidden lists must be spelled out, got %q", pair)
	}

	// A `dci ai` slash child is piped (no TTY), but a human reads the output
	// in the session: DCI_SESSION_RENDER selects the compact form too.
	forceTUI(t, false)
	t.Setenv("DCI_SESSION_RENDER", "1")
	session := renderHiddenColumnsHint(keys, hidden)
	if !strings.Contains(session, "+3 hidden: 2026-08-04 … 2026-08-19") {
		t.Fatalf("session-render hint must be the compact one-liner, got %q", session)
	}
}

func TestSessionRenderActive(t *testing.T) {
	t.Setenv("DCI_SESSION_RENDER", "")
	if sessionRenderActive() {
		t.Fatal("empty DCI_SESSION_RENDER must be inactive")
	}
	t.Setenv("DCI_SESSION_RENDER", "1")
	if !sessionRenderActive() {
		t.Fatal("DCI_SESSION_RENDER=1 must be active")
	}
	t.Setenv("DCI_SESSION_RENDER", "0")
	if sessionRenderActive() {
		t.Fatal("DCI_SESSION_RENDER=0 must be inactive")
	}
	t.Setenv("DCI_SESSION_RENDER", "banana")
	if sessionRenderActive() {
		t.Fatal("unparseable DCI_SESSION_RENDER must be inactive")
	}
}

func TestBareDCIRoutesOnTUIGate(t *testing.T) {
	oldRoot := cli.Root
	oldTUI := tuiActive
	oldLaunch := launchAISession
	t.Cleanup(func() { cli.Root = oldRoot; tuiActive = oldTUI; launchAISession = oldLaunch })

	dir := t.TempDir()
	launched := false
	launchAISession = func(configDir string) error { launched = true; return nil }

	// Each case executes a fresh root: cobra persists parsed flag values
	// (notably --help) across Execute calls on one command, which would let
	// every case after the --help one pass without exercising the RunE
	// routing at all.
	execute := func(args ...string) {
		t.Helper()
		cli.Root = &cobra.Command{Use: "dci", SilenceUsage: true, SilenceErrors: true}
		lockToDCI(dir)
		cli.Root.SetOut(io.Discard)
		cli.Root.SetErr(io.Discard)
		cli.Root.SetArgs(args)
		launched = false
		if err := cli.Root.Execute(); err != nil {
			t.Fatal(err)
		}
	}

	// Human at a terminal: bare dci opens the session.
	tuiActive = func() bool { return true }
	execute()
	if !launched {
		t.Fatal("bare dci at a terminal should open the AI session")
	}

	// --help wins even at a terminal: cobra resolves it before RunE.
	execute("--help")
	if launched {
		t.Fatal("dci --help must print help, never open a session")
	}

	// Pipes/CI/agents (the gate is false): help, exactly as before.
	tuiActive = func() bool { return false }
	execute()
	if launched {
		t.Fatal("non-TTY bare dci must print help, not open a session")
	}

	// The persisted opt-out: {"default": "help"} keeps help for humans too.
	tuiActive = func() bool { return true }
	if err := saveAISettings(dir, aiSettings{Default: "help"}); err != nil {
		t.Fatal(err)
	}
	execute()
	if launched {
		t.Fatal("default=help must fall back to the help screen")
	}

	// And removing the opt-out routes back to the session — proof the later
	// cases run against live routing, not a stale help flag.
	if err := saveAISettings(dir, aiSettings{}); err != nil {
		t.Fatal(err)
	}
	execute()
	if !launched {
		t.Fatal("clearing the opt-out should restore the session default")
	}
}

func TestAIDefaultEnabled(t *testing.T) {
	dir := t.TempDir()
	if !aiDefaultEnabled(dir) {
		t.Fatal("absent settings default to the session")
	}
	if err := saveAISettings(dir, aiSettings{Default: "help"}); err != nil {
		t.Fatal(err)
	}
	if aiDefaultEnabled(dir) {
		t.Fatal("default=help must disable the session default")
	}
	if err := saveAISettings(dir, aiSettings{Default: "sesion"}); err != nil { // typo stays session
		t.Fatal(err)
	}
	if !aiDefaultEnabled(dir) {
		t.Fatal("an unrecognized value must keep the session default")
	}
}

func TestCIEnvDetected(t *testing.T) {
	for value, want := range map[string]bool{"true": true, "1": true, "false": false, "": false, "woodpecker": false} {
		t.Setenv("CI", value)
		if got := ciEnvDetected(); got != want {
			t.Errorf("ciEnvDetected() with CI=%q = %v, want %v", value, got, want)
		}
	}
}
