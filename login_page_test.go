package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginCallbackServeHTTP(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		wantCode string
		wantIn   []string
		wantOut  []string
	}{
		{
			"code present serves success page and delivers code",
			"?code=abc123&expires_in=90",
			"abc123",
			[]string{"You&rsquo;re logged in", "Cloud Intelligence", "dci get-report --chart"},
			[]string{"$ERROR", "$DETAILS", "$TRYNEXT", "/run?t="},
		},
		{
			"no params delivers empty code with success page",
			"",
			"",
			[]string{"You&rsquo;re logged in"},
			nil,
		},
		{
			"error param serves error page with description",
			"?error=access_denied&error_description=user+said+no",
			"",
			[]string{"access_denied", "user said no", "Login didn&rsquo;t complete", "help.doit.com/docs/cli#authentication", "help.doit.com/docs/cli/cheatsheet"},
			[]string{"$ERROR", "$DETAILS", "You&rsquo;re logged in"},
		},
		{
			"error values are HTML-escaped",
			"?error=%3Cb%3Ex%3C%2Fb%3E&error_description=%3Cscript%3Ealert(1)%3C%2Fscript%3E",
			"",
			[]string{"&lt;b&gt;x&lt;/b&gt;", "&lt;script&gt;alert(1)&lt;/script&gt;"},
			[]string{"<b>x</b>", "<script>alert(1)</script>"},
		},
		{
			"error without description leaves details empty",
			"?error=server_error",
			"",
			[]string{"server_error"},
			[]string{"$DETAILS"},
		},
		{
			"placeholder-shaped error value cannot hijack the details slot",
			"?error=%24DETAILS&error_description=oops",
			"",
			[]string{`<div class="errdetail">oops</div>`},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := make(chan string, 1)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/"+tc.query, nil)

			loginCallbackHandler{c: c}.ServeHTTP(rec, req)

			if ct := rec.Header().Get("Content-Type"); ct != "text/html" {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
			select {
			case code := <-c:
				if code != tc.wantCode {
					t.Errorf("code on channel = %q, want %q", code, tc.wantCode)
				}
			default:
				t.Fatal("no code delivered on channel")
			}
			body := rec.Body.String()
			for _, want := range tc.wantIn {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q", want)
				}
			}
			for _, unwanted := range tc.wantOut {
				if strings.Contains(body, unwanted) {
					t.Errorf("body must not contain %q", unwanted)
				}
			}
		})
	}
}

func TestLoginPagesAreSelfContained(t *testing.T) {
	cases := []struct {
		name string
		page string
	}{
		{"success static", renderLoginSuccessPage()},
		{"error", loginErrorHTML},
		{"running", loginRunningHTML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The callback server has a 5s WriteTimeout; keep pages small.
			if len(tc.page) > 64*1024 {
				t.Errorf("page is %d bytes, want <= 64KB", len(tc.page))
			}
			if strings.Contains(tc.page, "<script") {
				t.Error("page must not contain scripts")
			}
			// No fetched resources: every src must be an inline data URI, and
			// https:// may appear only in anchors (help links) and xmlns attrs.
			for _, chunk := range strings.Split(tc.page, `src="`)[1:] {
				if !strings.HasPrefix(chunk, "data:") {
					t.Errorf("non-inline resource: src=%q...", chunk[:min(40, len(chunk))])
				}
			}
			if strings.Contains(tc.page, "@import") || strings.Contains(tc.page, "url(") {
				t.Error("CSS must not load external resources")
			}
		})
	}
}

// armTestOffer arms a run offer for one test and restores the nil state
// after. It bypasses armLoginRunOffer's TTY/agent gate, which would disarm
// under CI.
func armTestOffer(t *testing.T) *runOffer {
	t.Helper()
	loginRunOffer = newRunOffer()
	t.Cleanup(func() { loginRunOffer = nil })
	if loginRunOffer == nil {
		t.Fatal("newRunOffer returned nil")
	}
	return loginRunOffer
}

func TestRenderLoginSuccessPage(t *testing.T) {
	t.Run("static chip without an offer", func(t *testing.T) {
		page := renderLoginSuccessPage()
		if !strings.Contains(page, "dci get-report --chart") {
			t.Error("static chip missing the suggestion")
		}
		for _, unwanted := range []string{"$TRYNEXT", "$RUNTOKEN", "/run?t="} {
			if strings.Contains(page, unwanted) {
				t.Errorf("page must not contain %q without an offer", unwanted)
			}
		}
	})
	t.Run("linked chip embeds the offer token", func(t *testing.T) {
		offer := armTestOffer(t)
		page := renderLoginSuccessPage()
		if !strings.Contains(page, `href="/run?t=`+offer.token+`"`) {
			t.Error("run link with token missing")
		}
		if !strings.Contains(page, "click to run") {
			t.Error("explicit click-to-run affordance missing")
		}
		for _, unwanted := range []string{"$TRYNEXT", "$RUNTOKEN"} {
			if strings.Contains(page, unwanted) {
				t.Errorf("placeholder %q leaked", unwanted)
			}
		}
	})
}

func TestServeRun(t *testing.T) {
	get := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/run"+query, nil)
		loginCallbackHandler{c: make(chan string, 1)}.ServeHTTP(rec, req)
		return rec
	}

	t.Run("404 without an offer", func(t *testing.T) {
		if rec := get("?t=whatever"); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
	t.Run("404 on wrong token", func(t *testing.T) {
		offer := armTestOffer(t)
		close(offer.exchangeDone)
		if rec := get("?t=not-the-token"); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
		select {
		case <-offer.clicked:
			t.Error("wrong token must not signal a click")
		default:
		}
	})
	t.Run("404 when the exchange never completes", func(t *testing.T) {
		offer := armTestOffer(t)
		oldWait := runExchangeWait
		runExchangeWait = 20 * time.Millisecond
		t.Cleanup(func() { runExchangeWait = oldWait })
		if rec := get("?t=" + offer.token); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
	t.Run("valid click serves running page and signals once", func(t *testing.T) {
		offer := armTestOffer(t)
		close(offer.exchangeDone)
		for i := 0; i < 2; i++ { // repeat click stays idempotent
			rec := get("?t=" + offer.token)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "Running in your terminal") {
				t.Error("running page not served")
			}
		}
		select {
		case <-offer.clicked:
		default:
			t.Error("click not signaled")
		}
	})
}

func TestMaybeWaitForRunClick(t *testing.T) {
	runCommand := func(t *testing.T) *bool {
		t.Helper()
		ran := false
		oldRun := runSuggestedCommand
		runSuggestedCommand = func() { ran = true }
		t.Cleanup(func() { runSuggestedCommand = oldRun })
		oldGrace := runClickGraceWindow
		runClickGraceWindow = 20 * time.Millisecond
		t.Cleanup(func() { runClickGraceWindow = oldGrace })
		return &ran
	}

	t.Run("no offer returns immediately", func(t *testing.T) {
		ran := runCommand(t)
		maybeWaitForRunClick()
		if *ran {
			t.Error("must not run without an offer")
		}
	})
	t.Run("no exchange returns immediately", func(t *testing.T) {
		ran := runCommand(t)
		armTestOffer(t)
		maybeWaitForRunClick()
		if *ran {
			t.Error("must not run when the browser flow never completed")
		}
	})
	t.Run("click runs the suggestion", func(t *testing.T) {
		ran := runCommand(t)
		offer := armTestOffer(t)
		close(offer.exchangeDone)
		close(offer.clicked)
		maybeWaitForRunClick()
		if !*ran {
			t.Error("click must run the suggestion")
		}
	})
	t.Run("grace window expiry skips the run", func(t *testing.T) {
		ran := runCommand(t)
		offer := armTestOffer(t)
		close(offer.exchangeDone)
		maybeWaitForRunClick()
		if *ran {
			t.Error("timeout must not run the suggestion")
		}
	})
}

func TestLoginErrorPagePlaceholders(t *testing.T) {
	// ServeHTTP replaces each placeholder once; more than one occurrence
	// would leak a literal $ERROR/$DETAILS into the rendered page.
	for _, placeholder := range []string{"$ERROR", "$DETAILS"} {
		if got := strings.Count(loginErrorHTML, placeholder); got != 1 {
			t.Errorf("loginErrorHTML contains %d %s placeholders, want exactly 1", got, placeholder)
		}
	}
}

func TestAuthorizationCodeHandlerParameters(t *testing.T) {
	// The param names must keep matching what writeConfig stores in
	// apis.json, or existing configs would stop resolving.
	params := (&authorizationCodeHandler{}).Parameters()

	required := map[string]bool{}
	for _, p := range params {
		required[p.Name] = p.Required
	}
	cases := []struct {
		name         string
		wantRequired bool
	}{
		{"client_id", true},
		{"authorize_url", true},
		{"token_url", true},
		{"client_secret", false},
		{"scopes", false},
		{"redirect_url", false},
	}
	if len(params) != len(cases) {
		t.Errorf("Parameters() returned %d params, want %d", len(params), len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := required[tc.name]
			if !ok {
				t.Fatalf("param %q missing", tc.name)
			}
			if got != tc.wantRequired {
				t.Errorf("param %q required = %v, want %v", tc.name, got, tc.wantRequired)
			}
		})
	}
}

func TestAuthorizationCodeHandlerOnRequestSkipsAuthorized(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	req.Header.Set("Authorization", "Bearer already-set")

	err := (&authorizationCodeHandler{}).OnRequest(req, "dci:default", map[string]string{})
	if err != nil {
		t.Fatalf("OnRequest with Authorization set returned error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer already-set" {
		t.Errorf("Authorization header changed to %q", got)
	}
}

func TestAuthorizationCodeTokenSourceRedirectURL(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		want       string
	}{
		{"defaults to localhost:8484", "", "http://localhost:8484"},
		{"configured value wins", "http://localhost:9999", "http://localhost:9999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ac := &authorizationCodeTokenSource{RedirectURL: tc.configured}
			if got := ac.getRedirectURL(); got != tc.want {
				t.Errorf("getRedirectURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthorizationCodeTokenSourceFailsFastHeadless(t *testing.T) {
	// The interactive flow blocks on the browser callback with no timeout;
	// reached headless (agent-mode child, CI, or a --help description fetch)
	// it must return an error immediately instead of hanging the process.
	previousInteractive := invocationInteractive
	invocationInteractive = func() bool { return false }
	t.Cleanup(func() { invocationInteractive = previousInteractive })

	done := make(chan error, 1)
	go func() {
		_, err := (&authorizationCodeTokenSource{}).Token()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "DCI_API_KEY") {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Token() did not return headless")
	}
}

func TestRequestOAuthToken(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		response  string
		wantErr   string
		wantToken string
	}{
		{
			"parses token with expires_in",
			http.StatusOK,
			`{"token_type":"Bearer","access_token":"tok","refresh_token":"ref","expires_in":90}`,
			"",
			"tok",
		},
		{
			"error status surfaces response body",
			http.StatusUnauthorized,
			`{"error":"invalid_grant"}`,
			"invalid_grant",
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotContentType, gotBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotContentType = r.Header.Get("content-type")
				b := make([]byte, r.ContentLength)
				_, _ = r.Body.Read(b)
				gotBody = string(b)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()

			token, err := requestOAuthToken(server.URL, "grant_type=authorization_code&code=abc")

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotContentType != "application/x-www-form-urlencoded" {
				t.Errorf("content-type = %q, want form-encoded", gotContentType)
			}
			if !strings.Contains(gotBody, "code=abc") {
				t.Errorf("payload not forwarded, got %q", gotBody)
			}
			if token.AccessToken != tc.wantToken {
				t.Errorf("AccessToken = %q, want %q", token.AccessToken, tc.wantToken)
			}
			if token.RefreshToken != "ref" {
				t.Errorf("RefreshToken = %q, want %q", token.RefreshToken, "ref")
			}
			if remaining := time.Until(token.Expiry); remaining < 80*time.Second || remaining > 100*time.Second {
				t.Errorf("Expiry %v not ~90s out", token.Expiry)
			}
		})
	}
}
