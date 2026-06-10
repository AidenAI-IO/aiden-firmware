# Aiden MobileGym Integration

MobileGym 集成到 Aiden Agent，支持批量测试手机控制能力。

## 🚀 快速开始（Docker 一键）

```bash
# 1. 准备配置
cd benchmark/mobilegym/docker
cp ../config/agent.toml.template ../config/agent.toml
vim ../config/agent.toml  # 填入 LLM API key

# 2. 启动（国内用户推荐）
docker compose -f docker-compose.cn.yml build
docker compose -f docker-compose.cn.yml up -d

# 3. 运行测试
docker compose run --rm test --suite phone_control_v1

# 4. 查看结果
ls ../../runs/mobilegym/

# 5. 停止
docker compose down
```

详见 [docker/README.md](docker/README.md)

## 📁 目录结构

```text
benchmark/mobilegym/
├── README.md                    # 本文件
├── adapter/                     # Python 适配器（AidenGoAgent）
├── bridge/                      # Bridge server（HTTP ↔ MobileGym）
├── config/                      # Agent 配置
│   ├── agent.toml.template      # 配置模板
│   └── skills/device-operator/  # MobileGym 专用 skill
├── docker/                      # Docker 一键部署 ⭐
│   ├── README.md                # 快速指南
│   ├── Dockerfile               # 标准版
│   ├── Dockerfile.cn            # 国内加速版
│   └── docker-compose*.yml      # 编排文件
├── scripts/                     # 测试脚本
│   └── run_aiden.py             # 主入口
├── suites/                      # 自定义测试套件
│   ├── README.md                # 套件说明
│   └── aiden_smoke.yaml         # 示例套件
└── vendor/mobilegym/            # 上游 MobileGym（submodule）
```

## 📚 文档

- **一键启动**: [docker/README.md](docker/README.md) ⭐
- **自定义套件**: [suites/README.md](suites/README.md)
- **架构设计**: 见项目根目录文档

## 🎯 测试套件

### 运行内置套件

```bash
docker compose run --rm test --suite phone_control_v1
docker compose run --rm test --suite clock --limit 5
```

### 创建自定义套件

```bash
# 在 suites/ 创建 YAML
cat > suites/my_tests.yaml <<'EOF'
name: my_tests
description: 我的测试
tasks:
  - clock.CountAlarms
  - phone_control_v1.MakeCall
EOF

# 运行
docker compose run --rm test --suite my_tests
```

## 🚀 并发测试

使用 `parallel_run.sh` 在独立容器中并发运行测试，每个测试有完全隔离的环境。

```bash
cd docker

# 并发跑多个任务（每个任务独立容器）
./parallel_run.sh clock.CountAlarms clock.ToggleAlarm phone_control_v1.MakeCall

# 用多个容器跑同一个套件（任务自动分配）
PARALLEL=4 ./parallel_run.sh --suite phone_control_v1
```

**特点：**

- ✅ 完全隔离：每个容器独立的模拟器和 daemon
- ✅ 无状态干扰：修改闹钟、联系人、设置等互不影响
- ✅ 自动分配：MobileGym 自动分配任务到可用容器
- ⚠️ 资源开销：每个容器启动独立的浏览器实例

**注意：** `--parallel N` 参数在单容器内共享 daemon，可能导致测试间状态干扰，不推荐使用。

## ✅ 验证

```bash
# 单任务测试
docker compose run --rm test --task-id clock.CountAlarms

# 小套件测试
docker compose run --rm test --suite aiden_smoke
```
