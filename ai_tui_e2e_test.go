//go:build !windows

package main

// End-to-end tests for the `dci ai` session: the REAL binary on a REAL
// pseudo-terminal, driven keystroke by keystroke the way a user types —
// not the Bubble Tea model fed synthetic messages (ai_tui_test.go does
// that). These exist because a whole class of regressions lives between
// the model and the terminal: key decoding, the completion popup as
// actually rendered, the dispatch child re-exec, and the error text a
// logged-out user really sees. The first scenarios replay the bugs humans
// hit interactively (the v2.7.1 beta/login feedback round); the rest walk
// the session's other offline-reachable journeys — dispatch lifecycle,
// verbs and their persistence across restarts, history, paste, resize.
//
// Mechanics: TestMain re-execs this test binary as the CLI itself (the
// whole CLI is package main, so main() is right here), creack/pty gives
// it a real controlling terminal, and the harness answers the session's
// one startup terminal query (the OSC 11 background-color probe) so
// startup never waits out the mute-terminal timeout. Assertions match
// substrings of the escape-stripped output stream; each test gets a
// fresh session with isolated config/cache dirs and a from-scratch
// environment, so no credentials, agent markers, or proxy settings leak
// in — and the suite stays fully offline: the beta subtree hydrates from
// the embedded spec, the GA catalog from a forged spec cache
// (seedCachedSpec), and dispatch children either succeed locally
// (/logout), fail at parsing or preflight by design, or are the
// harness's scripted fake (tuiE2EFakeChild). Choose dispatch commands
// with care: anything
// under a resolvable command is intercepted by the session's name picker
// (a network fetch), and beta commands tolerate unknown flags and relax
// arity, so their offline failures are auth-shaped, not argv-shaped.
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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/fxamacker/cbor/v2"
	"github.com/rest-sh/restish/cli"
)

// tuiE2EChildMarker routes the re-exec: when set, TestMain hands the
// process straight to main() and it behaves as the CLI. The session's own
// dispatch children inherit it through the environment, so the entire
// process tree under test is the real CLI.
const tuiE2EChildMarker = "DCI_TUI_E2E_MAIN"

// tuiE2EFakeChild ("mode:token") substitutes a scripted child for any
// re-exec whose argv contains token — the only place the process tree
// under test is NOT the real CLI, used for session-side flows whose child
// behavior cannot be produced offline: "stall" is a dispatch that never
// finishes (the Esc-cancel path). Set per session via extraEnv; the
// session's own argv ("ai") never matches a dispatch token.
const tuiE2EFakeChild = "DCI_TUI_E2E_FAKE"

func TestMain(m *testing.M) {
	if os.Getenv(tuiE2EChildMarker) == "1" {
		if mode, token, ok := strings.Cut(os.Getenv(tuiE2EFakeChild), ":"); ok && slices.Contains(os.Args[1:], token) {
			switch mode {
			case "stall":
				select {} // runs until the session cancels the dispatch
			}
		}
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
	t         *testing.T
	cmd       *exec.Cmd
	tty       *os.File
	configDir string
	workDir   string // the session's cwd: where /export's relative paths land
	exitErr   error
	exited    chan struct{}

	mu  sync.Mutex
	raw strings.Builder
}

// tuiSessionConfig shapes one session under test. The zero value is the
// common case: `dci ai`, fresh isolated dirs, no fakes.
type tuiSessionConfig struct {
	args      []string                         // CLI argv after the binary; nil means {"ai"}
	configDir string                           // reuse a config dir across restarts; "" = fresh
	extraEnv  []string                         // e.g. the tuiE2EFakeChild selector
	prepare   func(configDir, cacheDir string) // seed the dirs before launch
	// rawFrame skips the session-frame startup waits, for invocations that
	// do not open the session at all (bare `dci` with /default help).
	rawFrame bool
}

// startTUISession boots the CLI on a fresh pty with isolated config and
// cache dirs and blocks until the whole first frame renders. The
// environment is built from scratch: no inherited credentials, no
// agent-detection markers, no proxy — plus the CLI's own DCI_AGENT_MODE=0
// override, because the test runner itself sits in an agent environment
// the CLI would otherwise rightly detect.
func startTUISession(t *testing.T, cfg tuiSessionConfig) *tuiSession {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configDir := cfg.configDir
	if configDir == "" {
		configDir = t.TempDir()
	}
	cacheDir := t.TempDir()
	if cfg.prepare != nil {
		cfg.prepare(configDir, cacheDir)
	}
	args := cfg.args
	if args == nil {
		args = []string{"ai"}
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TERM=xterm-256color", // no Kitty signal: the logo probe stays off
		tuiE2EChildMarker + "=1",
		"DCI_CONFIG_DIR=" + configDir,
		"DCI_CACHE_DIR=" + cacheDir,
		"DCI_AGENT_MODE=0",
	}, cfg.extraEnv...)
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 140})
	if err != nil {
		t.Fatal(err)
	}
	session := &tuiSession{
		t: t, cmd: cmd, tty: tty,
		configDir: configDir, workDir: cmd.Dir,
		exited: make(chan struct{}),
	}
	go func() {
		session.exitErr = cmd.Wait()
		close(session.exited)
	}()
	go session.pump()
	t.Cleanup(session.stop)
	if cfg.rawFrame {
		return session // not a session frame: the test does its own waiting
	}
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

// seedCachedSpec forges the on-disk artifacts a real login leaves behind:
// restish's CBOR spec cache plus the expiry stamp it validates, shaped
// exactly as cli.Load writes them. This is how the suite reaches the
// logged-out-with-hydrated-catalog state — the state the v2.7.0 "21
// commands" regression corrupted — without any network. The operation
// pair is deliberately resolver-shaped: get-widget takes the single
// trailing path parameter of list-widgets' collection, so the name
// resolution metadata derives a picker target for it.
func seedCachedSpec(t *testing.T, cacheDir string) {
	t.Helper()
	api := cli.API{
		Operations: []cli.Operation{
			{Name: "list-widgets", Short: "List widgets", Method: "GET",
				URITemplate: "https://api.doit.com/analytics/v1/widgets"},
			{Name: "get-widget", Short: "Get one widget", Method: "GET",
				URITemplate: "https://api.doit.com/analytics/v1/widgets/{widgetId}",
				PathParams:  []*cli.Param{{Name: "widgetId", Type: "string"}}},
		},
	}
	blob, err := cbor.Marshal(api)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "dci.cbor"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	expiry := fmt.Sprintf(`{"dci":{"expires":%q}}`+"\n", time.Now().Add(time.Hour).Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(cacheDir, "cache.json"), []byte(expiry), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedWidgetNames writes the resolver's name cache for the fixture's
// widget resource (no customer context), so the session's picker opens
// from disk instead of fetching.
func seedWidgetNames(t *testing.T, configDir string) {
	t.Helper()
	cache := nameCacheFile{
		Version:   nameCacheVersion,
		FetchedAt: time.Now(),
		Resources: map[string][]nameCacheEntry{
			"widgets": {
				{ID: "wProd0000000000000001", Name: "Prod Widget"},
				{ID: "wDev00000000000000002", Name: "Dev Widget"},
			},
		},
	}
	if err := writeNameCache(configDir, cache); err != nil {
		t.Fatal(err)
	}
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
	keyUp    = "\x1b[A"
	keyTab   = "\t"
	keyCtrlC = "\x03"
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
	session := startTUISession(t, tuiSessionConfig{})
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
	session := startTUISession(t, tuiSessionConfig{})
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
	session := startTUISession(t, tuiSessionConfig{})
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
	session := startTUISession(t, tuiSessionConfig{})
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
	session := startTUISession(t, tuiSessionConfig{})
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
	session := startTUISession(t, tuiSessionConfig{prepare: func(configDir, _ string) {
		commands := `{"beta-mine": {"command": "beta", "summary": "saved shadow"}}`
		if err := os.WriteFile(filepath.Join(configDir, aiUserCommandsFileName), []byte(commands), 0o600); err != nil {
			t.Fatal(err)
		}
	}})
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
	session := startTUISession(t, tuiSessionConfig{})
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
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/quit", keyEnter)
	session.expectCleanExit()
}

// expectCleanExit asserts the session ends by itself with status 0.
func (s *tuiSession) expectCleanExit() {
	s.t.Helper()
	select {
	case <-s.exited:
		if s.exitErr != nil {
			s.t.Fatalf("session exited with %v", s.exitErr)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("session did not end")
	}
}

// leaveKeyOnboarding exits the guided key entry that owns the keyboard in
// a fresh keyless session, for tests whose first input is not a slash.
func (s *tuiSession) leaveKeyOnboarding() {
	s.t.Helper()
	s.send(keyEsc)
	s.waitFor("key setup canceled")
}

// Esc during a running dispatch cancels it: the child is killed and the
// run card records the cancellation instead of a result. The child is the
// harness's stall fake — no real command blocks forever offline.
func TestE2EEscCancelsRunningDispatch(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{
		extraEnv: []string{tuiE2EFakeChild + "=stall:run-report"},
	})
	session.send("/beta run-report r1", keyEnter)
	time.Sleep(400 * time.Millisecond) // Enter set the running state; the stalled child never will
	session.send(keyEsc)
	session.waitFor("canceled after")
}

// A bare argument/flag rejection gets the one-line usage appended,
// spelled the session's way. The command has to be chosen with care to
// stay offline: beta commands tolerate unknown flags and relax arity for
// resolvable names, and anything under a resolvable command (all of
// customer-context) is intercepted by the session's name picker — a
// fetch — instead of dispatching. `docs` is a strict, non-resolvable
// custom root leaf, so an unknown flag fails in the child on parsing
// alone.
func TestE2EArgvErrorGetsSessionUsageLine(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/docs --nope", keyEnter)
	session.waitFor("unknown flag: --nope")
	session.waitFor("usage: /docs")
}

// Tab accepts the highlighted completion; the verb that then runs on
// Enter proves which row it was ("be" puts the /bell verb first — verbs
// list before catalog matches). The bell defaults ON, so the first toggle
// lands on off.
func TestE2ETabAcceptsHighlightedCompletion(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/be")
	session.waitFor("Toggle the end-of-turn terminal bell")
	session.send(keyTab, keyEnter)
	session.waitFor("bell off —")
}

// The popup is a window over more matches than it shows: bare "/" lists
// every candidate, and nine ↓ presses walk to /quit — the tenth row, past
// the six visible ones (the v2.6.2 bug made rows beyond the window
// unreachable). Enter accepts it, Enter again submits it: a clean exit is
// the proof the highlight really reached that row.
func TestE2EPopupWindowNavigatesPastVisibleRows(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/")
	session.waitFor("Show or set the customer context") // popup open on the verb rows
	for i := 0; i < 9; i++ {
		session.send(keyDown)
	}
	session.send(keyEnter, keyEnter)
	session.expectCleanExit()
}

// A slash line matching nothing never dispatches: it reports the unknown
// command with did-you-mean suggestions from the catalog.
func TestE2EUnknownCommandSuggests(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/betaa", keyEnter)
	session.waitFor("unknown command: /betaa")
	session.waitFor("did you mean: /beta")
}

// A slash line that does not parse reports the error instead of
// dispatching or falling through to the AI.
func TestE2EUnterminatedQuoteReports(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/beta 'oops", keyEnter)
	session.waitFor("could not parse: unterminated")
}

// /customer persists the context to the config dir, a restarted session
// reads it back into the banner, and the input history survives with it:
// ↑ recalls lines submitted in the previous session.
func TestE2ECustomerAndHistoryPersistAcrossSessions(t *testing.T) {
	configDir := t.TempDir()
	first := startTUISession(t, tuiSessionConfig{configDir: configDir})
	first.send("/customer acme.com", keyEnter)
	first.waitFor("Customer context set to acme.com")
	first.send("/customer", keyEnter)
	first.waitFor("Customer context: acme.com")
	first.stop() // appends its own /quit to the history

	second := startTUISession(t, tuiSessionConfig{configDir: configDir})
	second.waitFor("Tenant: acme.com") // the persisted context is in the banner
	second.leaveKeyOnboarding()
	// History, newest first: /quit, /customer, /customer acme.com.
	second.send(keyUp, keyUp, keyUp, keyEnter)
	second.waitFor("Customer context set to acme.com")
}

// /bell persists: the bell defaults ON, the first session toggles it off
// and saves that, so a session restarted over the same config dir starts
// from OFF and its own toggle lands back on — only a persisted setting
// can produce that order.
func TestE2EBellTogglePersistsAcrossSessions(t *testing.T) {
	configDir := t.TempDir()
	first := startTUISession(t, tuiSessionConfig{configDir: configDir})
	first.send("/bell", keyEnter)
	first.waitFor("bell off —")
	first.stop()

	second := startTUISession(t, tuiSessionConfig{configDir: configDir})
	second.send("/bell", keyEnter)
	second.waitFor("bell on —")
}

// /export writes the transcript to a file, and /clear really empties the
// transcript: a second export after clearing must not carry the earlier
// blocks. File contents are the one place "no longer present" is provable
// (the output stream keeps every frame; the file is a snapshot).
func TestE2EExportWritesTranscriptAndClearDropsIt(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/customer acme.com", keyEnter)
	session.waitFor("Customer context set to acme.com")
	session.send("/export before.txt", keyEnter)
	session.waitFor("Transcript saved to before.txt")
	session.send("/clear", keyEnter)
	session.send("/export after.txt", keyEnter)
	session.waitFor("Transcript saved to after.txt")

	before, err := os.ReadFile(filepath.Join(session.workDir, "before.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), "Customer context set to acme.com") {
		t.Fatal("export before /clear misses the transcript content")
	}
	after, err := os.ReadFile(filepath.Join(session.workDir, "after.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "Customer context set to acme.com") {
		t.Fatal("/clear left earlier transcript blocks behind")
	}
	if !strings.Contains(string(after), "Cloud Intelligence") {
		t.Fatal("the cleared session's export misses the fresh banner")
	}
}

// /logout dispatches a real child that succeeds offline — the one
// successful run card in the suite: output plus a plain elapsed footer,
// no exit code.
func TestE2ELogoutDispatchSucceedsOffline(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/logout", keyEnter)
	session.waitFor("Logged out. Credentials cleared.")
}

// A bracketed paste lands in the input and drives the completion popup,
// same as typing (v2 delivers pastes as their own message type — the
// v2.7.0 port had to route them explicitly).
func TestE2EBracketedPasteFillsInput(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.leaveKeyOnboarding() // a paste during key entry goes to the key buffer
	session.send("\x1b[200~/beta run\x1b[201~")
	session.waitFor("(beta) Run a saved report asynchronously")
}

// ctrl+c on an empty input arms, and a second press quits — the shell
// instinct, without a stray first press killing the session.
func TestE2ECtrlCTwiceQuits(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.leaveKeyOnboarding()
	session.send(keyCtrlC, keyCtrlC)
	session.expectCleanExit()
}

// A resize and a ctrl+l repaint must leave a live, usable frame: the
// popup still opens on the reflowed layout.
func TestE2EResizeAndRedrawSurvive(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	if err := pty.Setsize(session.tty, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // let the WindowSizeMsg land
	session.send("\x0c")               // ctrl+l: erase and repaint
	session.send("/beta")
	session.waitFor("(beta) Cancel an async report run operation")
}

// Bare `dci` at a human terminal opens the session (AI-DEFAULT-SPEC §3):
// the harness's own startup waits assert the banner and first frame.
func TestE2EBareInvocationOpensSession(t *testing.T) {
	startTUISession(t, tuiSessionConfig{args: []string{}})
}

// A cached spec hydrates the API half of the catalog with no login — the
// state the v2.7.0 regression corrupted ("21 commands" no matter what).
// The banner must show the plain count line, the GA operation must
// complete in the popup, and dispatching it logged out must produce the
// session-shaped sign-in error — the exact screenshot-1 report from the
// second feedback round, replayed on a GA command instead of beta.
func TestE2ECachedSpecHydratesCatalogOffline(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{prepare: func(_, cacheDir string) {
		seedCachedSpec(t, cacheDir)
	}})
	session.waitFor("commands · /help for how this works")
	if strings.Contains(session.snapshot(), "API commands appear after /login") {
		t.Fatal("banner shows the cold-cache line despite a warm spec cache")
	}
	session.send("/list-w")
	session.waitFor("List widgets") // the fixture operation completes in the popup
	session.send(keyEnter, keyEnter)
	session.waitFor("dci list-widgets")
	session.waitFor("you're not signed in — run /login to sign in")
	session.waitFor(fmt.Sprintf("exit %d", exitAuthentication))
}

// The zero-argument name picker, from the on-disk name cache: dispatching
// a resolvable command without its argument opens the selection, typing
// filters it, and Enter dispatches with the chosen entry's ID.
func TestE2EPickerSelectsCachedName(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{prepare: func(configDir, cacheDir string) {
		seedCachedSpec(t, cacheDir)
		seedWidgetNames(t, configDir)
	}})
	session.send("/get-widget", keyEnter)
	session.waitFor("Select a widget")
	session.waitFor("Prod Widget")
	session.send("dev") // filter narrows to the Dev entry
	session.send(keyEnter)
	session.waitFor("picked Dev Widget")
	session.waitFor("dci get-widget wDev00000000000000002") // the picked ID dispatched
}

// The /key lifecycle without editing any file by hand (the round-1
// feedback): guided entry saves a key and turns AI on, bare /key names
// the source, /key clear turns AI back off.
func TestE2EKeyManageLifecycle(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/key set", keyEnter)
	session.send("sk-ant-e2e-0123456789", keyEnter)
	session.waitFor("Key saved — AI is ready.")
	session.send("/key", keyEnter)
	session.waitFor("saved in " + aiSettingsFileName)
	session.send("/key clear", keyEnter)
	session.waitFor("Saved key cleared — AI is off.")
}

// /model shows the roster and applies a change live.
func TestE2EModelShowAndSet(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{})
	session.send("/model", keyEnter)
	session.waitFor("Available: " + strings.Join(aiKnownModels, ", "))
	session.send("/model "+aiKnownModels[len(aiKnownModels)-1], keyEnter)
	session.waitFor("Model set to " + aiKnownModels[len(aiKnownModels)-1])
}

// Every way out of the session: the /exit alias, the bare "exit" shell
// instinct (no slash), and ctrl+d on an empty input.
func TestE2EExitPaths(t *testing.T) {
	alias := startTUISession(t, tuiSessionConfig{})
	alias.send("/exit", keyEnter)
	alias.expectCleanExit()

	bare := startTUISession(t, tuiSessionConfig{})
	bare.leaveKeyOnboarding()
	bare.send("exit", keyEnter)
	bare.expectCleanExit()

	eof := startTUISession(t, tuiSessionConfig{})
	eof.leaveKeyOnboarding()
	eof.send("\x04") // ctrl+d
	eof.expectCleanExit()
}

// The full /help block renders only for the first session ever; later
// sessions get it on demand. Seeding history makes this a later session,
// so the block's absence at startup is a valid never-rendered negative.
func TestE2EHelpOnDemandAfterFirstSession(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{prepare: func(configDir, _ string) {
		if err := os.WriteFile(filepath.Join(configDir, aiHistoryFileName), []byte("/status\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	if strings.Contains(session.snapshot(), "How this session works") {
		t.Fatal("a session with history still opened with the first-timer help block")
	}
	session.send("/help", keyEnter)
	session.waitFor("How this session works")
}

// /default help persists the bare-dci opt-out: the next bare invocation
// prints the help screen and exits instead of opening the session.
func TestE2EDefaultHelpRoutesBareInvocation(t *testing.T) {
	configDir := t.TempDir()
	session := startTUISession(t, tuiSessionConfig{configDir: configDir})
	session.send("/default help", keyEnter)
	session.waitFor("bare dci shows the help screen")
	session.stop()

	help := startTUISession(t, tuiSessionConfig{configDir: configDir, args: []string{}, rawFrame: true})
	help.waitFor("(interactive AI session)") // the help screen's own example line
	help.expectCleanExit()
}

// The argument-placeholder ghost (AI-PLACEHOLDER-SPEC P1): accepting a
// completion rewrites the input row in one frame — a one-shot render, so
// the harness rules allow matching it — and that row carries the faint
// signature of the accepted command's remaining arguments. get-widget is
// resolvable against the forged spec's widgets collection, so the path
// slot ghosts as the resource noun, not the raw parameter name.
func TestE2EGhostSignatureAfterAcceptedCompletion(t *testing.T) {
	session := startTUISession(t, tuiSessionConfig{prepare: func(_, cacheDir string) {
		seedCachedSpec(t, cacheDir)
	}})
	session.send("/get-wid")
	session.waitFor("Get one widget")
	session.send(keyTab)
	session.waitFor("widget-name-or-id")
}
