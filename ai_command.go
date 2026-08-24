package main

// P1 of AI-SPEC: the `dci ai` cobra wiring. Hidden while the mode is doer
// dogfood (it unhides in P3 per the spec's phase plan); the natural-language
// path and the one-shot form `dci ai "question"` arrive in P2, so arguments
// are rejected with a pointer at what works today. Kept in a sibling file per
// the AGENTS.md chapter-split guidance.

import (
	"errors"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

func registerAICommand(configDir string) {
	command := &cobra.Command{
		Use:    "ai",
		Short:  "Interactive session: run commands and ask questions (preview)",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) > 0 {
				return errors.New("natural-language questions are not available yet — run dci ai with no arguments to open the interactive session")
			}
			if !tuiActive() {
				return errors.New("dci ai needs an interactive terminal")
			}
			return runAISession(configDir)
		},
	}
	cli.Root.AddCommand(command)
}
