package main

import (
	"bytes"
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
