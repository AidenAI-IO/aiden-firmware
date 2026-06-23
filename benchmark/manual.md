# Benchmark 使用手册

本文说明当前 `benchmark/` 目录下 Aiden benchmark 的设计和使用方式，覆盖推荐入口：

- WebUI：适合日常运行、多 suite、MobileGym 并发和人工查看报告。
- CLI：适合脚本化运行、单 suite 调试、rejudge、compare 和 unit tool 测试。

以下命令默认从仓库根目录进入 `benchmark/` 后执行：

```bash
cd benchmark
uv sync
```

## 1. 设计说明

### 1.1 核心目标

Benchmark 用来评估 Aiden agent 在手机 UI、记忆、规划、感知等任务上的表现。它把“执行任务”和“判分”分开：

1. Python runner 读取 `benchmark/suites/*.json`。
2. Runner 通过 HTTP 调用 Aiden Go agent daemon。
3. Agent 根据 prompt 调用工具操作设备或模拟器。
4. Runner 收集 history、trace、pre/post screenshot、日志等 artifact。
5. Hard assertions 先做确定性检查。
6. 可选 judge model 再根据 rubric、trace 和截图做离线判定。

这种设计的好处是：任务执行依赖真实 agent 和环境，判分可以离线重跑；失败时可以查看完整 trace 和截图，不必重新操作设备。

### 1.2 主要组件

| 组件 | 位置 | 作用 |
| --- | --- | --- |
| Suite | `benchmark/suites/*.json` | 定义任务、prompt、rubric、hard assertions、setup |
| Runner | `benchmark/runner/main.py` | CLI 入口，负责执行 suite、生成报告 |
| WebUI | `benchmark/runner/webui.py` | Web 控制台，负责管理 suites、jobs、environments、logs、reports |
| Agent client | `benchmark/runner/agent_client.py` | 调用 Go agent 的 `/api/chat`、`/api/tools/*`、`/api/history` |
| Judge | `benchmark/runner/judge.py` | 调用 OpenRouter 兼容接口，使用 pre/post screenshot 和 trace 判分 |
| MobileGym bridge | `benchmark/mobilegym/bridge/` | 把 MobileGym env 包装成 environment bridge API |
| Docker daemon worker | `benchmark/docker/Dockerfile.agent-daemon` | WebUI 运行 job 时启动隔离的 agent daemon |

### 1.3 执行流程

单个 task 的主流程在 `runner/runtask.py`：

1. 检查 agent daemon 是否可用。
2. 清空 agent conversation history。
3. 如果有 `environment_url`，调用 environment 的 `/api/setup`。
4. 执行 suite/task 的可选 setup。
5. 如果有 `environment_url`，通过 `/api/screen` 获取 `pre.jpg`。
6. 调用 agent `/api/chat` 执行正式 prompt。
7. 如果有 `environment_url`，再次通过 `/api/screen` 获取 `post.jpg`。
8. 从 agent history 提取 tool trace。
9. 执行 hard assertions。
10. 如果启用 judge，提交 rubric、trace、final response、pre/post screenshot 给 judge model。
11. 写入 `manifest.json`、`results.jsonl`、`summary.md`、`report.html` 和每个 task 的 artifact。
12. 如果有 `environment_url`，调用 `/api/release` 释放 task route。

Judge 当前只使用两张图片：任务开始前的 `pre.jpg` 和任务结束后的 `post.jpg`。它不会消费每一步截图。

### 1.4 Environment bridge 是什么

Environment bridge 是 benchmark 连接真实设备、模拟器或其它环境的统一 HTTP 协议。Go agent 和 MobileGym bridge 都实现这组接口；Go agent daemon 也可以启用 environment bridge mode，把部分本地工具调用转发到另一个 bridge。

典型场景：

- WebUI 为每个 job/task worker 启动一个隔离的 Docker agent daemon。
- 这个 daemon 通过 `--environment-bridge-mode` 把设备相关工具转发到所选 environment bridge。
- 对 agent 来说，仍然是在调用普通工具，例如 `screenshot`、`touch_gesture`、`keyboard_text`。
- 对环境来说，实际收到的是 HTTP `/api/tools/<tool>` 请求。

默认 WebUI Docker daemon 转发的工具包括：

```text
screenshot,touch_gesture,keyboard_text,keyboard_tap,mouse_click,mouse_move,mouse_scroll,quick_action
```

需要注意：

- Environment bridge 是 agent 执行动作、runner 初始化环境和采集 pre/post screenshot 的统一通道。
- Runner 获取 pre/post screenshot 不通过 agent tool 调用，而是直接调用 environment bridge 的 `/api/screen`。
- CLI `run --agent-url ...` 是直接调用指定 agent，不会自动启动 environment bridge；如需 environment bridge，需要自行启动 daemon 并传入相关 daemon 参数。

### 1.5 Bridge server 的作用

Bridge server 是具体环境和 Aiden benchmark 之间的 HTTP 适配层。MobileGym bridge 把 MobileGym env 暴露成 environment bridge；Go agent 在连接真实设备时也暴露同一组接口。

接入新环境时参考 [`benchmark/environment_bridge.md`](environment_bridge.md)。

标准 environment bridge 接口：

| Endpoint | 用途 |
| --- | --- |
| `GET /health` | 健康检查 |
| `GET /api/tools` | 工具目录，供 agent health check 和 environment bridge 使用 |
| `POST /api/tools/<tool>` | 执行工具，例如 screenshot/touch/keyboard |
| `POST /api/setup` | 为某个 benchmark task reset/claim env |
| `POST /api/release` | 释放某个 benchmark task 占用的 env |
| `GET /api/concurrent` | 返回当前 bridge 支持的并发 task 数量 |
| `GET /api/screen` | 返回 JSON screenshot，runner 和 WebUI task screen 页面使用 |

并发 MobileGym 通过 `benchmark-task-id` 路由：

- WebUI 为每个 task worker 生成形如 `<suite-key>:<task-id>` 的 `benchmark-task-id`。
- Runner 调用 `/api/setup`、`/api/release`、`/api/screen` 时会带这个 header。
- Agent daemon 的 environment bridge tool 请求也会带同一个 benchmark task id。
- Bridge 根据这个 id 把请求路由到同一个 env。

如果 MobileGym environment 的 env pool 容量是 `N`，而 suite 中 task 数量大于 `N`：

- WebUI 通过 `/api/concurrent` 读取 bridge 容量，最多同时启动 `N` 个 task worker。
- 多出来的 task 在 runner 的 worker queue 中等待。
- 已完成 task 调用 `/api/release` 后，对应 env 可被后续 task 复用。

当前 WebUI 创建 MobileGym environment 时，`Envs` 默认值是 `5`。

### 1.6 Suite 结构

普通 suite 的最小结构：

```json
{
  "name": "phone_control_v1",
  "description": "Agent-driven phone control benchmark.",
  "prompt_prefix": "附加到每个 task prompt 前的通用约束。",
  "global_reset": {},
  "tasks": [
    {
      "id": "open_settings",
      "category": "single_step",
      "description_for_judge": "Agent must open system Settings.",
      "prompt": "请打开系统设置。",
      "rubric": [
        {
          "id": "in_settings",
          "check": "Post-screenshot shows the Settings app main page."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 1,
        "max_tool_calls": 8,
        "must_complete_within_sec": 90
      }
    }
  ]
}
```

常用字段：

| 字段 | 说明 |
| --- | --- |
| `prompt_prefix` | 每个 task prompt 的前缀，用于约束设备类型、工具使用方式等 |
| `global_reset` | suite 级 reset 配置 |
| `setup` | task 级前置步骤，目前支持 `{"type": "agent_prompt", ...}` |
| `rubric` | judge model 的判定项 |
| `hard_assertions` | 确定性检查，例如工具调用数量、超时、必需/禁用工具 |
| `repeats` | 单个 task 重复运行次数 |
| `input_screenshot` | 静态图片输入，适合 perception 类任务 |
| `expected_answer` | 多选题/确定答案任务的直接判定答案 |
| `trace_observations` | 对 trace 中特定行为的检查，例如是否读取某个 skill |

Unit suite 是另一种格式，`kind` 为 `unit`，用于直接测试某个 tool 的输入输出，不经过 agent chat。

## 2. WebUI 使用说明

### 2.1 启动

推荐命令：

```bash
cd benchmark
uv run python -m runner webui
```

默认地址：

```text
http://127.0.0.1:8765
```

常用参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--host` | `127.0.0.1` | WebUI 监听地址 |
| `--port` | `8765` | WebUI 监听端口 |
| `--suites-dir` | `benchmark/suites` | suite 扫描目录 |
| `--runs-dir` | `benchmark/runs/webui` | WebUI job、raw run、日志保存目录 |
| `--base-config-dir` | `benchmark/config` | agent 配置模板目录 |
| `--agent-config` | 空 | 指定 WebUI 使用的 agent.toml 保存路径 |
| `--daemon-image` | `aiden-agent-daemon:local` | job worker daemon 镜像 |
| `--mobilegym-image` | `aiden-mobilegym-simulator:py311` | MobileGym environment 镜像 |
| `--no-build-daemon-image` | false | 不自动构建 daemon 镜像 |
| `--no-build-mobilegym-image` | false | 不自动构建 MobileGym 镜像 |

### 2.2 页面区域

WebUI 首屏主要区域：

- Suites：左侧 suite 列表，可筛选、勾选一个或多个 suite。
- Run configuration：judge 开关、judge model、API key。
- Run selected suites：点击后弹出 environment 选择/配置窗口。
- Progress：当前 active job 的总体进度。
- Task workers：MobileGym 并发任务的 task 粒度状态、screen、log、stop。
- Jobs：历史 job，显示 job id、suite 名称、environment、状态、综合 report。
- Agent config：查看和编辑本次 WebUI 使用的 `agent.toml`。
- Log：查看 job 或 task worker 的 runner/daemon 日志。

### 2.3 配置 judge

WebUI 默认启用 judge。当前 judge 通过 OpenRouter 兼容接口调用，API key 使用 `OPENROUTER_API_KEY`。

使用方式：

1. 保持 `Enable judge` 勾选。
2. 填写 `Judge model`，默认是 `anthropic/claude-sonnet-4-6`。
3. 填写 API key。保存后 WebUI 会持久化设置，但页面不会回显明文 key。

如果只想收集 trace 和截图，不调用 judge，可以取消 `Enable judge`。

### 2.4 配置 environment

点击 `Run selected suites` 后会弹出 environment 窗口。

#### Device environment

Device environment 用于已有的设备/tool endpoint。填写：

- Name：自定义名称。
- Endpoint：HTTP endpoint。

WebUI 启动 job 时会：

1. 为 job 启动隔离 Docker agent daemon。
2. 把该 daemon 的设备工具通过 environment bridge 转发到这个 endpoint。
3. 用隔离 daemon 的 `/api/chat` 跑 benchmark。

这种方式适合需要隔离 agent 配置、日志、memory 的实机或外部工具环境。

#### MobileGym environment

MobileGym environment 由 WebUI 直接创建和管理：

1. 切到 `MobileGym` tab。
2. 填写名称。
3. 设置 `Envs`，默认 `5`。
4. 点击 `Start MobileGym`。
5. 等状态变为 running 后选择该 environment。
6. 点击弹窗底部 `Run selected suites`。

WebUI 会启动 MobileGym container 和 bridge server，并记录：

- Bridge endpoint：供 Docker daemon environment bridge 使用。
- Public endpoint：供 WebUI 和 runner 调用 `/api/concurrent`、`/api/setup`、`/api/screen`、`/api/release`。
- Task screen link：每个 task worker 的 screen 链接由 WebUI 提供，WebUI 后端通过 bridge `/api/screen` 拉取截图。

### 2.5 运行 job

流程：

1. 勾选一个或多个 suite。
2. 设置 judge。
3. 点击 `Run selected suites`。
4. 在弹窗中选择或创建 environment。
5. 点击弹窗底部确认运行。

运行后：

- Jobs 表显示 suite 名称和 job 状态。
- Progress 显示总体进度。
- MobileGym 并发模式下，Task workers 会显示每个 task 的状态、screen、log。
- 可以对整个 job 点击 Stop，也可以对单个 task worker 点击 Stop。

### 2.6 查看结果

Jobs 表中的 `report` 是综合 report，不是单个 task report。

Report 内容包括：

- task 列表和通过率。
- 每个 task 的 prompt、final response、hard assertions、rubric。
- pre/post screenshot。
- `View full trace`，用于人工查看执行过程和校验 judge 是否合理。

WebUI 运行数据默认保存在：

```text
benchmark/runs/webui/<job-id>/
├── job.json
├── state.json
├── runner.log
├── daemon.log
├── config/
├── raw/<run-id>/
└── workers/
```

WebUI 会持久化 job record。重启 WebUI 后，历史 job 会从 `job.json` 恢复；如果重启前 job 仍在运行，恢复时会标记为 stopped。

### 2.7 常见问题

#### MobileGym screen 没有显示

优先检查：

- MobileGym environment 是否 running。
- Task worker 是否已经开始运行。
- `screen` 链接是否带了 `benchmark-task-id`。
- 对应 task 是否已经 release；release 后 screen 可能没有 active env。

#### Judge 说没有截图

检查 report 中 task metrics：

- `pre_screenshot_file`
- `post_screenshot_file`
- `judge_image_count`
- `judge_image_labels`

MobileGym 模式下，runner 应通过 bridge `/api/screen` 生成 `pre.jpg` 和 `post.jpg`。如果缺失，通常是 environment endpoint 不可达、缺少 `benchmark-task-id` 路由，或 task 在截图前已经 release/失败。

#### Suite task 数量大于 Envs

这是正常情况。WebUI 会按 env pool 容量并发运行，剩余 task 排队。

## 3. CLI 使用说明

### 3.1 入口

推荐入口：

```bash
cd benchmark
uv run python -m runner <command> [options]
```

兼容入口：

```bash
python3 scripts/aiden_benchmark.py <command> [options]
```

如果省略 `<command>`，legacy 脚本会按 `run` 处理。

### 3.2 run：运行普通 suite

基本命令：

```bash
uv run python -m runner run \
  --suite suites/phone_control_v1.json \
  --agent-url http://127.0.0.1:8080
```

常用参数：

| 参数 | 说明 |
| --- | --- |
| `--suite PATH` | 必填，suite JSON 路径 |
| `--agent-url URL` | agent daemon 地址，默认 `http://localhost:8080` 或 `AIDEN_AGENT_URL` |
| `--environment-url URL` | 可选，environment bridge 地址；用于 `/api/setup`、`/api/screen`、`/api/release` |
| `--auto-agent-setup` | 忽略 `--agent-url`，自动按 `/api/concurrent` 并发启动隔离 agent daemon |
| `--daemon-image IMAGE` | `--auto-agent-setup` 使用的 agent daemon image |
| `--base-config-dir DIR` | `--auto-agent-setup` 使用的 agent 配置模板目录 |
| `--agent-config PATH` | `--auto-agent-setup` 使用的 agent.toml |
| `--no-build-daemon-image` | 不自动构建 agent daemon image |
| `--judge-model MODEL` | judge model，默认 `claude-sonnet-4-6` |
| `--no-judge` | 跳过 LLM judge，只做 hard assertions |
| `--repeats N` | 覆盖 task 内的 repeats |
| `--out DIR` | 输出目录，默认 `benchmark/runs` |
| `--state-file PATH` | 写入运行状态 JSON，WebUI 使用它显示进度 |
| `--task-id ID` | 只运行某个 task，可重复传入 |
| `--task-ids A,B` | 只运行逗号分隔的多个 task |
| `--run-id ID` | 指定 run 目录名 |
| `--benchmark-task-id ID` | environment 路由 id，MobileGym worker 使用 |
| `--skip-clock-wait` | 不等待 agent board clock 同步 |
| `--clock-timeout-sec N` | 等待 clock 同步的最长时间 |
| `--agent-ready-timeout-sec N` | 每个 task 前等待 agent ready 的时间 |
| `--agent-recovery-timeout-sec N` | timeout/skipped/failed 后的恢复等待时间 |
| `--inter-task-cooldown-sec N` | task 之间的冷却时间 |
| `--verbose` / `-v` | 输出详细 rubric 结果 |

只跑某几个 task：

```bash
uv run python -m runner run \
  --suite suites/phone_control_v1.json \
  --task-id open_settings \
  --task-id scroll_page_down
```

不调用 judge：

```bash
uv run python -m runner run \
  --suite suites/phone_control_v1.json \
  --no-judge
```

带 MobileGym bridge 的 CLI 运行：

```bash
uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --agent-url http://127.0.0.1:8080 \
  --environment-url http://127.0.0.1:8888
```

说明：

- `--agent-url` 是 agent daemon。
- `--environment-url` 是实现 `/api/setup`、`/api/screen`、`/api/release` 的 environment bridge endpoint。
- 如果没有 `--environment-url`，runner 仍可执行 agent chat，但不会保存 live pre/post screenshot；依赖视觉截图的 judge 结果会变弱。

自动启动 agent daemon 并按 bridge 容量并发运行：

```bash
uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --environment-url http://127.0.0.1:19090 \
  --auto-agent-setup
```

该模式会忽略 `--agent-url`，读取 environment bridge `/api/concurrent`；如果读取失败，按并发 `1` 运行。

### 3.3 unit：运行 tool 单元测试 suite

Unit suite 用于直接调用某个 tool，检查输出结构和错误状态。

运行单个 unit suite：

```bash
uv run python -m runner unit \
  --suite suites/unit/tools/quick_action_android_v1.json \
  --agent-url http://127.0.0.1:8080
```

运行目录下所有 unit suite：

```bash
uv run python -m runner unit \
  --suite-dir suites/unit/tools \
  --agent-url http://127.0.0.1:8080
```

`unit` 不调用 LLM judge。

### 3.4 rejudge：重新判分

当只修改 rubric 或想换 judge model 时，不需要重新操作设备：

```bash
uv run python -m runner rejudge \
  --run-dir runs/<run-id> \
  --judge-model anthropic/claude-sonnet-4-6
```

`rejudge` 会读取已有 artifact 和 `rubric_spec`，重写 judge verdict/status。

### 3.5 compare：比较两次 run

```bash
uv run python -m runner compare \
  --runs runs/<run-a> runs/<run-b>
```

用于查看任务状态变化和性能变化，适合回归检查。

### 3.6 webui：从 CLI 启动 WebUI

```bash
uv run python -m runner webui \
  --host 127.0.0.1 \
  --port 8765
```

这个命令和第 2 节 WebUI 使用的是同一个入口。

### 3.7 start-mobilegym-env：启动 MobileGym environment

如果不使用 WebUI，也可以从 CLI 启动一个 detached MobileGym simulator + bridge container：

```bash
uv run python -m runner start-mobilegym-env
```

命令会打印：

- `environment_url`：runner 使用的 `--environment-url`。
- `docker_environment_url`：agent daemon 容器内访问 bridge 时使用的 endpoint。
- `web_url`：MobileGym simulator 页面。
- `stop_command`：停止该 environment 的命令。

常用参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--envs` / `--parallel-envs` | `1` | bridge 后面的 MobileGym env 数量 |
| `--web-port` | auto | MobileGym web 页面端口 |
| `--bridge-port` | auto | bridge API 端口 |
| `--mobilegym-image` | `aiden-mobilegym-simulator:py311` | MobileGym container image |
| `--no-build-mobilegym-image` | false | 只使用本地已有镜像 |
| `--json` | false | 输出机器可读 JSON |

`start-mobilegym-env` 不绑定 `benchmark-task-id`。Bridge 根据后续每个 setup、screen 或 tool 请求里携带的 `benchmark-task-id` 做路由。

### 3.8 start-agent-daemon：启动 agent daemon

启动一个 detached benchmark agent daemon container：

```bash
uv run python -m runner start-agent-daemon \
  --environment-bridge-endpoint http://127.0.0.1:19090 \
  --benchmark-task-id cli-task
```

命令会打印：

- `agent_url`：runner 使用的 `--agent-url`。
- `environment_bridge_endpoint`：宿主机视角的 environment endpoint。
- `docker_environment_bridge_endpoint`：容器内视角的 environment endpoint。
- `benchmark_task_id`：daemon environment bridge 请求会携带的 route id。
- `stop_command`：停止该 daemon 的命令。

常用参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--port` | auto | agent daemon API 端口 |
| `--environment-bridge-endpoint` | 空 | 设备或 MobileGym bridge endpoint；为空时不启用 environment bridge |
| `--benchmark-task-id` | `cli-task` | environment bridge 请求使用的 route id |
| `--agent-config` | 空 | 指定 agent.toml |
| `--base-config-dir` | `benchmark/config` | agent 配置模板目录 |
| `--daemon-image` | `aiden-agent-daemon:local` | agent daemon image |
| `--no-build-daemon-image` | false | 只使用本地已有镜像 |
| `--json` | false | 输出机器可读 JSON |

推荐的 CLI MobileGym 调试流程：

```bash
uv run python -m runner start-mobilegym-env --bridge-port 19090

uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --environment-url http://127.0.0.1:19090 \
  --auto-agent-setup
```

如需手动固定一个 route 调试单 task，也可以显式启动 agent daemon：

```bash
uv run python -m runner start-agent-daemon \
  --port 18081 \
  --environment-bridge-endpoint http://127.0.0.1:19090 \
  --benchmark-task-id cli-task

uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --agent-url http://127.0.0.1:18081 \
  --environment-url http://127.0.0.1:19090 \
  --benchmark-task-id cli-task
```

注意：如果 MobileGym bridge 后面有多个 env，`/api/setup`、`/api/screen`、`/api/tools/*` 和 `/api/release` 都必须使用同一个 `benchmark-task-id`。CLI 手动启动一个长期 agent daemon 时，建议用固定 route id，例如 `cli-task`；需要并发跑多个 task 时，优先使用 WebUI，让每个 task worker 拥有独立 daemon 和独立 route id。

## 4. 输出与报告

普通 CLI run 默认输出：

```text
benchmark/runs/<run-id>/
├── manifest.json
├── results.jsonl
├── summary.md
├── report.html
├── _judge_cache/
└── tasks/<task-id>/
    ├── pre.jpg
    ├── post.jpg
    ├── history.json
    ├── trace.json
    └── judge.json
```

关键文件：

| 文件 | 说明 |
| --- | --- |
| `manifest.json` | run 元数据、suite sha、git sha、totals |
| `results.jsonl` | 每个 task 一行结果，便于脚本处理 |
| `summary.md` | 文本摘要 |
| `report.html` | 人工查看的主报告 |
| `history.json` | agent 原始 history |
| `trace.json` | 从 history 抽取的工具调用和 final response |
| `pre.jpg` / `post.jpg` | judge 使用的前后截图 |
| `judge.json` | judge verdict、reason、image_count、cache_key |

Runner 结束时会尝试把 report 上传到 agent board 的 `/benchmark` 页面。如果上传失败，本地 `report.html` 仍然可用。

## 5. 环境变量

常用环境变量：

| 变量 | 说明 |
| --- | --- |
| `AIDEN_AGENT_URL` | CLI `--agent-url` 默认值 |
| `AIDEN_ENVIRONMENT_URL` | CLI `--environment-url` 默认值 |
| `OPENROUTER_API_KEY` | judge API key |
| `AIDEN_MODEL` / `MODEL_NAME` / `OPENAI_MODEL` | 记录到 manifest 的 agent model |
| `BENCHMARK_STATE_FILE` | CLI `--state-file` 默认值 |
| `MOBILEGYM_PARALLEL_ENVS` | `start_simulator.py` 默认 parallel env 数 |
| `AIDEN_BRIDGE_BIND_HOST` | MobileGym bridge bind host |
| `AIDEN_BRIDGE_PORT` | MobileGym bridge port |
| `AIDEN_BRIDGE_PUBLIC_HOST` | Docker 场景下 bridge 对外 hostname |

## 6. 推荐工作流

### 调试单个任务

```bash
uv run python -m runner run \
  --suite suites/phone_control_v1.json \
  --task-id open_settings \
  --no-judge \
  --verbose
```

先确认 agent 能完成操作，再打开 judge。

### 跑完整回归

```bash
uv run python -m runner run \
  --suite suites/phone_control_v1.json \
  --judge-model anthropic/claude-sonnet-4-6 \
  --verbose
```

### MobileGym 并发回归

推荐使用 WebUI：

```bash
uv run python -m runner webui
```

在页面中：

1. Start MobileGym，设置 `Envs`。
2. 勾选 suite。
3. 点击 `Run selected suites`。
4. 选择 MobileGym environment。
5. 运行并查看 Task workers、screen、log 和 report。

### 改 rubric 后重新判分

```bash
uv run python -m runner rejudge \
  --run-dir runs/<run-id> \
  --judge-model anthropic/claude-sonnet-4-6
```

## 7. 故障排查

### agent 不可达

现象：

```text
agent at http://... is not reachable
```

检查：

```bash
curl http://127.0.0.1:8080/api/tools
```

如果是 WebUI job，查看 job 的 daemon log。

### judge 报 missing env var OPENROUTER_API_KEY

CLI：

```bash
export OPENROUTER_API_KEY=...
```

WebUI：

在 Run configuration 中填写 API key，或关闭 `Enable judge`。

### report 没有 pre/post 图片

检查是否传入了 `--environment-url`，且该 endpoint 支持：

```bash
curl http://127.0.0.1:8888/api/screen
```

MobileGym 并发时需要带 task id：

```bash
curl -H 'benchmark-task-id: suite.json:task_id' \
  http://127.0.0.1:8888/api/screen
```

### MobileGym task 一直 pending 或 no env available

检查：

- MobileGym environment 是否 running。
- `Envs` 是否大于 0。
- 任务请求是否带 `benchmark-task-id`。
- 是否有任务异常退出但没有 release；停止 job 后重新启动 environment 通常可以清理状态。

### WebUI 重启后运行中的 job 变 stopped

这是当前设计。WebUI 会持久化历史记录，但不会恢复已中断的进程；重启时未终态 job 会标记为 stopped，避免误显示为仍在运行。
