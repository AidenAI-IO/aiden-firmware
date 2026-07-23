#!/usr/bin/env bash
#
# Wrapper to launch VPhone iOS benchmark services without sourcing vphone.env
# in every terminal. This script sources vphone.env itself, then starts the
# requested service with the values from that file.
#
# Usage:
#   ./start.sh bridge [extra start_bridge args...]
#   ./start.sh agent  [extra start-agent-daemon args...]
#   ./start.sh run    [--task-id X] [--no-judge] [runner run args...]
#   ./start.sh webui  [extra webui args...]
#   ./start.sh env                # print the loaded config (no secrets)
#
# `run` auto-discovers the running agent daemon's port and control token, so you
# never paste --agent-url / --benchmark-token-file. Start a daemon first with
# `./start.sh agent`.
#
# The env file is picked up in this order:
#   1. $VPHONE_ENV_FILE if already exported
#   2. ./vphone.env next to this script (default)
#   3. --env-file <path> as the FIRST argument after the subcommand
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_ENV_FILE="$SCRIPT_DIR/vphone.env"

usage() {
  sed -n '7,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-2}"
}

[ $# -ge 1 ] || usage 2
SUBCMD="$1"; shift

# Allow an explicit "--env-file <path>" override as the first extra arg.
ENV_FILE="${VPHONE_ENV_FILE:-$DEFAULT_ENV_FILE}"
if [ "${1:-}" = "--env-file" ] && [ $# -ge 2 ]; then
  ENV_FILE="$2"; shift 2
fi

if [ ! -r "$ENV_FILE" ]; then
  echo "error: env file not readable: $ENV_FILE" >&2
  exit 2
fi

# Load the config so callers never have to `set -a; source vphone.env`.
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a
export VPHONE_ENV_FILE="$ENV_FILE"

# Make homebrew tools (uv, docker, tmux) resolvable under a bare ssh shell too.
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"

cd "${VPHONE_BENCHMARK_ROOT:-$SCRIPT_DIR/..}"

# Refuse to start if the target TCP port is already taken, so we fail with a
# clear message instead of a Python "Address already in use" traceback.
require_free_port() {
  local port="$1" label="$2"
  local pid
  pid="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null || true)"
  if [ -n "$pid" ]; then
    echo "error: $label port $port is already in use by PID $pid:" >&2
    ps -o pid,lstart,command -p "$pid" 2>/dev/null | tail -n +1 >&2 || true
    echo "hint: it may already be running. Stop it first, or use a different port." >&2
    exit 1
  fi
}

case "$SUBCMD" in
  bridge)
    require_free_port "${VPHONE_BRIDGE_PORT:-8899}" "bridge"
    echo "starting VPhone bridge (guest_ssh_host=${VPHONE_GUEST_SSH_HOST:-<unset>}, env=$ENV_FILE)"
    exec uv run python -m vphone.scripts.start_bridge --env-file "$ENV_FILE" "$@"
    ;;
  agent)
    : "${VPHONE_BRIDGE_ENDPOINT:?VPHONE_BRIDGE_ENDPOINT missing from env}"
    : "${VPHONE_BENCHMARK_TASK_ID:?VPHONE_BENCHMARK_TASK_ID missing from env}"
    : "${VPHONE_AGENT_CONFIG:?VPHONE_AGENT_CONFIG missing from env}"
    echo "starting agent daemon (bridge=$VPHONE_BRIDGE_ENDPOINT, task_id=$VPHONE_BENCHMARK_TASK_ID)"
    exec uv run python -m runner start-agent-daemon \
      --name vphone-ios \
      --environment-bridge-endpoint "$VPHONE_BRIDGE_ENDPOINT" \
      --benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID" \
      --agent-config "$VPHONE_AGENT_CONFIG" \
      "$@"
    ;;
  run)
    # Discover the currently running agent daemon's host port and token, so you
    # never have to paste --agent-url / --benchmark-token-file by hand. The port
    # is auto-assigned per start-agent-daemon run and changes each time.
    ports="$(docker ps --filter name=aiden-benchmark-agent --format '{{.Ports}}' | head -1)"
    host_port="$(printf '%s' "$ports" | sed -nE 's/.*127\.0\.0\.1:([0-9]+)->8080.*/\1/p')"
    if [ -z "$host_port" ]; then
      echo "error: no running agent daemon found (container name aiden-benchmark-agent*)." >&2
      echo "hint: start one first with: ./start.sh agent" >&2
      exit 1
    fi
    agent_url="http://127.0.0.1:$host_port"
    token_file="${VPHONE_BENCHMARK_ROOT}/runs/cli-services/agent-vphone-ios/config/control_token"
    if [ ! -r "$token_file" ]; then
      echo "error: control token not found: $token_file" >&2
      echo "hint: (re)start the daemon with: ./start.sh agent" >&2
      exit 1
    fi
    # Default suite unless the caller passes their own --suite in "$@".
    suite_args=()
    case " $* " in
      *" --suite "*) : ;;
      *) suite_args=(--suite suites/vphone_ios_basic.json) ;;
    esac
    echo "running against daemon $agent_url (task_id=$VPHONE_BENCHMARK_TASK_ID)"
    exec uv run python -m runner run \
      "${suite_args[@]}" \
      --agent-url "$agent_url" \
      --benchmark-token-file "$token_file" \
      --environment-url "$VPHONE_BRIDGE_ENDPOINT" \
      --benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID" \
      "$@"
    ;;
  webui)
    require_free_port "8765" "webui"
    echo "starting benchmark WebUI on :8765 (agent-config=$VPHONE_AGENT_CONFIG)"
    exec uv run python -m runner webui \
      --host 127.0.0.1 \
      --port 8765 \
      --agent-config "$VPHONE_AGENT_CONFIG" \
      "$@"
    ;;
  env)
    echo "env_file=$ENV_FILE"
    echo "VPHONE_BENCHMARK_ROOT=${VPHONE_BENCHMARK_ROOT:-}"
    echo "VPHONE_SOCKET=${VPHONE_SOCKET:-}"
    echo "VPHONE_BRIDGE_ENDPOINT=${VPHONE_BRIDGE_ENDPOINT:-}"
    echo "VPHONE_BENCHMARK_TASK_ID=${VPHONE_BENCHMARK_TASK_ID:-}"
    echo "VPHONE_GUEST_SSH_HOST=${VPHONE_GUEST_SSH_HOST:-}"
    echo "VPHONE_GUEST_SSH_PORT=${VPHONE_GUEST_SSH_PORT:-}"
    echo "VPHONE_AGENT_CONFIG=${VPHONE_AGENT_CONFIG:-}  (contents not printed)"
    ;;
  -h|--help|help)
    usage 0
    ;;
  *)
    echo "error: unknown subcommand: $SUBCMD" >&2
    usage 2
    ;;
esac
