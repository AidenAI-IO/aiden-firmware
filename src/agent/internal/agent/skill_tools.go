package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SkillListTool struct {
	skillsDir string
}

func NewSkillListTool(skillsDir string) *SkillListTool {
	return &SkillListTool{skillsDir: skillsDir}
}

func (t *SkillListTool) Name() string { return "skill_list" }

func (t *SkillListTool) Description() string {
	return "List installed skills from the user skills directory. Input: optional search query string to filter by name/description."
}

func (t *SkillListTool) Call(_ context.Context, input string) (string, error) {
	query := strings.TrimSpace(strings.ToLower(input))
	entries, err := os.ReadDir(t.skillsDir)
	if err != nil {
		return "[]", nil
	}

	type skillInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	var results []skillInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(t.skillsDir, e.Name(), "SKILL.md")
		skill, err := loadSkillMetadata(path)
		if err != nil {
			continue
		}
		if query != "" {
			if !strings.Contains(strings.ToLower(skill.Name), query) &&
				!strings.Contains(strings.ToLower(skill.Description), query) {
				continue
			}
		}
		results = append(results, skillInfo{
			Name:        skill.Name,
			Description: skill.Description,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})
	data, _ := json.Marshal(results)
	return string(data), nil
}

type SkillReadTool struct {
	skillsDir string
}

func NewSkillReadTool(skillsDir string) *SkillReadTool {
	return &SkillReadTool{skillsDir: skillsDir}
}

func (t *SkillReadTool) Name() string { return "skill_read" }

func (t *SkillReadTool) Description() string {
	return "Read the full SKILL.md content for a skill. Input: skill name."
}

func (t *SkillReadTool) Call(_ context.Context, input string) (string, error) {
	name := strings.TrimSpace(input)
	if name == "" {
		return "", fmt.Errorf("skill name is required")
	}
	if !isValidSkillName(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	path := filepath.Join(t.skillsDir, name, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("skill %q not found", name)
	}
	return string(data), nil
}

type SkillManageTool struct {
	skillsDir    string
	manifestPath string
}

func NewSkillManageTool(skillsDir, manifestPath string) *SkillManageTool {
	return &SkillManageTool{skillsDir: skillsDir, manifestPath: manifestPath}
}

func (t *SkillManageTool) Name() string { return "skill_manage" }

func (t *SkillManageTool) Description() string {
	return `Create, edit, patch, or delete skills. Input: JSON with fields:
- action: "create"|"edit"|"patch"|"delete"|"write_file"|"remove_file"
- name: skill name
- content: full SKILL.md (for create/edit)
- old_string, new_string: for patch
- file_path, file_content: for write_file/remove_file
- reason: why this change is being made`
}

type skillManageInput struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Content     string `json:"content,omitempty"`
	OldString   string `json:"old_string,omitempty"`
	NewString   string `json:"new_string,omitempty"`
	FilePath    string `json:"file_path,omitempty"`
	FileContent string `json:"file_content,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func (t *SkillManageTool) Call(_ context.Context, input string) (string, error) {
	var req skillManageInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid JSON input: %w", err)
	}
	if req.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if !isValidSkillName(req.Name) {
		return "", fmt.Errorf("invalid skill name %q: must not contain path separators or '..'", req.Name)
	}

	switch req.Action {
	case "create":
		return t.create(req)
	case "edit":
		return t.edit(req)
	case "patch":
		return t.patch(req)
	case "delete":
		return t.deleteSkill(req)
	case "write_file":
		return t.writeFile(req)
	case "remove_file":
		return t.removeFile(req)
	default:
		return "", fmt.Errorf("unknown action %q", req.Action)
	}
}

var allowedSubDirs = map[string]bool{
	"references": true,
	"templates":  true,
	"scripts":    true,
	"assets":     true,
}

func (t *SkillManageTool) create(req skillManageInput) (string, error) {
	if req.Content == "" {
		return "", fmt.Errorf("content is required for create")
	}
	skillDir := filepath.Join(t.skillsDir, req.Name)
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if fileExists(skillPath) {
		return "", fmt.Errorf("skill %q already exists, use edit or patch", req.Name)
	}
	skill, err := parseSkillFromContent(req.Content)
	if err != nil {
		return "", fmt.Errorf("invalid SKILL.md: %w", err)
	}
	if skill.Name != req.Name {
		return "", fmt.Errorf("frontmatter name %q must match %q", skill.Name, req.Name)
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(skillPath, []byte(req.Content), 0o644); err != nil {
		return "", err
	}
	t.updateManifestOnModify(req.Name)
	return fmt.Sprintf("Created skill %q", req.Name), nil
}

func (t *SkillManageTool) edit(req skillManageInput) (string, error) {
	if req.Content == "" {
		return "", fmt.Errorf("content is required for edit")
	}
	skillPath := filepath.Join(t.skillsDir, req.Name, "SKILL.md")
	if !fileExists(skillPath) {
		return "", fmt.Errorf("skill %q not found, use create", req.Name)
	}
	skill, err := parseSkillFromContent(req.Content)
	if err != nil {
		return "", fmt.Errorf("invalid SKILL.md: %w", err)
	}
	if skill.Name != req.Name {
		return "", fmt.Errorf("frontmatter name %q must match %q", skill.Name, req.Name)
	}
	if err := os.WriteFile(skillPath, []byte(req.Content), 0o644); err != nil {
		return "", err
	}
	t.updateManifestOnModify(req.Name)
	return fmt.Sprintf("Updated skill %q", req.Name), nil
}

func (t *SkillManageTool) patch(req skillManageInput) (string, error) {
	if req.OldString == "" || req.NewString == "" {
		return "", fmt.Errorf("old_string and new_string are required for patch")
	}
	skillPath := filepath.Join(t.skillsDir, req.Name, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return "", fmt.Errorf("skill %q not found", req.Name)
	}
	content := string(data)
	count := strings.Count(content, req.OldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in skill %q", req.Name)
	}
	if count > 1 {
		return "", fmt.Errorf("old_string matches %d times, must be unique", count)
	}
	newContent := strings.Replace(content, req.OldString, req.NewString, 1)
	if _, err := parseSkillFromContent(newContent); err != nil {
		return "", fmt.Errorf("patch produces invalid SKILL.md: %w", err)
	}
	if err := os.WriteFile(skillPath, []byte(newContent), 0o644); err != nil {
		return "", err
	}
	t.updateManifestOnModify(req.Name)
	return fmt.Sprintf("Patched skill %q", req.Name), nil
}

func (t *SkillManageTool) deleteSkill(req skillManageInput) (string, error) {
	skillDir := filepath.Join(t.skillsDir, req.Name)
	if !fileExists(filepath.Join(skillDir, "SKILL.md")) {
		return "", fmt.Errorf("skill %q not found", req.Name)
	}
	if err := os.RemoveAll(skillDir); err != nil {
		return "", err
	}
	if t.manifestPath != "" {
		manifest := loadManifest(t.manifestPath)
		if _, ok := manifest.Skills[req.Name]; ok {
			entry := manifest.Skills[req.Name]
			entry.Status = StatusDeletedByUser
			manifest.Skills[req.Name] = entry
			saveManifest(t.manifestPath, manifest)
		}
	}
	return fmt.Sprintf("Deleted skill %q", req.Name), nil
}

func (t *SkillManageTool) writeFile(req skillManageInput) (string, error) {
	if req.FilePath == "" || req.FileContent == "" {
		return "", fmt.Errorf("file_path and file_content required for write_file")
	}
	if !isAllowedSubPath(req.FilePath) {
		return "", fmt.Errorf("file_path must be under references/, templates/, scripts/, or assets/")
	}
	fullPath := filepath.Join(t.skillsDir, req.Name, req.FilePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(fullPath, []byte(req.FileContent), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Wrote %s for skill %q", req.FilePath, req.Name), nil
}

func (t *SkillManageTool) removeFile(req skillManageInput) (string, error) {
	if req.FilePath == "" {
		return "", fmt.Errorf("file_path required for remove_file")
	}
	if !isAllowedSubPath(req.FilePath) {
		return "", fmt.Errorf("file_path must be under references/, templates/, scripts/, or assets/")
	}
	fullPath := filepath.Join(t.skillsDir, req.Name, req.FilePath)
	if err := os.Remove(fullPath); err != nil {
		return "", fmt.Errorf("remove %s: %w", req.FilePath, err)
	}
	return fmt.Sprintf("Removed %s from skill %q", req.FilePath, req.Name), nil
}

func (t *SkillManageTool) updateManifestOnModify(name string) {
	if t.manifestPath == "" {
		return
	}
	manifest := loadManifest(t.manifestPath)
	entry, ok := manifest.Skills[name]
	if !ok {
		return
	}
	entry.Status = StatusUserModified
	entry.LastFailedMergeKey = ""
	entry.LastMergeError = ""
	manifest.Skills[name] = entry
	saveManifest(t.manifestPath, manifest)
}

func isAllowedSubPath(p string) bool {
	cleaned := filepath.Clean(filepath.ToSlash(p))
	if strings.Contains(cleaned, "..") {
		return false
	}
	parts := strings.SplitN(cleaned, "/", 2)
	if len(parts) == 0 {
		return false
	}
	return allowedSubDirs[parts[0]]
}

func isValidSkillName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	return true
}
