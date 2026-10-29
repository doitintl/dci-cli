package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creativeprojects/go-selfupdate"
)

// names renders directory entries for failure messages.
func names(entries []os.DirEntry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Name()
	}
	return out
}

// fakeUpdateEnv pins the pieces of runUpdate's environment that tests need
// deterministic: released version, human/agent mode, confirm answer, and a
// captured external-command runner.
func fakeUpdateEnv(t *testing.T, agent bool, confirm bool) *[][]string {
	t.Helper()
	originalVersion := version
	version = "2.5.0"
	t.Cleanup(func() { version = originalVersion })

	originalAgent := agentMode
	agentMode = agent
	t.Cleanup(func() { agentMode = originalAgent })

	originalConfirm := confirmUpdate
	confirmUpdate = func(plan string) bool { return confirm }
	t.Cleanup(func() { confirmUpdate = originalConfirm })

	updateStatusReported = false
	t.Cleanup(func() { updateStatusReported = false })

	ran := &[][]string{}
	originalRun := runExternalCommand
	runExternalCommand = func(argv []string) error {
		*ran = append(*ran, argv)
		return nil
	}
	t.Cleanup(func() { runExternalCommand = originalRun })

	// No second binary exists to interrogate under test; an unavailable probe
	// is the "cannot prove a no-op" path, so the manager flows stay focused on
	// what they delegate. Tests that care about the check stub it themselves.
	originalProbe := installedVersionProbe
	installedVersionProbe = func() (string, error) {
		return "", fmt.Errorf("no version probe under test")
	}
	t.Cleanup(func() { installedVersionProbe = originalProbe })
	return ran
}

// fakeSelfUpdate captures self-channel replacements instead of hitting GitHub.
func fakeSelfUpdate(t *testing.T) *[]string {
	t.Helper()
	targets := &[]string{}
	original := performSelfUpdate
	performSelfUpdate = func(target string) error {
		*targets = append(*targets, target)
		return nil
	}
	t.Cleanup(func() { performSelfUpdate = original })
	return targets
}

// serveLatestRelease points the release lookup at a local server returning
// the given tag.
func serveLatestRelease(t *testing.T, tag string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprintf(writer, `{"tag_name": %q}`, tag)
	}))
	t.Cleanup(server.Close)
	originalURL := latestReleaseAPIURL
	latestReleaseAPIURL = server.URL
	t.Cleanup(func() { latestReleaseAPIURL = originalURL })
}

// forceChannel pins channel detection to a deterministic result — the real
// detector depends on the host OS and the test binary's path.
func forceChannel(t *testing.T, channel installChannel) {
	t.Helper()
	original := detectChannelForUpdate
	detectChannelForUpdate = func() installChannel { return channel }
	t.Cleanup(func() { detectChannelForUpdate = original })
}

func captureStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	runErr := run()
	writer.Close()
	os.Stdout = original
	out := make([]byte, 1<<16)
	n, _ := reader.Read(out)
	return string(out[:n]), runErr
}

func TestRunUpdateDelegatesToManagerAfterConfirm(t *testing.T) {
	ran := fakeUpdateEnv(t, false, true)
	serveLatestRelease(t, "v2.6.0")
	forceChannel(t, channelBrew)

	out, err := captureStdout(t, func() error { return runUpdate(t.TempDir(), updateOptions{}) })
	if err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 2 || strings.Join((*ran)[0], " ") != "brew update" || strings.Join((*ran)[1], " ") != "brew upgrade dci" {
		t.Fatalf("external commands = %v, want brew update then brew upgrade dci", *ran)
	}
	if !strings.Contains(out, "dci updated: 2.5.0 → 2.6.0") {
		t.Fatalf("output = %q, want the success line", out)
	}
}

func TestRunUpdateDeclineRunsNothing(t *testing.T) {
	ran := fakeUpdateEnv(t, false, false)
	serveLatestRelease(t, "v2.6.0")
	forceChannel(t, channelBrew)

	if _, err := captureStdout(t, func() error { return runUpdate(t.TempDir(), updateOptions{}) }); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 0 {
		t.Fatalf("declining the confirm must run nothing, ran %v", *ran)
	}
}

func TestRunUpdateUpToDate(t *testing.T) {
	ran := fakeUpdateEnv(t, false, true)
	serveLatestRelease(t, "v2.5.0")

	out, err := captureStdout(t, func() error { return runUpdate(t.TempDir(), updateOptions{}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "up to date") || len(*ran) != 0 {
		t.Fatalf("out = %q ran = %v, want up-to-date and no commands", out, *ran)
	}
}

func TestRunUpdateCheckOnly(t *testing.T) {
	ran := fakeUpdateEnv(t, false, true)
	serveLatestRelease(t, "v2.6.0")
	forceChannel(t, channelSelf)

	out, err := captureStdout(t, func() error { return runUpdate(t.TempDir(), updateOptions{check: true}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "A new version of dci is available: 2.5.0 → 2.6.0") || len(*ran) != 0 {
		t.Fatalf("out = %q ran = %v, want the notice and no commands", out, *ran)
	}
}

func TestRunUpdateAgentContract(t *testing.T) {
	ran := fakeUpdateEnv(t, true, true)
	serveLatestRelease(t, "v2.6.0")
	forceChannel(t, channelBrew)

	// Without --yes: a structured check result, nothing mutated.
	out, err := captureStdout(t, func() error { return runUpdate(t.TempDir(), updateOptions{}) })
	if err != nil {
		t.Fatal(err)
	}
	var result updateResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("agent output is not JSON: %q (%v)", out, err)
	}
	if !result.UpdateAvailable || result.Updated || result.Channel != "brew" || result.Current != "2.5.0" || result.Latest != "2.6.0" {
		t.Fatalf("agent result = %+v", result)
	}
	if len(*ran) != 0 {
		t.Fatalf("agent mode without --yes must run nothing, ran %v", *ran)
	}

	// With --yes: performs and reports updated.
	out, err = captureStdout(t, func() error { return runUpdate(t.TempDir(), updateOptions{yes: true}) })
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.Updated {
		t.Fatalf("agent --yes result = %q (%v)", out, err)
	}
	if len(*ran) != 2 {
		t.Fatalf("agent --yes must refresh and perform the update, ran %v", *ran)
	}
}

func TestRunUpdateRefusesDevBuilds(t *testing.T) {
	fakeUpdateEnv(t, false, true)
	originalVersion := version
	version = "dev"
	t.Cleanup(func() { version = originalVersion })

	err := runUpdate(t.TempDir(), updateOptions{})
	preflight, ok := err.(invocationPreflightError)
	if !ok || preflight.detail.Code != "UPDATE_FAILED" {
		t.Fatalf("dev build error = %#v, want UPDATE_FAILED", err)
	}
}

func TestRunUpdateRejectsInvalidPin(t *testing.T) {
	fakeUpdateEnv(t, false, true)
	err := runUpdate(t.TempDir(), updateOptions{version: "latest"})
	preflight, ok := err.(invocationPreflightError)
	if !ok || preflight.detail.Code != "USAGE_ERROR" {
		t.Fatalf("invalid pin error = %#v, want USAGE_ERROR", err)
	}
}

func TestRunUpdatePinAllowsRollback(t *testing.T) {
	fakeUpdateEnv(t, false, true)
	forceChannel(t, channelSelf)
	targets := fakeSelfUpdate(t)

	out, err := captureStdout(t, func() error { return runUpdate(t.TempDir(), updateOptions{version: "2.4.0"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dci updated: 2.5.0 → 2.4.0") {
		t.Fatalf("rollback out = %q", out)
	}
	if len(*targets) != 1 || (*targets)[0] != "v2.4.0" {
		t.Fatalf("self-update targets = %v, want v2.4.0", *targets)
	}
}

func TestRunUpdateRejectsPinOnManagedChannels(t *testing.T) {
	ran := fakeUpdateEnv(t, false, true)
	forceChannel(t, channelBrew)

	err := runUpdate(t.TempDir(), updateOptions{version: "2.4.0"})
	preflight, ok := err.(invocationPreflightError)
	if !ok || preflight.detail.Code != "USAGE_ERROR" {
		t.Fatalf("managed pin error = %#v, want USAGE_ERROR", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("a rejected pin must run nothing, ran %v", *ran)
	}
}

func TestRunUpdateAgentRefusesSudoChannels(t *testing.T) {
	ran := fakeUpdateEnv(t, true, true)
	serveLatestRelease(t, "v2.6.0")
	forceChannel(t, channelDeb)

	err := runUpdate(t.TempDir(), updateOptions{yes: true})
	preflight, ok := err.(invocationPreflightError)
	if !ok || preflight.detail.Code != "UPDATE_FAILED" || !strings.Contains(preflight.detail.Message, "interactive sudo") {
		t.Fatalf("agent deb error = %#v, want the sudo refusal", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("agent mode must never reach sudo, ran %v", *ran)
	}
}

func TestRunUpdateSuppressesPassiveFooter(t *testing.T) {
	fakeUpdateEnv(t, false, true)
	serveLatestRelease(t, "v2.5.0")
	dir := t.TempDir()

	if _, err := captureStdout(t, func() error { return runUpdate(dir, updateOptions{}) }); err != nil {
		t.Fatal(err)
	}
	if !updateStatusReported {
		t.Fatal("runUpdate must mark update status as reported")
	}
	// Even with a cache claiming a newer release, the footer stays quiet
	// after dci update has spoken.
	if err := writeUpdateCache(dir, updateCache{LatestVersion: "v9.9.9", CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out := captureStderr(t, func() { maybeNotifyUpdate(dir) })
	if out != "" {
		t.Fatalf("footer printed after dci update: %q", out)
	}
}

func TestManagerArgvAndInstruction(t *testing.T) {
	if got := strings.Join(managerRefreshArgv(channelBrew), " "); got != "brew update" {
		t.Errorf("brew refresh argv = %q", got)
	}
	if got := strings.Join(managerRefreshArgv(channelScoop), " "); got != "scoop update" {
		t.Errorf("scoop refresh argv = %q", got)
	}
	if got := managerRefreshArgv(channelWinget); got != nil {
		t.Errorf("winget refresh argv = %v, want none", got)
	}
	if got := strings.Join(managerArgv(channelBrew), " "); got != "brew upgrade dci" {
		t.Errorf("brew argv = %q", got)
	}
	if got := strings.Join(managerArgv(channelScoop), " "); got != "scoop update dci" {
		t.Errorf("scoop argv = %q", got)
	}
	if got := strings.Join(managerArgv(channelWinget), " "); got != "winget upgrade DoiT.dci" {
		t.Errorf("winget argv = %q", got)
	}
	if got := channelInstruction(channelSelf, "v2.6.0"); got != "dci update" {
		t.Errorf("self instruction = %q", got)
	}
	deb := channelInstruction(channelDeb, "v2.6.0")
	if !strings.Contains(deb, "curl -fsSLO") || !strings.Contains(deb, "sudo dpkg -i") || !strings.Contains(deb, "2.6.0_linux_") {
		t.Errorf("deb instruction = %q", deb)
	}
	rpm := channelInstruction(channelRPM, "v2.6.0")
	if !strings.Contains(rpm, "sudo rpm -U") {
		t.Errorf("rpm instruction = %q", rpm)
	}
}

func TestUpdateLinuxPackageVerifiesChecksum(t *testing.T) {
	ran := fakeUpdateEnv(t, false, true)
	packageBody := []byte("fake-deb-contents")
	sum := sha256.Sum256(packageBody)
	assetName := linuxPackageAssetName("deb", "v2.6.0")
	goodChecksums := hex.EncodeToString(sum[:]) + "  " + assetName + "\n"
	checksums := goodChecksums

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "checksums.txt"):
			fmt.Fprint(writer, checksums)
		case strings.HasSuffix(request.URL.Path, assetName):
			writer.Write(packageBody)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	originalBase := releaseDownloadBase
	releaseDownloadBase = server.URL
	t.Cleanup(func() { releaseDownloadBase = originalBase })

	if err := updateLinuxPackage(channelDeb, "v2.6.0"); err != nil {
		t.Fatalf("verified update failed: %v", err)
	}
	if len(*ran) != 1 || (*ran)[0][0] != "sudo" || (*ran)[0][1] != "dpkg" {
		t.Fatalf("install command = %v, want sudo dpkg -i", *ran)
	}

	// A checksum mismatch must abort before any install command runs.
	*ran = nil
	checksums = strings.Repeat("0", 64) + "  " + assetName + "\n"
	err := updateLinuxPackage(channelDeb, "v2.6.0")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch error = %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("mismatch must not install, ran %v", *ran)
	}

	// A missing checksum entry is equally fatal.
	checksums = strings.Repeat("0", 64) + "  some-other-file\n"
	err = updateLinuxPackage(channelDeb, "v2.6.0")
	if err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("missing entry error = %v", err)
	}
}

// A manager that exits 0 without moving the binary — a stale index re-resolving
// the installed version — must be reported as a failure, not as an update.
func TestVerifyManagerUpgradeCatchesNoOp(t *testing.T) {
	stubProbe := func(version string, err error) {
		original := installedVersionProbe
		installedVersionProbe = func() (string, error) { return version, err }
		t.Cleanup(func() { installedVersionProbe = original })
	}

	stubProbe("2.5.0", nil)
	err := verifyManagerUpgrade(channelBrew, "v2.6.0")
	if err == nil || !strings.Contains(err.Error(), "still 2.5.0") || !strings.Contains(err.Error(), "not 2.6.0") {
		t.Fatalf("no-op upgrade error = %v, want the version gap reported", err)
	}

	// The target landed: nothing to report.
	stubProbe("2.6.0", nil)
	if err := verifyManagerUpgrade(channelBrew, "v2.6.0"); err != nil {
		t.Fatalf("successful upgrade error = %v", err)
	}

	// A manager that installed something newer still succeeded.
	stubProbe("2.7.0", nil)
	if err := verifyManagerUpgrade(channelBrew, "v2.6.0"); err != nil {
		t.Fatalf("newer-than-target error = %v", err)
	}

	// An unanswerable probe proves nothing and must not invent a failure.
	stubProbe("", fmt.Errorf("probe unavailable"))
	if err := verifyManagerUpgrade(channelBrew, "v2.6.0"); err != nil {
		t.Fatalf("unavailable probe error = %v", err)
	}
}

// The no-op has to surface as a UPDATE_FAILED invocation error with the manual
// command as its hint, not as the success line.
func TestRunUpdateReportsManagerNoOp(t *testing.T) {
	fakeUpdateEnv(t, false, true)
	serveLatestRelease(t, "v2.6.0")
	forceChannel(t, channelBrew)
	installedVersionProbe = func() (string, error) { return "2.5.0", nil }

	out, err := captureStdout(t, func() error { return runUpdate(t.TempDir(), updateOptions{}) })
	preflight, ok := err.(invocationPreflightError)
	if !ok || preflight.detail.Code != "UPDATE_FAILED" {
		t.Fatalf("no-op upgrade error = %#v, want UPDATE_FAILED", err)
	}
	if !strings.Contains(preflight.detail.Hint, "brew update && brew upgrade dci") {
		t.Fatalf("hint = %q, want the manual command", preflight.detail.Hint)
	}
	if strings.Contains(out, "dci updated") {
		t.Fatalf("output = %q, must not claim an update happened", out)
	}
}

func TestParseVersionOutput(t *testing.T) {
	for _, out := range []string{"dci version 2.7.4", "dci version 2.7.4\nextra line\n", "  v2.7.4  "} {
		got, err := parseVersionOutput(out)
		if err != nil || strings.TrimPrefix(got, "v") != "2.7.4" {
			t.Errorf("parseVersionOutput(%q) = %q, %v", out, got, err)
		}
	}
	for _, out := range []string{"", "dci version dev", "dci version 2.7"} {
		if got, err := parseVersionOutput(out); err == nil {
			t.Errorf("parseVersionOutput(%q) = %q, want an error", out, got)
		}
	}
}

func TestStageReleaseTarget(t *testing.T) {
	t.Run("already named after the archive member updates in place", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "dci")
		if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		staged, cleanup, err := stageReleaseTarget(exe)
		if err != nil {
			t.Fatal(err)
		}
		if staged != exe {
			t.Fatalf("staged = %q, want the exe path unchanged", staged)
		}
		cleanup()
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
			t.Fatalf("dir entries = %v, %v; want only the binary, no staging dir", names(entries), err)
		}
	})

	t.Run("windows canonical dci.exe updates in place", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "dci.exe")
		if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		staged, cleanup, err := stageReleaseTarget(exe)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if staged != exe {
			t.Fatalf("staged = %q, want the exe path unchanged (the archive member matches dci.exe too)", staged)
		}
	})

	t.Run("renamed install stages under the archive member name", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "dci-standalone")
		if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		staged, cleanup, err := stageReleaseTarget(exe)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if filepath.Base(staged) != "dci" || filepath.Dir(filepath.Dir(staged)) != dir ||
			filepath.Base(filepath.Dir(staged)) == dir {
			t.Fatalf("staged = %q, want %s/.dci-update-*/dci", staged, dir)
		}
		if info, err := os.Stat(staged); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("staged file = %v, %v; want a regular file the library can replace", info, err)
		}
		cleanup()
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
			t.Fatalf("dir entries after cleanup = %v, %v; want only the binary", names(entries), err)
		}
	})

	t.Run("unwritable directory fails without touching the original", func(t *testing.T) {
		// Windows ignores directory chmod bits and Geteuid is meaningless
		// there, so the failure cannot be staged portably.
		if runtime.GOOS == "windows" {
			t.Skip("directory chmod does not restrict writes on windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("root writes anywhere; the permission failure cannot be staged")
		}
		dir := t.TempDir()
		exe := filepath.Join(dir, "dci-standalone")
		if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		if _, _, err := stageReleaseTarget(exe); err == nil {
			t.Fatal("staging in an unwritable directory must fail")
		}
		if got, err := os.ReadFile(exe); err != nil || string(got) != "old" {
			t.Fatalf("original = %q, %v; want it untouched", got, err)
		}
	})
}

// buildFakeTar gzips a one-member tar — the shape GoReleaser publishes for
// darwin/linux release assets — with the executable under the given member
// name.
func buildFakeTar(t *testing.T, member string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeReleaseSource serves a go-selfupdate HTTPSource manifest with one
// release (v2.9.0) whose asset is a tar.gz shipping the executable under the
// archive member name, plus its checksums.txt — the layout GoReleaser
// publishes. It repoints newSelfUpdater at the server so selfUpdateBinary's
// real DetectVersion/UpdateTo/Apply path runs offline. checksumHex overrides
// the recorded digest ("" = correct) to exercise checksum rejection.
func fakeReleaseSource(t *testing.T, payload []byte, checksumHex string) {
	t.Helper()
	assetName := fmt.Sprintf("dci_2.9.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := buildFakeTar(t, "dci", payload)
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	if checksumHex != "" {
		digest = checksumHex
	}

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	// Handlers may register after the server starts; the manifest needs the
	// server URL for the asset links.
	mux.HandleFunc("/doitintl/dci-cli/manifest.yaml", func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprintf(writer, `releases:
  - id: 1
    tag_name: v2.9.0
    assets:
      - id: 2
        name: %s
        url: %s/asset
      - id: 3
        name: checksums.txt
        url: %s/checksums
`, assetName, server.URL, server.URL)
	})
	mux.HandleFunc("/asset", func(writer http.ResponseWriter, request *http.Request) {
		writer.Write(archive)
	})
	mux.HandleFunc("/checksums", func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprintf(writer, "%s  %s\n", digest, assetName)
	})

	originalUpdater := newSelfUpdater
	newSelfUpdater = func() (*selfupdate.Updater, error) {
		source, err := selfupdate.NewHttpSource(selfupdate.HttpConfig{BaseURL: server.URL})
		if err != nil {
			return nil, err
		}
		return selfupdate.NewUpdater(selfupdate.Config{
			Source:    source,
			Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
		})
	}
	t.Cleanup(func() { newSelfUpdater = originalUpdater })
}

// The real DetectVersion → UpdateTo → update.Apply path against a fake release
// source: a renamed install updates in place through the staging contract
// (#91), an install named after the archive member never stages, and a bad
// checksum leaves the original untouched.
func TestSelfUpdateBinaryReplacesInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release ships a tar.gz; windows release assets are zip")
	}
	payload := []byte("the updated dci binary")
	fakeReleaseSource(t, payload, "")

	aimExe := func(t *testing.T, exe string) {
		t.Helper()
		original := executablePathForUpdate
		executablePathForUpdate = func() string { return exe }
		t.Cleanup(func() { executablePathForUpdate = original })
	}

	t.Run("renamed install", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "dci-standalone")
		if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		aimExe(t, exe)

		if err := selfUpdateBinary("v2.9.0"); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(exe); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("updated binary = %q, %v; want the release payload", got, err)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
			t.Fatalf("dir entries = %v, %v; want only the binary, no staging or backup junk", names(entries), err)
		}
	})

	t.Run("install named after the archive member", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "dci")
		if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		aimExe(t, exe)

		if err := selfUpdateBinary("v2.9.0"); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(exe); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("updated binary = %q, %v; want the release payload", got, err)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
			t.Fatalf("dir entries = %v, %v; want only the binary, no staging dir", names(entries), err)
		}
	})
}

func TestSelfUpdateBinaryRejectsBadChecksum(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release ships a tar.gz; windows release assets are zip")
	}
	payload := []byte("the updated dci binary")
	fakeReleaseSource(t, payload, "deadbeef")

	dir := t.TempDir()
	exe := filepath.Join(dir, "dci-standalone")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := executablePathForUpdate
	executablePathForUpdate = func() string { return exe }
	t.Cleanup(func() { executablePathForUpdate = original })

	if err := selfUpdateBinary("v2.9.0"); err == nil {
		t.Fatal("a bad checksum must abort the update")
	}
	if got, err := os.ReadFile(exe); err != nil || string(got) != "old" {
		t.Fatalf("original = %q, %v; want it untouched", got, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("dir entries = %v, %v; want only the binary, no partial junk", names(entries), err)
	}
}
