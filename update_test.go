package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFetchLatestVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(`{"tag_name": "v1.5.0", "name": "v1.5.0"}`))
		case "/notfound":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message": "Not Found"}`))
		case "/badjson":
			w.Write([]byte(`<html>rate limited</html>`))
		case "/emptytag":
			w.Write([]byte(`{"tag_name": ""}`))
		}
	}))
	defer srv.Close()

	if got, err := fetchLatestVersion(srv.URL + "/ok"); err != nil || got != "v1.5.0" {
		t.Errorf("fetchLatestVersion(ok) = %q, %v; want \"v1.5.0\", nil", got, err)
	}
	if _, err := fetchLatestVersion(srv.URL + "/notfound"); err == nil {
		t.Error("fetchLatestVersion(404): err = nil, want error")
	}
	if _, err := fetchLatestVersion(srv.URL + "/badjson"); err == nil {
		t.Error("fetchLatestVersion(bad json): err = nil, want error")
	}
	if _, err := fetchLatestVersion(srv.URL + "/emptytag"); err == nil {
		t.Error("fetchLatestVersion(empty tag): err = nil, want error")
	}
}

func TestUpdateNotice(t *testing.T) {
	got := updateNotice("1.4.2", "v1.5.0", "dci update")
	want := "A new version of dci is available: 1.4.2 → 1.5.0\nRun `dci update` to update.\n"
	if got != want {
		t.Errorf("updateNotice = %q, want %q", got, want)
	}
}

func TestUpdateCheckSuppressed(t *testing.T) {
	cases := []struct {
		name      string
		agentMode bool
		stderrTTY bool
		optOut    string
		version   string
		want      bool
	}{
		{"interactive release build", false, true, "", "1.4.2", false},
		{"agent mode", true, true, "", "1.4.2", true},
		{"non-tty stderr", false, false, "", "1.4.2", true},
		{"opt-out set", false, true, "1", "1.4.2", true},
		{"opt-out true", false, true, "true", "1.4.2", true},
		{"opt-out explicitly off", false, true, "0", "1.4.2", false},
		{"opt-out garbage ignored", false, true, "banana", "1.4.2", false},
		{"dev build", false, true, "", "dev", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := updateCheckSuppressed(tc.agentMode, tc.stderrTTY, tc.optOut, tc.version)
			if got != tc.want {
				t.Errorf("updateCheckSuppressed(%v, %v, %q, %q) = %v, want %v",
					tc.agentMode, tc.stderrTTY, tc.optOut, tc.version, got, tc.want)
			}
		})
	}
}

func TestRefreshUpdateCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"tag_name": "v1.5.0"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	refreshUpdateCache(dir, srv.URL+"/ok", now)
	c, ok := readUpdateCache(dir)
	if !ok || c.LatestVersion != "v1.5.0" || !c.CheckedAt.Equal(now) {
		t.Errorf("after refresh: cache = %+v, ok = %v; want latest v1.5.0 at %v", c, ok, now)
	}

	// A failed fetch must keep the previous result and stamp the failure
	// time for the retry backoff.
	failedAt := now.Add(time.Hour)
	refreshUpdateCache(dir, srv.URL+"/fail", failedAt)
	c, ok = readUpdateCache(dir)
	if !ok || c.LatestVersion != "v1.5.0" || !c.CheckedAt.Equal(now) {
		t.Errorf("after failed refresh: cache = %+v, ok = %v; want previous result kept", c, ok)
	}
	if !c.FailedAt.Equal(failedAt) {
		t.Errorf("FailedAt = %v, want %v", c.FailedAt, failedAt)
	}
}

func TestUpdateRetryBackoff(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		failedAt time.Time
		want     bool
	}{
		{"never failed", time.Time{}, false},
		{"just failed", now, true},
		{"14m ago", now.Add(-14 * time.Minute), true},
		{"16m ago", now.Add(-16 * time.Minute), false},
		{"clock rolled back", now.Add(time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := updateCache{FailedAt: tc.failedAt}
			if got := c.retryBackoffActive(now); got != tc.want {
				t.Errorf("retryBackoffActive = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAcquireUpdateRefreshLock(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	release, ok := acquireUpdateRefreshLock(dir, now)
	if !ok {
		t.Fatal("first acquisition must succeed")
	}
	if _, ok := acquireUpdateRefreshLock(dir, now); ok {
		t.Fatal("a live lock must block a second refresher")
	}
	release()
	release2, ok := acquireUpdateRefreshLock(dir, now)
	if !ok {
		t.Fatal("acquisition after release must succeed")
	}
	release2()

	// A stale lock (crashed refresher) is stolen.
	lockPath := filepath.Join(dir, "update_check.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	release3, ok := acquireUpdateRefreshLock(dir, time.Now())
	if !ok {
		t.Fatal("a stale lock must be stolen")
	}
	release3()
}

func TestUpdateRefreshNeeded(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	if !updateRefreshNeeded(dir, now) {
		t.Error("absent cache must need a refresh")
	}
	if err := writeUpdateCache(dir, updateCache{LatestVersion: "v1.5.0", CheckedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if updateRefreshNeeded(dir, now) {
		t.Error("fresh cache must not need a refresh")
	}
	if err := writeUpdateCache(dir, updateCache{LatestVersion: "v1.5.0", CheckedAt: now.Add(-6 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if !updateRefreshNeeded(dir, now) {
		t.Error("stale cache must need a refresh")
	}
	if err := writeUpdateCache(dir, updateCache{LatestVersion: "v1.5.0", CheckedAt: now.Add(-6 * time.Hour), FailedAt: now.Add(-5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if updateRefreshNeeded(dir, now) {
		t.Error("retry backoff must suppress the refresh")
	}
}

func TestUpdateCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	in := updateCache{LatestVersion: "1.5.0", CheckedAt: now}
	if err := writeUpdateCache(dir, in); err != nil {
		t.Fatalf("writeUpdateCache: %v", err)
	}
	out, ok := readUpdateCache(dir)
	if !ok {
		t.Fatal("readUpdateCache: ok = false after write")
	}
	if out.LatestVersion != "1.5.0" || !out.CheckedAt.Equal(now) {
		t.Errorf("readUpdateCache = %+v, want %+v", out, in)
	}
}

func TestUpdateCacheMissing(t *testing.T) {
	if _, ok := readUpdateCache(t.TempDir()); ok {
		t.Error("readUpdateCache on empty dir: ok = true, want false")
	}
}

func TestUpdateCacheCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "update_check.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readUpdateCache(dir); ok {
		t.Error("readUpdateCache on corrupt file: ok = true, want false")
	}
}

func TestUpdateCacheStale(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		checkedAt time.Time
		want      bool
	}{
		{"just checked", now, false},
		{"4h59m old", now.Add(-5*time.Hour + time.Minute), false},
		{"5h01m old", now.Add(-5*time.Hour - time.Minute), true},
		{"zero time", time.Time{}, true},
		{"clock rolled back far", now.Add(48 * time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := updateCache{LatestVersion: "1.5.0", CheckedAt: tc.checkedAt}
			if got := c.stale(now); got != tc.want {
				t.Errorf("stale(%v vs now %v) = %v, want %v", tc.checkedAt, now, got, tc.want)
			}
		})
	}
}

func TestDetectInstallChannel(t *testing.T) {
	originalProbe := packageOwnershipProbe
	t.Cleanup(func() { packageOwnershipProbe = originalProbe })
	owner := "" // which tool claims the binary: "dpkg", "rpm", or ""
	packageOwnershipProbe = func(tool string, args ...string) bool { return tool == owner }

	cases := []struct {
		name    string
		exePath string
		goos    string
		owner   string
		want    installChannel
	}{
		{"brew apple silicon", "/opt/homebrew/Cellar/dci/1.4.2/bin/dci", "darwin", "", channelBrew},
		{"brew intel mac", "/usr/local/Cellar/dci/1.4.2/bin/dci", "darwin", "", channelBrew},
		{"brew opt symlink", "/opt/homebrew/opt/dci/bin/dci", "darwin", "", channelBrew},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/Cellar/dci/1.4.2/bin/dci", "linux", "", channelBrew},
		{"scoop", `C:\Users\pat\scoop\apps\dci\current\dci.exe`, "windows", "", channelScoop},
		{"scoop shim", `C:\Users\pat\scoop\shims\dci.exe`, "windows", "", channelScoop},
		{"winget", `C:\Users\pat\AppData\Local\Microsoft\WinGet\Packages\DoiT.dci__abc\dci.exe`, "windows", "", channelWinget},
		{"dpkg-owned", "/usr/bin/dci", "linux", "dpkg", channelDeb},
		{"rpm-owned", "/usr/bin/dci", "linux", "rpm", channelRPM},
		{"linux unowned", "/usr/local/bin/dci", "linux", "", channelSelf},
		{"manual install", "/Users/pat/bin/dci", "darwin", "", channelSelf},
		{"windows manual", `C:\tools\dci.exe`, "windows", "", channelSelf},
		{"empty path", "", "darwin", "", channelSelf},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner = tc.owner
			if got := detectInstallChannel(tc.exePath, tc.goos); got != tc.want {
				t.Errorf("detectInstallChannel(%q, %q) = %q, want %q", tc.exePath, tc.goos, got, tc.want)
			}
		})
	}
}

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"patch newer", "1.4.2", "1.4.3", true},
		{"minor newer", "1.4.2", "1.5.0", true},
		{"major newer", "1.4.2", "2.0.0", true},
		{"equal", "1.4.2", "1.4.2", false},
		{"older", "1.5.0", "1.4.2", false},
		{"v prefix on both", "v1.4.2", "v1.5.0", true},
		{"v prefix on latest only", "1.4.2", "v1.5.0", true},
		{"numeric not lexicographic", "1.9.0", "1.10.0", true},
		{"prerelease latest never newer", "1.4.2", "1.5.0-rc.1", false},
		{"garbage latest", "1.4.2", "latest", false},
		{"empty latest", "1.4.2", "", false},
		{"dev current never upgraded", "dev", "1.5.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNewerVersion(tc.current, tc.latest); got != tc.want {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}
