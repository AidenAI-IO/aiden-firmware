package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillsFromDirsSkipsMalformedSkills(t *testing.T) {
	dir := t.TempDir()

	// Valid skill
	writeSKILL(t, dir, "valid", "---\nname: valid\ndescription: A valid skill\n---\n\nInstructions.")

	// Missing frontmatter delimiter
	writeSKILL(t, dir, "no-frontmatter", "name: no-frontmatter\ndescription: Missing delimiters\n\nInstructions.")

	// Missing name field
	writeSKILL(t, dir, "no-name", "---\ndescription: Missing name field\n---\n\nInstructions.")

	// Missing description field
	writeSKILL(t, dir, "no-desc", "---\nname: no-desc\n---\n\nInstructions.")

	// Invalid YAML
	writeSKILL(t, dir, "bad-yaml", "---\nname: bad-yaml\ndescription: [\n---\n\nInstructions.")

	index, err := LoadSkillsFromDirs([]string{dir})
	if err != nil {
		t.Fatalf("LoadSkillsFromDirs() failed: %v", err)
	}

	// Only the valid skill should be loaded
	if len(index.skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %v", len(index.skills), index.Names())
	}

	skill, ok := index.Get("valid")
	if !ok {
		t.Fatalf("expected 'valid' skill to be loaded")
	}
	if skill.Description != "A valid skill" {
		t.Errorf("skill description = %q, want %q", skill.Description, "A valid skill")
	}
}

func TestLoadSkillsFromDirsHandlesEmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	index, err := LoadSkillsFromDirs([]string{dir})
	if err != nil {
		t.Fatalf("LoadSkillsFromDirs() on empty dir failed: %v", err)
	}

	if len(index.skills) != 0 {
		t.Errorf("expected 0 skills in empty directory, got %d", len(index.skills))
	}
}

func TestLoadSkillsFromDirsHandlesNonexistentDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")

	_, err := LoadSkillsFromDirs([]string{dir})
	if err == nil {
		t.Fatal("expected error for nonexistent directory, got nil")
	}
}

func TestLoadSkillMetadataExtractsAllFields(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: test-skill
description: Test description
source: bundled
created_by: system
metadata:
  preferred_model: primary
  allowed_tools: [tool1, tool2]
  allowed_children: [child1]
  device_types: [android, iOS, android]
---

Skill instructions here.
More lines.
`
	writeSKILL(t, dir, "test-skill", content)

	skill, err := loadSkillMetadata(filepath.Join(dir, "test-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("loadSkillMetadata() failed: %v", err)
	}

	if skill.Name != "test-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "test-skill")
	}
	if skill.Description != "Test description" {
		t.Errorf("Description = %q, want %q", skill.Description, "Test description")
	}
	if skill.Source != "bundled" {
		t.Errorf("Source = %q, want %q", skill.Source, "bundled")
	}
	if skill.CreatedBy != "system" {
		t.Errorf("CreatedBy = %q, want %q", skill.CreatedBy, "system")
	}
	if skill.PreferredModel != "primary" {
		t.Errorf("PreferredModel = %q, want %q", skill.PreferredModel, "primary")
	}
	if len(skill.AllowedTools) != 2 || skill.AllowedTools[0] != "tool1" || skill.AllowedTools[1] != "tool2" {
		t.Errorf("AllowedTools = %v, want [tool1 tool2]", skill.AllowedTools)
	}
	if len(skill.AllowedChildren) != 1 || skill.AllowedChildren[0] != "child1" {
		t.Errorf("AllowedChildren = %v, want [child1]", skill.AllowedChildren)
	}
	if len(skill.DeviceTypes) != 2 || skill.DeviceTypes[0] != "Android" || skill.DeviceTypes[1] != "iOS" {
		t.Errorf("DeviceTypes = %v, want [Android iOS]", skill.DeviceTypes)
	}
	expectedInstructions := "Skill instructions here.\nMore lines."
	if skill.Instructions != expectedInstructions {
		t.Errorf("Instructions = %q, want %q", skill.Instructions, expectedInstructions)
	}
}

func TestLoadSkillMetadataRejectsInvalidDeviceTypes(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: test-skill
description: Test description
metadata:
  device_types: [Android, beos]
---

Skill instructions.
`
	writeSKILL(t, dir, "test-skill", content)

	_, err := loadSkillMetadata(filepath.Join(dir, "test-skill", "SKILL.md"))
	if err == nil {
		t.Fatal("expected invalid device_types to fail")
	}
	if !strings.Contains(err.Error(), "invalid metadata.device_types value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseFrontmatterHandlesCRLF(t *testing.T) {
	content := "---\r\nname: test\r\ndescription: Test\r\n---\r\n\r\nBody text."

	frontmatter, body, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parseFrontmatter() failed: %v", err)
	}

	// The parser strips the trailing \r from frontmatter when finding \n---\r\n
	if frontmatter != "name: test\r\ndescription: Test\r" {
		t.Errorf("frontmatter = %q, unexpected", frontmatter)
	}
	// Body starts with \r\n because the parser consumes \n---\r\n (endLen=6)
	if body != "\r\nBody text." {
		t.Errorf("body = %q, want %q", body, "\r\nBody text.")
	}
}

func TestParseSkillFromContent(t *testing.T) {
	content := "---\nname: inline\ndescription: Inline skill\n---\n\nInline instructions."

	skill, err := parseSkillFromContent(content)
	if err != nil {
		t.Fatalf("parseSkillFromContent() failed: %v", err)
	}

	if skill.Name != "inline" {
		t.Errorf("Name = %q, want %q", skill.Name, "inline")
	}
	if skill.Instructions != "Inline instructions." {
		t.Errorf("Instructions = %q, want %q", skill.Instructions, "Inline instructions.")
	}
	if skill.FilePath != "" {
		t.Errorf("FilePath = %q, want empty", skill.FilePath)
	}
}

func TestSkillIndexGet(t *testing.T) {
	index := NewSkillIndex()
	index.skills["test"] = &SkillDefinition{Name: "test", Description: "Test skill"}

	skill, ok := index.Get("test")
	if !ok {
		t.Fatal("Get() returned false for existing skill")
	}
	if skill.Name != "test" {
		t.Errorf("skill.Name = %q, want %q", skill.Name, "test")
	}

	_, ok = index.Get("nonexistent")
	if ok {
		t.Error("Get() returned true for nonexistent skill")
	}
}

func TestSkillIndexNames(t *testing.T) {
	index := NewSkillIndex()
	index.skills["alpha"] = &SkillDefinition{Name: "alpha"}
	index.skills["beta"] = &SkillDefinition{Name: "beta"}

	names := index.Names()
	if len(names) != 2 {
		t.Fatalf("Names() returned %d names, want 2", len(names))
	}

	nameSet := make(map[string]bool)
	for _, name := range names {
		nameSet[name] = true
	}
	if !nameSet["alpha"] || !nameSet["beta"] {
		t.Errorf("Names() = %v, want [alpha beta]", names)
	}
}

func TestLoadSkillsFromDirsSkipsDuplicates(t *testing.T) {
	dir := t.TempDir()

	// Create two subdirectories with skills of the same name
	subdir1 := filepath.Join(dir, "set1")
	subdir2 := filepath.Join(dir, "set2")
	os.MkdirAll(subdir1, 0o755)
	os.MkdirAll(subdir2, 0o755)

	writeSKILL(t, subdir1, "duplicate", "---\nname: duplicate\ndescription: First\n---\n\nFirst.")
	writeSKILL(t, subdir2, "duplicate", "---\nname: duplicate\ndescription: Second\n---\n\nSecond.")

	index, err := LoadSkillsFromDirs([]string{subdir1, subdir2})
	if err != nil {
		t.Fatalf("LoadSkillsFromDirs() failed: %v", err)
	}

	if len(index.skills) != 1 {
		t.Fatalf("expected 1 skill after dedup, got %d", len(index.skills))
	}

	skill, ok := index.Get("duplicate")
	if !ok {
		t.Fatal("expected 'duplicate' skill")
	}
	// Should keep the first one
	if skill.Description != "First" {
		t.Errorf("skill description = %q, want %q (should keep first)", skill.Description, "First")
	}
}
