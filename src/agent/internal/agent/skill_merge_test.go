package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockMergeModel struct {
	result *SkillMergeResult
	err    error
}

func (m *mockMergeModel) MergeSkill(_ context.Context, _ SkillMergeInput) (*SkillMergeResult, error) {
	return m.result, m.err
}

func TestMergeWorker_SuccessfulMerge(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(configDir, "skill-state")
	os.MkdirAll(stateDir, 0o755)
	manifestPath := filepath.Join(stateDir, ".bundled_manifest.json")

	userSkillDir := filepath.Join(configDir, "skills", "alpha")
	os.MkdirAll(userSkillDir, 0o755)
	localContent := "---\nname: alpha\ndescription: user version\n---\n\nUser steps.\n"
	os.WriteFile(filepath.Join(userSkillDir, "SKILL.md"), []byte(localContent), 0o644)

	mergedContent := "---\nname: alpha\ndescription: merged version\n---\n\nMerged steps.\n"
	model := &mockMergeModel{
		result: &SkillMergeResult{
			Status:        "merged",
			MergedSkillMD: mergedContent,
			Summary:       "merged ok",
		},
	}

	upstreamContent := "---\nname: alpha\ndescription: upstream\n---\n\nUpstream.\n"
	job := SkillMergeJob{
		SkillName:    "alpha",
		Mode:         MergeThreeWay,
		Base:         localContent,
		Upstream:     upstreamContent,
		Local:        localContent,
		LocalHash:    hashContent([]byte(localContent)),
		UpstreamHash: hashContent([]byte(upstreamContent)),
		MergeKey:     "test-key",
		UserPath:     filepath.Join(userSkillDir, "SKILL.md"),
		BasePath:     filepath.Join(stateDir, "bases", "alpha", "SKILL.md"),
		StateDir:     stateDir,
	}

	// Save initial manifest
	manifest := &BundledManifest{Version: 1, Skills: map[string]ManifestEntry{
		"alpha": {OriginHash: "old", Status: StatusUserModified},
	}}
	saveManifest(manifestPath, manifest)

	worker := NewMergeWorker(model, manifestPath)
	worker.Enqueue([]SkillMergeJob{job})
	worker.Start(context.Background())
	worker.Wait()

	got, err := os.ReadFile(filepath.Join(userSkillDir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != mergedContent {
		t.Fatalf("expected merged content, got %q", string(got))
	}

	m := loadManifest(manifestPath)
	if m.Skills["alpha"].Status != StatusMerged {
		t.Fatalf("expected merged status, got %s", m.Skills["alpha"].Status)
	}
}

func TestMergeWorker_FailedMerge(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(configDir, "skill-state")
	os.MkdirAll(stateDir, 0o755)
	manifestPath := filepath.Join(stateDir, ".bundled_manifest.json")

	userSkillDir := filepath.Join(configDir, "skills", "alpha")
	os.MkdirAll(userSkillDir, 0o755)
	localContent := "---\nname: alpha\ndescription: user\n---\n\nUser.\n"
	os.WriteFile(filepath.Join(userSkillDir, "SKILL.md"), []byte(localContent), 0o644)

	model := &mockMergeModel{
		result: &SkillMergeResult{Status: "failed", Summary: "conflict"},
	}

	manifest := &BundledManifest{Version: 1, Skills: map[string]ManifestEntry{
		"alpha": {OriginHash: "old", Status: StatusUserModified},
	}}
	saveManifest(manifestPath, manifest)

	job := SkillMergeJob{
		SkillName: "alpha",
		Mode:      MergeThreeWay,
		Local:     localContent,
		LocalHash: hashContent([]byte(localContent)),
		MergeKey:  "fail-key",
		UserPath:  filepath.Join(userSkillDir, "SKILL.md"),
		StateDir:  stateDir,
	}

	worker := NewMergeWorker(model, manifestPath)
	worker.Enqueue([]SkillMergeJob{job})
	worker.Start(context.Background())
	worker.Wait()

	// User file should be unchanged
	got, _ := os.ReadFile(filepath.Join(userSkillDir, "SKILL.md"))
	if string(got) != localContent {
		t.Fatal("user file was modified on failed merge")
	}

	m := loadManifest(manifestPath)
	if m.Skills["alpha"].LastFailedMergeKey != "fail-key" {
		t.Fatalf("expected last_failed_merge_key=fail-key, got %s", m.Skills["alpha"].LastFailedMergeKey)
	}
}

func TestMergeWorker_SkipsWhenLocalChanged(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(configDir, "skill-state")
	os.MkdirAll(stateDir, 0o755)
	manifestPath := filepath.Join(stateDir, ".bundled_manifest.json")

	userSkillDir := filepath.Join(configDir, "skills", "alpha")
	os.MkdirAll(userSkillDir, 0o755)
	originalContent := "---\nname: alpha\ndescription: original\n---\n\nOriginal.\n"
	os.WriteFile(filepath.Join(userSkillDir, "SKILL.md"), []byte(originalContent), 0o644)

	mergedContent := "---\nname: alpha\ndescription: merged\n---\n\nMerged.\n"
	model := &mockMergeModel{
		result: &SkillMergeResult{Status: "merged", MergedSkillMD: mergedContent},
	}

	// Use a WRONG local hash to simulate the file changing during merge
	job := SkillMergeJob{
		SkillName: "alpha",
		Mode:      MergeThreeWay,
		Local:     originalContent,
		LocalHash: "sha256:wrong-hash",
		MergeKey:  "stale-key",
		UserPath:  filepath.Join(userSkillDir, "SKILL.md"),
		StateDir:  stateDir,
	}

	manifest := &BundledManifest{Version: 1, Skills: map[string]ManifestEntry{
		"alpha": {OriginHash: "old", Status: StatusUserModified},
	}}
	saveManifest(manifestPath, manifest)

	worker := NewMergeWorker(model, manifestPath)
	worker.Enqueue([]SkillMergeJob{job})
	worker.Start(context.Background())
	worker.Wait()

	got, _ := os.ReadFile(filepath.Join(userSkillDir, "SKILL.md"))
	if string(got) != originalContent {
		t.Fatal("file was overwritten despite local hash mismatch")
	}
}

func TestSkillListTool(t *testing.T) {
	dir := t.TempDir()
	writeSKILL(t, dir, "alpha", testSkillA)
	writeSKILL(t, dir, "beta", testSkillB)

	tool := NewSkillListTool(dir)
	result, err := tool.Call(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "alpha") || !strings.Contains(result, "beta") {
		t.Fatalf("expected both skills in list, got %s", result)
	}

	// Filter
	result, _ = tool.Call(context.Background(), "alpha")
	if !strings.Contains(result, "alpha") {
		t.Fatal("filter should return alpha")
	}
	if strings.Contains(result, "beta") {
		t.Fatal("filter should not return beta")
	}
}

func TestSkillManageTool_CreateAndPatch(t *testing.T) {
	dir := t.TempDir()
	tool := NewSkillManageTool(dir, "")

	// Create
	content := "---\nname: foo\ndescription: Foo skill\n---\n\nDo foo.\n"
	input := `{"action":"create","name":"foo","content":"---\nname: foo\ndescription: Foo skill\n---\n\nDo foo.\n","reason":"test"}`
	result, err := tool.Call(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Created") {
		t.Fatalf("unexpected result: %s", result)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "foo", "SKILL.md"))
	if string(got) != content {
		t.Fatalf("content mismatch: %q", string(got))
	}

	// Patch
	patchInput := `{"action":"patch","name":"foo","old_string":"Do foo.","new_string":"Do foo better.","reason":"improve"}`
	result, err = tool.Call(context.Background(), patchInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Patched") {
		t.Fatalf("unexpected result: %s", result)
	}

	got, _ = os.ReadFile(filepath.Join(dir, "foo", "SKILL.md"))
	if !strings.Contains(string(got), "Do foo better.") {
		t.Fatal("patch not applied")
	}
}
