package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

// setupSpecCacheTest points cli.Cache at a fresh cache.json in a temp
// directory (the on-disk shape restish's initCache leaves behind) and makes
// it the current DCI_CACHE_DIR, so the chapter's reads and writes land in
// one isolated place. Returns that directory.
func setupSpecCacheTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, "cache.json")
	if err := os.WriteFile(cacheFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := cli.Cache
	cache := viper.New()
	cache.SetConfigFile(cacheFile)
	cache.SetConfigType("json")
	if err := cache.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	cli.Cache = cache
	t.Cleanup(func() { cli.Cache = previous })
	t.Setenv("DCI_CACHE_DIR", dir)
	return dir
}

func TestHelpInvocationNeedsSpec(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"api command --help", []string{"dci", "dci", "list-budgets", "--help"}, true},
		{"api command -h", []string{"dci", "dci", "list-budgets", "-h"}, true},
		{"help flag before the command", []string{"dci", "dci", "--help", "list-budgets"}, true},
		{"beta help still passes restish's load", []string{"dci", "dci", "beta", "--help"}, true},
		{"local question command help", []string{"dci", "dci", "budgets-at-risk", "--help"}, true},
		{"api command without help", []string{"dci", "dci", "list-budgets"}, false},
		{"root help flag", []string{"dci", "--help"}, false},
		{"help command", []string{"dci", "help"}, false},
		{"help command with a topic", []string{"dci", "help", "list-budgets"}, false},
		{"local command help", []string{"dci", "status", "--help"}, false},
		{"bare invocation", []string{"dci"}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := helpInvocationNeedsSpec(testCase.args); got != testCase.want {
				t.Fatalf("helpInvocationNeedsSpec(%v) = %v, want %v", testCase.args, got, testCase.want)
			}
		})
	}
}

func TestWriteSpecCacheIsReadBackAsWarm(t *testing.T) {
	dir := setupSpecCacheTest(t)
	if specCacheWarm(dir) {
		t.Fatal("empty cache dir reported warm")
	}

	api := cli.API{Operations: []cli.Operation{{Name: "list-budgets", Short: "List budgets", Method: "GET"}}}
	if err := writeSpecCache(dir, api); err != nil {
		t.Fatalf("writeSpecCache: %v", err)
	}

	if !specCacheWarm(dir) {
		t.Fatal("cache not warm after writeSpecCache")
	}
	blob, err := os.ReadFile(filepath.Join(dir, "dci.cbor"))
	if err != nil {
		t.Fatal(err)
	}
	var cached cli.API
	if err := cbor.Unmarshal(blob, &cached); err != nil {
		t.Fatalf("dci.cbor does not decode as a restish API: %v", err)
	}
	if len(cached.Operations) != 1 || cached.Operations[0].Name != "list-budgets" {
		t.Fatalf("cached operations = %+v", cached.Operations)
	}
	// The API subcommand restish compares against carries no version, so
	// the stamp must be empty — anything else makes cli.Load refetch (and
	// authenticate) despite the warm cache.
	if cached.RestishVersion != "" {
		t.Fatalf("RestishVersion = %q, want empty", cached.RestishVersion)
	}
	// The expiry must reach disk, in the nested shape viper writes, so the
	// next invocation's cli.Load (a fresh process) also takes the cache path.
	expiresAt := readCacheExpiry(dir, "dci.expires")
	if expiresAt.IsZero() || !expiresAt.After(time.Now().Add(23*time.Hour)) {
		t.Fatalf("persisted dci.expires = %v, want ~24h ahead", expiresAt)
	}
}

func TestWriteSpecCacheRequiresInitializedCache(t *testing.T) {
	previous := cli.Cache
	cli.Cache = nil
	t.Cleanup(func() { cli.Cache = previous })
	if err := writeSpecCache(t.TempDir(), cli.API{}); err == nil {
		t.Fatal("expected an error with no restish cache")
	}
}

func stubSpecRefresh(t *testing.T, refresh func() error, authenticated bool) *int32 {
	t.Helper()
	previousRefresh := refreshSpecCacheUnauthenticated
	previousCredentials := invocationCredentialsAvailable
	var calls int32
	refreshSpecCacheUnauthenticated = func() error {
		atomic.AddInt32(&calls, 1)
		return refresh()
	}
	invocationCredentialsAvailable = func() bool { return authenticated }
	t.Cleanup(func() {
		refreshSpecCacheUnauthenticated = previousRefresh
		invocationCredentialsAvailable = previousCredentials
	})
	return &calls
}

func TestPrepareSpecForHelpSkipsRefreshWhenCacheIsWarm(t *testing.T) {
	dir := setupSpecCacheTest(t)
	if err := writeSpecCache(dir, cli.API{Operations: []cli.Operation{{Name: "list-budgets"}}}); err != nil {
		t.Fatal(err)
	}
	calls := stubSpecRefresh(t, func() error { return errors.New("must not be called") }, false)

	if err := prepareSpecForHelp(); err != nil {
		t.Fatalf("prepareSpecForHelp: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("refresh called %d times with a warm cache", *calls)
	}
}

func TestPrepareSpecForHelpRefreshesColdCacheWithoutCredentials(t *testing.T) {
	setupSpecCacheTest(t)
	calls := stubSpecRefresh(t, func() error { return nil }, false)

	if err := prepareSpecForHelp(); err != nil {
		t.Fatalf("prepareSpecForHelp: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("refresh called %d times, want 1", *calls)
	}
}

func TestPrepareSpecForHelpDefersToAuthenticatedLoadWhenRefreshFailsWithCredentials(t *testing.T) {
	setupSpecCacheTest(t)
	stubSpecRefresh(t, func() error { return errors.New("403 from a guarded host") }, true)

	// Stored credentials mean restish's own authenticated load still gets
	// its turn, exactly as before this chapter existed.
	if err := prepareSpecForHelp(); err != nil {
		t.Fatalf("prepareSpecForHelp = %v, want nil", err)
	}
}

func TestPrepareSpecForHelpReportsTheFetchFailureWithoutCredentials(t *testing.T) {
	setupSpecCacheTest(t)
	t.Setenv("DCI_API_BASE_URL", "https://api.example.test")
	cause := &url.Error{Op: "Get", URL: "https://api.example.test/openapi.yaml", Err: errors.New("dial tcp: connection refused")}
	stubSpecRefresh(t, func() error { return cause }, false)

	err := prepareSpecForHelp()
	if err == nil {
		t.Fatal("expected an error when the description cannot be fetched and nothing can authenticate")
	}
	preflightError, ok := err.(invocationPreflightError)
	if !ok {
		t.Fatalf("error type = %T, want invocationPreflightError", err)
	}
	detail := preflightError.StructuredError()
	if detail.Code != "NETWORK_ERROR" || preflightError.ExitCode() != exitNetwork {
		t.Fatalf("code = %q exit = %d, want NETWORK_ERROR/%d", detail.Code, preflightError.ExitCode(), exitNetwork)
	}
	if !strings.Contains(detail.Message, "https://api.example.test") || !strings.Contains(detail.Message, "connection refused") {
		t.Fatalf("message = %q", detail.Message)
	}
	// The old failure modes named credentials; this one must not, because
	// credentials were never the problem.
	if strings.Contains(detail.Message, "credentials") {
		t.Fatalf("message blames credentials: %q", detail.Message)
	}
}

func TestRefreshSpecCacheUnauthenticatedFetchesThePublicDescription(t *testing.T) {
	dir := setupSpecCacheTest(t)
	var authorizedRequests, specRequests int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			atomic.AddInt32(&authorizedRequests, 1)
		}
		if request.URL.Path != "/openapi.yaml" {
			http.NotFound(writer, request)
			return
		}
		atomic.AddInt32(&specRequests, 1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"openapi": "3.0.0",
			"info": {"title": "DCI test", "version": "1.0.0"},
			"paths": {"/budgets": {"get": {"operationId": "list-budgets", "summary": "List budgets",
				"responses": {"200": {"description": "OK"}}}}}
		}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("DCI_API_BASE_URL", server.URL)
	previousClient := specHTTPClient
	specHTTPClient = server.Client()
	t.Cleanup(func() { specHTTPClient = previousClient })

	if err := refreshSpecCacheUnauthenticated(); err != nil {
		t.Fatalf("refreshSpecCacheUnauthenticated: %v", err)
	}
	if specRequests != 1 || authorizedRequests != 0 {
		t.Fatalf("spec requests = %d (want 1), authorized requests = %d (want 0)", specRequests, authorizedRequests)
	}
	if !specCacheWarm(dir) {
		t.Fatal("cache not warm after the public fetch")
	}
	blob, err := os.ReadFile(filepath.Join(dir, "dci.cbor"))
	if err != nil {
		t.Fatal(err)
	}
	var cached cli.API
	if err := cbor.Unmarshal(blob, &cached); err != nil {
		t.Fatal(err)
	}
	if len(cached.Operations) != 1 || cached.Operations[0].Name != "list-budgets" || cached.Operations[0].Short != "List budgets" {
		t.Fatalf("cached operations = %+v", cached.Operations)
	}
}

func TestConfigureSpecHTTPClientTLSHonorsInsecureConfig(t *testing.T) {
	previousTransport := specHTTPClient.Transport
	t.Cleanup(func() { specHTTPClient.Transport = previousTransport })
	viper.Set("rsh-insecure", false)
	t.Cleanup(func() { viper.Set("rsh-insecure", false) })

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "apis.json"), []byte(`{"dci":{"base":"https://api.example.test","tls":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	specHTTPClient.Transport = nil
	configureSpecHTTPClientTLS(configDir)
	if specHTTPClient.Transport != nil {
		t.Fatal("transport replaced although apis.json trusts the system roots")
	}

	if err := os.WriteFile(filepath.Join(configDir, "apis.json"), []byte(`{"dci":{"base":"https://api.example.test","tls":{"insecure":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configureSpecHTTPClientTLS(configDir)
	transport, ok := specHTTPClient.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("transport = %#v, want InsecureSkipVerify from apis.json", specHTTPClient.Transport)
	}
}

func TestRecoveringRunReturnsPanickedErrors(t *testing.T) {
	cause := errors.New("no credentials available")
	err := recoveringRun(func() error { panic(cause) })()
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want the panicked error", err)
	}

	if err := recoveringRun(func() error { return nil })(); err != nil {
		t.Fatalf("error = %v, want nil", err)
	}

	// Anything that is not an error is a bug, and stays a panic for run()'s
	// handler to label as an internal error.
	defer func() {
		if recovered := recover(); recovered != "boom" {
			t.Fatalf("recovered %v, want the original non-error panic", recovered)
		}
	}()
	_ = recoveringRun(func() error { panic("boom") })()
	t.Fatal("non-error panic was swallowed")
}

func TestHeadlessLoginErrorCarriesTheAuthenticationContract(t *testing.T) {
	previousHeadless := loginFlowHeadless
	loginFlowHeadless = func() bool { return true }
	t.Cleanup(func() { loginFlowHeadless = previousHeadless })

	_, err := (&authorizationCodeTokenSource{}).Token()
	var loginError headlessLoginError
	if !errors.As(err, &loginError) {
		t.Fatalf("error type = %T (%v), want headlessLoginError", err, err)
	}
	if exitCodeForExecutionError(err, 0) != exitAuthentication {
		t.Fatalf("exit code = %d, want %d", exitCodeForExecutionError(err, 0), exitAuthentication)
	}
	detail := structuredErrorForExecution(err, 0)
	if detail.Code != "AUTHENTICATION_REQUIRED" || detail.Hint == "" || detail.Retryable {
		t.Fatalf("structured error = %#v", detail)
	}
	// The envelope matches the preflight's early catch of the same
	// condition, so an agent sees one shape however late it surfaces.
	preflight := authenticationRequiredPreflightError().(invocationPreflightError).StructuredError()
	if detail != preflight {
		t.Fatalf("envelope = %#v, preflight envelope = %#v", detail, preflight)
	}
	// The human-mode line keeps carrying the remedy, since that path prints
	// only Error().
	if !strings.Contains(err.Error(), "DCI_API_KEY") || !strings.Contains(err.Error(), "dci login") {
		t.Fatalf("Error() = %q", err.Error())
	}
}
