package main

// Tests for the AI credential chapter (ai_credentials.go): the resolution
// precedence, the provided-token cache and its expiry handling, and the vend
// HTTP client's status mapping.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

// seedCachedToken swaps in a fresh restish token cache holding token — the
// slot the credential chapter reads (the same one cachedTokenIsDoer parses).
func seedCachedToken(t *testing.T, token string) {
	t.Helper()
	setupTestCache(t)
	cli.Cache.Set(testTokenCacheKey, token)
}

func TestResolveAICredentialsPrecedence(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(aiProvidedEnvName, "")

	t.Run("env key wins over everything", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-env")
		seedCachedToken(t, doerJWT())
		creds := resolveAICredentials(aiSettings{APIKey: "sk-saved"})
		if creds.source != aiCredsSourceEnv || creds.key != "sk-env" {
			t.Fatalf("got source=%q key=%q", creds.source, creds.key)
		}
	})

	t.Run("settings key wins over provided", func(t *testing.T) {
		seedCachedToken(t, doerJWT())
		creds := resolveAICredentials(aiSettings{APIKey: "sk-saved"})
		if creds.source != aiCredsSourceSettings || creds.key != "sk-saved" {
			t.Fatalf("got source=%q key=%q", creds.source, creds.key)
		}
	})

	t.Run("keyless doer gets provided access", func(t *testing.T) {
		seedCachedToken(t, doerJWT())
		creds := resolveAICredentials(aiSettings{})
		if creds.source != aiCredsSourceProvided {
			t.Fatalf("got source=%q, want provided", creds.source)
		}
		if creds.provided == nil {
			t.Fatal("provided credentials carry no token source")
		}
	})

	t.Run("keyless non-doer gets nothing", func(t *testing.T) {
		seedCachedToken(t, nonDoerJWT())
		if creds := resolveAICredentials(aiSettings{}); creds.available() {
			t.Fatalf("got source=%q, want none", creds.source)
		}
	})

	t.Run("DCI_AI_PROVIDED=off disables the doer path", func(t *testing.T) {
		t.Setenv(aiProvidedEnvName, "off")
		seedCachedToken(t, doerJWT())
		if creds := resolveAICredentials(aiSettings{}); creds.available() {
			t.Fatalf("got source=%q, want none", creds.source)
		}
	})

	t.Run("settings provided=off disables, env wins over it", func(t *testing.T) {
		seedCachedToken(t, doerJWT())
		if creds := resolveAICredentials(aiSettings{Provided: "off"}); creds.available() {
			t.Fatalf("settings off: got source=%q, want none", creds.source)
		}
		t.Setenv(aiProvidedEnvName, "on")
		if creds := resolveAICredentials(aiSettings{Provided: "off"}); creds.source != aiCredsSourceProvided {
			t.Fatalf("env on over settings off: got source=%q, want provided", creds.source)
		}
	})
}

func TestAICredentialsLabel(t *testing.T) {
	cases := map[string]string{
		aiCredsSourceEnv:      "API key from env",
		aiCredsSourceSettings: "API key from " + aiSettingsFileName,
		aiCredsSourceProvided: "DoiT-provided access",
		"":                    "",
	}
	for source, want := range cases {
		if got := (aiCredentials{source: source}).label(); got != want {
			t.Errorf("label(%q) = %q, want %q", source, got, want)
		}
	}
}

func TestAIProvidedTokenSourceCaching(t *testing.T) {
	calls := 0
	failing := false
	source := &aiProvidedTokenSource{fetch: func(context.Context) (aiVendedToken, error) {
		if failing {
			return aiVendedToken{}, errors.New("vend down")
		}
		calls++
		return aiVendedToken{AccessToken: fmt.Sprintf("tok-%d", calls), ExpiresIn: 600}, nil
	}}
	ctx := context.Background()

	first, err := source.token(ctx)
	if err != nil || first != "tok-1" {
		t.Fatalf("first token: %q, %v", first, err)
	}
	if second, _ := source.token(ctx); second != "tok-1" || calls != 1 {
		t.Fatalf("expected cached token, got %q after %d fetches", second, calls)
	}

	// Inside the refresh window a new token is vended.
	source.refreshAt = time.Now().Add(-time.Second)
	if third, _ := source.token(ctx); third != "tok-2" || calls != 2 {
		t.Fatalf("expected re-vended token, got %q after %d fetches", third, calls)
	}

	// A failed refresh keeps serving the cached token until hard expiry…
	failing = true
	source.refreshAt = time.Now().Add(-time.Second)
	source.expiresAt = time.Now().Add(time.Minute)
	if stale, err := source.token(ctx); err != nil || stale != "tok-2" {
		t.Fatalf("advisory-refresh failure: got %q, %v", stale, err)
	}
	// …and past hard expiry the fetch error is the caller's.
	source.expiresAt = time.Now().Add(-time.Second)
	if _, err := source.token(ctx); err == nil || !strings.Contains(err.Error(), "vend down") {
		t.Fatalf("expected the fetch error past expiry, got %v", err)
	}

	// invalidate forces a re-vend even with time left.
	failing = false
	token, _ := source.token(ctx)
	source.invalidate(token)
	if fresh, _ := source.token(ctx); fresh == token {
		t.Fatal("invalidate did not force a re-vend")
	}
	// invalidating a token that already rolled leaves the cache alone.
	current, _ := source.token(ctx)
	source.invalidate("tok-stale")
	if kept, _ := source.token(ctx); kept != current {
		t.Fatal("invalidating a superseded token dropped the current one")
	}
}

func TestAIProvidedTokenSourceRejectsEmptyToken(t *testing.T) {
	source := &aiProvidedTokenSource{fetch: func(context.Context) (aiVendedToken, error) {
		return aiVendedToken{AccessToken: "   ", ExpiresIn: 600}, nil
	}}
	if _, err := source.token(context.Background()); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected an empty-token error, got %v", err)
	}
}

func TestFetchAIProvidedToken(t *testing.T) {
	seedCacheFile(t, map[string]any{"token": "cli-bearer"}) // the vend call reads the session from disk

	// Pin the customer context: the vend URL carries it, and the 401 wording
	// branches on whether one is set — ambient config must not leak in.
	t.Setenv("DCI_CUSTOMER_CONTEXT", "acme.com")

	t.Run("success sends the cached bearer and customer context", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cli-bearer" {
				t.Errorf("Authorization = %q", got)
			}
			if got := r.URL.Query().Get("customerContext"); got != "acme.com" {
				t.Errorf("customerContext = %q, want acme.com", got)
			}
			fmt.Fprint(w, `{"access_token":"sk-ant-oat01-test","expires_in":1800}`)
		}))
		defer server.Close()
		t.Setenv(aiProvidedURLEnvName, server.URL)
		vended, err := fetchAIProvidedToken(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if vended.AccessToken != "sk-ant-oat01-test" || vended.ExpiresIn != 1800 {
			t.Fatalf("vended = %+v", vended)
		}
	})

	statusCases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "dci login"},
		{http.StatusForbidden, "not enabled"},
		{http.StatusNotFound, "not available"},
		{http.StatusInternalServerError, "500"},
	}
	for _, testCase := range statusCases {
		t.Run(fmt.Sprintf("status %d", testCase.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
			}))
			defer server.Close()
			t.Setenv(aiProvidedURLEnvName, server.URL)
			_, err := fetchAIProvidedToken(context.Background())
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("expected error containing %q, got %v", testCase.want, err)
			}
		})
	}

	t.Run("401 without a customer context names that fix, not re-login", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()
		t.Setenv(aiProvidedURLEnvName, server.URL)
		t.Setenv("DCI_CUSTOMER_CONTEXT", "")
		t.Setenv("DCI_CONFIG_DIR", t.TempDir()) // no customer_context file either
		_, err := fetchAIProvidedToken(context.Background())
		if err == nil || !strings.Contains(err.Error(), "customer context") {
			t.Fatalf("expected a customer-context error, got %v", err)
		}
	})

	t.Run("missing sign-in never dials", func(t *testing.T) {
		seedCacheFile(t, map[string]any{})
		t.Setenv(aiProvidedURLEnvName, "http://127.0.0.1:1") // would fail if dialed
		_, err := fetchAIProvidedToken(context.Background())
		if err == nil || !strings.Contains(err.Error(), "dci login") {
			t.Fatalf("expected a sign-in error, got %v", err)
		}
	})
}

func TestAIProvidedEndpointCustomerContext(t *testing.T) {
	t.Run("appends the active context to the API-base URL", func(t *testing.T) {
		t.Setenv(aiProvidedURLEnvName, "")
		t.Setenv("DCI_CUSTOMER_CONTEXT", "acme.com")
		endpoint, err := aiProvidedEndpoint()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(endpoint, aiProvidedPath+"?customerContext=acme.com") {
			t.Fatalf("endpoint = %q", endpoint)
		}
	})

	t.Run("appends the active context to an override URL", func(t *testing.T) {
		t.Setenv(aiProvidedURLEnvName, "https://example.test/vend")
		t.Setenv("DCI_CUSTOMER_CONTEXT", "acme.com")
		endpoint, err := aiProvidedEndpoint()
		if err != nil {
			t.Fatal(err)
		}
		if endpoint != "https://example.test/vend?customerContext=acme.com" {
			t.Fatalf("endpoint = %q", endpoint)
		}
	})

	t.Run("an override URL keeps its own context", func(t *testing.T) {
		t.Setenv(aiProvidedURLEnvName, "https://example.test/vend?customerContext=other.com")
		t.Setenv("DCI_CUSTOMER_CONTEXT", "acme.com")
		endpoint, err := aiProvidedEndpoint()
		if err != nil {
			t.Fatal(err)
		}
		if endpoint != "https://example.test/vend?customerContext=other.com" {
			t.Fatalf("endpoint = %q", endpoint)
		}
	})

	t.Run("no context leaves the URL untouched", func(t *testing.T) {
		t.Setenv(aiProvidedURLEnvName, "https://example.test/vend")
		t.Setenv("DCI_CUSTOMER_CONTEXT", "")
		t.Setenv("DCI_CONFIG_DIR", t.TempDir())
		endpoint, err := aiProvidedEndpoint()
		if err != nil {
			t.Fatal(err)
		}
		if endpoint != "https://example.test/vend" {
			t.Fatalf("endpoint = %q", endpoint)
		}
	})
}

func TestAILooksLikeAuthError(t *testing.T) {
	if !aiLooksLikeAuthError(errors.New(`POST 401 {"type":"authentication_error"}`)) {
		t.Fatal("401 not recognized")
	}
	if aiLooksLikeAuthError(errors.New("429 rate_limit_error")) {
		t.Fatal("rate limit misread as auth error")
	}
}

// seedCacheFile backs cli.Cache with a real cache file holding the given
// dci:default entry, the way a signed-in CLI has it on disk. The refresh path
// re-reads that file and writes refreshed tokens back to it, which an
// in-memory-only cache cannot exercise.
func seedCacheFile(t *testing.T, entry map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache.json")
	writeCacheFile(t, path, entry)
	old := cli.Cache
	cli.Cache = viper.New()
	cli.Cache.SetConfigFile(path)
	if err := cli.Cache.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Cache = old })
	// Keep the developer's real apis.json out of the refresh params.
	oldAPIs := aiDCIAPIsConfigPath
	apisPath := filepath.Join(filepath.Dir(path), "apis.json")
	aiDCIAPIsConfigPath = func() string { return apisPath }
	t.Cleanup(func() { aiDCIAPIsConfigPath = oldAPIs })
	return path
}

func writeCacheFile(t *testing.T, path string, entry map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"dci:default": entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeTokenEndpoint stands in for the Console token URL; handler answers the
// refresh request. It returns a pointer to the request count.
func fakeTokenEndpoint(t *testing.T, handler http.HandlerFunc) *int {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	oldURL := dciOAuthTokenURL
	dciOAuthTokenURL = server.URL
	t.Cleanup(func() { dciOAuthTokenURL = oldURL })
	return &calls
}

func TestAIDCIBearerRefresh(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	t.Run("expired token is refreshed before the vend call", func(t *testing.T) {
		path := seedCacheFile(t, map[string]any{"token": "stale-bearer", "type": "Bearer", "expires": past, "refresh": "refresh-1"})
		calls := fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-1" || r.Form.Get("client_id") != dciOAuthClientID {
				t.Errorf("refresh form = %v", r.Form)
			}
			fmt.Fprint(w, `{"access_token":"fresh-bearer","token_type":"Bearer","expires_in":3600}`)
		})
		vend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer fresh-bearer" {
				t.Errorf("vend Authorization = %q, want the refreshed bearer", got)
			}
			fmt.Fprint(w, `{"access_token":"sk-ant-oat01-test","expires_in":1800}`)
		}))
		defer vend.Close()
		t.Setenv(aiProvidedURLEnvName, vend.URL)
		if _, err := fetchAIProvidedToken(context.Background()); err != nil {
			t.Fatal(err)
		}
		if *calls != 1 {
			t.Fatalf("token endpoint calls = %d, want 1", *calls)
		}
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), "fresh-bearer") {
			t.Fatalf("refreshed token not written back to the cache file: %s", data)
		}
	})

	t.Run("failed refresh asks for a login and never goes interactive", func(t *testing.T) {
		seedCacheFile(t, map[string]any{"token": "stale-bearer", "expires": past, "refresh": "revoked"})
		calls := fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		})
		_, err := aiDCIBearer(context.Background())
		if err == nil || !strings.Contains(err.Error(), "dci login") {
			t.Fatalf("expected a dci login error, got %v", err)
		}
		if *calls != 1 {
			t.Fatalf("token endpoint calls = %d, want 1", *calls)
		}
	})

	t.Run("no refresh token asks for a login without dialing", func(t *testing.T) {
		seedCacheFile(t, map[string]any{"token": "stale-bearer", "expires": past})
		calls := fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {})
		if _, err := aiDCIBearer(context.Background()); err == nil || !strings.Contains(err.Error(), "dci login") {
			t.Fatalf("expected a dci login error, got %v", err)
		}
		if *calls != 0 {
			t.Fatalf("token endpoint calls = %d, want 0", *calls)
		}
	})

	t.Run("valid token is reused without a refresh", func(t *testing.T) {
		seedCacheFile(t, map[string]any{"token": "good-bearer", "expires": future, "refresh": "refresh-1"})
		calls := fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {})
		got, err := aiDCIBearer(context.Background())
		if err != nil || got != "good-bearer" || *calls != 0 {
			t.Fatalf("got %q, err %v, refresh calls %d", got, err, *calls)
		}
	})

	t.Run("no recorded expiry sends the token as-is", func(t *testing.T) {
		seedCacheFile(t, map[string]any{"token": "raw-bearer"})
		calls := fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {})
		got, err := aiDCIBearer(context.Background())
		if err != nil || got != "raw-bearer" || *calls != 0 {
			t.Fatalf("got %q, err %v, refresh calls %d", got, err, *calls)
		}
	})

	t.Run("a token a child process refreshed on disk is picked up", func(t *testing.T) {
		path := seedCacheFile(t, map[string]any{"token": "stale-bearer", "expires": past, "refresh": "refresh-1"})
		// A command the model ran refreshed the session after this process
		// loaded the cache.
		writeCacheFile(t, path, map[string]any{"token": "child-bearer", "expires": future, "refresh": "refresh-2"})
		calls := fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {})
		got, err := aiDCIBearer(context.Background())
		if err != nil || got != "child-bearer" || *calls != 0 {
			t.Fatalf("got %q, err %v, refresh calls %d", got, err, *calls)
		}
	})

	t.Run("after a refresh, a login or logout on disk still wins", func(t *testing.T) {
		path := seedCacheFile(t, map[string]any{"token": "stale-bearer", "type": "Bearer", "expires": past, "refresh": "refresh-1"})
		fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"access_token":"fresh-bearer","token_type":"Bearer","expires_in":3600}`)
		})
		if got, err := aiDCIBearer(context.Background()); err != nil || got != "fresh-bearer" {
			t.Fatalf("first call: got %q, err %v", got, err)
		}
		// /login as another account, run as a child process.
		writeCacheFile(t, path, map[string]any{"token": "other-account", "expires": future, "refresh": "refresh-9"})
		reloadCredentialCache() // what the TUI does after a child command
		if got, err := aiDCIBearer(context.Background()); err != nil || got != "other-account" {
			t.Fatalf("after login: got %q, err %v", got, err)
		}
		if got := cli.Cache.GetString("dci:default.token"); got != "other-account" {
			t.Fatalf("session cache still shadows the disk: %q", got)
		}
		// dci logout, run as a child process.
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		reloadCredentialCache()
		if got, err := aiDCIBearer(context.Background()); err != nil || got != "" {
			t.Fatalf("after logout: got %q, err %v", got, err)
		}
	})

	t.Run("a stalled token endpoint gives up when the turn is cancelled", func(t *testing.T) {
		seedCacheFile(t, map[string]any{"token": "stale-bearer", "expires": past, "refresh": "refresh-1"})
		release := make(chan struct{})
		fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		defer close(release)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(50 * time.Millisecond); cancel() }()
		start := time.Now()
		if _, err := aiDCIBearer(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("cancel did not interrupt the refresh")
		}
	})

	t.Run("a stalled token endpoint times out", func(t *testing.T) {
		seedCacheFile(t, map[string]any{"token": "stale-bearer", "expires": past, "refresh": "refresh-1"})
		release := make(chan struct{})
		fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		defer close(release)
		old := aiDCIRefreshTimeout
		aiDCIRefreshTimeout = 100 * time.Millisecond
		t.Cleanup(func() { aiDCIRefreshTimeout = old })
		_, err := aiDCIBearer(context.Background())
		if err == nil || strings.Contains(err.Error(), "dci login") || !strings.Contains(err.Error(), "could not reach") {
			t.Fatalf("expected a could-not-reach error, got %v", err)
		}
	})

	t.Run("a token endpoint outage is not reported as an expired session", func(t *testing.T) {
		seedCacheFile(t, map[string]any{"token": "stale-bearer", "expires": past, "refresh": "refresh-1"})
		fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		})
		_, err := aiDCIBearer(context.Background())
		if err == nil || strings.Contains(err.Error(), "dci login") {
			t.Fatalf("expected an outage error, got %v", err)
		}
	})

	t.Run("a refresh without a new refresh token keeps the old one", func(t *testing.T) {
		path := seedCacheFile(t, map[string]any{"token": "stale-bearer", "expires": past, "refresh": "refresh-1", "other": "kept"})
		fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"access_token":"fresh-bearer","token_type":"Bearer","expires_in":3600}`)
		})
		if _, err := aiDCIBearer(context.Background()); err != nil {
			t.Fatal(err)
		}
		reread := viper.New()
		reread.SetConfigFile(path)
		if err := reread.ReadInConfig(); err != nil {
			t.Fatal(err)
		}
		if got := reread.GetString("dci:default.refresh"); got != "refresh-1" {
			t.Fatalf("refresh token = %q, want refresh-1", got)
		}
		if exp := reread.GetTime("dci:default.expires"); !exp.After(time.Now()) {
			t.Fatalf("expiry not moved forward: %v", exp)
		}
		if got := reread.GetString("dci:default.other"); got != "kept" {
			t.Fatalf("unrelated cache key lost: %q", got)
		}
	})

	t.Run("the token URL and client in apis.json are honored", func(t *testing.T) {
		path := seedCacheFile(t, map[string]any{"token": "stale-bearer", "expires": past, "refresh": "refresh-1"})
		// The fallback endpoint must not be dialed.
		fallback := fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {})
		custom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			if r.Form.Get("client_id") != "custom-client" || r.Form.Get("audience") != "dci" {
				t.Errorf("refresh form = %v", r.Form)
			}
			fmt.Fprint(w, `{"access_token":"custom-bearer","expires_in":3600}`)
		}))
		defer custom.Close()
		apis := map[string]any{"dci": map[string]any{"profiles": map[string]any{"default": map[string]any{"auth": map[string]any{
			"name":   "oauth-authorization-code",
			"params": map[string]string{"client_id": "custom-client", "token_url": custom.URL, "audience": "dci"},
		}}}}}
		data, _ := json.Marshal(apis)
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "apis.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := aiDCIBearer(context.Background())
		if err != nil || got != "custom-bearer" || *fallback != 0 {
			t.Fatalf("got %q, err %v, fallback calls %d", got, err, *fallback)
		}
	})

	t.Run("DCI_API_KEY is never refreshed", func(t *testing.T) {
		seedCacheFile(t, map[string]any{"token": "x"})
		t.Setenv(apiKeyEnvName, "api-key-token")
		oldProfile := viper.GetString("rsh-profile")
		viper.Set("rsh-profile", "default") // restish's default; unset in tests
		t.Cleanup(func() { viper.Set("rsh-profile", oldProfile) })
		applyAPIKeyAuth()
		calls := fakeTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {})
		got, err := aiDCIBearer(context.Background())
		if err != nil || got != "api-key-token" || *calls != 0 {
			t.Fatalf("got %q, err %v, refresh calls %d", got, err, *calls)
		}
	})
}
