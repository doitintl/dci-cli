package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func TestCompletionPositionalWords(t *testing.T) {
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("output", "", "")
	flags.StringP("customer-context", "D", "", "")
	flags.Bool("yes", false, "")
	flags.BoolP("pivot", "P", false, "")

	for _, testCase := range []struct {
		name        string
		words       []string
		wantCommand string
		wantWords   []string
		wantOK      bool
	}{
		{name: "bare command", words: []string{"get-report"}, wantCommand: "get-report", wantOK: true},
		{name: "one positional", words: []string{"get-report", "monthly"}, wantCommand: "get-report", wantWords: []string{"monthly"}, wantOK: true},
		{name: "known value flag skips its value", words: []string{"get-report", "--output", "json"}, wantCommand: "get-report", wantOK: true},
		{name: "known value flag with equals", words: []string{"get-report", "--output=json"}, wantCommand: "get-report", wantOK: true},
		{name: "bool flag consumes nothing", words: []string{"get-report", "--yes", "monthly"}, wantCommand: "get-report", wantWords: []string{"monthly"}, wantOK: true},
		{name: "short value flag skips its value", words: []string{"get-report", "-D", "acme.com"}, wantCommand: "get-report", wantOK: true},
		{name: "short bool flag consumes nothing", words: []string{"get-report", "-P", "monthly"}, wantCommand: "get-report", wantWords: []string{"monthly"}, wantOK: true},
		{name: "interleaved flags and positionals", words: []string{"--output", "json", "get-report", "--yes", "arg1", "-D", "acme.com", "arg2"}, wantCommand: "get-report", wantWords: []string{"arg1", "arg2"}, wantOK: true},
		{name: "unknown flag assumed to take the next value", words: []string{"get-report", "--filter", "owner:me"}, wantCommand: "get-report", wantOK: true},
		{name: "unknown flag followed by a flag consumes nothing", words: []string{"get-report", "--full", "--yes"}, wantCommand: "get-report", wantOK: true},
		{name: "dangling known value flag owns the completion word", words: []string{"get-report", "--output"}, wantOK: false},
		{name: "dangling short value flag owns the completion word", words: []string{"get-report", "-D"}, wantOK: false},
		{name: "dangling unknown flag owns the completion word", words: []string{"get-report", "--filter"}, wantOK: false},
		{name: "dangling bool flag leaves the word positional", words: []string{"get-report", "--yes"}, wantCommand: "get-report", wantOK: true},
		{name: "terminator makes everything positional", words: []string{"get-report", "--", "--output"}, wantCommand: "get-report", wantWords: []string{"--output"}, wantOK: true},
		{name: "space-split name words stay in order", words: []string{"get-report", "Tom", "Playground1", "only"}, wantCommand: "get-report", wantWords: []string{"Tom", "Playground1", "only"}, wantOK: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			command, positionals, ok := completionPositionalWords(testCase.words, flags)
			if ok != testCase.wantOK {
				t.Fatalf("ok = %t, want %t", ok, testCase.wantOK)
			}
			if !testCase.wantOK {
				return
			}
			if command != testCase.wantCommand {
				t.Fatalf("command = %q, want %q", command, testCase.wantCommand)
			}
			if strings.Join(positionals, "\x00") != strings.Join(testCase.wantWords, "\x00") {
				t.Fatalf("positionals = %q, want %q", positionals, testCase.wantWords)
			}
		})
	}
}

func TestNameCacheFreshnessTiers(t *testing.T) {
	now := time.Now()
	for _, testCase := range []struct {
		name      string
		fetchedAt time.Time
		want      nameCacheState
	}{
		{"just written", now.Add(-time.Minute), nameCacheFresh},
		{"at the fresh boundary", now.Add(-nameCacheFreshTTL), nameCacheFresh},
		{"stale but servable", now.Add(-time.Hour), nameCacheStale},
		{"beyond the servable window", now.Add(-25 * time.Hour), nameCacheAbsent},
		{"future timestamp counts as absent", now.Add(time.Hour), nameCacheAbsent},
	} {
		if got := nameCacheFreshness(testCase.fetchedAt, now); got != testCase.want {
			t.Errorf("%s: freshness = %d, want %d", testCase.name, got, testCase.want)
		}
	}
}

func TestNameCacheReadWriteRoundtrip(t *testing.T) {
	configDir := t.TempDir()
	cache := nameCacheFile{
		Version:   nameCacheVersion,
		Context:   "acme.com",
		FetchedAt: time.Now(),
		Resources: map[string][]nameCacheEntry{"reports": {{ID: "r-1", Name: "Monthly Spend"}}},
	}
	if err := writeNameCache(configDir, cache); err != nil {
		t.Fatal(err)
	}
	path := nameCachePath(configDir, "acme.com")
	assertPrivateFilePerms(t, path)

	read, state := readNameCache(configDir, "acme.com", time.Now())
	if state != nameCacheFresh || read.Resources["reports"][0].Name != "Monthly Spend" {
		t.Fatalf("read = %+v, state = %d", read, state)
	}

	if _, state := readNameCache(configDir, "other.com", time.Now()); state != nameCacheAbsent {
		t.Fatal("different context read the same cache file")
	}
	if nameCachePath(configDir, "acme.com") == nameCachePath(configDir, "other.com") {
		t.Fatal("contexts hash to the same cache path")
	}

	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, state := readNameCache(configDir, "acme.com", time.Now()); state != nameCacheAbsent {
		t.Fatal("unknown cache version accepted")
	}
}

func TestPurgeNameCaches(t *testing.T) {
	configDir := t.TempDir()
	for _, context := range []string{"acme.com", "other.com"} {
		if err := writeNameCache(configDir, nameCacheFile{Version: nameCacheVersion, Context: context, FetchedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(configDir, "update_check.json")
	if err := os.WriteFile(keep, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := purgeNameCaches(configDir); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(configDir, "names-*.json"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("leftover caches = %v, err = %v", matches, err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated config file removed: %v", err)
	}
}

func configureCompletionPreflightTest(t *testing.T, api cli.API, specCached bool) (*int, string) {
	t.Helper()
	// Point HOME as well as XDG_CONFIG_HOME at the temp dir: os.UserConfigDir
	// only honors XDG_CONFIG_HOME on Linux, so dciConfigDir() must be asked for
	// the effective cache directory rather than assuming the XDG layout.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	configDir := dciConfigDir()
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}

	previousCache := invocationCachedSpecAvailable
	previousLoader := loadOperationAPI
	previousSpawn := spawnNameCacheRefresh
	previousContext := resolvedCustomerContext
	previousFlag := customerContextFlagValue
	previousRoot := cli.Root
	invocationCachedSpecAvailable = func() bool { return specCached }
	loadOperationAPI = func(entrypoint string, root *cobra.Command) (cli.API, error) { return api, nil }
	refreshCount := 0
	spawnNameCacheRefresh = func() { refreshCount++ }
	resolvedCustomerContext = "acme.com"
	customerContextFlagValue = ""
	cli.Root = &cobra.Command{Use: "dci"}
	t.Cleanup(func() {
		invocationCachedSpecAvailable = previousCache
		loadOperationAPI = previousLoader
		spawnNameCacheRefresh = previousSpawn
		resolvedCustomerContext = previousContext
		customerContextFlagValue = previousFlag
		cli.Root = previousRoot
	})
	return &refreshCount, configDir
}

func captureCompletionOutput(t *testing.T, run func()) string {
	t.Helper()
	previousStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	run()
	os.Stdout = previousStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func writeCompletionNameCache(t *testing.T, configDir string, fetchedAt time.Time) {
	t.Helper()
	err := writeNameCache(configDir, nameCacheFile{
		Version:   nameCacheVersion,
		Context:   "acme.com",
		FetchedAt: fetchedAt,
		Resources: map[string][]nameCacheEntry{"reports": {
			{ID: "r-1", Name: "Monthly AWS Spend"},
			{ID: "r-2", Name: "monthly gcp spend"},
			{ID: "r-3", Name: "Quarterly Overview"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompletionPreflightServesFreshCacheWithPrefixFilter(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}
	refreshCount, configDir := configureCompletionPreflightTest(t, api, true)
	writeCompletionNameCache(t, configDir, time.Now())

	var handled bool
	var exitCode int
	output := captureCompletionOutput(t, func() {
		handled, exitCode = completionPreflight([]string{"dci", "__complete", "dci", "get-report", "Mon"})
	})
	if !handled || exitCode != 0 {
		t.Fatalf("handled = %t, exit = %d", handled, exitCode)
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	want := []string{"Monthly AWS Spend\tr-1", "monthly gcp spend\tr-2", ":4"}
	if len(lines) != len(want) {
		t.Fatalf("output = %q", output)
	}
	for index := range want {
		if lines[index] != want[index] {
			t.Fatalf("line %d = %q, want %q", index, lines[index], want[index])
		}
	}
	if *refreshCount != 0 {
		t.Fatalf("fresh cache spawned %d refreshes", *refreshCount)
	}
}

func TestCompletionPreflightNoDescOmitsIDs(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}
	_, configDir := configureCompletionPreflightTest(t, api, true)
	writeCompletionNameCache(t, configDir, time.Now())

	output := captureCompletionOutput(t, func() {
		completionPreflight([]string{"dci", "__completeNoDesc", "dci", "get-report", "Quar"})
	})
	if output != "Quarterly Overview\n:4\n" {
		t.Fatalf("output = %q", output)
	}
}

// TestCompletionPreflightCompletesSpaceSplitNames covers the shell scripts'
// word splitting: zsh (`${=words}`), bash (COMP_WORDS), and fish
// (commandline -opc) all deliver an unquoted in-progress name as separate
// words, so candidates are matched against the rejoined words and emitted as
// the tail from the current word on. Each match also emits the full name: a
// quoted in-progress name arrives identically split (the scripts eval a flat
// request string), but the shell filters against the whole dequoted word,
// which only the full name can match.
func TestCompletionPreflightCompletesSpaceSplitNames(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}
	for _, testCase := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "mid-word tail",
			args: []string{"dci", "__complete", "dci", "get-report", "Monthly", "A"},
			want: []string{"AWS Spend\tr-1", "Monthly AWS Spend\tr-1", ":4"},
		},
		{
			name: "empty current word after a space",
			args: []string{"dci", "__complete", "dci", "get-report", "Monthly", ""},
			want: []string{"AWS Spend\tr-1", "Monthly AWS Spend\tr-1", "gcp spend\tr-2", "monthly gcp spend\tr-2", ":4"},
		},
		{
			name: "current word filters the tail",
			args: []string{"dci", "__complete", "dci", "get-report", "monthly", "g"},
			want: []string{"gcp spend\tr-2", "monthly gcp spend\tr-2", ":4"},
		},
		{
			name: "two preceding words",
			args: []string{"dci", "__complete", "dci", "get-report", "Monthly", "AWS", "Sp"},
			want: []string{"Spend\tr-1", "Monthly AWS Spend\tr-1", ":4"},
		},
		{
			name: "fully typed name leaves nothing to complete",
			args: []string{"dci", "__complete", "dci", "get-report", "Monthly", "AWS", "Spend", ""},
			want: []string{":4"},
		},
		{
			name: "flags between the words are skipped",
			args: []string{"dci", "__complete", "dci", "get-report", "Monthly", "--output", "json", "A"},
			want: []string{"AWS Spend\tr-1", "Monthly AWS Spend\tr-1", ":4"},
		},
		{
			name: "no-desc omits ids from tails",
			args: []string{"dci", "__completeNoDesc", "dci", "get-report", "Monthly", "A"},
			want: []string{"AWS Spend", "Monthly AWS Spend", ":4"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, configDir := configureCompletionPreflightTest(t, api, true)
			writeCompletionNameCache(t, configDir, time.Now())
			var handled bool
			output := captureCompletionOutput(t, func() {
				handled, _ = completionPreflight(testCase.args)
			})
			if !handled {
				t.Fatal("space-split name completion not handled")
			}
			got := strings.Split(strings.TrimRight(output, "\n"), "\n")
			if strings.Join(got, "\x00") != strings.Join(testCase.want, "\x00") {
				t.Fatalf("output lines = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestTrimPrefixFold(t *testing.T) {
	for _, testCase := range []struct {
		s, prefix, want string
		wantOK          bool
	}{
		{"Monthly AWS Spend", "Monthly ", "AWS Spend", true},
		{"Monthly AWS Spend", "monthly ", "AWS Spend", true},
		{"MONTHLY AWS Spend", "monthly ", "AWS Spend", true},
		{"Monthly", "Monthly ", "", false},
		{"Quarterly Overview", "Monthly ", "", false},
		{"Ödeme Raporu", "ödeme ", "Raporu", true},
	} {
		got, ok := trimPrefixFold(testCase.s, testCase.prefix)
		if ok != testCase.wantOK || got != testCase.want {
			t.Errorf("trimPrefixFold(%q, %q) = %q, %t; want %q, %t",
				testCase.s, testCase.prefix, got, ok, testCase.want, testCase.wantOK)
		}
	}
}

func TestCompletionPreflightStaleCacheServesAndRefreshes(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}
	refreshCount, configDir := configureCompletionPreflightTest(t, api, true)
	writeCompletionNameCache(t, configDir, time.Now().Add(-time.Hour))

	output := captureCompletionOutput(t, func() {
		completionPreflight([]string{"dci", "__complete", "dci", "get-report", "Quar"})
	})
	if !strings.Contains(output, "Quarterly Overview\tr-3") || !strings.HasSuffix(output, ":4\n") {
		t.Fatalf("output = %q", output)
	}
	if *refreshCount != 1 {
		t.Fatalf("stale cache spawned %d refreshes", *refreshCount)
	}
}

// setCompletionAuthState pins cli.Cache to a known token state for the
// cold-cache ActiveHelp message selection.
func setCompletionAuthState(t *testing.T, token, refresh string, expires time.Time) {
	t.Helper()
	previousCache := cli.Cache
	cli.Cache = viper.New()
	if token != "" {
		cli.Cache.Set("dci:default.token", token)
		cli.Cache.Set("dci:default.refresh", refresh)
		if !expires.IsZero() {
			cli.Cache.Set("dci:default.expires", expires.Format(time.RFC3339))
		}
	}
	t.Cleanup(func() { cli.Cache = previousCache })
}

func TestCompletionPreflightColdCacheArmsRefresh(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}
	for _, testCase := range []struct {
		name  string
		setup func(t *testing.T)
		want  string
	}{
		{
			name:  "valid token promises the fetch",
			setup: func(t *testing.T) { setCompletionAuthState(t, "token", "refresh", time.Now().Add(time.Hour)) },
			want:  "_activeHelp_ Fetching resource names in the background; press Tab again in a few seconds\n:4\n",
		},
		{
			name: "api key promises the fetch",
			setup: func(t *testing.T) {
				setCompletionAuthState(t, "", "", time.Time{})
				t.Setenv("DCI_API_KEY", "api-key")
			},
			want: "_activeHelp_ Fetching resource names in the background; press Tab again in a few seconds\n:4\n",
		},
		{
			name:  "no credentials points at login",
			setup: func(t *testing.T) { setCompletionAuthState(t, "", "", time.Time{}) },
			want:  "_activeHelp_ Not signed in; run dci login, then press Tab again\n:4\n",
		},
		{
			name:  "expired token points at a refreshing command",
			setup: func(t *testing.T) { setCompletionAuthState(t, "token", "refresh", time.Now().Add(-time.Hour)) },
			want:  "_activeHelp_ Session expired; run any dci command (e.g. dci validate) to refresh it, then press Tab again\n:4\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			refreshCount, _ := configureCompletionPreflightTest(t, api, true)
			t.Setenv("DCI_ACTIVE_HELP", "")
			t.Setenv("DCI_API_KEY", "")
			testCase.setup(t)

			output := captureCompletionOutput(t, func() {
				handled, _ := completionPreflight([]string{"dci", "__complete", "dci", "get-report", ""})
				if !handled {
					t.Error("cold name cache not handled")
				}
			})
			if output != testCase.want {
				t.Fatalf("output = %q, want %q", output, testCase.want)
			}
			if *refreshCount != 1 {
				t.Fatalf("cold cache spawned %d refreshes", *refreshCount)
			}
		})
	}
}

func TestCompletionPreflightColdCacheHonorsActiveHelpOptOut(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}
	_, _ = configureCompletionPreflightTest(t, api, true)
	setCompletionAuthState(t, "token", "refresh", time.Now().Add(time.Hour))
	t.Setenv("DCI_ACTIVE_HELP", "0")

	output := captureCompletionOutput(t, func() {
		completionPreflight([]string{"dci", "__complete", "dci", "get-report", ""})
	})
	if output != ":4\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestCompletionPreflightFallsThrough(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}
	for _, testCase := range []struct {
		name       string
		args       []string
		specCached bool
	}{
		{name: "not a completion invocation", args: []string{"dci", "dci", "get-report", "monthly"}, specCached: true},
		{name: "root-level completion", args: []string{"dci", "__complete", "status", ""}, specCached: true},
		{name: "unresolvable operation", args: []string{"dci", "__complete", "dci", "list-reports", ""}, specCached: true},
		{name: "second positional on a body operation", args: []string{"dci", "__complete", "dci", "update-report", "Monthly", ""}, specCached: true},
		{name: "flag word", args: []string{"dci", "__complete", "dci", "get-report", "--out"}, specCached: true},
		{name: "flag value position", args: []string{"dci", "__complete", "dci", "get-report", "--filter", ""}, specCached: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			refreshCount, _ := configureCompletionPreflightTest(t, api, testCase.specCached)
			handled, _ := completionPreflight(testCase.args)
			if handled {
				t.Fatal("completion preflight handled a case it must fall through")
			}
			if *refreshCount != 0 {
				t.Fatalf("fall-through spawned %d refreshes", *refreshCount)
			}
		})
	}
}

// TestCompletionPreflightGuardsUnservableSpec pins the browser guard: a Tab
// press must never fall through to cobra dispatch when the spec is not
// locally servable, because hydration would fetch it through restish's auth
// middleware and open an interactive OAuth browser window.
func TestCompletionPreflightGuardsUnservableSpec(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}
	guardNotice := "_activeHelp_ Completion isn't initialized; run dci validate once, then press Tab again\n:4\n"
	for _, testCase := range []struct {
		name  string
		args  []string
		setup func(t *testing.T)
		want  string
	}{
		{
			name: "cold spec cache on a name completion",
			args: []string{"dci", "__complete", "dci", "get-report", ""},
			setup: func(t *testing.T) {
				_, _ = configureCompletionPreflightTest(t, api, false)
			},
			want: guardNotice,
		},
		{
			name: "cold spec cache on a root-level completion",
			args: []string{"dci", "__complete", "status", ""},
			setup: func(t *testing.T) {
				_, _ = configureCompletionPreflightTest(t, api, false)
			},
			want: guardNotice,
		},
		{
			name: "cold spec cache honors the active-help opt-out",
			args: []string{"dci", "__complete", "dci", "get-report", ""},
			setup: func(t *testing.T) {
				_, _ = configureCompletionPreflightTest(t, api, false)
				t.Setenv("DCI_ACTIVE_HELP", "0")
			},
			want: ":4\n",
		},
		{
			name: "valid spec timestamp but unloadable spec",
			args: []string{"dci", "__complete", "dci", "get-report", ""},
			setup: func(t *testing.T) {
				_, _ = configureCompletionPreflightTest(t, api, true)
				loadOperationAPI = func(entrypoint string, root *cobra.Command) (cli.API, error) {
					return cli.API{}, errors.New("corrupt spec cache")
				}
			},
			want: guardNotice,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DCI_ACTIVE_HELP", "")
			testCase.setup(t)
			var handled bool
			output := captureCompletionOutput(t, func() {
				handled, _ = completionPreflight(testCase.args)
			})
			if !handled {
				t.Fatal("unservable spec fell through to cobra dispatch")
			}
			if output != testCase.want {
				t.Fatalf("output = %q, want %q", output, testCase.want)
			}
		})
	}
}

func TestRefreshNameCacheWritesFirstPages(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}
	_, _ = configureCompletionPreflightTest(t, api, true)
	t.Setenv("DCI_API_KEY", "test-token")
	configDir := t.TempDir()

	previousFetch := resolverListFetch
	fetched := []resolverFetchCall{}
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		fetched = append(fetched, resolverFetchCall{listPath: listPath, context: context, maxPages: maxPages})
		return resolverListResult{entries: []nameCacheEntry{{ID: "x-1", Name: strings.Repeat("n", 200)}}}, nil
	}
	t.Cleanup(func() { resolverListFetch = previousFetch })

	refreshNameCache(configDir)

	if len(fetched) != 2 {
		t.Fatalf("fetched = %+v", fetched)
	}
	if fetched[0].listPath != "/analytics/v1/budgets" || fetched[1].listPath != "/analytics/v1/reports" {
		t.Fatalf("fetched = %+v", fetched)
	}
	for _, call := range fetched {
		if call.maxPages != 1 || call.context != "acme.com" {
			t.Fatalf("fetched = %+v", fetched)
		}
	}
	data, err := os.ReadFile(nameCachePath(configDir, "acme.com"))
	if err != nil {
		t.Fatal(err)
	}
	var cache nameCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatal(err)
	}
	if cache.Version != nameCacheVersion || cache.Context != "acme.com" {
		t.Fatalf("cache = %+v", cache)
	}
	if got := len(cache.Resources["reports"][0].Name); got != nameCacheMaxNameLen {
		t.Fatalf("cached name length = %d, want %d", got, nameCacheMaxNameLen)
	}
}

func TestRefreshNameCacheExitsSilently(t *testing.T) {
	api := cli.API{Operations: resolutionTestOperations()}

	t.Run("without credentials", func(t *testing.T) {
		_, _ = configureCompletionPreflightTest(t, api, true)
		t.Setenv("DCI_API_KEY", "")
		previousCache := cli.Cache
		cli.Cache = nil
		t.Cleanup(func() { cli.Cache = previousCache })
		configDir := t.TempDir()
		refreshNameCache(configDir)
		if matches, _ := filepath.Glob(filepath.Join(configDir, "names-*.json")); len(matches) != 0 {
			t.Fatalf("cache written without credentials: %v", matches)
		}
	})

	t.Run("when every fetch fails nothing is written", func(t *testing.T) {
		_, _ = configureCompletionPreflightTest(t, api, true)
		t.Setenv("DCI_API_KEY", "test-token")
		previousFetch := resolverListFetch
		resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
			return resolverListResult{}, nameResolutionNetworkError{err: os.ErrDeadlineExceeded}
		}
		t.Cleanup(func() { resolverListFetch = previousFetch })
		configDir := t.TempDir()
		refreshNameCache(configDir)
		if matches, _ := filepath.Glob(filepath.Join(configDir, "names-*.json")); len(matches) != 0 {
			t.Fatalf("cache written despite fetch failure: %v", matches)
		}
	})

	t.Run("a failing resource is skipped but the rest are written", func(t *testing.T) {
		_, _ = configureCompletionPreflightTest(t, api, true)
		t.Setenv("DCI_API_KEY", "test-token")
		previousFetch := resolverListFetch
		resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
			if listPath == "/analytics/v1/budgets" {
				return resolverListResult{}, nameResolutionNetworkError{err: os.ErrDeadlineExceeded}
			}
			return resolverListResult{entries: []nameCacheEntry{{ID: "r-1", Name: "Monthly Spend"}}}, nil
		}
		t.Cleanup(func() { resolverListFetch = previousFetch })
		configDir := t.TempDir()
		refreshNameCache(configDir)
		data, err := os.ReadFile(nameCachePath(configDir, "acme.com"))
		if err != nil {
			t.Fatal(err)
		}
		var cache nameCacheFile
		if err := json.Unmarshal(data, &cache); err != nil {
			t.Fatal(err)
		}
		if _, ok := cache.Resources["budgets"]; ok {
			t.Fatalf("failed resource cached: %+v", cache.Resources)
		}
		if len(cache.Resources["reports"]) != 1 {
			t.Fatalf("surviving resource missing: %+v", cache.Resources)
		}
	})

	t.Run("an authentication failure aborts the whole refresh", func(t *testing.T) {
		_, _ = configureCompletionPreflightTest(t, api, true)
		t.Setenv("DCI_API_KEY", "test-token")
		previousFetch := resolverListFetch
		resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
			if listPath == "/analytics/v1/budgets" {
				return resolverListResult{}, authenticationRequiredPreflightError()
			}
			return resolverListResult{entries: []nameCacheEntry{{ID: "r-1", Name: "Monthly Spend"}}}, nil
		}
		t.Cleanup(func() { resolverListFetch = previousFetch })
		configDir := t.TempDir()
		refreshNameCache(configDir)
		if matches, _ := filepath.Glob(filepath.Join(configDir, "names-*.json")); len(matches) != 0 {
			t.Fatalf("cache written despite auth failure: %v", matches)
		}
	})
}

func TestRegisterStaticFlagCompletions(t *testing.T) {
	for flagName, wantValue := range map[string]string{
		"output":     "toon",
		"rows":       "keyed",
		"table-mode": "wrap",
	} {
		command := &cobra.Command{Use: "dci", Run: func(*cobra.Command, []string) {}}
		command.PersistentFlags().String("output", "", "")
		command.PersistentFlags().String("rows", "", "")
		command.PersistentFlags().StringP("table-mode", "M", "fit", "")
		registerStaticFlagCompletions(command)

		buffer := &strings.Builder{}
		command.SetOut(buffer)
		command.SetErr(io.Discard)
		command.SetArgs([]string{cobra.ShellCompRequestCmd, "--" + flagName, ""})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		output := buffer.String()
		if !strings.Contains(output, wantValue+"\n") {
			t.Fatalf("%s completions = %q, want to contain %q", flagName, output, wantValue)
		}
		if !strings.Contains(output, ":4") {
			t.Fatalf("%s directive missing from %q", flagName, output)
		}
	}
}
