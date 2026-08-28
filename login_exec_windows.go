//go:build windows

package main

import "errors"

// replaceProcess is unsupported on Windows (no execve); the caller falls
// back to running the command as a child process.
func replaceProcess(path string, argv []string, env []string) error {
	return errors.ErrUnsupported
}
