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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
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
// Why this does not reuse restish's oauth.TokenHandler:
//   - TokenHandler stores the refreshed token with cli.Cache.Set, which is
//     viper's override layer. reloadCredentialCache (ReadInConfig) never
//     replaces overrides, so after one refresh the session would keep its
//     own token over a later /login, account switch or logout done by a
//     child process, and could write the old account back to disk later.
//   - Its token request uses http.DefaultClient with no timeout and no
//     context, so a stalled token endpoint would hang the turn past Esc.
//   - It runs on the session goroutine while the TUI reads cli.Cache, and
//     viper is not safe for a concurrent write.
//
// So the session state is read from, and the refresh written to, a scratch
// viper loaded from cache.json; cli.Cache is never touched here and picks
// the new token up on the TUI's next reloadCredentialCache. The refresh is a
// single request bounded by ctx and aiDCIRefreshTimeout, and it never falls
// back to the browser login, which must not start in the middle of a turn.
func aiDCIBearer(ctx context.Context) (string, error) {
	// DCI_API_KEY is a long-lived key with nothing to refresh;
	// applyAPIKeyAuth puts it in cli.Cache only, never on disk.
	if key := strings.TrimSpace(os.Getenv(apiKeyEnvName)); key != "" {
		return key, nil
	}
	cache := aiDCICacheFromDisk()
	if cache == nil {
		return "", nil
	}
	bearer := strings.TrimSpace(cache.GetString(aiDCICacheKey + ".token"))
	if bearer == "" {
		return "", nil
	}
	// No recorded expiry means nothing to refresh against; send the token
	// as-is and let the API judge it.
	expiry := cache.GetTime(aiDCICacheKey + ".expires")
	if expiry.IsZero() || time.Until(expiry) > aiDCIRefreshMargin {
		return bearer, nil
	}
	refresh := strings.TrimSpace(cache.GetString(aiDCICacheKey + ".refresh"))
	if refresh == "" {
		return "", errAIDCISessionExpired
	}
	token, err := aiRefreshDCIToken(ctx, refresh)
	if err != nil {
		return "", err
	}
	// Same keys and shapes restish's TokenHandler writes, so ordinary
	// commands read this token back unchanged.
	cache.Set(aiDCICacheKey+".expires", token.expiry)
	cache.Set(aiDCICacheKey+".type", token.tokenType)
	cache.Set(aiDCICacheKey+".token", token.accessToken)
	if token.refreshToken != "" {
		// A refresh response without a new refresh token means the old one
		// stays valid; keep it.
		cache.Set(aiDCICacheKey+".refresh", token.refreshToken)
	}
	if err := cache.WriteConfig(); err != nil {
		// Failing here rather than using the token: a rotated refresh token
		// that never reached disk would be lost, and the next refresh would
		// fail with a misleading "run dci login".
		return "", fmt.Errorf("could not save the renewed DoiT session: %w", err)
	}
	return token.accessToken, nil
}

const aiDCICacheKey = "dci:default" // main.go pins the restish profile to "default"

// aiDCIRefreshMargin renews a token this close to expiry, matching the
// oauth2 package's expiryDelta that restish's ReuseTokenSource applies.
const aiDCIRefreshMargin = 10 * time.Second

// aiDCIRefreshTimeout bounds the token request like the vend call's client;
// a var so tests can shorten it.
var aiDCIRefreshTimeout = 15 * time.Second

var errAIDCISessionExpired = errors.New("your DoiT session has expired — run dci login and ask again")

// aiDCICacheFromDisk loads restish's cache.json into a private viper, or
// returns nil when there is no readable cache (not signed in).
func aiDCICacheFromDisk() *viper.Viper {
	path := ""
	if cli.Cache != nil {
		path = cli.Cache.ConfigFileUsed()
	}
	if path == "" {
		if dir := viper.GetString("cache-dir"); dir != "" {
			path = filepath.Join(dir, "cache.json")
		}
	}
	if path == "" {
		return nil
	}
	cache := viper.New()
	cache.SetConfigFile(path)
	if err := cache.ReadInConfig(); err != nil {
		return nil
	}
	return cache
}

// aiDCIOAuthParams returns the OAuth params of the dci profile in apis.json,
// the same ones restish's handler refreshes ordinary commands with, falling
// back to the values writeConfig puts there. An apis.json that names its own
// token URL or client is honored, so its refresh token never goes to a
// token endpoint that did not issue it.
func aiDCIOAuthParams() map[string]string {
	params := map[string]string{}
	if data, err := os.ReadFile(aiDCIAPIsConfigPath()); err == nil {
		var config struct {
			DCI struct {
				Profiles map[string]struct {
					Auth struct {
						Name   string            `json:"name"`
						Params map[string]string `json:"params"`
					} `json:"auth"`
				} `json:"profiles"`
			} `json:"dci"`
		}
		if json.Unmarshal(data, &config) == nil {
			if auth := config.DCI.Profiles["default"].Auth; auth.Name == "oauth-authorization-code" {
				for k, v := range auth.Params {
					params[k] = v
				}
			}
		}
	}
	if strings.TrimSpace(params["token_url"]) == "" || strings.TrimSpace(params["client_id"]) == "" {
		params["token_url"] = dciOAuthTokenURL
		params["client_id"] = dciOAuthClientID
	}
	return params
}

// aiDCIAPIsConfigPath is the apis.json restish loaded (its config-directory,
// which under DCI_API_BASE_URL is the per-invocation copy). A var so tests
// can point it away from the developer's real config.
var aiDCIAPIsConfigPath = func() string {
	dir := viper.GetString("config-directory")
	if dir == "" {
		dir = dciConfigDir()
	}
	return filepath.Join(dir, "apis.json")
}

type aiDCIToken struct {
	accessToken  string
	tokenType    string
	refreshToken string
	expiry       time.Time
}

// aiRefreshDCIToken trades the refresh token for a new access token with the
// same request restish's RefreshTokenSource sends (oauth/refresh.go and
// oauth/request.go @ v0.21.2), but bounded by ctx and aiDCIRefreshTimeout.
// Only a refusal from the token endpoint means the session is over; an
// outage or a cancelled turn is reported as what it is.
func aiRefreshDCIToken(ctx context.Context, refresh string) (aiDCIToken, error) {
	params := aiDCIOAuthParams()
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {params["client_id"]},
		"refresh_token": {refresh},
		"scope":         {strings.Join(strings.Split(params["scopes"], ","), " ")},
	}
	for k, v := range params {
		switch k {
		case "client_id", "client_secret", "scopes", "authorize_url", "token_url", "redirect_url":
		default:
			form.Set(k, v) // custom endpoint params, as restish copies them
		}
	}
	reqCtx, cancel := context.WithTimeout(ctx, aiDCIRefreshTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(reqCtx, http.MethodPost, params["token_url"], strings.NewReader(form.Encode()))
	if err != nil {
		return aiDCIToken{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return aiDCIToken{}, ctx.Err()
		}
		return aiDCIToken{}, fmt.Errorf("could not reach the DoiT sign-in service to renew your session: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusOK:
	case response.StatusCode >= 400 && response.StatusCode < 500:
		// invalid_grant and friends: the refresh token is revoked or expired.
		return aiDCIToken{}, errAIDCISessionExpired
	default:
		return aiDCIToken{}, fmt.Errorf("the DoiT sign-in service could not renew your session (%s) — try again shortly", response.Status)
	}
	var decoded struct {
		TokenType    string    `json:"token_type"`
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		ExpiresIn    int64     `json:"expires_in"`
		Expiry       time.Time `json:"expiry"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil || strings.TrimSpace(decoded.AccessToken) == "" {
		return aiDCIToken{}, errors.New("the DoiT sign-in service returned an unreadable session renewal — run dci login and ask again")
	}
	expiry := decoded.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(time.Duration(decoded.ExpiresIn) * time.Second)
	}
	tokenType := decoded.TokenType
	if tokenType == "" {
		tokenType = "Bearer" // oauth2.Token.Type()'s default, which restish stores
	}
	return aiDCIToken{
		accessToken:  strings.TrimSpace(decoded.AccessToken),
		tokenType:    tokenType,
		refreshToken: decoded.RefreshToken,
		expiry:       expiry,
	}, nil
}

// fetchAIProvidedToken calls the vend endpoint with the same bearer every
// other CLI call uses: DCI_API_KEY, or the dci login session, renewed first
// when it has expired (aiDCIBearer). Error strings are
// user-facing and self-contained: they flow verbatim through the session's
// error event, so each names its fix and deliberately avoids the raw
// status-code vocabulary aiFriendlyAPIError rewrites (ai_tui.go).
func fetchAIProvidedToken(ctx context.Context) (aiVendedToken, error) {
	endpoint, err := aiProvidedEndpoint()
	if err != nil {
		return aiVendedToken{}, err
	}
	bearer, err := aiDCIBearer(ctx)
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
