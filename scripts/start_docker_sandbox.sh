#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
WAIT_TIMEOUT="${AIDEN_SANDBOX_WAIT_TIMEOUT:-180}"
CONFIG_WEB_PORT="${AIDEN_CONFIG_WEB_PORT:-8000}"
AGENT_WEB_PORT="${AIDEN_AGENT_WEB_PORT:-8080}"
BUILD_IMAGE=false

if [[ $# -gt 1 ]] || [[ $# -eq 1 && "$1" != "--build" ]]; then
    echo "Usage: $0 [--build]" >&2
    exit 2
fi
if [[ ${1:-} == "--build" ]]; then
    BUILD_IMAGE=true
fi

cd "$ROOT_DIR"

if ! command -v docker >/dev/null 2>&1; then
    echo "Docker is required to start the sandbox." >&2
    exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
    echo "Docker Compose is required to start the sandbox." >&2
    exit 1
fi

compose_args=(
    --detach
    --wait
    --wait-timeout "$WAIT_TIMEOUT"
)
if [[ "$BUILD_IMAGE" == true ]]; then
    echo "Rebuilding and updating the Aiden Docker sandbox..."
    compose_args+=(--build)
else
    echo "Starting the Aiden Docker sandbox..."
fi

if ! docker compose up "${compose_args[@]}" aiden; then
    echo "The sandbox did not become healthy. Recent logs:" >&2
    docker compose logs --tail=100 aiden >&2 || true
    exit 1
fi

echo "Aiden Docker sandbox is ready."
echo "Config Web: http://localhost:${CONFIG_WEB_PORT}"
echo "Agent Web:  http://localhost:${AGENT_WEB_PORT}"
echo "Logs:       docker compose logs -f aiden"
