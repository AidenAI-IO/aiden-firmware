# Benchmark + user_files 全面迁入 agent (8080) 设计

## 背景

设备上 HTTP 入口当前分布在三个端口：

| 端口 | 服务               | 角色                                                 |
| ---- | ------------------ | ---------------------------------------------------- |
| 80   | `config_web` (C++) | 设备配置 + benchmark 管理面 + `/user_files`          |
| 8080 | `agent` (Go)       | agent 对话/工具/截图 API + `/coordinate-debug`       |
| 8090 | `files_proxy.py`   | 早期开发辅助，已被 config_web 完全覆盖，无 init 脚本 |

**问题**：

1. benchmark 是"和 agent 互动产生的运行时观测面"，挂在 80（config_web，做设备静态配置的 C++ 服务）与其语义错配。LLM 调用、跑 Python runner、截图录入未来都要扩展，C++ 实现成本远高于 Go。
2. `/user_files`（agent memory/skills/skillopt 报告）也是运行时观测，同上。
3. config_web.cpp 现 4000+ 行，benchmark 占 ~500 行 C++ + ~160 行 HTML/JS，再加截图录入会继续膨胀。
4. `files_proxy.py` 是技术债，没有生产路径但还在仓库里。

**触发本次工作的具体需求**：用户希望在网页上录入"perception 类截图任务"——加载截图（设备实时抓 / 上传）+ 拉矩形标目标 + 一句话描述意图，自动生成完整 task 并写入 suite 文件。

## 目标

把 benchmark 全部 HTTP 路由 + `/user_files` 从 config_web 迁到 agent，并在 agent 上新增"截图任务录入器"作为新功能。

完成后：

- 80 = 纯设备配置面（wifi、系统、proxy 等），无 benchmark 无 user_files
- 8080 = 全部 agent 运行时（对话 + benchmark 管理 + 截图录入 + user_files）
- 8090 删除

非目标：

- 不修改 benchmark runner（Python `runner/` 包）的执行/评估逻辑
- 不实现已导入 suite 的可视化编辑（首版只支持新建/导入/删除）
- 不替换 `view_agent_files.sh` 的报告生成脚本（agent 调用它，不重写）

## 迁移清单（C++ → Go）

| config_web.cpp 当前路由           | 迁后 agent 路由                   | 备注                           |
| --------------------------------- | --------------------------------- | ------------------------------ |
| `GET /benchmark`                  | `GET /benchmark`                  | HTML 页面，搬到 Go embed       |
| `GET /benchmark/suites`           | `GET /benchmark/suites`           | 返回 suites JSON 列表          |
| `GET /benchmark/runs`             | `GET /benchmark/runs`             | 列 runs 目录                   |
| `GET /benchmark/report/<id>`      | `GET /benchmark/report/<id>`      | serve report.html              |
| `GET /benchmark/status`           | `GET /benchmark/status`           | 读 state.json + self-heal      |
| `GET /benchmark/log`              | `GET /benchmark/log`              | tail benchmark_run.log         |
| `POST /benchmark/run`             | `POST /benchmark/run`             | double-fork 起 Python runner   |
| `POST /benchmark/suites/import`   | `POST /benchmark/suites/import`   | 验证 + 写 custom/<name>.json   |
| `POST /benchmark/suites/delete`   | `POST /benchmark/suites/delete`   | 删 custom suite                |
| `POST /benchmark/suites/generate` | `POST /benchmark/suites/generate` | 纯文本 LLM 生成                |
| `GET /user_files`                 | `GET /user_files`                 | 调 view_agent_files.sh + serve |
| `POST /user_files/regenerate`     | `POST /user_files/regenerate`     | 异步重生成                     |

新增（agent 独有）：

| 路由                                         | 用途                                    |
| -------------------------------------------- | --------------------------------------- |
| `POST /benchmark/suites/generate-perception` | LLM 生成含截图的 perception task        |
| `POST /benchmark/suites/import-with-assets`  | 导入含截图的 suite（base64 + 写子目录） |

URL 路径与现有 config_web 完全一致，便于历史用户脚本/书签从 80 改 host 到 8080 即可继续工作。

config_web 端：删除上述路由的所有实现 + UI。**不留 301 重定向，不留链接**——硬删。

## 架构

### 进程拓扑

- agent (Go) 进程在 8080 跑得起来时，handle 全部 benchmark + user_files
- Python runner 仍是独立子进程，用 `sh -c "(... &) &"` 形式 double-fork（与现状一致），脱离 agent 进程组：agent 重启不影响在跑的 benchmark
- agent 重启后通过 `/tmp/benchmark_runner.pid` 探活 + state.json mtime grace period 做 self-heal（与现 `benchmark_runner_alive()` 同语义）

### 工作目录约定

agent 启动时按顺序探测 benchmark 根目录：

1. CLI flag `-benchmark-dir`
2. env `AIDEN_BENCHMARK_DIR`
3. `/userdata/agent/benchmark`（设备）
4. `<cwd>/benchmark`（开发机）

第一个存在的胜出。`suites/` 在该根之下，`runs/` 同级，`state.json` 在根目录。

`view_agent_files.sh` 的位置（`/userdata/agent_tools/view_agent_files.sh`）和报告输出（`/userdata/agent/files_report.html`）保持现状，agent 用 `popen` 调脚本——不在 Go 里重写报告生成。

## 详细设计

### 1. benchmark 主页与子页（HTML）

config_web 现有 ~160 行 HTML/JS（路由 `/benchmark` 返回的 `benchmark_html_page()`）整体搬到 agent，存放在新文件 `src/agent/internal/agent/benchmark_html.go`，仿 `coordinate_debug_html.go` 的 raw-string 形式。

迁移时只改两点：

- 上方导航 / 链接里如果有写死的 host:port，统一改成相对路径（已经是相对的，目测 OK，迁移时 grep 确认）
- 新增"📷 录入截图任务"按钮链接到新页 `/benchmark/record`

新增页 `/benchmark/record`：截图录入器，详见后文"截图录入器"章节。

### 2. benchmark API 实现要点

新增文件 `src/agent/internal/agent/benchmark.go`，注册到 `server.go` 现有 `mux.HandleFunc` 区段（第 232–262 行附近）。

每个 handler 一对一翻译 C++ 实现，行为保持等价：

- **`/benchmark/suites`**：用 `filepath.Walk` 替代 `popen("find ...")`；过滤 `.json` 且非 `._*`；返回 JSON `{suites: [{path, name, custom: bool, ...}]}`
- **`/benchmark/runs`**：`os.ReadDir(<root>/runs)`，按名 reverse 排，取前 20，返回 `{runs: [{run_id}]}`
- **`/benchmark/report/<id>`**：sanitize id（仅 alnum + `-_`），打开 `<root>/runs/<id>/report.html` serve；不存在 404
- **`/benchmark/status`**：读 `state.json`，包含 self-heal 逻辑：若状态为 running 但 PID 进程不存在且 state.json mtime > 10s，重写为 `{"status":"idle","recovered":true}`
- **`/benchmark/log`**：读 `/tmp/benchmark_run.log` 末尾 64 KB
- **`/benchmark/run`**：写 state.json `{status:running,suite:<path>}`，然后 `exec.Command("sh", "-c", DOUBLE_FORK_SCRIPT)`，脚本与 config_web 当前命令字面值一致，仅替换：
  - `--judge-model` 从 agent.conf 新增的 `[benchmark] judge_model` 字段读，默认 `bytedance-seed/seed-2.0-lite`
  - `OPENROUTER_API_KEY` 直接从 agent 已加载的 `cfg.Model.APIKey` 内存中传入子进程 env，不再 `grep agent.toml`
- **`/benchmark/suites/import`**：复用 Go JSON 解析 + 调 `python3 -c "from runner.suite import load_suite; load_suite(...)"` 验证；写到 `<root>/suites/custom/<safe_name>.json` 用 atomic rename
- **`/benchmark/suites/delete`**：sanitize name，仅允许删 `<root>/suites/custom/` 下的；返回 `os.Remove` 结果
- **`/benchmark/suites/generate`**：见下节

并发安全：单个 suite 的写入用 `sync.Map[string]*sync.Mutex` 按 name 取锁。

### 3. `generate_suite_with_llm` 迁移

C++ 现实现 ~170 行：加载 agent config → 取 OpenRouter API key → 拉 `/api/tools` 列表（curl 8080） → 拼一段巨大的 system prompt → curl `chat/completions` → 解析 JSON。

Go 实现：

- 复用 `ModelManager`：用 `m.LLM().GenerateContent(...)` 替代 curl + tmpfile + auth header dance
- "拉 tools 列表"在同进程里就是函数调用（agent 自己就提供 `/api/tools`），不再绕一圈 HTTP
- **system prompt 1:1 复制 C++ 字符串**，迁移时不改一字（防止现有生成行为漂移）。复制到一个 `const benchmarkGenerateSystemPrompt = ...` 常量
- 错误码、响应格式与 config_web 一致：成功 `{ok, suite_json}`，失败 `{ok:false, error: "..."}`

### 4. 截图录入器（新增功能）

#### 端点

`POST /benchmark/suites/generate-perception` 请求体：

```json
{
  "name": "iphone_perception",
  "task_id": "find_settings_iphone",
  "user_intent": "打开设置 app",
  "screenshot_b64": "<base64>",
  "target_box_normalized": { "x1": 750, "y1": 380, "x2": 980, "y2": 550 },
  "target_name": "Settings icon"
}
```

后端：

1. 复用 ModelManager（同主模型；视觉能力依赖该模型，下文风险）
2. 用 langchain `llms.MessageContent` 拼一条 user message：text + image part（base64 dataURL）
3. system prompt（perception 专用，新写）严格要求输出单 task JSON，schema：

   ```json
   {
     "task": {
       "id": "<task_id>",
       "category": "perception",
       "input_screenshot": "screenshots/<task_id>.jpg",
       "prompt": "<中文/英文指令>",
       "description_for_judge": "<英文判官规则>",
       "rubric": [
         { "id": "called_click_tool", "check": "..." },
         {
           "id": "click_targets_<slug>",
           "check": "...normalized x in [a, b] and y in [c, d]"
         }
       ],
       "hard_assertions": {
         "min_tool_calls": 1,
         "max_tool_calls": 5,
         "must_complete_within_sec": 120,
         "response_required": true
       }
     }
   }
   ```

4. **后端兜底坐标 + slug**：LLM 返回后用 `target_box_normalized / 1000` 保留 2 位小数强制覆盖 rubric 中坐标范围；`<slug>` 由 `target_name` 转 lower + `[^a-z0-9_]` 替换为下划线（例 `"Settings icon"` → `"settings_icon"`），后端生成。
5. 返回 `{ok, task_json}` （单 task，不含 suite 包装）

`POST /benchmark/suites/import-with-assets` 请求体：

```json
{
  "name": "iphone_perception",
  "suite_json": "{\"name\":\"iphone_perception\",\"tasks\":[...]}",
  "assets": [
    { "task_id": "find_settings_iphone", "screenshot_b64": "<base64>" }
  ]
}
```

后端流程：

1. 校验 name + suite_json 大小 + 解析合法 + 每个 perception task 的 `input_screenshot` 形如 `screenshots/<task_id>.jpg` + assets 中存在对应 task_id
2. 在 `/tmp/<name>_<rand>/` 写 `<name>.json` + `screenshots/<task_id>.jpg` 全套
3. 跑 `python3 -c "from runner.suite import load_suite; load_suite('/tmp/<name>_<rand>/<name>.json')"` 验证
4. atomic 移动到 `<root>/suites/custom/<name>/`（如果目标已存在，先 rename 旧目录到 `.bak.<ts>` 再 mv）
5. 失败时清理 tmp 目录后返回错误 + HTTP 4xx/5xx

#### UI

`/benchmark/record` 页面（HTML embed）：

```
[选择 / 新建 suite ▼]    Task ID: [____]

[📷 抓取设备画面]   [📁 上传]   [📋 粘贴]
┌─────────────────────────┐
│  画布：图片 + 拖拽矩形    │
└─────────────────────────┘
矩形归一化坐标: (750, 380) - (980, 550)

Target name: [Settings icon]   User intent: [打开设置 app]

[✨ Generate with LLM]   ← 调 /benchmark/suites/generate-perception

生成后展开（可编辑）：
  Prompt / Description for judge / Rubric items / Hard assertions

[Preview Suite JSON]   [Import]   ← 调 /benchmark/suites/import-with-assets
```

复用 `/coordinate-debug` 已有的图片加载（设备抓取/上传/粘贴）和归一化坐标计算 JS——同进程同源，直接 inline import 那段 JS（迁移时考虑抽到 `static_assets.go` 共享）。

### 5. user_files 迁移

agent 新增：

- `GET /user_files`：检查 `/userdata/agent/files_report.html` 存在则 serve；否则 `popen("cd /userdata/agent_tools && ./view_agent_files.sh")` + 等待完成（带超时）后 serve。逻辑直译 C++ `ensure_user_files_report`
- `POST /user_files/regenerate`：异步起一个 goroutine 跑 view_agent_files.sh，立即返回 `{ok:true, regenerating:true}`

设备 cwd 不一定是 `/userdata/agent_tools`，agent 用绝对路径调脚本。

### 6. config_web 端清理

删除：

- `handle_request` 中 12 个 benchmark/user_files 相关 if 块（行号实施时 grep `request.path == "/benchmark"` / `"/user_files"` 确认）
- `benchmark_html_page()` 函数（约 160 行 HTML 字符串）
- `generate_suite_with_llm()` 函数（约 170 行）
- `benchmark_runner_alive()` 函数
- `ensure_user_files_report()` 函数 + 相关常量 `kUserFilesReportPath` `kUserFilesToolsDir` 等
- 主页 HTML 中跳转到 `/benchmark` `/user_files` 的入口（如果有）

预估 config_web.cpp 减少 ~600 行。

`tests/config_web_source_test.cpp` 中所有引用 `/benchmark` `/user_files` `ensure_user_files_report` `generate_suite_with_llm` 的 assertion 全部删除（约 30+ 行）。

### 7. 配置文件

`agent.conf` 新增段：

```toml
[benchmark]
judge_model = "bytedance-seed/seed-2.0-lite"
# benchmark_dir = "/userdata/agent/benchmark"  # 可选，默认探测
```

`agent.conf` schema（`config.go` + `config_meta.go`）相应新增字段定义。

## 顺手清理

随同本次 PR：

1. 删除 `scripts/files_proxy.py`
2. 删除 `benchmark/suites/full_smoke.json`（无任何代码引用，仅在历史 `benchmark/artifacts/last_run.jsonl` 出现）

## 测试

### Go 单测（`src/agent/internal/agent/benchmark_test.go`）

- benchmark 根目录探测：CLI/env/默认路径优先级
- `/benchmark/suites` 列表：建 tmpdir + 几个 json，断言返回结构
- `/benchmark/runs` 顺序、上限 20
- `/benchmark/status` self-heal：状态为 running 但 PID 文件指向不存在的 pid 时，10s grace 之后返回 idle+recovered
- `/benchmark/suites/import`：合法/非法 JSON、超大 body、name 校验、写盘后能被 Python `load_suite` 加载
- `/benchmark/suites/delete`：只能删 custom 下的；尝试删 `perception_v1.json` 这种内置应失败
- `/benchmark/suites/generate`：用 `provider = fake` 注入预设响应，断言 prompt 拼接 + tools 列表注入正确
- `/benchmark/suites/generate-perception`：fake LLM 返回模板 task，验证后端坐标 + slug 兜底覆盖正确
- `/benchmark/suites/import-with-assets`：tmpdir 落盘后 `runner.suite.load_suite` 加载成功；目标已存在时 .bak.<ts> 备份；并发同名 import 互斥
- `/user_files`：mock 调用 view_agent_files.sh（用 PATH 注入一个假脚本），断言 serve 行为

### 端到端手动验证

主机上跑：

1. `cd src/agent && go run ./cmd/daemon -config <repo>/agent.conf -addr :8080`
2. 浏览器开 `http://localhost:8080/benchmark`，UI 与原 80 端口的 benchmark 页面行为一致
3. 走截图录入流程：抓画面 → 拉矩形 → 生成 → import → 在 suites 列表能看到新 suite → run（如果有设备） → 看 report
4. `http://localhost:8080/user_files` 能生成报告
5. config_web 主页上 benchmark/user_files 入口已消失

设备验证：

6. 烧到设备，确认 80 上 benchmark 链接已删；8080 上 benchmark 完整可用
7. agent 重启时，正在跑的 benchmark runner 不被杀（double-fork 验证）
8. agent 没起来时强行删 `state.json` running 状态，重新起 agent → status 端点返回 idle+recovered

## 风险

- **Python runner 调用兼容性**：当前 `python3 -c 'from runner.main import cli; ...'` 依赖 cwd 在 `/userdata/agent/benchmark`，且 Python `sys.path` 能找到 `runner` 包。Go 起的 sh 子进程要复刻这个环境（`cd` + `sys.path.insert`）。具体命令字面值从 config_web.cpp 第 3895 行一字不动复制到 Go const，然后只把 `--judge-model` 替换为变量。
- **system prompt 漂移**：generate 的 system prompt 是经过调试的，迁过去要严格 1:1。建议把 C++ 字符串和 Go 字符串放并列做 diff 验证（一个临时脚本），通过后再合入。
- **进程生命周期事故**：agent 重启 → state.json 还是 running → 用户进 page 看到一直 running。self-heal 解决，但只有在用户**访问** status 路由时才会修。如果用户离开页面，state.json 永远不会被修复（直到下次访问）。和现状一致，不在本次范围扩大。
- **scope 比常规大**：约 700–1000 行 Go 新代码 + 删 ~600 行 C++ + 重写测试 + 修改 agent.conf schema。预估 1-2 周。建议拆 commit 而非 PR：先迁移基础路由（list/runs/report/status/log），再迁 generate/import/delete，最后加录入器，每段独立 commit 便于回滚审查。
- **C++ raw-string-literal 删除**：`benchmark_html_page()` 中的 HTML 字符串很长，确认完整删除避免残留 dead code。
- **`/api/tools` 同进程调用**：现 C++ 是 `popen("curl 127.0.0.1:8080/api/tools")` 拿 tools 列表给 generate 的 prompt 用。Go 直接拿 `s.tools` 内部状态拼字符串即可，但要保证拼出来的格式与 C++ 完全一致——不然 system prompt 行为漂移。
- **agent 启动顺序依赖**：`view_agent_files.sh` 依赖 `/userdata/agent_tools/` 下的 Python 脚本+模板，这些由 `_build_image.sh` 打包到 overlay。agent 启动早于这些目录 mount 完成的概率虽低，但 `/user_files` 路由要优雅返回错误（不 panic）。

## 实施顺序建议

1. 加 agent.conf `[benchmark]` 段 + `ModelManager` 暴露给 benchmark handler
2. 迁只读路由：`/benchmark`（基础页）、`/benchmark/suites`、`/benchmark/runs`、`/benchmark/report/...`、`/benchmark/status`、`/benchmark/log`
3. 迁写路由：`/benchmark/run`（double-fork 脚本）、`/benchmark/suites/import`、`/benchmark/suites/delete`
4. 迁 LLM：`/benchmark/suites/generate`（system prompt 1:1）
5. 迁 `/user_files` + `/user_files/regenerate`
6. 加 `/benchmark/record` 录入页 + `generate-perception` + `import-with-assets`
7. config_web 删除 + 测试同步
8. 顺手清理（`files_proxy.py`、`full_smoke.json`）
