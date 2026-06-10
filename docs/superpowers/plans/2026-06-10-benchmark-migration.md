# Benchmark + user_files 迁入 agent (8080) 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 config_web (80) 上 12 个 benchmark + user_files 路由迁到 agent (8080) Go 实现，并新增"截图任务录入器"。删除 config_web 中相关 ~600 行 C++、删除 `scripts/files_proxy.py`、删除 `benchmark/suites/full_smoke.json`。

**Architecture:** 全部 HTTP 路由保持原 URL 路径，host 从 80 改为 8080 即可。每个 handler 一对一翻译 C++ 实现，行为等价；LLM 调用复用 agent 的 `ModelManager` 替代 popen+curl+tmpfile；Python runner 仍用 sh 脚本 double-fork 起子进程，与现状一致。

**Tech Stack:** Go (agent)、TOML (config)、`os/exec`、langchain-go (`ModelManager`)、Python `runner.suite.load_suite` (validation)、HTML embed via raw-string-literal Go const。

**Spec:** `docs/superpowers/specs/2026-06-10-benchmark-migration-design.md`

---

## File Structure

新文件（Go）：

- `src/agent/internal/agent/benchmark.go` — 路由注册 + 普通 handlers + benchmark 根目录探测
- `src/agent/internal/agent/benchmark_html.go` — `/benchmark` 主页 + `/benchmark/record` 录入页 HTML/JS（raw-string）
- `src/agent/internal/agent/benchmark_runner.go` — `POST /benchmark/run` double-fork 子进程 + state.json + log
- `src/agent/internal/agent/benchmark_generate.go` — LLM 生成（纯文本 + perception 截图两种）
- `src/agent/internal/agent/benchmark_import.go` — `import` + `import-with-assets`
- `src/agent/internal/agent/user_files.go` — `/user_files` + `/user_files/regenerate`
- 对应单测：`*_test.go` 同名

修改：

- `src/agent/internal/agent/config.go` — `Config` 加 `Benchmark BenchmarkConfig`；新增 `BenchmarkConfig` 类型
- `src/agent/internal/agent/config_meta.go` — 在 schema 加 `[benchmark]` 段
- `src/agent/internal/agent/server.go:229–262` — 注册新路由
- `src/config_web.cpp` — 删除 12 个 if 块 + 4 个相关函数 + 主页相关入口
- `tests/config_web_source_test.cpp` — 删除迁走路由的所有 source-grep assertion
- `agent.conf` — 加 `[benchmark]` 段示例

删除：

- `scripts/files_proxy.py`
- `benchmark/suites/full_smoke.json`

---

## Server API 现状速查

实施时所有 handler 都是 `*Server` 方法。Server 已有的字段（`server.go:31`）通过 `s.runtime` 间接持有依赖：

- 配置：`s.runtime.config` (类型 `Config`) — `s.runtime.config.Model.APIKey`、`s.runtime.config.Benchmark.JudgeModel`
- 模型：`s.runtime.models` (类型 `ModelResolver`) — 调用 `.LLM()` 或现有 helper（grep `models.LLM` 找用例）
- 工具：`s.runtime.tools` (类型 `*ToolSet`) — 同 `handleTools` 里的用法（grep `s.runtime.tools` 找）

下文 task 里出现 `s.cfg`、`s.modelManager`、`s.tools()` 这种简写，**实施时一律改写为通过 `s.runtime` 访问**。本计划保留简写是为了任务伪代码读起来清爽；实际编译需要 `s.runtime.config`、`s.runtime.models`、`s.runtime.tools` 这种真实字段路径。

**实施前先 grep 确认现有命名**：

```bash
grep -n "s\.runtime\." src/agent/internal/agent/server.go | head
grep -n "func (s \*Server)" src/agent/internal/agent/server.go | head
```

新增的 benchmark 相关 handler 字段（如 `benchmarkDir`、`benchmarkPIDFile`、`benchmarkLauncher`）是直接挂在 `Server` 还是挂在 `Runtime`，按现有模式选择——倾向挂在 `Server`（HTTP 层关注），grep `addr ` 字段附近的字段定义点。

---

## TODO 占位符的特殊性

Task 9（benchmarkIndexHTML）和 Task 12（benchmarkGenerateSystemPrompt）使用 `<TODO: paste from ...>` 占位符。这**不是失败的 plan placeholder**——是 plan 显式要求实施者把 C++ 源文件中相应的 raw-string-literal 1:1 复制到 Go const。理由：内联粘贴会让计划文档膨胀到 5000+ 行，且复制本身机械。

测试已经强制保护：

- Task 9 `TestHandleBenchmarkIndex_ServesHTMLWithRouterButtons` 检查关键 marker 存在
- Task 12 `TestBenchmarkGenerateSystemPrompt_NotPlaceholder` 显式检查 `TODO` 字符串不在 const 中

实施时 **必须** 完成复制；测试会强制 fail。

---

## Task 1: 加 BenchmarkConfig 到 agent.conf schema

**Files:**

- Modify: `src/agent/internal/agent/config.go` (Config 结构追加字段)
- Modify: `src/agent/internal/agent/config_meta.go` (UI schema 加 [benchmark] 段)
- Test: `src/agent/internal/agent/config_test.go`
- Modify: `agent.conf` (示例配置)

- [ ] **Step 1: 写 config 失败测试**

加到 `config_test.go` 末尾：

```go
func TestLoadConfig_BenchmarkSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.conf")
	body := `
[model]
provider = "openrouter"
api_key = "x"
model = "y"

[benchmark]
judge_model = "custom/model-v1"
benchmark_dir = "/tmp/bench"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Benchmark.JudgeModel != "custom/model-v1" {
		t.Errorf("JudgeModel = %q", cfg.Benchmark.JudgeModel)
	}
	if cfg.Benchmark.Dir != "/tmp/bench" {
		t.Errorf("Dir = %q", cfg.Benchmark.Dir)
	}
}

func TestLoadConfig_BenchmarkDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.conf")
	body := `
[model]
provider = "openrouter"
api_key = "x"
model = "y"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Benchmark.JudgeModel != "" {
		t.Errorf("expected empty JudgeModel default, got %q", cfg.Benchmark.JudgeModel)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd src/agent && go test ./internal/agent -run TestLoadConfig_Benchmark -v
```

期望：FAIL（`cfg.Benchmark` 字段不存在）。

- [ ] **Step 3: 加 BenchmarkConfig 类型**

在 `config.go` 末尾加：

```go
// BenchmarkConfig configures the benchmark management endpoints in the agent
// (migrated from config_web). All fields are optional; empty strings fall back
// to runtime defaults.
type BenchmarkConfig struct {
	// JudgeModel is the OpenRouter model name passed to runner.main as
	// --judge-model. Defaults to "bytedance-seed/seed-2.0-lite" when empty.
	JudgeModel string `toml:"judge_model,omitempty"`
	// Dir overrides the auto-detected benchmark root. When empty, the
	// agent probes -benchmark-dir flag, AIDEN_BENCHMARK_DIR env,
	// /userdata/agent/benchmark, then <cwd>/benchmark.
	Dir string `toml:"benchmark_dir,omitempty"`
}
```

并在 `Config` 结构体加（位置：`Audio AudioConfig` 之后）：

```go
	Benchmark BenchmarkConfig `toml:"benchmark,omitempty"`
```

- [ ] **Step 4: 跑测试确认通过**

```bash
cd src/agent && go test ./internal/agent -run TestLoadConfig_Benchmark -v
```

期望：PASS。

- [ ] **Step 5: 加 config_meta schema 项**

在 `config_meta.go` 中找到现有 sections 数组（grep `Sections: []ConfigSection`），在 `audio` 段后追加：

```go
		{
			Key:   "benchmark",
			Title: "Benchmark",
			Fields: []ConfigField{
				{Key: "judge_model",
					Type:    "string",
					Label:   "Judge model",
					Default: "bytedance-seed/seed-2.0-lite",
					Help:    "OpenRouter model used to score benchmark runs.",
				},
				{Key: "benchmark_dir",
					Type:  "string",
					Label: "Benchmark directory",
					Help:  "Override auto-detected benchmark root. Leave blank for default.",
				},
			},
		},
```

注：`ConfigSection` / `ConfigField` 字段名可能需要按现有 struct 微调，先 read `config_meta.go` 顶部确认。

- [ ] **Step 6: agent.conf 示例追加**

在 `agent.conf` 末尾追加：

```toml

# Benchmark management endpoints (served on agent port 8080).
[benchmark]
# judge_model = "bytedance-seed/seed-2.0-lite"
# benchmark_dir = "/userdata/agent/benchmark"
```

- [ ] **Step 7: 跑全部 config 测试 + 提交**

```bash
cd src/agent && go test ./internal/agent -run TestLoadConfig -v
git add src/agent/internal/agent/config.go src/agent/internal/agent/config_test.go src/agent/internal/agent/config_meta.go agent.conf
git commit -m "feat(agent): add [benchmark] config section"
```

---

## Task 2: 加 benchmark 根目录探测

**Files:**

- Create: `src/agent/internal/agent/benchmark.go`
- Test: `src/agent/internal/agent/benchmark_test.go`

- [ ] **Step 1: 写失败测试**

新建 `benchmark_test.go`：

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBenchmarkDir_FlagWins(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveBenchmarkDir(dir, BenchmarkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveBenchmarkDir_ConfigDirWins(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveBenchmarkDir("", BenchmarkConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveBenchmarkDir_EnvWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AIDEN_BENCHMARK_DIR", dir)
	got, err := resolveBenchmarkDir("", BenchmarkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveBenchmarkDir_UserdataExists(t *testing.T) {
	tmp := t.TempDir()
	userdata := filepath.Join(tmp, "userdata", "agent", "benchmark")
	if err := os.MkdirAll(userdata, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIDEN_BENCHMARK_DIR", "")
	t.Setenv("AIDEN_BENCHMARK_USERDATA_ROOT", tmp)
	got, err := resolveBenchmarkDir("", BenchmarkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != userdata {
		t.Errorf("got %q, want %q", got, userdata)
	}
}

func TestResolveBenchmarkDir_NotFound(t *testing.T) {
	t.Setenv("AIDEN_BENCHMARK_DIR", "")
	t.Setenv("AIDEN_BENCHMARK_USERDATA_ROOT", t.TempDir())
	t.Chdir(t.TempDir())
	_, err := resolveBenchmarkDir("", BenchmarkConfig{})
	if err == nil {
		t.Errorf("expected error when no candidate exists")
	}
}
```

注：`AIDEN_BENCHMARK_USERDATA_ROOT` 是测试 hook，让 `/userdata/agent/benchmark` 路径可被替换。生产环境不读这个 env。

- [ ] **Step 2: 跑测试确认失败**

```bash
cd src/agent && go test ./internal/agent -run TestResolveBenchmarkDir -v
```

期望：FAIL（`resolveBenchmarkDir` 未定义）。

- [ ] **Step 3: 实现探测函数**

新建 `benchmark.go`：

```go
package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// resolveBenchmarkDir picks the first existing benchmark root from this
// priority order:
//   1. CLI flag value (passed in via flagValue)
//   2. AIDEN_BENCHMARK_DIR env var
//   3. cfg.Dir from agent.conf [benchmark] benchmark_dir
//   4. /userdata/agent/benchmark (overridable via AIDEN_BENCHMARK_USERDATA_ROOT for tests)
//   5. <cwd>/benchmark
func resolveBenchmarkDir(flagValue string, cfg BenchmarkConfig) (string, error) {
	candidates := []string{}
	if flagValue != "" {
		candidates = append(candidates, flagValue)
	}
	if env := os.Getenv("AIDEN_BENCHMARK_DIR"); env != "" {
		candidates = append(candidates, env)
	}
	if cfg.Dir != "" {
		candidates = append(candidates, cfg.Dir)
	}

	userdataRoot := os.Getenv("AIDEN_BENCHMARK_USERDATA_ROOT")
	if userdataRoot == "" {
		userdataRoot = "/"
	}
	candidates = append(candidates, filepath.Join(userdataRoot, "userdata", "agent", "benchmark"))

	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "benchmark"))
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c, nil
		}
	}

	return "", fmt.Errorf("no benchmark directory found among candidates: %v", candidates)
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
cd src/agent && go test ./internal/agent -run TestResolveBenchmarkDir -v
```

期望：PASS。

- [ ] **Step 5: 提交**

```bash
git add src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go
git commit -m "feat(agent): add benchmark dir resolver"
```

---

## Task 3: Server 嵌入 BenchmarkDir + CLI flag

**Files:**

- Modify: `src/agent/internal/agent/server.go` (Server 结构体加字段)
- Modify: `src/agent/cmd/daemon/main.go` (加 -benchmark-dir flag)
- Test: `src/agent/internal/agent/server_test.go` (如已存在；否则只写编译能过的简单断言)

- [ ] **Step 1: 读 server.go 结构体定义位置**

```bash
grep -n "type Server struct" /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites/src/agent/internal/agent/server.go
```

记下行号。打开 file 看 Server 结构体当前字段。

- [ ] **Step 2: 加 BenchmarkDir 字段**

在 `Server` 结构体加（紧贴现有字段，注意类型对齐）：

```go
	benchmarkDir string
```

在 `NewServer`（grep 找）签名加 `benchmarkDir string` 参数；初始化时赋值给 `s.benchmarkDir`。

- [ ] **Step 3: daemon main 加 flag**

在 `cmd/daemon/main.go` 找到现有 `flag.String` 调用位置，追加：

```go
	benchmarkDir := flag.String("benchmark-dir", "", "Override benchmark root directory (default: auto-detect)")
```

在调用 `NewServer` 处把 `*benchmarkDir` 传入；在更上层先用 `resolveBenchmarkDir(*benchmarkDir, cfg.Benchmark)` 解析（如解析失败，**不阻止启动**——只 log warning，benchmark 路由后续返回 503）。

- [ ] **Step 4: 编译通过 + 起一次 agent**

```bash
cd src/agent && go build ./...
cd /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites && timeout 3 build/bin/agent -config $PWD/agent.conf -addr 127.0.0.1:18080 || true
```

确认日志里能看到 benchmark dir 解析成功（`benchmark/` 目录存在）。

- [ ] **Step 5: 提交**

```bash
git add src/agent/internal/agent/server.go src/agent/cmd/daemon/main.go
git commit -m "feat(agent): wire benchmark dir into Server"
```

---

## Task 4: GET /benchmark/suites（列 suites）

**Files:**

- Modify: `src/agent/internal/agent/benchmark.go` (handler)
- Modify: `src/agent/internal/agent/server.go` (路由注册)
- Modify: `src/agent/internal/agent/benchmark_test.go`

参考实现：`src/config_web.cpp` 第 3699-3725 行。返回 `[{name, path, custom}]`，按 `name` 排序，`custom` = path 含 `/custom/`。

- [ ] **Step 1: 写失败测试**

加到 `benchmark_test.go`（顶部 import 加 `encoding/json`、`net/http`、`net/http/httptest`、`sort`）：

```go
func TestHandleBenchmarkSuites_ListsJSON(t *testing.T) {
	root := t.TempDir()
	suites := filepath.Join(root, "suites")
	os.MkdirAll(filepath.Join(suites, "custom"), 0o755)
	os.WriteFile(filepath.Join(suites, "perception_v1.json"), []byte(`{"name":"perception_v1","tasks":[]}`), 0o644)
	os.WriteFile(filepath.Join(suites, "custom", "mine.json"), []byte(`{"name":"mine","tasks":[]}`), 0o644)
	os.WriteFile(filepath.Join(suites, "._noise.json"), []byte("{}"), 0o644)

	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/suites", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkSuites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 2 {
		t.Fatalf("got %d suites: %+v", len(got), got)
	}
	for _, s := range got {
		if s["name"].(string) == "mine" && s["custom"] != true {
			t.Errorf("expected mine.custom = true")
		}
		if s["name"].(string) == "perception_v1" && s["custom"] != false {
			t.Errorf("expected perception_v1.custom = false")
		}
	}
}

func TestHandleBenchmarkSuites_NoBenchmarkDir(t *testing.T) {
	s := &Server{benchmarkDir: ""}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/suites", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkSuites(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkSuites -v
```

期望：FAIL（`s.handleBenchmarkSuites` 未定义）。

- [ ] **Step 3: 实现 handler**

`benchmark.go` 末尾追加（顶部 import 加 `encoding/json`、`fmt`、`net/http`、`os`、`path/filepath`、`sort`、`strings`）：

```go
type suiteListItem struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Custom bool   `json:"custom"`
}

func (s *Server) handleBenchmarkSuites(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	suitesDir := filepath.Join(s.benchmarkDir, "suites")
	var items []suiteListItem
	filepath.Walk(suitesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "._") || !strings.HasSuffix(base, ".json") {
			return nil
		}
		rel, _ := filepath.Rel(suitesDir, path)
		items = append(items, suiteListItem{
			Name:   strings.TrimSuffix(base, ".json"),
			Path:   path,
			Custom: strings.HasPrefix(rel, "custom"+string(filepath.Separator)),
		})
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}
```

- [ ] **Step 4: 注册路由**

`server.go` Start() 路由块追加：

```go
	mux.HandleFunc("/benchmark/suites", s.handleBenchmarkSuites)
```

- [ ] **Step 5: 跑测试 + 提交**

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkSuites -v
git add src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go src/agent/internal/agent/server.go
git commit -m "feat(agent): GET /benchmark/suites"
```

---

## Task 5: GET /benchmark/runs

**Files:**

- Modify: `src/agent/internal/agent/benchmark.go`
- Modify: `src/agent/internal/agent/server.go`
- Modify: `src/agent/internal/agent/benchmark_test.go`

参考：`src/config_web.cpp` 第 3727-3770 行。返回结构 `[{run_id, suite, totals: {passed, failed}}]`，按 run_id reverse 排，最多 20。

**重要前置**：实施时确认 totals 数据从哪儿读。先跑：

```bash
ls /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites/benchmark/runs/2026-06-01_062449/
```

如有 `summary.json` 用它；否则按 C++ 那样从 `report.html` 抓取（用正则 `<td class="pass">(\d+)</td>` 与 `<td class="fail">(\d+)</td>`）。如果都没有，totals 留空对象，UI 那里 fallback 显示 `0/0`。

- [ ] **Step 1: 写失败测试**

```go
func TestHandleBenchmarkRuns_ReverseSorted(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs")
	for _, id := range []string{"2026-05-01_010101", "2026-06-10_010101", "2026-04-15_010101"} {
		os.MkdirAll(filepath.Join(runs, id), 0o755)
	}
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/runs", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkRuns(rec, req)
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 3 || got[0]["run_id"] != "2026-06-10_010101" {
		t.Errorf("got %+v", got)
	}
}

func TestHandleBenchmarkRuns_Caps20(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs")
	for i := 0; i < 25; i++ {
		os.MkdirAll(filepath.Join(runs, fmt.Sprintf("2026-06-10_%06d", i)), 0o755)
	}
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/runs", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkRuns(rec, req)
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 20 {
		t.Errorf("got %d, want 20", len(got))
	}
}
```

- [ ] **Step 2: 跑测试确认失败 → 实现 → 通过**

```go
type runListItem struct {
	RunID  string         `json:"run_id"`
	Suite  string         `json:"suite,omitempty"`
	Totals map[string]int `json:"totals,omitempty"`
}

func (s *Server) handleBenchmarkRuns(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	runsDir := filepath.Join(s.benchmarkDir, "runs")
	entries, _ := os.ReadDir(runsDir)
	names := []string{}
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > 20 {
		names = names[:20]
	}
	items := make([]runListItem, 0, len(names))
	for _, n := range names {
		item := runListItem{RunID: n}
		if data, err := os.ReadFile(filepath.Join(runsDir, n, "summary.json")); err == nil {
			var raw struct {
				Suite  string         `json:"suite"`
				Totals map[string]int `json:"totals"`
			}
			if json.Unmarshal(data, &raw) == nil {
				item.Suite = raw.Suite
				item.Totals = raw.Totals
			}
		}
		items = append(items, item)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}
```

注册：

```go
	mux.HandleFunc("/benchmark/runs", s.handleBenchmarkRuns)
```

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkRuns -v
git add src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go src/agent/internal/agent/server.go
git commit -m "feat(agent): GET /benchmark/runs"
```

---

## Task 6: GET /benchmark/report/<id>

**Files:** 同上

参考：`src/config_web.cpp` 第 3771-3800 行。

- [ ] **Step 1: 写失败测试**

```go
func TestHandleBenchmarkReport_ServesHTML(t *testing.T) {
	root := t.TempDir()
	id := "2026-06-10_010101"
	os.MkdirAll(filepath.Join(root, "runs", id), 0o755)
	body := []byte("<html><body>Hello</body></html>")
	os.WriteFile(filepath.Join(root, "runs", id, "report.html"), body, 0o644)
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/report/"+id, nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkReport(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != string(body) {
		t.Errorf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleBenchmarkReport_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	s := &Server{benchmarkDir: root}
	for _, p := range []string{
		"/benchmark/report/../etc/passwd",
		"/benchmark/report/foo/bar",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		s.handleBenchmarkReport(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("path %s: should not 200", p)
		}
	}
}

func TestHandleBenchmarkReport_NotFound(t *testing.T) {
	s := &Server{benchmarkDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/report/2026-06-10", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkReport(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: 实现 + 注册 + 跑测试 + 提交**

```go
func (s *Server) handleBenchmarkReport(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/benchmark/report/")
	safe := sanitizeRunID(id)
	if safe == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.benchmarkDir, "runs", safe, "report.html"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func sanitizeRunID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
```

```go
	mux.HandleFunc("/benchmark/report/", s.handleBenchmarkReport)
```

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkReport -v
git add src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go src/agent/internal/agent/server.go
git commit -m "feat(agent): GET /benchmark/report/<id>"
```

---

## Task 7: GET /benchmark/status（含 self-heal）

参考：`src/config_web.cpp` 3801-3840 + `benchmark_runner_alive()` 3640-3680。

`Server` 加字段：

```go
	benchmarkPIDFile string  // path to runner.pid; defaults to /tmp/benchmark_runner.pid
```

`NewServer` 默认设为 `/tmp/benchmark_runner.pid`。

- [ ] **Step 1: 写失败测试**（test imports 加 `time`）

```go
func TestHandleBenchmarkStatus_PassThroughIdle(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"status":"idle"}`), 0o644)
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/status", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkStatus(rec, req)
	if !strings.Contains(rec.Body.String(), `"status":"idle"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestHandleBenchmarkStatus_SelfHealStaleRunning(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	os.WriteFile(statePath, []byte(`{"status":"running","suite":"x"}`), 0o644)
	old := time.Now().Add(-30 * time.Second)
	os.Chtimes(statePath, old, old)
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	os.WriteFile(pidFile, []byte("99999999"), 0o644)
	s := &Server{benchmarkDir: root, benchmarkPIDFile: pidFile}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/status", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkStatus(rec, req)
	if !strings.Contains(rec.Body.String(), `"recovered":true`) {
		t.Errorf("body = %s", rec.Body.String())
	}
	data, _ := os.ReadFile(statePath)
	if !strings.Contains(string(data), `"idle"`) {
		t.Errorf("state.json after self-heal: %s", data)
	}
}

func TestHandleBenchmarkStatus_GracePeriod(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"status":"running"}`), 0o644)
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	os.WriteFile(pidFile, []byte("99999999"), 0o644)
	s := &Server{benchmarkDir: root, benchmarkPIDFile: pidFile}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/status", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkStatus(rec, req)
	if !strings.Contains(rec.Body.String(), `"running"`) {
		t.Errorf("expected to keep running within grace; got %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: 实现**

```go
const benchmarkStartupGraceSec = 10

func (s *Server) handleBenchmarkStatus(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	statePath := filepath.Join(s.benchmarkDir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"idle"}`))
		return
	}
	body := string(data)
	if strings.Contains(body, `"running"`) {
		info, _ := os.Stat(statePath)
		ageSec := int64(-1)
		if info != nil {
			ageSec = int64(time.Since(info.ModTime()).Seconds())
		}
		past := ageSec < 0 || ageSec > benchmarkStartupGraceSec
		if past && !s.benchmarkRunnerAlive() {
			body = `{"status":"idle","recovered":true}`
			os.WriteFile(statePath, []byte(`{"status":"idle"}`), 0o644)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(body))
}

func (s *Server) benchmarkRunnerAlive() bool {
	pidFile := s.benchmarkPIDFile
	if pidFile == "" {
		pidFile = "/tmp/benchmark_runner.pid"
	}
	if data, err := os.ReadFile(pidFile); err == nil {
		var pid int
		if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil && pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					return true
				}
			}
		}
	}
	out, err := exec.Command("sh", "-c",
		"ps -w 2>/dev/null | grep -F 'runner.main' | grep -v grep | head -1").Output()
	if err != nil {
		return true
	}
	return len(strings.TrimSpace(string(out))) > 0
}
```

import 加：`os/exec`、`syscall`、`time`。

```go
	mux.HandleFunc("/benchmark/status", s.handleBenchmarkStatus)
```

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkStatus -v
git add src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go src/agent/internal/agent/server.go
git commit -m "feat(agent): GET /benchmark/status with self-heal"
```

---

## Task 8: GET /benchmark/log

参考：`src/config_web.cpp` 第 3842-3870 行。读 `/tmp/benchmark_run.log` 末 64 KB。

`Server` 加字段：

```go
	benchmarkLogPath string  // defaults to /tmp/benchmark_run.log
```

- [ ] **Step 1: 写失败测试**

```go
func TestHandleBenchmarkLog_TailsLast64KB(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	os.WriteFile(logPath, []byte(strings.Repeat("a", 100*1024)), 0o644)
	s := &Server{benchmarkLogPath: logPath}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/log", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkLog(rec, req)
	if rec.Body.Len() > 64*1024 {
		t.Errorf("body len = %d, want <= 64KB", rec.Body.Len())
	}
	if rec.Body.Len() < 60*1024 {
		t.Errorf("body len = %d, expected ~64KB", rec.Body.Len())
	}
}

func TestHandleBenchmarkLog_MissingReturnsEmpty(t *testing.T) {
	s := &Server{benchmarkLogPath: filepath.Join(t.TempDir(), "nonexistent")}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/log", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkLog(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("code=%d len=%d", rec.Code, rec.Body.Len())
	}
}
```

- [ ] **Step 2: 实现**

```go
const benchmarkLogTailBytes = 64 * 1024

func (s *Server) handleBenchmarkLog(w http.ResponseWriter, r *http.Request) {
	logPath := s.benchmarkLogPath
	if logPath == "" {
		logPath = "/tmp/benchmark_run.log"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	size := info.Size()
	start := int64(0)
	if size > benchmarkLogTailBytes {
		start = size - benchmarkLogTailBytes
	}
	if _, err := f.Seek(start, 0); err != nil {
		return
	}
	if start > 0 {
		// Skip partial first line.
		buf := make([]byte, 1)
		for {
			if _, err := f.Read(buf); err != nil {
				return
			}
			if buf[0] == '\n' {
				break
			}
		}
	}
	io.Copy(w, f)
}
```

import 加 `io`。

```go
	mux.HandleFunc("/benchmark/log", s.handleBenchmarkLog)
```

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkLog -v
git add src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go src/agent/internal/agent/server.go
git commit -m "feat(agent): GET /benchmark/log"
```

---

## Task 9: GET /benchmark（首页 HTML）

**Files:**

- Create: `src/agent/internal/agent/benchmark_html.go`
- Modify: `src/agent/internal/agent/benchmark.go`
- Modify: `src/agent/internal/agent/server.go`
- Modify: `src/agent/internal/agent/benchmark_test.go`

参考：`src/config_web.cpp` 中 `benchmark_html_page()` 函数（grep `benchmark_html_page` 找返回的 raw-string）。整段 HTML/JS 1:1 拷贝到 Go const。

- [ ] **Step 1: 找到 C++ 源 HTML**

```bash
grep -n "benchmark_html_page\|HTML page" /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites/src/config_web.cpp | head
```

打开该函数，复制 raw-string-literal 整段（通常以 `R"HTML(` 或类似定界符开始，到 `)HTML"` 结束）。

- [ ] **Step 2: 写失败测试**

```go
func TestHandleBenchmarkIndex_ServesHTMLWithRouterButtons(t *testing.T) {
	s := &Server{benchmarkDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodGet, "/benchmark", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, marker := range []string{
		`fetch('/benchmark/suites')`,
		`fetch('/benchmark/runs')`,
		`fetch('/benchmark/status')`,
		`/benchmark/record`,  // 新增的录入器入口
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("body missing %q", marker)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("ct = %q", ct)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkIndex -v
```

- [ ] **Step 4: 创建 benchmark_html.go**

```go
package agent

// benchmarkIndexHTML is the benchmark management page served at GET /benchmark.
// Migrated 1:1 from config_web.cpp benchmark_html_page() during the 2026-06-10
// migration; only added the "/benchmark/record" link at the top.
const benchmarkIndexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Benchmark</title>
<!-- TODO during implementation: paste the full HTML/JS body from
     config_web.cpp benchmark_html_page() raw-string-literal here.
     Keep all fetch('/benchmark/...') paths unchanged.
     Add a top-of-page link:
       <a href="/benchmark/record">📷 Record perception task</a>
-->
</head>
<body>
<!-- placeholder; replace per TODO above -->
</body>
</html>
`
```

**实施时**：把 `<head>` 后的注释占位换成 `config_web.cpp` raw-string 中 `<style>...</style></head><body>...<script>...</script></body>` 的所有内容（约 160 行）。在文档顶部 navigation 区加一个 `<a href="/benchmark/record">📷 Record perception task</a>` 链接。

校验：实施完成后再次用 grep 验证：

```bash
grep -c "fetch('/benchmark/suites')" src/agent/internal/agent/benchmark_html.go
```

应当 ≥ 1。

- [ ] **Step 5: 实现 handler**

`benchmark.go` 末尾加：

```go
func (s *Server) handleBenchmarkIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(benchmarkIndexHTML))
}
```

注册：

```go
	mux.HandleFunc("/benchmark", s.handleBenchmarkIndex)
```

- [ ] **Step 6: 跑测试 + 提交**

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkIndex -v
git add src/agent/internal/agent/benchmark_html.go src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go src/agent/internal/agent/server.go
git commit -m "feat(agent): GET /benchmark index page"
```

---

## Task 10: POST /benchmark/run（double-fork 起 Python runner）

**Files:**

- Create: `src/agent/internal/agent/benchmark_runner.go`
- Modify: `src/agent/internal/agent/server.go`
- Modify: `src/agent/internal/agent/benchmark_test.go`

参考：`src/config_web.cpp` 第 3871-3909 行。脚本字面值要保持，只把 `--judge-model` 和 `OPENROUTER_API_KEY` 来源换成 Go 内存中的值；保留 `cd /userdata/agent/benchmark`、`python3 -c '...runner.main...'`、`runner_pid=$!`、`/tmp/benchmark_runner.pid`、`/tmp/benchmark_run.log`、`echo idle` 这些细节。

`Server` 加字段：

```go
	benchmarkStatePath string  // defaults to <benchmarkDir>/state.json
```

- [ ] **Step 1: 写失败测试**

测试用 `--dry-run` 模式：把 sh 命令换成一个测试用替代命令（注入到 Server 字段）。简化为：

```go
func TestHandleBenchmarkRun_RejectsMissingSuite(t *testing.T) {
	s := &Server{benchmarkDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkRun(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleBenchmarkRun_WritesStateFile(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	// Stub the launcher so the test does not actually fork python.
	called := false
	s := &Server{
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkLauncher: func(suite, judge, apiKey string) error {
			called = true
			return nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run",
		strings.NewReader(`{"suite":"/tmp/x.json"}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("launcher was not invoked")
	}
	data, _ := os.ReadFile(statePath)
	if !strings.Contains(string(data), `"running"`) {
		t.Errorf("state.json = %s", data)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkRun -v
```

- [ ] **Step 3: 创建 benchmark_runner.go**

```go
package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

// benchmarkLauncherFunc is the type of function used to start the Python
// runner. Production code uses launchBenchmarkRunner; tests inject a stub.
type benchmarkLauncherFunc func(suitePath, judgeModel, openRouterAPIKey string) error

// launchBenchmarkRunner double-forks a sh script that runs the benchmark and
// writes /tmp/benchmark_runner.pid + /tmp/benchmark_run.log + updates
// <root>/state.json on completion. This mirrors the C++ implementation in
// config_web.cpp from the pre-migration era exactly, so the runner side does
// not need to change.
func (s *Server) launchBenchmarkRunner(suitePath, judgeModel, openRouterAPIKey string) error {
	if judgeModel == "" {
		judgeModel = "bytedance-seed/seed-2.0-lite"
	}
	suiteShell := shellQuote(suitePath)
	script := fmt.Sprintf(`cd /userdata/agent/benchmark && `+
		`export OPENROUTER_API_KEY=%s && `+
		`(`+
		`echo 'Starting benchmark...' > /tmp/benchmark_run.log; `+
		`python3 -c 'import sys;sys.path.insert(0,".");from runner.main import cli;sys.exit(cli())' `+
		`run --suite %s `+
		`--agent-url http://127.0.0.1:8080 `+
		`--judge-model %s `+
		`--state-file /userdata/agent/benchmark/state.json `+
		`>> /tmp/benchmark_run.log 2>&1 & `+
		`runner_pid=$!; echo $runner_pid > /tmp/benchmark_runner.pid; `+
		`wait $runner_pid; rm -f /tmp/benchmark_runner.pid; `+
		`echo '{"status":"idle"}' > /userdata/agent/benchmark/state.json) &`,
		shellQuote(openRouterAPIKey), suiteShell, shellQuote(judgeModel))
	return exec.Command("sh", "-c", script).Start()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (s *Server) handleBenchmarkRun(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Suite string `json:"suite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Suite == "" {
		http.Error(w, `{"error":"missing suite field"}`, http.StatusBadRequest)
		return
	}
	statePath := s.benchmarkStatePath
	if statePath == "" {
		statePath = filepath.Join(s.benchmarkDir, "state.json")
	}
	state := fmt.Sprintf(`{"status":"running","suite":%q}`, req.Suite)
	if err := os.WriteFile(statePath, []byte(state), 0o644); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	launch := s.benchmarkLauncher
	if launch == nil {
		launch = s.launchBenchmarkRunner
	}
	apiKey := s.cfg.Model.APIKey
	judge := s.cfg.Benchmark.JudgeModel
	if err := launch(req.Suite, judge, apiKey); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"launch failed: %s"}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"status":"running"}`))
}
```

`Server` 加字段：

```go
	benchmarkLauncher benchmarkLauncherFunc  // injected for tests
```

注：`s.cfg` 需要存在；`Server` 现在已经存了 config（grep 确认；没有就在 Step 3 前加上 `cfg Config` 字段并在 NewServer 赋值）。

import 顶部加：`strings`。

- [ ] **Step 4: 注册路由 + 跑测试 + 提交**

```go
	mux.HandleFunc("/benchmark/run", s.handleBenchmarkRun)
```

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkRun -v
git add src/agent/internal/agent/benchmark_runner.go src/agent/internal/agent/server.go src/agent/internal/agent/benchmark_test.go
git commit -m "feat(agent): POST /benchmark/run (double-fork python runner)"
```

---

## Task 11: POST /benchmark/suites/import + delete

**Files:**

- Create: `src/agent/internal/agent/benchmark_import.go`
- Modify: `src/agent/internal/agent/server.go`
- Modify: `src/agent/internal/agent/benchmark_test.go`

参考：`src/config_web.cpp` 第 3910-3974 行 (import) + 3975-4003 行 (delete)。

import 流程：

1. body `{name, json}` 校验
2. name 仅 alnum/-/\_，长度 1-64
3. JSON ≤ 256 KB
4. JSON 解析合法
5. 临时写到 `/tmp/suite_import_<safe_name>.json`
6. 调 `python3 -c "from runner.suite import load_suite; load_suite(...)"` 验证
7. mv 到 `<root>/suites/custom/<safe_name>.json`

delete 流程：sanitize name + `unlink` `<root>/suites/custom/<safe_name>.json`，限定 custom 目录下。

- [ ] **Step 1: 写失败测试**

```go
func TestHandleBenchmarkImport_Validates(t *testing.T) {
	s := &Server{benchmarkDir: t.TempDir()}
	cases := []struct {
		body   string
		status int
	}{
		{`{}`, http.StatusBadRequest},
		{`{"name":"","json":"{}"}`, http.StatusBadRequest},
		{`{"name":"bad/name","json":"{}"}`, http.StatusBadRequest},
		{`{"name":"ok","json":"not json"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/import", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		s.handleBenchmarkImport(rec, req)
		if rec.Code != tc.status {
			t.Errorf("body %s: status = %d, want %d", tc.body, rec.Code, tc.status)
		}
	}
}

func TestHandleBenchmarkImport_WritesCustomFile(t *testing.T) {
	root := t.TempDir()
	// Skip Python validation by stubbing the validator.
	s := &Server{
		benchmarkDir: root,
		benchmarkSuiteValidator: func(path string) error { return nil },
	}
	body := `{"name":"mine","json":"{\"name\":\"mine\",\"tasks\":[]}"}`
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/import", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleBenchmarkImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "suites", "custom", "mine.json")); err != nil {
		t.Errorf("file not written: %v", err)
	}
}

func TestHandleBenchmarkDelete_OnlyCustom(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "suites", "custom"), 0o755)
	os.WriteFile(filepath.Join(root, "suites", "custom", "mine.json"), []byte("{}"), 0o644)
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/delete",
		strings.NewReader(`{"name":"mine"}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkDelete(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "suites", "custom", "mine.json")); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestHandleBenchmarkDelete_RejectsBuiltin(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "suites"), 0o755)
	os.WriteFile(filepath.Join(root, "suites", "perception_v1.json"), []byte("{}"), 0o644)
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/delete",
		strings.NewReader(`{"name":"perception_v1"}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkDelete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "suites", "perception_v1.json")); err != nil {
		t.Errorf("builtin file should still exist: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd src/agent && go test ./internal/agent -run "TestHandleBenchmarkImport|TestHandleBenchmarkDelete" -v
```

- [ ] **Step 3: 实现**

`Server` 加字段：

```go
	benchmarkSuiteValidator func(path string) error  // defaults to validateSuiteWithPython
	benchmarkSuiteLocks     sync.Map                 // map[string]*sync.Mutex per suite name
```

`benchmark_import.go`：

```go
package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const benchmarkImportMaxBytes = 256 * 1024

func sanitizeSuiteName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Server) suiteLock(name string) *sync.Mutex {
	v, _ := s.benchmarkSuiteLocks.LoadOrStore(name, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func validateSuiteWithPython(path string) error {
	cmd := exec.Command("sh", "-c",
		fmt.Sprintf("cd /userdata/agent/benchmark && python3 -c 'import sys; sys.path.insert(0, \".\"); from runner.suite import load_suite; load_suite(\"%s\")' 2>&1", path))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("validation failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Server) handleBenchmarkImport(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Name string `json:"name"`
		JSON string `json:"json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON request"}`, http.StatusBadRequest)
		return
	}
	if body.Name == "" || body.JSON == "" {
		http.Error(w, `{"error":"name and json fields required"}`, http.StatusBadRequest)
		return
	}
	safe := sanitizeSuiteName(body.Name)
	if safe == "" || len(safe) > 64 {
		http.Error(w, `{"error":"name must be 1-64 chars of [A-Za-z0-9_-]"}`, http.StatusBadRequest)
		return
	}
	if len(body.JSON) > benchmarkImportMaxBytes {
		http.Error(w, `{"error":"suite JSON exceeds 256 KB"}`, http.StatusBadRequest)
		return
	}
	var probe any
	if err := json.Unmarshal([]byte(body.JSON), &probe); err != nil {
		http.Error(w, `{"error":"suite is not valid JSON"}`, http.StatusBadRequest)
		return
	}

	lock := s.suiteLock(safe)
	lock.Lock()
	defer lock.Unlock()

	tmpPath := filepath.Join(os.TempDir(), "suite_import_"+safe+".json")
	if err := os.WriteFile(tmpPath, []byte(body.JSON), 0o644); err != nil {
		http.Error(w, `{"error":"failed to open temp file"}`, http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpPath)

	validator := s.benchmarkSuiteValidator
	if validator == nil {
		validator = validateSuiteWithPython
	}
	if err := validator(tmpPath); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	destDir := filepath.Join(s.benchmarkDir, "suites", "custom")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	destPath := filepath.Join(destDir, safe+".json")
	if err := os.Rename(tmpPath, destPath); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to save suite: %s"}`, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"ok": true, "name": safe, "path": destPath}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleBenchmarkDelete(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
		return
	}
	safe := sanitizeSuiteName(body.Name)
	if safe == "" {
		http.Error(w, `{"error":"invalid name"}`, http.StatusBadRequest)
		return
	}
	path := filepath.Join(s.benchmarkDir, "suites", "custom", safe+".json")
	if err := os.Remove(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
```

注册：

```go
	mux.HandleFunc("/benchmark/suites/import", s.handleBenchmarkImport)
	mux.HandleFunc("/benchmark/suites/delete", s.handleBenchmarkDelete)
```

- [ ] **Step 4: 跑测试 + 提交**

```bash
cd src/agent && go test ./internal/agent -run "TestHandleBenchmarkImport|TestHandleBenchmarkDelete" -v
git add src/agent/internal/agent/benchmark_import.go src/agent/internal/agent/server.go src/agent/internal/agent/benchmark_test.go
git commit -m "feat(agent): POST /benchmark/suites/import and /delete"
```

---

## Task 12: POST /benchmark/suites/generate（纯文本 LLM）

**Files:**

- Create: `src/agent/internal/agent/benchmark_generate.go`
- Create: `src/agent/internal/agent/benchmark_prompt.go` (system prompt const)
- Modify: `src/agent/internal/agent/server.go`
- Modify: `src/agent/internal/agent/benchmark_test.go`

参考：`src/config_web.cpp` 第 2843-3120 行 `generate_suite_with_llm`。

**关键约束**：system prompt 必须 1:1 复制 C++ 字符串，不改一字。位置在 `config_web.cpp` 第 2926 行起，到 2982 行 `"OUTPUT: JSON only"` 结束。

- [ ] **Step 1: 建 benchmark_prompt.go 占位**

```go
package agent

// benchmarkGenerateSystemPrompt is the system prompt used for text-only suite
// generation. Migrated 1:1 from config_web.cpp:2926-2982. DO NOT EDIT WITHOUT
// re-running the prompt regression test.
const benchmarkGenerateSystemPrompt = `<TODO: paste from config_web.cpp lines 2926-2982>`
```

**实施时**：把 C++ 那段 `system_prompt = "...."` 拼接的字符串还原成完整字符串，贴到 backtick raw-string 内。注意每行 C++ 末尾的 `\n` 在 Go raw-string 里写为字面换行；C++ 的 `\\` 在 Go raw-string 里写为 `\`；`\"` 在 Go raw-string 里写为 `"`。

**diff 验证脚本**（实施完成后跑一次确认 1:1）：

```bash
cd /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites
# Extract C++ literal:
sed -n '2926,2982p' src/config_web.cpp | python3 -c "
import sys, re
src = sys.stdin.read()
# Strip leading 'std::string system_prompt =' and concatenated string literals.
parts = re.findall(r'\"((?:[^\"\\\\]|\\\\.)*)\"', src)
joined = ''.join(parts).encode().decode('unicode_escape')
print(joined, end='')
" > /tmp/cpp_prompt.txt
# Extract Go const value:
go run ./cmd/_dump_prompt > /tmp/go_prompt.txt  # add a small helper main if needed
diff /tmp/cpp_prompt.txt /tmp/go_prompt.txt
```

如果项目没有方便的 dump 工具，最低限度：人工 diff 主要关键词（"BENCHMARK SUITE", "SCHEMA", "TOOL USAGE VALIDATION", "OUTPUT: JSON only"）确实存在；以及行数对得上。

- [ ] **Step 2: 写失败测试**

```go
func TestBenchmarkGenerateSystemPrompt_NotPlaceholder(t *testing.T) {
	if strings.Contains(benchmarkGenerateSystemPrompt, "TODO") {
		t.Fatal("system prompt is still a placeholder; copy from config_web.cpp:2926-2982")
	}
	for _, marker := range []string{
		"OUTPUT: JSON only",
		"SCHEMA:",
		"TOOL USAGE VALIDATION",
	} {
		if !strings.Contains(benchmarkGenerateSystemPrompt, marker) {
			t.Errorf("system prompt missing marker %q", marker)
		}
	}
}

func TestHandleBenchmarkGenerate_UsesFakeLLM(t *testing.T) {
	cfg := Config{
		Model: ModelConfig{
			Provider: "fake",
			Responses: []string{`{"name":"fake_suite","tasks":[]}`},
		},
	}
	mm, err := NewModelManager(cfg.Model, ProxyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg:          cfg,
		modelManager: mm,
	}
	body := `{"prompt":"do thing","name":"fake_suite"}`
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleBenchmarkGenerate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		OK        bool   `json:"ok"`
		SuiteJSON string `json:"suite_json"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.OK || !strings.Contains(got.SuiteJSON, "fake_suite") {
		t.Errorf("got %+v", got)
	}
}
```

注：测试假设 `ModelManager` 已经能注入到 Server。grep `modelManager` 确认 Server 已经持有这个字段；没有就先在 Step 3 前加上。

- [ ] **Step 3: 跑测试确认失败 → 实现**

`benchmark_generate.go`：

````go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

func (s *Server) handleBenchmarkGenerate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Prompt == "" || body.Name == "" {
		http.Error(w, `{"ok":false,"error":"prompt and name required"}`, http.StatusBadRequest)
		return
	}

	toolsList := s.formatToolsListForPrompt()
	userMessage := "User scenario: " + body.Prompt + "\n\nSuite name: " + body.Name +
		"\n\nAvailable tools:\n" + toolsList

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	resp, err := s.modelManager.LLM().GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, benchmarkGenerateSystemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, userMessage),
	}, llms.WithTemperature(0.7), llms.WithMaxTokens(4000))
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}
	if len(resp.Choices) == 0 {
		http.Error(w, `{"ok":false,"error":"empty response"}`, http.StatusBadGateway)
		return
	}
	suiteJSON := strings.TrimSpace(resp.Choices[0].Content)
	suiteJSON = strings.TrimPrefix(suiteJSON, "```json")
	suiteJSON = strings.TrimPrefix(suiteJSON, "```")
	suiteJSON = strings.TrimSuffix(suiteJSON, "```")
	suiteJSON = strings.TrimSpace(suiteJSON)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "suite_json": suiteJSON})
}

// formatToolsListForPrompt mirrors C++ logic in config_web.cpp:2879-2923 for
// building the tool list section of the prompt. Keeps format identical.
func (s *Server) formatToolsListForPrompt() string {
	tools := s.tools()  // existing accessor; grep s.tools to confirm signature
	var b strings.Builder
	for i, tool := range tools {
		if i >= 30 {
			break
		}
		b.WriteString("  - " + tool.Name + "(")
		first := true
		for paramName := range tool.Parameters {
			if !first {
				b.WriteString(", ")
			}
			b.WriteString(paramName)
			first = false
		}
		b.WriteString(")\n")
	}
	if b.Len() == 0 {
		return "  - keyboard_tap(keys)\n  - keyboard_text(text)\n  - mouse_click(x, y, button, coord_space)\n  - screenshot()\n"
	}
	return b.String()
}
````

import 加 `time`。

注：`s.tools()` 和 `tool.Name`/`tool.Parameters` 命名需要按 agent 现有结构调整。grep `handleTools` 找到现有的 tools 列表生成逻辑，复用同一份数据源（`s.toolset` 或类似）以避免数据漂移。

注册：

```go
	mux.HandleFunc("/benchmark/suites/generate", s.handleBenchmarkGenerate)
```

- [ ] **Step 4: 跑测试 + 提交**

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkGenerate -v
git add src/agent/internal/agent/benchmark_generate.go src/agent/internal/agent/benchmark_prompt.go src/agent/internal/agent/server.go src/agent/internal/agent/benchmark_test.go
git commit -m "feat(agent): POST /benchmark/suites/generate (text LLM)"
```

---

## Task 13: GET /user_files + POST /user_files/regenerate

**Files:**

- Create: `src/agent/internal/agent/user_files.go`
- Modify: `src/agent/internal/agent/server.go`
- Create: `src/agent/internal/agent/user_files_test.go`

参考：`src/config_web.cpp` 第 4033-4090 行 + `ensure_user_files_report` 第 544-569 行。

`Server` 加字段：

```go
	userFilesReportPath string  // /userdata/agent/files_report.html
	userFilesToolsDir   string  // /userdata/agent_tools
```

`NewServer` 默认设这两个值（设备路径）。测试可注入 tmpdir。

- [ ] **Step 1: 写失败测试**

```go
func TestHandleUserFiles_ServesExistingReport(t *testing.T) {
	dir := t.TempDir()
	rpt := filepath.Join(dir, "files_report.html")
	os.WriteFile(rpt, []byte("<html>cached</html>"), 0o644)
	s := &Server{userFilesReportPath: rpt}
	req := httptest.NewRequest(http.MethodGet, "/user_files", nil)
	rec := httptest.NewRecorder()
	s.handleUserFiles(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cached") {
		t.Errorf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleUserFiles_GeneratesIfMissing(t *testing.T) {
	tools := t.TempDir()
	rpt := filepath.Join(t.TempDir(), "report.html")
	// Stub the script: write a known body to the output and exit 0.
	script := filepath.Join(tools, "view_agent_files.sh")
	body := `#!/bin/sh
echo '<html>generated</html>' > ` + rpt
	os.WriteFile(script, []byte(body), 0o755)
	s := &Server{userFilesReportPath: rpt, userFilesToolsDir: tools}
	req := httptest.NewRequest(http.MethodGet, "/user_files", nil)
	rec := httptest.NewRecorder()
	s.handleUserFiles(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "generated") {
		t.Errorf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleUserFilesRegenerate_RunsAsync(t *testing.T) {
	tools := t.TempDir()
	rpt := filepath.Join(t.TempDir(), "report.html")
	marker := filepath.Join(t.TempDir(), "marker")
	script := filepath.Join(tools, "view_agent_files.sh")
	body := `#!/bin/sh
sleep 0.1
touch ` + marker + `
echo '<html>regen</html>' > ` + rpt
	os.WriteFile(script, []byte(body), 0o755)
	s := &Server{userFilesReportPath: rpt, userFilesToolsDir: tools}
	req := httptest.NewRequest(http.MethodPost, "/user_files/regenerate", nil)
	rec := httptest.NewRecorder()
	s.handleUserFilesRegenerate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	// Wait up to 2s for the async script to complete.
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("script did not run within timeout")
}
```

- [ ] **Step 2: 跑测试确认失败 → 实现**

`user_files.go`：

```go
package agent

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

const userFilesGenerateTimeoutMs = 30 * 1000

func (s *Server) ensureUserFilesReport() error {
	if _, err := os.Stat(s.userFilesReportPath); err == nil {
		return nil
	}
	cmd := exec.Command("sh", "-c", "cd "+shellQuote(s.userFilesToolsDir)+" && ./view_agent_files.sh 2>&1")
	timer := time.AfterFunc(time.Duration(userFilesGenerateTimeoutMs)*time.Millisecond, func() {
		_ = cmd.Process.Kill()
	})
	out, err := cmd.CombinedOutput()
	timer.Stop()
	if err != nil {
		return fmt.Errorf("user_files generation failed: %s", string(out))
	}
	if _, err := os.Stat(s.userFilesReportPath); err != nil {
		return fmt.Errorf("script ran but report not produced")
	}
	return nil
}

func (s *Server) handleUserFiles(w http.ResponseWriter, r *http.Request) {
	if err := s.ensureUserFilesReport(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, err := os.ReadFile(s.userFilesReportPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleUserFilesRegenerate(w http.ResponseWriter, r *http.Request) {
	go func() {
		cmd := exec.Command("sh", "-c", "cd "+shellQuote(s.userFilesToolsDir)+" && ./view_agent_files.sh > /tmp/user_files_regenerate.log 2>&1")
		_ = cmd.Run()
	}()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"regenerating":true}`))
}
```

注册：

```go
	mux.HandleFunc("/user_files", s.handleUserFiles)
	mux.HandleFunc("/user_files/regenerate", s.handleUserFilesRegenerate)
```

`NewServer` 设默认值：

```go
	s.userFilesReportPath = "/userdata/agent/files_report.html"
	s.userFilesToolsDir = "/userdata/agent_tools"
```

- [ ] **Step 3: 跑测试 + 提交**

```bash
cd src/agent && go test ./internal/agent -run TestHandleUserFiles -v
git add src/agent/internal/agent/user_files.go src/agent/internal/agent/user_files_test.go src/agent/internal/agent/server.go
git commit -m "feat(agent): /user_files + /user_files/regenerate"
```

---

## Task 14: 录入器 — generate-perception

**Files:**

- Modify: `src/agent/internal/agent/benchmark_generate.go` (加新 handler)
- Create: `src/agent/internal/agent/benchmark_perception_prompt.go`
- Modify: `src/agent/internal/agent/server.go`
- Modify: `src/agent/internal/agent/benchmark_test.go`

新增端点 `POST /benchmark/suites/generate-perception`，给 LLM 发图 + 文本，返回单 task JSON；后端兜底坐标范围和 slug。

- [ ] **Step 1: 写 perception system prompt**

新建 `benchmark_perception_prompt.go`：

```go
package agent

const benchmarkPerceptionSystemPrompt = `You generate a single PERCEPTION-class benchmark task for a device-control agent.

Output ONLY a single JSON object (no markdown, no explanation, no fences) with this exact shape:

{
  "task": {
    "id": "<task_id>",
    "category": "perception",
    "input_screenshot": "screenshots/<task_id>.jpg",
    "prompt": "<short instruction in same language as user_intent>",
    "description_for_judge": "<English explanation of what success looks like>",
    "rubric": [
      {"id": "called_click_tool", "check": "The tool trace contains at least one touch_gesture or mouse_click call."},
      {"id": "click_targets_<slug>", "check": "The touch/click coordinates target the <target_name> area: normalized x in [PLACEHOLDER_X], y in [PLACEHOLDER_Y]."}
    ],
    "hard_assertions": {"min_tool_calls": 1, "max_tool_calls": 5, "must_complete_within_sec": 120, "response_required": true}
  }
}

The user supplies the screenshot, target rectangle, target_name, task_id, and user_intent. You will receive these in the user message. Use them to fill in:
- "prompt": short imperative in the user's language (Chinese stays Chinese)
- "description_for_judge": English description of how the agent should succeed (mention the visible UI element)
- The rubric "check" wording. Leave the literal placeholders PLACEHOLDER_X and PLACEHOLDER_Y in the second rubric — the server will substitute exact coordinate ranges.
- The slug placeholder <slug> — leave as the literal text "<slug>"; the server replaces it.

Do not invent a screenshot path other than "screenshots/<task_id>.jpg". Do not fabricate coordinate numbers. Output JSON only.`
```

- [ ] **Step 2: 写失败测试**

```go
func TestHandleBenchmarkGeneratePerception_BackfillsCoordsAndSlug(t *testing.T) {
	cfg := Config{
		Model: ModelConfig{
			Provider: "fake",
			Responses: []string{`{"task":{"id":"find_settings","category":"perception","input_screenshot":"screenshots/find_settings.jpg","prompt":"打开设置","description_for_judge":"agent should tap settings","rubric":[{"id":"called_click_tool","check":"..."},{"id":"click_targets_<slug>","check":"target Settings icon, normalized x in [PLACEHOLDER_X], y in [PLACEHOLDER_Y]"}],"hard_assertions":{"min_tool_calls":1,"max_tool_calls":5,"must_complete_within_sec":120,"response_required":true}}}`},
		},
	}
	mm, _ := NewModelManager(cfg.Model, ProxyConfig{})
	s := &Server{cfg: cfg, modelManager: mm}
	body := `{
		"name":"iphone","task_id":"find_settings","user_intent":"打开设置 app",
		"screenshot_b64":"AAAA",
		"target_box_normalized":{"x1":750,"y1":380,"x2":980,"y2":550},
		"target_name":"Settings icon"
	}`
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/generate-perception", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleBenchmarkGeneratePerception(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		OK       bool   `json:"ok"`
		TaskJSON string `json:"task_json"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.OK {
		t.Fatalf("not ok: %s", rec.Body.String())
	}
	for _, marker := range []string{
		"click_targets_settings_icon",      // slug filled
		"normalized x in [0.75, 0.98]",     // x range
		"y in [0.38, 0.55]",                // y range
	} {
		if !strings.Contains(got.TaskJSON, marker) {
			t.Errorf("task_json missing %q\nfull: %s", marker, got.TaskJSON)
		}
	}
	if strings.Contains(got.TaskJSON, "PLACEHOLDER_X") || strings.Contains(got.TaskJSON, "PLACEHOLDER_Y") {
		t.Errorf("placeholders not substituted: %s", got.TaskJSON)
	}
	if strings.Contains(got.TaskJSON, "<slug>") {
		t.Errorf("slug placeholder not substituted")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Settings icon": "settings_icon",
		"WeChat 主界面":   "wechat_",
		"  multi  space ": "multi_space",
		"!@#":             "",
	}
	for in, want := range cases {
		got := slugify(in)
		if got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 3: 实现**

加到 `benchmark_generate.go`：

````go
type perceptionRequest struct {
	Name          string `json:"name"`
	TaskID        string `json:"task_id"`
	UserIntent    string `json:"user_intent"`
	ScreenshotB64 string `json:"screenshot_b64"`
	TargetBox     struct {
		X1 int `json:"x1"`
		Y1 int `json:"y1"`
		X2 int `json:"x2"`
		Y2 int `json:"y2"`
	} `json:"target_box_normalized"`
	TargetName string `json:"target_name"`
}

func slugify(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		case r == ' ' || r == '_' || r == '-':
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.TrimRight(b.String(), "_")
}

func (s *Server) handleBenchmarkGeneratePerception(w http.ResponseWriter, r *http.Request) {
	var req perceptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"ok":false,"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.TaskID == "" || req.UserIntent == "" || req.ScreenshotB64 == "" || req.TargetName == "" {
		http.Error(w, `{"ok":false,"error":"missing required fields"}`, http.StatusBadRequest)
		return
	}

	// Build user message with image part.
	userText := fmt.Sprintf("task_id: %s\nuser_intent: %s\ntarget_name: %s\ntarget rectangle (normalized 0-1000): (%d,%d)-(%d,%d)\n",
		req.TaskID, req.UserIntent, req.TargetName,
		req.TargetBox.X1, req.TargetBox.Y1, req.TargetBox.X2, req.TargetBox.Y2)

	imgURL := "data:image/jpeg;base64," + req.ScreenshotB64
	parts := []llms.ContentPart{
		llms.TextPart(userText),
		llms.ImageURLPart(imgURL),
	}
	msgs := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, benchmarkPerceptionSystemPrompt),
		{Role: llms.ChatMessageTypeHuman, Parts: parts},
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	resp, err := s.modelManager.LLM().GenerateContent(ctx, msgs,
		llms.WithTemperature(0.3), llms.WithMaxTokens(2000))
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}
	if len(resp.Choices) == 0 {
		http.Error(w, `{"ok":false,"error":"empty response"}`, http.StatusBadGateway)
		return
	}
	raw := strings.TrimSpace(resp.Choices[0].Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	// Backfill coords and slug.
	xLo := fmt.Sprintf("%.2f", float64(req.TargetBox.X1)/1000)
	xHi := fmt.Sprintf("%.2f", float64(req.TargetBox.X2)/1000)
	yLo := fmt.Sprintf("%.2f", float64(req.TargetBox.Y1)/1000)
	yHi := fmt.Sprintf("%.2f", float64(req.TargetBox.Y2)/1000)
	slug := slugify(req.TargetName)
	raw = strings.ReplaceAll(raw, "PLACEHOLDER_X", xLo+", "+xHi)
	raw = strings.ReplaceAll(raw, "PLACEHOLDER_Y", yLo+", "+yHi)
	raw = strings.ReplaceAll(raw, "<slug>", slug)

	// Validate it parses.
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":"LLM did not return valid JSON: %s","raw":%q}`, err, raw), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "task_json": raw})
}
````

注册：

```go
	mux.HandleFunc("/benchmark/suites/generate-perception", s.handleBenchmarkGeneratePerception)
```

- [ ] **Step 4: 跑测试 + 提交**

```bash
cd src/agent && go test ./internal/agent -run "TestHandleBenchmarkGeneratePerception|TestSlugify" -v
git add src/agent/internal/agent/benchmark_generate.go src/agent/internal/agent/benchmark_perception_prompt.go src/agent/internal/agent/server.go src/agent/internal/agent/benchmark_test.go
git commit -m "feat(agent): POST /benchmark/suites/generate-perception"
```

---

## Task 15: 录入器 — import-with-assets

**Files:**

- Modify: `src/agent/internal/agent/benchmark_import.go`
- Modify: `src/agent/internal/agent/server.go`
- Modify: `src/agent/internal/agent/benchmark_test.go`

新端点 `POST /benchmark/suites/import-with-assets`，写到 `<root>/suites/custom/<name>/<name>.json` + `<root>/suites/custom/<name>/screenshots/<task_id>.jpg`。

- [ ] **Step 1: 写失败测试**

```go
func TestHandleBenchmarkImportWithAssets_WritesSubdirLayout(t *testing.T) {
	root := t.TempDir()
	s := &Server{
		benchmarkDir:            root,
		benchmarkSuiteValidator: func(string) error { return nil },
	}
	suiteJSON := `{"name":"iphone","tasks":[{"id":"find_settings","category":"perception","input_screenshot":"screenshots/find_settings.jpg","prompt":"x","description_for_judge":"y","rubric":[{"id":"r","check":"r"}],"hard_assertions":{"min_tool_calls":1,"max_tool_calls":5,"must_complete_within_sec":120,"response_required":true}}]}`
	body := fmt.Sprintf(`{
		"name":"iphone",
		"suite_json":%q,
		"assets":[{"task_id":"find_settings","screenshot_b64":"%s"}]
	}`, suiteJSON, base64.StdEncoding.EncodeToString([]byte("FAKEJPEG")))
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/import-with-assets",
		strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleBenchmarkImportWithAssets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	jsonPath := filepath.Join(root, "suites", "custom", "iphone", "iphone.json")
	imgPath := filepath.Join(root, "suites", "custom", "iphone", "screenshots", "find_settings.jpg")
	for _, p := range []string{jsonPath, imgPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	imgData, _ := os.ReadFile(imgPath)
	if string(imgData) != "FAKEJPEG" {
		t.Errorf("image content = %q", imgData)
	}
}

func TestHandleBenchmarkImportWithAssets_BacksUpExisting(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "suites", "custom", "iphone")
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, "iphone.json"), []byte(`{"old":true}`), 0o644)
	s := &Server{
		benchmarkDir:            root,
		benchmarkSuiteValidator: func(string) error { return nil },
	}
	suiteJSON := `{"name":"iphone","tasks":[]}`
	body := fmt.Sprintf(`{"name":"iphone","suite_json":%q,"assets":[]}`, suiteJSON)
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/import-with-assets", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleBenchmarkImportWithAssets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	// .bak.<ts> dir should exist.
	entries, _ := os.ReadDir(filepath.Join(root, "suites", "custom"))
	foundBackup := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "iphone.bak.") {
			foundBackup = true
		}
	}
	if !foundBackup {
		t.Errorf("expected iphone.bak.* backup, got %v", entries)
	}
}
```

测试 import 加 `encoding/base64`。

- [ ] **Step 2: 实现**

加到 `benchmark_import.go`：

```go
const benchmarkImportWithAssetsMaxTotalBytes = 10 * 1024 * 1024  // 10 MB total

type importWithAssetsRequest struct {
	Name      string `json:"name"`
	SuiteJSON string `json:"suite_json"`
	Assets    []struct {
		TaskID        string `json:"task_id"`
		ScreenshotB64 string `json:"screenshot_b64"`
	} `json:"assets"`
}

func (s *Server) handleBenchmarkImportWithAssets(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, benchmarkImportWithAssetsMaxTotalBytes)
	var req importWithAssetsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadRequest)
		return
	}
	safe := sanitizeSuiteName(req.Name)
	if safe == "" || len(safe) > 64 {
		http.Error(w, `{"error":"invalid name"}`, http.StatusBadRequest)
		return
	}
	if req.SuiteJSON == "" {
		http.Error(w, `{"error":"suite_json required"}`, http.StatusBadRequest)
		return
	}
	var suite struct {
		Tasks []struct {
			ID              string `json:"id"`
			Category        string `json:"category"`
			InputScreenshot string `json:"input_screenshot"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(req.SuiteJSON), &suite); err != nil {
		http.Error(w, `{"error":"suite_json not valid JSON"}`, http.StatusBadRequest)
		return
	}
	// Validate every perception task has matching asset.
	assetByID := map[string]string{}
	for _, a := range req.Assets {
		assetByID[a.TaskID] = a.ScreenshotB64
	}
	for _, t := range suite.Tasks {
		if t.Category == "perception" {
			expected := "screenshots/" + t.ID + ".jpg"
			if t.InputScreenshot != expected {
				http.Error(w, fmt.Sprintf(`{"error":"task %q input_screenshot must be %q"}`, t.ID, expected), http.StatusBadRequest)
				return
			}
			if _, ok := assetByID[t.ID]; !ok {
				http.Error(w, fmt.Sprintf(`{"error":"missing asset for task %q"}`, t.ID), http.StatusBadRequest)
				return
			}
		}
	}

	lock := s.suiteLock(safe)
	lock.Lock()
	defer lock.Unlock()

	// Build in tmp dir.
	tmpRoot, err := os.MkdirTemp("", "suite_assets_"+safe+"_")
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpRoot)
	tmpJSON := filepath.Join(tmpRoot, safe+".json")
	if err := os.WriteFile(tmpJSON, []byte(req.SuiteJSON), 0o644); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Join(tmpRoot, "screenshots"), 0o755); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	for taskID, b64 := range assetByID {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"asset %q base64 decode failed: %s"}`, taskID, err), http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(filepath.Join(tmpRoot, "screenshots", taskID+".jpg"), data, 0o644); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
			return
		}
	}

	// Validate via Python.
	validator := s.benchmarkSuiteValidator
	if validator == nil {
		validator = validateSuiteWithPython
	}
	if err := validator(tmpJSON); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	// Move into place.
	dest := filepath.Join(s.benchmarkDir, "suites", "custom", safe)
	if _, err := os.Stat(dest); err == nil {
		bak := dest + ".bak." + time.Now().Format("20060102_150405")
		if err := os.Rename(dest, bak); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"backup failed: %s"}`, err), http.StatusInternalServerError)
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpRoot, dest); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"finalize move failed: %s"}`, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "name": safe, "path": dest})
}
```

import 加 `encoding/base64`、`time`。

注册：

```go
	mux.HandleFunc("/benchmark/suites/import-with-assets", s.handleBenchmarkImportWithAssets)
```

- [ ] **Step 3: 跑测试 + 提交**

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkImportWithAssets -v
git add src/agent/internal/agent/benchmark_import.go src/agent/internal/agent/server.go src/agent/internal/agent/benchmark_test.go
git commit -m "feat(agent): POST /benchmark/suites/import-with-assets"
```

---

## Task 16: 录入器 — /benchmark/record 页面

**Files:**

- Modify: `src/agent/internal/agent/benchmark_html.go` (加 `benchmarkRecordHTML` 常量)
- Modify: `src/agent/internal/agent/benchmark.go` (加 handler)
- Modify: `src/agent/internal/agent/server.go` (注册路由)

UI 复用 `coordinate_debug_html.go` 中的图片加载/画布逻辑，加上拉矩形 + 提交。

- [ ] **Step 1: 阅读 coordinate_debug_html.go**

```bash
sed -n '1,50p' src/agent/internal/agent/coordinate_debug_html.go
```

记下：图片加载（设备抓 `/api/screenshot.jpg`、上传、粘贴）、坐标转换公式（pixel ↔ normalized 0-1000）。

- [ ] **Step 2: 写 benchmarkRecordHTML 常量**

加到 `benchmark_html.go`：

```go
// benchmarkRecordHTML is the screenshot-task recorder served at /benchmark/record.
// Reuses the image loading + normalized-coordinate logic from
// coordinate_debug_html.go; adds rectangle drag, target name, and a
// "Generate with LLM" button that POSTs to /benchmark/suites/generate-perception
// then to /benchmark/suites/import-with-assets.
const benchmarkRecordHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>录入截图任务</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f1ede2; color: #1e241d; padding: 20px; }
.container { max-width: 1100px; margin: 0 auto; background: #fffbf5; border-radius: 16px; padding: 24px; box-shadow: 0 8px 22px rgba(43,47,40,0.08); }
h1 { color: #155646; margin-bottom: 8px; font-size: 22px; }
.back-link { color: #1f7a63; font-size: 13px; }
input[type="text"], textarea { width: 100%; padding: 8px; border: 1px solid #d8cfbf; border-radius: 6px; font-size: 14px; background: #fffdf8; }
textarea { min-height: 60px; font-family: inherit; }
label { display: block; font-size: 12px; color: #43493d; margin: 12px 0 4px; font-weight: 600; }
.btn { padding: 10px 16px; background: #1f7a63; color: #fff; border: none; border-radius: 6px; cursor: pointer; font-size: 14px; }
.btn:hover { background: #155646; }
.btn:disabled { background: #b9c2b6; cursor: not-allowed; }
.btn-secondary { background: #be7d34; }
.btn-secondary:hover { background: #9d6328; }
.canvas-wrap { margin: 12px 0; border: 2px solid #1f7a63; border-radius: 8px; display: inline-block; max-width: 100%; }
canvas { display: block; cursor: crosshair; max-width: 100%; height: auto; }
.row { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; }
.muted { color: #697063; font-size: 12px; }
.field-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.json-preview { background: #1e241d; color: #cce4d8; padding: 12px; border-radius: 6px; font-family: monospace; font-size: 12px; white-space: pre-wrap; max-height: 320px; overflow: auto; }
.error { color: #be4334; font-size: 13px; margin-top: 6px; }
.ok { color: #1f7a63; font-size: 13px; margin-top: 6px; }
</style>
</head>
<body>
<div class="container">
<a class="back-link" href="/benchmark">← 返回 Benchmark</a>
<h1>📷 录入截图任务</h1>

<div class="field-grid">
<div>
<label for="suiteName">Suite name</label>
<input type="text" id="suiteName" placeholder="iphone_perception">
</div>
<div>
<label for="taskId">Task ID</label>
<input type="text" id="taskId" placeholder="find_settings_iphone">
</div>
</div>

<label>截图</label>
<div class="row">
<button class="btn" id="grabBtn">📷 抓取设备画面</button>
<input type="file" id="fileInput" accept="image/*" style="display:none">
<button class="btn btn-secondary" id="uploadBtn">📁 上传图片</button>
<span class="muted">或 Ctrl/Cmd+V 粘贴</span>
</div>
<div class="canvas-wrap" id="canvasWrap" style="display:none">
<canvas id="canvas"></canvas>
</div>
<div class="muted" id="rectInfo">在画面上拖拽鼠标画矩形选中目标</div>

<div class="field-grid">
<div>
<label for="targetName">Target name</label>
<input type="text" id="targetName" placeholder="Settings icon">
</div>
<div>
<label for="userIntent">User intent</label>
<input type="text" id="userIntent" placeholder="打开设置 app">
</div>
</div>

<div class="row" style="margin-top:14px">
<button class="btn" id="genBtn" disabled>✨ Generate with LLM</button>
<button class="btn btn-secondary" id="importBtn" disabled>Import to suite</button>
<span id="msg"></span>
</div>

<label>预览生成的 task JSON（可手动修改）</label>
<div class="json-preview" id="jsonPreview" contenteditable="false">（点 Generate 后显示）</div>
</div>

<script>
let img=null, rect=null, canvas, ctx, lastTaskJSON='';
const wrap=document.getElementById('canvasWrap');

function setupCanvas(){
  canvas=document.getElementById('canvas');
  ctx=canvas.getContext('2d');
  let dragging=false, startX=0, startY=0;
  canvas.addEventListener('mousedown',e=>{
    const r=canvas.getBoundingClientRect();
    startX=(e.clientX-r.left)*canvas.width/r.width;
    startY=(e.clientY-r.top)*canvas.height/r.height;
    dragging=true;
  });
  canvas.addEventListener('mousemove',e=>{
    if(!dragging) return;
    const r=canvas.getBoundingClientRect();
    const x=(e.clientX-r.left)*canvas.width/r.width;
    const y=(e.clientY-r.top)*canvas.height/r.height;
    drawWithRect(startX,startY,x,y);
  });
  canvas.addEventListener('mouseup',e=>{
    if(!dragging) return;
    dragging=false;
    const r=canvas.getBoundingClientRect();
    const x=(e.clientX-r.left)*canvas.width/r.width;
    const y=(e.clientY-r.top)*canvas.height/r.height;
    rect={x1:Math.min(startX,x),y1:Math.min(startY,y),x2:Math.max(startX,x),y2:Math.max(startY,y)};
    drawWithRect(rect.x1,rect.y1,rect.x2,rect.y2);
    showRectInfo();
    document.getElementById('genBtn').disabled=false;
  });
}

function showRectInfo(){
  if(!rect||!img){return}
  const w=img.width-1, h=img.height-1;
  const nx1=Math.round(rect.x1/w*1000), ny1=Math.round(rect.y1/h*1000);
  const nx2=Math.round(rect.x2/w*1000), ny2=Math.round(rect.y2/h*1000);
  document.getElementById('rectInfo').textContent='Normalized rectangle: ('+nx1+','+ny1+')-('+nx2+','+ny2+')';
}

function drawImage(){
  if(!img) return;
  canvas.width=img.width; canvas.height=img.height;
  ctx.drawImage(img,0,0);
}
function drawWithRect(x1,y1,x2,y2){
  drawImage();
  ctx.strokeStyle='#be4334'; ctx.lineWidth=4;
  ctx.strokeRect(Math.min(x1,x2),Math.min(y1,y2),Math.abs(x2-x1),Math.abs(y2-y1));
}

function loadImage(url, onDone){
  const i=new Image();
  i.onload=()=>{img=i; rect=null; setupCanvas(); drawImage(); wrap.style.display='inline-block'; if(onDone) onDone()};
  i.src=url;
}

document.getElementById('grabBtn').addEventListener('click',async()=>{
  const r=await fetch('/api/screenshot.jpg?t='+Date.now(),{cache:'no-store'});
  if(!r.ok){alert('抓取失败 '+r.status); return}
  const blob=await r.blob();
  loadImage(URL.createObjectURL(blob));
});
document.getElementById('uploadBtn').addEventListener('click',()=>document.getElementById('fileInput').click());
document.getElementById('fileInput').addEventListener('change',e=>{
  const f=e.target.files[0]; if(!f) return;
  const fr=new FileReader();
  fr.onload=ev=>loadImage(ev.target.result);
  fr.readAsDataURL(f);
});
document.addEventListener('keydown',e=>{
  if((e.ctrlKey||e.metaKey)&&e.key==='v'&&navigator.clipboard&&navigator.clipboard.read){
    navigator.clipboard.read().then(items=>{
      for(const it of items) for(const t of it.types) if(t.startsWith('image/')){
        it.getType(t).then(b=>{
          const fr=new FileReader();
          fr.onload=ev=>loadImage(ev.target.result);
          fr.readAsDataURL(b);
        });
        return;
      }
    }).catch(()=>{});
  }
});

async function blobOrCanvasToB64(){
  return new Promise((resolve,reject)=>{
    canvas.toBlob(b=>{
      const fr=new FileReader();
      fr.onloadend=()=>resolve(fr.result.split(',')[1]);
      fr.onerror=reject;
      fr.readAsDataURL(b);
    },'image/jpeg',0.85);
  });
}

document.getElementById('genBtn').addEventListener('click',async()=>{
  const msg=document.getElementById('msg');
  msg.textContent='生成中...'; msg.className='';
  try {
    const w=img.width-1, h=img.height-1;
    const box={
      x1:Math.round(rect.x1/w*1000), y1:Math.round(rect.y1/h*1000),
      x2:Math.round(rect.x2/w*1000), y2:Math.round(rect.y2/h*1000)
    };
    const b64=await blobOrCanvasToB64();
    const resp=await fetch('/benchmark/suites/generate-perception',{
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({
        name:document.getElementById('suiteName').value.trim(),
        task_id:document.getElementById('taskId').value.trim(),
        user_intent:document.getElementById('userIntent').value.trim(),
        screenshot_b64:b64,
        target_box_normalized:box,
        target_name:document.getElementById('targetName').value.trim()
      })
    });
    const d=await resp.json();
    if(!d.ok){msg.textContent=d.error||'生成失败'; msg.className='error'; return}
    lastTaskJSON=d.task_json;
    document.getElementById('jsonPreview').textContent=lastTaskJSON;
    document.getElementById('jsonPreview').contentEditable='true';
    document.getElementById('importBtn').disabled=false;
    msg.textContent='已生成，预览后点 Import';msg.className='ok';
  } catch(e) { msg.textContent=String(e); msg.className='error'; }
});

document.getElementById('importBtn').addEventListener('click',async()=>{
  const msg=document.getElementById('msg');
  const name=document.getElementById('suiteName').value.trim();
  const taskId=document.getElementById('taskId').value.trim();
  const taskRaw=document.getElementById('jsonPreview').textContent.trim();
  let task;
  try { task=JSON.parse(taskRaw).task; } catch(e) { msg.textContent='task JSON 解析失败: '+e; msg.className='error'; return }
  const suiteJSON=JSON.stringify({name:name, tasks:[task]});
  const b64=await blobOrCanvasToB64();
  msg.textContent='导入中...';msg.className='';
  const resp=await fetch('/benchmark/suites/import-with-assets',{
    method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({
      name:name, suite_json:suiteJSON,
      assets:[{task_id:taskId, screenshot_b64:b64}]
    })
  });
  const d=await resp.json();
  if(!d.ok){msg.textContent=d.error||'导入失败'; msg.className='error'; return}
  msg.textContent='导入成功 → '+d.path;msg.className='ok';
});

console.log('📷 录入截图任务已加载');
</script>
</body>
</html>
`
```

- [ ] **Step 3: 加 handler + 注册**

`benchmark.go`：

```go
func (s *Server) handleBenchmarkRecord(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(benchmarkRecordHTML))
}
```

注册：

```go
	mux.HandleFunc("/benchmark/record", s.handleBenchmarkRecord)
```

- [ ] **Step 4: 写最小冒烟测试**

```go
func TestHandleBenchmarkRecord_HasFormAndScripts(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/record", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkRecord(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, marker := range []string{
		`/api/screenshot.jpg`,
		`/benchmark/suites/generate-perception`,
		`/benchmark/suites/import-with-assets`,
		`id="canvas"`,
	} {
		if !strings.Contains(rec.Body.String(), marker) {
			t.Errorf("body missing %q", marker)
		}
	}
}
```

- [ ] **Step 5: 跑测试 + 主机端到端 + 提交**

```bash
cd src/agent && go test ./internal/agent -run TestHandleBenchmarkRecord -v
cd /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites && go run ./src/agent/cmd/daemon -config $PWD/agent.conf -addr 127.0.0.1:18080 &
sleep 2
curl -s http://127.0.0.1:18080/benchmark/record | grep canvas | head -1
kill %1 2>/dev/null || true
```

```bash
git add src/agent/internal/agent/benchmark_html.go src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go src/agent/internal/agent/server.go
git commit -m "feat(agent): GET /benchmark/record perception task recorder"
```

---

## Task 17: 删除 config_web 中所有迁走的路由 + UI

**Files:**

- Modify: `src/config_web.cpp`
- Modify: `tests/config_web_source_test.cpp`
- Test: 跑现有 cpp 测试套件

- [ ] **Step 1: 列出所有要删的位置**

```bash
grep -n 'request.path == "/benchmark\|starts_with(request.path, "/benchmark\|request.path == "/user_files' /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites/src/config_web.cpp
grep -n 'benchmark_html_page\|generate_suite_with_llm\|benchmark_runner_alive\|ensure_user_files_report\|kUserFilesReportPath\|kUserFilesToolsDir\|kUserFilesGenerateCommandTimeoutMs' /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites/src/config_web.cpp
```

将打印出的每一处文件位置记录下来。

- [ ] **Step 2: 删除 12 个 if 块**

依次删除：

- `request.path == "/benchmark"` 块
- `request.path == "/benchmark/suites"` 块
- `request.path == "/benchmark/runs"` 块
- `starts_with(request.path, "/benchmark/report/")` 块
- `request.path == "/benchmark/status"` 块
- `request.path == "/benchmark/log"` 块
- `request.path == "/benchmark/run"` 块
- `request.path == "/benchmark/suites/import"` 块
- `request.path == "/benchmark/suites/delete"` 块
- `request.path == "/benchmark/suites/generate"` 块
- `request.path == "/user_files"` 块
- `request.path == "/user_files/regenerate"` 块

- [ ] **Step 3: 删除函数定义**

- `benchmark_html_page()`
- `generate_suite_with_llm()`
- `benchmark_runner_alive()`
- `ensure_user_files_report()`
- 相关常量：`kUserFilesReportPath`, `kUserFilesToolsDir`, `kUserFilesGenerateCommandTimeoutMs`

- [ ] **Step 4: 主页 HTML 入口清理**

```bash
grep -n '/benchmark\b\|/user_files' /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites/src/config_web_html.h
```

删除所有指向 benchmark / user_files 的 `<a>` 链接和按钮。

- [ ] **Step 5: 同步删除测试断言**

```bash
grep -n 'benchmark\|user_files\|ensure_user_files_report\|generate_suite_with_llm\|benchmark_html_page' /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites/tests/config_web_source_test.cpp
```

删除每一处与上述路由 / 函数相关的 source-grep assertion。

- [ ] **Step 6: 编译 + 跑现有测试**

```bash
cd /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites
make test-clean && make test
```

期望：所有现有 ctest 通过。配置面、wifi、ota 等其他功能不受影响。

- [ ] **Step 7: 提交**

```bash
git add src/config_web.cpp src/config_web_html.h tests/config_web_source_test.cpp
git commit -m "refactor(config_web): remove benchmark + user_files routes (migrated to agent)"
```

---

## Task 18: 删 files_proxy.py + full_smoke.json

- [ ] **Step 1: 删文件**

```bash
cd /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites
git rm scripts/files_proxy.py benchmark/suites/full_smoke.json
```

- [ ] **Step 2: 验证无残留引用**

```bash
grep -rn "files_proxy\|full_smoke" . --include='*.go' --include='*.py' --include='*.cpp' --include='*.h' --include='*.sh' --include='*.md' 2>/dev/null | grep -v ".git/\|node_modules\|.venv\|__pycache__\|/runs/\|/artifacts/" | head
```

期望：无输出（或仅留 docs/spec 这种历史文档引用）。

- [ ] **Step 3: 提交**

```bash
git commit -m "chore: drop scripts/files_proxy.py and benchmark/suites/full_smoke.json"
```

---

## Task 19: 端到端验证

不写代码；按 spec 测试章节走一遍人工冒烟。

- [ ] **Step 1: 主机起 agent**

```bash
cd /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites/src/agent
go build ./...
cd /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites
./build/bin/agent -config $PWD/agent.conf -addr 127.0.0.1:18080
```

新开终端，依次访问以下 URL 并核对每个 endpoint 返回数据合理：

- `http://127.0.0.1:18080/benchmark` → 主页能打开
- `http://127.0.0.1:18080/benchmark/suites` → JSON 列表，含 `perception_v1`
- `http://127.0.0.1:18080/benchmark/runs` → JSON 列表
- `http://127.0.0.1:18080/benchmark/status` → `{"status":"idle"}`
- `http://127.0.0.1:18080/benchmark/log` → 文本（可能为空）
- `http://127.0.0.1:18080/benchmark/record` → 录入页

- [ ] **Step 2: 录入器手动走一遍**

打开 `http://127.0.0.1:18080/benchmark/record`：

1. 上传一张设备截图
2. 拖拽一个矩形
3. 填 suite name `smoketest`、task id `smoke_target`、target name `Some icon`、user intent `do thing`
4. 点 Generate with LLM（如果 agent.conf 模型不支持图像，会失败 → 验证错误信息有用）
5. 编辑 JSON（如需要）
6. 点 Import
7. 验证：`ls benchmark/suites/custom/smoketest/`

- [ ] **Step 3: 设备烧录 + 验证（如有设备）**

烧 image 到设备：

```bash
cd /Users/jacob/.prowl/repos/aiden-hardware-demo/feat/image_suites && bash build_image.sh
# 烧录步骤参考仓库 README.md
```

设备启动后：

- `http://<device-ip>:8080/benchmark` 能打开（agent 端口）
- `http://<device-ip>/benchmark` 应当 **404 或返回 config_web 主页**（已删除）
- 在设备上录一个截图任务，端到端跑通

- [ ] **Step 4: 提交记录**

不需要代码 commit。在 PR 描述里记录验证结果（OK 列表 + 任何遇到的问题）。

---

## Self-Review

实施完成后，prepare PR 前再过一次 spec：

```bash
diff <(grep '^### ' docs/superpowers/specs/2026-06-10-benchmark-migration-design.md) <(grep '^## Task' docs/superpowers/plans/2026-06-10-benchmark-migration.md)
```

确认每个 spec 章节都有对应 task 实现。
