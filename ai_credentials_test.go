package main

// Tests for the AI credential chapter (ai_credentials.go): the resolution
// precedence, the provided-token cache and its expiry handling, and the vend
// HTTP client's status mapping.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
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
	seedCachedToken(t, "cli-bearer")

	t.Run("success sends the cached bearer", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cli-bearer" {
				t.Errorf("Authorization = %q", got)
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

	t.Run("missing sign-in never dials", func(t *testing.T) {
		seedCachedToken(t, "")
		t.Setenv(aiProvidedURLEnvName, "http://127.0.0.1:1") // would fail if dialed
		_, err := fetchAIProvidedToken(context.Background())
		if err == nil || !strings.Contains(err.Error(), "dci login") {
			t.Fatalf("expected a sign-in error, got %v", err)
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
