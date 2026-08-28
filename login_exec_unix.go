//go:build !windows

package main

import "syscall"

// replaceProcess swaps this process image for the given command (execve).
// On success it never returns: every goroutine of the old image dies with
// it — including Token()'s manual-code reader still parked in a read on the
// shared terminal, which is exactly why the login flow's click-to-run
// suggestion must exec instead of spawning a child (see
// execLoginRunSuggestion).
func replaceProcess(path string, argv []string, env []string) error {
	return syscall.Exec(path, argv, env)
}
