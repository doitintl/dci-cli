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
			[]string{"$ERROR", "$DETAILS"},
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
		{"success", loginSuccessHTML},
		{"error", loginErrorHTML},
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
