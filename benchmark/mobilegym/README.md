# Aiden MobileGym Integration

MobileGym 集成到 Aiden Agent，支持批量测试手机控制能力。

> ℹ️ MobileGym 官方本身支持本地 `--parallel` 并发，并不依赖 Docker。本集成推荐 Docker，是因为当前 Aiden Go daemon 只有一个 active MobileGym session，且 conversation/memory 是 daemon 全局状态；并发 benchmark 需要用独立 daemon/config/network/volume 隔离这些状态。

## 🚀 快速开始（Aiden 隔离推荐：Docker）

```bash
cd benchmark/mobilegym/docker

# 1. 一键初始化：生成 token、从模板创建 agent.toml、生成 .env
./init.sh

# 2. 编辑 LLM 配置（必须填入真实 api_key）
vim ../config/agent.toml

# 3. 构建（国内用户用 docker-compose.cn.yml）
docker compose build

# 4. 启动常驻服务
docker compose up -d

# 5. 跑一个内置任务验证
docker compose run --rm test \
  --task-id clock.CountAlarms \
  --aiden-control-token "$(cat ../config/control_token)"

# 6. 查看结果
ls ../../runs/mobilegym/

# 7. 停止
docker compose down
```

详见 [docker/README.md](docker/README.md)

## 📁 目录结构

```text
benchmark/mobilegym/
├── README.md                       # 本文件
├── adapter/                        # Python 适配器（AidenGoAgent）
├── bridge/                         # Bridge server（HTTP ↔ MobileGym）
├── config/                         # Agent 配置
│   ├── agent.toml.template         # 配置模板
│   └── skills/device-operator/     # MobileGym 专用 skill
├── docker/                         # Aiden 隔离运行方案 ⭐
│   ├── README.md                   # 主入口
│   ├── Dockerfile                  # 标准版
│   ├── Dockerfile.cn               # 国内加速版
│   ├── docker-compose.yml          # 标准编排
│   ├── docker-compose.cn.yml       # 国内编排
│   ├── docker-compose.parallel.yml # 并发模式 overlay（重置端口）
│   ├── init.sh                     # 纯净环境一键初始化
│   └── parallel_run.sh             # 并发隔离 orchestrator
├── report.py                       # 批次/套件 HTML 报告生成器
├── scripts/                        # 测试脚本
│   └── run_aiden.py                # 主入口（容器/本地串行执行）
├── suites/                         # 自定义测试套件 YAML（当前未被加载）
│   └── aiden_smoke.yaml            # 示例套件
└── vendor/mobilegym/               # 上游 MobileGym（submodule）
```

## 📚 文档

- **一键启动**: [docker/README.md](docker/README.md) ⭐
- **测试流程**: [docker/TEST_FLOW.md](docker/TEST_FLOW.md)
- **代理配置**: [docker/SETUP_PROXY.md](docker/SETUP_PROXY.md)
- **架构设计**: 见项目根目录文档

## 🎯 测试套件

### 运行 Aiden JSON suite

`run_aiden.py` 现在支持 `--aiden-suite` 直接加载 `benchmark/suites/<name>.json`：

```bash
docker compose run --rm test --aiden-suite memory_v1 --limit 5
```

并发执行：

```bash
PARALLEL=4 ./parallel_run.sh --aiden-suite memory_v1
```

### 运行内置 suite

```bash
docker compose run --rm test --suite phone_control_v1 --limit 3
docker compose run --rm test --suite clock --limit 5
```

> ℹ️ `--suite` 接受的是 MobileGym 内置 suite 名（`clock`, `phone_control_v1`, `calendar`, `wechat`, ...）。`benchmark/mobilegym/suites/*.yaml` 是仓库内的自定义 YAML，**当前版本** `run_aiden.py` 还没解析它们，请用 `--task-id` 一个个列举或选用内置 suite。

## 🚀 并发测试

使用 `parallel_run.sh` 并发运行 Aiden benchmark。每个 worker 都是一套独立的 Docker Compose project，包含独立的 MobileGym 模拟器、Aiden daemon、配置/token、network、volume 和日志；只有结果根目录共享。这样做是为了隔离 Aiden daemon 状态，不是因为 MobileGym 官方并发必须依赖 Docker。

```bash
cd benchmark/mobilegym/docker

# 并发跑多个任务（每个任务独立容器）
./parallel_run.sh clock.CountAlarms clock.ToggleAlarm phone_control_v1.MakeCall

# 用多个隔离 worker 跑同一个内置 suite（按 shard 分配任务）
PARALLEL=4 ./parallel_run.sh --suite phone_control_v1

# 并发跑多个内置 suite，每个 suite 单独汇总结果
PARALLEL=2 MAX_JOBS=2 ./parallel_run.sh --suites clock,phone_control_v1

# 使用国内 compose 文件；parallel no-ports override 会自动追加
COMPOSE_FILES=docker-compose.cn.yml PARALLEL=2 ./parallel_run.sh --suite clock
```

**特点：**

- ✅ 完全隔离：每个 worker 独立的模拟器、daemon、配置和 Docker project
- ✅ 无状态干扰：修改闹钟、联系人、设置等互不影响
- ✅ 自动分片：suite 任务按 shard 分配到可用 worker
- ✅ 单独看结果：每个 suite 有独立 `index.html` 和 `summary.json`
- ⚠️ 资源开销：每个 worker 启动独立的 simulator + daemon 容器
- ⚠️ 需要 Docker Compose **v2.24+**（`!reset` 语法）

**注意：** 对本集成来说，`run_aiden.py --parallel N` 会在同一 Aiden daemon 内共享 session、history 和 memory，可能导致测试间状态干扰；需要并发时优先用 `parallel_run.sh`。

结果位于：

```bash
ls ../../runs/mobilegym/
open ../../runs/mobilegym/<run-id>/index.html            # direct run 报告
open ../../runs/mobilegym/<batch-id>/index.html          # parallel batch 总览
open ../../runs/mobilegym/<batch-id>/<suite>/index.html  # parallel 单个 suite
```

## ✅ 验证

```bash
# 单任务测试
docker compose run --rm test --task-id clock.CountAlarms

# 内置 suite 限量测试
docker compose run --rm test --suite clock --limit 1
```
