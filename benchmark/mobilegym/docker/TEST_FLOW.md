# MobileGym Docker 测试流程

## 前置条件

✅ Docker Desktop 已配置代理
✅ 镜像构建完成

## 完整测试流程

### 1. 启动服务

```bash
cd benchmark/mobilegym/docker
docker compose up -d
```

查看状态：

```bash
docker compose ps
```

应该看到 2 个常驻服务：

- `aiden-mobilegym` - 模拟器 (4173端口)
- `aiden-daemon` - Go Agent (8080端口)

`aiden-test` 是测试运行器，通过 `docker compose run --rm test ...` 按需启动。

### 2. 运行单个任务测试

```bash
docker compose run --rm test \
  --mobilegym-root /mobilegym \
  --task-id clock.CountAlarms \
  --env-url http://mobilegym:4173 \
  --aiden-daemon-url http://daemon:8080 \
  --aiden-control-token "$(cat ../config/control_token)" \
  --parallel 1 --headless
```

### 3. 运行内置 suite

```bash
# 闹钟测试（限制 3 个任务跑得快）
docker compose run --rm test \
  --mobilegym-root /mobilegym \
  --suite clock --limit 3 \
  --env-url http://mobilegym:4173 \
  --aiden-daemon-url http://daemon:8080 \
  --aiden-control-token "$(cat ../config/control_token)" \
  --parallel 1 --headless
```

> ℹ️ `--suite` 接受的是 MobileGym 内置 suite 名（`clock`, `phone_control_v1`, `calendar`, `wechat`, ...）。仓库 `benchmark/mobilegym/suites/*.yaml` 里的自定义 YAML suite **当前版本未被加载**，请用 `--task-id` 一个个列举，或者选内置 suite。

### 4. 跑多个内置 suite

```bash
# 电话控制测试
docker compose run --rm test --suite phone_control_v1 --limit 3

# 闹钟测试
docker compose run --rm test --suite clock --limit 5
```

### 5. 查看结果

```bash
# 结果保存在宿主机
ls -lh ../../runs/mobilegym/

# 查看最新 batch 报告
LATEST=$(ls -t ../../runs/mobilegym/ | head -1)
open "../../runs/mobilegym/$LATEST/index.html"

# 查看单个 suite 报告
open "../../runs/mobilegym/$LATEST/phone_control_v1/index.html"
```

并发运行使用 benchmark WebUI：

```bash
python -m runner.main webui
```

在 WebUI 中启动 MobileGym environment 时设置 `Envs`，然后选择该 environment 和
suite 运行。WebUI 会为并发 task worker 启动独立 daemon，并通过
`benchmark-task-id` 路由到不同 env。

### 6. 查看日志

```bash
# 实时日志
docker compose logs -f daemon

# 模拟器日志
docker compose logs mobilegym

# 所有日志
docker compose logs
```

### 7. 停止服务

```bash
docker compose down
```

## 验证清单

- [ ] 镜像构建成功（`docker images | grep feature-mobilegym`）
- [ ] 服务启动成功（`docker compose ps` 显示 `mobilegym` 和 `daemon` healthy）
- [ ] 单任务测试运行
- [ ] 自定义套件运行
- [ ] 结果正确保存到 `../../runs/mobilegym/`
- [ ] Judge 评判正常（success/passed 字段）

## 故障排查

### 服务未启动

```bash
docker compose logs daemon
docker compose restart daemon
```

### 测试失败

```bash
# 检查 daemon 配置
cat ../config/agent.toml

# 检查 API key
grep "api_key" ../config/agent.toml

# 手动测试 daemon
curl http://localhost:8080
```

### 代理问题

如果容器内需要访问 LLM API 的代理：

```bash
# 编辑 .env
cat > .env <<EOF
HTTPS_PROXY=http://host.docker.internal:7897
HTTP_PROXY=http://host.docker.internal:7897
EOF

# 重启
docker compose down
docker compose up -d
```
