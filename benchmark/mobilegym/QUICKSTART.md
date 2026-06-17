# MobileGym 重构 - 快速验证指南

## ✅ 重构已完成

MobileGym 已从**测试框架模式**重构为**纯模拟器模式**。

## 🎯 验证新架构

### 方式 1：验证脚本语法（已完成 ✓）

```bash
# 验证帮助信息
python benchmark/mobilegym/scripts/start_simulator.py --help
python benchmark/mobilegym/scripts/configure_daemon.py --help

# 验证 JSON 格式
python -m json.tool benchmark/suites/mobilegym_basic.json > /dev/null
```

### 方式 2：本地运行测试（需要 MobileGym submodule）

```bash
# 1. 初始化 MobileGym submodule（如果还没有）
git submodule update --init --recursive benchmark/mobilegym/vendor/mobilegym

# 2. 安装 MobileGym 依赖
pip install -r benchmark/mobilegym/vendor/mobilegym/bench_env/requirements.txt
playwright install chromium

# 3. 启动 MobileGym web 模拟器（需要另外的终端）
# 参考 MobileGym 文档启动 web simulator

# 4. 启动我们的 Bridge server
python benchmark/mobilegym/scripts/start_simulator.py \
  --env-url http://localhost:4173 \
  --bridge-port 8888 \
  --token-dir /tmp/mobilegym-tokens \
  --bridge-url-file /tmp/mobilegym-bridge-url

# 5. 健康检查
curl http://localhost:8888/health

# 6. 启动 Aiden daemon（假设已配置）
# ./aiden-go --config benchmark/mobilegym/config/agent.toml --port 8080

# 7. 配置 daemon 连接 bridge
python benchmark/mobilegym/scripts/configure_daemon.py \
  --daemon-url http://localhost:8080 \
  --bridge-url $(cat /tmp/mobilegym-bridge-url) \
  --bridge-token-file /tmp/mobilegym-tokens/bridge_device_token

# 8. 运行测试
python -m benchmark.runner run \
  --suite benchmark/suites/mobilegym_basic.json \
  --agent-url http://localhost:8080
```

### 方式 3：Docker 运行（推荐）

```bash
cd benchmark/mobilegym/docker

# 1. 使用新的 compose 文件
cp docker-compose.new.yml docker-compose.yml
cp init.new.sh init.sh

# 2. 初始化配置
./init.sh

# 3. 编辑 agent.toml，填入真实的 API key
vim ../config/agent.toml

# 4. 构建镜像
docker compose build

# 5. 启动所有服务
docker compose up -d

# 6. 检查服务状态
docker compose ps

# 7. 查看日志
docker compose logs mobilegym-simulator
docker compose logs aiden-daemon

# 8. 运行测试
docker compose run --rm runner \
  python -m benchmark.runner run \
  --suite /benchmark/suites/mobilegym_basic.json \
  --agent-url http://aiden-daemon:8080

# 9. 查看结果
ls ../../runs/
```

## 📋 提交的变更

```
Commit: 64773f5
Message: refactor(benchmark): convert mobilegym from test framework to pure simulator

Files changed: 17
  +1720 lines added
  -88 lines deleted

Key changes:
  ✅ 新增 start_simulator.py - 启动纯模拟器
  ✅ 新增 configure_daemon.py - 配置 daemon
  ✅ 新增 Docker 镜像和 compose 配置
  ✅ 新增详细的重构文档
  ✅ 新增示例测试套件
  ✅ 重写 README.md
  ✅ 标记废弃旧的 Agent 实现
```

## 🔍 下一步建议

### 立即验证

1. **检查脚本运行**：尝试运行 `start_simulator.py` 看是否能正常启动
2. **测试 Docker 构建**：`docker compose -f docker-compose.new.yml build`
3. **验证 suite 加载**：确认 `benchmark.runner` 能正确加载 `mobilegym_basic.json`

### 迁移现有测试

如果你有现有的 MobileGym 测试任务：

1. 查看 `MIGRATION.md` 了解如何转换
2. 参考 `mobilegym_basic.json` 的格式
3. 将 MobileGym suite 转换为 Aiden suite JSON

### 清理（可选）

确认新架构稳定后：

```bash
# 删除旧文件
rm benchmark/mobilegym/adapter/*.old.py
rm benchmark/mobilegym/adapter/*.deprecated.py
rm benchmark/mobilegym/scripts/*.old.py
rm benchmark/mobilegym/scripts/*.deprecated.py
rm benchmark/mobilegym/README.old.md
```

## 📚 参考文档

新架构的完整说明：

- **README.md** - 使用指南和架构说明
- **REFACTOR_PLAN.md** - 重构设计和目标
- **MIGRATION.md** - 从旧版迁移的详细步骤
- **REFACTOR_SUMMARY.md** - 完成的工作总结

## 🎉 重构完成

MobileGym 现在是一个纯净的设备模拟器，不再绑定到测试框架。
所有 benchmark 现在使用统一的 `benchmark/runner` 和 `suites/*.json` 格式。

---

如有任何问题，请参考文档或查看 git log。
