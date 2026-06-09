# Docker 构建代理配置

Docker 构建需要从 Docker Hub 拉取基础镜像。如果你的网络环境需要代理，有两种方案：

## 方案 1：配置 Docker daemon 代理（推荐）

### macOS (Docker Desktop)

1. 打开 Docker Desktop → Settings → Resources → Proxies
2. 勾选 "Manual proxy configuration"
3. 填入代理地址：
   - Web Server (HTTP): `http://127.0.0.1:7897`
   - Secure Web Server (HTTPS): `http://127.0.0.1:7897`
4. 点击 "Apply & Restart"

### Linux

编辑 `/etc/systemd/system/docker.service.d/http-proxy.conf`：

```ini
[Service]
Environment="HTTP_PROXY=http://127.0.0.1:7897"
Environment="HTTPS_PROXY=http://127.0.0.1:7897"
Environment="NO_PROXY=localhost,127.0.0.1"
```

重启 Docker：

```bash
sudo systemctl daemon-reload
sudo systemctl restart docker
```

## 方案 2：使用国内镜像源（无需代理）

编辑 Dockerfile，将基础镜像改为国内源：

```dockerfile
# 原始
FROM node:18-bullseye AS mobilegym-base

# 改为阿里云镜像
FROM registry.cn-hangzhou.aliyuncs.com/google_containers/node:18-bullseye AS mobilegym-base
```

或者配置 Docker daemon 的镜像加速器（`/etc/docker/daemon.json`）：

```json
{
  "registry-mirrors": [
    "https://registry.docker-cn.com",
    "https://docker.mirrors.ustc.edu.cn"
  ]
}
```

## 验证

配置完成后重新构建：

```bash
docker compose build mobilegym
```
