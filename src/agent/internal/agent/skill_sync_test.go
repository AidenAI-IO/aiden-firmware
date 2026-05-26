package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeSKILL(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSKILL(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const testSkillA = `---
name: alpha
description: Alpha skill
---

Do alpha things.
`

const testSkillAv2 = `---
name: alpha
description: Alpha skill v2
---

Do alpha things better.
`

const testSkillB = `---
name: beta
description: Beta skill
---

Do beta things.
`

func TestSyncBundledSkills_NewSkillCopied(t *testing.T) {
	configDir := t.TempDir()
	bundledDir := t.TempDir()
	writeSKILL(t, bundledDir, "alpha", testSkillA)

	report, err := SyncBundledSkills(context.Background(), SkillSyncOptions{
		ConfigDir:        configDir,
		BundledSkillsDir: bundledDir,
		Quiet:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Copied) != 1 || report.Copied[0] != "alpha" {
		t.Fatalf("expected copied=[alpha], got %v", report.Copied)
	}

	got := readSKILL(t, filepath.Join(configDir, "skills"), "alpha")
	if got != testSkillA {
		t.Fatalf("user skill content mismatch")
	}

	manifest := loadManifest(filepath.Join(configDir, "skill-state", ".bundled_manifest.json"))
	entry := manifest.Skills["alpha"]
	if entry.Status != StatusSynced {
		t.Fatalf("expected status synced, got %s", entry.Status)
	}
	if entry.OriginHash == "" {
		t.Fatal("expected non-empty origin_hash")
	}
}

func TestSyncBundledSkills_UpdateUnmodified(t *testing.T) {
	configDir := t.TempDir()
	bundledDir := t.TempDir()
	writeSKILL(t, bundledDir, "alpha", testSkillA)

	SyncBundledSkills(context.Background(), SkillSyncOptions{
		ConfigDir: configDir, BundledSkillsDir: bundledDir, Quiet: true,
	})

	// Bundled updates
	writeSKILL(t, bundledDir, "alpha", testSkillAv2)
	report, _ := SyncBundledSkills(context.Background(), SkillSyncOptions{
		ConfigDir: configDir, BundledSkillsDir: bundledDir, Quiet: true,
	})

	if len(report.Updated) != 1 {
		t.Fatalf("expected updated=[alpha], got %v", report.Updated)
	}
	got := readSKILL(t, filepath.Join(configDir, "skills"), "alpha")
	if got != testSkillAv2 {
		t.Fatalf("expected v2 content")
	}
}

func TestSyncBundledSkills_KeepUserModified(t *testing.T) {
	configDir := t.TempDir()
	bundledDir := t.TempDir()
	writeSKILL(t, bundledDir, "alpha", testSkillA)

	SyncBundledSkills(context.Background(), SkillSyncOptions{
		ConfigDir: configDir, BundledSkillsDir: bundledDir, Quiet: true,
	})

	// User modifies the skill
	userModified := "---\nname: alpha\ndescription: My alpha\n---\n\nCustom.\n"
	writeSKILL(t, filepath.Join(configDir, "skills"), "alpha", userModified)

	report, _ := SyncBundledSkills(context.Background(), SkillSyncOptions{
		ConfigDir: configDir, BundledSkillsDir: bundledDir, Quiet: true,
	})

	if len(report.KeptUser) != 1 {
		t.Fatalf("expected kept_user=[alpha], got %v", report.KeptUser)
	}
	got := readSKILL(t, filepath.Join(configDir, "skills"), "alpha")
	if got != userModified {
		t.Fatalf("user modification was overwritten")
	}
}

func TestSyncBundledSkills_DeletedByUser(t *testing.T) {
	configDir := t.TempDir()
	bundledDir := t.TempDir()
	writeSKILL(t, bundledDir, "alpha", testSkillA)

	SyncBundledSkills(context.Background(), SkillSyncOptions{
		ConfigDir: configDir, BundledSkillsDir: bundledDir, Quiet: true,
	})

	// User deletes the skill
	os.RemoveAll(filepath.Join(configDir, "skills", "alpha"))

	report, _ := SyncBundledSkills(context.Background(), SkillSyncOptions{
		ConfigDir: configDir, BundledSkillsDir: bundledDir, Quiet: true,
	})

	if len(report.DeletedByUser) != 1 {
		t.Fatalf("expected deleted_by_user=[alpha], got %v", report.DeletedByUser)
	}
	// Should not re-copy
	if fileExists(filepath.Join(configDir, "skills", "alpha", "SKILL.md")) {
		t.Fatal("deleted skill was re-copied")
	}
}

func TestSyncBundledSkills_StaleManifestCleaned(t *testing.T) {
	configDir := t.TempDir()
	bundledDir := t.TempDir()
	writeSKILL(t, bundledDir, "alpha", testSkillA)
	writeSKILL(t, bundledDir, "beta", testSkillB)

	SyncBundledSkills(context.Background(), SkillSyncOptions{
		ConfigDir: configDir, BundledSkillsDir: bundledDir, Quiet: true,
	})

	// Remove beta from bundled (simulating firmware removing a skill)
	os.RemoveAll(filepath.Join(bundledDir, "beta"))

	report, _ := SyncBundledSkills(context.Background(), SkillSyncOptions{
		ConfigDir: configDir, BundledSkillsDir: bundledDir, Quiet: true,
	})

	if len(report.Stale) != 1 || report.Stale[0] != "beta" {
		t.Fatalf("expected stale=[beta], got %v", report.Stale)
	}
	manifest := loadManifest(filepath.Join(configDir, "skill-state", ".bundled_manifest.json"))
	if _, ok := manifest.Skills["beta"]; ok {
		t.Fatal("stale entry not cleaned from manifest")
	}
}
