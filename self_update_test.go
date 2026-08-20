package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

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

	ran := &[][]string{}
	originalRun := runExternalCommand
	runExternalCommand = func(argv []string) error {
		*ran = append(*ran, argv)
		return nil
	}
	t.Cleanup(func() { runExternalCommand = originalRun })
	return ran
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
	if len(*ran) != 1 || strings.Join((*ran)[0], " ") != "brew upgrade dci" {
		t.Fatalf("external commands = %v, want exactly brew upgrade dci", *ran)
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
	if len(*ran) != 1 {
		t.Fatalf("agent --yes must perform the update, ran %v", *ran)
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
	ran := fakeUpdateEnv(t, false, true)
	forceChannel(t, channelBrew)

	out, err := captureStdout(t, func() error { return runUpdate(t.TempDir(), updateOptions{version: "2.4.0"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dci updated: 2.5.0 → 2.4.0") || len(*ran) != 1 {
		t.Fatalf("rollback out = %q ran = %v", out, *ran)
	}
}

func TestManagerArgvAndInstruction(t *testing.T) {
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
