package main

// Login pages: a dci-owned copy of restish's OAuth 2.0 authorization-code +
// PKCE flow (oauth/authcode.go @ v0.21.2) so the browser callback on
// localhost:8484 serves DoiT Cloud Intelligence™-branded pages instead of
// restish's stock HTML, which lives in private vars we cannot override.
// Behavioral parity with restish is intentional and must be preserved: PKCE
// S256 challenge, http://localhost:8484 default redirect, manual code entry
// on a TTY, server shutdown after the code arrives, os.Exit(1) on an empty
// code, and refresh-token caching under "<key>.refresh" via the exported
// oauth.RefreshTokenSource/oauth.TokenHandler. One deliberate fix over
// restish: the error page HTML-escapes the reflected query params.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/oauth"
	"golang.org/x/oauth2"
)

// loginRunSuggestion is the fixed command offered on the success page. It is
// compiled in — nothing from the HTTP request or the page ever selects or
// parameterizes what runs. Must match the command shown in the chip markup.
var loginRunSuggestion = []string{"get-report", "--chart"}

// Chip variants substituted for $TRYNEXT in loginSuccessHTML at serve time.
const loginTryNextStatic = `<div class="trylabel">Try next</div>
    <div class="term" aria-hidden="true">
      <span class="prompt">$</span>
      <span>dci get-report --chart&nbsp;<span class="cursor"></span></span>
    </div>`

const loginTryNextRun = `<div class="trylabel">Try next &mdash; click to run</div>
    <a class="term" href="/run?t=$RUNTOKEN">
      <span class="prompt">$</span>
      <span>dci get-report --chart&nbsp;<span class="cursor"></span></span>
    </a>
    <p class="runhint">Runs it in the terminal you came from.</p>`

// runOffer is the one-click "run it for me" state for a single login: the
// success page links to /run?t=<token>, and the login command waits a grace
// window for that click after the flow completes (issue #88).
type runOffer struct {
	token        string
	exchangeDone chan struct{} // closed once the token exchange succeeded
	clicked      chan struct{} // closed on the first valid /run request
	clickOnce    sync.Once
}

// loginRunOffer is armed only by the `dci login` command; every other
// invocation that happens to trigger the browser flow serves the static chip
// and shuts the callback server down immediately, exactly as before.
var loginRunOffer *runOffer

// Overridable in tests; production values per issue #88 decisions.
var (
	runClickGraceWindow = 60 * time.Second
	runExchangeWait     = 10 * time.Second
	runSuggestedCommand = execLoginRunSuggestion
)

func armLoginRunOffer() {
	// Scripted and agent invocations must keep today's prompt-returns-
	// immediately behavior; the offer (and its grace window) is a human,
	// interactive-terminal affordance only.
	if agentUAMode == uaModeAgent || !stderrIsTTY() {
		return
	}
	loginRunOffer = newRunOffer()
}

func newRunOffer() *runOffer {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil // no offer rather than a guessable token
	}
	return &runOffer{
		token:        base64.RawURLEncoding.EncodeToString(tokenBytes),
		exchangeDone: make(chan struct{}),
		clicked:      make(chan struct{}),
	}
}

// renderLoginSuccessPage fills the $TRYNEXT slot: a click-to-run chip when an
// offer is armed, the static chip otherwise.
func renderLoginSuccessPage() string {
	offer := loginRunOffer
	if offer == nil {
		return strings.Replace(loginSuccessHTML, "$TRYNEXT", loginTryNextStatic, 1)
	}
	chip := strings.Replace(loginTryNextRun, "$RUNTOKEN", offer.token, 1)
	return strings.Replace(loginSuccessHTML, "$TRYNEXT", chip, 1)
}

// maybeWaitForRunClick holds the login command open for the grace window so a
// click on the success page chip can run the suggested command in this
// terminal. No-op unless the browser flow actually completed an exchange.
func maybeWaitForRunClick() {
	offer := loginRunOffer
	if offer == nil {
		return
	}
	select {
	case <-offer.exchangeDone:
	default:
		return // cached/refresh token — no browser page was served
	}
	fmt.Fprintf(os.Stderr, "Waiting %d seconds in case you click the suggested command in the browser — Ctrl-C to skip.\n", int(runClickGraceWindow.Seconds()))
	select {
	case <-offer.clicked:
		runSuggestedCommand()
	case <-time.After(runClickGraceWindow):
	}
}

// execLoginRunSuggestion runs the fixed suggestion in this terminal and exits
// with its status; the login process becomes a thin wrapper around it.
func execLoginRunSuggestion() {
	fmt.Fprintf(os.Stderr, "$ dci %s\n", strings.Join(loginRunSuggestion, " "))
	cmd := exec.Command(executablePath(), loginRunSuggestion...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if err == nil {
		os.Exit(0)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	fmt.Fprintf(os.Stderr, "failed to run suggested command: %v\n", err)
	os.Exit(1)
}

// loginCallbackHandler answers the OAuth redirect on the local server: it
// serves the branded HTML and sends the `code` query param (or "" on an
// OAuth error) over the channel.
type loginCallbackHandler struct {
	c chan string
}

func (h loginCallbackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/run" {
		h.serveRun(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html")

	if errName := r.URL.Query().Get("error"); errName != "" {
		details := r.URL.Query().Get("error_description")
		// Single pass so a value containing a placeholder token (e.g.
		// error=$DETAILS) can never hijack the other substitution.
		rendered := strings.NewReplacer(
			"$ERROR", html.EscapeString(errName),
			"$DETAILS", html.EscapeString(details),
		).Replace(loginErrorHTML)
		_, _ = w.Write([]byte(rendered))
		h.c <- ""
		return
	}

	h.c <- r.URL.Query().Get("code")
	_, _ = w.Write([]byte(renderLoginSuccessPage()))
}

// serveRun answers a click on the success page chip. Only a request carrying
// the one-time token embedded in that page is honored, and only after the
// token exchange has succeeded; everything else is a 404 with no side
// effects. Repeat clicks re-render the running page but signal only once.
func (h loginCallbackHandler) serveRun(w http.ResponseWriter, r *http.Request) {
	offer := loginRunOffer
	t := r.URL.Query().Get("t")
	if offer == nil || subtle.ConstantTimeCompare([]byte(t), []byte(offer.token)) != 1 {
		http.NotFound(w, r)
		return
	}
	select {
	case <-offer.exchangeDone:
	case <-time.After(runExchangeWait):
		http.NotFound(w, r)
		return
	}
	offer.clickOnce.Do(func() { close(offer.clicked) })
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte(loginRunningHTML))
}

// readManualCode waits for user input and sends it to the channel with the
// trailing newline removed.
func readManualCode(input chan string) {
	r := bufio.NewReader(os.Stdin)
	result, err := r.ReadString('\n')
	if err != nil {
		panic(err)
	}

	input <- strings.TrimRight(result, "\n")
}

// oauthTokenResponse parses token-provider responses regardless of whether
// `expires_in` or `expiry` is returned.
type oauthTokenResponse struct {
	TokenType    string        `json:"token_type"`
	AccessToken  string        `json:"access_token"`
	RefreshToken string        `json:"refresh_token,omitempty"`
	ExpiresIn    time.Duration `json:"expires_in"`
	Expiry       time.Time     `json:"expiry,omitempty"`
}

// requestOAuthToken POSTs the form-encoded payload to the token URL and
// returns the parsed token.
func requestOAuthToken(tokenURL, payload string) (*oauth2.Token, error) {
	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(payload))
	if err != nil {
		return nil, err
	}

	req.Header.Add("content-type", "application/x-www-form-urlencoded")

	cli.LogDebugRequest(req)

	start := time.Now()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	cli.LogDebugResponse(start, res)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	if res.StatusCode > 200 {
		return nil, fmt.Errorf("bad response from token endpoint:\n%s", body)
	}

	decoded := oauthTokenResponse{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}

	expiry := decoded.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(decoded.ExpiresIn * time.Second)
	}

	token := &oauth2.Token{
		AccessToken:  decoded.AccessToken,
		TokenType:    decoded.TokenType,
		RefreshToken: decoded.RefreshToken,
		Expiry:       expiry,
	}

	return token, nil
}

// authorizationCodeTokenSource runs the PKCE flow: a local HTTP server on
// port 8484, a browser log-in that redirects back with an authorization
// code, and a token request exchanging that code.
type authorizationCodeTokenSource struct {
	ClientID       string
	ClientSecret   string
	AuthorizeURL   string
	TokenURL       string
	RedirectURL    string
	EndpointParams *url.Values
	Scopes         []string
}

func (ac *authorizationCodeTokenSource) getRedirectURL() string {
	if ac.RedirectURL == "" {
		return "http://localhost:8484"
	}

	return ac.RedirectURL
}

// loginFlowHeadless reports whether the interactive browser flow cannot
// complete: explicit agent mode, or no terminal on stderr (the same
// human-presence predicate armLoginRunOffer uses). A var so tests can force
// both outcomes without a PTY.
var loginFlowHeadless = func() bool {
	return agentUAMode == uaModeAgent || !stderrIsTTY()
}

// Token generates a new token using an authorization code.
func (ac *authorizationCodeTokenSource) Token() (*oauth2.Token, error) {
	// Headless guard: everything below needs a human to complete the browser
	// round-trip, and the select on the callback has no timeout. Reaching here
	// in agent mode or with no terminal at all — an AI-session child, CI, or
	// an API command's --help loading the description with no cached token —
	// would hang forever. Keyed off stderr (the channel this flow talks to
	// the human on), NOT stdin: a piped stdin is normally the request body
	// (dci query < query.json) with a human still present, and must keep the
	// browser flow — e.g. for re-auth after a stale refresh token.
	if loginFlowHeadless() {
		return nil, errors.New("no credentials available and this session cannot open a browser to log in: set DCI_API_KEY, or run dci login from an interactive terminal")
	}

	// Generate a random code verifier string
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, err
	}

	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Generate a code challenge. Only the challenge is sent when requesting a
	// code which allows us to keep it secret for now.
	shaBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(shaBytes[:])

	// Generate a URL with the challenge to have the user log in.
	authorizeURL, err := url.Parse(ac.AuthorizeURL)
	if err != nil {
		panic(err)
	}

	aq := authorizeURL.Query()
	aq.Set("response_type", "code")
	aq.Set("code_challenge", challenge)
	aq.Set("code_challenge_method", "S256")
	aq.Set("client_id", ac.ClientID)
	aq.Set("redirect_uri", ac.getRedirectURL())
	aq.Set("scope", strings.Join(ac.Scopes, " "))
	if ac.EndpointParams != nil {
		for k, v := range *ac.EndpointParams {
			aq.Set(k, v[0])
		}
	}
	authorizeURL.RawQuery = aq.Encode()

	// Run server before opening the user's browser so we are ready for any redirect.
	codeChan := make(chan string)
	handler := loginCallbackHandler{
		c: codeChan,
	}

	// strip protocol prefix from configured redirect url for local webserver
	u, err := url.Parse(ac.getRedirectURL())
	if err != nil {
		panic(err)
	}
	redirectServer := fmt.Sprintf("%s:%s", u.Hostname(), u.Port())

	s := &http.Server{
		Addr:           redirectServer,
		Handler:        handler,
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   5 * time.Second,
		MaxHeaderBytes: 1024,
	}

	go func() {
		// Run in a goroutine until the server is closed or we get an error.
		if err := s.ListenAndServe(); err != http.ErrServerClosed {
			panic(err)
		}
	}()

	// Open auth URL in browser, print for manual use in case open fails.
	fmt.Fprintln(os.Stderr, "Open your browser to log in using the URL:")
	fmt.Fprintln(os.Stderr, authorizeURL.String())
	_ = openInBrowser(authorizeURL.String())

	// Provide a way to manually enter the code, e.g. for remote SSH sessions.
	// Only read from stdin if it is a live terminal, if a file or command has
	// been piped in it is likely the request body to use after auth.
	manualCodeChan := make(chan string)
	if isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		fmt.Fprint(os.Stderr, "Alternatively, enter the code manually: ")
		go readManualCode(manualCodeChan)
	}

	// Get code from handler, exchange it for a token, and then return it. This
	// select blocks until one code becomes available.
	// There is currently no timeout.
	var code string
	select {
	case code = <-codeChan:
	case code = <-manualCodeChan:
	}
	fmt.Fprintln(os.Stderr, "")
	// With a run offer armed, the server must outlive the exchange so the
	// success page's /run link stays reachable through the grace window; the
	// process exit (or exec of the suggestion) bounds its lifetime instead.
	if loginRunOffer == nil {
		s.Shutdown(context.Background())
	}

	if code == "" {
		fmt.Fprintln(os.Stderr, "Unable to get a code. See browser for details. Aborting!")
		os.Exit(1)
	}

	payload := url.Values{}
	payload.Set("grant_type", "authorization_code")
	payload.Set("client_id", ac.ClientID)
	payload.Set("code_verifier", verifier)
	payload.Set("code", code)
	payload.Set("redirect_uri", ac.getRedirectURL())
	if ac.ClientSecret != "" {
		payload.Set("client_secret", ac.ClientSecret)
	}

	token, err := requestOAuthToken(ac.TokenURL, payload.Encode())
	if err == nil {
		if offer := loginRunOffer; offer != nil {
			close(offer.exchangeDone)
		}
	}
	return token, err
}

// authorizationCodeHandler is the dci registration for the
// "oauth-authorization-code" auth type. Same parameter surface as restish's
// oauth.AuthorizationCodeHandler, so the existing apis.json config and
// cached credentials keep working unchanged.
type authorizationCodeHandler struct{}

// Parameters returns a list of OAuth2 Authorization Code inputs.
func (h *authorizationCodeHandler) Parameters() []cli.AuthParam {
	return []cli.AuthParam{
		{Name: "client_id", Required: true, Help: "OAuth 2.0 Client ID"},
		{Name: "client_secret", Required: false, Help: "OAuth 2.0 Client Secret if exists"},
		{Name: "authorize_url", Required: true, Help: "OAuth 2.0 authorization URL, e.g. https://api.example.com/oauth/authorize"},
		{Name: "token_url", Required: true, Help: "OAuth 2.0 token URL, e.g. https://api.example.com/oauth/token"},
		{Name: "scopes", Help: "Optional scopes to request in the token"},
		{Name: "redirect_url", Help: "Optional redirect URL with protocol and port, defaults to 'http://localhost:8484' if not specified. "},
	}
}

// OnRequest gets run before the request goes out on the wire.
func (h *authorizationCodeHandler) OnRequest(request *http.Request, key string, params map[string]string) error {
	if request.Header.Get("Authorization") == "" {
		endpointParams := url.Values{}
		for k, v := range params {
			if k == "client_id" || k == "client_secret" || k == "scopes" || k == "authorize_url" || k == "token_url" || k == "redirect_url" {
				// Not a custom param...
				continue
			}

			endpointParams.Add(k, v)
		}

		source := &authorizationCodeTokenSource{
			ClientID:       params["client_id"],
			ClientSecret:   params["client_secret"],
			AuthorizeURL:   params["authorize_url"],
			TokenURL:       params["token_url"],
			RedirectURL:    params["redirect_url"],
			EndpointParams: &endpointParams,
			Scopes:         strings.Split(params["scopes"], ","),
		}

		// Try to get a cached refresh token from the current profile and use
		// it to wrap the auth code token source with a refreshing source.
		refreshKey := key + ".refresh"
		refreshSource := oauth.RefreshTokenSource{
			ClientID:       params["client_id"],
			TokenURL:       params["token_url"],
			Scopes:         strings.Split(params["scopes"], ","),
			EndpointParams: &endpointParams,
			RefreshToken:   cli.Cache.GetString(refreshKey),
			TokenSource:    source,
		}

		return oauth.TokenHandler(&refreshSource, key, request)
	}

	return nil
}

// loginPageWordmarkPNG is the "Cloud Intelligence™" wordmark bitmap from
// console.doit.com, inlined as base64 so the pages make no external requests.
const loginPageWordmarkPNG = "iVBORw0KGgoAAAANSUhEUgAABDoAAAB/CAYAAAAO96J1AAAABGdBTUEAALGPC/xhBQAAACBjSFJNAAB6JgAAgIQAAPoAAACA6AAAdTAAAOpgAAA6mAAAF3CculE8AAAAhGVYSWZNTQAqAAAACAAFARIAAwAAAAEAAQAAARoABQAAAAEAAABKARsABQAAAAEAAABSASgAAwAAAAEAAgAAh2kABAAAAAEAAABaAAAAAAAAAEgAAAABAAAASAAAAAEAA6ABAAMAAAABAAEAAKACAAQAAAABAAAEOqADAAQAAAABAAAAfwAAAADLKsbIAAAACXBIWXMAAAsTAAALEwEAmpwYAAABWWlUWHRYTUw6Y29tLmFkb2JlLnhtcAAAAAAAPHg6eG1wbWV0YSB4bWxuczp4PSJhZG9iZTpuczptZXRhLyIgeDp4bXB0az0iWE1QIENvcmUgNi4wLjAiPgogICA8cmRmOlJERiB4bWxuczpyZGY9Imh0dHA6Ly93d3cudzMub3JnLzE5OTkvMDIvMjItcmRmLXN5bnRheC1ucyMiPgogICAgICA8cmRmOkRlc2NyaXB0aW9uIHJkZjphYm91dD0iIgogICAgICAgICAgICB4bWxuczp0aWZmPSJodHRwOi8vbnMuYWRvYmUuY29tL3RpZmYvMS4wLyI+CiAgICAgICAgIDx0aWZmOk9yaWVudGF0aW9uPjE8L3RpZmY6T3JpZW50YXRpb24+CiAgICAgIDwvcmRmOkRlc2NyaXB0aW9uPgogICA8L3JkZjpSREY+CjwveDp4bXBtZXRhPgoZXuEHAABAAElEQVR4Ae29S5ITyfbn765HGWZU/Up3+LceXNUKUJFcszsrMesZWSsgmfWMzBWQrIBk2COSFZCsADErsyJBNfvPEKPu2VUV0EaXQvI+x12RCkkhKfwRL+nrRiIpwh/HP+Hhfvz4Swg4EAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAABEAAB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"

// loginSuccessHTML is served when the OAuth redirect carries a `code` param;
// $TRYNEXT is replaced at serve time with the static or click-to-run chip.
const loginSuccessHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>dci — logged in</title>
<style>
  :root {
    --ink: #050A2E;
    --ink-soft: rgba(5, 10, 46, 0.62);
    --pink: #EB6087;
    --ok: #0BA05F;
    --card: #FFFFFF;
    --ground: #FBFAF7;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    font-family: Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    background: var(--ground);
    color: var(--ink);
    display: grid;
    place-items: center;
    -webkit-font-smoothing: antialiased;
  }
  .bg {
    position: fixed; inset: 0; z-index: 0;
    background:
      repeating-linear-gradient(90deg,
        rgba(255,255,255,0) 0,
        rgba(255,255,255,0) calc(100%/12 - 1.5px),
        rgba(255,255,255,0.6) calc(100%/12 - 1.5px),
        rgba(255,255,255,0.6) calc(100%/12)),
      repeating-linear-gradient(90deg,
        rgba(255,255,255,0.28) 0,
        rgba(255,255,255,0.28) calc(100%/12),
        rgba(255,255,255,0) calc(100%/12),
        rgba(255,255,255,0) calc(100%/4)),
      repeating-linear-gradient(90deg,
        rgba(255,255,255,0) 0,
        rgba(255,255,255,0) calc(100%/12 * 3),
        rgba(255,255,255,0.22) calc(100%/12 * 3),
        rgba(255,255,255,0.22) calc(100%/12 * 4),
        rgba(255,255,255,0) calc(100%/12 * 4),
        rgba(255,255,255,0) calc(100%/12 * 5)),
      radial-gradient(50% 55% at 6% -8%, rgba(124,102,215,0.34), transparent 68%),
      radial-gradient(38% 42% at 34% 0%, rgba(151,131,226,0.20), transparent 65%),
      radial-gradient(52% 60% at 70% -10%, rgba(124,102,215,0.30), transparent 70%),
      radial-gradient(36% 40% at 97% 18%, rgba(151,131,226,0.22), transparent 68%),
      radial-gradient(30% 34% at 52% 22%, rgba(151,131,226,0.10), transparent 68%),
      linear-gradient(180deg, #EFEBF9 0%, #F8F6F3 48%, #FCFBF9 100%);
  }
  .card {
    position: relative; z-index: 1;
    width: min(420px, calc(100vw - 48px));
    background: var(--card);
    border: 1px solid rgba(5, 10, 46, 0.07);
    border-radius: 16px;
    box-shadow: 0 24px 48px -24px rgba(5, 10, 46, 0.18), 0 2px 8px rgba(5, 10, 46, 0.05);
    padding: 40px 40px 32px;
    text-align: center;
    min-height: 505px;
    display: flex;
    flex-direction: column;
    animation: rise 480ms cubic-bezier(0.2, 0.9, 0.3, 1) both;
  }
  .lockup {
    display: flex; align-items: center; justify-content: center; gap: 9px;
    margin-bottom: 36px;
  }
  .lockup .mark { width: 30px; height: 30px; display: block; }
  .lockup .wordmark { height: 17px; display: block; }
  .main {
    flex: 1;
    display: flex;
    flex-direction: column;
    justify-content: center;
  }
  .check {
    width: 64px; height: 64px; margin: 0 auto 20px; display: block;
  }
  .check .ring {
    fill: none; stroke: var(--ok); stroke-width: 2.5; opacity: 0.25;
  }
  .check .tick {
    fill: none; stroke: var(--ok); stroke-width: 5;
    stroke-linecap: round; stroke-linejoin: round;
    stroke-dasharray: 48; stroke-dashoffset: 48;
    animation: draw 420ms 240ms cubic-bezier(0.65, 0, 0.35, 1) forwards;
  }
  h1 {
    font-size: 22px; font-weight: 600; letter-spacing: -0.01em;
    margin-bottom: 10px;
  }
  .sub {
    font-size: 14.5px; line-height: 1.55; color: var(--ink-soft);
    margin-bottom: 28px;
  }
  .trylabel {
    font-size: 11px; font-weight: 600; letter-spacing: 0.08em; text-transform: uppercase;
    color: rgba(5, 10, 46, 0.4);
    text-align: left;
    margin-bottom: 8px;
  }
  .term {
    display: flex; align-items: center; gap: 8px;
    background: #0B1030;
    border-radius: 10px;
    padding: 13px 16px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
    font-size: 13px;
    text-align: left;
    color: rgba(255,255,255,0.88);
  }
  .term .prompt { color: var(--pink); user-select: none; }
  .term .cursor {
    display: inline-block; width: 8px; height: 16px;
    background: rgba(255,255,255,0.75);
    animation: blink 1.1s steps(1) infinite;
    vertical-align: text-bottom;
  }
  a.term {
    text-decoration: none;
    cursor: pointer;
    transition: box-shadow 160ms ease, transform 160ms ease;
  }
  a.term:hover, a.term:focus-visible {
    box-shadow: 0 8px 22px -8px rgba(11, 16, 48, 0.55);
    transform: translateY(-1px);
  }
  .runhint {
    margin-top: 8px;
    font-size: 12px;
    color: rgba(5, 10, 46, 0.45);
  }
  .help {
    margin-top: 16px;
    font-size: 12.5px;
    color: rgba(5, 10, 46, 0.45);
  }
  .help a {
    color: rgba(5, 10, 46, 0.72);
    font-weight: 500;
    text-decoration: none;
    border-bottom: 1px solid rgba(5, 10, 46, 0.2);
    padding-bottom: 1px;
  }
  .help a:hover, .help a:focus-visible { color: #EB6087; border-bottom-color: #EB6087; }
  .foot {
    position: relative; z-index: 1;
    margin-top: 20px;
    font-size: 12px; color: rgba(5, 10, 46, 0.45);
    text-align: center;
    animation: rise 480ms 90ms cubic-bezier(0.2, 0.9, 0.3, 1) both;
  }
  .wrap { position: relative; z-index: 1; }
  @keyframes rise {
    from { opacity: 0; transform: translateY(10px); }
    to   { opacity: 1; transform: none; }
  }
  @keyframes draw { to { stroke-dashoffset: 0; } }
  @keyframes blink { 50% { opacity: 0; } }
  @media (prefers-reduced-motion: reduce) {
    .card, .foot { animation: none; }
    .check .tick { animation: none; stroke-dashoffset: 0; }
    .term .cursor { animation: none; }
  }
</style>
</head>
<body>
<div class="bg" aria-hidden="true"></div>
<div class="wrap">
  <div class="card">
    <div class="lockup">
      <svg class="mark" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
        <path d="M19.0769 10.2046C19.656 10.795 19.6383 11.7361 19.038 12.323C18.4476 12.9 17.4761 12.8901 16.9112 12.3011C16.3476 11.7142 16.366 10.7257 16.9501 10.1685C17.5496 9.59653 18.4964 9.61279 19.0769 10.2053V10.2046Z" fill="#EB6087"/>
        <path d="M14.9926 14.1899C15.0174 11.3828 15.0032 8.57579 14.9997 5.76875C14.9983 4.80288 14.3657 4.12571 13.4897 4.125C12.6179 4.125 11.9888 4.80995 11.9881 5.77158C11.9874 6.98511 11.9938 8.19864 11.9973 9.48222C11.7822 9.40297 11.6372 9.34778 11.4907 9.29683C8.97094 8.41587 6.1915 9.56501 5.0275 11.9694C3.86916 14.3625 4.67087 17.2424 6.91891 18.6534C8.65252 19.7417 10.4696 19.8174 12.2577 18.8374C14.0649 17.8467 14.9742 16.249 14.9926 14.1899ZM8.1197 15.7968C7.2996 14.9109 7.33285 13.4334 8.19046 12.6317C9.09902 11.7833 10.5538 11.8272 11.3796 12.728C12.2132 13.6372 12.1657 15.0461 11.2742 15.8725C10.3748 16.7068 8.9299 16.6721 8.11899 15.7961L8.1197 15.7968Z" fill="#EB6087"/>
      </svg>
      <img class="wordmark" src="data:image/png;base64,` + loginPageWordmarkPNG + `" alt="Cloud Intelligence™">
    </div>
    <div class="main">
    <svg class="check" viewBox="0 0 64 64" aria-hidden="true">
      <circle class="ring" cx="32" cy="32" r="29"/>
      <path class="tick" d="M20 33.5 L28.5 42 L44.5 24.5"/>
    </svg>
    <h1>You&rsquo;re logged in</h1>
    <p class="sub">Return to the terminal &mdash; you can close this&nbsp;tab.</p>
    </div>
$TRYNEXT
    <p class="help">New here? Start with the <a href="https://help.doit.com/docs/cli/cheatsheet">CLI cheatsheet</a>.</p>
  </div>
  <p class="foot">Cloud Intelligence&trade;</p>
</div>
</body>
</html>
`

// loginErrorHTML is served when the OAuth redirect carries an `error` param;
// $ERROR and $DETAILS are replaced with the HTML-escaped query params.
const loginErrorHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>dci — login failed</title>
<style>
  :root {
    --ink: #050A2E;
    --ink-soft: rgba(5, 10, 46, 0.62);
    --pink: #EB6087;
    --err: #C93A54;
    --card: #FFFFFF;
    --ground: #FBFAF7;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    font-family: Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    background: var(--ground);
    color: var(--ink);
    display: grid;
    place-items: center;
    -webkit-font-smoothing: antialiased;
  }
  .bg {
    position: fixed; inset: 0; z-index: 0;
    background:
      repeating-linear-gradient(90deg,
        rgba(255,255,255,0) 0,
        rgba(255,255,255,0) calc(100%/12 - 1.5px),
        rgba(255,255,255,0.6) calc(100%/12 - 1.5px),
        rgba(255,255,255,0.6) calc(100%/12)),
      repeating-linear-gradient(90deg,
        rgba(255,255,255,0.28) 0,
        rgba(255,255,255,0.28) calc(100%/12),
        rgba(255,255,255,0) calc(100%/12),
        rgba(255,255,255,0) calc(100%/4)),
      repeating-linear-gradient(90deg,
        rgba(255,255,255,0) 0,
        rgba(255,255,255,0) calc(100%/12 * 3),
        rgba(255,255,255,0.22) calc(100%/12 * 3),
        rgba(255,255,255,0.22) calc(100%/12 * 4),
        rgba(255,255,255,0) calc(100%/12 * 4),
        rgba(255,255,255,0) calc(100%/12 * 5)),
      radial-gradient(50% 55% at 6% -8%, rgba(124,102,215,0.34), transparent 68%),
      radial-gradient(38% 42% at 34% 0%, rgba(151,131,226,0.20), transparent 65%),
      radial-gradient(52% 60% at 70% -10%, rgba(124,102,215,0.30), transparent 70%),
      radial-gradient(36% 40% at 97% 18%, rgba(151,131,226,0.22), transparent 68%),
      radial-gradient(30% 34% at 52% 22%, rgba(151,131,226,0.10), transparent 68%),
      linear-gradient(180deg, #EFEBF9 0%, #F8F6F3 48%, #FCFBF9 100%);
  }
  .card {
    position: relative; z-index: 1;
    width: min(420px, calc(100vw - 48px));
    background: var(--card);
    border: 1px solid rgba(5, 10, 46, 0.07);
    border-radius: 16px;
    box-shadow: 0 24px 48px -24px rgba(5, 10, 46, 0.18), 0 2px 8px rgba(5, 10, 46, 0.05);
    padding: 40px 40px 32px;
    text-align: center;
    min-height: 505px;
    display: flex;
    flex-direction: column;
    animation: rise 480ms cubic-bezier(0.2, 0.9, 0.3, 1) both;
  }
  .lockup {
    display: flex; align-items: center; justify-content: center; gap: 9px;
    margin-bottom: 36px;
  }
  .lockup .mark { width: 30px; height: 30px; display: block; }
  .lockup .wordmark { height: 17px; display: block; }
  .main {
    flex: 1;
    display: flex;
    flex-direction: column;
    justify-content: center;
  }
  .badge { width: 64px; height: 64px; margin: 0 auto 20px; display: block; }
  .badge .ring { fill: none; stroke: var(--err); stroke-width: 2.5; opacity: 0.25; }
  .badge .x {
    fill: none; stroke: var(--err); stroke-width: 5;
    stroke-linecap: round;
    stroke-dasharray: 26; stroke-dashoffset: 26;
    animation: draw 320ms 240ms cubic-bezier(0.65, 0, 0.35, 1) forwards;
  }
  .badge .x2 { animation-delay: 420ms; }
  h1 {
    font-size: 22px; font-weight: 600; letter-spacing: -0.01em;
    margin-bottom: 14px;
  }
  .errbox {
    background: rgba(201, 58, 84, 0.06);
    border: 1px solid rgba(201, 58, 84, 0.18);
    border-radius: 10px;
    padding: 14px 16px;
    margin-bottom: 24px;
    text-align: left;
  }
  .errname {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
    font-size: 13px; font-weight: 600;
    color: var(--err);
    margin-bottom: 4px;
    overflow-wrap: anywhere;
  }
  .errdetail {
    font-size: 13.5px; line-height: 1.55; color: var(--ink-soft);
    overflow-wrap: anywhere;
  }
  .errdetail:empty { display: none; }
  .sub {
    font-size: 14.5px; line-height: 1.55; color: var(--ink-soft);
    margin-bottom: 28px;
  }
  .term {
    display: flex; align-items: center; gap: 8px;
    background: #0B1030;
    border-radius: 10px;
    padding: 13px 16px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
    font-size: 13px;
    text-align: left;
    color: rgba(255,255,255,0.88);
  }
  .term .prompt { color: var(--pink); user-select: none; }
  .term .cursor {
    display: inline-block; width: 8px; height: 16px;
    background: rgba(255,255,255,0.75);
    animation: blink 1.1s steps(1) infinite;
    vertical-align: text-bottom;
  }
  .help {
    margin-top: 16px;
    font-size: 12.5px;
    color: rgba(5, 10, 46, 0.45);
  }
  .help a {
    color: rgba(5, 10, 46, 0.72);
    font-weight: 500;
    text-decoration: none;
    border-bottom: 1px solid rgba(5, 10, 46, 0.2);
    padding-bottom: 1px;
  }
  .help a:hover, .help a:focus-visible { color: #EB6087; border-bottom-color: #EB6087; }
  .help .sep { margin: 0 6px; color: rgba(5, 10, 46, 0.25); }
  .foot {
    position: relative; z-index: 1;
    margin-top: 20px;
    font-size: 12px; color: rgba(5, 10, 46, 0.45);
    text-align: center;
    animation: rise 480ms 90ms cubic-bezier(0.2, 0.9, 0.3, 1) both;
  }
  .wrap { position: relative; z-index: 1; }
  @keyframes rise {
    from { opacity: 0; transform: translateY(10px); }
    to   { opacity: 1; transform: none; }
  }
  @keyframes draw { to { stroke-dashoffset: 0; } }
  @keyframes blink { 50% { opacity: 0; } }
  @media (prefers-reduced-motion: reduce) {
    .card, .foot { animation: none; }
    .badge .x { animation: none; stroke-dashoffset: 0; }
    .term .cursor { animation: none; }
  }
</style>
</head>
<body>
<div class="bg" aria-hidden="true"></div>
<div class="wrap">
  <div class="card">
    <div class="lockup">
      <svg class="mark" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
        <path d="M19.0769 10.2046C19.656 10.795 19.6383 11.7361 19.038 12.323C18.4476 12.9 17.4761 12.8901 16.9112 12.3011C16.3476 11.7142 16.366 10.7257 16.9501 10.1685C17.5496 9.59653 18.4964 9.61279 19.0769 10.2053V10.2046Z" fill="#EB6087"/>
        <path d="M14.9926 14.1899C15.0174 11.3828 15.0032 8.57579 14.9997 5.76875C14.9983 4.80288 14.3657 4.12571 13.4897 4.125C12.6179 4.125 11.9888 4.80995 11.9881 5.77158C11.9874 6.98511 11.9938 8.19864 11.9973 9.48222C11.7822 9.40297 11.6372 9.34778 11.4907 9.29683C8.97094 8.41587 6.1915 9.56501 5.0275 11.9694C3.86916 14.3625 4.67087 17.2424 6.91891 18.6534C8.65252 19.7417 10.4696 19.8174 12.2577 18.8374C14.0649 17.8467 14.9742 16.249 14.9926 14.1899ZM8.1197 15.7968C7.2996 14.9109 7.33285 13.4334 8.19046 12.6317C9.09902 11.7833 10.5538 11.8272 11.3796 12.728C12.2132 13.6372 12.1657 15.0461 11.2742 15.8725C10.3748 16.7068 8.9299 16.6721 8.11899 15.7961L8.1197 15.7968Z" fill="#EB6087"/>
      </svg>
      <img class="wordmark" src="data:image/png;base64,` + loginPageWordmarkPNG + `" alt="Cloud Intelligence™">
    </div>
    <div class="main">
    <svg class="badge" viewBox="0 0 64 64" aria-hidden="true">
      <circle class="ring" cx="32" cy="32" r="29"/>
      <path class="x" d="M23 23 L41 41"/>
      <path class="x x2" d="M41 23 L23 41"/>
    </svg>
    <h1>Login didn&rsquo;t complete</h1>
    <div class="errbox">
      <div class="errname">$ERROR</div>
      <div class="errdetail">$DETAILS</div>
    </div>
    <p class="sub">Return to the terminal and try again.</p>
    </div>
    <div class="term" aria-hidden="true">
      <span class="prompt">$</span>
      <span>dci login&nbsp;<span class="cursor"></span></span>
    </div>
    <p class="help">Need a hand?
      <a href="https://help.doit.com/docs/cli#authentication">Authentication docs</a><span class="sep">&middot;</span><a href="https://help.doit.com/docs/cli/cheatsheet">CLI cheatsheet</a>
    </p>
  </div>
  <p class="foot">Cloud Intelligence&trade;</p>
</div>
</body>
</html>
`

// loginRunningHTML is served when the user clicks the run chip; the suggested
// command starts in the terminal that ran dci login.
const loginRunningHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>dci — running</title>
<style>
  :root {
    --ink: #050A2E;
    --ink-soft: rgba(5, 10, 46, 0.62);
    --pink: #EB6087;
    --ok: #0BA05F;
    --card: #FFFFFF;
    --ground: #FBFAF7;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    font-family: Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    background: var(--ground);
    color: var(--ink);
    display: grid;
    place-items: center;
    -webkit-font-smoothing: antialiased;
  }
  .bg {
    position: fixed; inset: 0; z-index: 0;
    background:
      repeating-linear-gradient(90deg,
        rgba(255,255,255,0) 0,
        rgba(255,255,255,0) calc(100%/12 - 1.5px),
        rgba(255,255,255,0.6) calc(100%/12 - 1.5px),
        rgba(255,255,255,0.6) calc(100%/12)),
      repeating-linear-gradient(90deg,
        rgba(255,255,255,0.28) 0,
        rgba(255,255,255,0.28) calc(100%/12),
        rgba(255,255,255,0) calc(100%/12),
        rgba(255,255,255,0) calc(100%/4)),
      repeating-linear-gradient(90deg,
        rgba(255,255,255,0) 0,
        rgba(255,255,255,0) calc(100%/12 * 3),
        rgba(255,255,255,0.22) calc(100%/12 * 3),
        rgba(255,255,255,0.22) calc(100%/12 * 4),
        rgba(255,255,255,0) calc(100%/12 * 4),
        rgba(255,255,255,0) calc(100%/12 * 5)),
      radial-gradient(50% 55% at 6% -8%, rgba(124,102,215,0.34), transparent 68%),
      radial-gradient(38% 42% at 34% 0%, rgba(151,131,226,0.20), transparent 65%),
      radial-gradient(52% 60% at 70% -10%, rgba(124,102,215,0.30), transparent 70%),
      radial-gradient(36% 40% at 97% 18%, rgba(151,131,226,0.22), transparent 68%),
      radial-gradient(30% 34% at 52% 22%, rgba(151,131,226,0.10), transparent 68%),
      linear-gradient(180deg, #EFEBF9 0%, #F8F6F3 48%, #FCFBF9 100%);
  }
  .card {
    position: relative; z-index: 1;
    width: min(420px, calc(100vw - 48px));
    background: var(--card);
    border: 1px solid rgba(5, 10, 46, 0.07);
    border-radius: 16px;
    box-shadow: 0 24px 48px -24px rgba(5, 10, 46, 0.18), 0 2px 8px rgba(5, 10, 46, 0.05);
    padding: 40px 40px 32px;
    text-align: center;
    min-height: 505px;
    display: flex;
    flex-direction: column;
    animation: rise 480ms cubic-bezier(0.2, 0.9, 0.3, 1) both;
  }
  .lockup {
    display: flex; align-items: center; justify-content: center; gap: 9px;
    margin-bottom: 36px;
  }
  .lockup .mark { width: 30px; height: 30px; display: block; }
  .lockup .wordmark { height: 17px; display: block; }
  .main {
    flex: 1;
    display: flex;
    flex-direction: column;
    justify-content: center;
  }
  .check {
    width: 64px; height: 64px; margin: 0 auto 20px; display: block;
  }
  .check .ring {
    fill: none; stroke: var(--ok); stroke-width: 2.5; opacity: 0.25;
  }
  .check .tick {
    fill: none; stroke: var(--ok); stroke-width: 5;
    stroke-linecap: round; stroke-linejoin: round;
    stroke-dasharray: 48; stroke-dashoffset: 48;
    animation: draw 420ms 240ms cubic-bezier(0.65, 0, 0.35, 1) forwards;
  }
  h1 {
    font-size: 22px; font-weight: 600; letter-spacing: -0.01em;
    margin-bottom: 10px;
  }
  .sub {
    font-size: 14.5px; line-height: 1.55; color: var(--ink-soft);
    margin-bottom: 28px;
  }
  .trylabel {
    font-size: 11px; font-weight: 600; letter-spacing: 0.08em; text-transform: uppercase;
    color: rgba(5, 10, 46, 0.4);
    text-align: left;
    margin-bottom: 8px;
  }
  .term {
    display: flex; align-items: center; gap: 8px;
    background: #0B1030;
    border-radius: 10px;
    padding: 13px 16px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
    font-size: 13px;
    text-align: left;
    color: rgba(255,255,255,0.88);
  }
  .term .prompt { color: var(--pink); user-select: none; }
  .term .cursor {
    display: inline-block; width: 8px; height: 16px;
    background: rgba(255,255,255,0.75);
    animation: blink 1.1s steps(1) infinite;
    vertical-align: text-bottom;
  }
  a.term {
    text-decoration: none;
    cursor: pointer;
    transition: box-shadow 160ms ease, transform 160ms ease;
  }
  a.term:hover, a.term:focus-visible {
    box-shadow: 0 8px 22px -8px rgba(11, 16, 48, 0.55);
    transform: translateY(-1px);
  }
  .runhint {
    margin-top: 8px;
    font-size: 12px;
    color: rgba(5, 10, 46, 0.45);
  }
  .help {
    margin-top: 16px;
    font-size: 12.5px;
    color: rgba(5, 10, 46, 0.45);
  }
  .help a {
    color: rgba(5, 10, 46, 0.72);
    font-weight: 500;
    text-decoration: none;
    border-bottom: 1px solid rgba(5, 10, 46, 0.2);
    padding-bottom: 1px;
  }
  .help a:hover, .help a:focus-visible { color: #EB6087; border-bottom-color: #EB6087; }
  .foot {
    position: relative; z-index: 1;
    margin-top: 20px;
    font-size: 12px; color: rgba(5, 10, 46, 0.45);
    text-align: center;
    animation: rise 480ms 90ms cubic-bezier(0.2, 0.9, 0.3, 1) both;
  }
  .wrap { position: relative; z-index: 1; }
  @keyframes rise {
    from { opacity: 0; transform: translateY(10px); }
    to   { opacity: 1; transform: none; }
  }
  @keyframes draw { to { stroke-dashoffset: 0; } }
  @keyframes blink { 50% { opacity: 0; } }
  @media (prefers-reduced-motion: reduce) {
    .card, .foot { animation: none; }
    .check .tick { animation: none; stroke-dashoffset: 0; }
    .term .cursor { animation: none; }
  }
</style>
</head>
<body>
<div class="bg" aria-hidden="true"></div>
<div class="wrap">
  <div class="card">
    <div class="lockup">
      <svg class="mark" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
        <path d="M19.0769 10.2046C19.656 10.795 19.6383 11.7361 19.038 12.323C18.4476 12.9 17.4761 12.8901 16.9112 12.3011C16.3476 11.7142 16.366 10.7257 16.9501 10.1685C17.5496 9.59653 18.4964 9.61279 19.0769 10.2053V10.2046Z" fill="#EB6087"/>
        <path d="M14.9926 14.1899C15.0174 11.3828 15.0032 8.57579 14.9997 5.76875C14.9983 4.80288 14.3657 4.12571 13.4897 4.125C12.6179 4.125 11.9888 4.80995 11.9881 5.77158C11.9874 6.98511 11.9938 8.19864 11.9973 9.48222C11.7822 9.40297 11.6372 9.34778 11.4907 9.29683C8.97094 8.41587 6.1915 9.56501 5.0275 11.9694C3.86916 14.3625 4.67087 17.2424 6.91891 18.6534C8.65252 19.7417 10.4696 19.8174 12.2577 18.8374C14.0649 17.8467 14.9742 16.249 14.9926 14.1899ZM8.1197 15.7968C7.2996 14.9109 7.33285 13.4334 8.19046 12.6317C9.09902 11.7833 10.5538 11.8272 11.3796 12.728C12.2132 13.6372 12.1657 15.0461 11.2742 15.8725C10.3748 16.7068 8.9299 16.6721 8.11899 15.7961L8.1197 15.7968Z" fill="#EB6087"/>
      </svg>
      <img class="wordmark" src="data:image/png;base64,` + loginPageWordmarkPNG + `" alt="Cloud Intelligence™">
    </div>
    <div class="main">
    <svg class="check" viewBox="0 0 64 64" aria-hidden="true">
      <circle class="ring" cx="32" cy="32" r="29"/>
      <path class="tick" d="M20 33.5 L28.5 42 L44.5 24.5"/>
    </svg>
    <h1>Running in your terminal</h1>
    <p class="sub">Output is on its way &mdash; switch back to the&nbsp;terminal.</p>
    </div>
    <div class="term" aria-hidden="true">
      <span class="prompt">$</span>
      <span>dci get-report --chart</span>
    </div>
  </div>
  <p class="foot">Cloud Intelligence&trade;</p>
</div>
</body>
</html>
`
