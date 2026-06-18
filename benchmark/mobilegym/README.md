# Aiden MobileGym Integration

MobileGym 作为纯模拟器集成到 Aiden benchmark，使用统一的 `benchmark/runner` 框架。

## 🎯 架构设计

MobileGym **仅作为设备模拟器**，通过统一的 `/api/tools` endpoint 提供服务：

```
benchmark/runner/main.py (测试编排)
  ↓ /api/chat
Aiden Go Daemon (tool-proxy-endpoint)
  ↓ /api/tools/* (统一工具接口)
MobileGym Bridge Server (HTTP ↔ env.step)
  ↓ env.step(action)
MobileGym Simulator (bench_env)
```

**关键点**：
- ✅ 使用 `benchmark/runner` 统一测试流程
- ✅ 使用 `benchmark/suites/*.json` 定义测试任务
- ✅ Bridge Server 提供 `/api/tools` 统一接口，与 Go agent 完全兼容
- ✅ 通过 tool proxy 模式连接，无需特殊配置
- ❌ 不使用 MobileGym 的 `SerialRunner`、`factory`、agent 注册

## 🚀 快速开始

### 方式 1：本地运行（开发）

```bash
# 1. 启动 MobileGym 模拟器和 Bridge Server（后台）
python benchmark/mobilegym/scripts/start_simulator.py \
  --env-url http://localhost:4173 \
  --bridge-port 8888 &

# 2. 启动 Aiden daemon 使用 tool proxy 模式
# 启动 daemon，指定 tool proxy endpoint
go run src/agent/cmd/daemon/main.go \
  --config /path/to/agent.toml \
  --tool-proxy-endpoint http://localhost:8888 \
  --tool-proxy-forward screenshot,touch_gesture,keyboard_text,keyboard_tap &

# 3. 运行 benchmark（使用标准 runner）
python -m benchmark.runner run \
  --suite benchmark/suites/mobilegym_basic.json \
  --agent-url http://localhost:8080
```

**注意**：现在使用统一的 tool proxy 模式，不再需要 `device.backend=mobilegym` 配置。

### 方式 2：Docker（推荐）

```bash
cd benchmark/mobilegym/docker

# 1. 初始化配置
./init.sh

# 2. 编辑 agent.toml 填入 API key
vim ../config/agent.toml

# 3. 构建镜像
docker compose build

# 4. 启动所有服务
docker compose up -d

# 5. 运行 benchmark
docker compose run --rm runner \
  --suite /benchmark/suites/mobilegym_basic.json \
  --agent-url http://aiden-daemon:8080

# 6. 查看结果
ls ../../runs/
open ../../runs/<run-id>/report.html
```

## 📁 目录结构

```text
benchmark/mobilegym/
├── README.md                       # 本文件
├── REFACTOR_PLAN.md                # 重构说明
├── bridge/                         # Bridge server（HTTP ↔ MobileGym env）⭐
│   ├── server.py                   # HTTP 端点
│   ├── episode.py                  # Episode 状态管理
│   ├── protocol.py                 # 协议定义
│   └── actions.py                  # Action 转换
├── config/                         # Aiden 配置
│   ├── agent.toml.template         # 配置模板
│   └── skills/device-operator/     # MobileGym skill
├── docker/                         # Docker 编排
│   ├── Dockerfile.simulator        # 模拟器镜像
│   ├── Dockerfile.runner           # Runner 镜像
│   ├── docker-compose.yml          # 服务编排
│   └── init.sh                     # 初始化脚本
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
          "description": "成功打开时钟应用"
        },
        {
          "id": "counted_alarms",
          "description": "正确识别并报告闹钟数量"
        }
      ],
      "hard_assertions": {
        "must_complete_within_sec": 120,
        "min_tool_calls": 2,
        "required_tools": ["screenshot", "tap"]
      }
    }
  ]
}
```

## 🔧 配置说明

### Tool Proxy 模式

使用命令行参数启动 daemon：

```bash
go run cmd/daemon/main.go \
  --config agent.toml \
  --tool-proxy-endpoint http://localhost:8888 \
  --tool-proxy-forward screenshot,touch_gesture,keyboard_text,keyboard_tap
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

# 测试 bridge 操作
curl -X POST http://localhost:8888/screenshot \
  -H "Content-Type: application/json" \
  -d '{"episode_id": "test-001"}'

# 运行单个测试
python -m benchmark.runner run \
  --suite benchmark/suites/mobilegym_basic.json \
  --agent-url http://localhost:8080 \
  --no-judge  # 快速验证，跳过 judge
```

## 📚 相关文档

- **统一 Tool API**: [bridge/TOOLS_API.md](bridge/TOOLS_API.md) ⭐ **新增**
- **重构说明**: [REFACTOR_PLAN.md](REFACTOR_PLAN.md)
- **迁移指南**: [MIGRATION.md](MIGRATION.md)
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
# 确认 device backend 配置
curl http://localhost:8080/api/mobilegym/bridge/status

# 如果使用 configure_daemon.py，确认已成功配置
curl http://localhost:8080/api/mobilegym/bridge/status
```
