# MobileGym 重构迁移指南

## 变更概览

MobileGym 从**测试框架模式**重构为**纯模拟器模式**。

### 核心变化

| 方面 | 旧版本（框架模式） | 新版本（模拟器模式） |
|------|------------------|---------------------|
| **定位** | 使用 MobileGym 的测试框架 | MobileGym 仅作为设备模拟器 |
| **测试编排** | `bench_env.runner.SerialRunner` | `benchmark/runner/main.py` |
| **测试定义** | MobileGym suites + Aiden JSON | 统一的 `benchmark/suites/*.json` |
| **Agent 实现** | 注册 `AidenGoAgent` 到 MobileGym | Aiden daemon 通过 HTTP 调用设备 |
| **启动方式** | `scripts/run_aiden.py` | `scripts/start_simulator.py` + `python -m runner` |

## 文件变更清单

### ✅ 保留的组件

- `benchmark/mobilegym/bridge/` - HTTP 桥接层（完整保留）
- `benchmark/mobilegym/adapter/daemon.py` - Daemon 管理（保留）
- `benchmark/mobilegym/adapter/artifacts.py` - 工件导出（保留）
- `benchmark/mobilegym/config/` - Agent 配置和 skills（保留）
- `benchmark/mobilegym/docker/` - Docker 编排（重写）

### 🔄 重写的组件

| 旧文件 | 新文件 | 说明 |
|--------|--------|------|
| `scripts/run_aiden.py` | `scripts/start_simulator.py` | 启动纯模拟器，不运行测试 |
| - | `scripts/configure_daemon.py` | 配置 daemon 连接 bridge |
| `docker-compose.yml` | `docker-compose.new.yml` | 分离模拟器和测试编排 |
| `docker/init.sh` | `docker/init.new.sh` | 简化初始化流程 |

### ❌ 废弃的组件

| 文件 | 原用途 | 替代方案 |
|------|--------|---------|
| `adapter/register.py` | 注册 agent 到 MobileGym | 不再需要 agent 注册 |
| `adapter/aiden_go_agent.py` | MobileGym Agent 接口实现 | 使用 `AgentClient` + bridge |
| `suites/*.yaml` | 自定义 MobileGym suite | 使用 `benchmark/suites/*.json` |
| `report.py` | MobileGym 报告生成 | 使用 `benchmark/runner` 报告 |

## 迁移步骤

### 1. 备份旧配置

```bash
cd benchmark/mobilegym
cp docker/docker-compose.yml docker/docker-compose.old.yml
cp docker/init.sh docker/init.old.sh
```

### 2. 使用新文件

```bash
# 替换 Docker 编排
mv docker/docker-compose.new.yml docker/docker-compose.yml
mv docker/init.new.sh docker/init.sh

# 重新初始化
cd docker
./init.sh

# 检查配置
cat ../config/agent.toml
```

### 3. 转换测试定义

如果你有自定义的 MobileGym suite，需要转换为 `benchmark/suites/*.json` 格式。

**旧格式**（MobileGym YAML）：
```yaml
tasks:
  - id: clock.CountAlarms
    goal: "Count the alarms in the Clock app"
    apps: ["clock"]
```

**新格式**（Aiden JSON）：
```json
{
  "name": "mobilegym_clock",
  "tasks": [
    {
      "id": "count_alarms",
      "prompt": "打开时钟应用，告诉我有多少个闹钟。",
      "description_for_judge": "Agent 应该打开时钟应用并正确报告闹钟数量",
      "rubric": [...],
      "hard_assertions": {...}
    }
  ]
}
```

### 4. 更新启动脚本

**旧方式**：
```bash
python benchmark/mobilegym/scripts/run_aiden.py \
  --suite clock \
  --aiden-daemon-url http://localhost:8080
```

**新方式**：
```bash
# 启动模拟器（后台）
python benchmark/mobilegym/scripts/start_simulator.py \
  --bridge-port 8888 \
  --token-dir /tmp/mobilegym &

# 运行测试。推荐让 runner 自动启动隔离 agent daemon。
cd benchmark
uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --environment-url http://localhost:8888 \
  --auto-agent-setup
```

### 5. Docker 使用方式

**旧方式**：
```bash
docker compose run --rm test --suite clock --limit 5
```

**新方式**：
```bash
# 启动服务
docker compose up -d

# 推荐使用 WebUI 创建 MobileGym environment 并运行 job
cd benchmark
uv run python -m runner webui
```

## 架构对比

### 旧架构（框架模式）

```text
run_aiden.py
  ↓
MobileGym SerialRunner
  ↓
AidenGoAgent (注册到 MobileGym)
  ↓ /api/chat
Aiden Daemon
  ↓ Bridge HTTP
MobileGym env
```

### 新架构（模拟器模式）

```text
runner.main
  ↓ /api/chat
Aiden Daemon
  ↓ /api/tools/* through environment bridge
MobileGym Bridge + Simulator
```

## 常见问题

### Q1: 旧的测试结果还能用吗？

A: 旧的结果保留在 `benchmark/runs/mobilegym/` 下，但格式可能不同。新版使用 `benchmark/runner` 的统一格式。

### Q2: 如何运行旧的 MobileGym suite？

A: 需要将 MobileGym suite 转换为 `benchmark/suites/*.json` 格式。参考 `mobilegym_basic.json` 示例。

### Q3: 并发测试怎么办？

A: 新架构下，可以：
1. 使用 benchmark WebUI 创建 `Envs > 1` 的 MobileGym environment
2. 由 WebUI 为并发 task worker 启动独立 daemon
3. 通过每个请求携带的 `benchmark-task-id` 路由到对应 env

### Q4: Bridge server 的 API 有变化吗？

A: Bridge API 保持不变。只是启动方式从 `run_aiden.py` 内部启动改为独立的 `start_simulator.py`。

### Q5: 我可以继续使用旧版本吗？

A: 可以，旧文件重命名为 `.old.py` 保留。但建议迁移到新架构以获得更好的一致性和可维护性。

## 优势总结

迁移到新架构的优势：

1. **统一性**：所有 benchmark（HDMI、MobileGym）使用相同的 runner 和 suite 格式
2. **解耦**：MobileGym 纯粹是设备模拟器，测试逻辑在 `benchmark/runner`
3. **灵活性**：可以轻松替换为其他模拟器或真实设备
4. **简化**：不需要实现 MobileGym Agent 接口，直接使用 `AgentClient`
5. **可维护**：代码职责清晰，模拟器和测试框架分离

## 获取帮助

- 查看新的 README: `benchmark/mobilegym/README.md`
- 查看重构计划: `benchmark/mobilegym/REFACTOR_PLAN.md`
- 查看示例 suite: `benchmark/suites/mobilegym_basic.json`
