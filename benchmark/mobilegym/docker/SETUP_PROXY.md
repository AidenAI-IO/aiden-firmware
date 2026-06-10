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
docker compose run --rm test --suite aiden_smoke
```

## 📝 当前状态

- ✅ Dockerfile 已添加代理 ARG 支持
- ✅ .env 文件已创建
- ⚠️ 需要配置 Docker Desktop 才能拉取基础镜像

## 🔄 切换到 Docker 后的清理

配置好 Docker 代理并测试成功后，停止本地服务：

```bash
# 停止本地 daemon
pkill -f "bin/daemon"

# 停止本地模拟器
pkill -f "npm run preview"

# 清理运行时文件
rm -rf .mobilegym_run/
```

之后统一使用 Docker：

```bash
cd benchmark/mobilegym/docker
docker compose up -d    # 启动
docker compose down     # 停止
```
