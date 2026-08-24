package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

func TestValidateMaxResults(t *testing.T) {
	cases := []struct {
		name    string
		command string
		args    []string
		wantErr bool
		wantIn  []string
	}{
		{"over spec cap", "list-tickets", []string{"list-tickets", "--max-results", "101"}, true, []string{"101", "100", "list-tickets", "spec"}},
		{"over verified cap", "list-dimensions", []string{"list-dimensions", "--max-results", "501"}, true, []string{"501", "500", "verified against the live API"}},
		{"equals form", "list-dimensions", []string{"list-dimensions", "--max-results=1000"}, true, []string{"1000", "500"}},
		{"at cap", "list-dimensions", []string{"list-dimensions", "--max-results", "500"}, false, nil},
		{"under cap", "list-tickets", []string{"list-tickets", "--max-results", "50"}, false, nil},
		{"flag absent", "list-dimensions", []string{"list-dimensions"}, false, nil},
		{"unknown command", "list-reports", []string{"list-reports", "--max-results", "9999"}, false, nil},
		{"non-numeric left to pflag", "list-dimensions", []string{"list-dimensions", "--max-results", "many"}, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMaxResults(tc.command, tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil {
				return
			}
			preflight, ok := err.(invocationPreflightError)
			if !ok {
				t.Fatalf("error type = %T, want invocationPreflightError", err)
			}
			if preflight.ExitCode() != exitUsage {
				t.Errorf("exit code = %d, want %d", preflight.ExitCode(), exitUsage)
			}
			if preflight.StructuredError().Code != "USAGE_ERROR" {
				t.Errorf("code = %q, want USAGE_ERROR", preflight.StructuredError().Code)
			}
			for _, fragment := range tc.wantIn {
				if !strings.Contains(preflight.StructuredError().Message, fragment) {
					t.Errorf("message %q missing %q", preflight.StructuredError().Message, fragment)
				}
			}
		})
	}
}

func TestPreflightAPIInvocationRejectsOverCapMaxResults(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "list-dimensions"}}}
	configureInvocationPreflightTest(t, api, true, false, false)

	err := preflightAPIInvocation([]string{"dci", "dci", "list-dimensions", "--max-results", "501"})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want over-cap rejection naming the cap", err)
	}

	if err := preflightAPIInvocation([]string{"dci", "dci", "list-dimensions", "--max-results", "500"}); err != nil {
		t.Fatalf("at-cap invocation rejected: %v", err)
	}
}

func TestCollectionPageToken(t *testing.T) {
	if got := collectionPageToken(map[string]interface{}{"pageToken": "abc"}); got != "abc" {
		t.Errorf("pageToken = %q", got)
	}
	if got := collectionPageToken(map[string]interface{}{"nextPageToken": "n"}); got != "n" {
		t.Errorf("nextPageToken = %q", got)
	}
	if got := collectionPageToken(map[string]interface{}{"pageToken": "  "}); got != "" {
		t.Errorf("blank token = %q, want empty", got)
	}
	if got := collectionPageToken([]interface{}{"not", "a", "map"}); got != "" {
		t.Errorf("non-map = %q, want empty", got)
	}
}

func capturePaginationStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	oldStderr := cli.Stderr
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() { cli.Stderr = oldStderr })
	return &stderr
}

func TestNotePageTokenDropped(t *testing.T) {
	listBody := func(token string) map[string]interface{} {
		return map[string]interface{}{
			"rowCount":  float64(50),
			"pageToken": token,
			"dimensions": []interface{}{
				map[string]interface{}{"id": "zone", "label": "Zone"},
			},
		}
	}

	stderr := capturePaginationStderr(t)
	notePageTokenDropped(listBody("c2t1"))
	note := stderr.String()
	if !strings.Contains(note, "c2t1") || !strings.Contains(note, "--page-token") || !strings.Contains(note, "50") {
		t.Errorf("note = %q, want token, flag, and row count", note)
	}

	// No token: silent.
	stderr = capturePaginationStderr(t)
	notePageTokenDropped(map[string]interface{}{
		"rowCount":   float64(1),
		"dimensions": []interface{}{map[string]interface{}{"id": "zone"}},
	})
	if stderr.String() != "" {
		t.Errorf("tokenless body produced note %q", stderr.String())
	}

	// Token but not list-shaped (no collection array): silent.
	stderr = capturePaginationStderr(t)
	notePageTokenDropped(map[string]interface{}{"pageToken": "x", "id": "r1"})
	if stderr.String() != "" {
		t.Errorf("non-list body produced note %q", stderr.String())
	}
}

// scriptedTransport returns canned JSON responses in order and records the
// requests it saw.
type scriptedTransport struct {
	responses []string
	requests  []*http.Request
}

func (s *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, req)
	index := len(s.requests) - 1
	if index >= len(s.responses) {
		index = len(s.responses) - 1
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(s.responses[index])),
		ContentLength: int64(len(s.responses[index])),
	}, nil
}

func activateAllPages(t *testing.T, boost int) {
	t.Helper()
	viper.Set("all-pages", true)
	viper.Set("all-pages-boost", boost)
	t.Cleanup(func() {
		viper.Set("all-pages", false)
		viper.Set("all-pages-boost", 0)
	})
}

func apiGetRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://api.doit.com/analytics/v1/dimensions", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestPaginatingTransportMergesPages(t *testing.T) {
	activateAllPages(t, 0)
	scripted := &scriptedTransport{responses: []string{
		`{"dimensions":[{"id":"a"}],"rowCount":1,"pageToken":"t2"}`,
		`{"dimensions":[{"id":"b"}],"rowCount":1,"pageToken":"t3"}`,
		`{"dimensions":[{"id":"c"}],"rowCount":1}`,
	}}
	transport := paginatingTransport{next: scripted}

	response, err := transport.RoundTrip(apiGetRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	var merged map[string]interface{}
	if err := json.Unmarshal(body, &merged); err != nil {
		t.Fatalf("merged body is not JSON: %v\n%s", err, body)
	}
	if rows := merged["dimensions"].([]interface{}); len(rows) != 3 {
		t.Errorf("merged rows = %d, want 3", len(rows))
	}
	if merged["rowCount"] != float64(3) {
		t.Errorf("rowCount = %v, want 3", merged["rowCount"])
	}
	if merged["pagesFetched"] != float64(3) {
		t.Errorf("pagesFetched = %v, want 3", merged["pagesFetched"])
	}
	if _, hasToken := merged["pageToken"]; hasToken {
		t.Error("merged body still carries pageToken")
	}
	if len(scripted.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(scripted.requests))
	}
	if got := scripted.requests[1].URL.Query().Get("pageToken"); got != "t2" {
		t.Errorf("second request pageToken = %q, want t2", got)
	}
	if got := scripted.requests[2].URL.Query().Get("pageToken"); got != "t3" {
		t.Errorf("third request pageToken = %q, want t3", got)
	}
}

func TestPaginatingTransportBoostsEveryPageSize(t *testing.T) {
	activateAllPages(t, 500)
	scripted := &scriptedTransport{responses: []string{
		`{"dimensions":[{"id":"a"}],"rowCount":1,"pageToken":"t2"}`,
		`{"dimensions":[{"id":"b"}],"rowCount":1}`,
	}}
	transport := paginatingTransport{next: scripted}

	if _, err := transport.RoundTrip(apiGetRequest(t)); err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(scripted.requests))
	}
	for index, request := range scripted.requests {
		if got := request.URL.Query().Get("maxResults"); got != "500" {
			t.Errorf("request %d maxResults = %q, want boosted 500 on every page", index+1, got)
		}
	}
}

func TestPaginatingTransportInertWithoutAllFlag(t *testing.T) {
	viper.Set("all-pages", false)
	scripted := &scriptedTransport{responses: []string{
		`{"dimensions":[{"id":"a"}],"rowCount":1,"pageToken":"t2"}`,
	}}
	transport := paginatingTransport{next: scripted}

	response, err := transport.RoundTrip(apiGetRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != 1 {
		t.Fatalf("requests = %d, want passthrough single request", len(scripted.requests))
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), `"pageToken":"t2"`) {
		t.Errorf("body = %s, want untouched", body)
	}
}

func TestPaginatingTransportIgnoresOtherHosts(t *testing.T) {
	activateAllPages(t, 0)
	scripted := &scriptedTransport{responses: []string{
		`{"items":[{"id":"a"}],"rowCount":1,"pageToken":"t2"}`,
	}}
	transport := paginatingTransport{next: scripted}

	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != 1 {
		t.Fatalf("requests = %d, want 1 (foreign host untouched)", len(scripted.requests))
	}
}

func TestPaginatingTransportPageCapKeepsResumeToken(t *testing.T) {
	activateAllPages(t, 0)
	stderr := capturePaginationStderr(t)
	// Every page returns a token: the loop must stop at the cap.
	pages := make([]string, allPagesMaxPages+5)
	for index := range pages {
		pages[index] = fmt.Sprintf(`{"dimensions":[{"id":"d%d"}],"rowCount":1,"pageToken":"t%d"}`, index, index+1)
	}
	scripted := &scriptedTransport{responses: pages}
	transport := paginatingTransport{next: scripted}

	response, err := transport.RoundTrip(apiGetRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != allPagesMaxPages {
		t.Fatalf("requests = %d, want capped at %d", len(scripted.requests), allPagesMaxPages)
	}
	body, _ := io.ReadAll(response.Body)
	var merged map[string]interface{}
	if err := json.Unmarshal(body, &merged); err != nil {
		t.Fatal(err)
	}
	if merged["pageToken"] == nil || merged["pageToken"] == "" {
		t.Error("cap-hit merge lost the resume token")
	}
	if !strings.Contains(stderr.String(), "--page-token") {
		t.Errorf("cap-hit note = %q, want resume guidance", stderr.String())
	}
}

func TestValidateAllPagesFlags(t *testing.T) {
	if err := validateAllPagesFlags([]string{"list-dimensions", "--all"}); err != nil {
		t.Errorf("--all alone rejected: %v", err)
	}
	if err := validateAllPagesFlags([]string{"list-dimensions"}); err != nil {
		t.Errorf("no flags rejected: %v", err)
	}
	err := validateAllPagesFlags([]string{"list-dimensions", "--all", "--page-token", "x"})
	if err == nil || !strings.Contains(err.Error(), "--page-token") {
		t.Errorf("err = %v, want page-token conflict", err)
	}
	err = validateAllPagesFlags([]string{"list-dimensions", "--all", "--max-results=10"})
	if err == nil || !strings.Contains(err.Error(), "--max-results") {
		t.Errorf("err = %v, want max-results conflict", err)
	}
}

func TestValidateSearchFlags(t *testing.T) {
	if err := validateSearchFlags([]string{"list-dimensions", "--search", "genai"}); err != nil {
		t.Errorf("--search alone rejected: %v", err)
	}
	if err := validateSearchFlags([]string{"list-dimensions", "--search", "genai", "--all"}); err != nil {
		t.Errorf("--search with --all rejected: %v", err)
	}
	for _, conflicting := range []string{"--page-token", "--max-results"} {
		err := validateSearchFlags([]string{"list-dimensions", "--search", "genai", conflicting, "x"})
		if err == nil || !strings.Contains(err.Error(), conflicting) {
			t.Errorf("err = %v, want %s conflict", err, conflicting)
		}
	}
}

func TestTableAndCSVMarshalNoteDroppedPageToken(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	body := map[string]interface{}{
		"rowCount":  float64(1),
		"pageToken": "resume-me",
		"items": []interface{}{
			map[string]interface{}{"id": "a", "name": "Alpha"},
		},
	}

	stderr := capturePaginationStderr(t)
	if _, err := (dciCSVContentType{}).Marshal(body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "resume-me") {
		t.Errorf("csv note = %q, want continuation token", stderr.String())
	}

	stderr = capturePaginationStderr(t)
	if _, err := (dciTableContentType{}).Marshal(body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "resume-me") {
		t.Errorf("table note = %q, want continuation token", stderr.String())
	}
}
