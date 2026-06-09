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

应该看到 3 个服务：

- `aiden-mobilegym` - 模拟器 (4173端口)
- `aiden-daemon` - Go Agent (8080端口)
- `aiden-test` - 测试运行器（按需启动）

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

### 3. 运行自定义套件

```bash
# 运行示例套件（3个任务）
docker compose run --rm test \
  --mobilegym-root /mobilegym \
  --suite aiden_smoke \
  --env-url http://mobilegym:4173 \
  --aiden-daemon-url http://daemon:8080 \
  --aiden-control-token "$(cat ../config/control_token)" \
  --parallel 1 --headless
```

### 4. 运行内置套件

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

# 查看最新结果
LATEST=$(ls -t ../../runs/mobilegym/ | head -1)
cat "../../runs/mobilegym/$LATEST/results.jsonl" | python3 -m json.tool
```

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
- [ ] 服务启动成功（`docker compose ps` 显示 3/3 healthy）
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

## 下一步

测试成功后：

1. 停止本地服务（如果还在运行）

   ```bash
   pkill -f "bin/daemon"
   pkill -f "npm run preview"
   ```

2. 清理本地文件

   ```bash
   rm -rf .mobilegym_run/ bin/
   ```

3. 提交代码
   ```bash
   git add .
   git commit -m "feat: add MobileGym Docker integration"
   ```
