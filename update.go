package main

// Update check: notifies about new releases and powers `dci upgrade`.
// Kept in a sibling file per the AGENTS.md chapter-split guidance.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// startUpdateCheck spawns a background refresh of the release cache when it
// is stale, so the lookup runs in parallel with the command's own API call
// instead of adding latency. Returns nil when no refresh is needed (fresh
// cache or suppressed); otherwise a channel closed when the refresh finishes.
func startUpdateCheck(configDir string) <-chan struct{} {
	if updateCheckSuppressed(agentMode, stderrIsTTY(), os.Getenv("DCI_NO_UPDATE_CHECK"), version) {
		return nil
	}
	if c, ok := readUpdateCache(configDir); ok && !c.stale(time.Now()) {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshUpdateCache(configDir, latestReleaseAPIURL, time.Now())
	}()
	return done
}

// maybeNotifyUpdate prints the new-version hint after command output. It
// waits up to a second for an in-flight refresh — usually already finished
// since it ran alongside the command — and abandons it otherwise; the next
// run picks up where this one left off.
func maybeNotifyUpdate(configDir string, done <-chan struct{}) {
	if updateCheckSuppressed(agentMode, stderrIsTTY(), os.Getenv("DCI_NO_UPDATE_CHECK"), version) {
		return
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	c, ok := readUpdateCache(configDir)
	if !ok || !isNewerVersion(version, c.LatestVersion) {
		return
	}
	fmt.Fprint(os.Stderr, "\n"+updateNotice(version, c.LatestVersion, upgradeInstruction(executablePath(), runtime.GOOS)))
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

func registerUpgradeCommand(configDir string) {
	cli.Root.AddCommand(&cobra.Command{
		Use:   "upgrade",
		Short: "Check for a new version of dci",
		Long:  "Checks for the latest dci release and prints the upgrade command for your install method. Does not install anything.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Explicit user action: always fetch fresh and report failures,
			// unlike the passive background check.
			tag, err := fetchLatestVersion(latestReleaseAPIURL)
			if err != nil {
				return fmt.Errorf("could not check for updates: %w", err)
			}
			_ = writeUpdateCache(configDir, updateCache{LatestVersion: tag, CheckedAt: time.Now()})
			if !isNewerVersion(version, tag) {
				fmt.Fprintf(os.Stdout, "dci %s is up to date (latest release: %s).\n",
					version, strings.TrimPrefix(tag, "v"))
				return nil
			}
			fmt.Fprint(os.Stdout, updateNotice(version, tag, upgradeInstruction(executablePath(), runtime.GOOS)))
			return nil
		},
	})
}

const updateCheckTTL = 5 * time.Hour

const latestReleaseAPIURL = "https://api.github.com/repos/doitintl/dci-cli/releases/latest"

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
}

// stale reports whether the cache needs a refresh. A CheckedAt far in the
// future (clock rolled back since the last check) also counts as stale, so a
// bad timestamp can't suppress checks indefinitely.
func (c updateCache) stale(now time.Time) bool {
	d := now.Sub(c.CheckedAt)
	return d > updateCheckTTL || d < -updateCheckTTL
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
// Errors are swallowed: on any failure the previous cache stays as-is and the
// check simply retries on a later run.
func refreshUpdateCache(configDir, url string, now time.Time) {
	tag, err := fetchLatestVersion(url)
	if err != nil {
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

const releasesPageURL = "https://github.com/doitintl/dci-cli/releases/latest"

// upgradeInstruction returns the upgrade command for the package manager that
// owns the running binary, detected from the executable path. The deb/rpm
// packages are downloaded straight from GitHub Releases (there is no apt/yum
// repository), so they — and any unrecognized install — get the releases page
// instead of a package-manager command that could never find an update.
func upgradeInstruction(exePath, goos string) string {
	p := strings.ToLower(exePath)
	switch {
	case strings.Contains(p, "cellar") || strings.Contains(p, "homebrew"):
		return "brew upgrade dci"
	case goos == "windows" && strings.Contains(p, "scoop"):
		return "scoop update dci"
	case goos == "windows" && strings.Contains(p, "winget"):
		return "winget upgrade DoiT.dci"
	}
	return "Download the latest release from " + releasesPageURL
}

// updateNotice formats the new-version message. An instruction that is a
// runnable command gets wrapped in "Run `...` to update."; the releases-page
// fallback from upgradeInstruction is already a full sentence and is printed
// verbatim.
func updateNotice(current, latest, instruction string) string {
	msg := fmt.Sprintf("A new version of dci is available: %s → %s\n",
		strings.TrimPrefix(current, "v"), strings.TrimPrefix(latest, "v"))
	if strings.HasPrefix(instruction, "Download ") {
		return msg + instruction + "\n"
	}
	return msg + "Run `" + instruction + "` to update.\n"
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
