# Aiden MobileGym Integration

MobileGym 作为纯模拟器集成到 Aiden benchmark，使用统一的 `benchmark/runner` 框架。

## 🎯 架构设计

MobileGym **仅作为设备模拟器**，不使用其测试框架：

```
benchmark/runner/main.py (测试编排)
  ↓ /api/chat
Aiden Go Daemon (配置 device-operator skill)
  ↓ /tap, /swipe, /screenshot (HTTP)
MobileGym Bridge Server (HTTP ↔ env.step)
  ↓ env.step(action)
MobileGym Simulator (bench_env)
```

**关键点**：
- ✅ 使用 `benchmark/runner` 统一测试流程
- ✅ 使用 `benchmark/suites/*.json` 定义测试任务
- ✅ MobileGym 仅提供设备环境，不涉及测试逻辑
- ❌ 不使用 MobileGym 的 `SerialRunner`、`factory`、agent 注册

## 🚀 快速开始

### 方式 1：本地运行（开发）

```bash
# 1. 启动 MobileGym 模拟器（后台）
python benchmark/mobilegym/scripts/start_simulator.py \
  --env-url http://localhost:4173 \
  --bridge-port 8888 \
  --token-dir /tmp/mobilegym-tokens \
  --bridge-url-file /tmp/mobilegym-bridge-url &

# 2. 启动 Aiden daemon（假设已配置 agent.toml）
# daemon 需要配置 device-operator skill 指向 bridge

# 3. 配置 daemon 连接 bridge
python benchmark/mobilegym/scripts/configure_daemon.py \
  --daemon-url http://localhost:8080 \
  --bridge-url $(cat /tmp/mobilegym-bridge-url) \
  --bridge-token-file /tmp/mobilegym-tokens/bridge_device_token

# 4. 运行 benchmark（使用标准 runner）
python -m benchmark.runner run \
  --suite benchmark/suites/mobilegym_basic.json \
  --agent-url http://localhost:8080
```

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

### Aiden agent.toml

确保配置了 `device-operator` skill：

```toml
[skills.device-operator]
path = "benchmark/mobilegym/config/skills/device-operator"
enabled = true
```

### Bridge 环境变量

启动 simulator 时可用的环境变量：

- `MOBILEGYM_ENV_URL`: MobileGym web 模拟器地址（默认 `http://localhost:4173`）
- `AIDEN_BRIDGE_BIND_HOST`: Bridge 绑定地址（默认 `127.0.0.1`）
- `AIDEN_BRIDGE_PORT`: Bridge 端口（默认自动分配）
- `AIDEN_BRIDGE_PUBLIC_HOST`: Bridge 公开地址（Docker 需要）
- `BRIDGE_CONTROL_TOKEN`: Bridge 控制 token（默认自动生成）
- `BRIDGE_DEVICE_TOKEN`: Bridge 设备 token（默认自动生成）

## ✅ 验证

```bash
# 检查模拟器健康状态
curl http://localhost:8888/health

# 测试 bridge 操作
curl -X POST http://localhost:8888/screenshot \
  -H "Authorization: Bearer <device-token>" \
  -H "Content-Type: application/json" \
  -d '{"episode_id": "test-001"}'

# 运行单个测试
python -m benchmark.runner run \
  --suite benchmark/suites/mobilegym_basic.json \
  --agent-url http://localhost:8080 \
  --no-judge  # 快速验证，跳过 judge
```

## 🔄 与旧版本对比

### 旧版（使用 MobileGym 框架）
```bash
# 依赖 MobileGym SerialRunner、agent 注册
python benchmark/mobilegym/scripts/run_aiden.py \
  --suite clock \
  --aiden-daemon-url http://localhost:8080
```

### 新版（纯模拟器）
```bash
# 1. 启动模拟器
python benchmark/mobilegym/scripts/start_simulator.py &

# 2. 使用统一 runner
python -m benchmark.runner run \
  --suite benchmark/suites/mobilegym_clock.json \
  --agent-url http://localhost:8080
```

**优势**：
1. **解耦**：MobileGym 仅作为设备模拟器
2. **统一**：所有 benchmark 使用相同的 runner 和 suite 格式
3. **简化**：不需要适配 MobileGym Agent 接口
4. **灵活**：可以轻松替换为其他模拟器或真实设备

## 📚 相关文档

- **重构说明**: [REFACTOR_PLAN.md](REFACTOR_PLAN.md)
- **Bridge 协议**: [bridge/README.md](bridge/README.md)
- **Docker 使用**: [docker/README.md](docker/README.md)
- **架构设计**: 见项目根目录文档

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

# 检查 token 是否正确
cat /tmp/mobilegym-tokens/bridge_device_token
```

### Daemon 无法调用设备工具
```bash
# 确认 skill 已加载
curl http://localhost:8080/api/skills

# 确认 bridge 配置
curl http://localhost:8080/api/mobilegym/bridge/status
```
