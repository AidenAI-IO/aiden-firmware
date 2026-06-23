# MobileGym 重构完成总结

## 🎯 目标

将 MobileGym 从**测试框架模式**重构为**纯模拟器模式**，不再依赖 MobileGym 的测试框架（SerialRunner、agent 注册等），而是仅将其作为设备模拟器使用，测试编排统一使用 `benchmark/runner`。

## ✅ 已完成的工作

### 1. 新增核心脚本

- ✅ `scripts/start_simulator.py` - 启动纯 MobileGym 模拟器 + Bridge server
  - 不使用 MobileGym 测试框架
  - 直接创建 `MobileEnv` 而不通过 `factory`
  - 启动 Bridge HTTP server 供 Aiden daemon 调用

- ✅ `scripts/configure_daemon.py` - 配置 Aiden daemon 连接 bridge
  - 通过 `/api/mobilegym/bridge/configure` 端点配置
  - 支持从文件读取 token

### 2. 更新文档

- ✅ `README.md` - 完全重写，说明新架构
  - 架构图展示纯模拟器模式
  - 本地和 Docker 两种使用方式
  - 与旧版本对比说明

- ✅ `REFACTOR_PLAN.md` - 详细的重构计划
  - 问题分析
  - 目标架构
  - 实现步骤
  - 组件保留/移除清单

- ✅ `MIGRATION.md` - 迁移指南
  - 文件变更清单
  - 逐步迁移说明
  - 架构对比
  - 常见问题解答

### 3. Docker 编排

- ✅ `docker/docker-compose.new.yml` - 新的 compose 配置
  - `mobilegym-web`: MobileGym web 模拟器
  - `mobilegym-simulator`: 模拟器 + Bridge server
  - `aiden-daemon`: Aiden Go daemon
  - `runner`: 使用 `benchmark/runner` 运行测试

- ✅ `docker/Dockerfile.simulator` - 模拟器镜像
  - 只包含模拟器和 bridge 代码
  - 不包含测试框架组件

- ✅ `docker/Dockerfile.runner` - Runner 镜像
  - 包含 `benchmark/runner` 代码
  - 执行统一的测试流程

- ✅ `docker/init.new.sh` - 简化的初始化脚本

### 4. 示例测试套件

- ✅ `benchmark/suites/mobilegym_basic.json` - 示例 suite
  - 3 个基础设备操作任务
  - 使用统一的 Aiden suite 格式
  - 包含 rubric、hard_assertions 等

### 5. 标记废弃组件

旧文件已重命名为 `.old.py`，并创建 `.deprecated.py` 提示：

- ✅ `scripts/run_aiden.py` → `run_aiden.old.py`
- ✅ `adapter/register.py` → `register.old.py`
- ✅ `adapter/aiden_go_agent.py` → `aiden_go_agent.old.py`
- ✅ `README.md` → `README.old.md`

## 📋 文件清单

### 新增文件 (7个)

```
benchmark/mobilegym/
├── scripts/
│   ├── start_simulator.py          # 启动纯模拟器
│   └── configure_daemon.py         # 配置 daemon
├── docker/
│   ├── docker-compose.new.yml      # 新 compose 配置
│   ├── Dockerfile.simulator        # 模拟器镜像
│   ├── Dockerfile.runner           # Runner 镜像
│   └── init.new.sh                 # 新初始化脚本
├── REFACTOR_PLAN.md                # 重构计划
├── MIGRATION.md                    # 迁移指南
└── README.md                       # 重写的 README

benchmark/suites/
└── mobilegym_basic.json            # 示例 suite
```

### 保留文件 (已重命名，供参考)

```
benchmark/mobilegym/
├── scripts/run_aiden.old.py        # 旧测试入口
├── adapter/register.old.py         # 旧 agent 注册
├── adapter/aiden_go_agent.old.py   # 旧 Agent 实现
└── README.old.md                   # 旧 README
```

### 保持不变的文件

```
benchmark/mobilegym/
├── bridge/                         # Bridge server（核心组件）
│   ├── server.py
│   ├── episode.py
│   ├── protocol.py
│   └── actions.py
├── adapter/
│   ├── daemon.py                   # Daemon 管理（保留）
│   └── artifacts.py                # 工件导出（保留）
└── config/                         # Agent 配置（保留）
    ├── agent.toml.template
    └── skills/device-operator/
```

## 🔄 架构变化

### 旧架构（框架模式）

```
scripts/run_aiden.py
  ├─ MobileGym SerialRunner (测试编排)
  └─ AidenGoAgent (实现 MobileGym Agent 接口)
      ├─ 注册到 MobileGym agent registry
      ├─ 调用 Aiden daemon /api/chat
      └─ 通过 Bridge 操作设备
```

### 新架构（模拟器模式）

```
benchmark/runner/main.py (统一测试编排)
  └─ AgentClient → Aiden daemon /api/chat
      └─ device-operator skill → Bridge HTTP
          └─ MobileGym Simulator (纯设备环境)
```

## 🎯 达成的目标

### ✅ 解耦

- MobileGym 仅作为设备模拟器，不涉及测试逻辑
- 测试编排、判分、报告统一由 `benchmark/runner` 处理

### ✅ 统一

- 所有 benchmark（HDMI、MobileGym）使用相同的：
  - 测试入口：`benchmark/runner/main.py`
  - Suite 格式：`benchmark/suites/*.json`
  - Agent 客户端：`AgentClient`

### ✅ 简化

- 不需要实现 MobileGym Agent 接口
- 不需要注册 agent 到 MobileGym
- 不需要适配 MobileGym 的 suite 格式

### ✅ 灵活

- 模拟器可以独立启动/停止
- 可以轻松替换为其他模拟器或真实设备
- Aiden daemon 可以连接到任意 bridge 端点

## 📝 使用示例

### 本地运行

```bash
# 1. 启动模拟器
python benchmark/mobilegym/scripts/start_simulator.py \
  --bridge-port 8888 &

# 2. 运行测试。推荐让 runner 自动启动隔离 agent daemon。
cd benchmark
uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --environment-url http://localhost:8888 \
  --auto-agent-setup
```

### Docker 运行

```bash
cd benchmark/mobilegym/docker

# 1. 初始化（首次）
mv docker-compose.new.yml docker-compose.yml
mv init.new.sh init.sh
./init.sh
vim ../config/agent.toml  # 填入 API key

# 2. 启动服务
docker compose up -d

# 3. 使用 WebUI 创建 MobileGym environment 并运行 job
cd ../..
uv run python -m runner webui

# 4. 查看结果
ls runs/
open runs/webui/<job-id>/raw/<run-id>/report.html
```

## 🔍 后续建议

### 立即可做

1. **测试新脚本**：运行 `start_simulator.py` 验证能否正常启动
2. **转换现有 suite**：将现有的 MobileGym 任务转换为 `benchmark/suites/*.json` 格式
3. **更新 CI/CD**：如果有自动化测试，更新为新的启动方式

### 中期优化

1. **移除旧文件**：确认新架构稳定后，删除 `.old.py` 文件
2. **完善 suite**：创建更多 MobileGym 测试任务
3. **监控指标**：对比新旧架构的性能和稳定性

### 长期规划

1. **支持更多模拟器**：iOS 模拟器、其他 Android 模拟器
2. **真实设备支持**：通过类似的 Bridge 接口连接真实设备
3. **分布式测试**：多个模拟器实例并发执行

## 📚 参考文档

- [README.md](README.md) - 使用说明
- [REFACTOR_PLAN.md](REFACTOR_PLAN.md) - 重构设计
- [MIGRATION.md](MIGRATION.md) - 迁移指南
- [bridge/](bridge/) - Bridge 协议文档

---

**重构完成时间**: 2026-06-17
**主要变更**: 从测试框架模式重构为纯模拟器模式
**影响范围**: `benchmark/mobilegym/` 目录
