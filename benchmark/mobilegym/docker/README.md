# MobileGym Docker 一键测试

新人在纯净环境也能跑通。

## 🚀 快速开始（5 步）

### 1. 进入 docker 目录

```bash
cd benchmark/mobilegym/docker
```

### 2. 一键初始化

```bash
./init.sh
```

这会自动：
- ✅ 生成 `control_token` 和 `bridge_token`
- ✅ 从模板创建 `agent.toml`（自动填充容器内路径）
- ✅ 创建 `.env` 默认配置

### 3. 填入 LLM API Key

```bash
vim ../config/agent.toml
```

修改 `[model]` 部分：
```toml
[model]
provider = "openrouter"  # 或 openai/anthropic
model = "anthropic/claude-3.5-sonnet"
base_url = "https://openrouter.ai/api/v1"
api_key = "sk-or-v1-..."  # 你的 API key
```

### 4. （可选）配置代理

如果容器内需要代理访问 LLM API：

```bash
vim .env
```

取消注释：
```bash
HTTPS_PROXY=http://host.docker.internal:7897
HTTP_PROXY=http://host.docker.internal:7897
NO_PROXY=localhost,127.0.0.1,mobilegym,daemon
```

> 💡 容器内访问宿主机用 `host.docker.internal`，不是 `127.0.0.1`

### 5. 构建并启动

```bash
# 构建镜像（首次约 5-10 分钟）
docker compose build

# 启动服务
docker compose up -d

# 验证（应该看到 healthy）
docker compose ps
```

### 6. 运行测试

```bash
# 单个任务
docker compose run --rm test \
  --task-id clock.ToggleAlarm \
  --aiden-control-token "$(cat ../config/control_token)"

# 自定义套件
docker compose run --rm test \
  --suite aiden_smoke \
  --aiden-control-token "$(cat ../config/control_token)"
```

### 7. 查看结果

```bash
ls ../../runs/mobilegym/
docker compose logs -f daemon  # 实时日志
```

### 8. 停止

```bash
docker compose down
```

## 📁 文件说明

| 文件 | 说明 | 是否在 git |
|------|------|----------|
| `Dockerfile` | 镜像构建 | ✅ |
| `docker-compose.yml` | 服务编排 | ✅ |
| `daemon-entrypoint.sh` | Daemon 启动脚本 | ✅ |
| `init.sh` | 一键初始化 | ✅ |
| `.env.example` | 配置模板 | ✅ |
| `.dockerignore` | 构建排除 | ✅ |
| `.env` | 实际配置（含代理）| ❌ ignored |
| `../config/agent.toml` | LLM 配置 | ❌ ignored（含 API key）|
| `../config/control_token` | 认证 token | ❌ ignored |
| `../config/bridge_token` | 认证 token | ❌ ignored |

## 🔧 端口冲突？

修改 `.env`:
```bash
MOBILEGYM_PORT=14173
AIDEN_DAEMON_PORT=18080
```

## 🐛 故障排查

### 构建失败（拉取镜像超时）

需要配置 Docker Desktop 代理：
- Docker Desktop → Settings → Resources → Proxies
- 填入：`http://127.0.0.1:7897`

详见 [SETUP_PROXY.md](SETUP_PROXY.md)

### 测试失败（LLM 调用超时）

容器内代理配置：编辑 `.env` 用 `host.docker.internal`

```bash
HTTPS_PROXY=http://host.docker.internal:7897
```

### 平台警告（Mac M 系列）

`linux/amd64` vs `arm64/v8` 警告可以忽略，不影响功能。
（Rosetta 2 自动转译）

## 📚 更多文档

- [SETUP_PROXY.md](SETUP_PROXY.md) - Docker 代理详细配置
- [TEST_FLOW.md](TEST_FLOW.md) - 测试流程
- [DOCKER_GUIDE.md](DOCKER_GUIDE.md) - 高级用法
- [../suites/README.md](../suites/README.md) - 自定义测试套件

## 🆚 与本地开发对比

| 方式 | 命令 | 适用 |
|------|------|------|
| Docker（推荐） | `docker compose up -d` | 所有人 |
| 本地（已废弃） | - | 已统一用 Docker |

## ✅ 验证清单（新人参考）

- [ ] `./init.sh` 执行成功
- [ ] `agent.toml` 中填入了真实 API key
- [ ] （如需）`.env` 配置了代理
- [ ] `docker compose build` 成功
- [ ] `docker compose ps` 显示 mobilegym 和 daemon healthy
- [ ] `docker compose run --rm test ...` 能跑测试
- [ ] `../../runs/mobilegym/` 看到结果文件
