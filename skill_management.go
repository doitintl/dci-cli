package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

const embeddedSkillRoot = "skills/dci-cli"

type skillAgent struct {
	Name        string
	RelativeDir string
}

var skillAgents = []skillAgent{
	{Name: "claude", RelativeDir: ".claude"},
	{Name: "codex", RelativeDir: ".codex"},
	{Name: "kiro", RelativeDir: ".kiro"},
	{Name: "gemini", RelativeDir: ".gemini"},
	{Name: "opencode", RelativeDir: ".config/opencode"},
}

type skillFileInfo struct {
	Path            string `json:"path"`
	Bytes           int    `json:"bytes"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

type skillDiff struct {
	Changed []string
	Missing []string
	Extra   []string
}

func installSkill(targetDir string) error {
	destinationRoot := filepath.Join(targetDir, "skills", "dci-cli")
	return fs.WalkDir(skillFS, embeddedSkillRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(embeddedSkillRoot, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(destinationRoot, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := skillFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o644)
	})
}

func embeddedSkillFiles() ([]skillFileInfo, error) {
	files := make([]skillFileInfo, 0)
	err := fs.WalkDir(skillFS, embeddedSkillRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := skillFS.ReadFile(path)
		if err != nil {
			return err
		}
		relativePath, err := filepath.Rel(embeddedSkillRoot, path)
		if err != nil {
			return err
		}
		files = append(files, skillFileInfo{
			Path:            filepath.ToSlash(relativePath),
			Bytes:           len(data),
			EstimatedTokens: (len(data) + 3) / 4,
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

func inspectInstalledSkill(targetDir string) (skillDiff, error) {
	root := filepath.Join(targetDir, "skills", "dci-cli")
	embedded := map[string][]byte{}
	err := fs.WalkDir(skillFS, embeddedSkillRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relativePath, err := filepath.Rel(embeddedSkillRoot, path)
		if err != nil {
			return err
		}
		data, err := skillFS.ReadFile(path)
		if err != nil {
			return err
		}
		embedded[filepath.Clean(relativePath)] = data
		return nil
	})
	if err != nil {
		return skillDiff{}, err
	}

	result := skillDiff{}
	for relativePath, expected := range embedded {
		actual, readErr := os.ReadFile(filepath.Join(root, relativePath))
		if os.IsNotExist(readErr) {
			result.Missing = append(result.Missing, filepath.ToSlash(relativePath))
			continue
		}
		if readErr != nil {
			return skillDiff{}, readErr
		}
		if !bytes.Equal(actual, expected) {
			result.Changed = append(result.Changed, filepath.ToSlash(relativePath))
		}
	}

	if _, statErr := os.Stat(root); statErr == nil {
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			relativePath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relativePath = filepath.Clean(relativePath)
			if _, exists := embedded[relativePath]; !exists {
				result.Extra = append(result.Extra, filepath.ToSlash(relativePath))
			}
			return nil
		})
		if err != nil {
			return skillDiff{}, err
		}
	}

	for _, values := range [][]string{result.Changed, result.Missing, result.Extra} {
		sort.Strings(values)
	}
	return result, nil
}

func (diff skillDiff) HasLocalChanges() bool {
	return len(diff.Changed) > 0 || len(diff.Missing) > 0 || len(diff.Extra) > 0
}

func (diff skillDiff) LocalChangePaths() []string {
	paths := append([]string{}, diff.Changed...)
	paths = append(paths, diff.Missing...)
	paths = append(paths, diff.Extra...)
	sort.Strings(paths)
	return paths
}

func skillAgentByName(name string) (skillAgent, bool) {
	for _, agent := range skillAgents {
		if agent.Name == name {
			return agent, true
		}
	}
	return skillAgent{}, false
}

func defaultSkillTarget(agent skillAgent) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, agent.RelativeDir), nil
}

func resolvedSkillTarget(agent skillAgent, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return filepath.Clean(override), nil
	}
	return defaultSkillTarget(agent)
}

func detectedSkillTargets(installedOnly bool) ([]struct {
	Agent skillAgent
	Path  string
}, error) {
	result := make([]struct {
		Agent skillAgent
		Path  string
	}, 0)
	for _, agent := range skillAgents {
		path, err := defaultSkillTarget(agent)
		if err != nil {
			return nil, err
		}
		probe := path
		if installedOnly {
			probe = filepath.Join(path, "skills", "dci-cli")
		}
		if info, statErr := os.Stat(probe); statErr == nil && info.IsDir() {
			result = append(result, struct {
				Agent skillAgent
				Path  string
			}{Agent: agent, Path: path})
		}
	}
	return result, nil
}

func registerSkillCommands() {
	var installAll bool
	skillCommand := &cobra.Command{
		Use:   "skill",
		Short: "Manage the dci skill for AI agents",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if !installAll {
				return command.Help()
			}
			targets, err := detectedSkillTargets(false)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				return errorsForNoDetectedAgents()
			}
			for _, target := range targets {
				if err := installSkill(target.Path); err != nil {
					return fmt.Errorf("install %s skill: %w", target.Agent.Name, err)
				}
				fmt.Fprintf(os.Stdout, "Skill installed for %s at %s\n", target.Agent.Name, filepath.Join(target.Path, "skills", "dci-cli"))
			}
			return nil
		},
	}
	skillCommand.Flags().BoolVar(&installAll, "all", false, "Install into every detected agent directory")

	for _, configuredAgent := range skillAgents {
		agent := configuredAgent
		var targetOverride string
		agentCommand := &cobra.Command{
			Use:   agent.Name,
			Short: fmt.Sprintf("Install skill for %s", agent.Name),
			Args:  cobra.NoArgs,
			RunE: func(command *cobra.Command, args []string) error {
				target, err := resolvedSkillTarget(agent, targetOverride)
				if err != nil {
					return err
				}
				if err := installSkill(target); err != nil {
					return fmt.Errorf("install %s skill: %w", agent.Name, err)
				}
				fmt.Fprintf(os.Stdout, "Skill installed to %s\n", filepath.Join(target, "skills", "dci-cli"))
				return nil
			},
		}
		agentCommand.Flags().StringVar(&targetOverride, "dir", "", "Override the agent configuration directory")
		skillCommand.AddCommand(agentCommand)
	}

	skillCommand.AddCommand(newSkillListCommand())
	skillCommand.AddCommand(newSkillUpdateCommand())
	cli.Root.AddCommand(skillCommand)
}

func newSkillListCommand() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List embedded skill files and token estimates",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			files, err := embeddedSkillFiles()
			if err != nil {
				return err
			}
			if jsonOutput || agentMode {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"files": files})
			}
			fmt.Fprintln(os.Stdout, "PATH\tBYTES\tESTIMATED TOKENS")
			for _, file := range files {
				fmt.Fprintf(os.Stdout, "%s\t%d\t%d\n", file.Path, file.Bytes, file.EstimatedTokens)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "Emit JSON")
	return command
}

func newSkillUpdateCommand() *cobra.Command {
	var targetOverride string
	var force bool
	command := &cobra.Command{
		Use:   "update [agent]",
		Short: "Refresh installed skill files from this CLI version",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			targets, err := skillUpdateTargets(args, targetOverride)
			if err != nil {
				return err
			}
			for _, target := range targets {
				diff, err := inspectInstalledSkill(target.Path)
				if err != nil {
					return err
				}
				if diff.HasLocalChanges() && !force {
					return fmt.Errorf("installed %s skill has local changes to %s; inspect them and re-run with --force to overwrite", target.Agent.Name, strings.Join(diff.LocalChangePaths(), ", "))
				}
				if err := installSkill(target.Path); err != nil {
					return fmt.Errorf("update %s skill: %w", target.Agent.Name, err)
				}
				fmt.Fprintf(os.Stdout, "Skill updated for %s at %s\n", target.Agent.Name, filepath.Join(target.Path, "skills", "dci-cli"))
			}
			return nil
		},
	}
	command.Flags().StringVar(&targetOverride, "dir", "", "Override the agent configuration directory")
	command.Flags().BoolVar(&force, "force", false, "Overwrite locally edited skill files")
	return command
}

func skillUpdateTargets(args []string, targetOverride string) ([]struct {
	Agent skillAgent
	Path  string
}, error) {
	if len(args) == 0 {
		if targetOverride != "" {
			return nil, errorsForInvalidAgentArgument("--dir requires an agent argument")
		}
		targets, err := detectedSkillTargets(true)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 {
			return nil, errorsForNoDetectedAgents()
		}
		return targets, nil
	}
	agent, exists := skillAgentByName(args[0])
	if !exists {
		return nil, errorsForInvalidAgentArgument(fmt.Sprintf("unknown agent %q", args[0]))
	}
	target, err := resolvedSkillTarget(agent, targetOverride)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(filepath.Join(target, "skills", "dci-cli")); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, fmt.Errorf("skill is not installed for %s; run dci skill %s first", agent.Name, agent.Name)
		}
		return nil, statErr
	}
	return []struct {
		Agent skillAgent
		Path  string
	}{{Agent: agent, Path: target}}, nil
}

func errorsForNoDetectedAgents() error {
	return errorsForInvalidAgentArgument("no supported agent directories were detected")
}

func errorsForInvalidAgentArgument(message string) error {
	return fmt.Errorf("invalid argument: %s", message)
}
