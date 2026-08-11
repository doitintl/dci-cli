package main

import (
	"fmt"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

var docsEntryPoints = [][2]string{
	{"CLI guide", "https://help.doit.com/docs/cli"},
	{"CLI guide (Markdown)", "https://help.doit.com/docs/cli.md"},
	{"Command reference", "https://help.doit.com/docs/cli/generated/command-groups/"},
	{"API reference", "https://developer.doit.com/"},
	{"Agent documentation index", "https://help.doit.com/llms.txt"},
	{"Agent documentation corpus", "https://help.doit.com/llms-full.txt"},
	{"Embedded agent guidance", "dci skill <claude|codex|cursor|gemini|kiro|opencode>"},
	{"Machine-readable commands", "dci commands --json"},
}

func newDocsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "docs",
		Short: "Print documentation entry points",
		Args:  cobra.NoArgs,
		Run: func(command *cobra.Command, _ []string) {
			for _, entry := range docsEntryPoints {
				fmt.Fprintf(command.OutOrStdout(), "%-30s %s\n", entry[0]+":", entry[1])
			}
		},
	}
}

func registerDocsCommand() {
	cli.Root.AddCommand(newDocsCommand())
}
