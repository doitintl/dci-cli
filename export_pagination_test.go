package main

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

// exportPage is one canned file-shaped response: a body plus the
// continuation token the API would have put in X-Next-Page-Token.
type exportPage struct {
	body  string
	token string
}

// scriptedExportTransport replays exportPages in order, recording the
// requests so a test can assert on the page tokens and time windows the
// merge loop asked for.
type scriptedExportTransport struct {
	pages       []exportPage
	contentType string
	requests    []*http.Request
	// endlessToken makes every response carry the same token, to exercise the
	// request cap.
	endlessToken string
}

func (s *scriptedExportTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, request.Clone(request.Context()))
	contentType := s.contentType
	if contentType == "" {
		contentType = "text/csv"
	}
	page := exportPage{body: "a\n", token: s.endlessToken}
	if s.endlessToken == "" {
		index := len(s.requests) - 1
		if index >= len(s.pages) {
			index = len(s.pages) - 1
		}
		page = s.pages[index]
	}
	header := http.Header{"Content-Type": []string{contentType}}
	if page.token != "" {
		header.Set(nextPageTokenHeader, page.token)
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(page.body)),
		ContentLength: int64(len(page.body)),
	}, nil
}

func exportGetRequest(t *testing.T, query string) *http.Request {
	t.Helper()
	url := "https://api.doit.com/datahub/v1/datasets/demo/records"
	if query != "" {
		url += "?" + query
	}
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func asExportCommand(t *testing.T) {
	t.Helper()
	previous := invokedCommandName
	invokedCommandName = "export-datahub-dataset-records"
	// The file-export paths gate on the user having typed --all, not on the
	// all-pages key --search also sets.
	viper.Set("all-pages-explicit", true)
	t.Cleanup(func() {
		invokedCommandName = previous
		viper.Set("all-pages-explicit", false)
	})
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestMergeCSVExportPagesUnionsHeaders(t *testing.T) {
	// Page 2 introduces label.team and drops nothing; page 3 reorders the
	// columns and adds metric.usage. The export computes business columns per
	// page, so this is the documented behavior, not a hypothetical.
	pages := [][]byte{
		[]byte("event_id,usage_date,metric.cost\ne1,2026-01-01T00:00:00Z,1\n"),
		[]byte("event_id,usage_date,metric.cost,label.team\ne2,2026-01-02T00:00:00Z,2,growth\n"),
		[]byte("usage_date,event_id,metric.usage\n2026-01-03T00:00:00Z,e3,42\n"),
	}
	merged, rows, err := mergeCSVExportPages(pages)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 3 {
		t.Errorf("rows = %d, want 3", rows)
	}
	want := "event_id,usage_date,metric.cost,label.team,metric.usage\n" +
		"e1,2026-01-01T00:00:00Z,1,,\n" +
		"e2,2026-01-02T00:00:00Z,2,growth,\n" +
		"e3,2026-01-03T00:00:00Z,,,42\n"
	if string(merged) != want {
		t.Errorf("merged =\n%s\nwant\n%s", merged, want)
	}
}

func TestMergeCSVExportPagesPreservesQuotedCells(t *testing.T) {
	pages := [][]byte{
		[]byte("event_id,fixed.project\ne1,\"Acme, Inc\"\n"),
		[]byte("event_id,fixed.project\ne2,plain\n"),
	}
	merged, _, err := mergeCSVExportPages(pages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "\"Acme, Inc\"") {
		t.Errorf("merged =\n%s\nwant the embedded comma still quoted", merged)
	}
}

func TestMergeCSVExportPagesRejectsBrokenCSV(t *testing.T) {
	pages := [][]byte{
		[]byte("event_id\ne1\n"),
		[]byte("event_id\n\"unterminated,x\n"),
	}
	if _, _, err := mergeCSVExportPages(pages); err == nil {
		t.Error("mergeCSVExportPages accepted unparsable CSV; want an error rather than a silently mangled export")
	}
}

func TestMergeLineDelimitedPages(t *testing.T) {
	// The middle page has no trailing newline: without normalization its last
	// record would glue onto the next page's first one.
	pages := [][]byte{
		[]byte("{\"id\":1}\n"),
		[]byte("{\"id\":2}"),
		[]byte("{\"id\":3}\n"),
	}
	merged, rows, err := mergeLineDelimitedPages(pages)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 3 {
		t.Errorf("rows = %d, want 3", rows)
	}
	if string(merged) != "{\"id\":1}\n{\"id\":2}\n{\"id\":3}\n" {
		t.Errorf("merged = %q, want one record per line", merged)
	}
}

func TestMergeFileExportBodiesLeavesSinglePageUntouched(t *testing.T) {
	// Byte identity matters: --all on a one-page export must equal the same
	// command without it, down to the quoting the API chose.
	page := []byte("event_id,fixed.project\ne1,\"Acme, Inc\"\n")
	merged, rows, err := mergeFileExportBodies("text/csv", [][]byte{page})
	if err != nil {
		t.Fatal(err)
	}
	if string(merged) != string(page) {
		t.Errorf("merged = %q, want the page verbatim", merged)
	}
	if rows != -1 {
		t.Errorf("rows = %d, want -1 (not counted for a single page)", rows)
	}
}

func TestPaginatingTransportFollowsExportHeaderToken(t *testing.T) {
	activateAllPages(t, 50000)
	asExportCommand(t)
	stderr := capturePaginationStderr(t)
	scripted := &scriptedExportTransport{pages: []exportPage{
		{body: "event_id,metric.cost\ne1,1\n", token: "t2"},
		{body: "event_id,metric.cost\ne2,2\n", token: "t3"},
		{body: "event_id,metric.cost\ne3,3\n"},
	}}
	transport := paginatingTransport{next: scripted}

	response, err := transport.RoundTrip(exportGetRequest(t, "startTime=2026-01-01T00:00:00Z&endTime=2026-02-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, response)
	if got := strings.Count(body, "\n"); got != 4 {
		t.Errorf("merged body has %d lines, want 4 (header + 3 rows):\n%s", got, body)
	}
	if response.Header.Get(nextPageTokenHeader) != "" {
		t.Errorf("merged response still advertises %s = %q", nextPageTokenHeader, response.Header.Get(nextPageTokenHeader))
	}
	if response.Header.Get(rowCountHeader) != "3" {
		t.Errorf("%s = %q, want 3", rowCountHeader, response.Header.Get(rowCountHeader))
	}
	if len(scripted.requests) != 3 {
		t.Fatalf("made %d requests, want 3", len(scripted.requests))
	}
	if got := scripted.requests[1].URL.Query().Get("pageToken"); got != "t2" {
		t.Errorf("second request pageToken = %q, want t2", got)
	}
	if got := scripted.requests[2].URL.Query().Get("pageToken"); got != "t3" {
		t.Errorf("third request pageToken = %q, want t3", got)
	}
	if !strings.Contains(stderr.String(), "merged 3 pages") {
		t.Errorf("stderr = %q, want a note naming the pages merged", stderr.String())
	}
}

func TestPaginatingTransportWalksExportWindows(t *testing.T) {
	activateAllPages(t, 50000)
	asExportCommand(t)
	stderr := capturePaginationStderr(t)
	scripted := &scriptedExportTransport{pages: []exportPage{
		{body: "event_id\ne1\n"},
		{body: "event_id\ne2\n"},
		{body: "event_id\ne3\n"},
	}}
	transport := paginatingTransport{next: scripted}

	// 2025-01-01 to 2026-09-05 is 612 days: three windows of at most 366.
	response, err := transport.RoundTrip(exportGetRequest(t, "startTime=2025-01-01T00:00:00Z&endTime=2026-09-05T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != 2 {
		t.Fatalf("made %d requests, want 2 windows", len(scripted.requests))
	}
	first := scripted.requests[0].URL.Query()
	if first.Get("startTime") != "2025-01-01T00:00:00Z" {
		t.Errorf("first window start = %q, want the requested start", first.Get("startTime"))
	}
	if first.Get("endTime") != "2026-01-02T00:00:00Z" {
		t.Errorf("first window end = %q, want the start plus 366 days", first.Get("endTime"))
	}
	second := scripted.requests[1].URL.Query()
	if second.Get("startTime") != first.Get("endTime") {
		t.Errorf("second window start = %q, want the first window's end (windows abut exactly)", second.Get("startTime"))
	}
	if second.Get("endTime") != "2026-09-05T00:00:00Z" {
		t.Errorf("second window end = %q, want the requested end", second.Get("endTime"))
	}
	if second.Get("pageToken") != "" {
		t.Errorf("second window carried pageToken %q; a new window starts from the beginning", second.Get("pageToken"))
	}
	if rows := response.Header.Get(rowCountHeader); rows != "2" {
		t.Errorf("%s = %q, want 2", rowCountHeader, rows)
	}
	if !strings.Contains(stderr.String(), "2 time windows") {
		t.Errorf("stderr = %q, want a note naming the windows", stderr.String())
	}
}

func TestPaginatingTransportPagesWithinEachExportWindow(t *testing.T) {
	activateAllPages(t, 50000)
	asExportCommand(t)
	capturePaginationStderr(t)
	// Window 1 pages once, then window 2 pages once: the token page of
	// window 2 must carry window 2's bounds, not the original request's.
	scripted := &scriptedExportTransport{pages: []exportPage{
		{body: "event_id\ne1\n", token: "w1p2"},
		{body: "event_id\ne2\n"},
		{body: "event_id\ne3\n", token: "w2p2"},
		{body: "event_id\ne4\n"},
	}}
	transport := paginatingTransport{next: scripted}

	if _, err := transport.RoundTrip(exportGetRequest(t, "startTime=2025-01-01T00:00:00Z&endTime=2026-09-05T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != 4 {
		t.Fatalf("made %d requests, want 4", len(scripted.requests))
	}
	fourth := scripted.requests[3].URL.Query()
	if fourth.Get("pageToken") != "w2p2" {
		t.Errorf("fourth request pageToken = %q, want w2p2", fourth.Get("pageToken"))
	}
	if fourth.Get("startTime") != "2026-01-02T00:00:00Z" || fourth.Get("endTime") != "2026-09-05T00:00:00Z" {
		t.Errorf("fourth request window = %q..%q, want the second window's bounds",
			fourth.Get("startTime"), fourth.Get("endTime"))
	}
}

func TestPaginatingTransportExportRequestCapKeepsResumeToken(t *testing.T) {
	activateAllPages(t, 50000)
	asExportCommand(t)
	stderr := capturePaginationStderr(t)
	scripted := &scriptedExportTransport{endlessToken: "forever"}
	transport := paginatingTransport{next: scripted}

	response, err := transport.RoundTrip(exportGetRequest(t, "startTime=2026-01-01T00:00:00Z&endTime=2026-02-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != maxFileExportRequests {
		t.Errorf("made %d requests, want the cap of %d", len(scripted.requests), maxFileExportRequests)
	}
	if response.Header.Get(nextPageTokenHeader) != "forever" {
		t.Errorf("%s = %q, want the resume token kept", nextPageTokenHeader, response.Header.Get(nextPageTokenHeader))
	}
	note := stderr.String()
	if !strings.Contains(note, "stopped after") || !strings.Contains(note, "--page-token forever") {
		t.Errorf("stderr = %q, want a note naming the resume command", note)
	}
}

func TestPaginatingTransportLeavesExportsAloneWithoutAllFlag(t *testing.T) {
	viper.Set("all-pages", false)
	asExportCommand(t)
	scripted := &scriptedExportTransport{pages: []exportPage{
		{body: "event_id\ne1\n", token: "t2"},
		{body: "event_id\ne2\n"},
	}}
	transport := paginatingTransport{next: scripted}

	response, err := transport.RoundTrip(exportGetRequest(t, "startTime=2026-01-01T00:00:00Z&endTime=2026-02-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != 1 {
		t.Errorf("made %d requests without --all, want 1", len(scripted.requests))
	}
	if response.Header.Get(nextPageTokenHeader) != "t2" {
		t.Error("the continuation token was stripped without --all; the stderr note needs it")
	}
}

func TestPaginatingTransportIgnoresExportsOnOtherHosts(t *testing.T) {
	activateAllPages(t, 50000)
	asExportCommand(t)
	scripted := &scriptedExportTransport{pages: []exportPage{
		{body: "event_id\ne1\n", token: "t2"},
		{body: "event_id\ne2\n"},
	}}
	transport := paginatingTransport{next: scripted}
	request, err := http.NewRequest(http.MethodGet, "https://example.com/records?startTime=2026-01-01T00:00:00Z&endTime=2026-02-01T00:00:00Z", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != 1 {
		t.Errorf("made %d requests against another host, want 1", len(scripted.requests))
	}
}

func TestClampExportWindow(t *testing.T) {
	activateAllPages(t, 50000)
	asExportCommand(t)

	t.Run("over-long window is clamped and walked", func(t *testing.T) {
		request, walk := clampExportWindow(exportGetRequest(t, "startTime=2025-01-01T00:00:00Z&endTime=2026-09-05T00:00:00Z"))
		if walk == nil {
			t.Fatal("walk = nil, want a window walk")
		}
		if got := request.URL.Query().Get("endTime"); got != "2026-01-02T00:00:00Z" {
			t.Errorf("clamped endTime = %q, want start + 366 days", got)
		}
		if !walk.requestedEnd.Equal(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("requestedEnd = %v, want the user's end", walk.requestedEnd)
		}
	})

	t.Run("window inside the cap is untouched", func(t *testing.T) {
		request, walk := clampExportWindow(exportGetRequest(t, "startTime=2026-01-01T00:00:00Z&endTime=2026-02-01T00:00:00Z"))
		if walk != nil {
			t.Error("walk != nil for a window inside the cap")
		}
		if got := request.URL.Query().Get("endTime"); got != "2026-02-01T00:00:00Z" {
			t.Errorf("endTime = %q, want it untouched", got)
		}
	})

	t.Run("unparsable bounds are left to the API", func(t *testing.T) {
		_, walk := clampExportWindow(exportGetRequest(t, "startTime=yesterday&endTime=today"))
		if walk != nil {
			t.Error("walk != nil for unparsable bounds; the API owns that error")
		}
	})

	t.Run("other commands are untouched", func(t *testing.T) {
		previous := invokedCommandName
		invokedCommandName = "list-dimensions"
		t.Cleanup(func() { invokedCommandName = previous })
		_, walk := clampExportWindow(exportGetRequest(t, "startTime=2020-01-01T00:00:00Z&endTime=2026-09-05T00:00:00Z"))
		if walk != nil {
			t.Error("walk != nil for a command with no window cap")
		}
	})
}

func TestClampExportWindowOnlyUnderAllFlag(t *testing.T) {
	viper.Set("all-pages", false)
	asExportCommand(t)
	request, walk := clampExportWindow(exportGetRequest(t, "startTime=2025-01-01T00:00:00Z&endTime=2026-09-05T00:00:00Z"))
	if walk != nil {
		t.Error("walk != nil without --all; a silently narrowed export is the worst answer")
	}
	if got := request.URL.Query().Get("endTime"); got != "2026-09-05T00:00:00Z" {
		t.Errorf("endTime = %q, want it untouched so the API rules on it", got)
	}
}

func TestExportWindowWalkCoversRangeExactly(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	spec := fileExportOperations["export-datahub-dataset-records"]
	walk := &exportWindowWalk{spec: spec, requestedEnd: end, currentEnd: start.Add(spec.maxWindow), windows: 1}

	previousEnd := walk.currentEnd
	for {
		windowStart, windowEnd, more := walk.next()
		if !more {
			break
		}
		if !windowStart.Equal(previousEnd) {
			t.Fatalf("window start %v does not abut the previous end %v", windowStart, previousEnd)
		}
		if windowEnd.Sub(windowStart) > spec.maxWindow {
			t.Fatalf("window %v..%v exceeds the cap", windowStart, windowEnd)
		}
		previousEnd = windowEnd
	}
	if !previousEnd.Equal(end) {
		t.Errorf("last window ends at %v, want the requested end %v", previousEnd, end)
	}
	if walk.windows != 3 {
		t.Errorf("windows = %d, want 3 for a 882-day range", walk.windows)
	}
}

func TestValidateExportWindow(t *testing.T) {
	command := "export-datahub-dataset-records"
	overLong := []string{"demo", "--start-time", "2025-01-01T00:00:00Z", "--end-time", "2026-09-05T00:00:00Z"}

	err := validateExportWindow(command, overLong)
	if err == nil {
		t.Fatal("validateExportWindow accepted a 612-day window without --all")
	}
	preflight, ok := err.(invocationPreflightError)
	if !ok {
		t.Fatalf("err = %T, want invocationPreflightError", err)
	}
	if !strings.Contains(preflight.detail.Hint, "--all") {
		t.Errorf("hint = %q, want it to point at --all", preflight.detail.Hint)
	}
	if preflight.exitCode != exitUsage {
		t.Errorf("exitCode = %d, want %d", preflight.exitCode, exitUsage)
	}

	if err := validateExportWindow(command, append(overLong, "--all")); err != nil {
		t.Errorf("with --all: %v, want nil (the CLI walks the windows)", err)
	}
	if err := validateExportWindow(command, []string{"demo", "--start-time=2026-01-01T00:00:00Z", "--end-time=2026-02-01T00:00:00Z"}); err != nil {
		t.Errorf("window inside the cap: %v, want nil", err)
	}
	if err := validateExportWindow(command, []string{"demo", "--start-time", "nonsense", "--end-time", "2026-02-01T00:00:00Z"}); err != nil {
		t.Errorf("unparsable bounds: %v, want nil (the API reports those)", err)
	}
	if err := validateExportWindow(command, []string{"demo"}); err != nil {
		t.Errorf("no window flags: %v, want nil", err)
	}
	if err := validateExportWindow("list-dimensions", overLong); err != nil {
		t.Errorf("command without a window cap: %v, want nil", err)
	}
}

func TestValidateReimportFlag(t *testing.T) {
	command := "export-datahub-dataset-records"
	if err := validateReimportFlag(command, []string{"demo", "--for-reimport"}); err != nil {
		t.Errorf("CSV export with --for-reimport: %v, want nil", err)
	}
	if err := validateReimportFlag(command, []string{"demo"}); err != nil {
		t.Errorf("without the flag: %v, want nil", err)
	}
	if err := validateReimportFlag(command, []string{"demo", "--for-reimport", "--format", "jsonl"}); err == nil {
		t.Error("--for-reimport with --format jsonl was accepted; the NDJSON has no columns to rewrite")
	}
	if err := validateReimportFlag(command, []string{"demo", "--for-reimport", "--format=csv"}); err != nil {
		t.Errorf("--format=csv with --for-reimport: %v, want nil", err)
	}
	if err := validateReimportFlag("list-datahub-datasets", []string{"--for-reimport"}); err == nil {
		t.Error("--for-reimport on a command with no CSV export was accepted")
	}
}

func TestValidatePagingFlagsSupported(t *testing.T) {
	paged := &cli.Operation{
		Name:        "list-dimensions",
		QueryParams: []*cli.Param{{Name: "pageToken"}, {Name: "maxResults"}},
	}
	unpaged := &cli.Operation{Name: "list-datahub-datasets"}

	if err := validatePagingFlagsSupported(paged, []string{"--page-token", "abc", "--max-results", "10"}); err != nil {
		t.Errorf("paged operation: %v, want nil", err)
	}
	for _, flag := range [][]string{{"--page-token", "abc"}, {"--max-results", "10"}, {"--max-results=10"}} {
		err := validatePagingFlagsSupported(unpaged, flag)
		if err == nil {
			t.Errorf("%v on an unpaged operation was accepted; the value would be dropped", flag)
			continue
		}
		if preflight, ok := err.(invocationPreflightError); !ok || preflight.exitCode != exitUsage {
			t.Errorf("%v: err = %v, want a usage preflight error", flag, err)
		}
	}
	if err := validatePagingFlagsSupported(nil, []string{"--page-token", "abc"}); err != nil {
		t.Errorf("nil operation: %v, want nil", err)
	}
}

func TestNoteAllPagesIneffective(t *testing.T) {
	unpaged := &cli.Operation{Name: "list-datahub-datasets"}
	paged := &cli.Operation{Name: "list-dimensions", QueryParams: []*cli.Param{{Name: "pageToken"}}}

	stderr := capturePaginationStderr(t)
	if err := validatePagingFlagsSupported(unpaged, []string{"--all"}); err != nil {
		t.Fatalf("--all on an unpaged operation returned %v, want a note and nil", err)
	}
	if !strings.Contains(stderr.String(), "no effect on list-datahub-datasets") {
		t.Errorf("stderr = %q, want a note that --all had nothing to follow", stderr.String())
	}

	stderr = capturePaginationStderr(t)
	if err := validatePagingFlagsSupported(paged, []string{"--all"}); err != nil {
		t.Fatal(err)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want no note for a paged operation", stderr.String())
	}
}

func TestFileExportOperationsMatchPagingCaps(t *testing.T) {
	// --all sizes its pages from pagingCaps (boostPageSize); a file export
	// missing from that map would fetch default-sized pages and make many
	// more requests than it needs.
	for name := range fileExportOperations {
		if _, ok := pagingCaps[name]; !ok {
			t.Errorf("%s is a file export with no pagingCaps entry", name)
		}
	}
}

func TestPreflightRejectsPagingFlagsOnUnpagedOperation(t *testing.T) {
	// get-datahub-dataset declares only a path parameter, so a page size has
	// nowhere to go: the request would succeed and quietly ignore it.
	api := cli.API{Operations: []cli.Operation{{Name: "get-datahub-dataset"}}}
	configureInvocationPreflightTest(t, api, true, false, false)

	err := preflightAPIInvocation([]string{"dci", "dci", "get-datahub-dataset", "demo", "--max-results", "10"})
	if err == nil || !strings.Contains(err.Error(), "does not page") {
		t.Fatalf("error = %v, want a rejection explaining the flag would be ignored", err)
	}
	if err := preflightAPIInvocation([]string{"dci", "dci", "get-datahub-dataset", "demo"}); err != nil {
		t.Fatalf("plain invocation rejected: %v", err)
	}
}

func TestPreflightRejectsOverLongExportWindow(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{
		Name:        "export-datahub-dataset-records",
		QueryParams: []*cli.Param{{Name: "pageToken"}, {Name: "maxResults"}, {Name: "startTime"}, {Name: "endTime"}},
	}}}
	configureInvocationPreflightTest(t, api, true, false, false)

	args := []string{"dci", "dci", "export-datahub-dataset-records", "demo",
		"--start-time", "2025-01-01T00:00:00Z", "--end-time", "2026-09-05T00:00:00Z"}
	err := preflightAPIInvocation(args)
	if err == nil || !strings.Contains(err.Error(), "366 days") {
		t.Fatalf("error = %v, want the window cap named", err)
	}
	if err := preflightAPIInvocation(append(args, "--all")); err != nil {
		t.Fatalf("--all invocation rejected: %v", err)
	}
}

func TestFileExportIgnoresSearchImpliedAllPages(t *testing.T) {
	// --search sets all-pages (pagination.go) but can neither filter nor even
	// reach a file body: the walk must not fire, or a bare --search would
	// silently issue up to 200 requests.
	activateAllPages(t, 50000)
	asExportCommand(t)
	viper.Set("all-pages-explicit", false)
	viper.Set("list-search", "foo")
	t.Cleanup(func() { viper.Set("list-search", "") })
	capturePaginationStderr(t)
	scripted := &scriptedExportTransport{pages: []exportPage{
		{body: "event_id\ne1\n", token: "t2"},
		{body: "event_id\ne2\n"},
	}}
	transport := paginatingTransport{next: scripted}

	response, err := transport.RoundTrip(exportGetRequest(t, "startTime=2025-01-01T00:00:00Z&endTime=2026-09-05T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != 1 {
		t.Errorf("made %d requests for a --search-implied --all, want 1", len(scripted.requests))
	}
	if got := scripted.requests[0].URL.Query().Get("endTime"); got != "2026-09-05T00:00:00Z" {
		t.Errorf("endTime = %q, want the window untouched (no clamp without an explicit --all)", got)
	}
	if response.Header.Get(nextPageTokenHeader) != "t2" {
		t.Error("the continuation token was stripped; the stderr note needs it")
	}
}

func TestValidateSearchOnFileExport(t *testing.T) {
	err := validateSearchOnFileExport("export-datahub-dataset-records", []string{"demo", "--search", "foo"})
	if err == nil {
		t.Fatal("--search on a file export was accepted; it matches nothing there")
	}
	if preflight, ok := err.(invocationPreflightError); !ok || preflight.exitCode != exitUsage {
		t.Errorf("err = %v, want a usage preflight error", err)
	}
	if err := validateSearchOnFileExport("export-datahub-dataset-records", []string{"demo"}); err != nil {
		t.Errorf("without --search: %v, want nil", err)
	}
	if err := validateSearchOnFileExport("list-dimensions", []string{"--search", "genai"}); err != nil {
		t.Errorf("--search on a list command: %v, want nil", err)
	}
}

func TestFileExportRequestCapDoesNotConsumeAnUnfetchedWindow(t *testing.T) {
	// The cap trips exactly at a window rollover: the last in-window page
	// returns no token, so the loop would advance the window next. The
	// unfetched window must stay unconsumed (its rows would be lost) and the
	// resume note must name it instead of a dangling empty --page-token.
	activateAllPages(t, 50000)
	asExportCommand(t)
	stderr := capturePaginationStderr(t)

	pages := make([]exportPage, maxFileExportRequests)
	for index := range pages {
		pages[index] = exportPage{body: "event_id\ne" + strconv.Itoa(index) + "\n"}
		if index < maxFileExportRequests-1 {
			pages[index].token = "t" + strconv.Itoa(index+1)
		}
	}
	scripted := &scriptedExportTransport{pages: pages}
	transport := paginatingTransport{next: scripted}

	// 2025-01-01 to 2027-06-01 is 882 days: three windows, and every request
	// is spent paging inside the first one.
	response, err := transport.RoundTrip(exportGetRequest(t, "startTime=2025-01-01T00:00:00Z&endTime=2027-06-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scripted.requests) != maxFileExportRequests {
		t.Fatalf("made %d requests, want the cap of %d", len(scripted.requests), maxFileExportRequests)
	}
	// The second window's bounds must not have been consumed by a request
	// that was never sent.
	if got := scripted.requests[len(scripted.requests)-1].URL.Query().Get("endTime"); got != "2026-01-02T00:00:00Z" {
		t.Errorf("last request endTime = %q, want the first window still", got)
	}
	note := stderr.String()
	if strings.Contains(note, "--page-token") {
		t.Errorf("note = %q, want no --page-token at a window boundary (there is none)", note)
	}
	if !strings.Contains(note, "--start-time 2026-01-02T00:00:00Z") || !strings.Contains(note, "--end-time 2027-06-01T00:00:00Z") {
		t.Errorf("note = %q, want the unfetched range as the resume window", note)
	}
	if !strings.Contains(note, "incomplete") {
		t.Errorf("note = %q, want it to say the export is incomplete", note)
	}
	if response.Header.Get(nextPageTokenHeader) != "" {
		t.Errorf("%s = %q, want empty at a window boundary", nextPageTokenHeader, response.Header.Get(nextPageTokenHeader))
	}
}
