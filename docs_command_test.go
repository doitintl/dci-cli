package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocsCommandListsHumanAndAgentEntryPoints(t *testing.T) {
	var output bytes.Buffer
	command := newDocsCommand()
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatalf("docs command failed: %v", err)
	}

	for _, expected := range []string{
		"https://help.doit.com/docs/cli",
		"https://help.doit.com/docs/cli.md",
		"https://help.doit.com/docs/cli/generated/command-groups/",
		"https://developer.doit.com/",
		"https://help.doit.com/llms.txt",
		"https://help.doit.com/llms-full.txt",
		"dci skill",
		"dci commands --json",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("docs output missing %q", expected)
		}
	}
}

func TestDocsCommandRejectsArguments(t *testing.T) {
	command := newDocsCommand()
	command.SetArgs([]string{"unexpected"})
	if err := command.Execute(); err == nil {
		t.Fatal("docs command accepted an argument")
	}
}

func TestAgentOnboardingHintShowsOnce(t *testing.T) {
	oldAgentMode := agentMode
	oldEnvDetected := agentEnvDetected
	agentMode = true
	agentEnvDetected = "TEST_AGENT"
	t.Cleanup(func() {
		agentMode = oldAgentMode
		agentEnvDetected = oldEnvDetected
	})

	configDir := t.TempDir()

	captureStderr := func(fn func()) string {
		old := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w
		fn()
		if err := w.Close(); err != nil {
			t.Fatalf("close stderr writer: %v", err)
		}
		os.Stderr = old
		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		return string(buf[:n])
	}

	first := captureStderr(func() { maybeAgentOnboardingHint(configDir) })
	if !strings.Contains(first, "dci skill") || !strings.Contains(first, "llms.txt") {
		t.Fatalf("first hint = %q, want skill and llms.txt pointers", first)
	}
	if _, err := os.Stat(filepath.Join(configDir, "agent_onboarding_shown")); err != nil {
		t.Fatalf("marker file not written: %v", err)
	}

	second := captureStderr(func() { maybeAgentOnboardingHint(configDir) })
	if second != "" {
		t.Fatalf("second run printed %q, want silence", second)
	}
}

func TestAgentOnboardingHintSkippedInHumanMode(t *testing.T) {
	oldAgentMode := agentMode
	oldEnvDetected := agentEnvDetected
	agentMode = false
	agentEnvDetected = ""
	t.Cleanup(func() {
		agentMode = oldAgentMode
		agentEnvDetected = oldEnvDetected
	})

	configDir := t.TempDir()
	maybeAgentOnboardingHint(configDir)
	if _, err := os.Stat(filepath.Join(configDir, "agent_onboarding_shown")); err == nil {
		t.Fatal("marker written in human mode")
	}
}
