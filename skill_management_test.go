package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedSkillFilesIncludesTokenEstimates(t *testing.T) {
	files, err := embeddedSkillFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("expected embedded skill files")
	}
	for _, file := range files {
		if file.Bytes <= 0 || file.EstimatedTokens <= 0 {
			t.Fatalf("invalid file estimate: %+v", file)
		}
		if file.EstimatedTokens != (file.Bytes+3)/4 {
			t.Fatalf("estimate for %s = %d", file.Path, file.EstimatedTokens)
		}
	}
}

func TestInspectInstalledSkillDetectsLocalChanges(t *testing.T) {
	target := t.TempDir()
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}

	clean, err := inspectInstalledSkill(target)
	if err != nil {
		t.Fatal(err)
	}
	if clean.HasLocalChanges() || len(clean.Missing) != 0 {
		t.Fatalf("fresh install is dirty: %+v", clean)
	}

	changedPath := filepath.Join(target, "skills", "dci-cli", "SKILL.md")
	if err := os.WriteFile(changedPath, []byte("locally edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extraPath := filepath.Join(target, "skills", "dci-cli", "local.md")
	if err := os.WriteFile(extraPath, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff, err := inspectInstalledSkill(target)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.HasLocalChanges() {
		t.Fatal("expected local changes")
	}
	if !containsString(diff.Changed, "SKILL.md") || !containsString(diff.Extra, "local.md") {
		t.Fatalf("unexpected diff: %+v", diff)
	}
}

func TestInspectInstalledSkillTreatsMissingFileAsLocalChange(t *testing.T) {
	target := t.TempDir()
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}

	missingPath := filepath.Join(target, "skills", "dci-cli", "SKILL.md")
	if err := os.Remove(missingPath); err != nil {
		t.Fatal(err)
	}

	diff, err := inspectInstalledSkill(target)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.HasLocalChanges() {
		t.Fatal("missing embedded file was not treated as a local change")
	}
	if !containsString(diff.Missing, "SKILL.md") || !containsString(diff.LocalChangePaths(), "SKILL.md") {
		t.Fatalf("unexpected diff: %+v", diff)
	}
}

func TestSkillUpdateTargetsSupportsCustomDirectory(t *testing.T) {
	target := t.TempDir()
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}

	targets, err := skillUpdateTargets([]string{"codex"}, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Agent.Name != "codex" || targets[0].Path != target {
		t.Fatalf("unexpected targets: %+v", targets)
	}
}

func TestSkillUpdateTargetsRejectsUnknownAgent(t *testing.T) {
	_, err := skillUpdateTargets([]string{"unknown"}, "")
	if err == nil || !strings.Contains(err.Error(), "invalid argument") {
		t.Fatalf("error = %v", err)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
