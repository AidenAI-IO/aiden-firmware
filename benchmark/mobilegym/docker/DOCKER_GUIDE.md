# MobileGym Benchmark - Docker Setup

> 这是详细参考。日常使用建议先看 [README.md](README.md) 的"纯净环境快速启动"。

快速启动 MobileGym 测试环境，无需安装 Node.js/Python/Playwright 等依赖。

## 🚀 快速开始

### 1. 前置要求

- Docker >= 20.10
- Docker Compose >= **v2.24**（并发模式用到 `!reset` 语法）
- 已准备好 `benchmark/mobilegym/config/agent.toml`

```bash
cd benchmark/mobilegym/docker
./init.sh                       # 一键生成 token、agent.toml、.env
vim ../config/agent.toml        # 填入真实 LLM API key
```

### 2. 构建镜像

```bash
docker compose build
```

### 3. 启动服务

```bash
# 启动模拟器 + daemon（后台运行）
docker compose up -d

# 查看日志
docker compose logs -f

# 检查健康状态
docker compose ps
```

### 4. 运行测试

```bash
# 单个任务
docker compose run --rm test \
  --mobilegym-root /mobilegym \
  --task-id clock.ToggleAlarm \
  --env-url http://mobilegym:4173 \
  --aiden-daemon-url http://daemon:8080 \
  --aiden-control-token "$(cat benchmark/mobilegym/config/control_token)" \
  --parallel 1 --headless

# 测试套件
docker compose run --rm test \
  --mobilegym-root /mobilegym \
  --suite phone_control_v1 \
  --limit 5 \
  --env-url http://mobilegym:4173 \
  --aiden-daemon-url http://daemon:8080 \
  --aiden-control-token "$(cat benchmark/mobilegym/config/control_token)" \
  --parallel 1 --headless
```

### 5. 查看结果

```bash
# 结果保存在宿主机
ls -la benchmark/runs/mobilegym/
```

### 6. 停止服务

```bash
docker compose down
```

## 🔧 配置代理

如果需要代理访问 LLM API：

```bash
# 创建 .env 文件
cat > .env <<EOF
HTTPS_PROXY=http://host.docker.internal:7897
HTTP_PROXY=http://host.docker.internal:7897
ALL_PROXY=socks5://host.docker.internal:7897
EOF

# 重启服务
docker compose down
docker compose up -d
```

**注意**: Docker 容器内用 `host.docker.internal` 访问宿主机的代理。

## 📁 目录结构

```text
├── Dockerfile                              # 多阶段构建（simulator/daemon/test）
├── docker-compose.yml                      # 服务编排
├── .env.example                            # 环境变量示例
├── benchmark/
│   ├── mobilegym/
│   │   ├── config/
│   │   │   ├── agent.toml                  # LLM 配置（你需要创建）
│   │   │   └── control_token               # daemon 认证 token (init.sh 生成)
│   │   ├── adapter/                        # Python adapter
│   │   ├── bridge/                         # Bridge server
│   │   └── vendor/mobilegym/               # MobileGym 上游代码
│   └── runs/                               # 测试结果（持久化到宿主机）
```

## 🛠️ 开发调试

### 查看单个服务日志

```bash
docker compose logs -f mobilegym   # 模拟器
docker compose logs -f daemon      # Go Agent
```

### 进入容器调试

```bash
docker compose exec daemon sh
docker compose exec mobilegym bash
```

### 重新构建某个服务

```bash
docker compose build daemon
docker compose up -d daemon
```

## 🆚 单容器 vs 并发隔离

MobileGym 官方支持本地 `--parallel` 并发；下表只针对 Aiden 集成。当前 Aiden Go daemon 的 MobileGym session、conversation 和 memory 是 daemon 级状态，所以同一个 daemon 内并发会互相污染。

| 方式                                        | 优点                                    | 缺点                          |
| ------------------------------------------- | --------------------------------------- | ----------------------------- |
| 单容器 (`docker compose run --rm test ...`) | 启动快、调试友好                        | 任务间共享 daemon，状态会污染 |
| 并发隔离 (`./parallel_run.sh`)              | 每 worker 独立 daemon/simulator/network | 资源占用大                    |

建议：

- 调试单个任务：用 `docker compose run --rm test ...`
- 跑套件 / 多任务：用 `./parallel_run.sh`，详见 [README.md](README.md) "并发测试"

## 🔍 故障排查

### 模拟器启动失败

```bash
docker compose logs mobilegym
# 常见问题: Node.js 依赖安装失败、端口占用
```

### Daemon 连不上模拟器

```bash
# 检查网络
docker compose exec daemon ping mobilegym

# 检查模拟器健康
curl http://localhost:4173
```

### 测试运行失败

```bash
# 查看 control_token 是否正确
cat benchmark/mobilegym/config/control_token

# 手动测试 daemon API
docker compose exec daemon curl http://localhost:8080
```

## 📚 更多信息

- [MobileGym 上游仓库](https://github.com/Purewhiter/mobilegym)
- [README.md](README.md) - 主入口（包含 init.sh、并发测试、代理配置）
