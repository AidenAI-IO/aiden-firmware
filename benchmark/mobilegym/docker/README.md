# MobileGym Docker 一键测试

一键启动 MobileGym 环境并运行测试，无需本地安装依赖。

## 🚀 快速开始

### 1. 准备配置（首次）

```bash
# 从项目根目录
cd benchmark/mobilegym/docker

# 复制配置模板
cp ../config/agent.toml.template ../config/agent.toml

# 编辑填入 LLM API key
vim ../config/agent.toml
```

### 2. 构建并启动

```bash
# 国内用户（推荐）
docker compose -f docker-compose.cn.yml build
docker compose -f docker-compose.cn.yml up -d

# 国际用户
docker compose build
docker compose up -d
```

### 3. 运行测试

```bash
# 运行单个任务
docker compose run --rm test --task-id clock.CountAlarms

# 运行内置套件
docker compose run --rm test --suite phone_control_v1

# 运行自定义套件
docker compose run --rm test --suite aiden_smoke

# 限制任务数量
docker compose run --rm test --suite phone_control_v1 --limit 5
```

### 4. 查看结果

```bash
# 结果保存在宿主机
ls ../../runs/mobilegym/

# 实时日志
docker compose logs -f daemon
```

### 5. 停止

```bash
docker compose down
```

## 📁 自定义测试套件

在 `../suites/` 创建 YAML 文件：

```yaml
# ../suites/my_tests.yaml
name: my_tests
description: 我的测试
tasks:
  - clock.CountAlarms
  - phone_control_v1.MakeCall
```

运行：`docker compose run --rm test --suite my_tests`

查看示例：`cat ../suites/aiden_smoke.yaml`

## 🔧 高级用法

```bash
# 自定义参数
docker compose run --rm test \
  --suite phone_control_v1 \
  --limit 3 \
  --max-steps 20 \
  --quiet

# 调试
docker compose exec daemon sh
docker compose logs daemon

# 代理（创建 .env 文件）
echo "HTTPS_PROXY=http://host.docker.internal:7897" > .env
docker compose restart daemon
```

## 📊 常用命令

| 命令                                          | 说明     |
| --------------------------------------------- | -------- |
| `docker compose up -d`                        | 启动服务 |
| `docker compose ps`                           | 查看状态 |
| `docker compose run --rm test --suite <name>` | 运行套件 |
| `docker compose logs -f`                      | 查看日志 |
| `docker compose down`                         | 停止服务 |

## 📚 更多文档

- [DOCKER_GUIDE.md](DOCKER_GUIDE.md) - 详细指南
- [DOCKER_PROXY.md](DOCKER_PROXY.md) - 代理配置
- [../suites/README.md](../suites/README.md) - 自定义套件

## 🐛 故障排查

- **构建失败**：国内用户使用 `docker-compose.cn.yml`
- **测试失败**：检查 `docker compose logs daemon`
- **端口冲突**：修改 docker-compose.yml 端口映射
