package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxWriteScriptBytes        = 256 * 1024
	maxScriptDescriptionRunes  = 200
	scriptDescriptionScanLines = 50
)

// resolveScriptFilePath validates a bare script file name and joins it under the
// configured scripts directory. Absolute paths and directory traversal are
// rejected so callers cannot read or write outside scripts/.
func resolveScriptFilePath(scriptsDir, file string) (string, error) {
	if strings.TrimSpace(scriptsDir) == "" {
		return "", fmt.Errorf("scripts directory is not configured")
	}
	if file == "" || file == "." || file == ".." {
		return "", fmt.Errorf("invalid script file name %q", file)
	}
	if filepath.IsAbs(file) || strings.ContainsAny(file, `/\`) || strings.Contains(file, "..") || filepath.Base(file) != file {
		return "", fmt.Errorf("script file must be a file name under scripts/, got %q", file)
	}
	return filepath.Join(scriptsDir, file), nil
}

// extractScriptDescription returns the leading description for a script. The
// description is built from the consecutive comment lines (lines beginning with
// '#') at the top of the file, with the leading '#' and surrounding whitespace
// trimmed. Blank lines before the first comment are ignored; scanning stops at
// the first non-comment, non-blank line.
func extractScriptDescription(data []byte) string {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), maxRunScriptLineBytes)
	var parts []string
	lines := 0
	for scanner.Scan() {
		if lines >= scriptDescriptionScanLines {
			break
		}
		lines++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			if len(parts) > 0 {
				break
			}
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		text := strings.TrimSpace(strings.TrimLeft(line, "#"))
		if text != "" {
			parts = append(parts, text)
		}
	}
	desc := strings.Join(parts, " ")
	return truncateRunes(desc, maxScriptDescriptionRunes)
}

// ListScriptsTool lists the demo script files available under the agent config
// directory's scripts/ folder, along with the description declared at the top of
// each file as '#' comment lines.
type ListScriptsTool struct {
	scriptsDir string
}

func NewListScriptsTool(scriptsDir string) *ListScriptsTool {
	return &ListScriptsTool{scriptsDir: scriptsDir}
}

func (t *ListScriptsTool) Name() string { return "list_scripts" }

func (t *ListScriptsTool) Description() string {
	return strings.Join([]string{
		"List all demo script files under the agent config directory's scripts/ folder.",
		"Each result has the file name and its description, taken from the leading '#' comment lines at the top of the script.",
		"Use this to discover which scripts exist before running one with run_script or before overwriting one with write_script.",
		`Input: {} (empty JSON object).`,
	}, " ")
}

func (t *ListScriptsTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{})
}

type scriptInfo struct {
	File        string `json:"file"`
	Description string `json:"description"`
}

func (t *ListScriptsTool) Call(_ context.Context, _ string) (string, error) {
	if strings.TrimSpace(t.scriptsDir) == "" {
		return "[]", nil
	}
	entries, err := os.ReadDir(t.scriptsDir)
	if err != nil {
		return "[]", nil
	}

	results := make([]scriptInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		info := scriptInfo{File: name}
		if data, err := os.ReadFile(filepath.Join(t.scriptsDir, name)); err == nil {
			info.Description = extractScriptDescription(data)
		}
		results = append(results, info)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].File < results[j].File
	})
	data, _ := json.Marshal(results)
	return string(data), nil
}

// ReadScriptTool returns the full content of a demo script file under the agent
// config directory's scripts/ folder.
type ReadScriptTool struct {
	scriptsDir string
	readFile   func(string) ([]byte, error)
}

func NewReadScriptTool(scriptsDir string) *ReadScriptTool {
	return &ReadScriptTool{scriptsDir: scriptsDir, readFile: os.ReadFile}
}

func (t *ReadScriptTool) Name() string { return "read_script" }

func (t *ReadScriptTool) Description() string {
	return strings.Join([]string{
		"Read the full content of a demo script file under the agent config directory's scripts/ folder.",
		"Use this to inspect a script's '#' description header and JSONL steps before running it with run_script or before overwriting it with write_script.",
		`Input JSON: {"file":"demo.jsonl"}. The file name is resolved under scripts/; full paths and directory traversal are rejected.`,
	}, " ")
}

func (t *ReadScriptTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"file": stringArgSchema("Script file name under scripts/, for example demo.jsonl. Do not pass a path."),
	}, "file")
}

func (t *ReadScriptTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v. Expected JSON: {\"file\":\"demo.jsonl\"}", err), nil
	}
	file := strings.TrimSpace(args.File)
	if file == "" {
		return "error: file is required", nil
	}
	path, err := resolveScriptFilePath(t.scriptsDir, file)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	readFile := t.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("error: script %q not found", file), nil
		}
		return fmt.Sprintf("error: read script %q: %v", file, err), nil
	}
	return string(data), nil
}

// WriteScriptTool creates or overwrites a demo script file under the agent
// config directory's scripts/ folder.
type WriteScriptTool struct {
	scriptsDir string
	writeFile  func(string, []byte, os.FileMode) error
	mkdirAll   func(string, os.FileMode) error
}

func NewWriteScriptTool(scriptsDir string) *WriteScriptTool {
	return &WriteScriptTool{
		scriptsDir: scriptsDir,
		writeFile:  os.WriteFile,
		mkdirAll:   os.MkdirAll,
	}
}

func (t *WriteScriptTool) Name() string { return "write_script" }

func (t *WriteScriptTool) Description() string {
	return strings.Join([]string{
		"Create or overwrite a demo script file under the agent config directory's scripts/ folder.",
		"Start the file with one or more '#' comment lines describing what the script does; list_scripts reports those lines as the description.",
		"Each remaining non-empty, non-comment line is one JSONL step understood by run_script: {\"type\":\"wait\",\"ms\":500}, {\"type\":\"tts\",\"text\":\"...\"}, or {\"type\":\"call\",\"tool\":\"touch_gesture\",\"input\":{...}}.",
		`Input JSON: {"file":"demo.jsonl","content":"# description\n{\"type\":\"wait\",\"ms\":500}"}. The file name is resolved under scripts/; full paths and directory traversal are rejected. Writing replaces any existing file with the same name.`,
	}, " ")
}

func (t *WriteScriptTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"file":    stringArgSchema("Script file name under scripts/, for example demo.jsonl. Do not pass a path."),
		"content": stringArgSchema("Full script file content. Begin with '#' description lines, then one JSONL step per line."),
	}, "file", "content")
}

type writeScriptResult struct {
	OK          bool   `json:"ok"`
	File        string `json:"file"`
	Bytes       int    `json:"bytes"`
	Description string `json:"description,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (t *WriteScriptTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		File    string `json:"file"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v. Expected JSON: {\"file\":\"demo.jsonl\",\"content\":\"...\"}", err), nil
	}
	file := strings.TrimSpace(args.File)
	if file == "" {
		return "error: file is required", nil
	}
	if args.Content == "" {
		return "error: content is required", nil
	}
	if len(args.Content) > maxWriteScriptBytes {
		return fmt.Sprintf("error: content is too large (%d bytes > %d bytes)", len(args.Content), maxWriteScriptBytes), nil
	}
	path, err := resolveScriptFilePath(t.scriptsDir, file)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	mkdirAll := t.mkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	if err := mkdirAll(t.scriptsDir, 0o700); err != nil {
		return fmt.Sprintf("error: create scripts directory: %v", err), nil
	}
	writeFile := t.writeFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	if err := writeFile(path, []byte(args.Content), 0o600); err != nil {
		return fmt.Sprintf("error: write script %q: %v", file, err), nil
	}

	result := writeScriptResult{
		OK:          true,
		File:        file,
		Bytes:       len(args.Content),
		Description: extractScriptDescription([]byte(args.Content)),
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}
