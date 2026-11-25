package main

// Chapter: AI credential resolution (AI-FEDERATION-SPEC §5). Decides how a
// dci ai session authenticates to Anthropic: an explicit API key always wins
// (env, then ai_settings.json — mirroring the Anthropic SDK's own precedence,
// where ANTHROPIC_API_KEY shadows every other source), and Doers without one
// get DoiT-provided access — a short-lived Anthropic token fetched from the
// console API, which mints it via workload identity federation server-side
// (spec §4.5). The vended token lives in process memory only and is
// re-fetched near expiry; nothing new persists under the config dir. Kept in
// a sibling file per the AGENTS.md chapter-split guidance.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/oauth"
	"golang.org/x/oauth2"
)

const (
	aiCredsSourceEnv      = "env"
	aiCredsSourceSettings = "settings"
	aiCredsSourceProvided = "provided"

	// aiProvidedPath is the vend endpoint on the API base (AI-FEDERATION-SPEC
	// §4.5); the console auth team owns the final path, and
	// DCI_AI_PROVIDED_URL overrides it wholesale until it stabilizes.
	aiProvidedPath       = "/auth/ai/token"
	aiProvidedEnvName    = "DCI_AI_PROVIDED"
	aiProvidedURLEnvName = "DCI_AI_PROVIDED_URL"
)

// aiCredentials is the resolved way a session authenticates to Anthropic.
// The zero value means no credentials — renderers gate on available().
type aiCredentials struct {
	source   string
	key      string                 // the key sources
	provided *aiProvidedTokenSource // the provided source
}

func (c aiCredentials) available() bool { return c.source != "" }

// label names the credential source in the session banner. The key labels
// predate this chapter — keep them stable.
func (c aiCredentials) label() string {
	switch c.source {
	case aiCredsSourceEnv:
		return "API key from env"
	case aiCredsSourceSettings:
		return "API key from " + aiSettingsFileName
	case aiCredsSourceProvided:
		return "DoiT-provided access"
	}
	return ""
}

// streamer builds the model transport for these credentials. Key modes bind
// one client for the session's life. Provided mode resolves a token per
// model call — the source caches it until near expiry, so long sessions ride
// token rollover without a visible seam, and the first vend failure surfaces
// as that turn's error rather than killing the session.
func (c aiCredentials) streamer() aiModelStreamer {
	if c.provided == nil {
		return newAnthropicStreamer(anthropic.NewClient(option.WithAPIKey(c.key)))
	}
	source := c.provided
	return func(ctx context.Context, params anthropic.MessageNewParams, onDelta, onThinking func(string)) (anthropic.Message, error) {
		token, err := source.token(ctx)
		if err != nil {
			return anthropic.Message{}, err
		}
		client := anthropic.NewClient(option.WithAuthToken(token))
		message, err := newAnthropicStreamer(client)(ctx, params, onDelta, onThinking)
		if err == nil || !aiLooksLikeAuthError(err) {
			return message, err
		}
		// Anthropic rejected the cached token (revoked, or expired despite
		// the margin). It fails before any content streams, so one silent
		// re-vend and retry is safe — a second rejection is the turn's.
		source.invalidate(token)
		token, vendErr := source.token(ctx)
		if vendErr != nil {
			return message, vendErr
		}
		client = anthropic.NewClient(option.WithAuthToken(token))
		return newAnthropicStreamer(client)(ctx, params, onDelta, onThinking)
	}
}

// aiLooksLikeAuthError reports whether a model-call failure is Anthropic
// rejecting the credential — the only failure worth a token re-vend.
func aiLooksLikeAuthError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "authentication_error") || strings.Contains(message, "401")
}

// resolveAICredentials returns how the session should authenticate. An
// explicit key always beats provided access, so a Doer who needs a personal
// key (evals, another org) just exports or saves one — no opt-out concept.
// Non-doers without a key get nothing: the guided key setup is their path,
// byte-for-byte unchanged. The doer gate here only avoids a pointless round
// trip — the vend endpoint's entitlement check is the authoritative one.
func resolveAICredentials(settings aiSettings) aiCredentials {
	if key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); key != "" {
		return aiCredentials{source: aiCredsSourceEnv, key: key}
	}
	if key := strings.TrimSpace(settings.APIKey); key != "" {
		return aiCredentials{source: aiCredsSourceSettings, key: key}
	}
	if aiProvidedEnabled(settings) && cachedTokenIsDoer() {
		return aiCredentials{source: aiCredsSourceProvided, provided: newAIProvidedTokenSource()}
	}
	return aiCredentials{}
}

// aiProvidedEnabled reports whether provided access may be attempted:
// DCI_AI_PROVIDED=off or {"provided": "off"} in ai_settings.json disables it.
// The env var wins over the settings file and an unrecognized value falls
// through rather than failing a session over a config typo — the same
// precedence and tolerance as resolveAIEffort (ai_session.go).
func aiProvidedEnabled(settings aiSettings) bool {
	for _, value := range []string{os.Getenv(aiProvidedEnvName), settings.Provided} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "off", "false", "0":
			return false
		case "on", "true", "1":
			return true
		}
	}
	return true
}

// aiVendedToken is the vend endpoint's response (AI-FEDERATION-SPEC §4.5).
type aiVendedToken struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// aiProvidedTokenSource caches one vended token and re-fetches before it
// expires. fetch is a function value so tests script vends without a server
// — the same seam pattern as aiModelStreamer.
type aiProvidedTokenSource struct {
	fetch func(ctx context.Context) (aiVendedToken, error)

	mu        sync.Mutex
	cached    string
	refreshAt time.Time // start re-fetching here (expiry minus a margin)
	expiresAt time.Time // hard expiry: never serve the token past this
}

func newAIProvidedTokenSource() *aiProvidedTokenSource {
	return &aiProvidedTokenSource{fetch: fetchAIProvidedToken}
}

// invalidate drops the cached token if it is still the one that just failed,
// so the next token() call re-vends. The equality check keeps a concurrent
// refresh's newer token intact.
func (s *aiProvidedTokenSource) invalidate(failed string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached == failed {
		s.cached = ""
	}
}

// token returns a currently-valid vended token, fetching when the cache is
// empty or inside the refresh window. A failed refresh keeps serving the
// cached token until its hard expiry (the vend endpoint may be briefly
// unreachable while the token is still good); past that, the fetch error is
// the caller's.
func (s *aiProvidedTokenSource) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.cached != "" && now.Before(s.refreshAt) {
		return s.cached, nil
	}
	vended, err := s.fetch(ctx)
	if err != nil {
		if s.cached != "" && now.Before(s.expiresAt) {
			return s.cached, nil
		}
		return "", err
	}
	if strings.TrimSpace(vended.AccessToken) == "" {
		return "", errors.New("the DoiT API returned an empty AI access token — try again, or export ANTHROPIC_API_KEY")
	}
	lifetime := time.Duration(vended.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 5 * time.Minute // defensive: a missing expires_in still refreshes soon
	}
	margin := 2 * time.Minute
	if quarter := lifetime / 4; quarter < margin {
		margin = quarter // short-lived tokens keep a proportional margin
	}
	s.cached = strings.TrimSpace(vended.AccessToken)
	s.expiresAt = now.Add(lifetime)
	s.refreshAt = s.expiresAt.Add(-margin)
	return s.cached, nil
}

// aiProvidedEndpoint resolves the vend URL: the override env var, else the
// vend path on the configured API base. Either way the active customer
// context rides along as the customerContext query parameter: the console
// API's employee auth middleware refuses any Doer token whose request lacks
// one — with a 401, indistinguishable from an expired session — even though
// the vended token itself is tenant-independent. An override URL that already
// carries the parameter keeps its own value.
func aiProvidedEndpoint() (string, error) {
	endpoint := strings.TrimSpace(os.Getenv(aiProvidedURLEnvName))
	if endpoint == "" {
		base, err := apiBase()
		if err != nil {
			return "", err
		}
		endpoint = strings.TrimRight(base, "/") + aiProvidedPath
	}
	customerContext := readCustomerContext(resolveDCIConfigDir())
	if customerContext == "" {
		return endpoint, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid AI vend URL: %w", err)
	}
	query := parsed.Query()
	if query.Get("customerContext") == "" {
		query.Set("customerContext", customerContext)
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}

// aiDCIBearer returns the DoiT access token for the vend call, refreshing an
// expired one first. Every other request gets this from restish's auth
// handler (authorizationCodeHandler.OnRequest, login_page.go); the vend call
// bypasses restish, so before this it sent an expired bearer, got a 401, and
// told the user to log in again although their refresh token was still good.
//
// The cache file is re-read first: in a session the commands the model runs
// are child processes that refresh the token on disk, so the in-memory copy
// can be stale, and refreshing from it would write an older token over the
// children's newer one.
//
// The refresh never opens a browser. Restish's RefreshTokenSource falls back
// to its wrapped source when the refresh fails, and in the real handler that
// source is the interactive PKCE login, which must not start in the middle of
// a session turn or a piped one-shot. Here the fallback just reports that the
// session needs a new login.
func aiDCIBearer() (string, error) {
	if cli.Cache == nil {
		return "", nil
	}
	reloadCredentialCache()
	const key = "dci:default"
	bearer := strings.TrimSpace(cli.Cache.GetString(key + ".token"))
	if bearer == "" {
		return "", nil
	}
	// No recorded expiry means nothing to refresh against; send the token
	// as-is and let the API judge it. DCI_API_KEY lands here with a far-future
	// expiry (applyAPIKeyAuth), so it is reused without a refresh too.
	if cli.Cache.GetTime(key + ".expires").IsZero() {
		return bearer, nil
	}
	source := &oauth.RefreshTokenSource{
		ClientID:     dciOAuthClientID,
		TokenURL:     dciOAuthTokenURL,
		RefreshToken: cli.Cache.GetString(key + ".refresh"),
		TokenSource:  aiNoInteractiveLogin{},
	}
	// TokenHandler reuses the cached token while it is valid, otherwise
	// refreshes it and writes the new one back to the cache, then sets the
	// Authorization header on the request it is given.
	probe, err := http.NewRequest(http.MethodPost, "https://dci.invalid/", nil)
	if err != nil {
		return "", err
	}
	if err := oauth.TokenHandler(source, key, probe); err != nil {
		return "", errors.New("your DoiT session has expired — run dci login and ask again")
	}
	_, refreshed, found := strings.Cut(probe.Header.Get("Authorization"), " ")
	if !found || strings.TrimSpace(refreshed) == "" {
		return "", errors.New("your DoiT session has expired — run dci login and ask again")
	}
	return strings.TrimSpace(refreshed), nil
}

// aiNoInteractiveLogin is the fallback when the refresh token cannot renew
// the session: it fails instead of starting the browser login.
type aiNoInteractiveLogin struct{}

func (aiNoInteractiveLogin) Token() (*oauth2.Token, error) {
	return nil, errors.New("the DoiT session cannot be refreshed without signing in again")
}

// fetchAIProvidedToken calls the vend endpoint with the same cached bearer
// every other CLI call uses (dci login and DCI_API_KEY both land in the
// restish cache slot — see applyAPIKeyOverride, main.go). Error strings are
// user-facing and self-contained: they flow verbatim through the session's
// error event, so each names its fix and deliberately avoids the raw
// status-code vocabulary aiFriendlyAPIError rewrites (ai_tui.go).
func fetchAIProvidedToken(ctx context.Context) (aiVendedToken, error) {
	endpoint, err := aiProvidedEndpoint()
	if err != nil {
		return aiVendedToken{}, err
	}
	bearer, err := aiDCIBearer()
	if err != nil {
		return aiVendedToken{}, err
	}
	if bearer == "" {
		return aiVendedToken{}, errors.New("DoiT-provided AI access needs a DoiT sign-in — run dci login first")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return aiVendedToken{}, err
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return aiVendedToken{}, fmt.Errorf("could not reach the DoiT API for AI access: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
		var vended aiVendedToken
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&vended); err != nil {
			return aiVendedToken{}, fmt.Errorf("the DoiT API returned an unreadable AI access response: %w", err)
		}
		return vended, nil
	case http.StatusUnauthorized:
		// The employee auth middleware answers 401 both for a stale session
		// and for a Doer request with no customer context; with no context to
		// send, name the actual fix instead of a re-login that won't help.
		if readCustomerContext(resolveDCIConfigDir()) == "" {
			return aiVendedToken{}, errors.New("DoiT-provided AI access needs a customer context — run dci customer-context set <domain> and ask again")
		}
		return aiVendedToken{}, errors.New("your DoiT session has expired — run dci login and ask again")
	case http.StatusForbidden:
		return aiVendedToken{}, errors.New("your account is not enabled for DoiT-provided AI access — export ANTHROPIC_API_KEY or save a key in " + aiSettingsFileName + " instead")
	case http.StatusNotFound:
		// The endpoint predates its backend: the CLI can ship first (spec
		// §5.2) and degrade to the key paths with an honest message.
		return aiVendedToken{}, errors.New("DoiT-provided AI access is not available on this API yet — export ANTHROPIC_API_KEY or save a key in " + aiSettingsFileName)
	default:
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 200))
		return aiVendedToken{}, fmt.Errorf("the DoiT API refused to issue an AI access token (%s): %s",
			response.Status, strings.TrimSpace(string(snippet)))
	}
}
