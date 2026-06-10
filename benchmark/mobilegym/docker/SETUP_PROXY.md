# Docker 代理配置指南

Docker 构建需要从 Docker Hub 拉取镜像。你的网络需要代理访问。

## 🔧 配置 Docker Desktop 代理（推荐）

### macOS / Windows

1. 打开 Docker Desktop
2. 点击右上角齿轮图标 → **Settings**
3. 左侧选择 **Resources** → **Proxies**
4. 选择 **Manual proxy configuration**
5. 填入代理地址：
   ```text
   Web Server (HTTP): http://127.0.0.1:7897
   Secure Web Server (HTTPS): http://127.0.0.1:7897
   Bypass: localhost,127.0.0.1
   ```
6. 点击 **Apply & Restart**

### 验证

```bash
# 重启后测试
docker pull hello-world
```

## ✅ 配置完成后

```bash
cd benchmark/mobilegym/docker

# 构建镜像
docker compose build

# 启动服务
docker compose up -d

# 运行测试
docker compose run --rm test \
  --task-id clock.CountAlarms \
  --aiden-control-token "$(cat ../config/control_token)"
```

## 📝 当前状态

- ✅ Dockerfile 已添加代理 ARG 支持
- ✅ .env 文件已创建
- ⚠️ 需要配置 Docker Desktop 才能拉取基础镜像

## 🔄 容器内 LLM 代理

容器自身访问 LLM API 的代理由 `.env` 控制（`./init.sh` 会生成默认模板）。详见 [README.md](README.md) 的"配置代理"。
