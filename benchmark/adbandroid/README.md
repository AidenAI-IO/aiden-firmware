# ADB Android Environment Bridge

通过 adb 控制 Android 模拟器（Genymotion）或真机，并向 benchmark 暴露与
MobileGym bridge **完全兼容的 HTTP 协议**。Aiden Go agent 不感知设备实现，仍然只用
`--environment-bridge-mode` 转发工具调用。

```text
benchmark/runner (测试编排)
  ↓ /api/chat
Aiden Go Daemon (environment-bridge mode)
  ↓ POST /api/tools/{tool}         ← 工具调用（screenshot / touch_gesture / ...）
ADB Android Bridge (本模块, 本地 Python 进程)
  ↓ adb -s <serial> shell input ...
Android 模拟器 / 真机
```

与 MobileGym 的差异：MobileGym 跑在 Docker 里、支持多环境并发；ADB bridge 是**本地进程 +
单设备**（`/api/concurrent` 恒为 1），不需要 Docker。

## 前置条件

1. **adb 可用**：`adb` 在 PATH 中（或用 `--adb-path` 指定），`adb devices` 能看到目标设备：

   ```text
   $ adb devices
   List of devices attached
   127.0.0.1:6555	device        # Genymotion（默认值）
   emulator-5554	device        # 官方 AVD 是这个名字
   XXXXXXXX	device            # USB 真机
   ```

   ⚠️ 默认 serial 是 Genymotion 的 `127.0.0.1:6555`。用 AVD 或真机必须显式传
   `--adb-serial <serial>`（或设置环境变量 `ANDROID_SERIAL`）。

2. **Python 依赖**：在 `benchmark/` 目录用 `uv` 运行即可（Pillow 用于截图压缩，已在
   `pyproject.toml` 依赖中）。

3. **agent 配置**：Go daemon 需要你自己的配置目录（LLM provider 与 API key），参考
   `benchmark/config/agent.toml.template`。仓库不含任何密钥。

## 快速开始

### 方式 1：CLI（推荐）

```bash
cd benchmark

# 1. 启动 bridge（后台进程，输出 environment_url 等信息）
uv run python -m runner start-adb-android-env \
  --adb-serial 127.0.0.1:6555 \
  --bridge-port 8899

# 2. 启动 agent daemon（Docker 容器，自动构建镜像）
uv run python -m runner start-agent-daemon \
  --environment-bridge-endpoint http://127.0.0.1:8899

# 3. 运行 benchmark（--agent-url / token 参数以第 2 步输出为准）
#    上面这条会启用 judge——需先 export OPENROUTER_API_KEY，详见「判分」一节。
#    只想验证链路、不判分时，追加 --no-judge。
uv run python -m runner run \
  --suite suites/adb_android_basic.json \
  --agent-url http://127.0.0.1:<agent-port> \
  --environment-url http://127.0.0.1:8899 \
  --benchmark-task-id cli-task \
  -v
```

停止 bridge：用第 1 步输出里的 `stop_command`（`kill -TERM <pid>`）。

### 方式 2：手动启动（调试 Go daemon 时）

```bash
# 1. 直接启动 bridge 前台进程
cd benchmark
uv run python -m adbandroid.scripts.start_bridge \
  --adb-serial 127.0.0.1:6555 \
  --bridge-port 8899

# 2. 本地跑 daemon（注意 daemon 和 runner 必须用同一个 benchmark-task-id）
cd ../src/agent
go run ./cmd/daemon \
  -config <你的配置目录> \
  --environment-bridge-mode \
  --environment-bridge-endpoint http://127.0.0.1:8899 \
  --environment-bridge-tools "screenshot,touch_gesture,keyboard_text,keyboard_tap,enter_text_in_field,enter_text_via_bridge,mouse_click,mouse_move,mouse_scroll,quick_action" \
  --benchmark-task-id cli-task

# 3. 运行 benchmark（同方式 1 第 3 步，--benchmark-task-id cli-task）
```

### 方式 3：WebUI

```bash
cd benchmark
uv run python -m runner webui
```

浏览器打开后：选择 suite → Run → 环境弹窗切到 **ADB Android** 标签 → 填 serial →
Start → 选中该环境 → Run。WebUI 负责启动/停止 bridge 进程，重启后能通过
pidfile + `/health` 探测找回仍在运行的 bridge。

## 判分（judge）

judge 用每个任务的 pre/post 截图 + trace 逐条评 rubric（例如"设置真的打开了吗"、
"闹钟数对了吗"），走 OpenRouter 兼容接口。judge 模型与被测 agent 模型完全独立
（agent 模型在 `agent.toml` 里配）。

- **`--no-judge`**：不判分，rubric 记 0/N，通过与否只看硬断言（有动作、未超时）。
  适合先验证链路是否跑通。
- **启用 judge**（`run` 的默认行为，即不加 `--no-judge`）：必须提供
  `OPENROUTER_API_KEY`，否则报 `missing env var OPENROUTER_API_KEY`。

```bash
cd benchmark
export OPENROUTER_API_KEY=sk-or-...

uv run python -m runner run \
  --suite suites/adb_android_basic.json \
  --agent-url http://127.0.0.1:<agent-port> \
  --environment-url http://127.0.0.1:8899 \
  --benchmark-task-id cli-task \
  --judge-model anthropic/claude-sonnet-4-6 \
  -v
```

`--judge-model` 可省略（默认 `anthropic/claude-sonnet-4-6`）；想换判分模型时传 OpenRouter 的
模型名，如 `anthropic/claude-sonnet-4-6`。

> 多步任务（时钟数闹钟 / 检查 WiFi / 打开应用抽屉）的正确性只有 judge 能验证，
> `--no-judge` 下它们的 rubric 恒为 0/N。

### 对已有结果补判分（rejudge）

已用 `--no-judge` 跑完一轮后，pre/post 截图和 trace 都存在 run 目录里，可以**不重跑
真机**直接补判分：

```bash
export OPENROUTER_API_KEY=sk-or-...
uv run python -m runner rejudge \
  --run-dir runs/<那次运行的目录> \
  --judge-model anthropic/claude-sonnet-4-6
```

调 rubric 措辞、或先跑 no-judge 验证链路再补分时，这样省掉一整轮真机操作。

## 测试套件

`benchmark/suites/adb_android_basic.json`，8 个任务：

- 基础能力（5）：截图、返回 Home、打开设置、滑动、输入英文文本
- 多步任务（3，移植自 mobilegym_basic）：打开时钟数闹钟、检查 WiFi 状态、打开应用抽屉

注意：动作类任务的 `required_tools` 故意留空——daemon 的每个动作工具都自带
post-action 截图，且 agent 可能用 `quick_action` 或 `touch_gesture` 达成同一目标。
多步任务的正确性主要靠 judge rubric 评定，`--no-judge` 下硬断言只能验证「有动作且未超时」。

## HTTP 协议

与 MobileGym bridge 一致（runner 和 Go agent 均按此对接）：

| 端点 | 说明 |
|---|---|
| `GET /health` | 设备在线返回 200；adb 不可达返回 503 |
| `GET /api/concurrent` | `{"ok":true,"data":{"concurrent":1,...}}`，单设备恒为 1 |
| `GET /api/screen` | 当前截图（无需 setup，供 runner 抓 pre/post 截图） |
| `POST /api/setup` | 复位到主屏 + 建立 episode；按 `benchmark-task-id` 头做单设备占用 |
| `POST /api/release` | 释放 task id 占用 |
| `GET /api/tools` | 工具目录 |
| `POST /api/tools/{tool}` | 工具调用（Go agent 转发入口） |

任务路由语义（单设备版，与 MobileGym 单 env 行为对齐）：

- **空 `benchmark-task-id`**：直接落到唯一设备状态，不做占用校验（WebUI 串行模式下
  daemon 工具调用不带该头，依赖此行为）
- **带 task id**：首次 setup 占用；同 id 幂等；不同 id 未 release 时返回
  `429 no_bridge_env_available`

## 工具与坐标

支持的工具与 MobileGym 相同：`screenshot` `touch_gesture` `keyboard_text` `keyboard_tap`
`enter_text_in_field` `enter_text_via_bridge` `mouse_click` `mouse_move` `mouse_scroll`
`quick_action`。

坐标空间（`coord_space`）：

- `normalized`（默认）：0-1000 → 按 `adb shell wm size` 换算为像素（Override 优先）
- `absolute`：0-32767（HID 空间）→ 像素
- `auto`：仅接受 0-1000，越界报错（与 MobileGym 一致）
- `pixel`：ADB bridge 额外支持的真实像素坐标，需显式指定并会 clamp 到屏幕范围

`quick_action`（`platform=android`）：`back` / `home` / `app_switch` / `send` /
`open_settings`（`am start -a android.settings.SETTINGS`）/ `notification_center` /
`control_center` / `dismiss_panel`（`cmd statusbar` 系列，失败自动回退手势）。
传 `{"list": true}` 可查完整 catalog。

## 已知限制

- **文本输入仅支持英文**：`adb input text` 无法输入中文/IME 组合文本，非 ASCII 会返回
  明确错误；文本中的字面 `%s` 会被输成空格（`input text` 的转义约定）。中文输入需要
  clipboard/IME 方案，暂未实现。
- **单设备串行**：一个 bridge 只管一台设备，不支持并发任务。多设备需起多个 bridge
  实例（不同端口 + 不同 serial）。
- 动作后固定等待 0.6s 再截图（`DEFAULT_ACTION_SETTLE_SEC`），慢速模拟器上个别转场
  动画可能仍未结束，agent 可再调一次 `screenshot` 确认。

## 排障

| 现象 | 原因 / 处理 |
|---|---|
| `/health` 返回 503 `device_unavailable` | `adb devices` 确认 serial 正确且状态为 `device`（不是 `offline`/`unauthorized`） |
| 启动报 `adb binary not found` | adb 不在 PATH，传 `--adb-path /path/to/adb` |
| 工具调用返回 429 `no_bridge_env_available` | 设备被其他 task id 占用：确认 daemon 与 runner 的 `--benchmark-task-id` 一致，或调 `/api/release` |
| 工具调用返回 409 `no_active_episode` | 未先调 `/api/setup`（runner 会自动做；手动 curl 调试时需先 setup） |
| 截图报 Pillow 相关 ImportError | venv 安装损坏，`uv sync --reinstall-package pillow` |

单元测试（无需真机）：

```bash
cd benchmark
uv run pytest tests/test_adbandroid_bridge.py tests/test_adbandroid_tools.py
```
