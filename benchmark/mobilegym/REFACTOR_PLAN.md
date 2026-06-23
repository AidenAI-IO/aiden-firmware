# MobileGym 重构计划：仅作为模拟器使用

## 当前问题

当前实现依赖了 MobileGym 的测试框架：
- 使用 `bench_env.runner.SerialRunner` 来执行测试
- 使用 `bench_env.factory` 来加载任务和创建环境
- 使用 `bench_env.agent.register_agent` 注册 `AidenGoAgent`
- 适配了 MobileGym 的 suite 系统

## 目标架构

MobileGym **仅作为模拟器**，提供设备环境：
- 保留 `bridge/` - HTTP 桥接层，转换 Aiden 工具调用为 MobileGym 环境操作
- 保留 `adapter/daemon.py` - Aiden daemon 管理
- **移除对 MobileGym 测试框架的依赖**：
  - 不使用 `SerialRunner`
  - 不使用 `factory.load_tasks()`
  - 不注册 agent
- **复用现有 benchmark/runner**：
  - 使用 `benchmark/runner/main.py` 的测试流程
  - 使用 `AgentClient` 与 Aiden daemon 通信
  - 使用 `benchmark/suites/*.json` 定义测试任务

## 新架构分层

```
┌─────────────────────────────────────────┐
│  benchmark/runner/main.py               │
│  - 测试编排                              │
│  - 使用 AgentClient                      │
│  - 加载 suites/*.json                    │
└──────────────┬──────────────────────────┘
               │
               ├─ /api/chat (Aiden daemon)
               │
               v
┌─────────────────────────────────────────┐
│  Aiden Go Daemon                         │
│  - 配置 MobileGym skill                  │
│  - 通过 bridge 调用设备工具               │
└──────────────┬──────────────────────────┘
               │
               ├─ /api/tools/*, /api/screen (environment bridge HTTP)
               │
               v
┌─────────────────────────────────────────┐
│  benchmark/mobilegym/bridge/server.py   │
│  - HTTP endpoint 转 MobileGym env 操作   │
│  - 不涉及测试框架                         │
└──────────────┬──────────────────────────┘
               │
               ├─ env.step(action)
               │
               v
┌─────────────────────────────────────────┐
│  MobileGym Simulator (bench_env)        │
│  - 纯模拟器，提供 env 接口                │
│  - 不使用 runner/factory/agent 注册      │
└─────────────────────────────────────────┘
```

## 实现步骤

### 1. 创建新的启动脚本：`benchmark/mobilegym/scripts/start_simulator.py`
- 启动 MobileGym 模拟器环境
- 启动 Bridge HTTP server
- **不使用** MobileGym 的测试框架

### 2. 简化依赖
- 移除 `adapter/register.py`（不再注册 agent）
- 移除 `adapter/aiden_go_agent.py`（不再实现 MobileGym Agent 接口）
- 保留 `adapter/daemon.py`（daemon 生命周期管理）

### 3. 集成到现有 benchmark runner
- 使用 `--environment-url` 连接 MobileGym environment bridge
- 使用 `--auto-agent-setup` 按 `/api/concurrent` 自动启动隔离 agent daemon
- 使用标准的 `AgentClient` + `benchmark/suites/*.json` 流程

### 4. Docker 编排调整
- `docker-compose.yml` 分离服务：
  - `mobilegym-simulator`: 纯模拟器 + bridge
  - `aiden-daemon`: Aiden Go daemon
  - `runner`: benchmark/runner/main.py

## 保留的组件

- ✅ `benchmark/mobilegym/bridge/` - 完整保留
- ✅ `benchmark/mobilegym/adapter/daemon.py` - 保留
- ✅ `benchmark/mobilegym/config/` - 保留（agent.toml, skills）
- ✅ `benchmark/mobilegym/docker/` - 调整，不再使用 MobileGym runner

## 移除/重构的组件

- ❌ `benchmark/mobilegym/scripts/run_aiden.py` - 移除或重写为纯模拟器启动器
- ❌ `benchmark/mobilegym/adapter/register.py` - 移除
- ❌ `benchmark/mobilegym/adapter/aiden_go_agent.py` - 移除
- ❌ `benchmark/mobilegym/adapter/artifacts.py` - 移除（或移到 bridge）
- 🔄 `benchmark/mobilegym/report.py` - 简化或移除（使用 benchmark/runner 的报告）

## 使用方式对比

### 之前（使用 MobileGym 框架）
```bash
# 依赖 MobileGym 的 SerialRunner
docker compose run --rm test --suite clock --limit 5
```

### 之后（纯模拟器）
```bash
# 1. 启动模拟器（后台）
docker compose up -d mobilegym-simulator

# 2. 使用 WebUI 创建 MobileGym environment 并运行 benchmark
cd benchmark
uv run python -m runner webui
```

## 优势

1. **解耦**：MobileGym 纯粹是设备模拟器，不影响测试逻辑
2. **统一**：所有 benchmark（HDMI、MobileGym）都用相同的 runner
3. **灵活**：可以轻松切换不同模拟器或真实设备
4. **简单**：不需要适配 MobileGym 的 Agent 接口
