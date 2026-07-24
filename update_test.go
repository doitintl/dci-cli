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
	got := updateNotice("1.4.2", "v1.5.0", "brew upgrade dci")
	want := "A new version of dci is available: 1.4.2 → 1.5.0\nRun `brew upgrade dci` to update.\n"
	if got != want {
		t.Errorf("updateNotice = %q, want %q", got, want)
	}

	got = updateNotice("1.4.2", "v1.5.0", "Download the latest release from https://github.com/doitintl/dci-cli/releases/latest")
	want = "A new version of dci is available: 1.4.2 → 1.5.0\nDownload the latest release from https://github.com/doitintl/dci-cli/releases/latest\n"
	if got != want {
		t.Errorf("updateNotice(download hint) = %q, want %q", got, want)
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

	// A failed fetch must leave the previous cache intact.
	refreshUpdateCache(dir, srv.URL+"/fail", now.Add(time.Hour))
	c, ok = readUpdateCache(dir)
	if !ok || c.LatestVersion != "v1.5.0" || !c.CheckedAt.Equal(now) {
		t.Errorf("after failed refresh: cache = %+v, ok = %v; want previous cache untouched", c, ok)
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

func TestUpgradeInstruction(t *testing.T) {
	releasesHint := "Download the latest release from https://github.com/doitintl/dci-cli/releases/latest"
	cases := []struct {
		name    string
		exePath string
		goos    string
		want    string
	}{
		{"brew apple silicon", "/opt/homebrew/Cellar/dci/1.4.2/bin/dci", "darwin", "brew upgrade dci"},
		{"brew intel mac", "/usr/local/Cellar/dci/1.4.2/bin/dci", "darwin", "brew upgrade dci"},
		{"brew opt symlink", "/opt/homebrew/opt/dci/bin/dci", "darwin", "brew upgrade dci"},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/Cellar/dci/1.4.2/bin/dci", "linux", "brew upgrade dci"},
		{"scoop", `C:\Users\pat\scoop\apps\dci\current\dci.exe`, "windows", "scoop update dci"},
		{"scoop shim", `C:\Users\pat\scoop\shims\dci.exe`, "windows", "scoop update dci"},
		{"winget", `C:\Users\pat\AppData\Local\Microsoft\WinGet\Packages\DoiT.dci__abc\dci.exe`, "windows", "winget upgrade DoiT.dci"},
		{"deb-rpm usr bin", "/usr/bin/dci", "linux", releasesHint},
		{"manual install", "/Users/pat/bin/dci", "darwin", releasesHint},
		{"windows manual", `C:\tools\dci.exe`, "windows", releasesHint},
		{"empty path", "", "darwin", releasesHint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := upgradeInstruction(tc.exePath, tc.goos); got != tc.want {
				t.Errorf("upgradeInstruction(%q, %q) = %q, want %q", tc.exePath, tc.goos, got, tc.want)
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
