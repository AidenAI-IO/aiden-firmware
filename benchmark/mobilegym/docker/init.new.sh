#!/bin/bash
# Simplified init script for MobileGym simulator-only setup

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_DIR="$SCRIPT_DIR/../config"

echo "🚀 Initializing MobileGym Simulator Setup"
echo "=========================================="

# 1. Generate control token if not exists
if [ ! -f "$CONFIG_DIR/control_token" ]; then
    echo "📝 Generating Aiden control token..."
    python3 -c "import secrets; print(secrets.token_urlsafe(32))" > "$CONFIG_DIR/control_token"
    echo "✓ Control token generated: $CONFIG_DIR/control_token"
else
    echo "✓ Control token already exists: $CONFIG_DIR/control_token"
fi

# 2. Create agent.toml from template if not exists
if [ ! -f "$CONFIG_DIR/agent.toml" ]; then
    if [ -f "$CONFIG_DIR/agent.toml.template" ]; then
        echo "📝 Creating agent.toml from template..."
        cp "$CONFIG_DIR/agent.toml.template" "$CONFIG_DIR/agent.toml"
        echo "✓ Created: $CONFIG_DIR/agent.toml"
        echo ""
        echo "⚠️  IMPORTANT: Edit $CONFIG_DIR/agent.toml and fill in your API keys!"
        echo ""
    else
        echo "❌ Error: Template not found at $CONFIG_DIR/agent.toml.template"
        exit 1
    fi
else
    echo "✓ agent.toml already exists: $CONFIG_DIR/agent.toml"
fi

# 3. Check if MobileGym submodule is initialized
MOBILEGYM_VENDOR="$SCRIPT_DIR/../vendor/mobilegym"
if [ ! -d "$MOBILEGYM_VENDOR/bench_env" ]; then
    echo "📦 Initializing MobileGym submodule..."
    git submodule update --init --recursive "$MOBILEGYM_VENDOR"
    echo "✓ MobileGym submodule initialized"
else
    echo "✓ MobileGym submodule already initialized"
fi

# 4. Create runs directory
RUNS_DIR="$SCRIPT_DIR/../../runs/mobilegym"
mkdir -p "$RUNS_DIR"
echo "✓ Runs directory: $RUNS_DIR"

echo ""
echo "=========================================="
echo "✅ Initialization complete!"
echo ""
echo "Next steps:"
echo "  1. Edit agent.toml: vim $CONFIG_DIR/agent.toml"
echo "  2. Build images: docker compose -f docker-compose.new.yml build"
echo "  3. Start services: docker compose -f docker-compose.new.yml up -d"
echo "  4. Run benchmark: docker compose -f docker-compose.new.yml run --rm runner \\"
echo "       python -m benchmark.runner run \\"
echo "       --suite /benchmark/suites/mobilegym_basic.json \\"
echo "       --agent-url http://aiden-daemon:8080"
echo ""
