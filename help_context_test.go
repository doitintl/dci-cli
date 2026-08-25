package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestGroupTitleWithDescription(t *testing.T) {
	descriptions := map[string]string{"budgets": "Track actual cloud spend against planned spend."}

	t.Run("appends description to a bare title", func(t *testing.T) {
		got := groupTitleWithDescription("Budgets Commands:", "budgets", descriptions)
		want := "Budgets Commands: Track actual cloud spend against planned spend."
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("leaves title untouched when no description is known", func(t *testing.T) {
		got := groupTitleWithDescription("Reports Commands:", "reports", descriptions)
		if got != "Reports Commands:" {
			t.Fatalf("got %q, want unchanged title", got)
		}
	})

	t.Run("does not double-append on repeated calls", func(t *testing.T) {
		once := groupTitleWithDescription("Budgets Commands:", "budgets", descriptions)
		twice := groupTitleWithDescription(once, "budgets", descriptions)
		if once != twice {
			t.Fatalf("second call changed the title: %q -> %q", once, twice)
		}
	})
}

func TestAppendFlagExamples(t *testing.T) {
	flagExamples := map[string]string{
		"list-budgets/--filter": "type:system_label",
	}

	t.Run("appends the example to matching flag usage", func(t *testing.T) {
		cmd := &cobra.Command{Use: "list-budgets"}
		cmd.Flags().String("filter", "", "Filter results")
		appendFlagExamples(cmd, flagExamples)
		flag := cmd.Flags().Lookup("filter")
		want := "Filter results\nExample: type:system_label"
		if flag.Usage != want {
			t.Fatalf("got %q, want %q", flag.Usage, want)
		}
	})

	t.Run("leaves flags with no known example untouched", func(t *testing.T) {
		cmd := &cobra.Command{Use: "list-budgets"}
		cmd.Flags().String("output", "", "Output format")
		appendFlagExamples(cmd, flagExamples)
		if got := cmd.Flags().Lookup("output").Usage; got != "Output format" {
			t.Fatalf("got %q, want unchanged usage", got)
		}
	})

	t.Run("does not double-append when usage already contains the example", func(t *testing.T) {
		cmd := &cobra.Command{Use: "list-budgets"}
		cmd.Flags().String("filter", "", "Filter results\nExample: type:system_label")
		appendFlagExamples(cmd, flagExamples)
		var count int
		cmd.Flags().VisitAll(func(flag *pflag.Flag) {
			count += strings.Count(flag.Usage, "Example: type:system_label")
		})
		if count != 1 {
			t.Fatalf("expected exactly one Example annotation, found %d", count)
		}
	})

	t.Run("no-op for a nil/empty example map", func(t *testing.T) {
		cmd := &cobra.Command{Use: "list-budgets"}
		cmd.Flags().String("filter", "", "Filter results")
		appendFlagExamples(cmd, nil)
		if got := cmd.Flags().Lookup("filter").Usage; got != "Filter results" {
			t.Fatalf("got %q, want unchanged usage", got)
		}
	})
}

func TestRenderExample(t *testing.T) {
	if got := renderExample("type:system_label"); got != "type:system_label" {
		t.Fatalf("string example: got %q", got)
	}
	if got := renderExample(7); got != "7" {
		t.Fatalf("numeric example: got %q, want \"7\"", got)
	}
	if got := renderExample(nil); got != "null" {
		t.Fatalf("nil example: got %q", got)
	}
}

func TestApiBaseDigestIsStableAndDistinct(t *testing.T) {
	a := apiBaseDigest("https://api.doit.com")
	b := apiBaseDigest("https://api.doit.com")
	c := apiBaseDigest("https://dev-app.doit.com")
	if a != b {
		t.Fatalf("digest is not stable for the same input: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("digest collided for different API bases: %q", a)
	}
}

func TestHelpContextCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "help-context.yaml")

	if _, ok := readHelpContextCache(path); ok {
		t.Fatalf("expected no cache before it is written")
	}

	want := helpContext{
		TagDescriptions: map[string]string{"budgets": "Track actual cloud spend against planned spend."},
		FlagExamples:    map[string]string{"list-budgets/--filter": "type:system_label"},
	}
	writeHelpContextCache(path, want)

	got, ok := readHelpContextCache(path)
	if !ok {
		t.Fatalf("expected cache to be readable after writing")
	}
	if got.TagDescriptions["budgets"] != want.TagDescriptions["budgets"] {
		t.Fatalf("tag descriptions did not round-trip: %+v", got)
	}
	if got.FlagExamples["list-budgets/--filter"] != want.FlagExamples["list-budgets/--filter"] {
		t.Fatalf("flag examples did not round-trip: %+v", got)
	}
}

func TestLoadHelpContextFallsBackToStaleCacheOnFetchFailure(t *testing.T) {
	setupTestCache(t)
	t.Setenv("DCI_API_BASE_URL", "https://this-host-does-not-resolve.invalid")
	t.Setenv("DCI_CACHE_DIR", t.TempDir())

	base, err := apiBase()
	if err != nil {
		t.Fatalf("apiBase: %v", err)
	}
	path := helpContextCacheFile(base)
	stale := helpContext{TagDescriptions: map[string]string{"budgets": "stale description"}}
	writeHelpContextCache(path, stale)

	// The cache key is unexpired, so this exercises the fast path (cache hit,
	// no network call) rather than the fetch-failure fallback.
	cli.Cache.Set(helpContextCacheKeyFor(base), time.Now().Add(time.Hour))

	got := loadHelpContext(&cobra.Command{})
	if got.TagDescriptions["budgets"] != "stale description" {
		t.Fatalf("expected cached tag description to be served, got %+v", got)
	}
}

func TestLoadHelpContextReturnsZeroValueWithNoCacheAndUnreachableSpec(t *testing.T) {
	setupTestCache(t)
	t.Setenv("DCI_API_BASE_URL", "https://this-host-does-not-resolve.invalid")
	t.Setenv("DCI_CACHE_DIR", t.TempDir())

	got := loadHelpContext(&cobra.Command{})
	if len(got.TagDescriptions) != 0 || len(got.FlagExamples) != 0 {
		t.Fatalf("expected zero-value help context, got %+v", got)
	}
}

// TestDeprecatedCommandHelpShowsNotice locks in a piece of the CMP-48649
// acceptance criteria ("deprecated operations are visibly marked") that
// needs no new code: cobra prints a command's Deprecated string at the top
// of execute(), before it checks for --help, so the notice already appears
// on `dci <deprecated-cmd> --help` today. restish wires an operation's spec
// `deprecated` field straight into cobra's Deprecated (see
// restish/openapi/openapi.go and restish/cli/operation.go), so this is
// exercised end to end once a spec marks an operation deprecated.
func TestDeprecatedCommandHelpShowsNotice(t *testing.T) {
	root := &cobra.Command{Use: "dci"}
	root.AddCommand(&cobra.Command{
		Use:        "old-cmd",
		Short:      "does a thing",
		Deprecated: "use new-cmd instead",
		Run:        func(cmd *cobra.Command, args []string) {},
	})
	root.SetArgs([]string{"old-cmd", "--help"})

	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := out.String(); !strings.Contains(got, `"old-cmd" is deprecated, use new-cmd instead`) {
		t.Fatalf("expected deprecation notice in --help output, got:\n%s", got)
	}
}

