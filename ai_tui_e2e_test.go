//go:build !windows

package main

// End-to-end tests for the `dci ai` session: the REAL binary on a REAL
// pseudo-terminal, driven keystroke by keystroke the way a user types —
// not the Bubble Tea model fed synthetic messages (ai_tui_test.go does
// that). These exist because a whole class of regressions lives between
// the model and the terminal: key decoding, the completion popup as
// actually rendered, the dispatch child re-exec, and the error text a
// logged-out user really sees. Every scenario here is a replay of a bug
// a human hit interactively (the v2.7.1 beta/login feedback round).
//
// Mechanics: TestMain re-execs this test binary as the CLI itself (the
// whole CLI is package main, so main() is right here), creack/pty gives
// it a real controlling terminal, and the harness answers the session's
// one startup terminal query (the OSC 11 background-color probe) so
// startup never waits out the mute-terminal timeout. Assertions match
// substrings of the escape-stripped output stream; each test gets a
// fresh session with isolated config/cache dirs and a from-scratch
// environment, so no credentials, agent markers, or proxy settings leak
// in — and the suite stays fully offline (the beta subtree hydrates from
// the embedded spec; the one dispatch test fails at preflight by design).
// Unix-only: creack/pty has no Windows support, and CI runs on Linux.
//
// Two rules keep these assertions honest against a raw output stream:
//
//   - Positive matches must be content the renderer writes IN ONE SHOT —
//     popup rows, transcript blocks, status lines. The input line is off
//     limits: Bubble Tea's renderer diffs at cell level, so typing writes
//     one character per frame and "› /quit" never appears contiguously.
//     Where a test must prove Enter ACCEPTED a completion into the input,
//     it presses Enter again and asserts on the submit's transcript
//     output, which only the accepted command could have produced.
//   - Negative matches only work for content that was never legitimately
//     rendered in the session's whole life: the stream accumulates every
//     frame, so "not on screen anymore" is unprovable, but "never
//     appeared" is exactly what a regression would violate.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// tuiE2EChildMarker routes the re-exec: when set, TestMain hands the
// process straight to main() and it behaves as the CLI. The session's own
// dispatch children inherit it through the environment, so the entire
// process tree under test is the real CLI.
const tuiE2EChildMarker = "DCI_TUI_E2E_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(tuiE2EChildMarker) == "1" {
		main() // never returns: main exits with the CLI's status
		return
	}
	os.Exit(m.Run())
}

// tuiEscapePattern strips everything a terminal would interpret rather
// than print: OSC strings (title, color queries), CSI sequences (styles,
// cursor motion, erase), charset designations, and single-ESC controls.
// The chapter's stripANSI handles only SGR — enough for command output,
// not for a raw Bubble Tea frame stream.
var tuiEscapePattern = regexp.MustCompile(
	`\x1b\][^\x07\x1b]*(\x07|\x1b\\)` + // OSC ... BEL/ST
		`|\x1b\[[0-9;?<=>]*[ -/]*[@-~]` + // CSI
		`|\x1b[()][0-9A-Za-z]` + // charset designation
		`|\x1b[@-Z\\^_]`, // other single-character escapes
)

func stripTerminalEscapes(s string) string {
	s = tuiEscapePattern.ReplaceAllString(s, "")
	return strings.NewReplacer("\r", "", "\x07", "").Replace(s)
}

// tuiSession is one live `dci ai` under test: the child process, its pty
// master, and everything it has written so far. exited closes once the
// process ends (exitErr then holds its Wait result) — a closed channel,
// not a one-shot value send, so the quit test and the cleanup can both
// observe the exit without deadlocking each other.
type tuiSession struct {
	t       *testing.T
	cmd     *exec.Cmd
	tty     *os.File
	exitErr error
	exited  chan struct{}

	mu  sync.Mutex
	raw strings.Builder
}

// startTUISession boots `dci ai` on a fresh pty with isolated config and
// cache dirs (prepare seeds them first — saved commands, cached specs) and
// blocks until the banner renders. The environment is built from scratch:
// no inherited credentials, no agent-detection markers, no proxy — plus
// the CLI's own DCI_AGENT_MODE=0 override, because the test runner itself
// sits in an agent environment the CLI would otherwise rightly detect.
func startTUISession(t *testing.T, prepare func(configDir string)) *tuiSession {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configDir, cacheDir := t.TempDir(), t.TempDir()
	if prepare != nil {
		prepare(configDir)
	}
	cmd := exec.Command(exe, "ai")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TERM=xterm-256color", // no Kitty signal: the logo probe stays off
		tuiE2EChildMarker + "=1",
		"DCI_CONFIG_DIR=" + configDir,
		"DCI_CACHE_DIR=" + cacheDir,
		"DCI_AGENT_MODE=0",
	}
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 140})
	if err != nil {
		t.Fatal(err)
	}
	session := &tuiSession{t: t, cmd: cmd, tty: tty, exited: make(chan struct{})}
	go func() {
		session.exitErr = cmd.Wait()
		close(session.exited)
	}()
	go session.pump()
	t.Cleanup(session.stop)
	// Wait for the WHOLE first frame, not just the banner: keys written
	// while the terminal is still being set up (background-color probe,
	// raw-mode switch) are silently swallowed by the line discipline. The
	// key-onboarding status line is the last chrome of the first frame in
	// these keyless sessions, so once it renders, typing is safe.
	session.waitFor("Cloud Intelligence™ CLI")
	session.waitFor("paste your key · enter save")
	time.Sleep(150 * time.Millisecond)
	return session
}

// pump drains the pty into the transcript buffer and answers the one
// terminal query the session blocks on: lipgloss's OSC 11 background
// probe (a mute terminal otherwise costs its full query timeout).
func (s *tuiSession) pump() {
	answered := false
	buf := make([]byte, 4096)
	for {
		n, err := s.tty.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.raw.Write(buf[:n])
			s.mu.Unlock()
			if !answered && strings.Contains(s.snapshotRaw(), "\x1b]11;?") {
				answered = true
				_, _ = s.tty.WriteString("\x1b]11;rgb:0000/0000/0000\x07")
			}
		}
		if err != nil {
			return // pty closed: the session exited
		}
	}
}

func (s *tuiSession) snapshotRaw() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raw.String()
}

// snapshot is everything the session has rendered so far, escape-stripped.
func (s *tuiSession) snapshot() string {
	return stripTerminalEscapes(s.snapshotRaw())
}

// send writes keystrokes to the terminal one byte-group at a time. Escape
// sequences (arrows) go as one write; printable text goes rune by rune so
// the input driver sees typing, not a paste.
func (s *tuiSession) send(keys ...string) {
	s.t.Helper()
	for _, key := range keys {
		if strings.HasPrefix(key, "\x1b") || len(key) == 1 {
			if _, err := s.tty.WriteString(key); err != nil {
				s.t.Fatalf("send %q: %v", key, err)
			}
			time.Sleep(15 * time.Millisecond)
			continue
		}
		for _, r := range key {
			if _, err := s.tty.WriteString(string(r)); err != nil {
				s.t.Fatalf("send %q: %v", key, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

const (
	keyEnter = "\r"
	keyEsc   = "\x1b"
	keyDown  = "\x1b[B"
)

// waitFor blocks until the stripped transcript contains want, and returns
// the transcript. Ten seconds is an eternity for a local frame render but
// keeps a loaded CI runner from flaking.
func (s *tuiSession) waitFor(want string) string {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		text := s.snapshot()
		if strings.Contains(text, want) {
			return text
		}
		if time.Now().After(deadline) {
			tail := text
			if len(tail) > 2000 {
				tail = tail[len(tail)-2000:]
			}
			s.t.Fatalf("timed out waiting for %q; transcript tail:\n%s", want, tail)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// stop ends the session: the polite /quit first, a kill if the frame is
// wedged. Safe to call twice (t.Cleanup runs it after explicit stops).
func (s *tuiSession) stop() {
	select {
	case <-s.exited:
	default:
		_, _ = s.tty.WriteString(keyEsc) // cancels a running dispatch first
		time.Sleep(50 * time.Millisecond)
		_, _ = s.tty.WriteString("/quit\r")
		select {
		case <-s.exited:
		case <-time.After(3 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.exited
		}
	}
	_ = s.tty.Close()
}

// --- Scenarios ----------------------------------------------------------------

// A fresh session with no credentials and no cached spec: the banner says
// where the API commands are instead of showing a mysteriously small
// count, the first-timer help block renders, and a typed slash drops out
// of the key onboarding into the completion popup.
func TestE2ELoggedOutBannerAndSlashEntersPopup(t *testing.T) {
	session := startTUISession(t, nil)
	session.waitFor("API commands appear after /login")
	session.waitFor("How this session works")
	session.waitFor("API key:") // the guided key onboarding owns the keyboard...
	session.send("/")
	session.waitFor("/customer") // ...until a typed slash opens the popup
}

// The v2.7.1 regression replayed: type /beta, arrow to a subcommand, hit
// Enter. Enter must accept the highlighted row into the input — it used to
// re-submit the bare /beta because "beta" was *a* completion in the list.
// The second Enter submits what the first accepted, and the run card
// header names the command that actually dispatched: only the accepted
// subcommand can put "dci beta run-report" there, and only the regression
// can put "Beta commands" (the bare /beta listing) anywhere at all.
func TestE2EBetaPopupEnterAcceptsHighlightedSubcommand(t *testing.T) {
	session := startTUISession(t, nil)
	session.send("/beta")
	session.waitFor("/beta run-report-config") // full popup up, embedded spec hydrated
	session.send(keyDown, keyDown, keyDown, keyDown)
	session.send(keyEnter) // accepts "beta run-report" into the input
	session.send(keyEnter) // submits it (exact match: no arguments added)
	text := session.waitFor("dci beta run-report")
	if strings.Contains(text, "Beta commands") {
		t.Fatal("Enter re-submitted the bare /beta instead of accepting the highlighted subcommand")
	}
	// The sibling's dispatch header contains the wanted one as a substring
	// ("dci beta run-report-config"), so its absence is what pins WHICH row
	// the highlight was on. Popup rows spell it "/beta run-report-config",
	// so this header never legitimately renders here.
	if strings.Contains(text, "dci beta run-report-config") {
		t.Fatal("Enter accepted the row after the highlighted one")
	}
}

// "/beta run" — a partial second token — must keep completing: prefix
// matches first, and Enter accepts like Tab. It used to strand the user
// with the popup hidden the moment the input carried a space. As above,
// the second Enter proves what the first one accepted.
func TestE2EBetaPartialSecondTokenCompletes(t *testing.T) {
	session := startTUISession(t, nil)
	session.send("/beta run")
	session.waitFor("(beta) Run a saved report asynchronously") // run-report's row is up
	session.send(keyEnter)                                      // accepts the prefix tier's first row
	session.send(keyEnter)                                      // submits it
	text := session.waitFor("dci beta run-report")
	if strings.Contains(text, "Beta commands") {
		t.Fatal("Enter fell back to the bare /beta instead of completing the partial token")
	}
	// See the highlighted-subcommand test: the sibling's header contains
	// this one as a substring, so only its absence pins the selected row.
	if strings.Contains(text, "dci beta run-report-config") {
		t.Fatal("Enter completed to the sibling row instead of the prefix tier's first match")
	}
}

// "/beta operation" — the last token matching inside subcommand names —
// completes through the substring tier, same as the single-token popup.
func TestE2EBetaSubstringSecondTokenCompletes(t *testing.T) {
	session := startTUISession(t, nil)
	session.send("/beta operation")
	text := session.waitFor("/beta cancel-report-operation")
	if !strings.Contains(text, "/beta get-report-operation") {
		t.Fatal("substring tier missed a sibling subcommand")
	}
}

// A fully typed /beta submits on Enter (the exact row is highlighted even
// though subcommand rows share the popup) and renders the session-shaped
// beta list, not the shell-shaped child help.
func TestE2EBareBetaEnterSubmitsAndListsSessionStyle(t *testing.T) {
	session := startTUISession(t, nil)
	session.send("/beta")
	session.waitFor("/beta run-report-config") // popup up: the exact row is highlighted
	session.send(keyEnter)
	text := session.waitFor("Beta commands")
	session.waitFor("Run one with /beta <command>")
	if strings.Contains(text, "dci beta <command>") {
		t.Fatal("bare /beta echoed the shell-shaped child help")
	}
}

// The review-round regression replayed: a saved user command that
// prefix-extends a real name sorts above it in the popup. The rebuilt
// popup must snap the highlight to the exact row so Enter still submits
// the fully typed command instead of rewriting it into the saved one.
func TestE2ESavedCommandShadowStillSubmitsExactInput(t *testing.T) {
	session := startTUISession(t, func(configDir string) {
		commands := `{"beta-mine": {"command": "beta", "summary": "saved shadow"}}`
		if err := os.WriteFile(filepath.Join(configDir, aiUserCommandsFileName), []byte(commands), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	session.send("/beta")
	session.waitFor("saved shadow") // the shadow's popup row is up, above the exact row
	session.send(keyEnter)
	// Only a submit renders the beta listing; the regression would accept
	// "beta-mine" into the input instead and this wait would time out.
	session.waitFor("Beta commands")
}

// Logged out, dispatching a real command: the child subprocess runs, its
// preflight fails, and the session shows the session-shaped remedy — not
// "this session cannot open a browser". Exercises the real re-exec child
// with the real dispatch environment, offline (preflight fails first).
func TestE2ELoggedOutDispatchSaysRunLogin(t *testing.T) {
	session := startTUISession(t, nil)
	session.send("/beta run-report r1", keyEnter)
	text := session.waitFor("you're not signed in — run /login to sign in")
	session.waitFor(fmt.Sprintf("exit %d", exitAuthentication))
	if strings.Contains(text, "cannot open a browser") {
		t.Fatal("logged-out dispatch still shows the shell-shaped browser message")
	}
}

// /quit ends the process cleanly — the session must exit 0, not linger
// until the harness kills it.
func TestE2EQuitExitsCleanly(t *testing.T) {
	session := startTUISession(t, nil)
	session.send("/quit", keyEnter)
	select {
	case <-session.exited:
		if session.exitErr != nil {
			t.Fatalf("session exited with %v", session.exitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/quit did not end the session")
	}
}
