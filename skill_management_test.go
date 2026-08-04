package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
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

func TestInspectInstalledSkillAcceptsFilesFromPreviousManifest(t *testing.T) {
	target := t.TempDir()
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(target, "skills", "dci-cli")
	manifest, exists, err := readSkillManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("skill manifest was not installed")
	}

	previous := []byte("previous release\n")
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), previous, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest.Files["SKILL.md"] = skillFileDigest(previous)
	if err := writeSkillManifest(root, manifest); err != nil {
		t.Fatal(err)
	}

	diff, err := inspectInstalledSkill(target)
	if err != nil {
		t.Fatal(err)
	}
	if diff.HasLocalChanges() {
		t.Fatalf("previous installed version was treated as a local edit: %+v", diff)
	}
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}
	updated, err := inspectInstalledSkill(target)
	if err != nil {
		t.Fatal(err)
	}
	if updated.HasLocalChanges() {
		t.Fatalf("updated skill is dirty: %+v", updated)
	}
}

func TestInspectInstalledSkillDoesNotBlockOnExtraFiles(t *testing.T) {
	target := t.TempDir()
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}
	extraPath := filepath.Join(target, "skills", "dci-cli", ".DS_Store")
	if err := os.WriteFile(extraPath, []byte("metadata"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff, err := inspectInstalledSkill(target)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(diff.Extra, ".DS_Store") {
		t.Fatalf("extra file was not reported: %+v", diff)
	}
	if diff.HasLocalChanges() {
		t.Fatalf("non-overwritten extra file blocked update: %+v", diff)
	}
}

func TestInspectInstalledSkillAcceptsReleasedFilesWithoutManifest(t *testing.T) {
	target := t.TempDir()
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(target, "skills", "dci-cli")
	if err := os.Remove(filepath.Join(root, skillManifestName)); err != nil {
		t.Fatal(err)
	}
	previous := []byte("previous released skill\n")
	previousDigest := skillFileDigest(previous)
	releasedSkillFileDigests["SKILL.md"][previousDigest] = true
	t.Cleanup(func() { delete(releasedSkillFileDigests["SKILL.md"], previousDigest) })
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), previous, 0o644); err != nil {
		t.Fatal(err)
	}

	diff, err := inspectInstalledSkill(target)
	if err != nil {
		t.Fatal(err)
	}
	if diff.HasLocalChanges() {
		t.Fatalf("released manifest-free install was treated as a local edit: %+v", diff)
	}
}

func TestInstallSkillSafelyProtectsAndBacksUpLocalEdits(t *testing.T) {
	target := t.TempDir()
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target, "skills", "dci-cli", "SKILL.md")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := append(append([]byte{}, original...), []byte("\nLOCAL EDIT\n")...)
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := installSkillSafely(target, false); err == nil {
		t.Fatal("expected local-edit protection")
	}
	protected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(protected), "LOCAL EDIT") {
		t.Fatal("local edit was overwritten without force")
	}

	backups, err := installSkillSafely(target, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	if strings.HasPrefix(backups[0], filepath.Join(target, "skills", "dci-cli")+string(filepath.Separator)) {
		t.Fatalf("backup was created inside the managed skill root: %s", backups[0])
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(backup), "LOCAL EDIT") {
		t.Fatal("backup does not contain the local edit")
	}
	installed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(installed), "LOCAL EDIT") {
		t.Fatal("forced update did not restore the embedded file")
	}

	secondEdit := append(append([]byte{}, installed...), []byte("\nSECOND EDIT\n")...)
	if err := os.WriteFile(path, secondEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	secondBackups, err := installSkillSafely(target, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondBackups) != 1 || secondBackups[0] == backups[0] {
		t.Fatalf("repeated force backups = %v, first backups = %v", secondBackups, backups)
	}
	if _, err := os.Stat(backups[0]); err != nil {
		t.Fatalf("first backup was not preserved: %v", err)
	}
	secondBackup, err := os.ReadFile(secondBackups[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secondBackup), "SECOND EDIT") {
		t.Fatal("second backup does not contain the second local edit")
	}
}

func TestSkillForceRequiresAllOrNamedAgent(t *testing.T) {
	setupTestRoot(t)
	registerSkillCommands()
	command, _, err := cli.Root.Find([]string{"skill"})
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}
	err = command.RunE(command, nil)
	if err == nil || !strings.Contains(err.Error(), "--force requires --all or a named agent") {
		t.Fatalf("error = %v", err)
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
