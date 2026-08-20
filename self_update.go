package main

// Self-update: the `dci update` command (UPDATE-SPEC.md) — one command that
// brings the installed CLI to the latest release on every OS and install
// channel. Package-manager installs delegate to the manager's own upgrade
// command after a default-Cancel confirm (overwriting a managed binary would
// corrupt its bookkeeping); deb/rpm installs run the checksum-verified
// download+install pipeline (interactive sudo); unmanaged installs replace
// the binary directly via go-selfupdate, validated against the checksums.txt
// GoReleaser publishes. Kept in a sibling file per the AGENTS.md
// chapter-split guidance.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/creativeprojects/go-selfupdate"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

// installChannel classifies who owns the running binary — the authority on
// how it must be updated.
type installChannel string

const (
	channelBrew   installChannel = "brew"
	channelScoop  installChannel = "scoop"
	channelWinget installChannel = "winget"
	channelDeb    installChannel = "deb"
	channelRPM    installChannel = "rpm"
	channelSelf   installChannel = "self"
)

const updateRepoSlug = "doitintl/dci-cli"

// releaseDownloadBase is the GitHub release asset host; a var so tests can
// point the deb/rpm pipeline at a local server.
var releaseDownloadBase = "https://github.com/doitintl/dci-cli/releases/download"

// packageOwnershipProbe reports whether the packaging tool claims ownership
// of the path (dpkg -S / rpm -qf exit zero for owned files). A missing tool
// simply fails the probe — a dpkg-less system cannot hold a dpkg-owned
// binary. A var so tests need no packaging tools.
var packageOwnershipProbe = func(tool string, args ...string) bool {
	command := exec.Command(tool, args...)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	return command.Run() == nil
}

// detectInstallChannel classifies the resolved executable path. Path
// heuristics come first (cheap, cover brew/scoop/winget on every OS); the
// dpkg/rpm ownership probes run only on Linux and only when the heuristics
// miss (UPDATE-SPEC §3).
func detectInstallChannel(exePath, goos string) installChannel {
	p := strings.ToLower(exePath)
	switch {
	case strings.Contains(p, "cellar") || strings.Contains(p, "homebrew") || strings.Contains(p, "linuxbrew"):
		return channelBrew
	case goos == "windows" && strings.Contains(p, "scoop"):
		return channelScoop
	case goos == "windows" && (strings.Contains(p, "winget") || strings.Contains(p, "windowsapps")):
		return channelWinget
	}
	if goos == "linux" && exePath != "" {
		if packageOwnershipProbe("dpkg", "-S", exePath) {
			return channelDeb
		}
		if packageOwnershipProbe("rpm", "-qf", exePath) {
			return channelRPM
		}
	}
	return channelSelf
}

// detectChannelForUpdate binds channel detection to the running process; a
// var so tests can pin a channel regardless of the host OS (the dpkg/rpm
// probes only ever run on Linux).
var detectChannelForUpdate = func() installChannel {
	return detectInstallChannel(executablePath(), runtime.GOOS)
}

// channelInstruction is the human-runnable command that updates the given
// channel — shown in --check output, printed when a confirm is declined, and
// embedded in structured results for agents.
func channelInstruction(channel installChannel, targetVersion string) string {
	switch channel {
	case channelBrew:
		return "brew upgrade dci"
	case channelScoop:
		return "scoop update dci"
	case channelWinget:
		return "winget upgrade DoiT.dci"
	case channelDeb:
		return linuxPackagePipeline("deb", targetVersion)
	case channelRPM:
		return linuxPackagePipeline("rpm", targetVersion)
	}
	return "dci update"
}

func linuxPackageAssetName(format, targetVersion string) string {
	return fmt.Sprintf("dci_%s_linux_%s.%s", strings.TrimPrefix(targetVersion, "v"), runtime.GOARCH, format)
}

func linuxPackagePipeline(format, targetVersion string) string {
	asset := linuxPackageAssetName(format, targetVersion)
	install := "sudo dpkg -i " + asset
	if format == "rpm" {
		install = "sudo rpm -U " + asset
	}
	return fmt.Sprintf("curl -fsSLO %s/%s/%s && %s", releaseDownloadBase, targetVersion, asset, install)
}

// managerArgv is the delegated update command for manager-owned installs.
func managerArgv(channel installChannel) []string {
	switch channel {
	case channelBrew:
		return []string{"brew", "upgrade", "dci"}
	case channelScoop:
		return []string{"scoop", "update", "dci"}
	case channelWinget:
		return []string{"winget", "upgrade", "DoiT.dci"}
	}
	return nil
}

// runExternalCommand executes a delegated update with the user's terminal
// attached — package managers stream progress, and sudo prompts for the
// password interactively (the CLI never sees or handles it). A var so tests
// capture the argv instead of executing anything.
var runExternalCommand = func(argv []string) error {
	command := exec.Command(argv[0], argv[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}

// confirmUpdate is the default-Cancel prompt shown before anything is
// mutated. Returning false (including on Esc/Ctrl-C, renderer failure, or a
// non-interactive terminal) means "not confirmed". A var so tests force
// either answer.
var confirmUpdate = func(plan string) bool {
	if !tuiActive() {
		return false
	}
	fmt.Fprintln(os.Stderr, tuiNoticeBox.Render(plan))
	confirmed := false
	field := huh.NewConfirm().
		Title("Proceed?").
		Affirmative("Update").
		Negative("Cancel").
		Value(&confirmed)
	if err := tuiForm(field).Run(); err != nil {
		return false
	}
	return confirmed
}

// updateResult is the structured outcome for agent mode (UPDATE-SPEC §7).
type updateResult struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Channel         string `json:"channel"`
	Instruction     string `json:"instruction,omitempty"`
	Updated         bool   `json:"updated,omitempty"`
}

type updateOptions struct {
	check   bool
	yes     bool
	version string
}

func registerUpdateCommand(configDir string) {
	options := updateOptions{}
	command := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Update dci to the latest release",
		Long: "Updates dci in place: package-manager installs (Homebrew, Scoop, WinGet) run the manager's own upgrade command after a confirmation, " +
			"deb/rpm installs run the checksum-verified download+install pipeline, and standalone binaries are replaced directly after validating " +
			"the release checksum. Pass --check to only report whether an update is available.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(configDir, options)
		},
	}
	command.Flags().BoolVar(&options.check, "check", false, "Only check for a new release and print how to update; install nothing")
	command.Flags().BoolVar(&options.yes, "yes", false, "Skip the confirmation prompt")
	command.Flags().StringVar(&options.version, "version", "", "Update (or roll back) to a specific release tag, e.g. v2.5.1 (standalone installs only)")
	cli.Root.AddCommand(command)
}

func updateFailedError(message, hint string) error {
	return invocationPreflightError{
		detail: structuredError{
			Code:      "UPDATE_FAILED",
			Message:   message,
			Hint:      hint,
			Retryable: false,
		},
		exitCode: exitGenericFailure,
	}
}

func updateNetworkError(err error) error {
	return invocationPreflightError{
		detail: structuredError{
			Code:      "NETWORK_ERROR",
			Message:   "could not check for updates: " + err.Error(),
			Hint:      "Check network connectivity and retry",
			Retryable: true,
		},
		exitCode: exitNetwork,
	}
}

// updateStatusReported records that this invocation was `dci update` itself:
// it owns update reporting, so the passive end-of-run footer must stay quiet —
// after a successful install it would falsely announce the pre-update version
// gap, and after --check it would duplicate the notice just printed.
var updateStatusReported bool

func runUpdate(configDir string, options updateOptions) error {
	updateStatusReported = true
	current := version
	if _, ok := parseVersion(current); !ok {
		return updateFailedError(
			fmt.Sprintf("this is a development build (version %q) with no release to compare against", current),
			"Install a released build, or use your normal build workflow to update this binary",
		)
	}

	target := strings.TrimSpace(options.version)
	if target != "" {
		if _, ok := parseVersion(target); !ok {
			return invocationPreflightError{
				detail: structuredError{
					Code:      "USAGE_ERROR",
					Message:   fmt.Sprintf("invalid --version %q (expected a release tag like v2.5.1)", options.version),
					Hint:      "Pass a full major.minor.patch release tag",
					Retryable: false,
				},
				exitCode: exitUsage,
			}
		}
		if !strings.HasPrefix(target, "v") {
			target = "v" + target
		}
	} else {
		tag, err := fetchLatestVersion(latestReleaseAPIURL)
		if err != nil {
			return updateNetworkError(err)
		}
		target = tag
		_ = writeUpdateCache(configDir, updateCache{LatestVersion: tag, CheckedAt: time.Now()})
	}

	channel := detectChannelForUpdate()
	// A version pin only means something on a standalone install: the
	// manager channels run the manager's own upgrade command, which always
	// installs its latest — accepting the flag there would report a rollback
	// that never happened.
	if options.version != "" && channel != channelSelf {
		return invocationPreflightError{
			detail: structuredError{
				Code:      "USAGE_ERROR",
				Message:   fmt.Sprintf("--version applies to standalone installs only; this binary is managed by %s", channel),
				Hint:      "Install the specific version through your package manager instead",
				Retryable: false,
			},
			exitCode: exitUsage,
		}
	}
	pinned := options.version != "" && !versionsEqual(current, target)
	available := isNewerVersion(current, target) || pinned
	result := updateResult{
		Current:         strings.TrimPrefix(current, "v"),
		Latest:          strings.TrimPrefix(target, "v"),
		UpdateAvailable: available,
		Channel:         string(channel),
		Instruction:     channelInstruction(channel, target),
	}

	// Machine contract: without an explicit --yes nothing is ever mutated —
	// the invocation degrades to a check that reports the structured result.
	if agentMode {
		if !available || options.check || !options.yes {
			return emitUpdateResult(result)
		}
		// The deb/rpm install step needs sudo's interactive password prompt;
		// on a pty-backed agent shell sudo would block on /dev/tty forever
		// instead of failing, so agent mode refuses rather than hangs.
		if channel == channelDeb || channel == channelRPM {
			return updateFailedError(
				fmt.Sprintf("updating a %s-managed install needs an interactive sudo prompt, which agent mode cannot provide", channel),
				"Run manually: "+result.Instruction,
			)
		}
		if err := performUpdate(channel, target); err != nil {
			return updateFailedError(err.Error(), "Update manually: "+result.Instruction)
		}
		result.Updated = true
		return emitUpdateResult(result)
	}

	if !available {
		fmt.Fprintf(os.Stdout, "dci %s is up to date (latest release: %s).\n", result.Current, result.Latest)
		return nil
	}
	if options.check {
		fmt.Fprint(os.Stdout, updateNotice(current, target, result.Instruction))
		return nil
	}

	plan := updatePlan(channel, result.Current, result.Latest, result.Instruction)
	if !options.yes && !confirmUpdate(plan) {
		fmt.Fprintf(os.Stderr, "Update cancelled. To update manually, run: %s\n", result.Instruction)
		return nil
	}
	if err := performUpdate(channel, target); err != nil {
		return updateFailedError(err.Error(), "Update manually: "+result.Instruction)
	}
	fmt.Fprintf(os.Stdout, "dci updated: %s → %s\n", result.Current, result.Latest)
	return nil
}

func versionsEqual(a, b string) bool {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	return okA && okB && va == vb
}

func emitUpdateResult(result updateResult) error {
	return json.NewEncoder(os.Stdout).Encode(result)
}

// updatePlan describes what confirming will run, so the default-Cancel
// prompt is informed consent rather than a mystery box.
func updatePlan(channel installChannel, current, latest, instruction string) string {
	action := ""
	switch channel {
	case channelBrew, channelScoop, channelWinget:
		action = "This runs: " + instruction
	case channelDeb, channelRPM:
		action = "This downloads the package, verifies its checksum, and runs the install step with sudo (you will be asked for your password):\n" + instruction
	default:
		action = "This downloads the release, verifies its checksum, and replaces this binary in place."
	}
	return fmt.Sprintf("Update dci %s → %s\n%s", current, latest, action)
}

func performUpdate(channel installChannel, target string) error {
	switch channel {
	case channelBrew, channelScoop, channelWinget:
		argv := managerArgv(channel)
		if err := runExternalCommand(argv); err != nil {
			return fmt.Errorf("%s failed: %w", strings.Join(argv, " "), err)
		}
		return nil
	case channelDeb, channelRPM:
		return updateLinuxPackage(channel, target)
	default:
		return performSelfUpdate(target)
	}
}

// performSelfUpdate binds the standalone-binary replacement; a var so tests
// can exercise runUpdate's self-channel flows without touching GitHub.
var performSelfUpdate = selfUpdateBinary

// selfUpdateBinary replaces the running binary with the release asset for
// this GOOS/GOARCH, validated against the release's checksums.txt. The
// library replaces atomically with rollback and handles the Windows
// can't-overwrite-a-running-exe rename.
func selfUpdateBinary(target string) error {
	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
	})
	if err != nil {
		return fmt.Errorf("initialize updater: %w", err)
	}
	ctx := context.Background()
	release, found, err := updater.DetectVersion(ctx, selfupdate.ParseSlug(updateRepoSlug), target)
	if err != nil {
		return fmt.Errorf("locate release %s: %w", target, err)
	}
	if !found || release == nil {
		return fmt.Errorf("release %s has no asset for %s/%s", target, runtime.GOOS, runtime.GOARCH)
	}
	exe := executablePath()
	if exe == "" {
		return fmt.Errorf("could not resolve the running executable path")
	}
	if err := updater.UpdateTo(ctx, release, exe); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	return nil
}

// updateLinuxPackage is the deb/rpm path: no repository exists to delegate
// to, and replacing a dpkg/rpm-owned file behind the database's back is off
// the table — so the package is downloaded and checksum-verified by dci, and
// only the install step runs under sudo (interactively, in the user's
// terminal).
func updateLinuxPackage(channel installChannel, target string) error {
	format := "deb"
	install := []string{"sudo", "dpkg", "-i"}
	if channel == channelRPM {
		format = "rpm"
		install = []string{"sudo", "rpm", "-U"}
	}
	asset := linuxPackageAssetName(format, target)
	dir, err := os.MkdirTemp("", "dci-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	packagePath := filepath.Join(dir, asset)
	if err := downloadReleaseFile(target, asset, packagePath); err != nil {
		return err
	}
	checksumsPath := filepath.Join(dir, "checksums.txt")
	if err := downloadReleaseFile(target, "checksums.txt", checksumsPath); err != nil {
		return err
	}
	if err := verifyChecksum(packagePath, checksumsPath, asset); err != nil {
		return err
	}
	if err := runExternalCommand(append(install, packagePath)); err != nil {
		return fmt.Errorf("%s failed: %w", strings.Join(install, " "), err)
	}
	return nil
}

func downloadReleaseFile(target, name, destination string) error {
	url := fmt.Sprintf("%s/%s/%s", releaseDownloadBase, target, name)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", buildUserAgent(agentUAMode))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d from %s", name, response.StatusCode, url)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, response.Body)
	return err
}

// verifyChecksum compares the file's SHA-256 against the release's
// checksums.txt entry. No entry, or a mismatch, aborts the update loudly —
// an unverifiable package is never handed to the package manager.
func verifyChecksum(path, checksumsPath, assetName string) error {
	sums, err := os.ReadFile(checksumsPath)
	if err != nil {
		return err
	}
	expected := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == assetName {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksums.txt has no entry for %s", assetName)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", assetName, expected, actual)
	}
	return nil
}
