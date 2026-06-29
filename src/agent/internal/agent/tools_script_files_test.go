package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	langtools "github.com/tmc/langchaingo/tools"
)

func TestListScriptsReturnsFilesWithDescriptions(t *testing.T) {
	scriptsDir := t.TempDir()
	writeRunScriptTestFile(t, scriptsDir, "open-settings.jsonl", strings.Join([]string{
		"# 打开设置演示",
		"# 第二行补充说明",
		`{"type":"wait","ms":500}`,
	}, "\n"))
	writeRunScriptTestFile(t, scriptsDir, "no-desc.jsonl", `{"type":"wait","ms":100}`)
	writeRunScriptTestFile(t, scriptsDir, ".hidden.jsonl", "# hidden")
	if err := os.Mkdir(filepath.Join(scriptsDir, "subdir"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	tool := NewListScriptsTool(scriptsDir)
	out, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var results []scriptInfo
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want 2 visible files", results)
	}
	if results[0].File != "no-desc.jsonl" || results[0].Description != "" {
		t.Fatalf("results[0] = %#v, want no-desc.jsonl with empty description", results[0])
	}
	if results[1].File != "open-settings.jsonl" || results[1].Description != "打开设置演示 第二行补充说明" {
		t.Fatalf("results[1] = %#v, want joined description", results[1])
	}
}

func TestListScriptsMissingDirReturnsEmpty(t *testing.T) {
	tool := NewListScriptsTool(filepath.Join(t.TempDir(), "missing"))
	out, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("output = %q, want []", out)
	}
}

func TestWriteScriptCreatesFileAndReportsDescription(t *testing.T) {
	scriptsDir := t.TempDir()
	tool := NewWriteScriptTool(scriptsDir)
	content := "# 演示脚本\n{\"type\":\"wait\",\"ms\":250}"
	in, _ := json.Marshal(map[string]string{"file": "demo.jsonl", "content": content})

	out, err := tool.Call(context.Background(), string(in))
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var result writeScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if !result.OK || result.File != "demo.jsonl" || result.Description != "演示脚本" {
		t.Fatalf("result = %#v", result)
	}
	if result.Bytes != len(content) {
		t.Fatalf("result.Bytes = %d, want %d", result.Bytes, len(content))
	}
	data, err := os.ReadFile(filepath.Join(scriptsDir, "demo.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Fatalf("written content = %q, want %q", data, content)
	}
}

func TestWriteScriptCreatesPrivateDirectoryAndFile(t *testing.T) {
	scriptsDir := filepath.Join(t.TempDir(), "scripts")
	tool := NewWriteScriptTool(scriptsDir)
	in, _ := json.Marshal(map[string]string{"file": "demo.jsonl", "content": "# private"})

	if _, err := tool.Call(context.Background(), string(in)); err != nil {
		t.Fatalf("Call error: %v", err)
	}
	dirInfo, err := os.Stat(scriptsDir)
	if err != nil {
		t.Fatalf("Stat scripts dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("scripts dir mode = %#o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(scriptsDir, "demo.jsonl"))
	if err != nil {
		t.Fatalf("Stat script file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("script file mode = %#o, want 0600", got)
	}
}

func TestWriteScriptTightensExistingDirectoryAndFilePermissions(t *testing.T) {
	scriptsDir := filepath.Join(t.TempDir(), "scripts")
	if err := os.Mkdir(scriptsDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	path := filepath.Join(scriptsDir, "demo.jsonl")
	if err := os.WriteFile(path, []byte("# old"), 0o644); err != nil {
		t.Fatalf("WriteFile old script: %v", err)
	}
	tool := NewWriteScriptTool(scriptsDir)
	in, _ := json.Marshal(map[string]string{"file": "demo.jsonl", "content": "# private"})

	if _, err := tool.Call(context.Background(), string(in)); err != nil {
		t.Fatalf("Call error: %v", err)
	}
	dirInfo, err := os.Stat(scriptsDir)
	if err != nil {
		t.Fatalf("Stat scripts dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("scripts dir mode = %#o, want 0700", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat script file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("script file mode = %#o, want 0600", got)
	}
}

func TestWriteScriptOverwritesExistingFile(t *testing.T) {
	scriptsDir := t.TempDir()
	writeRunScriptTestFile(t, scriptsDir, "demo.jsonl", "# old\n{\"type\":\"wait\",\"ms\":1}")
	tool := NewWriteScriptTool(scriptsDir)
	in, _ := json.Marshal(map[string]string{"file": "demo.jsonl", "content": "# new\n{\"type\":\"wait\",\"ms\":2}"})

	if _, err := tool.Call(context.Background(), string(in)); err != nil {
		t.Fatalf("Call error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(scriptsDir, "demo.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "# new") || strings.Contains(string(data), "# old") {
		t.Fatalf("file was not overwritten: %q", data)
	}
}

func TestWriteScriptRejectsPathLikeFileName(t *testing.T) {
	tool := NewWriteScriptTool(t.TempDir())
	for _, file := range []string{"", ".", "..", "../demo.jsonl", "nested/demo.jsonl", "/tmp/demo.jsonl", `nested\demo.jsonl`} {
		in, _ := json.Marshal(map[string]string{"file": file, "content": "# x"})
		out, err := tool.Call(context.Background(), string(in))
		if err != nil {
			t.Fatalf("Call error for %q: %v", file, err)
		}
		if !strings.Contains(out, "file is required") &&
			!strings.Contains(out, "invalid script file name") &&
			!strings.Contains(out, "script file must be a file name under scripts/") {
			t.Fatalf("output for %q = %s, want file-name rejection", file, out)
		}
	}
}

func TestReadScriptReturnsContent(t *testing.T) {
	scriptsDir := t.TempDir()
	content := "# 演示\n{\"type\":\"wait\",\"ms\":250}"
	writeRunScriptTestFile(t, scriptsDir, "demo.jsonl", content)
	tool := NewReadScriptTool(scriptsDir)

	out, err := tool.Call(context.Background(), `{"file":"demo.jsonl"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != content {
		t.Fatalf("output = %q, want %q", out, content)
	}
}

func TestReadScriptMissingFile(t *testing.T) {
	tool := NewReadScriptTool(t.TempDir())
	out, err := tool.Call(context.Background(), `{"file":"missing.jsonl"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if !strings.Contains(out, "not found") {
		t.Fatalf("output = %s, want not-found error", out)
	}
}

func TestReadScriptRequiresFile(t *testing.T) {
	tool := NewReadScriptTool(t.TempDir())
	if out, _ := tool.Call(context.Background(), `{}`); !strings.Contains(out, "file is required") {
		t.Fatalf("missing file output = %s", out)
	}
}

func TestReadScriptRejectsPathLikeFileName(t *testing.T) {
	tool := NewReadScriptTool(t.TempDir())
	for _, file := range []string{"", ".", "..", "../demo.jsonl", "nested/demo.jsonl", "/tmp/demo.jsonl", `nested\demo.jsonl`} {
		in, _ := json.Marshal(map[string]string{"file": file})
		out, err := tool.Call(context.Background(), string(in))
		if err != nil {
			t.Fatalf("Call error for %q: %v", file, err)
		}
		if !strings.Contains(out, "file is required") &&
			!strings.Contains(out, "invalid script file name") &&
			!strings.Contains(out, "script file must be a file name under scripts/") {
			t.Fatalf("output for %q = %s, want file-name rejection", file, out)
		}
	}
}

func TestWriteScriptRequiresFileAndContent(t *testing.T) {
	tool := NewWriteScriptTool(t.TempDir())
	if out, _ := tool.Call(context.Background(), `{"content":"# x"}`); !strings.Contains(out, "file is required") {
		t.Fatalf("missing file output = %s", out)
	}
	if out, _ := tool.Call(context.Background(), `{"file":"demo.jsonl"}`); !strings.Contains(out, "content is required") {
		t.Fatalf("missing content output = %s", out)
	}
}

func TestWriteScriptThenRunScriptSkipsCommentLines(t *testing.T) {
	scriptsDir := t.TempDir()
	calledTool := &stubTool{name: "calculator", output: "5"}
	writer := NewWriteScriptTool(scriptsDir)
	content := strings.Join([]string{
		"# 计算演示",
		"# 解释说明",
		`{"type":"call","tool":"calculator","input":{"expression":"2+3"}}`,
	}, "\n")
	in, _ := json.Marshal(map[string]string{"file": "math.jsonl", "content": content})
	if _, err := writer.Call(context.Background(), string(in)); err != nil {
		t.Fatalf("write Call error: %v", err)
	}

	runner := NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		if name == calledTool.name {
			return calledTool, true
		}
		return nil, false
	})
	out, err := runner.Call(context.Background(), `{"file":"math.jsonl"}`)
	if err != nil {
		t.Fatalf("run Call error: %v", err)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if !result.OK || result.StepsRun != 1 {
		t.Fatalf("result = %#v, comment lines should be skipped", result)
	}
	if len(calledTool.inputs) != 1 || calledTool.inputs[0] != `{"expression":"2+3"}` {
		t.Fatalf("calculator inputs = %#v", calledTool.inputs)
	}
}

func TestExtractScriptDescription(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single", "# hello\n{}", "hello"},
		{"multi", "#  a \n# b\n{}", "a b"},
		{"blank-before", "\n\n# desc\n{}", "desc"},
		{"stops-at-step", "# desc\n{}\n# later", "desc"},
		{"no-comment", "{}\n# nope", ""},
		{"hashes", "### heading", "heading"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractScriptDescription([]byte(c.in)); got != c.want {
				t.Fatalf("extractScriptDescription(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestScriptToolsUseConfigScriptsDir(t *testing.T) {
	configDir := t.TempDir()
	tools := NewBuiltinToolSetFromConfig(Config{Model: ModelConfig{Provider: "fake"}, ConfigDir: configDir}, ProxyConfig{}, nil)

	writer, ok := tools.Get("write_script")
	if !ok {
		t.Fatal("write_script tool missing")
	}
	in, _ := json.Marshal(map[string]string{"file": "demo.jsonl", "content": "# 演示\n{\"type\":\"wait\",\"ms\":1}"})
	if _, err := writer.Call(context.Background(), string(in)); err != nil {
		t.Fatalf("write Call error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "scripts", "demo.jsonl")); err != nil {
		t.Fatalf("script not written under config scripts dir: %v", err)
	}

	lister, ok := tools.Get("list_scripts")
	if !ok {
		t.Fatal("list_scripts tool missing")
	}
	out, err := lister.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("list Call error: %v", err)
	}
	if !strings.Contains(out, `"file":"demo.jsonl"`) || !strings.Contains(out, `"description":"演示"`) {
		t.Fatalf("list output = %s, want demo.jsonl with description", out)
	}

	reader, ok := tools.Get("read_script")
	if !ok {
		t.Fatal("read_script tool missing")
	}
	content, err := reader.Call(context.Background(), `{"file":"demo.jsonl"}`)
	if err != nil {
		t.Fatalf("read Call error: %v", err)
	}
	if !strings.Contains(content, "# 演示") || !strings.Contains(content, `"type":"wait"`) {
		t.Fatalf("read output = %s, want full script content", content)
	}
}

func TestRunScriptScriptsDirOptionOverridesConfigDefault(t *testing.T) {
	configDir := t.TempDir()
	overrideDir := filepath.Join(t.TempDir(), "override-scripts")
	tools := NewBuiltinToolSetFromConfig(
		Config{Model: ModelConfig{Provider: "fake"}, ConfigDir: configDir},
		ProxyConfig{},
		WithRunScriptScriptsDir(overrideDir),
	)

	writer, ok := tools.Get("write_script")
	if !ok {
		t.Fatal("write_script tool missing")
	}
	in, _ := json.Marshal(map[string]string{"file": "demo.jsonl", "content": "# override\n{\"type\":\"wait\",\"ms\":1}"})
	if _, err := writer.Call(context.Background(), string(in)); err != nil {
		t.Fatalf("write Call error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(overrideDir, "demo.jsonl")); err != nil {
		t.Fatalf("script not written under override dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "scripts", "demo.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("script unexpectedly written under config default, stat err=%v", err)
	}
}
