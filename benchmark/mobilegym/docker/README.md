# MobileGym Docker 一键测试

在纯净环境一键启动 MobileGym + Aiden Agent 测试。

> ℹ️ MobileGym 官方支持不使用 Docker 的本地并发。本目录提供的是 Aiden 集成的隔离运行方案：当前 Aiden Go daemon 只有一个 active MobileGym session，且 conversation/memory 是 daemon 全局状态；并发 benchmark 需要给每个 worker 独立 daemon、配置、网络和 volume，避免互相污染。

## 前置条件

- Docker >= 20.10，Docker Compose **v2.24+**（并发模式用到 `!reset` 语法，`docker compose version` 查看）
- macOS / Linux（脚本依赖 bash 4+；macOS 自带 bash 3.x，建议 `brew install bash`）
- 能访问 LLM API；如需走代理见下方"配置代理"

## 一、纯净环境快速启动

```bash
cd benchmark/mobilegym/docker

# 1. 一键初始化：生成 control_token、从模板创建 agent.toml、生成 .env
./init.sh

# 2. 编辑 LLM 配置（必须填入真实 api_key）
vim ../config/agent.toml

# 3. （可选）配置代理 — 详见下面"配置代理"
# vim .env

# 4. 构建镜像（首次约 5-10 分钟）
docker compose build
# 国内网络：docker compose -f docker-compose.cn.yml build

# 5. 启动常驻服务（mobilegym + daemon）
docker compose up -d

# 6. 跑一个内置任务验证
docker compose run --rm test \
  --task-id clock.CountAlarms \
  --aiden-control-token "$(cat ../config/control_token)"

# 7. 完成后停止
docker compose down
```

> 💡 完整 LLM 配置示例：
>
> ```toml
> [model]
> provider = "openrouter"
> model = "anthropic/claude-haiku-4-5"        # 或 openai/gpt-4o-mini
> base_url = "https://openrouter.ai/api/v1"
> api_key = "sk-or-v1-..."
> temperature = 0.2
> max_tokens = 1024
> ```
>
> ⚠️ 不要用 reasoning model（如 `bytedance-seed/seed-2.0-lite`），单次推理可能 20s+ 触发 chat 超时。

## 二、配置代理

如果容器内需要代理访问 LLM API，编辑 `init.sh` 生成的 `.env`：

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

代理设好之后 `docker compose down && docker compose up -d` 重启。

更详细：[SETUP_PROXY.md](SETUP_PROXY.md)

## 三、运行测试

### 单次测试

```bash
# 单个任务
docker compose run --rm test \
  --task-id clock.ToggleAlarm \
  --aiden-control-token "$(cat ../config/control_token)"

# 测试套件（使用 MobileGym 内置 suite，例如 clock）
docker compose run --rm test \
  --suite clock --limit 3 \
  --aiden-control-token "$(cat ../config/control_token)"
```

> ℹ️ `--suite` 接受的是 MobileGym 内置 suite 名（`clock`, `phone_control_v1`, `calendar`, `wechat`, ...）。`benchmark/mobilegym/suites/*.yaml` 是仓库内的自定义 YAML，**当前版本** `run_aiden.py` 还没解析它们，请直接用 `--task-id` 列举或选用内置 suite。

### 并发测试

使用 benchmark WebUI 运行 Aiden MobileGym 并发：

```bash
python -m runner.main webui
```

在 WebUI 中创建 MobileGym environment 时把 `Envs` 设为大于 1。WebUI 会启动一个
MobileGym simulator/bridge container，内部创建多个 env；运行 job 时，每个并发
task worker 使用独立 daemon，并在 reset/tool-proxy 请求中附带相同的
`benchmark-task-id`，由 bridge 路由到对应 env。

## 四、查看结果

```bash
ls ../../runs/mobilegym/
open ../../runs/mobilegym/<run-id>/index.html            # direct run 报告
open ../../runs/webui/<job-id>/raw/<run-id>/report.html  # WebUI task/suite report
```

普通 `docker compose run ... test` 会在 run 目录生成 `index.html`、`summary.json` 和原始 MobileGym 输出。WebUI runs 保存在 `benchmark/runs/webui/`。

## 五、停止

```bash
docker compose down
```

## 📁 文件说明

| 文件                          | 说明                         | 是否在 git               |
| ----------------------------- | ---------------------------- | ------------------------ |
| `Dockerfile`                  | 镜像构建                     | ✅                       |
| `docker-compose.yml`          | 服务编排                     | ✅                       |
| `daemon-entrypoint.sh`        | Daemon 启动脚本              | ✅                       |
| `init.sh`                     | 一键初始化                   | ✅                       |
| `.env.example`                | 配置模板                     | ✅                       |
| `.dockerignore`               | 构建排除                     | ✅                       |
| `.env`                        | 实际配置（含代理）           | ❌ ignored               |
| `../config/agent.toml`        | LLM 配置                     | ❌ ignored（含 API key） |
| `../config/control_token`     | 认证 token                   | ❌ ignored               |
| `../report.py`                | 批次/套件 HTML 报告          | ✅                       |

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

| 方式             | 适用                                                                 |
| ---------------- | -------------------------------------------------------------------- |
| Docker（推荐）   | 完整集成验证、多人复现、并发 benchmark、需要隔离 daemon 状态的场景   |
| 本地串行（可用） | 调试 adapter/bridge/daemon；需自备 MobileGym checkout、Playwright 依赖和 Aiden daemon，不适合共享 daemon 并发 |

## ✅ 验证清单

- [ ] `./init.sh` 执行成功
- [ ] `agent.toml` 中填入了真实 API key
- [ ] （如需）`.env` 配置了代理
- [ ] `docker compose build` 成功
- [ ] `docker compose ps` 显示 mobilegym 和 daemon healthy
- [ ] `docker compose run --rm test ...` 能跑测试
- [ ] `../../runs/mobilegym/` 看到结果文件
