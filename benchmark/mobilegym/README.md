# Aiden MobileGym Integration

MobileGym 作为纯模拟器集成到 Aiden benchmark，使用统一的 `benchmark/runner` 框架。

## 🎯 架构设计

MobileGym **仅作为设备模拟器**，通过统一的 environment bridge API 提供服务：

```text
benchmark/runner/main.py (测试编排)
  ↓ /api/chat
Aiden Go Daemon (environment-bridge mode)
  ↓ /api/tools/* (统一工具接口)
MobileGym Bridge Server (HTTP ↔ env.step)
  ↓ env.step(action)
MobileGym Simulator (bench_env)
```

**关键点**：
- ✅ 使用 `benchmark/runner` 统一测试流程
- ✅ 使用 `benchmark/suites/*.json` 定义测试任务
- ✅ Bridge Server 提供 environment bridge 接口，与 Go agent 调度协议对齐
- ✅ 通过 environment bridge 模式连接，无需特殊配置
- ❌ 不使用 MobileGym 的 `SerialRunner`、`factory`、agent 注册

## 🚀 快速开始

### 方式 1：本地运行（开发）

```bash
# 1. 启动 MobileGym 模拟器和 Bridge Server（后台）
python benchmark/mobilegym/scripts/start_simulator.py \
  --env-url http://localhost:4173 \
  --bridge-port 8888 &

# 2. 启动 Aiden daemon 使用 environment bridge 模式。
# 手动调试单 task 时，daemon 和 runner 要使用同一个 benchmark-task-id。
go run src/agent/cmd/daemon/main.go \
  --config /path/to/agent.toml \
  --environment-bridge-mode \
  --environment-bridge-endpoint http://localhost:8888 \
  --environment-bridge-tools touch_gesture,keyboard_text,keyboard_tap \
  --benchmark-task-id cli-task &

# 3. 运行 benchmark（使用标准 runner）
cd benchmark
uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --agent-url http://localhost:8080 \
  --environment-url http://localhost:8888 \
  --benchmark-task-id cli-task
```

**注意**：现在使用统一的 environment bridge 模式，不再需要 `device.backend=mobilegym` 配置。

### 方式 2：WebUI / CLI services（推荐）

```bash
# WebUI 会按需构建 MobileGym simulator image 和 agent daemon image。
cd benchmark
uv run python -m runner webui
```

也可以从 CLI 启动环境和 daemon：

```bash
cd benchmark
uv run python -m runner start-mobilegym-env --envs 5
uv run python -m runner start-agent-daemon --environment-bridge-endpoint http://127.0.0.1:<bridge-port>
uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --agent-url http://127.0.0.1:<agent-port> \
  --environment-url http://127.0.0.1:<bridge-port>
```

## 📁 目录结构

```text
benchmark/mobilegym/
├── README.md                       # 本文件
├── bridge/                         # Bridge server（HTTP ↔ MobileGym env）⭐
│   ├── server.py                   # HTTP 端点
│   ├── episode.py                  # Episode 状态管理
│   ├── protocol.py                 # 协议定义
│   └── actions.py                  # Action 转换
├── docker/                         # WebUI/CLI 使用的 MobileGym base image
│   ├── Dockerfile                  # mobilegym-base target
│   └── README.md                   # 当前 Docker 入口说明
├── scripts/                        # 启动脚本 ⭐
│   ├── start_simulator.py          # 启动纯模拟器
│   └── configure_daemon.py         # 配置 daemon bridge
└── vendor/mobilegym/               # 上游 MobileGym (submodule)
```

**移除的组件**（不再需要）：
- ❌ `adapter/register.py` - agent 注册
- ❌ `adapter/aiden_go_agent.py` - MobileGym Agent 适配器
- ❌ `scripts/run_aiden.py` - MobileGym 测试框架入口
- ❌ `suites/*.yaml` - MobileGym 自定义 suite（改用 benchmark/suites/*.json）

## 🎯 测试定义

所有测试任务定义在 `benchmark/suites/*.json`，使用统一格式：

```json
{
  "name": "mobilegym_basic",
  "prompt_prefix": "你正在控制一台 Android 模拟器。",
  "global_reset": {
    "type": "agent_prompt",
    "prompt": "请重置设备到初始状态",
    "timeout_sec": 30,
    "clear_history_after": true
  },
  "tasks": [
    {
      "id": "clock_count_alarms",
      "category": "device_operation",
      "prompt": "打开时钟应用，告诉我有多少个闹钟。",
      "description_for_judge": "Agent 应该打开时钟应用并正确报告闹钟数量",
      "rubric": [
        {
          "id": "opened_clock_app",
          "check": "Post-screenshot shows the Clock app."
        },
        {
          "id": "counted_alarms",
          "check": "Final response reports the correct alarm count."
        }
      ],
      "hard_assertions": {
        "must_complete_within_sec": 120,
        "min_tool_calls": 2,
        "required_tools": ["screenshot", "touch_gesture"]
      }
    }
  ]
}
```

## 🔧 配置说明

### Environment Bridge 模式

使用命令行参数启动 daemon：

```bash
go run cmd/daemon/main.go \
  --config agent.toml \
  --environment-bridge-mode \
  --environment-bridge-endpoint http://localhost:8888 \
  --environment-bridge-tools touch_gesture,keyboard_text,keyboard_tap \
  --benchmark-task-id cli-task
```

### Bridge 环境变量

启动 simulator 时可用的环境变量：

- `MOBILEGYM_ENV_URL`: MobileGym web 模拟器地址（默认 `http://localhost:4173`）
- `AIDEN_BRIDGE_BIND_HOST`: Bridge 绑定地址（默认 `127.0.0.1`）
- `AIDEN_BRIDGE_PORT`: Bridge 端口（默认自动分配）
- `AIDEN_BRIDGE_PUBLIC_HOST`: Bridge 公开地址（Docker 需要）

## ✅ 验证

```bash
# 检查模拟器健康状态
curl http://localhost:8888/health

# claim 一个 task route
curl -X POST http://localhost:8888/api/setup \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: cli-task" \
  -d '{}'

# 获取 runner/judge 使用的截图
curl -X POST http://localhost:8888/api/providers/screenshot \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: cli-task" \
  -d '{"format": "jpeg", "quality": 80}'

# 测试 bridge tool 操作
curl -X POST http://localhost:8888/api/tools/touch_gesture \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: cli-task" \
  -d '{"input": {"type": "tap", "point": {"x": 500, "y": 800}}}'

# 运行单个测试
cd benchmark
uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --agent-url http://localhost:8080 \
  --environment-url http://localhost:8888 \
  --benchmark-task-id cli-task \
  --no-judge  # 快速验证，跳过 judge
```

## 📚 相关文档

- **统一 Tool API**: [bridge/TOOLS_API.md](bridge/TOOLS_API.md) ⭐ **新增**
- **Docker 使用**: [docker/README.md](docker/README.md)
- **Bridge 协议**: 见 `bridge/` 目录下的 Python 实现

## 🐛 故障排查

### 模拟器无法启动
```bash
# 检查 MobileGym 依赖
pip install -r benchmark/mobilegym/vendor/mobilegym/bench_env/requirements.txt
playwright install chromium
```

### Bridge 连接失败
```bash
# 检查 bridge 健康状态
curl http://localhost:8888/health
```

### Daemon 无法调用设备工具
```bash
# 确认 daemon 健康
curl http://localhost:8080/health

# 确认 bridge tool catalog
curl http://localhost:8888/api/tools
```
