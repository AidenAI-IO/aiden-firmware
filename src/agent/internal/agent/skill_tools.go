package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const maxSkillReadBytes = 64 * 1024

type SkillListTool struct {
	skillsDir    string
	usagePath    string
	deviceTypeFn func() string
}

func NewSkillListTool(skillsDir string, usagePath ...string) *SkillListTool {
	tool := &SkillListTool{skillsDir: skillsDir}
	if len(usagePath) > 0 {
		tool.usagePath = usagePath[0]
	}
	return tool
}

func (t *SkillListTool) SetDeviceTypeFunc(fn func() string) {
	t.deviceTypeFn = fn
}

func (t *SkillListTool) Name() string { return "skill_list" }

func (t *SkillListTool) Description() string {
	return strings.Join([]string{
		"List available skills by name and description, similar to Hermes skills_list.",
		"Use when the user asks what skills exist, when you need to browse/search skills, or when the Available skills catalog is insufficient.",
		"For ordinary task execution, prefer the Available skills catalog and call skill_read directly for the matching skill.",
	}, " ")
}

func (t *SkillListTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"query":                stringArgSchema("Optional skill name or description search query."),
		"state":                stringEnumArgSchema("Optional lifecycle state filter.", SkillUsageStateActive, SkillUsageStateStale, SkillUsageStateArchived),
		"include_archived":     boolArgSchema("Include archived skills in results."),
		"include_incompatible": boolArgSchema("Include skills whose metadata.device_types does not match the current global device_type. Use only for explicit inspection or maintenance."),
		"limit":                minIntegerArgSchema("Maximum number of skills to return.", 1),
	})
}

func (t *SkillListTool) Call(_ context.Context, input string) (string, error) {
	req := parseSkillListInput(input)
	query := strings.ToLower(req.Query)
	applyAutomaticSkillLifecycle(t.skillsDir, t.usagePath, time.Now())
	entries, err := os.ReadDir(t.skillsDir)
	if err != nil {
		return "[]", nil
	}
	usage := loadSkillUsage(t.usagePath)
	deviceType := t.deviceType()

	type skillInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		State       string   `json:"state"`
		ViewCount   int      `json:"view_count,omitempty"`
		UseCount    int      `json:"use_count,omitempty"`
		ModifyCount int      `json:"modify_count,omitempty"`
		DeviceTypes []string `json:"device_types,omitempty"`
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
		skillUsage := usage[skill.Name]
		state := normalizeSkillUsageState(skillUsage.State)
		if req.State != "" && state != req.State {
			continue
		}
		if !req.IncludeArchived && state == SkillUsageStateArchived {
			continue
		}
		if !req.IncludeIncompatible && !skillSupportsDeviceType(skill, deviceType) {
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
			State:       state,
			ViewCount:   skillUsage.ViewCount,
			UseCount:    skillUsage.UseCount,
			ModifyCount: skillUsage.ModifyCount,
			DeviceTypes: skill.DeviceTypes,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})
	if req.Limit > 0 && len(results) > req.Limit {
		results = results[:req.Limit]
	}
	data, _ := json.Marshal(results)
	return string(data), nil
}

type skillListInput struct {
	Query               string `json:"query"`
	State               string `json:"state"`
	IncludeArchived     bool   `json:"include_archived"`
	IncludeIncompatible bool   `json:"include_incompatible"`
	Limit               int    `json:"limit"`
}

func parseSkillListInput(input string) skillListInput {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return skillListInput{}
	}
	if strings.HasPrefix(trimmed, "{") {
		var req skillListInput
		if err := json.Unmarshal([]byte(trimmed), &req); err == nil {
			req.Query = strings.TrimSpace(req.Query)
			req.State = normalizeSkillUsageState(strings.TrimSpace(req.State))
			if req.State == SkillUsageStateActive && !strings.Contains(trimmed, `"state"`) {
				req.State = ""
			}
			return req
		}
	}
	return skillListInput{Query: trimmed}
}

func (t *SkillListTool) deviceType() string {
	if t == nil || t.deviceTypeFn == nil {
		return ""
	}
	return t.deviceTypeFn()
}

type SkillMarkUsedTool struct {
	skillsDir string
	usagePath string
}

func NewSkillMarkUsedTool(skillsDir, usagePath string) *SkillMarkUsedTool {
	return &SkillMarkUsedTool{skillsDir: skillsDir, usagePath: usagePath}
}

func (t *SkillMarkUsedTool) Name() string { return "skill_mark_used" }

func (t *SkillMarkUsedTool) Description() string {
	return "Mark a skill as actually used after following it. Input: skill name."
}

func (t *SkillMarkUsedTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"name": stringArgSchema("Skill name to mark as used."),
	}, "name")
}

func (t *SkillMarkUsedTool) Call(_ context.Context, input string) (string, error) {
	name := strings.TrimSpace(input)
	if strings.HasPrefix(name, "{") {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(name), &req); err == nil {
			name = strings.TrimSpace(req.Name)
		}
	}
	if name == "" {
		return "", fmt.Errorf("skill name is required")
	}
	if !isValidSkillName(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	if !fileExists(filepath.Join(t.skillsDir, name, "SKILL.md")) {
		return "", fmt.Errorf("skill %q not found", name)
	}
	recordSkillUsed(t.usagePath, name)
	return fmt.Sprintf("Marked skill %q as used", name), nil
}

type SkillReadTool struct {
	skillsDir    string
	usagePath    string
	deviceTypeFn func() string
}

func NewSkillReadTool(skillsDir string, usagePath ...string) *SkillReadTool {
	tool := &SkillReadTool{skillsDir: skillsDir}
	if len(usagePath) > 0 {
		tool.usagePath = usagePath[0]
	}
	return tool
}

func (t *SkillReadTool) SetDeviceTypeFunc(fn func() string) {
	t.deviceTypeFn = fn
}

func (t *SkillReadTool) Name() string { return "skill_read" }

func (t *SkillReadTool) Description() string {
	return strings.Join([]string{
		"Load the full SKILL.md instructions for an available skill, similar to Hermes skill_view.",
		"Use this before acting when the user's task matches an Available skills entry and the skill is not already fully active.",
		"Also use it when the user explicitly asks to inspect a skill, or before patching a skill with skill_manage.",
		"Do not read every skill; choose only relevant skills. Reads UTF-8 text files only; binary assets are rejected.",
	}, " ")
}

func (t *SkillReadTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"name":                 stringArgSchema("Skill name to read."),
		"file_path":            stringArgSchema("Optional linked file path under SKILL.md, references/, templates/, scripts/, or assets/."),
		"include_incompatible": boolArgSchema("Allow reading a skill whose metadata.device_types does not match the current global device_type. Use only for explicit inspection or maintenance."),
	}, "name")
}

type skillReadInput struct {
	Name                string `json:"name"`
	FilePath            string `json:"file_path"`
	IncludeIncompatible bool   `json:"include_incompatible"`
}

func (t *SkillReadTool) Call(_ context.Context, input string) (string, error) {
	req := parseSkillReadInput(input)
	name := req.Name
	if name == "" {
		return "", fmt.Errorf("skill name is required")
	}
	if !isValidSkillName(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	skillPath, err := safeSkillReadPath(t.skillsDir, name, "SKILL.md")
	if err != nil {
		return "", err
	}
	skill, err := loadSkillMetadata(skillPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("skill %q not found", name)
		}
		return "", err
	}
	deviceType := t.deviceType()
	if !req.IncludeIncompatible && !skillSupportsDeviceType(skill, deviceType) {
		return "", fmt.Errorf("skill %q supports device_type %s, current device_type is %s", name, formatSkillDeviceTypes(skill.DeviceTypes), currentDeviceTypeForMessage(deviceType))
	}
	filePath := strings.TrimSpace(req.FilePath)
	if filePath == "" {
		filePath = "SKILL.md"
	}
	path, err := safeSkillReadPath(t.skillsDir, name, filePath)
	if err != nil {
		return "", err
	}
	data, err := readSkillTextFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if filePath == "SKILL.md" {
				return "", fmt.Errorf("skill %q not found", name)
			}
			return "", fmt.Errorf("skill %q file %q not found", name, filePath)
		}
		return "", err
	}
	recordSkillViewed(t.usagePath, name)
	content := string(data)
	if filePath == "SKILL.md" {
		content = appendLinkedFilesSection(content, t.skillsDir, name)
	}
	return content, nil
}

func (t *SkillReadTool) deviceType() string {
	if t == nil || t.deviceTypeFn == nil {
		return ""
	}
	return t.deviceTypeFn()
}

func appendLinkedFilesSection(content, skillsDir, name string) string {
	files := listSkillLinkedFiles(skillsDir, name)
	if len(files) == 0 {
		return content
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(content, "\n"))
	b.WriteString("\n\n---\nLinked files available via skill_read {\"name\":\"")
	b.WriteString(name)
	b.WriteString("\",\"file_path\":...}:\n")
	for _, file := range files {
		b.WriteString("- ")
		b.WriteString(file)
		b.WriteByte('\n')
	}
	return b.String()
}

func listSkillLinkedFiles(skillsDir, name string) []string {
	skillDir := filepath.Join(skillsDir, name)
	var files []string
	for dir := range allowedSubDirs {
		root := filepath.Join(skillDir, dir)
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(skillDir, path)
			if err != nil {
				return nil
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
	}
	sort.Strings(files)
	return files
}

func readSkillTextFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxSkillReadBytes {
		return nil, fmt.Errorf("skill file %q is too large (%d bytes > %d bytes)", path, info.Size(), maxSkillReadBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxSkillReadBytes {
		return nil, fmt.Errorf("skill file %q is too large (%d bytes > %d bytes)", path, len(data), maxSkillReadBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("skill file %q is not valid UTF-8 text", path)
	}
	return data, nil
}

func parseSkillReadInput(input string) skillReadInput {
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "{") {
		var req skillReadInput
		if err := json.Unmarshal([]byte(trimmed), &req); err == nil {
			req.Name = strings.TrimSpace(req.Name)
			req.FilePath = strings.TrimSpace(req.FilePath)
			return req
		}
	}
	return skillReadInput{Name: trimmed}
}

func safeSkillReadPath(skillsDir, name, filePath string) (string, error) {
	skillDir := filepath.Join(skillsDir, name)
	if filePath == "SKILL.md" {
		return safeResolvedSkillPath(skillsDir, skillDir, "SKILL.md")
	}
	clean := filepath.Clean(filePath)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid file_path %q", filePath)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	if len(parts) < 2 || !allowedSubDirs[parts[0]] {
		return "", fmt.Errorf("file_path must be SKILL.md or under references/, templates/, scripts/, or assets/")
	}
	return safeResolvedSkillPath(skillsDir, skillDir, clean)
}

func safeResolvedSkillPath(skillsDir, skillDir, filePath string) (string, error) {
	path := filepath.Join(skillDir, filePath)
	resolvedSkillsRoot, err := filepath.EvalSymlinks(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("resolve skills directory: %w", err)
	}
	resolvedSkillDir, err := filepath.EvalSymlinks(skillDir)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("resolve skill directory: %w", err)
	}
	if !pathWithin(resolvedSkillsRoot, resolvedSkillDir) {
		return "", fmt.Errorf("skill directory %q escapes skills directory", filepath.Base(skillDir))
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("resolve skill file %q: %w", filePath, err)
	}
	if !pathWithin(resolvedSkillDir, resolvedPath) {
		return "", fmt.Errorf("file_path %q escapes skill directory", filePath)
	}
	return resolvedPath, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

type SkillManageTool struct {
	skillsDir    string
	manifestPath string
	usagePath    string
	httpClient   *http.Client
	onModify     func()
}

func NewSkillManageTool(skillsDir, manifestPath string, onModify ...func()) *SkillManageTool {
	tool := &SkillManageTool{
		skillsDir:    skillsDir,
		manifestPath: manifestPath,
		usagePath:    usagePathForManifest(manifestPath),
		httpClient:   newSkillInstallHTTPClient(ProxyConfigFromEnvironment()),
	}
	if len(onModify) > 0 {
		tool.onModify = onModify[0]
	}
	return tool
}

// SetHTTPClient replaces the external client used by action=install. It is
// primarily useful for callers that need proxy/auth policy or deterministic
// tests; nil restores the default bounded client.
func (t *SkillManageTool) SetHTTPClient(client *http.Client) {
	if client == nil {
		client = newSkillInstallHTTPClient(ProxyConfigFromEnvironment())
	}
	t.httpClient = client
}

func (t *SkillManageTool) Name() string { return "skill_manage" }

func (t *SkillManageTool) Description() string {
	return `For any HTTP(S) or GitHub skill URL, call this tool directly with {"action":"install","source_url":"<original URL>"}; do not fetch or copy the remote contents first. The install action downloads, validates, and atomically commits the complete skill and its supported files. This tool also creates, edits, patches, deletes, archives, or restores local skills. Use it only when the user requests skill installation or maintenance, or after reading a skill with skill_read and deciding a maintenance change is needed.`
}

const skillManageInputExample = `{"action":"patch","name":"device-operator","old_string":"old instructions","new_string":"new instructions","reason":"add recovery steps"}`

type skillManageInput struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Content     string `json:"content,omitempty"`
	OldString   string `json:"old_string,omitempty"`
	NewString   string `json:"new_string,omitempty"`
	FilePath    string `json:"file_path,omitempty"`
	FileContent string `json:"file_content,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func (t *SkillManageTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"action":       stringEnumArgSchema("Skill management action. Use install for a remote skill URL.", "create", "edit", "patch", "install", "delete", "write_file", "remove_file", "mark_stale", "archive", "restore_archive"),
		"name":         stringArgSchema("Skill name. Optional for install; otherwise required."),
		"content":      stringArgSchema("Full SKILL.md content for create or edit."),
		"old_string":   stringArgSchema("Exact text to replace for patch."),
		"new_string":   stringArgSchema("Replacement text for patch."),
		"file_path":    stringArgSchema("File path under references/, templates/, scripts/, or assets/ for write_file/remove_file."),
		"file_content": stringArgSchema("Full file content for write_file."),
		"source_url":   stringArgSchema("Original HTTP(S) URL; required for install. GitHub tree URLs install SKILL.md and supported files under references/, templates/, scripts/, and assets/.", "https://github.com/owner/repo/tree/main/skills/example"),
		"reason":       stringArgSchema("Reason for making this skill change."),
	}, "action")
}

func (t *SkillManageTool) Call(ctx context.Context, input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("skill_manage input must be a JSON object, for example %s", skillManageInputExample)
	}
	var req skillManageInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("skill_manage input must be a JSON object, for example %s: invalid JSON input: %w", skillManageInputExample, err)
	}
	if req.Action != "install" && req.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if req.Name != "" && !isValidSkillName(req.Name) {
		return "", fmt.Errorf("invalid skill name %q: must not contain path separators or '..'", req.Name)
	}

	switch req.Action {
	case "create":
		return t.create(req)
	case "edit":
		return t.edit(req)
	case "patch":
		return t.patch(req)
	case "install":
		return t.install(ctx, req)
	case "delete":
		return t.deleteSkill(req)
	case "write_file":
		return t.writeFile(req)
	case "remove_file":
		return t.removeFile(req)
	case "mark_stale":
		return t.setLifecycleState(req, SkillUsageStateStale, "Marked skill %q as stale")
	case "archive":
		return t.setLifecycleState(req, SkillUsageStateArchived, "Archived skill %q")
	case "restore_archive":
		return t.setLifecycleState(req, SkillUsageStateActive, "Restored skill %q")
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
	if err := validateSkillDefinition(skill, req.Name); err != nil {
		return "", err
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return "", err
	}
	skillFileMu.Lock()
	err = writeFileAtomic(skillPath, []byte(req.Content), 0o644)
	skillFileMu.Unlock()
	if err != nil {
		return "", err
	}
	t.updateManifestOnModify(req.Name, true)
	t.recordModify(req.Name)
	return skillManageContentResult("Created", req.Name, []byte(req.Content)), nil
}

func (t *SkillManageTool) edit(req skillManageInput) (string, error) {
	if req.Content == "" {
		return "", fmt.Errorf("content is required for edit")
	}
	skillPath := filepath.Join(t.skillsDir, req.Name, "SKILL.md")
	previous, err := os.ReadFile(skillPath)
	if err != nil {
		return "", fmt.Errorf("skill %q not found, use create", req.Name)
	}
	skill, err := parseSkillFromContent(req.Content)
	if err != nil {
		return "", fmt.Errorf("invalid SKILL.md: %w", err)
	}
	if err := validateSkillDefinition(skill, req.Name); err != nil {
		return "", err
	}
	skillFileMu.Lock()
	err = writeFileAtomic(skillPath, []byte(req.Content), 0o644)
	skillFileMu.Unlock()
	if err != nil {
		return "", err
	}
	t.updateManifestOnModify(req.Name, true)
	t.recordModify(req.Name)
	return skillManageReplacementResult("Updated", req.Name, previous, []byte(req.Content), changedLineRange(previous, []byte(req.Content))), nil
}

func (t *SkillManageTool) patch(req skillManageInput) (string, error) {
	if req.OldString == "" {
		return "", fmt.Errorf("old_string is required for patch")
	}
	skillPath := filepath.Join(t.skillsDir, req.Name, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return "", fmt.Errorf("skill %q not found", req.Name)
	}
	content := string(data)
	count := strings.Count(content, req.OldString)
	if count == 0 {
		caseInsensitiveMatches := strings.Count(strings.ToLower(content), strings.ToLower(req.OldString))
		normalizedContent := strings.Join(strings.Fields(content), " ")
		normalizedOld := strings.Join(strings.Fields(req.OldString), " ")
		whitespaceNormalizedMatches := 0
		if normalizedOld != "" {
			whitespaceNormalizedMatches = strings.Count(normalizedContent, normalizedOld)
		}
		return "", fmt.Errorf(
			"old_string not found in skill %q (case_insensitive_matches=%d, whitespace_normalized_matches=%d)",
			req.Name,
			caseInsensitiveMatches,
			whitespaceNormalizedMatches,
		)
	}
	if count > 1 {
		return "", fmt.Errorf(
			"old_string matches %d times, must be unique (matching_lines=%s)",
			count,
			matchingLineNumbers(content, req.OldString, 8),
		)
	}
	newContent := strings.Replace(content, req.OldString, req.NewString, 1)
	skill, err := parseSkillFromContent(newContent)
	if err != nil {
		return "", fmt.Errorf("patch produces invalid SKILL.md: %w", err)
	}
	if err := validateSkillDefinition(skill, req.Name); err != nil {
		return "", fmt.Errorf("patch produces invalid SKILL.md: %w", err)
	}
	skillFileMu.Lock()
	err = writeFileAtomic(skillPath, []byte(newContent), 0o644)
	skillFileMu.Unlock()
	if err != nil {
		return "", err
	}
	t.updateManifestOnModify(req.Name, true)
	t.recordModify(req.Name)
	start := strings.Index(content, req.OldString)
	changedStart, changedEnd := lineSpan(content, start, start+len(req.OldString))
	return skillManageReplacementResult(
		"Patched", req.Name, data, []byte(newContent),
		fmt.Sprintf("changed_lines=%d-%d", changedStart, changedEnd),
	), nil
}

func validateSkillDefinition(skill *SkillDefinition, expectedName string) error {
	if skill == nil {
		return fmt.Errorf("invalid SKILL.md: skill definition is empty")
	}
	if skill.Name != expectedName {
		return fmt.Errorf("frontmatter name %q must match %q", skill.Name, expectedName)
	}
	if strings.TrimSpace(skill.Description) == "" {
		return fmt.Errorf("invalid SKILL.md: skill description is required")
	}
	if strings.TrimSpace(skill.Instructions) == "" {
		return fmt.Errorf("invalid SKILL.md: skill instructions are required")
	}
	if !allowedToolsExist(skill.AllowedTools) {
		return fmt.Errorf("invalid SKILL.md: metadata.allowed_tools contains unknown tools")
	}
	return nil
}

func skillManageContentResult(action, name string, content []byte) string {
	return fmt.Sprintf(
		`%s skill %q (bytes=%d, lines=%d, sha256=%s)`,
		action,
		name,
		len(content),
		lineCount(content),
		strings.TrimPrefix(hashContent(content), "sha256:"),
	)
}

func skillManageReplacementResult(action, name string, previous, current []byte, details string) string {
	return fmt.Sprintf(
		`%s skill %q (%s, previous_bytes=%d, %s)`,
		action,
		name,
		skillContentSummary(current),
		len(previous),
		details,
	)
}

func skillContentSummary(content []byte) string {
	return fmt.Sprintf("bytes=%d, lines=%d, sha256=%s", len(content), lineCount(content), strings.TrimPrefix(hashContent(content), "sha256:"))
}

func lineSpan(content string, start, end int) (int, int) {
	if start < 0 {
		return 0, 0
	}
	startLine := 1 + strings.Count(content[:start], "\n")
	if end <= start {
		return startLine, startLine
	}
	last := end - 1
	if last >= len(content) {
		last = len(content) - 1
	}
	if last < 0 {
		return startLine, startLine
	}
	return startLine, 1 + strings.Count(content[:last], "\n")
}

func matchingLineNumbers(content, target string, limit int) string {
	var lines []string
	for offset := 0; offset < len(content) && len(lines) < limit; {
		index := strings.Index(content[offset:], target)
		if index < 0 {
			break
		}
		absolute := offset + index
		lines = append(lines, fmt.Sprintf("%d", 1+strings.Count(content[:absolute], "\n")))
		offset = absolute + len(target)
	}
	return strings.Join(lines, ",")
}

func changedLineRange(previous, current []byte) string {
	oldLines := strings.Split(string(previous), "\n")
	newLines := strings.Split(string(current), "\n")
	first := -1
	last := -1
	maxLines := len(oldLines)
	if len(newLines) > maxLines {
		maxLines = len(newLines)
	}
	for i := 0; i < maxLines; i++ {
		var oldLine, newLine string
		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}
		if oldLine != newLine {
			if first == -1 {
				first = i + 1
			}
			last = i + 1
		}
	}
	if first == -1 {
		return "changed_lines=none"
	}
	return fmt.Sprintf("changed_lines=%d-%d", first, last)
}

func (t *SkillManageTool) deleteSkill(req skillManageInput) (string, error) {
	skillDir := filepath.Join(t.skillsDir, req.Name)
	if !fileExists(filepath.Join(skillDir, "SKILL.md")) {
		return "", fmt.Errorf("skill %q not found", req.Name)
	}
	skillFileMu.Lock()
	err := os.RemoveAll(skillDir)
	skillFileMu.Unlock()
	if err != nil {
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
	t.recordModify(req.Name)
	return fmt.Sprintf("Deleted skill %q", req.Name), nil
}

func (t *SkillManageTool) writeFile(req skillManageInput) (string, error) {
	if req.FilePath == "" {
		return "", fmt.Errorf("file_path required for write_file")
	}
	if !isAllowedSubPath(req.FilePath) {
		return "", fmt.Errorf("file_path must be under references/, templates/, scripts/, or assets/")
	}
	if !fileExists(filepath.Join(t.skillsDir, req.Name, "SKILL.md")) {
		return "", fmt.Errorf("skill %q not found", req.Name)
	}
	fullPath := filepath.Join(t.skillsDir, req.Name, req.FilePath)
	_, previousErr := os.Stat(fullPath)
	previousExists := previousErr == nil
	if previousErr != nil && !os.IsNotExist(previousErr) {
		return "", fmt.Errorf("read existing %s: %w", req.FilePath, previousErr)
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", err
	}
	skillFileMu.Lock()
	err := writeFileAtomic(fullPath, []byte(req.FileContent), 0o644)
	skillFileMu.Unlock()
	if err != nil {
		return "", err
	}
	t.updateManifestOnModify(req.Name, false)
	t.recordModify(req.Name)
	return fmt.Sprintf(
		`Wrote %s for skill %q (created=%t, %s)`,
		req.FilePath,
		req.Name,
		!previousExists,
		skillContentSummary([]byte(req.FileContent)),
	), nil
}

func (t *SkillManageTool) removeFile(req skillManageInput) (string, error) {
	if req.FilePath == "" {
		return "", fmt.Errorf("file_path required for remove_file")
	}
	if !isAllowedSubPath(req.FilePath) {
		return "", fmt.Errorf("file_path must be under references/, templates/, scripts/, or assets/")
	}
	if !fileExists(filepath.Join(t.skillsDir, req.Name, "SKILL.md")) {
		return "", fmt.Errorf("skill %q not found", req.Name)
	}
	fullPath := filepath.Join(t.skillsDir, req.Name, req.FilePath)
	skillFileMu.Lock()
	err := os.Remove(fullPath)
	skillFileMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("remove %s: %w", req.FilePath, err)
	}
	t.updateManifestOnModify(req.Name, false)
	t.recordModify(req.Name)
	return fmt.Sprintf("Removed %s from skill %q", req.FilePath, req.Name), nil
}

func (t *SkillManageTool) setLifecycleState(req skillManageInput, state, message string) (string, error) {
	if !fileExists(filepath.Join(t.skillsDir, req.Name, "SKILL.md")) {
		return "", fmt.Errorf("skill %q not found", req.Name)
	}
	setSkillUsageState(t.usagePath, req.Name, state)
	t.recordModify(req.Name)
	return fmt.Sprintf(message, req.Name), nil
}

func (t *SkillManageTool) recordModify(name string) {
	recordSkillModified(t.usagePath, name)
	if t.onModify != nil {
		t.onModify()
	}
}

func (t *SkillManageTool) updateManifestOnModify(name string, clearFailedMerge bool) {
	if t.manifestPath == "" {
		return
	}
	manifest := loadManifest(t.manifestPath)
	entry, ok := manifest.Skills[name]
	if !ok {
		return
	}
	entry.Status = StatusUserModified
	if clearFailedMerge {
		entry.LastFailedMergeKey = ""
		entry.LastMergeFailedAt = ""
		entry.LastMergeError = ""
	}
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
