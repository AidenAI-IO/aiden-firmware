#!/bin/bash
# MobileGym 一键初始化脚本
# 在纯净环境快速搭建
#
# 用法: ./init.sh

set -e
umask 077  # token 文件只允许当前用户读写

cd "$(dirname "${BASH_SOURCE[0]}")"

CONFIG_DIR="../config"

# 生成强随机 token（优先 openssl，备选 /dev/urandom）
gen_token() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 32
    else
        head -c 32 /dev/urandom | xxd -p -c 32
    fi
}

echo "=== MobileGym Docker 环境初始化 ==="
echo ""

# 1. 生成 token 文件
echo "1. 生成认证 token..."
if [ ! -f "$CONFIG_DIR/control_token" ]; then
    gen_token > "$CONFIG_DIR/control_token"
    chmod 600 "$CONFIG_DIR/control_token"
    echo "   ✓ 创建 control_token"
else
    echo "   - control_token 已存在"
fi

if [ ! -f "$CONFIG_DIR/bridge_token" ]; then
    gen_token > "$CONFIG_DIR/bridge_token"
    chmod 600 "$CONFIG_DIR/bridge_token"
    echo "   ✓ 创建 bridge_token"
else
    echo "   - bridge_token 已存在"
fi

# 2. 检查 agent.toml
echo ""
echo "2. 检查 agent.toml..."
if [ ! -f "$CONFIG_DIR/agent.toml" ]; then
    cp "$CONFIG_DIR/agent.toml.template" "$CONFIG_DIR/agent.toml"
    # 用 Docker 容器内的路径填充
    sed -i.bak 's|{{BRIDGE_URL}}|http://mobilegym:4173|g' "$CONFIG_DIR/agent.toml"
    sed -i.bak 's|{{BRIDGE_TOKEN_FILE}}|/config/bridge_token|g' "$CONFIG_DIR/agent.toml"
    sed -i.bak 's|{{CONTROL_TOKEN_FILE}}|/config/control_token|g' "$CONFIG_DIR/agent.toml"
    rm -f "$CONFIG_DIR/agent.toml.bak"
    echo "   ✓ 从模板创建 agent.toml"
    echo ""
    echo "   ⚠️  请编辑填入 [model] 部分的真实 API key:"
    echo "      vim $CONFIG_DIR/agent.toml"
else
    echo "   - agent.toml 已存在"
fi

# 3. 创建 .env 文件
echo ""
echo "3. 检查 .env 配置..."
if [ ! -f .env ]; then
    cat > .env <<'EOF'
# 容器内访问 LLM API 的代理（如果需要）
# Docker 容器内访问宿主机用 host.docker.internal
# HTTPS_PROXY=http://host.docker.internal:7897
# HTTP_PROXY=http://host.docker.internal:7897
# NO_PROXY=localhost,127.0.0.1,mobilegym,daemon

# 端口配置（如果默认端口冲突，改成其他）
# MOBILEGYM_PORT=14173
# AIDEN_DAEMON_PORT=18080
EOF
    echo "   ✓ 创建 .env (默认无代理)"
    echo ""
    echo "   💡 如果需要代理访问 LLM API，编辑 .env:"
    echo "      vim .env"
else
    echo "   - .env 已存在"
fi

echo ""
echo "✅ 初始化完成！"
echo ""
echo "下一步:"
echo "  1. 编辑 $CONFIG_DIR/agent.toml 填入 LLM API key"
echo "  2. (可选) 编辑 .env 配置代理或端口"
echo "  3. 构建镜像: docker compose build"
echo "  4. 启动服务: docker compose up -d"
echo "  5. 运行测试: docker compose run --rm test --task-id clock.CountAlarms ..."
echo ""
echo "详见 README.md"
