package main

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
	// Embed the IANA zone database so DCI_TZ works on hosts without one
	// (notably Windows installs via Scoop/WinGet).
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/alexeyco/simpletable"
	"github.com/mattn/go-runewidth"
	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/openapi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	toon "github.com/toon-format/toon-go"
	"golang.org/x/term"
)

var version string = "dev"

const (
	defaultAPIBase = "https://api.doit.com"
	apiKeyEnvName  = "DCI_API_KEY"
)

var configuredAPIBase string

// apiBase returns the API base URL, allowing override via DCI_API_BASE_URL.
func apiBase() (string, error) {
	environmentBase := strings.TrimSpace(os.Getenv("DCI_API_BASE_URL"))
	if environmentBase != "" {
		base, err := normalizeAPIBase(environmentBase)
		if err != nil {
			return "", fmt.Errorf("invalid DCI_API_BASE_URL: %w", err)
		}
		return base, nil
	}
	base := configuredAPIBase
	if base == "" {
		base = defaultAPIBase
	}
	return normalizeAPIBase(base)
}

func normalizeAPIBase(base string) (string, error) {
	base = strings.TrimSpace(base)
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid API base: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("API base must use https:// scheme (got %q)", u.Scheme)
	}
	return strings.TrimRight(base, "/"), nil
}

//go:embed skills/dci-cli
var skillFS embed.FS

// customerContextFlagValue holds the --customer-context / -D flag value when
// set, used to suppress the Doer hint even when no persistent context file exists.
var customerContextFlagValue string

// agentMode reports whether dci is running in agent mode for the current
// invocation (compact, deterministic output with no decoration). It is resolved
// once at startup and read throughout the run.
var agentMode bool

// agentModeReason records why agentMode resolved the way it did, for diagnostics.
var agentModeReason string

// uaMode classifies how dci is being driven, for the User-Agent "mode=" token.
// It is finer-grained than agentMode (a bool): agent mode reached via the
// non-TTY soft signal (pipes, redirects, CI/CD) is reported as noninteractive
// rather than agent, so analytics can tell genuine AI-agent traffic apart from
// incidental non-interactive use.
type uaMode string

const (
	uaModeInteractive    uaMode = "interactive"    // human at a TTY (or explicit human mode)
	uaModeAgent          uaMode = "agent"          // explicit --agent/DCI_AGENT_MODE=1, or a known AI-agent env var
	uaModeNonInteractive uaMode = "noninteractive" // non-TTY soft signal: pipe/redirect/CI
)

// agentUAMode records the interface classification for the User-Agent token.
var agentUAMode uaMode

// agentEnvDetected holds the name of the detected agent environment variable (if
// any), used to surface the human-mode "an optimized agent mode exists" tip.
var agentEnvDetected string

// cachedDCIConfigDir memoizes dciConfigDir()'s first resolution for the
// current invocation (reset alongside the rest of run()'s per-invocation
// state). applyAPIBaseOverride later points DCI_CONFIG_DIR at a private,
// per-invocation temp dir so restish's own cli.Init() picks up the override
// — but completion (name_completion.go), the `dci open` picker
// (tui_picker.go), and anything else that resolves the config dir AFTER
// that point must still see the real, persisted directory (the name cache,
// customer context, and update-check cache all live there and are never
// copied into the throwaway temp dir). Caching the first, pre-override
// resolution and reusing it for the rest of the invocation gives every
// caller the real directory without threading configDir through each of
// them individually.
var cachedDCIConfigDir string

// dciConfigDir mirrors restish's own getConfigDir("dci"): DCI_CONFIG_DIR
// takes priority when set. Without this, dci-cli's own ensureConfig could
// read/write a different apis.json than the one restish's cli.Init()
// actually loads — silently defeating anything that depends on both sides
// agreeing on where apis.json lives (this is exactly how DCI_API_BASE_URL
// stayed broken across several earlier fix attempts).
func dciConfigDir() string {
	if cachedDCIConfigDir != "" {
		return cachedDCIConfigDir
	}
	cachedDCIConfigDir = resolveDCIConfigDir()
	return cachedDCIConfigDir
}

// resetDCIConfigDirCache clears dciConfigDir()'s memoized value. run()
// already does this as part of its per-invocation reset; tests that call
// completionPreflight/pickerEntries/other dciConfigDir() callers directly —
// without going through run() — need it too, or a value memoized by an
// earlier test in the same process leaks into one that expects a fresh
// resolution (e.g. a "cold cache" test finding a warm one from a previous
// test's directory).
func resetDCIConfigDirCache() {
	cachedDCIConfigDir = ""
}

func resolveDCIConfigDir() string {
	if dir := os.Getenv("DCI_CONFIG_DIR"); dir != "" {
		return dir
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		cfgDir := filepath.Join(dir, "dci")

		// Prefer existing config directories to avoid breaking users on macOS.
		if _, err := os.Stat(cfgDir); err == nil {
			return cfgDir
		}
		legacy := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "dci")
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
		return cfgDir
	}

	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "dci")
}

func ensureConfig(configDir string) (bool, error) {
	configFile := filepath.Join(configDir, "apis.json")

	// DCI_API_BASE_URL is deliberately NOT persisted here: apiBase() prefers
	// the env var for the invocation it is set on, and one dev-targeted run
	// must not silently strand every later run on that base (which is exactly
	// what the previous persist-on-sight behavior did).
	if _, err := os.Stat(configFile); err == nil {
		if err := tightenFilePermissions(configFile, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "warning: unable to tighten config permissions for %s: %v\n", configFile, err)
		}
		base, err := readConfigBase(configFile)
		if err == nil {
			base, err = normalizeAPIBase(base)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: unable to use API base from %s (%v); using %s\n", configFile, err, defaultAPIBase)
			base = defaultAPIBase
			if err := writeConfig(configFile, base); err != nil {
				return false, fmt.Errorf("unable to repair %s: %w", configFile, err)
			}
		}
		configuredAPIBase = base
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return false, err
	}

	// First run always seeds the file with the production default, even under
	// DCI_API_BASE_URL — the env override stays per-invocation (apiBase()
	// prefers it at runtime) and never becomes the stored base.
	if err := writeConfig(configFile, defaultAPIBase); err != nil {
		return false, err
	}
	configuredAPIBase = defaultAPIBase

	return true, nil
}

// writeOverriddenAPIsConfig reads srcFile (the real apis.json), patches only
// its "dci.base" field to override, and writes the result to dstFile (a
// path inside the private per-invocation temp dir). Every other field —
// auth, TLS, and anything else already on disk — is preserved untouched.
func writeOverriddenAPIsConfig(srcFile, dstFile, override string) error {
	original, err := os.ReadFile(srcFile)
	if err != nil {
		return err
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(original, &doc); err != nil {
		return fmt.Errorf("unable to parse %s: %w", srcFile, err)
	}
	dci, ok := doc["dci"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("%s is missing a \"dci\" section", srcFile)
	}
	dci["base"] = override
	doc["dci"] = dci

	patched, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dstFile, patched, 0o600)
}

// restishCacheDir mirrors restish's own getCacheDir("dci"): honors
// DCI_CACHE_DIR, falling back to os.UserCacheDir()/dci.
func restishCacheDir() string {
	if dir := os.Getenv("DCI_CACHE_DIR"); dir != "" {
		return dir
	}
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userCacheDir, "dci")
}

// detachedRefreshCommands are the hidden subcommands a background refresher
// re-execs the binary as (update.go's __refresh-update-check, name_completion.go's
// __refresh-names). Each inherits the parent's full environment, including
// DCI_API_BASE_URL, but neither ever talks to the DCI API — applyAPIBaseOverride
// skips them entirely rather than paying for an isolated config/cache dir
// neither would use.
var detachedRefreshCommands = map[string]bool{
	"__refresh-update-check": true,
	"__refresh-names":        true,
}

// isDetachedRefreshInvocation reports whether args is a detached background
// refresher re-exec rather than a normal user invocation.
func isDetachedRefreshInvocation(args []string) bool {
	return len(args) > 1 && detachedRefreshCommands[args[1]]
}

// realDCIDirOverrides records what DCI_CONFIG_DIR/DCI_CACHE_DIR were set to
// (or absent) before applyAPIBaseOverride pointed them at its throwaway temp
// dir, if it did. Nil until the first (successful) override of this
// invocation; reset alongside run()'s other per-invocation state.
//
// detachedRefreshEnv reads this to give the __refresh-names child (spawned
// mid-invocation by completionPreflight, after applyAPIBaseOverride has
// already run) the REAL directories rather than letting it inherit the
// temp-dir env vars via plain os.Environ(): that child does real network
// I/O and writes its refreshed name cache to whatever DCI_CONFIG_DIR/
// DCI_CACHE_DIR say, and the parent's deferred cleanup removes the temp dir
// on exit — a write racing (or losing to) that removal, into a directory
// nobody will ever read again, is exactly the kind of silently-broken
// completion refresh this exists to prevent.
type dciDirOverride struct {
	value string
	had   bool
}
type realDCIDirOverridesState struct {
	configDir dciDirOverride
	cacheDir  dciDirOverride
}

var realDCIDirOverrides *realDCIDirOverridesState

// realCacheDir returns restish's REAL cache directory (dci.cbor, cache.json,
// the OAuth token) regardless of any active DCI_API_BASE_URL override —
// unlike restishCacheDir(), which just reads the current DCI_CACHE_DIR and
// so returns applyAPIBaseOverride's throwaway temp dir once an override is
// active. Callers that only ever consult local cache state rather than make
// a routed API call — completion's cachedSpecAvailableForInvocation, `dci
// ai`'s tool-call subprocess env — must use this one: the temp dir
// deliberately never gets a copy of dci.cbor (to avoid ever serving a spec
// cached from a different host), so asking it whether a spec is cached
// always answers "no", silently breaking Tab completion for an entire
// DCI_API_BASE_URL session even though a real, warm cache exists.
func realCacheDir() string {
	if realDCIDirOverrides != nil {
		if realDCIDirOverrides.cacheDir.had {
			return realDCIDirOverrides.cacheDir.value
		}
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		return filepath.Join(userCacheDir, "dci")
	}
	return restishCacheDir()
}

// detachedRefreshEnv returns the environment a detached refresh child
// (__refresh-update-check, __refresh-names) should inherit: os.Environ(),
// with DCI_CONFIG_DIR/DCI_CACHE_DIR forced back to what they were before any
// active DCI_API_BASE_URL override, so the child never writes into (or
// reads a stale copy from) the parent's temp dir. A nil realDCIDirOverrides
// (no override active, or the child is spawned before applyAPIBaseOverride
// ever runs) means os.Environ() is already correct as-is.
func detachedRefreshEnv() []string {
	if realDCIDirOverrides == nil {
		return os.Environ()
	}
	env := os.Environ()
	filtered := make([]string, 0, len(env)+2)
	for _, e := range env {
		if strings.HasPrefix(e, "DCI_CONFIG_DIR=") || strings.HasPrefix(e, "DCI_CACHE_DIR=") {
			continue
		}
		filtered = append(filtered, e)
	}
	if realDCIDirOverrides.configDir.had {
		filtered = append(filtered, "DCI_CONFIG_DIR="+realDCIDirOverrides.configDir.value)
	}
	if realDCIDirOverrides.cacheDir.had {
		filtered = append(filtered, "DCI_CACHE_DIR="+realDCIDirOverrides.cacheDir.value)
	}
	return filtered
}

// applyAPIBaseOverride makes DCI_API_BASE_URL actually control routing.
//
// This used to work by rewriting the real apis.json's "dci.base" field on
// disk in place, restoring it right after restish's cli.Init() read it.
// That traded one bug (the override being ignored) for a much worse class:
// every invocation — override or not — shared one mutable file that
// restish's own cli.Init()/cli.Run() also read/wrote, so making the swap
// safe required a cross-process lock, stale-lock reclamation, and a
// spec-cache invalidation sidecar. Six rounds of review each found a new
// concurrency bug in that machinery (a panic during cli.Init() leaking the
// swapped base forever; a lock-file TOCTOU letting two processes both
// "reclaim" a stale lock and end up inside the critical section at once;
// DCI_CONFIG_DIR — which restish itself honors — silently pointing the two
// processes at different files entirely) — each fix added surface area
// instead of shrinking it, which is the sign the shared-mutable-file
// approach was the wrong layer to solve this at.
//
// Instead: when the override is set, this seeds a private, per-invocation
// config+cache directory (DCI_CONFIG_DIR/DCI_CACHE_DIR, both already read by
// restish's own getConfigDir/getCacheDir) with a copy of the real apis.json
// — patched to the override base — and the real cache.json (so the OAuth
// session carries over; restish's disk cache is a single directory shared
// by auth, the OpenAPI spec, and HTTP response caching, so isolating one
// means isolating all three). The real apis.json is never touched, so
// there is nothing to lock, nothing to restore, and nothing for a
// concurrent invocation — with or without its own override — to race.
// dci.cbor (the cached OpenAPI spec) is deliberately NOT copied in: it
// starts empty in the temp dir, so cli.Init()'s later cli.Load() always
// fetches fresh from the override host rather than risking a stale spec
// from a different host.
//
// pendingAPIBaseOverrideCleanup holds the cleanup func applyAPIBaseOverride
// returns, until run() defers runPendingAPIBaseOverrideCleanup (which reads
// and clears it, guarding against a double-run). Package-level rather than
// a plain local so login_page.go's os.Exit() paths — os.Exit never runs
// deferred functions, so run()'s own defer would silently never fire — can
// reach in and run it themselves first.
var pendingAPIBaseOverrideCleanup func()

// runPendingAPIBaseOverrideCleanup runs and clears pendingAPIBaseOverrideCleanup,
// if one is set. Deferred by run() (the ordinary path), and also called
// directly by login_page.go immediately before every os.Exit() in the login
// flow (execLoginRunSuggestion's re-exec of a suggested command, and the
// empty-authorization-code abort): `dci login` under an active
// DCI_API_BASE_URL override writes the newly-acquired OAuth token only into
// applyAPIBaseOverride's temp-dir cache.json, and only this cleanup copies
// it back to the real cache dir — an os.Exit() that skips it silently loses
// the session (login appears to succeed, but the next invocation must
// re-authenticate) and leaks the temp dir on disk forever. Safe to call
// more than once: the second call is a no-op since the first already
// cleared the var.
func runPendingAPIBaseOverrideCleanup() {
	if pendingAPIBaseOverrideCleanup == nil {
		return
	}
	cleanup := pendingAPIBaseOverrideCleanup
	pendingAPIBaseOverrideCleanup = nil
	cleanup()
}

// Returns a cleanup func to defer immediately: it must run even if
// cli.Init() panics, since that's exactly when a stale env var pointing at
// a now-deleted temp dir would otherwise strand the next command.
func applyAPIBaseOverride(realConfigDir string, args []string) (cleanup func()) {
	noop := func() {}
	if isDetachedRefreshInvocation(args) {
		return noop
	}
	envBase := strings.TrimSpace(os.Getenv("DCI_API_BASE_URL"))
	if envBase == "" {
		return noop
	}
	base, err := apiBase()
	if err != nil {
		// A malformed override falls back to the persisted base silently:
		// `status` calls apiBase() itself and will surface this exact error
		// on its own terms for the one command that actually cares, so
		// warning here too would just double it.
		return noop
	}

	tempDir, err := os.MkdirTemp("", "dci-api-base-override-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: unable to apply DCI_API_BASE_URL (%v); using the persisted API base\n", err)
		return noop
	}
	cleanupTempDir := func() { _ = os.RemoveAll(tempDir) }

	// Copy the real apis.json and only patch its "dci.base" field, so any
	// customization already on disk — the TLS block in particular, e.g.
	// "insecure" for a self-signed dev/test host — survives into the
	// isolated copy exactly like swapConfiguredAPIBase used to preserve it
	// in place.
	if err := writeOverriddenAPIsConfig(filepath.Join(realConfigDir, "apis.json"), filepath.Join(tempDir, "apis.json"), base); err != nil {
		fmt.Fprintf(os.Stderr, "warning: unable to apply DCI_API_BASE_URL (%v); using the persisted API base\n", err)
		cleanupTempDir()
		return noop
	}
	// Preserve auth (the cached OAuth token) across the isolated cache dir;
	// its absence just means an extra login prompt, not misrouted traffic,
	// so a copy failure is worth a warning but not worth aborting over.
	// originalCacheData is compared against at cleanup time, to tell "this
	// invocation refreshed the session" from "nothing changed" — see the
	// cleanup closure's own comment for why that distinction matters.
	var originalCacheData []byte
	if cacheData, err := os.ReadFile(filepath.Join(restishCacheDir(), "cache.json")); err == nil {
		originalCacheData = cacheData
		if err := os.WriteFile(filepath.Join(tempDir, "cache.json"), cacheData, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "warning: unable to carry over the cached session for DCI_API_BASE_URL (%v); you may need to log in again\n", err)
		}
	}
	// Preserve the persisted customer context: it's user/tenant selection
	// data, not host-specific, so copying it carries none of the
	// cross-host-leak risk dci.cbor is deliberately excluded to avoid.
	// Without this, `dci ai` tool-call subprocesses (which re-exec this
	// binary and so resolve their own dciConfigDir() — the temp dir, since
	// they inherit DCI_CONFIG_DIR pointed at it) would silently lose the
	// user's selected customer for the whole override session.
	if ctxData, err := os.ReadFile(customerContextPath(realConfigDir)); err == nil {
		if err := os.WriteFile(customerContextPath(tempDir), ctxData, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "warning: unable to carry over the customer context for DCI_API_BASE_URL (%v)\n", err)
		}
	}

	oldConfigDir, hadConfigDir := os.LookupEnv("DCI_CONFIG_DIR")
	oldCacheDir, hadCacheDir := os.LookupEnv("DCI_CACHE_DIR")
	realDCIDirOverrides = &realDCIDirOverridesState{
		configDir: dciDirOverride{value: oldConfigDir, had: hadConfigDir},
		cacheDir:  dciDirOverride{value: oldCacheDir, had: hadCacheDir},
	}
	os.Setenv("DCI_CONFIG_DIR", tempDir)
	os.Setenv("DCI_CACHE_DIR", tempDir)

	return func() {
		// Copy the temp dir's cache.json back to the real cache dir before
		// it's deleted: restish writes a refreshed/new OAuth token there on
		// an access-token refresh or a `dci login` run under the override
		// (DCI_CACHE_DIR points at the temp dir for the whole invocation),
		// and without this that session is silently discarded — the next
		// invocation, override or not, finds the same stale real cache.json
		// and has to re-authenticate despite the login/refresh having
		// appeared to succeed.
		//
		// Only writes back when the temp copy actually changed from what
		// was seeded at the start of this invocation — not unconditionally
		// on every override invocation's cleanup. An unconditional
		// overwrite is a lost-update race: a concurrent plain `dci logout`
		// (no override, real cache dir) in another terminal could clear
		// the real cache.json while this invocation is still in flight, and
		// this cleanup would then silently revive the old session on disk
		// by writing back a snapshot that predates the logout.
		if cacheData, err := os.ReadFile(filepath.Join(tempDir, "cache.json")); err == nil && !bytes.Equal(cacheData, originalCacheData) {
			// oldCacheDir's own emptiness — not hadCacheDir — decides the
			// fallback: restishCacheDir() (used above to seed
			// originalCacheData from the real dir in the first place) and
			// dciConfigDir() both treat an explicitly-empty DCI_CACHE_DIR as
			// unset, falling back to os.UserCacheDir()+"/dci". Branching on
			// hadCacheDir here instead would disagree with that and resolve
			// to "" for a DCI_CACHE_DIR="" invocation — silently skipping
			// the write-back below with no diagnostic at all.
			realCacheDirPath := oldCacheDir
			if realCacheDirPath == "" {
				if userCacheDir, err := os.UserCacheDir(); err == nil {
					realCacheDirPath = filepath.Join(userCacheDir, "dci")
				}
			}
			if realCacheDirPath == "" {
				fmt.Fprintln(os.Stderr, "warning: unable to persist the session refreshed under DCI_API_BASE_URL (could not determine the real cache directory); you may need to log in again")
			} else if err := os.MkdirAll(realCacheDirPath, 0o700); err != nil {
				fmt.Fprintf(os.Stderr, "warning: unable to persist the session refreshed under DCI_API_BASE_URL (%v); you may need to log in again\n", err)
			} else if err := os.WriteFile(filepath.Join(realCacheDirPath, "cache.json"), cacheData, 0o600); err != nil {
				fmt.Fprintf(os.Stderr, "warning: unable to persist the session refreshed under DCI_API_BASE_URL (%v); you may need to log in again\n", err)
			}
		}
		if hadConfigDir {
			os.Setenv("DCI_CONFIG_DIR", oldConfigDir)
		} else {
			os.Unsetenv("DCI_CONFIG_DIR")
		}
		if hadCacheDir {
			os.Setenv("DCI_CACHE_DIR", oldCacheDir)
		} else {
			os.Unsetenv("DCI_CACHE_DIR")
		}
		realDCIDirOverrides = nil
		cleanupTempDir()
	}
}

func writeConfig(configFile, base string) error {
	config := map[string]interface{}{
		"$schema": "https://rest.sh/schemas/apis.json",
		"dci": map[string]interface{}{
			"base": base,
			"profiles": map[string]interface{}{
				"default": map[string]interface{}{
					"auth": map[string]interface{}{
						"name": "oauth-authorization-code",
						"params": map[string]interface{}{
							"authorize_url": "https://console.doit.com/sign-in/oauth",
							"client_id":     "cli",
							"token_url":     "https://console.doit.com/api/auth/token",
						},
					},
				},
			},
			"tls": map[string]interface{}{},
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		return err
	}
	return nil
}

func readConfigBase(configFile string) (string, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return "", err
	}
	var config struct {
		DCI struct {
			Base string `json:"base"`
		} `json:"dci"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return "", err
	}
	base := strings.TrimSpace(config.DCI.Base)
	if base == "" {
		return "", errors.New("dci.base is missing from apis.json")
	}
	return strings.TrimRight(base, "/"), nil
}

func tightenFilePermissions(path string, desired os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	perm := info.Mode().Perm()
	if perm&^desired == 0 {
		return nil
	}

	return os.Chmod(path, desired)
}

func printFirstRunOnboarding(configured bool) {
	// Onboarding is decorative chatter — skip it entirely in agent mode.
	if !configured || agentMode || !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}

	fmt.Fprintln(os.Stderr, "Cloud Intelligence™ CLI is ready.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Next steps:")
	fmt.Fprintln(os.Stderr, "  dci        (open the AI session — ask questions in plain English)")
	fmt.Fprintln(os.Stderr, "  dci status")
	fmt.Fprintln(os.Stderr, "  dci list-budgets")
	fmt.Fprintln(os.Stderr, "  dci --help (list every command)")
	fmt.Fprintln(os.Stderr, "")
}

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	// Reset per-invocation state so repeated calls (e.g. in tests) start clean.
	customerContextFlagValue = ""
	resolvedCustomerContext = ""
	helpFullRequested = false
	requestReportCurrency = ""
	invokedCommandName = ""
	bufferedRequestBody = nil
	configuredAPIBase = ""
	displayTimeLocation = nil
	localizedInstantShown = false
	nonJSONErrorResponse = false
	cachedDCIConfigDir = ""
	realDCIDirOverrides = nil
	resetErrorContractState()
	resetDestructiveContractState()
	resetPathValidationState()
	resetNameResolutionState()

	// Resolve agent mode once up front. Downstream behavior — color, default
	// output format, stderr routing, and the User-Agent mode token — all key off
	// this.
	agentEnvDetected = detectedAgentEnv()
	if v := strings.TrimSpace(os.Getenv("DCI_AGENT_MODE")); v != "" {
		if _, ok := parseBoolish(v); !ok {
			fmt.Fprintf(os.Stderr, "warning: ignoring unrecognized DCI_AGENT_MODE=%q (use 1 or 0)\n", v)
		}
	}
	dec := resolveAgentMode(os.Getenv("DCI_AGENT_MODE"), os.Args, agentEnvDetected, ciEnvDetected(), stdoutIsTTY())
	agentMode = dec.enabled
	agentModeReason = dec.reason
	agentUAMode = dec.mode
	if agentMode {
		// Disable restish/aurora color before cli.Init configures the output
		// writers; it reads this via viper's automatic env binding.
		os.Setenv("NOCOLOR", "1")
	}

	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "dci encountered an internal error: %v\n", r)
			if os.Getenv("DCI_DEBUG_PANIC") == "1" {
				debug.PrintStack()
			}
			exitCode = 1
		}
	}()

	configDir := dciConfigDir()
	configured, err := ensureConfig(configDir)
	if err != nil {
		return reportExecutionError(fmt.Errorf("failed to initialize config: %w", err), 0, configDir)
	}

	// Kick off the update check now so it runs in parallel with the command;
	// the deferred notify covers every return path after command output.
	startUpdateCheck(configDir)
	defer maybeNotifyUpdate(configDir)

	// cli.Init reads apis.json into restish's in-process API config map right
	// away (not cli.Defaults(), despite the name suggesting setup-time
	// config), and that map — not apiBase() — is what cli.Run() consults to
	// pick the base URL for every data command. When DCI_API_BASE_URL is
	// set, applyAPIBaseOverride points DCI_CONFIG_DIR/DCI_CACHE_DIR at a
	// private, per-invocation temp directory seeded with the override
	// before cli.Init() reads it, instead of mutating the real apis.json —
	// see its own doc comment for why. Stashed in pendingAPIBaseOverrideCleanup
	// (deferred via runPendingAPIBaseOverrideCleanup, not called directly)
	// so login_page.go's os.Exit() paths — which a plain defer here would
	// never reach — can invoke it themselves first; see that function's
	// doc comment.
	pendingAPIBaseOverrideCleanup = applyAPIBaseOverride(configDir, os.Args)
	defer runPendingAPIBaseOverrideCleanup()
	cli.Init("dci", version)
	cli.Defaults()
	overrideTableOutput()
	installOutputGuard()
	installResponseGuard()
	installDestructiveActionSummaryGuard()
	registerAgentFlags()
	printFirstRunOnboarding(configured)
	maybeHintAgentMode()

	cli.AddLoader(openapi.New())
	cli.AddAuth("oauth-authorization-code", &authorizationCodeHandler{})

	if err := rejectProfileFlags(os.Args); err != nil {
		return reportExecutionError(err, 0, configDir)
	}
	// Keep profile fixed until we support multi-profile UX.
	os.Setenv("RSH_PROFILE", "default")
	viper.Set("rsh-profile", "default")

	// Hardcode user-agent so the DCI API can identify CLI traffic. It carries a
	// mode=<interactive|agent|noninteractive> token (never the end user) so
	// traffic can be segmented by interface. Restish picks this up via rsh-header
	// and skips its own default.
	viper.Set("rsh-header", []string{"user-agent:" + buildUserAgent(agentUAMode)})

	cli.Load("dci", cli.Root)
	applyAPIKeyAuth()
	brandRootCommand()
	brandDCIRootCommand()
	registerStatusCommands(configDir)
	registerAuthCommands(configDir)
	registerCustomerContextCommands(configDir)
	registerUpdateCommand(configDir)
	registerUpdateRefreshCommand(configDir)
	registerVersionCommand()
	registerDocsCommand()
	registerOpenCommand(configDir)
	registerNameRefreshCommand(configDir)
	registerSkillCommands()
	registerCommandCatalog()
	registerBetaCommands()
	registerAICommand(configDir)
	if cachedTokenIsDoer() {
		for _, command := range cli.Root.Commands() {
			if command.Use == "customer-context" {
				command.Hidden = false
				break
			}
		}
	}
	addOutputFlag()
	hideGlobalFlags()
	customizeDCIUsage()
	applyCustomerContext(configDir)
	lockToDCI(configDir)
	setupCompletion()
	// The API subcommands do not exist yet — restish hydrates them inside
	// cli.Run — so the arity relaxation for space-split resolvable names must
	// re-run once they do. Cobra initializers fire after hydration and flag
	// parsing but before args validation.
	cobra.OnInitialize(relaxResolvableArgsValidation)
	os.Args = rewriteHelpFullFlag(os.Args)
	os.Args = normalizeArgs(os.Args)
	if handled, completionExitCode := completionPreflight(os.Args); handled {
		return completionExitCode
	}
	if err := preflightAPIInvocation(os.Args); err != nil {
		return reportExecutionError(err, 0, configDir)
	}

	if err := executeCLI(); err != nil {
		return reportExecutionError(err, cli.GetLastStatus(), configDir)
	}
	// One exit-code taxonomy for every mode: the same failure maps to the same
	// exit code whether the CLI is driven by a human, an agent, or a script.
	code := exitCodeForProcessStatus(cli.GetLastStatus())
	if responseExitCode != 0 {
		code = responseExitCode
	}
	if code == 0 && nonJSONErrorResponse {
		code = exitServer
	}
	maybeHintDoerContext(code, cli.GetLastStatus(), configDir)
	if code == 0 {
		// Success only: failure stderr must stay a single parseable envelope.
		maybeAgentOnboardingHint(configDir)
	}
	return code
}

func reportExecutionError(err error, status int, configDir string) int {
	if preflightError, ok := err.(invocationPreflightError); ok {
		if agentMode {
			writeStructuredError(os.Stderr, preflightError.StructuredError())
		} else {
			fmt.Fprintln(os.Stderr, preflightError.Error())
		}
		maybeHintDoerContext(preflightError.ExitCode(), status, configDir)
		return preflightError.ExitCode()
	}
	if !agentErrorContractEnabled() {
		code := exitCodeForExecutionError(err, status)
		if code == exitSuccess && isSilentExecutionError(err) {
			return exitSuccess
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		maybeHintDoerContext(code, status, configDir)
		return code
	}
	code := exitCodeForExecutionError(err, status)
	if code == exitSuccess && isSilentExecutionError(err) {
		return exitSuccess
	}
	if !agentErrorWritten {
		writeStructuredError(os.Stderr, structuredErrorForExecution(err, status))
	}
	maybeHintDoerContext(code, status, configDir)
	return code
}

func exitCodeForProcessStatus(status int) int {
	if viper.GetBool("rsh-ignore-status-code") {
		return exitSuccess
	}
	return exitCodeForHTTPStatus(status)
}

func rejectProfileFlags(args []string) error {
	flags := cli.Root.PersistentFlags()

	for _, arg := range args[1:] {
		if arg == "--" {
			// Everything after `--` is a positional operand.
			return nil
		}
		if arg == "--profile" || arg == "--rsh-profile" || strings.HasPrefix(arg, "--profile=") || strings.HasPrefix(arg, "--rsh-profile=") {
			return fmt.Errorf("invalid argument: profile selection is currently disabled")
		}
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || arg == "-" {
			continue
		}

		shorts := strings.TrimPrefix(arg, "-")
		if shorts == "" {
			continue
		}
		if beforeEq, _, ok := strings.Cut(shorts, "="); ok {
			shorts = beforeEq
		}

		for i := 0; i < len(shorts); i++ {
			ch := string(shorts[i])
			if ch == "p" {
				return fmt.Errorf("profile selection is currently disabled")
			}
			flag := flags.ShorthandLookup(ch)
			if flag != nil && !isBoolFlag(flag) {
				// Remaining bytes belong to this flag's value.
				break
			}
		}
	}
	return nil
}

var helpFullRequested bool

func rewriteHelpFullFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--help-full" {
			helpFullRequested = true
			out = append(out, "--help")
			continue
		}
		out = append(out, arg)
	}
	return out
}

func terseHelpText(long string) (string, bool) {
	idx := strings.Index(long, "## ")
	if idx < 0 {
		return long, false
	}
	head := strings.TrimSpace(long[:idx])
	if head == "" {
		head = "(no description)"
	}
	return head + "\n\nSchemas and examples: add --help-full", true
}

func normalizeArgs(args []string) []string {
	if len(args) <= 1 {
		// Bare `dci` reaches the root RunE untouched, which routes on the TUI
		// gate: a human terminal opens the AI session, everything else prints
		// help (AI-DEFAULT-SPEC §3). The old rewrite to --help would bypass
		// that routing inside cobra before the RunE could run.
		return args
	}

	cmd := firstCommandArg(args)
	if cmd == "" || cmd == "help" || cmd == "version" || cmd == "completion" || isRootCommand(cmd) {
		return args
	}

	// __complete and __completeNoDesc are hidden cobra commands invoked by
	// shell completion scripts. The args after them mirror user input and
	// need the same "dci" prefix insertion so cobra resolves completions
	// under the API subcommand.
	if cmd == "__complete" || cmd == "__completeNoDesc" {
		return normalizeCompletionArgs(args, cmd)
	}

	return append([]string{args[0], "dci"}, args[1:]...)
}

// normalizeCompletionArgs inserts "dci" after __complete/__completeNoDesc when
// the completion target is an API command (not a root command). This mirrors
// normalizeArgs so that tab-completion resolves under the API subcommand.
func normalizeCompletionArgs(args []string, completionCmd string) []string {
	// Find the position of __complete/__completeNoDesc.
	idx := -1
	for i, a := range args {
		if a == completionCmd {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(args) {
		return args
	}

	// Only the words before the last one (the word being completed) can commit
	// a command. A partial first word (dci st<Tab>) must stay at root, where
	// cobra's subcommand matching and cli.Root's ValidArgsFunction together
	// offer root commands and API operations in one candidate list.
	preceding := args[idx+1 : len(args)-1]
	command, _, ok := completionPositionalWords(preceding, completionFlagSets()...)
	if !ok || command == "" || command == "help" || isRootCommand(command) {
		return args
	}

	// Insert "dci" after the completion command to route into the API subcommand.
	result := make([]string, 0, len(args)+1)
	result = append(result, args[:idx+1]...)
	result = append(result, "dci")
	result = append(result, args[idx+1:]...)
	return result
}

func firstCommandArg(args []string) string {
	if cli.Root == nil {
		return commandArg(args, 1)
	}
	return commandArg(args, 1, cli.Root.PersistentFlags())
}

func commandArg(args []string, start int, flagSets ...*pflag.FlagSet) string {
	lookupFlag := func(name string) *pflag.Flag {
		for _, flags := range flagSets {
			if flags != nil {
				if flag := flags.Lookup(name); flag != nil {
					return flag
				}
			}
		}
		return nil
	}
	lookupShorthand := func(name string) *pflag.Flag {
		for _, flags := range flagSets {
			if flags != nil {
				if flag := flags.ShorthandLookup(name); flag != nil {
					return flag
				}
			}
		}
		return nil
	}

	for i := start; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return arg
		}

		// Long flag.
		if strings.HasPrefix(arg, "--") {
			name, hasValue := splitLongFlag(arg)
			if name == "" {
				continue
			}
			if hasValue {
				continue
			}
			flag := lookupFlag(name)
			if flag != nil && !isBoolFlag(flag) && i+1 < len(args) {
				i++
			}
			continue
		}

		// Short flag(s), including compact values (e.g. -pfoo).
		shorts := arg[1:]
		for j := 0; j < len(shorts); j++ {
			flag := lookupShorthand(string(shorts[j]))
			if flag == nil {
				continue
			}
			if isBoolFlag(flag) {
				continue
			}
			if j == len(shorts)-1 && i+1 < len(args) {
				i++
			}
			break
		}
	}

	return ""
}

func splitLongFlag(arg string) (name string, hasValue bool) {
	s := strings.TrimPrefix(arg, "--")
	if s == "" {
		return "", false
	}
	if n, _, ok := strings.Cut(s, "="); ok {
		return n, true
	}
	return s, false
}

func isBoolFlag(flag *pflag.Flag) bool {
	if flag == nil || flag.Value == nil {
		return false
	}
	return flag.Value.Type() == "bool"
}

func isRootCommand(name string) bool {
	for _, cmd := range cli.Root.Commands() {
		if cmd.Name() == name {
			return true
		}
		for _, alias := range cmd.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

func hideGlobalFlags() {
	// Keep the flags functional but hide them from help output. The agent-mode
	// flags stay visible so users can discover them.
	cli.Root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "agent" || f.Name == "no-agent" {
			return
		}
		f.Hidden = true
	})
}

// --- Agent mode detection -------------------------------------------------

// agentEnvVars lists environment variables set by known AI coding agents. When
// any is present with a non-empty value, dci treats the session as agent-driven
// unless an explicit override says otherwise. The list is intentionally
// conservative and documented; PRs to extend it are welcome.
var agentEnvVars = []string{
	"CLAUDECODE",      // Claude Code
	"CLAUDE_CODE",     // Claude Code (defensive alias)
	"CURSOR_AGENT",    // Cursor agent
	"KIRO_AGENT",      // Kiro
	"AIDER_SESSION",   // Aider
	"GEMINI_CLI",      // Gemini CLI
	"REPLIT_AGENT",    // Replit Agent
	"WINDSURF_AGENT",  // Windsurf
	"OPENHANDS_AGENT", // OpenHands
	"DEVIN_AGENT",     // Devin
}

type agentModeResult struct {
	enabled bool
	mode    uaMode
	reason  string
}

// resolveAgentMode decides whether agent mode is active, following the
// documented precedence:
//  1. DCI_AGENT_MODE env var (explicit override, always wins)
//  2. --agent / --no-agent flags (explicit, per-invocation)
//  3. a known agent env var (heuristic)
//  4. non-TTY stdout (soft signal: pipe/redirect)
//
// Inputs are passed explicitly so the logic stays easy to test.
func resolveAgentMode(dciAgentMode string, args []string, agentEnv string, ciEnv, stdoutTTY bool) agentModeResult {
	if v := strings.TrimSpace(dciAgentMode); v != "" {
		// Only recognized boolean tokens are decisive. An unrecognized value
		// (e.g. DCI_AGENT_MODE=2) is ignored so a typo can't silently force a
		// mode; run() warns about it separately.
		if b, ok := parseBoolish(v); ok {
			if b {
				return agentModeResult{enabled: true, mode: uaModeAgent, reason: "DCI_AGENT_MODE override"}
			}
			return agentModeResult{enabled: false, mode: uaModeInteractive, reason: "DCI_AGENT_MODE override"}
		}
	}
	switch agentFlagOverride(args) {
	case 1:
		return agentModeResult{enabled: true, mode: uaModeAgent, reason: "--agent/--no-agent flag"}
	case -1:
		return agentModeResult{enabled: false, mode: uaModeInteractive, reason: "--agent/--no-agent flag"}
	}
	if agentEnv != "" {
		return agentModeResult{enabled: true, mode: uaModeAgent, reason: "agent env var " + agentEnv}
	}
	if ciEnv {
		// CI systems sometimes allocate a PTY, which would otherwise open the
		// bare-`dci` AI session and block on input (AI-DEFAULT-SPEC §7). Same
		// behavior and UA classification as the non-TTY soft signal.
		return agentModeResult{enabled: true, mode: uaModeNonInteractive, reason: "CI env var"}
	}
	if !stdoutTTY {
		// Non-TTY is a soft signal (pipe/redirect/CI), not a confirmed agent, so
		// it gets its own UA classification even though behavior matches agent mode.
		return agentModeResult{enabled: true, mode: uaModeNonInteractive, reason: "non-TTY stdout"}
	}
	return agentModeResult{enabled: false, mode: uaModeInteractive, reason: "interactive terminal"}
}

// ciEnvDetected reports whether a CI environment declared itself via the
// near-universal CI variable (GitHub Actions, GitLab, CircleCI, Travis, …
// all set CI=true). Only boolean-ish values count, so CI=<vendor name>
// oddities don't force the mode.
func ciEnvDetected() bool {
	on, ok := parseBoolish(os.Getenv("CI"))
	return ok && on
}

// detectedAgentEnv returns the name of the first agent env var found, or "".
func detectedAgentEnv() string {
	for _, name := range agentEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return name
		}
	}
	return ""
}

func stdoutIsTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func heatmapEnabled(requested, agent, terminal, noColor bool) bool {
	return requested && !agent && terminal && !noColor
}

// sessionRenderActive reports that a human is watching this output through
// the dci ai session's transcript: the process is piped (no TTY), but the
// rendering should stay human-shaped — compact hints, colored tables — the
// way an interactive terminal would show it. Set by the session's dispatcher
// (ai_tui.go), never by users.
func sessionRenderActive() bool {
	on, valid := parseBoolish(os.Getenv("DCI_SESSION_RENDER"))
	return valid && on
}

// agentFlagOverride scans args for the explicit --agent / --no-agent flags and
// returns +1 for agent mode, -1 for human mode, or 0 for no override. These are
// two independent pflag bool flags, so this scan mirrors pflag's own semantics:
// each flag's last occurrence wins, a bare flag means true, and the value (when
// present) is parsed with strconv.ParseBool exactly as pflag does. Crucially, an
// explicit false (--agent=false / --no-agent=0) just leaves that flag unset
// rather than forcing the opposite mode — only a flag set to true is an
// override. If both end up true, the most recently enabled one wins. Scanning
// stops at the "--" operand terminator. Keeping this in step with pflag matters
// because the result drives side effects (color, default output, User-Agent)
// before cobra parses the flags itself.
func agentFlagOverride(args []string) int {
	agent, noAgent := false, false
	last := 0 // +1 if --agent was most recently enabled, -1 if --no-agent was
	for _, a := range args {
		if a == "--" {
			break
		}
		name, val, hasVal := strings.Cut(a, "=")
		if name != "--agent" && name != "--no-agent" {
			continue
		}
		enabled := true
		if hasVal {
			b, err := strconv.ParseBool(val)
			if err != nil {
				continue // let cobra surface the parse error later
			}
			enabled = b
		}
		switch name {
		case "--agent":
			agent = enabled
		case "--no-agent":
			noAgent = enabled
		}
		if enabled {
			if name == "--agent" {
				last = 1
			} else {
				last = -1
			}
		}
	}
	switch {
	case agent && noAgent:
		return last // conflicting flags: most recently enabled wins
	case agent:
		return 1
	case noAgent:
		return -1
	default:
		return 0
	}
}

// parseBoolish interprets a boolean-ish env var value (DCI_AGENT_MODE),
// case-insensitively and forgivingly: it accepts the strconv.ParseBool tokens
// plus common aliases (yes/no, on/off, y/n). Returns (value, true) for a
// recognized token and (false, false) otherwise. It is intentionally more
// lenient than the flag parsing, which mirrors pflag exactly.
func parseBoolish(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "yes", "y", "on":
		return true, true
	case "0", "f", "false", "no", "n", "off":
		return false, true
	}
	return false, false
}

// buildUserAgent returns the User-Agent header value identifying CLI traffic to
// the DCI API. It always carries a mode=<interactive|agent|noninteractive>
// token so API traffic can be segmented by interface in analytics. The token
// reflects only how dci was driven, never the end user, so the value stays a
// stable client identifier.
func buildUserAgent(mode uaMode) string {
	if mode == "" {
		mode = uaModeInteractive
	}
	return fmt.Sprintf("dci-cli/%s (%s; %s/%s; mode=%s)", version, runtime.Version(), runtime.GOOS, runtime.GOARCH, mode)
}

// defaultOutputFormat is the output format used when --output is not given.
// Humans get the table view; agents get TOON — compact, token-efficient, and
// parse-friendly.
func defaultOutputFormat() string {
	if agentMode {
		return "toon"
	}
	return "table"
}

// registerAgentFlags adds the global --agent / --no-agent flags so cobra accepts
// them. The actual decision is made earlier in run() by scanning os.Args, since
// it must be live before any HTTP call (User-Agent) and before cli.Init (color).
func registerAgentFlags() {
	pf := cli.Root.PersistentFlags()
	if pf.Lookup("agent") == nil {
		pf.Bool("agent", false, "Force agent mode: compact TOON, no color, chatter to stderr")
	}
	if pf.Lookup("no-agent") == nil {
		pf.Bool("no-agent", false, "Force human mode even when an agent is auto-detected")
	}
}

// maybeHintAgentMode nudges a misclassified agent toward the optimized path:
// when an agent env var is present but agent mode is off (the caller opted out
// via --no-agent or DCI_AGENT_MODE=0), emit a one-line tip on stderr so stdout
// stays parseable.
func maybeHintAgentMode() {
	if agentMode || agentEnvDetected == "" {
		return
	}
	fmt.Fprintln(os.Stderr, "Tip: set DCI_AGENT_MODE=1 (or pass --agent) for compact, parse-friendly output.")
}

// maybeAgentOnboardingHint prints a one-time stderr pointer when an agent
// environment is first seen with this config dir, so agents discover the
// embedded skill, the machine-readable catalog, and the docs without being
// told. Stderr only — stdout must stay parseable — and marker-gated so it
// never becomes per-command chatter.
func maybeAgentOnboardingHint(configDir string) {
	if !agentMode || agentEnvDetected == "" {
		return
	}
	marker := filepath.Join(configDir, "agent_onboarding_shown")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return // cannot persist the marker; stay silent rather than repeat forever
	}
	fmt.Fprintln(os.Stderr, "Agent mode is active. Useful entry points:")
	fmt.Fprintln(os.Stderr, "  dci skill <agent>    install CLI usage guidance for this agent (claude, codex, cursor, gemini, kiro, opencode)")
	fmt.Fprintln(os.Stderr, "  dci commands --json  machine-readable command catalog (args, flags, destructive metadata)")
	fmt.Fprintln(os.Stderr, "  dci docs             documentation entry points, incl. https://help.doit.com/llms.txt")
}

const dciUsageTemplate = `Usage:{{if .Runnable}}
  {{.Use}}{{if .HasAvailableFlags}} [flags]{{end}}{{end}}{{if .HasAvailableSubCommands}}
  dci [command]
  dci [command] --help{{else}}
  {{.Use}} --help{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}{{if hasVisibleCommandsInGroup $cmds $group.ID}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if or .HasAvailableLocalFlags .HasAvailableInheritedFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{if .HasAvailableInheritedFlags}}
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}
`

const dciLongDescription = "Command-line interface for the Cloud Intelligence™ API.\n\n" +
	"Run `dci` with no arguments at a terminal to open the interactive AI session\n" +
	"(ask questions in plain English; /default help restores this screen instead).\n" +
	"In pipes, scripts, and CI, bare `dci` prints this help.\n\n" +
	"Documentation: https://help.doit.com/docs/cli or run `dci docs` for every entry point.\n" +
	"AI agents: `dci skill <agent>` installs usage guidance; `dci commands --json` prints the machine-readable catalog."

var rootExamples = []string{
	"  dci        (interactive AI session)",
	"  dci status",
	"  dci list-budgets",
	"  dci list-reports --output table",
}

var apiExamples = []string{
	"  dci list-budgets",
	"  dci list-reports --output table",
	"  dci query <query.json",
}

func findDCICommand() *cobra.Command {
	for _, cmd := range cli.Root.Commands() {
		if cmd.Name() == "dci" {
			return cmd
		}
	}
	return nil
}

func customizeDCIUsage() {
	cobra.AddTemplateFunc("hasVisibleCommandsInGroup", func(cmds []*cobra.Command, groupID string) bool {
		for _, cmd := range cmds {
			if cmd.GroupID == groupID && (cmd.IsAvailableCommand() || cmd.Name() == "help") {
				return true
			}
		}
		return false
	})

	dciCmd := findDCICommand()
	if dciCmd == nil {
		return
	}

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.SetUsageTemplate(dciUsageTemplate)
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(dciCmd)
}

func applyCommandBranding(cmd *cobra.Command, short string, examples []string) {
	if cmd == nil {
		return
	}
	cmd.Short = short
	cmd.Long = dciLongDescription
	cmd.Example = strings.Join(examples, "\n")
}

func brandRootCommand() {
	applyCommandBranding(cli.Root, "Cloud Intelligence™ CLI", rootExamples)
	cli.Root.SetUsageTemplate(dciUsageTemplate)
}

func lockToDCI(configDir string) {
	cli.Root.Args = cobra.NoArgs
	cli.Root.Run = nil
	// Bare `dci` opens the AI session for a human at a terminal; every other
	// caller — pipes, CI, agents, TERM=dumb, DCI_NO_TUI=1 — keeps help,
	// byte-identical to before, guarded by the same tuiActive() gate the rest
	// of the TUI uses (AI-DEFAULT-SPEC §2–3). --help never reaches this RunE:
	// cobra resolves the help flag first. The persisted opt-out ({"default":
	// "help"} in ai_settings.json, set by /default) restores help for humans
	// whose muscle memory wants the usage screen.
	cli.Root.RunE = func(cmd *cobra.Command, args []string) error {
		if tuiActive() && aiDefaultEnabled(configDir) {
			return launchAISession(configDir)
		}
		return cmd.Help()
	}
	cli.Root.ValidArgsFunction = nil

	// Remove API management commands, generic RESTish commands, and any
	// additional API entrypoints so users can only call the DCI API.
	allowed := map[string]bool{
		"completion": true,
		"dci":        true,
		"help":       true,
		"login":      true,
		"logout":     true,
	}
	toRemove := make([]*cobra.Command, 0)
	for _, cmd := range cli.Root.Commands() {
		if allowed[cmd.Name()] {
			continue
		}

		if cmd.Name() == "api" || cmd.GroupID == "generic" || (cmd.GroupID == "api" && cmd.Name() != "dci") {
			toRemove = append(toRemove, cmd)
		}
	}
	for _, cmd := range toRemove {
		cli.Root.RemoveCommand(cmd)
	}
}

// setupCompletion configures shell completion and root help so that API
// commands appear at root level (alongside status, login, etc.).
//
// The "dci" API subcommand is hidden since users access its commands directly
// via normalizeArgs. Its ValidArgsFunction (which returns URL paths from
// restish) is cleared so completions show command names instead.
//
// Restish lazily loads API operations inside cli.Run() by inspecting os.Args
// for the API name. For root-level completion and help, restish skips loading
// because __complete and --help args are filtered out. We work around this by
// triggering the load on demand.
func setupCompletion() {
	var dciCmd *cobra.Command
	for _, cmd := range cli.Root.Commands() {
		if cmd.Name() == "dci" {
			dciCmd = cmd
			break
		}
	}
	if dciCmd == nil {
		return
	}

	// Hide the "dci" namespace — users interact with API commands at root level.
	dciCmd.Hidden = true

	// Clear restish's ValidArgsFunction that returns URL paths.
	dciCmd.ValidArgsFunction = nil

	// loadAPI triggers lazy API loading into the dci subcommand. Restish's
	// cli.Run() normally does this by parsing os.Args, but --help and
	// __complete are filtered out so we must load explicitly.
	//
	// To avoid triggering OAuth when no auth is cached, we only call
	// cli.Load when restish's API cache file exists. If it doesn't, the
	// user hasn't authenticated yet and API commands won't be shown until
	// they run "dci login".
	var apiLoaded bool
	loadAPI := func() {
		if apiLoaded {
			return
		}
		apiLoaded = true
		cacheDir, _ := os.UserCacheDir()
		cacheFile := filepath.Join(cacheDir, "dci", "dci.cbor")
		if _, err := os.Stat(cacheFile); err != nil {
			return
		}
		base, err := apiBase()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return
		}
		cli.Load(base, dciCmd)
	}

	// Surface API subcommands in root-level completion so "dci <Tab>"
	// shows list-budgets, list-reports, etc. alongside status, login, etc.
	cli.Root.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		loadAPI()
		var completions []string
		for _, sub := range dciCmd.Commands() {
			if sub.Hidden {
				continue
			}
			if strings.HasPrefix(sub.Name(), toComplete) {
				completions = append(completions, sub.Name()+"\t"+sub.Short)
			}
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	}

	// Override root help to include API commands. Load the API, move its
	// commands to root so the standard usage template renders them, then
	// show help normally.
	defaultHelp := cli.Root.HelpFunc()
	cli.Root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		sanitizeFlagPlaceholders(cmd)
		augmentVerifiedFlagHelp(cmd)
		if !helpFullRequested {
			if terse, truncated := terseHelpText(cmd.Long); truncated {
				originalLong := cmd.Long
				cmd.Long = terse
				defer func() { cmd.Long = originalLong }()
			}
		}
		hasAPICommands := false
		if cmd == cli.Root {
			loadAPI()
			hasAPICommands = len(dciCmd.Commands()) > 0
			// Copy command groups from the API subcommand to root so the
			// usage template can render grouped commands.
			for _, g := range dciCmd.Groups() {
				if !cli.Root.ContainsGroup(g.ID) {
					cli.Root.AddGroup(g)
				}
			}
			// Collect first — iterating Commands() while removing mutates the slice.
			subs := make([]*cobra.Command, len(dciCmd.Commands()))
			copy(subs, dciCmd.Commands())
			for _, sub := range subs {
				dciCmd.RemoveCommand(sub)
				cli.Root.AddCommand(sub)
			}
		}
		defaultHelp(cmd, args)
		if cmd == cli.Root && !hasAPICommands {
			hint := "\n! To get started, authenticate with: dci login (or set DCI_API_KEY)\n\n"
			// In agent mode the hint is chatter — route it to stderr (plain, no
			// color) so stdout stays parseable.
			if agentMode {
				fmt.Fprint(os.Stderr, hint)
			} else {
				if term.IsTerminal(int(os.Stdout.Fd())) {
					hint = "\n\033[1;33m!\033[0m To get started, authenticate with: \033[1mdci login\033[0m (or set \033[1mDCI_API_KEY\033[0m)\n\n"
				}
				fmt.Fprint(os.Stdout, hint)
			}
		}
	})
}

// augmentVerifiedFlagHelp appends behavior verified against the live API to
// spec-generated flag descriptions that undersell or omit it. Kept minimal and
// per-command: each entry documents only what was actually probed.
func augmentVerifiedFlagHelp(cmd *cobra.Command) {
	augment := func(flagName, note string) {
		flag := cmd.LocalFlags().Lookup(flagName)
		if flag == nil || strings.Contains(flag.Usage, note) {
			return
		}
		flag.Usage = strings.TrimRight(flag.Usage, " \n") + "\n" + note
	}
	switch cmd.Name() {
	case "list-dimensions":
		augment("filter", "Syntax: a single field:value term matched exactly (e.g. type:system_label, label:team). Globs, substrings, and multi-term expressions are not supported; unrecognized expressions are silently ignored and return the unfiltered listing. For substring search across the whole collection use --search instead.")
	}
}

// sanitizeFlagPlaceholders strips backticks from generated flag descriptions.
// pflag renders a backticked word as the flag's value placeholder, so spec
// descriptions like "together with `startDate`" would render as
// "--end-date startDate" — the wrong flag's name in the placeholder position.
func sanitizeFlagPlaceholders(cmd *cobra.Command) {
	sanitize := func(flag *pflag.Flag) {
		if strings.Count(flag.Usage, "`") >= 2 {
			flag.Usage = strings.ReplaceAll(flag.Usage, "`", "")
		}
	}
	cmd.LocalFlags().VisitAll(sanitize)
	cmd.InheritedFlags().VisitAll(sanitize)
}

func registerCustomerContextCommands(configDir string) {
	cmd := &cobra.Command{
		Use:    "customer-context",
		Short:  "Manage default customerContext for requests",
		Hidden: true,
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "set TOKEN",
		Short: "Set the default customerContext",
		Long: "Set the default customerContext applied to every request.\n\n" +
			"TOKEN can be a customer domain (acme.com), a customer ID, or a customer\n" +
			"URL display name as shown in the DoiT Console URL (e.g. acme).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token := strings.TrimSpace(args[0])
			if err := validateCustomerContextValue(token); err != nil {
				return err
			}
			if err := os.WriteFile(customerContextPath(configDir), []byte(token+"\n"), 0o600); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "customerContext saved")
			fmt.Fprintln(os.Stderr, "Note: the context is applied to every subsequent command. Verify access with: dci validate")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear the default customerContext",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := os.Remove(customerContextPath(configDir)); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintln(os.Stdout, "customerContext cleared")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show the current default customerContext",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ctx := readCustomerContext(configDir); ctx != "" {
				fmt.Fprintln(os.Stdout, ctx)
			} else {
				fmt.Fprintln(os.Stdout, "customerContext not set")
			}
			return nil
		},
	})

	cli.Root.AddCommand(cmd)
}

// customerSlugPattern matches customer URL display names: 3-12 chars,
// lowercase letters/digits/dashes, starting and ending with a letter or
// digit. Mirrors omni's services/customers/pkg/domain/requests.go.
var customerSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,10}[a-z0-9]$`)

// validateCustomerContextValue applies syntactic checks before persisting a
// customer context: a bad value silently breaks every subsequent command with
// a 403, so obvious mistakes are rejected at set time.
func validateCustomerContextValue(token string) error {
	if token == "" {
		return fmt.Errorf("customerContext cannot be empty")
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return fmt.Errorf("customerContext cannot contain whitespace")
	}
	// Valid contexts are customer domains (acme.com), customer IDs (20-char
	// alphanumeric), or customer URL display names (short lowercase slugs).
	// Anything matching none of those shapes is almost certainly a typo.
	if !strings.Contains(token, ".") && len(token) < 8 && !customerSlugPattern.MatchString(token) {
		return fmt.Errorf("customerContext %q does not look like a customer domain (e.g. acme.com), customer ID, or URL display name", token)
	}
	return nil
}

func brandDCIRootCommand() {
	applyCommandBranding(findDCICommand(), "Cloud Intelligence™ API CLI", apiExamples)
}

func registerStatusCommands(configDir string) {
	currentOutput := func() string {
		output := strings.TrimSpace(viper.GetString("rsh-output-format"))
		if output == "" || output == "auto" {
			output = defaultOutputFormat()
		}
		return output
	}

	renderStatus := func(cmd *cobra.Command, args []string) error {
		ctx := readCustomerContext(configDir)

		base, err := apiBase()
		if err != nil {
			return err
		}

		_, _ = fmt.Fprintln(os.Stdout, "Cloud Intelligence™")
		if os.Getenv("DCI_API_BASE_URL") != "" {
			fmt.Fprintf(os.Stdout, "API Base: %s (DCI_API_BASE_URL)\n", base)
		} else {
			fmt.Fprintf(os.Stdout, "API Base: %s\n", base)
		}
		fmt.Fprintf(os.Stdout, "Auth: %s\n", authSource())
		switch {
		case os.Getenv("DCI_API_KEY") != "":
			_, _ = fmt.Fprintln(os.Stdout, "Session: API key set (verify identity and permissions with: dci validate)")
		case cli.Cache != nil && cli.Cache.GetString("dci:default.token") != "":
			_, _ = fmt.Fprintln(os.Stdout, "Session: cached OAuth token (verify with: dci validate)")
		default:
			_, _ = fmt.Fprintln(os.Stdout, "Session: not authenticated (run: dci login, or set DCI_API_KEY)")
		}
		fmt.Fprintf(os.Stdout, "Default Output: %s\n", currentOutput())
		if agentMode {
			fmt.Fprintf(os.Stdout, "Agent Mode: on (%s)\n", agentModeReason)
		} else {
			fmt.Fprintf(os.Stdout, "Agent Mode: off (%s)\n", agentModeReason)
		}
		fmt.Fprintf(os.Stdout, "Config Dir: %s\n", configDir)
		if ctx != "" {
			if os.Getenv("DCI_CUSTOMER_CONTEXT") != "" {
				fmt.Fprintf(os.Stdout, "Customer context: %s (DCI_CUSTOMER_CONTEXT)\n", ctx)
			} else {
				fmt.Fprintf(os.Stdout, "Customer context: %s\n", ctx)
			}
		}
		return nil
	}

	cli.Root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show DoiT CLI configuration and active context",
		Args:  cobra.NoArgs,
		RunE:  renderStatus,
	})
}

func registerVersionCommand() {
	cli.Root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the DCI CLI version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", cmd.Root().Name(), version)
		},
	})
}

// applyAPIKeyAuth injects DCI_API_KEY into restish's auth cache as a Bearer
// token. Restish's OAuth TokenHandler checks the cache before triggering a
// browser flow, so pre-populating it bypasses interactive login. We use
// cli.Cache (an exported *viper.Viper) because restish does not export its
// config internals (the configs map and apis viper instance are private).
func applyAPIKeyAuth() {
	apiKey := os.Getenv(apiKeyEnvName)
	if apiKey == "" {
		return
	}

	profile := viper.GetString("rsh-profile")
	key := "dci:" + profile
	cli.Cache.Set(key+".token", apiKey)
	cli.Cache.Set(key+".type", "Bearer")
	cli.Cache.Set(key+".expires", "9999-12-31T23:59:59Z")
	cli.Cache.Set(key+".refresh", "")
}

func authSource() string {
	if os.Getenv(apiKeyEnvName) != "" {
		return "API key (DCI_API_KEY)"
	}
	return "OAuth (DoiT Console)"
}

// maybeHintDoerContext prints a targeted hint when a @doit.com user hits a 403
// without a customer context set — covering both interactive and CI/CD usage.
// status is the HTTP status code from the last request (pass cli.GetLastStatus()).
func maybeHintDoerContext(exitCode int, status int, configDir string) {
	if agentErrorContractEnabled() || exitCode == 0 || (status != 401 && status != 403) {
		return
	}
	if !cachedTokenIsDoer() {
		return
	}
	if readCustomerContext(configDir) != "" || customerContextFlagValue != "" {
		return
	}
	if term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "\033[1;33m!\033[0m DoiT employees need a customer context for API calls.\n")
		fmt.Fprintf(os.Stderr, "  Interactive:  \033[1mdci customer-context set doit.com\033[0m\n")
		fmt.Fprintf(os.Stderr, "  CI/scripts:   \033[1mexport DCI_CUSTOMER_CONTEXT=doit.com\033[0m\n")
		fmt.Fprintln(os.Stderr, "")
	} else {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "! DoiT employees need a customer context for API calls.")
		fmt.Fprintln(os.Stderr, "  Interactive:  dci customer-context set doit.com")
		fmt.Fprintln(os.Stderr, "  CI/scripts:   export DCI_CUSTOMER_CONTEXT=doit.com")
		fmt.Fprintln(os.Stderr, "")
	}
}

// applyDoerContext auto-configures the customer context to "doit.com" for
// @doit.com accounts that haven't set one yet. The validate endpoint requires
// customerContext for DoiT employees; calling this after the OAuth token is
// cached fixes the chicken-and-egg problem on first login. Returns true if the
// context was written so the caller can clear a 403 error from validate.
func applyDoerContext(configDir string) bool {
	if !cachedTokenIsDoer() {
		return false
	}
	if readCustomerContext(configDir) != "" {
		return false // already configured, don't overwrite
	}
	err := os.WriteFile(customerContextPath(configDir), []byte("doit.com\n"), 0o600)
	if err != nil {
		return false
	}
	// Presentation lives with the caller (announceLoginSuccess): this helper
	// only mutates the context, so the styled login confirmation is not
	// duplicated by helper output.
	return true
}

// cachedTokenIsDoer reports whether the cached OAuth JWT contains
// DoitEmployee: true. This is more reliable than email-domain matching because
// it is an explicit claim set by the DoiT auth server and is domain-independent.
// Returns false if the cache is empty, the token is absent, or the JWT is malformed.
func cachedTokenIsDoer() bool {
	claims, ok := cachedTokenClaims()
	return ok && claims.DoitEmployee
}

// dciTokenClaims are the JWT payload fields dci reads: the doer marker and
// the signed-in identity (shown in the ai session's banner).
type dciTokenClaims struct {
	DoitEmployee bool   `json:"DoitEmployee"`
	Email        string `json:"email"`
}

func cachedTokenClaims() (dciTokenClaims, bool) {
	if cli.Cache == nil {
		return dciTokenClaims{}, false
	}
	return parseTokenClaims(cli.Cache.GetString("dci:default.token"))
}

func parseTokenClaims(token string) (dciTokenClaims, bool) {
	if token == "" {
		return dciTokenClaims{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return dciTokenClaims{}, false
	}
	// JWT payload is base64url-encoded without padding.
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return dciTokenClaims{}, false
	}
	var claims dciTokenClaims
	if err := json.Unmarshal(b, &claims); err != nil {
		return dciTokenClaims{}, false
	}
	return claims, true
}

func registerAuthCommands(configDir string) {
	cli.Root.AddCommand(&cobra.Command{
		Use:     "login",
		Aliases: []string{"auth", "init"},
		Short:   "Authenticate with the DoiT Console",
		Long:    "Opens a browser window to sign in via the DoiT Console. Credentials are cached locally for subsequent commands.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv(apiKeyEnvName) != "" {
				return fmt.Errorf("login is not needed when DCI_API_KEY is set")
			}
			// Only the login command offers click-to-run on the success page:
			// other commands that happen to trigger the browser flow must not
			// linger after their own output (issue #88).
			armLoginRunOffer()
			// Trigger the OAuth flow via validate. This call is internal (login
			// only needs the cached token), so isolate all its observable effects
			// — stdout, guard stderr, guard exit-code flag — from the login result.
			os.Args = []string{os.Args[0], "dci", "validate"}
			oldOut, oldErr := cli.Stdout, cli.Stderr
			cli.Stdout, cli.Stderr = io.Discard, io.Discard
			stopSpinner := startTUISpinner("Waiting for browser sign-in… (Ctrl-C to cancel)")
			err := cli.Run()
			stopSpinner()
			cli.Stdout, cli.Stderr = oldOut, oldErr
			nonJSONErrorResponse = false

			// Auto-configure DoiT employees who have no customer context set.
			// The validate endpoint requires customerContext for @doit.com accounts,
			// causing a 403 on first login before any context is configured. The OAuth
			// token exchange succeeds (token is cached) even when validate returns 403,
			// so we can inspect the token here and fix the chicken-and-egg problem.
			doerConfigured := applyDoerContext(configDir)
			if doerConfigured {
				err = nil
				acceptDoerLoginValidation()
			}

			if err != nil {
				return err
			}
			announceLoginSuccess(configDir, doerConfigured)
			maybeWaitForRunClick()
			return nil
		},
	})

	cli.Root.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Clear stored authentication credentials",
		Long:  "Removes cached OAuth tokens. You will need to sign in again on the next API call.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv(apiKeyEnvName) != "" {
				return fmt.Errorf("logout has no effect when DCI_API_KEY is set; unset the environment variable instead")
			}
			profile := viper.GetString("rsh-profile")
			key := "dci:" + profile
			cli.Cache.Set(key+".token", "")
			cli.Cache.Set(key+".refresh", "")
			cli.Cache.Set(key+".type", "")
			cli.Cache.Set(key+".expires", nil)
			if err := cli.Cache.WriteConfig(); err != nil {
				return fmt.Errorf("failed to clear credentials: %w", err)
			}
			if err := purgeNameCaches(configDir); err != nil {
				fmt.Fprintf(os.Stderr, "warning: unable to clear cached resource names: %v\n", err)
			}
			fmt.Fprintln(os.Stdout, "Logged out. Credentials cleared.")
			return nil
		},
	})
}

func acceptDoerLoginValidation() {
	responseExitCode = 0
	agentErrorWritten = false
	viper.Set("rsh-ignore-status-code", true)
}

// customerContextPath returns the path to the custom file that stores the
// default customer context. We use a dedicated file instead of restish's
// apis.json profile query params because restish's config internals are
// private — there is no exported API to read/write profile settings
// programmatically, and writing apis.json directly risks conflicts with
// restish's in-memory config state.
func customerContextPath(configDir string) string {
	return filepath.Join(configDir, "customer_context")
}

func readCustomerContext(configDir string) string {
	if ctx := os.Getenv("DCI_CUSTOMER_CONTEXT"); ctx != "" {
		return strings.TrimSpace(ctx)
	}
	data, err := os.ReadFile(customerContextPath(configDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// The customer context is sent on BOTH transports during the API migration:
// the legacy customerContext query parameter (the only one api.doit.com reads
// today — dropping it broke every context-scoped request in v1.5.0) and the
// X-Tenant-Id header the API is moving to. Keep both until the API confirms
// header support, then drop the query param.
const (
	// tenantIDHeaderPrefix is the header (in restish's "name:value"
	// rsh-header format) that carries the customer context to the API.
	tenantIDHeaderPrefix = "X-Tenant-Id:"
	// customerContextQueryPrefix is the legacy rsh-query entry ("name=value")
	// carrying the same context.
	customerContextQueryPrefix = "customerContext="
)

// isTenantHeader reports whether the rsh-header entry h carries the tenant
// header. HTTP header names are case-insensitive, and users can inject
// arbitrary headers via the hidden but functional -H/--rsh-header flag
// (restish re-parses it inside cli.Run(), after our setup), so a lowercase
// x-tenant-id entry must match or we'd send two tenant headers.
func isTenantHeader(h string) bool {
	return len(h) >= len(tenantIDHeaderPrefix) &&
		strings.EqualFold(h[:len(tenantIDHeaderPrefix)], tenantIDHeaderPrefix)
}

func applyCustomerContext(configDir string) {
	ctx := readCustomerContext(configDir)
	resolvedCustomerContext = ctx
	if ctx == "" {
		return
	}

	// All-or-nothing: if either transport already carries a context, leave
	// both untouched. Filling only the missing one from the file/env could
	// send two different tenants on the two transports. (Not reachable today
	// — rsh-header is hard-set to just our user-agent right before this runs
	// and nothing has set rsh-query — but the guard keeps injection safe
	// against ordering changes.)
	headers := viper.GetStringSlice("rsh-header")
	for _, h := range headers {
		if isTenantHeader(h) {
			return
		}
	}
	queries := viper.GetStringSlice("rsh-query")
	for _, q := range queries {
		if strings.HasPrefix(q, customerContextQueryPrefix) {
			return
		}
	}

	viper.Set("rsh-header", append(headers, tenantIDHeaderPrefix+ctx))
	viper.Set("rsh-query", append(queries, customerContextQueryPrefix+ctx))
}

func addOutputFlag() {
	dciCmd := findDCICommand()
	if dciCmd == nil {
		return
	}

	dciCmd.PersistentFlags().String("output", "", "Output format: table, json, yaml, csv, auto, toon (default: table, or toon in agent mode). toon is compact and token-efficient — good for LLM agents.")
	dciCmd.PersistentFlags().StringP("table-mode", "M", "fit", "Table rendering: fit (truncate), wrap (multi-line), or interactive (full-screen scrollable viewer; falls back to fit outside a terminal)")
	dciCmd.PersistentFlags().StringP("table-columns", "C", "", "Comma-separated list of columns to include in table/toon output (default: all)")
	dciCmd.PersistentFlags().IntP("table-width", "W", 0, "Table width in columns (default: auto-detect terminal width)")
	dciCmd.PersistentFlags().IntP("table-max-col-width", "X", 0, "Maximum width per column when fitting or wrapping (0 = auto)")
	dciCmd.PersistentFlags().StringP("customer-context", "D", "", "Override the active customer context for this command: a customer domain, ID, or URL display name (e.g. acme.com)")
	dciCmd.PersistentFlags().String("fields", "", "Comma-separated response fields to include")
	dciCmd.PersistentFlags().String("exclude", "", "Comma-separated top-level fields to exclude from response items or wrappers")
	dciCmd.PersistentFlags().Bool("full", false, "Return the full response without agent-oriented truncation")
	dciCmd.PersistentFlags().Bool("no-truncate", false, "Disable long-value truncation")
	dciCmd.PersistentFlags().Bool("yes", false, "Confirm a destructive operation")
	dciCmd.PersistentFlags().Bool("dry-run", false, "Preview a destructive operation without executing it")
	dciCmd.PersistentFlags().Int("max-rows", -1, "Maximum report/query result rows to output (default: 500 in agent mode, unlimited otherwise; 0 = unlimited). Does not affect list commands, which page server-side: use --all, --max-results, or --page-token")
	dciCmd.PersistentFlags().String("rows", "", "Report row encoding: positional (default) or keyed (schema-named objects)")
	dciCmd.PersistentFlags().Bool("pivot", false, "Force the pivot report view (groups as rows, time periods as columns, with totals) for any output format or mode")
	dciCmd.PersistentFlags().Bool("flat", false, "Render report results as flat rows instead of the default interactive pivot view")
	dciCmd.PersistentFlags().Bool("include-empty-rows", false, "Keep null-group, zero-metric report rows (dropped by default)")
	dciCmd.PersistentFlags().Bool("drop-unlabeled-rows", false, "Drop report rows whose grouped label dimensions are all null or [Value N/A], regardless of cost — the bucket aggregating all unlabeled spend when grouping by sparse labels")
	dciCmd.PersistentFlags().Bool("include-dismissed", false, "Keep dismissed insights in list-insights results (excluded by default)")
	dciCmd.PersistentFlags().String("rollup", "", "Aggregate report rows client-side: comma-separated result columns to group by; numeric metric columns are summed, all other columns (including per-period timestamps) are dropped (e.g. --rollup service_description totals a monthly result per service)")
	dciCmd.PersistentFlags().Bool("raw-numbers", false, "Keep numbers unformatted and preserve epoch timestamps in table/TOON output")
	dciCmd.PersistentFlags().Bool("utc", false, "Render timestamps in UTC instead of the local timezone (table output only; machine formats and report period columns are always UTC)")
	dciCmd.PersistentFlags().Bool("heatmap", true, "Shade pivot cells by magnitude in interactive table output (respects NO_COLOR)")
	dciCmd.PersistentFlags().String("chart", "", "Render a chart of the report under the table: 'stacked' columns per group (the default), a 'line' of period totals, a one-line 'sparkline', or a group-by-period 'heatmap' (table output in human mode only; ignored elsewhere)")
	// Bare --chart keeps working (and picks the stacked view): cobra fills
	// the value from NoOptDefVal when the flag is passed without one.
	dciCmd.PersistentFlags().Lookup("chart").NoOptDefVal = "stacked"
	dciCmd.PersistentFlags().Bool("id", false, "Treat positional resource arguments as literal IDs and skip name resolution")
	dciCmd.PersistentFlags().Bool("name", false, "Force name resolution even when an argument matches the resource ID format")
	dciCmd.PersistentFlags().Bool("all", false, "Fetch every page of a paged list response before rendering (follows the server's page tokens; GET list commands only). Cannot be combined with --page-token or --max-results")
	dciCmd.PersistentFlags().String("search", "", "Client-side case-insensitive substring filter over list items, matched against every text field (list commands only; e.g. list-dimensions --search genai). Implies --all so the whole collection is searched. Cannot be combined with --page-token or --max-results")
	registerStaticFlagCompletions(dciCmd)

	// Bind table flags into viper so the renderer can pick them up.
	prev := dciCmd.PersistentPreRunE
	dciCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(cmd, args); err != nil {
				return err
			}
		}
		// Record the leaf command so response transforms can apply
		// command-specific shaping (e.g. the list-insights view).
		invokedCommandName = cmd.Name()

		outFlag := cmd.Flags().Lookup("output")
		if outFlag == nil || !outFlag.Changed {
			viper.Set("rsh-output-format", defaultOutputFormatForCommand(cmd.Name()))
		} else {
			out := strings.TrimSpace(outFlag.Value.String())
			switch out {
			case "table", "json", "yaml", "csv", "auto", "toon":
				viper.Set("rsh-output-format", out)
			default:
				return fmt.Errorf("invalid --output %q (supported: table, json, yaml, csv, auto, toon)", out)
			}
		}
		defaultToBodyOutput()

		// Always (re)set these keys: viper state persists across in-process
		// runs, so conditional writes would leak a previous invocation's flags.
		maxRows := -1 // auto: agent default in agent mode, unlimited otherwise
		if flag := cmd.Flags().Lookup("max-rows"); flag != nil && flag.Changed {
			v, err := strconv.Atoi(flag.Value.String())
			if err != nil || v < 0 {
				return fmt.Errorf("invalid --max-rows %q (use a non-negative integer; 0 = unlimited)", flag.Value.String())
			}
			maxRows = v
		}
		viper.Set("max-rows", maxRows)

		rowsMode := ""
		if flag := cmd.Flags().Lookup("rows"); flag != nil && flag.Changed {
			rowsMode = strings.ToLower(strings.TrimSpace(flag.Value.String()))
			switch rowsMode {
			case "keyed", "positional":
			default:
				return fmt.Errorf("invalid --rows %q (supported: positional, keyed)", flag.Value.String())
			}
		}
		viper.Set("rows-mode", rowsMode)

		// --all pagination state: always reset (viper persists across
		// in-process runs). The boost is the endpoint's known page-size cap,
		// so --all fetches the fewest pages possible.
		allPages := false
		if flag := cmd.Flags().Lookup("all"); flag != nil && flag.Changed {
			allPages = flag.Value.String() == "true"
		}
		searchTerm := ""
		if flag := cmd.Flags().Lookup("search"); flag != nil && flag.Changed {
			searchTerm = strings.TrimSpace(flag.Value.String())
		}
		viper.Set("list-search", searchTerm)
		if searchTerm != "" {
			// Searching one page would silently miss the rest of the
			// collection, so --search always fetches all pages.
			allPages = true
		}
		viper.Set("all-pages", allPages)
		viper.Set("all-pages-boost", pagingCaps[cmd.Name()].limit)

		dropUnlabeled := false
		if flag := cmd.Flags().Lookup("drop-unlabeled-rows"); flag != nil && flag.Changed {
			dropUnlabeled = flag.Value.String() == "true"
		}
		viper.Set("drop-unlabeled-rows", dropUnlabeled)

		rollup := ""
		if flag := cmd.Flags().Lookup("rollup"); flag != nil && flag.Changed {
			rollup = strings.TrimSpace(flag.Value.String())
		}
		viper.Set("report-rollup", rollup)
		if allPages {
			// Installed lazily, only for --all runs: restish configures TLS and
			// proxies via a http.DefaultTransport.(*http.Transport) assertion
			// (request.go), which a standing wrapper would break for every
			// invocation.
			installPaginatingTransport()
		}
		// After the paginating transport so the spinner wraps it: one spinner
		// spans a whole --all fetch instead of restarting per page.
		installSpinnerTransport()

		viper.Set("report-currency", "")
		viper.Set("money-columns", "")
		viper.Set("report-hourly", false)
		viper.Set("table-columns-auto", false)
		viper.Set("table-priority-column", "")
		viper.Set("table-accent-column", "")
		viper.Set("table-accent-flag-key", "")
		viper.Set("table-link-column", "")
		viper.Set("table-link-url-key", "")
		// Whether cell accents (e.g. the insights easy-win title highlight) may
		// color interactive table output. Same gate as the heatmap, minus the
		// --heatmap flag, which governs only the pivot shading.
		viper.Set("table-color", heatmapEnabled(true, agentMode, stdoutIsTTY() || sessionRenderActive(), os.Getenv("NO_COLOR") != ""))
		viper.Set("pivot-active", false)
		viper.Set("pivot-total-rows", 0)
		viper.Set("utc-label-columns", "")

		// Resolve the instant-display zone once per invocation; nil outside a
		// normal command run keeps the pipeline UTC-deterministic in tests.
		displayTimeLocation = resolveDisplayLocation(os.Getenv("DCI_TZ"))
		localizedInstantShown = false

		heatmapRequested := true
		if flag := cmd.Flags().Lookup("heatmap"); flag != nil {
			heatmapRequested = flag.Value.String() == "true"
		}
		viper.Set("heatmap", heatmapEnabled(heatmapRequested, agentMode, stdoutIsTTY(), os.Getenv("NO_COLOR") != ""))

		resetChartState()
		chartMode := ""
		if flag := cmd.Flags().Lookup("chart"); flag != nil && flag.Changed {
			chartMode = strings.ToLower(strings.TrimSpace(flag.Value.String()))
			switch chartMode {
			case "stacked", "line", "sparkline", "heatmap":
			case "true": // the flag's former boolean spelling keeps working
				chartMode = "stacked"
			case "false", "":
				chartMode = ""
			default:
				return fmt.Errorf("invalid --chart %q (supported: stacked, line, sparkline, heatmap)", flag.Value.String())
			}
		}
		viper.Set("chart-requested", chartMode != "")
		viper.Set("chart-mode", chartMode)

		for flagName, configName := range map[string]string{
			"pivot":              "pivot-rows",
			"flat":               "flat-rows",
			"include-empty-rows": "include-empty-rows",
			"include-dismissed":  "include-dismissed",
			"raw-numbers":        "raw-numbers",
			"utc":                "display-utc",
		} {
			value := false
			if flag := cmd.Flags().Lookup(flagName); flag != nil {
				value = flag.Value.String() == "true"
			}
			viper.Set(configName, value)
		}

		if flag := cmd.Flags().Lookup("table-mode"); flag != nil {
			v := strings.TrimSpace(flag.Value.String())
			if v == "" {
				v = "fit"
			}
			viper.Set("table-mode", v)
		}
		if flag := cmd.Flags().Lookup("table-columns"); flag != nil {
			v := strings.TrimSpace(flag.Value.String())
			viper.Set("table-columns", v)
		}
		bindNonNegativeIntFlag(cmd, "table-width")
		bindNonNegativeIntFlag(cmd, "table-max-col-width")
		for flagName, configName := range map[string]string{
			"fields":  "agent-fields",
			"exclude": "agent-exclude",
			"yes":     "agent-confirm-destructive",
			"dry-run": "agent-dry-run",
		} {
			if flag := cmd.Flags().Lookup(flagName); flag != nil {
				viper.Set(configName, flag.Value.String())
			}
		}
		if fields := strings.TrimSpace(viper.GetString("agent-fields")); fields != "" {
			if flag := cmd.Flags().Lookup("table-columns"); flag == nil || !flag.Changed {
				viper.Set("table-columns", fields)
			}
		}
		// Resolution first, so typed path-param checks below see the resolved
		// ID and the destructive gate can display the true target.
		if err := resolvePathArguments(cmd, args); err != nil {
			return err
		}
		// Path parameters first: a bad identifier is rejected before stdin is
		// buffered for body validation, and before the destructive check below,
		// so --dry-run validates too and no request is ever built.
		if err := validatePathParameters(cmd, args); err != nil {
			return err
		}
		// The interactive query builder may substitute the request body, so
		// it runs before the body is buffered and validated.
		if err := maybeRunQueryBuilder(cmd, args); err != nil {
			return err
		}
		if err := validateRequestBody(cmd, args); err != nil {
			return err
		}

		// If --customer-context / -D was explicitly passed, override whatever
		// applyCustomerContext() injected from the file or env var.
		if flag := cmd.Flags().Lookup("customer-context"); flag != nil && flag.Changed {
			val := strings.TrimSpace(flag.Value.String())
			if val == "" {
				return fmt.Errorf("--customer-context requires a non-empty domain name")
			}
			headers := viper.GetStringSlice("rsh-header")
			filteredHeaders := headers[:0]
			for _, h := range headers {
				if !isTenantHeader(h) {
					filteredHeaders = append(filteredHeaders, h)
				}
			}
			viper.Set("rsh-header", append(filteredHeaders, tenantIDHeaderPrefix+val))

			queries := viper.GetStringSlice("rsh-query")
			filteredQueries := queries[:0]
			for _, q := range queries {
				if !strings.HasPrefix(q, customerContextQueryPrefix) {
					filteredQueries = append(filteredQueries, q)
				}
			}
			viper.Set("rsh-query", append(filteredQueries, customerContextQueryPrefix+val))
			customerContextFlagValue = val
		}

		return enforceDestructiveConfirmation(cmd, args)
	}
}

func bindNonNegativeIntFlag(cmd *cobra.Command, name string) {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		v, _ := strconv.Atoi(flag.Value.String())
		if v < 0 {
			v = 0
		}
		viper.Set(name, v)
	}
}

func defaultToBodyOutput() {
	// By default restish prints response status + headers for TTY output when no
	// filter is specified. This CLI is primarily focused on the response body,
	// so default to `body` unless the user explicitly requested raw output or a
	// filter was already set.
	if !viper.GetBool("rsh-raw") && viper.GetString("rsh-filter") == "" {
		viper.Set("rsh-filter", "body")
	}
}

type dciTableContentType struct{}

func overrideTableOutput() {
	// Restish's built-in table output expects the response body to be a JSON
	// array of objects. Many DCI list endpoints return an object that contains
	// the array under a field (e.g. `budgets: [...]`). This keeps `--output table`
	// ergonomic by extracting the most likely array or wrapping single objects.
	cli.AddContentType("table", "", -1, &dciTableContentType{})
	// TOON output is opt-in via --output toon; table stays the default. Args
	// mirror the table registration: the name (2nd arg) only feeds the Accept
	// header for content negotiation, which is skipped at q=-1, so it's left
	// empty — restish resolves --output by the short name ("toon").
	cli.AddContentType("toon", "", -1, &dciToonContentType{})
	cli.AddContentType("csv", "", -1, &dciCSVContentType{})
}

// nonJSONErrorResponse flags that the last API response was an error page/body
// so run() can force a non-zero exit even on a 2xx status. Reset each run().
var nonJSONErrorResponse bool

// dciResponseGuard wraps restish's formatter to catch error responses the DCI
// API can leak as non-JSON HTML (e.g. a Cloudflare 524 maintenance page) or as
// a JSON error body under a locked 2xx status, replacing restish's success
// handling with a clear message and a non-zero exit code.
type dciResponseGuard struct {
	next cli.ResponseFormatter
}

func (g dciResponseGuard) Format(resp cli.Response) error {
	if resp.Status >= 400 {
		if agentErrorContractEnabled() {
			responseExitCode = exitCodeForHTTPStatus(resp.Status)
			writeStructuredError(cli.Stderr, structuredErrorForResponse(resp))
			if isHTMLErrorPage(resp) {
				return nil
			}
			return g.next.Format(resp)
		}
		if agentUAMode == uaModeInteractive {
			nonJSONErrorResponse = true
			responseExitCode = exitCodeForHTTPStatus(resp.Status)
			if isHTMLErrorPage(resp) {
				printNonJSONError(resp)
			} else {
				printHumanAPIError(resp)
			}
			return nil
		}
		if isHTMLErrorPage(resp) {
			nonJSONErrorResponse = true
			printNonJSONError(resp)
			return nil
		}
		return g.next.Format(resp)
	}
	if resp.Status >= 200 && resp.Status < 300 && responseBodyIsEmpty(resp.Body) {
		return nil
	}
	if isHTMLErrorPage(resp) {
		nonJSONErrorResponse = true
		if agentErrorContractEnabled() {
			responseExitCode = exitServer
			detail := structuredErrorForResponse(resp)
			detail.Code = "UPSTREAM_NON_JSON_RESPONSE"
			detail.Message = "The DoiT API returned a non-JSON response"
			detail.Hint = "Retry the request; contact DoiT support with the request ID if it persists"
			detail.Retryable = true
			writeStructuredError(cli.Stderr, detail)
			return nil
		}
		printNonJSONError(resp)
		return nil
	}
	if msg, ok := jsonApplicationError(resp); ok {
		nonJSONErrorResponse = true
		if agentErrorContractEnabled() {
			responseExitCode = exitServer
			detail := structuredErrorForResponse(resp)
			detail.Code = "APPLICATION_ERROR"
			detail.Message = msg
			writeStructuredError(cli.Stderr, detail)
			return nil
		}
		if err := g.next.Format(resp); err != nil {
			return err
		}
		// cli.Stderr (not os.Stderr) so callers like login can suppress it; don't revert.
		fmt.Fprintf(cli.Stderr, "Error: the DoiT API returned an application error: %s\n", msg)
		return nil
	}
	resp.Body = transformSuccessBody(resp.Body)
	if err := g.next.Format(resp); err != nil {
		return err
	}
	maybeNoteDisplayZone()
	return nil
}

func responseBodyIsEmpty(body interface{}) bool {
	switch value := body.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(value) == ""
	case []byte:
		return strings.TrimSpace(string(value)) == ""
	}
	return false
}

func printHumanAPIError(resp cli.Response) {
	message := responseErrorMessage(resp.Body)
	if message == "" {
		message = fmt.Sprintf("DoiT API request failed with HTTP status %d", resp.Status)
	}
	_, _ = fmt.Fprintf(cli.Stderr, "Error: %s (HTTP %d)\n", message, resp.Status)
	if trace := traceID(resp.Headers); trace != "" {
		_, _ = fmt.Fprintf(cli.Stderr, "Trace: %s\n", trace)
	}
	if resp.Status == 401 || resp.Status == 403 {
		if ctx := activeCustomerContext(); ctx != "" {
			_, _ = fmt.Fprintf(cli.Stderr, "Active customer context: %s\n", ctx)
		}
		_, _ = fmt.Fprintf(cli.Stderr, "Hint: %s\n", authFailureHint(resp.Status))
	}
}

// installResponseGuard wraps the active restish formatter. It must run after
// cli.Defaults() (which sets cli.Formatter) and overrideTableOutput().
func installResponseGuard() {
	if cli.Formatter == nil {
		return
	}
	cli.Formatter = dciResponseGuard{next: cli.Formatter}
}

// isHTMLErrorPage reports whether a response is HTML rather than JSON, checking
// the Content-Type and sniffing the body (edge error pages can carry a
// misleading or absent type). JSON and SSE responses are left untouched.
func isHTMLErrorPage(resp cli.Response) bool {
	if strings.Contains(strings.ToLower(headerValue(resp.Headers, "Content-Type")), "text/html") {
		return true
	}
	switch b := resp.Body.(type) {
	case string:
		return bodyLooksLikeHTML(b)
	case []byte:
		return bodyLooksLikeHTML(string(b))
	}
	return false
}

func isErrorResponseBody(resp cli.Response) bool {
	if isHTMLErrorPage(resp) {
		return true
	}
	_, applicationError := jsonApplicationError(resp)
	return applicationError
}

func bodyLooksLikeHTML(s string) bool {
	s = strings.TrimSpace(strings.TrimPrefix(s, "\ufeff")) // strip UTF-8 BOM some proxies prepend
	if len(s) > 16 {
		s = s[:16] // only the prefix matters; avoid lowercasing the whole body
	}
	s = strings.ToLower(s)
	return strings.HasPrefix(s, "<!doctype html") ||
		strings.HasPrefix(s, "<html") ||
		strings.HasPrefix(s, "<head") ||
		strings.HasPrefix(s, "<body") ||
		strings.HasPrefix(s, "<?xml") ||
		strings.HasPrefix(s, "<!--")
}

// jsonApplicationError reports whether a 2xx response carries a non-empty
// top-level JSON `error` field (e.g. an AVA askSync failure under a locked 2xx
// status), returning a human-readable message. Such a body is not a valid DCI
// success shape, so it is treated as a failure.
//
// The object-only, top-level-only scoping is load-bearing, verified against the
// OpenAPI spec: the only other 2xx body carrying a top-level `error` is
// POST /insights/v1/results, whose 200 is a top-level *array* of per-row error
// items. The map assertion below lets that array pass through untouched. Do not
// recurse into arrays or nested objects here, or it will false-positive on
// legitimate partial-result responses.
func jsonApplicationError(resp cli.Response) (string, bool) {
	if resp.Status < 200 || resp.Status >= 300 {
		return "", false
	}
	body, ok := resp.Body.(map[string]interface{})
	if !ok {
		return "", false
	}
	switch v := body["error"].(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return "", false
		}
		return v, true
	case map[string]interface{}:
		// Defensive only: no current DCI endpoint returns an object-typed error (AVA returns a string).
		if len(v) == 0 {
			return "", false
		}
		if m, ok := v["message"].(string); ok && strings.TrimSpace(m) != "" {
			return m, true
		}
		return "application error", true
	}
	return "", false
}

func printNonJSONError(resp cli.Response) {
	var b strings.Builder
	b.WriteString("Error: the DoiT API returned a non-JSON (HTML) response.\n")
	b.WriteString("This usually means an upstream timeout or maintenance window rather than a problem with your request.\n")
	if resp.Status != 0 {
		fmt.Fprintf(&b, "HTTP status: %d\n", resp.Status)
	}
	if trace := traceID(resp.Headers); trace != "" {
		fmt.Fprintf(&b, "Trace: %s\n", trace)
	}
	b.WriteString("Please retry in a moment. If it persists, contact DoiT support with the trace above.\n")
	// cli.Stderr (not os.Stderr) so internal callers like login can redirect it.
	fmt.Fprint(cli.Stderr, b.String())
}

// traceID returns the first available upstream trace identifier so users can
// reference it in a support request.
func traceID(headers map[string]string) string {
	for _, name := range []string{"Cf-Ray", "X-Doit-Trace", "X-Request-Id", "X-Cloud-Trace-Context", "Traceparent"} {
		if v := strings.TrimSpace(headerValue(headers, name)); v != "" {
			return name + "=" + v
		}
	}
	return ""
}

// headerValue looks up a header case-insensitively. Restish canonicalizes
// header keys, but sniffing defensively keeps this robust across sources.
func headerValue(headers map[string]string, name string) string {
	if v, ok := headers[name]; ok {
		return v
	}
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func (t dciTableContentType) Detect(contentType string) bool { return false }

func (t dciTableContentType) Marshal(value interface{}) ([]byte, error) {
	jsonSafe, err := toJSONSafe(value)
	if err != nil {
		return nil, err
	}

	rows, err := toTableRows(jsonSafe, labelDisplay)
	if err != nil {
		// Response is not table-friendly; fall back to indented JSON.
		b, jsonErr := json.MarshalIndent(jsonSafe, "", "  ")
		if jsonErr != nil {
			return nil, err // return original table error
		}
		return append(b, '\n'), nil
	}
	// The table drops the list wrapper (pageToken and friends); say so when
	// that hides a continuation token.
	notePageTokenDropped(jsonSafe)
	return renderTable(rows)
}

func (t dciTableContentType) Unmarshal(data []byte, value interface{}) error {
	return fmt.Errorf("unimplemented")
}

type dciToonContentType struct{}

func (t dciToonContentType) Detect(contentType string) bool { return false }

func (t dciToonContentType) Marshal(value interface{}) ([]byte, error) {
	// TOON (Token-Oriented Object Notation) is a compact encoding that uses far
	// fewer tokens than JSON for list-shaped data — useful when the CLI is
	// driven by an LLM agent. Normalize types first so toon sees plain
	// maps/slices, then fall back to indented JSON if encoding fails so the user
	// still gets output (matches dciTableContentType's degrade-gracefully behavior).
	jsonSafe, err := toJSONSafe(value)
	if err != nil {
		return nil, err
	}

	b, err := toon.Marshal(toonPrepare(jsonSafe))
	if err != nil {
		fallback, jsonErr := json.MarshalIndent(jsonSafe, "", "  ")
		if jsonErr != nil {
			return nil, err // return original toon error
		}
		return append(fallback, '\n'), nil
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	return b, nil
}

func (t dciToonContentType) Unmarshal(data []byte, value interface{}) error {
	return fmt.Errorf("unimplemented")
}

// toonPrepare normalizes a JSON-safe response body so list-shaped data folds
// into TOON's compact tabular form (`items[N]{a,b}:` + CSV rows). The TOON
// spec only folds arrays of uniform objects whose values are ALL primitives;
// real DCI rows carry array/object fields (labels: [], nested configs) that
// disqualify the fold. The transforms mirror the table renderer so both
// formats present the same information — but unlike the table, wrapper
// siblings (pageToken, totals, …) are kept: agents need them for pagination.
func toonPrepare(v interface{}) interface{} {
	if root, ok := v.(map[string]interface{}); ok {
		// get-report rows arrive as arrays of arrays; map them to schema-named
		// objects (same as the table path) so they can fold tabular. Labels
		// stay full RFC3339 UTC — agent output is zone-independent.
		if rows, handled, err := extractGetReportRows(root, labelRFC3339); handled && err == nil {
			items := make([]interface{}, len(rows))
			for i, r := range rows {
				items[i] = r
			}
			// Replace rows in the same container extractGetReportRows read from:
			// the first of result/results that is an object holding a rows key.
			for _, key := range []string{"result", "results"} {
				if c, ok := root[key].(map[string]interface{}); ok {
					if _, ok := c["rows"]; ok {
						c["rows"] = items
						liftConstantReportColumns(c, rows)
						break
					}
				}
			}
		}
	}
	return toonNormalize(v, toonRowOptionsFromConfig())
}

// toonRowOptions carries the user's explicit field requests into row
// normalization: explicitly requested fields are never dropped (see
// toonNormalizeRows).
type toonRowOptions struct {
	// selected is the -C column selection (shared with the table renderer).
	selected []string
	// keepAll is set when a custom -f filter shaped the body: the agent
	// already hand-picked the fields, so dropping any would discard requested
	// data.
	keepAll bool
}

func toonRowOptionsFromConfig() toonRowOptions {
	filter := strings.TrimSpace(viper.GetString("rsh-filter"))
	return toonRowOptions{
		selected: getTableOptions().columns,
		// "body" is the default filter injected by setDefaultFilter, not a
		// user choice.
		keepAll: filter != "" && filter != "body",
	}
}

// toonNormalize recursively applies the fold-enabling transforms. Arrays of
// objects are candidate tabular rows and get the table renderer's column
// rules (see toonNormalizeRows); other objects are detail bodies with no fold
// to win, so their nested structure is kept — only info-free empty objects
// are pruned and primitive arrays joined like table cells. An empty array
// outside a row is meaningful (`reports[0]:` = no results) and is kept as-is.
func toonNormalize(v interface{}, opts toonRowOptions) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			nv := toonNormalize(val, opts)
			if m, ok := nv.(map[string]interface{}); ok && len(m) == 0 {
				continue // empty objects carry no information
			}
			if arr, ok := nv.([]interface{}); ok && len(arr) > 0 && allPrimitives(arr) {
				nv = joinPrimitives(arr)
			}
			out[k] = nv
		}
		return out
	case []interface{}:
		if len(x) > 0 && allObjects(x) {
			return toonNormalizeRows(x, opts)
		}
		out := make([]interface{}, len(x))
		for i, item := range x {
			out[i] = toonNormalize(item, opts)
		}
		return out
	default:
		return x
	}
}

// toonNormalizeRows mirrors the table renderer's column rules so every row
// ends up with a uniform, all-primitive key set and TOON folds the list into
// its tabular form: columns holding objects are dropped from all rows (the
// table hides those columns), arrays become ", "-joined cell strings (empty
// → blank cell), and keys missing from some rows are filled with blank cells
// (the table renders the union of columns), exactly as table cells render.
//
// Explicitly requested fields are never dropped: a -C selection (applied to
// lists that contain at least one selected column, so it doesn't empty
// unrelated nested lists) or a custom -f filter exempts columns from the
// object-column drop, and their object-valued cells encode as compact JSON
// strings so the fold still survives.
func toonNormalizeRows(items []interface{}, opts toonRowOptions) []interface{} {
	dropped := map[string]bool{}
	seen := map[string]bool{}
	columns := []string{}
	for _, item := range items {
		row := item.(map[string]interface{})
		for k, v := range row {
			if !dropped[k] && containsObject(v) {
				dropped[k] = true
			}
			if !seen[k] {
				seen[k] = true
				columns = append(columns, k)
			}
		}
	}

	if len(opts.selected) > 0 {
		matches := false
		for _, k := range opts.selected {
			if seen[k] {
				matches = true
				break
			}
		}
		if matches {
			columns = opts.selected
			dropped = map[string]bool{}
		}
	}
	if opts.keepAll {
		dropped = map[string]bool{}
	}

	out := make([]interface{}, len(items))
	for i, item := range items {
		row := item.(map[string]interface{})
		nr := make(map[string]interface{}, len(columns))
		for _, k := range columns {
			if dropped[k] {
				continue
			}
			v, ok := row[k]
			if !ok {
				nr[k] = ""
				continue
			}
			if containsObject(v) {
				// Explicitly requested object values: keep the data, keep the
				// fold — encode the cell as a compact JSON string.
				nr[k] = jsonCell(v)
				continue
			}
			if arr, ok := v.([]interface{}); ok {
				// Object-free array cells are formatted as the table would.
				v = joinPrimitives(arr)
			}
			nr[k] = v
		}
		out[i] = nr
	}
	return out
}

func jsonCell(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Unreachable for JSON-roundtripped values; degrade readably.
		return formatValue(v)
	}
	return string(b)
}

func allObjects(arr []interface{}) bool {
	for _, v := range arr {
		if _, ok := v.(map[string]interface{}); !ok {
			return false
		}
	}
	return true
}

func allPrimitives(arr []interface{}) bool {
	for _, v := range arr {
		switch v.(type) {
		case map[string]interface{}, []interface{}:
			return false
		}
	}
	return true
}

func joinPrimitives(arr []interface{}) string {
	// Same ", " join and value formatting the table renderer uses for array
	// cells, so toon and table show identical cell content.
	parts := make([]string, len(arr))
	for i, v := range arr {
		parts[i] = formatValue(v)
	}
	return strings.Join(parts, ", ")
}

func toJSONSafe(value interface{}) (interface{}, error) {
	// Roundtrip through encoding/json to normalize map/slice types.
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func toTableRows(value interface{}, style timestampLabelStyle) ([]map[string]interface{}, error) {
	switch v := value.(type) {
	case []interface{}:
		rows := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			obj, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("error building table. Must be array of objects")
			}
			rows = append(rows, obj)
		}
		return rows, nil
	case map[string]interface{}:
		// Special case for get-report responses where rows are in
		// result.rows/results.rows and each row can be an array.
		if rows, handled, err := extractGetReportRows(v, style); handled {
			return rows, err
		}

		// If this is a list response wrapper, pull out the most likely list field.
		if list := pickObjectArrayField(v); list != nil {
			return toTableRows(list, style)
		}
		// Otherwise treat it as a single-row table.
		return []map[string]interface{}{v}, nil
	default:
		return nil, fmt.Errorf("error building table. Must be array of objects")
	}
}

func extractGetReportRows(root map[string]interface{}, style timestampLabelStyle) ([]map[string]interface{}, bool, error) {
	containers := []string{"result", "results"}
	for _, key := range containers {
		rawContainer, ok := root[key]
		if !ok {
			continue
		}

		container, ok := rawContainer.(map[string]interface{})
		if !ok {
			continue
		}

		rawRows, ok := container["rows"]
		if !ok {
			continue
		}

		rowItems, ok := rawRows.([]interface{})
		if !ok {
			// It looked like a get-report container, but rows is malformed.
			return nil, true, fmt.Errorf("error building table. result.rows must be an array")
		}

		schema := readReportSchema(container["schema"])
		colNames := readReportSchemaColumnNames(container["schema"])
		redundant := redundantTimeDimensionColumns(schema)
		rows := make([]map[string]interface{}, 0, len(rowItems))
		for _, item := range rowItems {
			switch row := item.(type) {
			case map[string]interface{}:
				rows = append(rows, displayTimestampFields(row, schema, style))
			case []interface{}:
				obj := map[string]interface{}{}
				for i, cell := range row {
					if redundant[i] {
						continue
					}
					obj[reportColumnName(colNames, i)] = displayTimestampCell(cell, schemaColumnType(schema, i), style)
				}
				rows = append(rows, obj)
			default:
				// Defensive fallback for unexpected scalar rows.
				obj := map[string]interface{}{
					reportColumnName(colNames, 0): row,
				}
				rows = append(rows, obj)
			}
		}
		return rows, true, nil
	}

	return nil, false, nil
}

// redundantTimeDimensionColumns marks datetime dimension columns (year,
// month, day, …) that repeat information already carried by a timestamp
// column in the same schema — the pivot classifier's judgment
// (classifyPivotColumns), applied to the flat views: each row would otherwise
// carry the same period twice (month "07" + year "2026" + the timestamp).
// The timestamp is kept — it is the machine-sortable form. An explicit -C
// selection disables the suppression: requested columns are never dropped.
func redundantTimeDimensionColumns(schema []reportColumn) map[int]bool {
	if strings.TrimSpace(viper.GetString("table-columns")) != "" {
		return nil
	}
	hasTimestamp := false
	for _, col := range schema {
		if col.Type == "timestamp" || col.Type == "datetime" {
			hasTimestamp = true
			break
		}
	}
	if !hasTimestamp {
		return nil
	}
	redundant := map[int]bool{}
	for i, col := range schema {
		name := strings.ToLower(col.Name)
		for _, part := range pivotTimeParts {
			if name == part {
				redundant[i] = true
				break
			}
		}
	}
	if len(redundant) == 0 {
		return nil
	}
	return redundant
}

func readReportSchemaColumnNames(rawSchema interface{}) []string {
	schema, ok := rawSchema.([]interface{})
	if !ok {
		return nil
	}

	names := make([]string, 0, len(schema))
	for _, col := range schema {
		if m, ok := col.(map[string]interface{}); ok {
			if n, ok := m["name"].(string); ok && strings.TrimSpace(n) != "" {
				names = append(names, n)
			}
		}
	}
	return names
}

func reportColumnName(schemaCols []string, i int) string {
	if i >= 0 && i < len(schemaCols) {
		return schemaCols[i]
	}
	return fmt.Sprintf("col_%d", i+1)
}

func pickObjectArrayField(m map[string]interface{}) interface{} {
	// The invoked command's registered items key names the response's primary
	// collection — size-based discovery below can lose to a sideloaded
	// secondary array (Zendesk tickets responses carry a larger `users`
	// array). This holds even when the curated presentation view does not
	// apply (explicit -C/--fields selections, csv output): the primary
	// collection is a property of the command, not of the view.
	if view, ok := listViews[invokedCommandName]; ok {
		if v, ok := m[view.itemsKey]; ok && isObjectArray(v) {
			return v
		}
	}
	// Prefer common patterns if present.
	if v, ok := m["items"]; ok {
		if isObjectArray(v) {
			return v
		}
	}

	// Otherwise pick the largest array-of-objects field.
	bestKey := ""
	bestLen := -1
	for k, v := range m {
		arr, ok := v.([]interface{})
		if !ok {
			continue
		}
		if !isObjectArray(arr) {
			continue
		}
		if len(arr) > bestLen {
			bestKey = k
			bestLen = len(arr)
		}
	}
	if bestKey == "" {
		return nil
	}
	return m[bestKey]
}

func isObjectArray(v interface{}) bool {
	arr, ok := v.([]interface{})
	if !ok {
		return false
	}
	// Empty array is ambiguous; treat as acceptable so table doesn't error.
	if len(arr) == 0 {
		return true
	}
	_, ok = arr[0].(map[string]interface{})
	return ok
}

func renderTable(rows []map[string]interface{}) ([]byte, error) {
	opts := getTableOptions()

	if len(rows) == 0 {
		return []byte("No results\n"), nil
	}

	keys := collectKeys(rows, opts.columns)
	if len(keys) == 0 {
		return []byte("No results\n"), nil
	}

	// Auto-hide columns containing object values (map[...]) unless the user
	// explicitly selected columns via -C.
	var hidden []string
	if len(opts.columns) == 0 {
		keys, hidden = filterObjectColumns(rows, keys)
	}

	if len(keys) == 0 {
		return []byte("No results\n"), nil
	}

	maxColWidth := opts.maxColWidth
	if maxColWidth < 0 {
		maxColWidth = 0
	}

	terminalWidth := detectTerminalWidth(opts.width)

	// Wide responses (e.g. anomalies with 16 columns) would otherwise squeeze
	// every column into unreadable "…" stubs. Keep only as many columns as
	// render readably and report the rest through the same hidden-columns
	// hint used for object columns. An explicit -C selection or wrap mode
	// keeps every requested column; an auto-generated column order (the
	// pivot's, or the list-insights default view's) is not a user selection,
	// so it stays fit-eligible.
	if (len(opts.columns) == 0 || viper.GetBool("table-columns-auto")) && opts.mode == "fit" {
		var hiddenForWidth []string
		keys, hiddenForWidth = fitColumnsToTerminal(rows, keys, terminalWidth)
		hidden = append(hidden, hiddenForWidth...)
	}

	keys = augmentTableViewColumns(rows, keys)

	if opts.mode == "interactive" {
		selection, err := runTableViewer(rows, keys)
		if err != nil {
			return nil, err
		}
		if selection == "" {
			return []byte{}, nil
		}
		return []byte(selection + "\n"), nil
	}

	contentW := measureContentWidths(rows, keys)

	colWidths := computeColumnWidths(contentW, terminalWidth, maxColWidth)
	widenPriorityColumn(keys, contentW, colWidths)
	out, err := buildTableString(rows, keys, colWidths, opts.mode)
	if err != nil {
		return nil, err
	}
	// The first rendered line is the table's top border: its rune width is
	// the exact table width, so the chart can line up with it.
	maybeRenderChart(utf8.RuneCountInString(firstLine(out)))

	if len(hidden) > 0 {
		out += renderHiddenColumnsHint(keys, hidden)
	}
	return []byte(out), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// renderHiddenColumnsHint reports the columns the table did not render. On an
// interactive terminal it is a single dim line — count, first…last span, and
// the escape hatches — instead of the full listing, which for a pivoted month
// of daily columns wrapped into several lines of dates. Non-TTY output keeps
// the verbose form: scripts and agents need every hidden column name spelled
// out, and the -C example stays copy-pasteable.
func renderHiddenColumnsHint(keys, hidden []string) string {
	if !tuiActive() && !sessionRenderActive() {
		return fmt.Sprintf("\nHidden columns (nested objects, or too many to fit): %s\n", strings.Join(hidden, ", ")) +
			fmt.Sprintf("Use -C to choose columns (e.g.: -C %s), -M wrap to wrap, or -W to widen\n", strings.Join(append(keys, hidden...), ","))
	}
	span := strings.Join(hidden, ", ")
	if len(hidden) > 2 {
		span = hidden[0] + " … " + hidden[len(hidden)-1]
	}
	return "\n" + tuiDimStyle.Render(fmt.Sprintf("+%d hidden: %s · -C to choose · -M wrap · -W widen", len(hidden), span)) + "\n"
}

type tableOptions struct {
	mode        string
	columns     []string
	width       int
	maxColWidth int
}

func getTableOptions() tableOptions {
	mode := strings.ToLower(strings.TrimSpace(viper.GetString("table-mode")))
	if mode == "" {
		mode = "fit"
	}
	if mode == "i" {
		mode = "interactive"
	}
	switch mode {
	case "fit", "wrap", "interactive":
	default:
		mode = "fit"
	}
	if mode == "interactive" && !tuiActive() {
		// A saved alias must never break a pipe: degrade, don't error.
		fmt.Fprintln(os.Stderr, "note: --table-mode interactive needs an interactive terminal; falling back to fit")
		mode = "fit"
	}

	colsRaw := strings.TrimSpace(viper.GetString("table-columns"))
	var cols []string
	if colsRaw != "" {
		for _, c := range strings.Split(colsRaw, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				cols = append(cols, c)
			}
		}
	}

	width := viper.GetInt("table-width")
	maxColWidth := viper.GetInt("table-max-col-width")

	return tableOptions{
		mode:        mode,
		columns:     cols,
		width:       width,
		maxColWidth: maxColWidth,
	}
}

func detectTerminalWidth(forced int) int {
	if forced > 0 {
		return forced
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if n, err := strconv.Atoi(cols); err == nil && n > 0 {
			return n
		}
	}
	return 120
}

func tableDisplayWidth(s string) int {
	max := 0
	for _, line := range strings.Split(s, "\n") {
		if w := runewidth.StringWidth(line); w > max {
			max = w
		}
	}
	return max
}

// measureContentWidths returns the max display width of each column's content
// across all rows (including the header key name).
func measureContentWidths(rows []map[string]interface{}, keys []string) []int {
	widths := make([]int, len(keys))
	for i, k := range keys {
		widths[i] = runewidth.StringWidth(k)
	}
	for _, row := range rows {
		for i, k := range keys {
			w := runewidth.StringWidth(renderCellText(row, k))
			if w > widths[i] {
				widths[i] = w
			}
		}
	}
	return widths
}

// renderCellText renders a cell with full row context: monetary cells get the
// currency symbol and whole-unit rounding when the currency is known (from
// the row itself, e.g. budgets, or from the report request config).
func renderCellText(row map[string]interface{}, key string) string {
	val := row[key]
	if !viper.GetBool("raw-numbers") {
		if amount, ok := numericCell(val); ok {
			if currency := cellCurrency(row, key); currency != "" {
				return formatMoney(amount, currency)
			}
		}
	}
	return tableCellText(key, val)
}

// cellCurrency decides whether a cell is monetary and in which currency:
// either the transform marked the column (report metrics, pivot periods), or
// the row itself carries a currency field next to a money-named column.
func cellCurrency(row map[string]interface{}, key string) string {
	reportCurrency := strings.TrimSpace(viper.GetString("report-currency"))
	for _, column := range strings.Split(viper.GetString("money-columns"), ",") {
		if column != "" && column == key {
			return reportCurrency
		}
	}
	if rowCurrency, ok := row["currency"].(string); ok && rowCurrency != "" && moneyNamedColumn(key) {
		return rowCurrency
	}
	return ""
}

// currencySymbols maps ISO codes to their conventional signs; unknown codes
// prefix the code itself ("SEK 1,234").
var currencySymbols = map[string]string{
	"USD": "$", "EUR": "€", "GBP": "£", "ILS": "₪", "JPY": "¥",
	"AUD": "A$", "CAD": "C$", "BRL": "R$", "MXN": "MX$", "SGD": "S$", "TWD": "NT$",
}

// formatMoney renders a monetary amount for humans: currency sign, digit
// grouping, rounded to whole units (cents are noise at cloud-bill scale).
func formatMoney(amount float64, currency string) string {
	rounded := int64(math.Round(math.Abs(amount)))
	grouped := groupDigits(strconv.FormatInt(rounded, 10))
	sign := ""
	if amount < 0 && rounded != 0 {
		sign = "-"
	}
	if symbol, ok := currencySymbols[strings.ToUpper(currency)]; ok {
		return sign + symbol + grouped
	}
	return sign + strings.ToUpper(currency) + " " + grouped
}

// tableCellText renders a raw row value as table cell text, joining arrays
// the same way toon cells do. The column name lets epoch-second values in
// time-named columns render as dates (millisecond epochs are recognized by
// magnitude alone; second epochs overlap plausible numeric data, so they
// convert only when the column name says "time").
func tableCellText(key string, val interface{}) string {
	if s, ok := val.([]interface{}); ok {
		converted := make([]string, len(s))
		for j := range s {
			converted[j] = formatValue(s[j])
		}
		return strings.Join(converted, ", ")
	}
	if !viper.GetBool("raw-numbers") && utcLabelColumn(key) {
		// View-designated UTC label columns (anomaly usage-window boundaries):
		// epoch values become UTC label text, never zone-shifted — an hourly
		// anomaly starting 01:00 UTC must not relabel onto another local day.
		if ms, ok := numericCell(val); ok && ms >= 1e12 && ms < 4.1e12 {
			return utcEpochDateLabel(time.UnixMilli(int64(ms)))
		}
		if sec, ok := numericCell(val); ok && sec >= 1e9 && sec < 4.1e9 {
			return utcEpochDateLabel(time.Unix(int64(sec), 0))
		}
	}
	if !viper.GetBool("raw-numbers") && timeNamedColumn(key) {
		if sec, ok := numericCell(val); ok && sec >= 1e9 && sec < 4.1e9 {
			return prettifyTimestamp(time.Unix(int64(sec), 0).UTC().Format(time.RFC3339))
		}
	}
	return formatTableValue(val)
}

// utcLabelColumn reports whether the view marked this column as a UTC label
// (set by the response transform, e.g. anomaly startTime/endTime).
func utcLabelColumn(key string) bool {
	for _, column := range strings.Split(viper.GetString("utc-label-columns"), ",") {
		if column != "" && column == key {
			return true
		}
	}
	return false
}

func timeNamedColumn(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "time") || strings.Contains(lower, "date")
}

// computeColumnWidths distributes terminal width across columns. Columns that
// fit within an equal share get exactly their content width, freeing surplus
// space for columns that need more. This repeats until stable, so narrow
// columns (dates, IDs) stay compact while wider columns share the remainder.
func computeColumnWidths(contentWidths []int, terminalWidth int, maxColWidth int) []int {
	cols := len(contentWidths)
	if cols <= 0 {
		return nil
	}
	if terminalWidth <= 0 {
		terminalWidth = 120
	}

	overhead := tableOverhead(cols)
	available := terminalWidth - overhead
	if available < cols {
		available = cols
	}

	capped := cappedContentWidths(contentWidths, maxColWidth)
	widths := make([]int, cols)
	settled := make([]bool, cols)
	remaining, unsettled := settleNarrowColumns(widths, settled, capped, available, cols)
	if unsettled > 0 {
		distributeRemainder(widths, settled, remaining, unsettled, maxColWidth)
	}
	// When every column fits its content, leftover terminal width stays
	// unused: stretching short columns to fill wide terminals hurts
	// readability.
	return widths
}

// cappedContentWidths returns content widths capped by maxColWidth (if set).
func cappedContentWidths(contentWidths []int, maxColWidth int) []int {
	capped := make([]int, len(contentWidths))
	for i, cw := range contentWidths {
		if maxColWidth > 0 && cw > maxColWidth {
			cw = maxColWidth
		}
		capped[i] = cw
	}
	return capped
}

// settleNarrowColumns assigns exact content width to columns that fit within
// an equal share, iterating until no more columns can be settled.
func settleNarrowColumns(widths []int, settled []bool, capped []int, available int, unsettled int) (remaining int, unsettledCount int) {
	remaining = available
	for unsettled > 0 {
		share := remaining / unsettled
		changed := false
		for i, cw := range capped {
			if settled[i] || cw > share {
				continue
			}
			widths[i] = cw
			remaining -= cw
			settled[i] = true
			unsettled--
			changed = true
		}
		if !changed {
			break
		}
	}
	return remaining, unsettled
}

// distributeRemainder divides leftover space evenly among unsettled columns.
func distributeRemainder(widths []int, settled []bool, remaining int, unsettled int, maxColWidth int) {
	if unsettled <= 0 {
		return
	}
	share := remaining / unsettled
	rem := remaining % unsettled
	for i := range widths {
		if settled[i] {
			continue
		}
		widths[i] = share
		if rem > 0 {
			widths[i]++
			rem--
		}
		if maxColWidth > 0 && widths[i] > maxColWidth {
			widths[i] = maxColWidth
		}
		if widths[i] < 1 {
			widths[i] = 1
		}
	}
}

// widenPriorityColumn reallocates width toward the view-designated priority
// column (e.g. the insights title, which carries the row's meaning): when the
// even split leaves it narrower than its content, the other columns donate
// their surplus down to a readable floor (their content width, capped at 16),
// widest donor first. Total width is preserved, so the table still fits the
// terminal.
func widenPriorityColumn(keys []string, contentWidths, colWidths []int) {
	priority := strings.TrimSpace(viper.GetString("table-priority-column"))
	if priority == "" {
		return
	}
	target := -1
	for i, k := range keys {
		if k == priority {
			target = i
			break
		}
	}
	if target < 0 {
		return
	}
	deficit := contentWidths[target] - colWidths[target]
	for deficit > 0 {
		donor, donorSurplus := -1, 0
		for i := range colWidths {
			if i == target {
				continue
			}
			floor := contentWidths[i]
			if floor > 16 {
				floor = 16
			}
			if surplus := colWidths[i] - floor; surplus > donorSurplus {
				donor, donorSurplus = i, surplus
			}
		}
		if donor < 0 {
			return
		}
		take := donorSurplus
		if take > deficit {
			take = deficit
		}
		colWidths[donor] -= take
		colWidths[target] += take
		deficit -= take
	}
}

func tableOverhead(cols int) int {
	if cols <= 0 {
		return 0
	}
	// simpletable StyleUnicode: 1 right border + (1 left pad + 2 separator) per column = 1 + 3*cols
	return 1 + 3*cols
}

// formatValue converts a raw cell value to a display string. Large numeric
// values that look like Unix timestamps (milliseconds since epoch, roughly
// 2001–2099) are formatted as ISO 8601 in UTC. Both float64 and int64 are
// handled: integral response numbers are normalized to int64.
func formatValue(val interface{}) string {
	if val == nil {
		return "" // an empty cell, not a literal "<nil>"
	}
	if ms, ok := numericCell(val); ok && ms >= 1e12 && ms < 4.1e12 {
		sec := int64(ms) / 1000
		rem := int64(ms) % 1000
		return time.Unix(sec, rem*1e6).UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%v", val)
}

// schemaColumnType returns the schema type of column i, or "" when unknown.
func schemaColumnType(schema []reportColumn, i int) string {
	if i >= 0 && i < len(schema) {
		return schema[i].Type
	}
	return ""
}

// timestampLabelStyle selects how schema-declared timestamp cells (report
// result period labels) are encoded for a renderer. Labels are UTC in every
// style — the choice is only between machine and human encodings.
type timestampLabelStyle int

const (
	// labelRFC3339 keeps full RFC3339 UTC strings — TOON and CSV.
	labelRFC3339 timestampLabelStyle = iota
	// labelDisplay emits the final human label text ("2026-08-01",
	// "2026-08-09 01:00") — the table. Deliberately shorter than
	// prettifyTimestamp's 20-char pre-check, so label strings can never
	// re-enter the zone-aware instant path.
	labelDisplay
)

// utcEpochDateLabel formats a UTC time as final label text: bare date at
// midnight (daily/monthly grain carries no time information), minute
// precision otherwise or when the result is hourly-grain.
func utcEpochDateLabel(t time.Time) string {
	utc := t.UTC()
	if !viper.GetBool("report-hourly") && utc.Hour() == 0 && utc.Minute() == 0 && utc.Second() == 0 {
		return utc.Format("2006-01-02")
	}
	return utc.Format("2006-01-02 15:04")
}

// displayTimestampCell converts schema-declared timestamp cells (epoch
// seconds) for the human-facing renderers: RFC3339 UTC for TOON/CSV, final
// label text for the table. These are data-bucket labels, never instants —
// they stay UTC in every zone configuration. Machine formats (json, yaml)
// keep the raw epoch values, and --raw-numbers opts table/TOON/CSV
// consumers back into raw epochs too.
func displayTimestampCell(cell interface{}, colType string, style timestampLabelStyle) interface{} {
	if viper.GetBool("raw-numbers") {
		return cell
	}
	if colType != "timestamp" && colType != "datetime" {
		return cell
	}
	sec, ok := numericCell(cell)
	if !ok || sec < 1e9 || sec >= 4.1e9 {
		return cell
	}
	t := time.Unix(int64(sec), 0).UTC()
	if style == labelDisplay {
		return utcEpochDateLabel(t)
	}
	return t.Format(time.RFC3339)
}

// displayTimestampFields applies displayTimestampCell to keyed rows.
func displayTimestampFields(row map[string]interface{}, schema []reportColumn, style timestampLabelStyle) map[string]interface{} {
	if len(schema) == 0 {
		return row
	}
	out := make(map[string]interface{}, len(row))
	for k, v := range row {
		out[k] = v
	}
	for _, col := range schema {
		if v, ok := out[col.Name]; ok {
			out[col.Name] = displayTimestampCell(v, col.Type, style)
		}
	}
	return out
}

// formatTableValue renders a cell for table output: decimal numbers get digit
// grouping and two decimals, integral numbers group without decimals, and
// epoch-millisecond timestamps become ISO dates (the table pipeline
// roundtrips through JSON, so int64 normalization does not survive here).
// --raw-numbers disables all of it.
func formatTableValue(val interface{}) string {
	if viper.GetBool("raw-numbers") {
		return formatValue(val)
	}
	switch v := val.(type) {
	case string:
		return prettifyTimestamp(v)
	case float64:
		if v >= 1e12 && v < 4.1e12 {
			return prettifyTimestamp(formatValue(v)) // epoch milliseconds
		}
		if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
			return groupDigits(strconv.FormatInt(int64(v), 10))
		}
		return groupDigits(fmt.Sprintf("%.2f", v))
	default:
		return prettifyTimestamp(formatValue(val))
	}
}

// displayTimeLocation is the zone instants render in for the current
// invocation, resolved once in the dci PersistentPreRunE. It stays nil
// outside a normal command run (tests, internal calls), where UTC keeps
// output deterministic — same fallback rationale as shouldPivotReportRows'
// empty-output-format check.
var displayTimeLocation *time.Location

// localizedInstantShown records that at least one instant rendered with a
// non-zero UTC offset this invocation, so the one-line zone note on stderr
// only appears when the output actually differs from UTC.
var localizedInstantShown bool

// resolveDisplayLocation picks the instant-display zone: DCI_TZ (IANA name)
// when set, otherwise the system zone (time.Local, which already honors TZ).
// An invalid DCI_TZ warns once and falls back to the system zone rather than
// silently changing what the user asked for.
func resolveDisplayLocation(dciTZ string) *time.Location {
	dciTZ = strings.TrimSpace(dciTZ)
	if dciTZ == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(dciTZ)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: ignoring invalid DCI_TZ=%q (use an IANA zone name like Europe/Berlin); using the system zone\n", dciTZ)
		return time.Local
	}
	return loc
}

// displayLocation returns the zone instants render in. UTC for every
// machine-consumed context — agent mode, json/yaml/csv/toon, --utc, and
// pipelines running outside a normal command — so only the human table view
// ever sees machine-local times.
func displayLocation() *time.Location {
	if displayTimeLocation == nil || agentMode || viper.GetBool("display-utc") {
		return time.UTC
	}
	switch strings.TrimSpace(viper.GetString("rsh-output-format")) {
	case "table", "auto":
		return displayTimeLocation
	default:
		return time.UTC
	}
}

// maybeNoteDisplayZone emits the once-per-invocation stderr note after a
// table render that actually localized an instant, so users always know
// which zone they are reading and how to get UTC back.
func maybeNoteDisplayZone() {
	if !localizedInstantShown || cli.Stderr == nil {
		return
	}
	localizedInstantShown = false
	loc := displayLocation()
	if loc == time.UTC {
		return
	}
	name := loc.String()
	if name == "Local" {
		name = "local time"
	}
	_, offset := time.Now().In(loc).Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	fmt.Fprintf(cli.Stderr, "note: times shown in %s (UTC%s%02d:%02d); pass --utc for UTC\n",
		name, sign, offset/3600, (offset%3600)/60)
}

// prettifyTimestamp renders RFC3339 strings for human eyes. Values at
// exactly midnight UTC are date-valued (calendar dates: contract terms,
// invoice dates, budget periods, daily report grain) — they become a bare
// UTC date and are never zone-shifted. Anything with a real time-of-day is
// an instant and renders in displayLocation() at minute precision. Hourly
// report results keep the time even at midnight — the resolution is part of
// the data. Non-timestamp strings pass through untouched; machine formats
// (json, yaml, csv, toon) never see this — they keep full RFC3339.
func prettifyTimestamp(s string) string {
	if len(s) < 20 || s[4] != '-' || s[10] != 'T' {
		return s // cheap pre-check before parsing
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	utc := parsed.UTC()
	if utc.Hour() == 0 && utc.Minute() == 0 && utc.Second() == 0 {
		if viper.GetBool("report-hourly") {
			return utc.Format("2006-01-02 15:04")
		}
		return utc.Format("2006-01-02")
	}
	local := utc.In(displayLocation())
	if _, offset := local.Zone(); offset != 0 {
		localizedInstantShown = true
	}
	return local.Format("2006-01-02 15:04")
}

// groupDigits inserts thousands separators into the integer part of a
// formatted decimal number.
func groupDigits(s string) string {
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, digit := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(digit)
	}
	out := b.String()
	if hasFrac {
		out += "." + fracPart
	}
	if neg {
		out = "-" + out
	}
	return out
}

func buildTableString(rows []map[string]interface{}, keys []string, colWidths []int, mode string) (string, error) {
	if len(keys) == 0 {
		return "No results\n", nil
	}
	if len(colWidths) != len(keys) {
		return "", fmt.Errorf("internal error: mismatched column widths")
	}

	table := simpletable.New()

	// Pad headers with U+2800 (Braille Pattern Blank) to enforce column widths.
	// simpletable auto-sizes columns to the widest cell; since its newContent
	// calls strings.TrimSpace (which strips regular spaces), we use U+2800 which
	// is not considered whitespace by Go's unicode.IsSpace. Body cells are left
	// unpadded so simpletable's AlignRight can position them within the column.
	header := make([]*simpletable.Cell, 0, len(keys))
	for i, k := range keys {
		header = append(header, &simpletable.Cell{
			Align: simpletable.AlignCenter,
			Text:  padCell(truncateText(k, colWidths[i]), colWidths[i]),
		})
	}
	table.Header = &simpletable.Header{Cells: header}

	heat := newHeatmap(rows, keys)
	accentColumn, accentFlagKey := tableAccentConfig()
	linkColumn, linkURLKey := tableLinkConfig()
	links := &tableLinks{}
	for rowIndex, row := range rows {
		body := make([]*simpletable.Cell, 0, len(keys))
		for i, k := range keys {
			val := row[k]
			cellText := renderCellText(row, k)
			if linkColumn != "" {
				// Stray marker runes in real data would pair with a link
				// marker and misplace a hyperlink.
				cellText = stripLinkMarkers(cellText)
			}
			cellText = formatCell(cellText, colWidths[i], mode)
			if heat != nil {
				cellText = heat.colorize(rowIndex, k, val, cellText)
			}
			if k == accentColumn {
				cellText = accentCell(row, accentFlagKey, cellText)
			}
			if k == linkColumn {
				cellText = links.mark(row, linkURLKey, cellText)
			}
			// Numbers and money align right for magnitude comparison; text
			// reads left.
			align := simpletable.AlignLeft
			if _, isNumeric := numericCell(val); isNumeric || moneyText(val) {
				align = simpletable.AlignRight
			}
			body = append(body, &simpletable.Cell{Align: align, Text: cellText})
		}
		table.Body.Cells = append(table.Body.Cells, body)
	}

	table.SetStyle(simpletable.StyleUnicode)
	out := table.String()
	// Replace the U+2800 padding placeholder with real spaces.
	out = strings.ReplaceAll(out, "\u2800", " ")
	return links.apply(out), nil
}

// tableAccentConfig returns the view-designated accent: the column to color
// and the hidden per-row flag key that selects which rows get it (e.g. the
// insights easy-win title highlight). Empty when no accent is configured or
// color is off for this invocation (agent mode, non-TTY, NO_COLOR).
func tableAccentConfig() (column, flagKey string) {
	if !viper.GetBool("table-color") {
		return "", ""
	}
	column = strings.TrimSpace(viper.GetString("table-accent-column"))
	flagKey = strings.TrimSpace(viper.GetString("table-accent-flag-key"))
	if column == "" || flagKey == "" {
		return "", ""
	}
	return column, flagKey
}

// accentCell colors a cell green (bold) when the row's accent flag is a
// non-empty string. Applied after width formatting, like the heatmap, so the
// escape codes never count against the column width.
func accentCell(row map[string]interface{}, flagKey, cellText string) string {
	flag, _ := row[flagKey].(string)
	flag = strings.TrimSpace(flag)
	if flag == "" {
		return cellText
	}
	if band, ok := strings.CutPrefix(flag, "utilization-"); ok {
		return accentUtilizationCell(cellText, band)
	}
	if flag == "accent-red" {
		return "\x1b[1;31m" + cellText + "\x1b[0m"
	}
	return "\x1b[1;32m" + cellText + "\x1b[0m"
}

// accentUtilizationCell colors a utilization bar in bands: the filled cells
// run green through amber into red as the bar climbs (deciles 1–7, 8–9, 10 —
// the thresholds the risk accent already used), the empty cells dim, and the
// percent text takes the row's own band. Basic ANSI colors on purpose: cells
// print raw to stdout with no downsampling writer in front of them.
func accentUtilizationCell(cellText, band string) string {
	percentColor, ok := map[string]string{"green": "32", "amber": "33", "red": "31"}[band]
	if !ok {
		return cellText
	}
	var b strings.Builder
	filled := 0
	for _, r := range cellText {
		switch {
		case r == '▓':
			cellColor := "32"
			if filled >= 9 {
				cellColor = "31"
			} else if filled >= 7 {
				cellColor = "33"
			}
			b.WriteString("\x1b[" + cellColor + "m▓\x1b[0m")
			filled++
		case r == '░':
			b.WriteString("\x1b[2m░\x1b[0m")
		case r == ' ':
			b.WriteRune(r)
		default: // the percent text
			b.WriteString("\x1b[1;" + percentColor + "m" + string(r) + "\x1b[0m")
		}
	}
	return b.String()
}

// tableLinkConfig returns the view-designated hyperlink: the column whose
// cells link out and the per-row key holding the URL (e.g. the insights
// title linking to reportUrl). Gated like the accent — OSC 8 hyperlinks are
// for interactive human terminals only.
func tableLinkConfig() (column, urlKey string) {
	if !viper.GetBool("table-color") {
		return "", ""
	}
	column = strings.TrimSpace(viper.GetString("table-link-column"))
	urlKey = strings.TrimSpace(viper.GetString("table-link-url-key"))
	if column == "" || urlKey == "" {
		return "", ""
	}
	return column, urlKey
}

// Link cells cannot carry their OSC 8 escape sequences through simpletable:
// its width math strips CSI color codes (the heatmap and accent SGR codes)
// but not OSC sequences, so an in-cell URL would count toward the measured
// column width and shear every border after it. Instead, linked cells are
// wrapped in zero-width marker runes — invisible to go-runewidth, so
// alignment is unaffected — with the URLs queued in render order, and apply
// swaps each marked segment for the real hyperlink after the table string is
// built.
const (
	linkStartMarker = "​" // zero width space
	linkEndMarker   = "‌" // zero width non-joiner
)

type tableLinks struct {
	urls []string
}

// mark wraps each non-blank line of a linked cell in marker runes and queues
// the row's URL once per marked line: wrap mode splits a cell across output
// lines, and each must become its own hyperlink — one link spanning the
// newline would swallow the table borders between them.
func (l *tableLinks) mark(row map[string]interface{}, urlKey, cellText string) string {
	url, _ := row[urlKey].(string)
	url = strings.TrimSpace(url)
	if url == "" {
		return cellText
	}
	lines := strings.Split(cellText, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		l.urls = append(l.urls, url)
		lines[i] = linkStartMarker + line + linkEndMarker
	}
	return strings.Join(lines, "\n")
}

// apply replaces the marked segments with OSC 8 hyperlinks, consuming the
// queued URLs in order: simpletable renders rows in the order they were
// added, so marker pairs appear in the output in queue order.
func (l *tableLinks) apply(out string) string {
	if len(l.urls) == 0 {
		return out
	}
	var b strings.Builder
	rest := out
	for _, url := range l.urls {
		start := strings.Index(rest, linkStartMarker)
		if start < 0 {
			break
		}
		length := strings.Index(rest[start:], linkEndMarker)
		if length < 0 {
			break
		}
		text := rest[start+len(linkStartMarker) : start+length]
		b.WriteString(rest[:start])
		b.WriteString("\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\")
		rest = rest[start+length+len(linkEndMarker):]
	}
	b.WriteString(rest)
	return b.String()
}

func stripLinkMarkers(s string) string {
	s = strings.ReplaceAll(s, linkStartMarker, "")
	return strings.ReplaceAll(s, linkEndMarker, "")
}

// moneyText reports whether a cell is a pre-formatted monetary string
// ("$1,234.56", "-$3.20") so money columns right-align like numeric ones.
func moneyText(val interface{}) bool {
	s, ok := val.(string)
	if !ok {
		return false
	}
	s = strings.TrimPrefix(s, "-")
	if !strings.HasPrefix(s, "$") {
		return false
	}
	s = strings.ReplaceAll(strings.TrimPrefix(s, "$"), ",", "")
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

type tableHeatmap struct {
	max        float64
	totalsRows map[int]bool
	periodSet  map[string]bool
}

var heatRamp = []int{22, 28, 100, 166, 124}

func newHeatmap(rows []map[string]interface{}, keys []string) *tableHeatmap {
	if !viper.GetBool("heatmap") || !viper.GetBool("pivot-active") || len(rows) < 2 {
		return nil
	}
	periodSet := map[string]bool{}
	for _, k := range keys {
		// Period columns are the date-shaped ones ("2026-05", "2026-08-09 01:00").
		if len(k) >= 7 && k[4] == '-' {
			periodSet[k] = true
		}
	}
	if len(periodSet) == 0 {
		return nil
	}
	heat := &tableHeatmap{totalsRows: map[int]bool{}, periodSet: periodSet}
	totalRowStart := len(rows) - viper.GetInt("pivot-total-rows")
	for rowIndex, row := range rows {
		if totalRowStart >= 0 && rowIndex >= totalRowStart {
			heat.totalsRows[rowIndex] = true
			continue
		}
		for k := range periodSet {
			if v, ok := numericCell(row[k]); ok && math.Abs(v) > heat.max {
				heat.max = math.Abs(v)
			}
		}
	}
	if heat.max <= 0 {
		return nil
	}
	return heat
}

func (h *tableHeatmap) colorize(rowIndex int, key string, val interface{}, cellText string) string {
	if h.totalsRows[rowIndex] || !h.periodSet[key] {
		return cellText
	}
	v, ok := numericCell(val)
	if !ok || v == 0 {
		return cellText
	}
	// Square-root scaling keeps skewed cost distributions from washing out
	// the lower buckets.
	bucket := int(math.Sqrt(math.Abs(v)/h.max) * float64(len(heatRamp)))
	if bucket >= len(heatRamp) {
		bucket = len(heatRamp) - 1
	}
	return fmt.Sprintf("\x1b[48;5;%d;38;5;231m%s\x1b[0m", heatRamp[bucket], cellText)
}

func padCell(s string, width int) string {
	if width <= 0 {
		return s
	}
	cur := runewidth.StringWidth(s)
	if cur >= width {
		return s
	}
	// Use Braille Pattern Blank (U+2800) instead of spaces because simpletable's
	// newContent calls strings.TrimSpace on cell text, which would strip regular
	// space padding and cause columns to shrink to content width. U+2800 is not
	// considered whitespace by Go's unicode.IsSpace, so it survives the trim.
	// buildTableString replaces U+2800 back to spaces in the final output.
	return s + strings.Repeat("\u2800", width-cur)
}

// fitColumnsToTerminal keeps the leading columns that can render at a
// readable width within the terminal, hiding the rest. Column content width
// is capped for the fit decision so one very wide column (a URL, a long
// name) doesn't evict everything after it.
const fitColumnContentCap = 28

// fitPriorityColumns always survive the fit before other columns are
// considered — hiding what a row is (id, name) or when it happened
// (startTime, createTime) helps nobody.
var fitPriorityColumns = map[string]bool{"id": true, "name": true, "startTime": true, "createTime": true, "total": true, "trend": true}

func fitColumnsToTerminal(rows []map[string]interface{}, keys []string, terminalWidth int) (visible, hidden []string) {
	if terminalWidth <= 0 {
		terminalWidth = 120
	}
	contentW := measureContentWidths(rows, keys)
	cappedWidth := func(i int) int {
		if contentW[i] > fitColumnContentCap {
			return fitColumnContentCap
		}
		return contentW[i]
	}

	kept := map[string]bool{}
	used := 0
	count := 0
	allocate := func(i int, key string) {
		width := cappedWidth(i)
		if count > 0 && used+width+tableOverhead(count+1) > terminalWidth {
			return
		}
		kept[key] = true
		used += width
		count++
	}
	for i, key := range keys {
		if fitPriorityColumns[key] {
			allocate(i, key)
		}
	}
	for i, key := range keys {
		if !fitPriorityColumns[key] {
			allocate(i, key)
		}
	}

	for _, key := range keys {
		if kept[key] {
			visible = append(visible, key)
		} else {
			hidden = append(hidden, key)
		}
	}
	return visible, hidden
}

// filterObjectColumns splits keys into visible and hidden. A column is hidden
// if any row contains a nested object (map) either directly or inside an array.
func filterObjectColumns(rows []map[string]interface{}, keys []string) (visible, hidden []string) {
	for _, k := range keys {
		isObject := false
		for _, row := range rows {
			if containsObject(row[k]) {
				isObject = true
				break
			}
		}
		if isObject {
			hidden = append(hidden, k)
		} else {
			visible = append(visible, k)
		}
	}
	return visible, hidden
}

// containsObject returns true if val is a map or an array containing a map.
func containsObject(val interface{}) bool {
	switch v := val.(type) {
	case map[string]interface{}:
		return true
	case []interface{}:
		for _, item := range v {
			if _, ok := item.(map[string]interface{}); ok {
				return true
			}
		}
	}
	return false
}

func collectKeys(rows []map[string]interface{}, preferred []string) []string {
	if len(preferred) > 0 {
		return preferred
	}

	keys := make([]string, 0, 16)
	seen := map[string]bool{}
	for _, row := range rows {
		for k := range row {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

func formatCell(val string, width int, mode string) string {
	if width <= 0 {
		return val
	}
	switch mode {
	case "wrap":
		return wrapText(val, width)
	default:
		return truncateText(val, width)
	}
}

// wrapText soft-wraps at word boundaries when possible, falling back to a
// hard break only for words wider than the column, so wrapped cells never
// split words mid-token.
func wrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		lines = append(lines, wrapLine(paragraph, width)...)
	}
	return strings.Join(lines, "\n")
}

func wrapLine(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{s}
	}
	lines := []string{}
	current := ""
	currentWidth := 0
	flush := func() {
		if current != "" {
			lines = append(lines, current)
			current = ""
			currentWidth = 0
		}
	}
	for _, word := range words {
		wordWidth := runewidth.StringWidth(word)
		if wordWidth > width {
			// A single over-wide token (URL, ID) must hard-break.
			flush()
			lines = append(lines, hardBreak(word, width)...)
			if len(lines) > 0 {
				last := lines[len(lines)-1]
				lines = lines[:len(lines)-1]
				current = last
				currentWidth = runewidth.StringWidth(last)
			}
			continue
		}
		separator := 0
		if current != "" {
			separator = 1
		}
		if currentWidth+separator+wordWidth > width {
			flush()
			separator = 0
		}
		if separator == 1 {
			current += " "
			currentWidth++
		}
		current += word
		currentWidth += wordWidth
	}
	flush()
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func hardBreak(s string, width int) []string {
	var lines []string
	var current strings.Builder
	currentWidth := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if rw < 0 {
			rw = 0
		}
		if currentWidth+rw > width && current.Len() > 0 {
			lines = append(lines, current.String())
			current.Reset()
			currentWidth = 0
		}
		current.WriteRune(r)
		currentWidth += rw
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func truncateText(s string, width int) string {
	if width <= 0 {
		return s
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}

	ellipsis := "…"
	ellipsisWidth := runewidth.StringWidth(ellipsis)
	if width <= ellipsisWidth {
		return ellipsis
	}

	// Leave room for ellipsis.
	target := width - ellipsisWidth
	if target < 1 {
		target = 1
	}
	var b strings.Builder
	curWidth := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if rw < 0 {
			rw = 0
		}
		if curWidth+rw > target {
			break
		}
		b.WriteRune(r)
		curWidth += rw
	}
	b.WriteString(ellipsis)
	return b.String()
}
