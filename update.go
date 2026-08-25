package main

// Update check: notifies about new releases and powers `dci upgrade`.
// Kept in a sibling file per the AGENTS.md chapter-split guidance.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// startUpdateCheck refreshes the release cache when it is stale, by
// re-execing the binary as a released background process (the same detached
// pattern the name cache uses, name_completion.go) — an in-process goroutine
// dies with fast commands, wasting the fetch. The debounce is the cache TTL
// plus a short retry backoff after a failed fetch, and the child holds an
// advisory lock so concurrent invocations spawn at most one refresher
// (UPDATE-SPEC §8).
func startUpdateCheck(configDir string) {
	if updateCheckSuppressed(agentMode, stderrIsTTY(), os.Getenv("DCI_NO_UPDATE_CHECK"), version) {
		return
	}
	if !updateRefreshNeeded(configDir, time.Now()) {
		return
	}
	spawnUpdateCacheRefresh()
}

// updateRefreshNeeded reports whether the release cache warrants a detached
// refresh: absent, or stale without an active retry backoff.
func updateRefreshNeeded(configDir string, now time.Time) bool {
	c, ok := readUpdateCache(configDir)
	if !ok {
		return true
	}
	return c.stale(now) && !c.retryBackoffActive(now)
}

// spawnUpdateCacheRefresh re-execs the binary as a released background
// process; a var so tests can observe the spawn without forking.
var spawnUpdateCacheRefresh = func() {
	command := exec.Command(os.Args[0], "__refresh-update-check")
	if err := command.Start(); err != nil {
		return
	}
	_ = command.Process.Release()
}

// maybeNotifyUpdate prints the new-version hint after command output, from
// whatever the cache holds — at most one run behind, since the detached
// refresh armed by startUpdateCheck lands for the next invocation.
func maybeNotifyUpdate(configDir string) {
	if updateStatusReported {
		// `dci update` itself reported the outcome — the footer would either
		// contradict a just-completed install or duplicate --check output.
		return
	}
	if updateCheckSuppressed(agentMode, stderrIsTTY(), os.Getenv("DCI_NO_UPDATE_CHECK"), version) {
		return
	}
	c, ok := readUpdateCache(configDir)
	if !ok || !isNewerVersion(version, c.LatestVersion) {
		return
	}
	fmt.Fprint(tuiStyledStderr(), "\n"+styleUpdateNotice(updateNotice(version, c.LatestVersion, "dci update")))
}

func stderrIsTTY() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// executablePath resolves symlinks so a Homebrew `bin/dci -> ../Cellar/...`
// link is recognized as a brew install.
func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// registerUpdateRefreshCommand adds the hidden command the detached refresh
// spawn runs: take the advisory lock, refresh the cache, exit. Silent on
// every failure — it runs detached and must never surface errors or OAuth.
func registerUpdateRefreshCommand(configDir string) {
	cli.Root.AddCommand(&cobra.Command{
		Use:    "__refresh-update-check",
		Short:  "Refresh the cached latest-release version used by the update notice",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			release, ok := acquireUpdateRefreshLock(configDir, time.Now())
			if !ok {
				return nil
			}
			defer release()
			refreshUpdateCache(configDir, latestReleaseAPIURL, time.Now())
			return nil
		},
	})
}

// updateRefreshLockStaleAfter bounds how long a crashed refresher can block
// later ones: well past the fetch's 5s timeout, short enough that a stale
// lock never survives to the next TTL window.
const updateRefreshLockStaleAfter = 2 * time.Minute

// acquireUpdateRefreshLock takes the advisory refresh lock (O_CREATE|O_EXCL).
// A lock older than the staleness ceiling belonged to a crashed refresher and
// is stolen; a younger one means another refresher is live, so back off.
func acquireUpdateRefreshLock(configDir string, now time.Time) (func(), bool) {
	path := filepath.Join(configDir, "update_check.lock")
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = file.Close()
			return func() { _ = os.Remove(path) }, true
		}
		info, statErr := os.Stat(path)
		if statErr != nil || now.Sub(info.ModTime()) <= updateRefreshLockStaleAfter {
			return nil, false
		}
		_ = os.Remove(path)
	}
	return nil, false
}

const updateCheckTTL = 5 * time.Hour

// latestReleaseAPIURL is the release lookup endpoint; a var so tests can
// point it at a local server.
var latestReleaseAPIURL = "https://api.github.com/repos/doitintl/dci-cli/releases/latest"

// fetchLatestVersion returns the tag name of the newest GitHub release
// (e.g. "v1.5.0"). The unauthenticated API rate limit (60/hour/IP) is far
// above what the 5-hour cache TTL can generate.
func fetchLatestVersion(url string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", buildUserAgent(agentUAMode))
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release lookup returned HTTP %d", resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return "", fmt.Errorf("release lookup returned an empty tag")
	}
	return release.TagName, nil
}

// updateCache is the persisted result of the last release lookup, stored in
// the config dir so the notification can print without a network call.
type updateCache struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
	// FailedAt is the time of the last failed fetch; it backs the retry
	// backoff so a GitHub outage doesn't spawn a refresher per invocation.
	FailedAt time.Time `json:"failed_at,omitempty"`
}

// stale reports whether the cache needs a refresh. A CheckedAt far in the
// future (clock rolled back since the last check) also counts as stale, so a
// bad timestamp can't suppress checks indefinitely.
func (c updateCache) stale(now time.Time) bool {
	d := now.Sub(c.CheckedAt)
	return d > updateCheckTTL || d < -updateCheckTTL
}

// updateRetryBackoff is how long a failed fetch suppresses new refresh
// spawns — long enough to ride out a rate limit, short next to the TTL.
const updateRetryBackoff = 15 * time.Minute

// retryBackoffActive reports whether the last fetch failed recently. A
// FailedAt in the future (clock rolled back) does not suppress refreshes.
func (c updateCache) retryBackoffActive(now time.Time) bool {
	if c.FailedAt.IsZero() {
		return false
	}
	d := now.Sub(c.FailedAt)
	return d >= 0 && d < updateRetryBackoff
}

// updateCheckSuppressed reports whether the passive update check and its
// notification should be skipped: agent transcripts and piped/CI stderr must
// stay clean, DCI_NO_UPDATE_CHECK is an explicit opt-out, and local dev
// builds have no meaningful version to compare. `dci upgrade` bypasses this —
// there the check is an explicit user action.
func updateCheckSuppressed(agentMode, stderrTTY bool, optOutEnv, currentVersion string) bool {
	if agentMode || !stderrTTY {
		return true
	}
	if on, ok := parseBoolish(optOutEnv); ok && on {
		return true
	}
	if _, ok := parseVersion(currentVersion); !ok {
		return true
	}
	return false
}

// refreshUpdateCache fetches the latest release tag and rewrites the cache.
// A failed fetch keeps the previous result and stamps the failure time for
// the retry backoff; the check simply retries on a later run.
func refreshUpdateCache(configDir, url string, now time.Time) {
	tag, err := fetchLatestVersion(url)
	if err != nil {
		c, _ := readUpdateCache(configDir)
		c.FailedAt = now
		_ = writeUpdateCache(configDir, c)
		return
	}
	_ = writeUpdateCache(configDir, updateCache{LatestVersion: tag, CheckedAt: now})
}

func updateCachePath(configDir string) string {
	return filepath.Join(configDir, "update_check.json")
}

func readUpdateCache(configDir string) (updateCache, bool) {
	data, err := os.ReadFile(updateCachePath(configDir))
	if err != nil {
		return updateCache{}, false
	}
	var c updateCache
	if err := json.Unmarshal(data, &c); err != nil {
		return updateCache{}, false
	}
	return c, true
}

// writeUpdateCache writes via temp file + rename so concurrent dci
// invocations can't interleave partial writes.
func writeUpdateCache(configDir string, c updateCache) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(configDir, "update_check-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), updateCachePath(configDir))
}

// updateNotice formats the new-version message around the runnable
// instruction — `dci update` in the passive notice, the channel's own
// command in `dci update --check` output (channelInstruction,
// self_update.go).
func updateNotice(current, latest, instruction string) string {
	return fmt.Sprintf("A new version of dci is available: %s → %s\nRun `%s` to update.\n",
		strings.TrimPrefix(current, "v"), strings.TrimPrefix(latest, "v"), instruction)
}

// parseVersion parses a "v"-optional major.minor.patch triple. Prerelease or
// otherwise decorated tags (1.5.0-rc.1) intentionally fail to parse so they
// never trigger an upgrade notification.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

// isNewerVersion reports whether latest is a strictly newer release than
// current. Unparseable versions on either side (dev builds, prerelease tags,
// garbage) compare as not-newer.
func isNewerVersion(current, latest string) bool {
	c, ok := parseVersion(current)
	if !ok {
		return false
	}
	l, ok := parseVersion(latest)
	if !ok {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}
