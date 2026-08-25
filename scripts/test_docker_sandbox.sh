#!/bin/sh
set -eu

sandbox_project="aiden-sandbox-smoke-$$"
config_port="${AIDEN_SANDBOX_TEST_CONFIG_PORT:-0}"
agent_port="${AIDEN_SANDBOX_TEST_AGENT_PORT:-0}"
bridge_port="${AIDEN_SANDBOX_TEST_BRIDGE_PORT:-}"
sandbox_bridge_endpoint=""
sandbox_device_type=""
bridge_pid=""
bridge_log=""
script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

compose() {
    AIDEN_CONFIG_WEB_PORT="$config_port" \
    AIDEN_AGENT_WEB_PORT="$agent_port" \
    AIDEN_DEVICE_TYPE="$sandbox_device_type" \
    AIDEN_ENVIRONMENT_BRIDGE_ENDPOINT="$sandbox_bridge_endpoint" \
    AIDEN_BENCHMARK_TASK_ID=docker-sandbox-smoke \
    AIDEN_BRIDGE_EPISODE_ID=docker-sandbox-smoke \
        docker compose -p "$sandbox_project" "$@"
}

cleanup() {
    trap - INT TERM EXIT
    compose down -v --remove-orphans >/dev/null 2>&1 || true
    if [ -n "$bridge_pid" ]; then
        kill "$bridge_pid" 2>/dev/null || true
        wait "$bridge_pid" 2>/dev/null || true
    fi
    if [ -n "$bridge_log" ] && [ -f "$bridge_log" ]; then
        rm -f "$bridge_log"
    fi
}

agent_pid() {
    compose exec -T aiden /usr/local/bin/aiden-agent-service status 2>/dev/null \
        | sed -n 's/^agent=running pid=\([0-9][0-9]*\).*/\1/p'
}

wait_for_agent() {
    attempt=1
    while [ "$attempt" -le 45 ]; do
        if curl -fsS --max-time 5 "http://127.0.0.1:$config_port/" >/dev/null 2>&1 \
            && curl -fsS --max-time 5 "http://127.0.0.1:$agent_port/api/tools" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        attempt="$((attempt + 1))"
    done
    compose logs
    return 1
}

trap cleanup INT TERM EXIT

selected_ports="$(python3 "$script_dir/select_docker_web_ports.py" "$config_port" "$agent_port")"
config_port="${selected_ports%% *}"
agent_port="${selected_ports#* }"

compose up -d --build
wait_for_agent

compose exec -T aiden sh -ec '
python3 --version
python3 -m pip --version
node --version
wetty --version
rg --version
fq --version
yq --version
'

config_page="$(curl -fsS --max-time 10 "http://127.0.0.1:$config_port/")"
agent_page="$(curl -fsS --max-time 10 "http://127.0.0.1:$agent_port/")"
terminal_page="$(curl -fsSL --max-time 10 "http://127.0.0.1:$agent_port/wetty/")"
terminal_headers="$(curl -fsSIL --max-time 10 "http://127.0.0.1:$agent_port/wetty/")"
terminal_socket="$(curl -fsS --max-time 10 "http://127.0.0.1:$agent_port/wetty/socket.io/?EIO=4&transport=polling")"
reported_agent_port="$(
    curl -fsS --max-time 10 "http://127.0.0.1:$config_port/api/config" \
        | python3 -c 'import json, sys; print(json.load(sys.stdin)["agent_status"]["public_port"])'
)"
test "$reported_agent_port" -eq "$agent_port"
case "$config_page" in
    *'<!DOCTYPE html>'*) ;;
    *) printf 'Config Web did not return HTML.\n' >&2; exit 1 ;;
esac
case "$agent_page" in
    *'<!DOCTYPE html>'*) ;;
    *) printf 'Agent Web did not return HTML.\n' >&2; exit 1 ;;
esac
case "$terminal_page" in
    *'<!doctype html>'*|*'<!DOCTYPE html>'*) ;;
    *) printf 'WeTTY did not return HTML through Agent Web.\n' >&2; exit 1 ;;
esac
case "$terminal_headers" in
    *"connect-src 'self' ws://127.0.0.1:$agent_port"*) ;;
    *) printf 'WeTTY did not advertise the Agent Web address for WebSocket connections.\n' >&2; exit 1 ;;
esac
case "$terminal_socket" in
    '0{'*'"upgrades":["websocket"]'*) ;;
    *) printf 'WeTTY Socket.IO handshake failed through Agent Web.\n' >&2; exit 1 ;;
esac

container_id="$(compose ps -q aiden)"
restart_count="$(docker inspect --format '{{.RestartCount}}' "$container_id")"
wetty_pid="$(compose exec -T aiden sh -ec '
for process in /proc/[0-9]*; do
    executable="$(readlink "$process/exe" 2>/dev/null || true)"
    if [ "$executable" = /usr/local/bin/node ]; then
        basename "$process"
        exit 0
    fi
done
exit 1
')"
test -n "$wetty_pid"
compose exec -T aiden sh -c 'kill "$1"' sh "$wetty_pid"

attempt=1
while [ "$attempt" -le 30 ]; do
    current_restart_count="$(docker inspect --format '{{.RestartCount}}' "$container_id")"
    if [ "$current_restart_count" -gt "$restart_count" ]; then
        break
    fi
    sleep 1
    attempt="$((attempt + 1))"
done
test "$current_restart_count" -gt "$restart_count"
wait_for_agent
curl -fsSL --max-time 10 "http://127.0.0.1:$agent_port/wetty/" >/dev/null

before_pid="$(agent_pid)"
test -n "$before_pid"

curl -fsS --max-time 15 -X POST \
    -H 'Content-Type: application/json' \
    --data '{"system_env":"AIDEN_DOCKER_SANDBOX_SMOKE=1\n"}' \
    "http://127.0.0.1:$config_port/api/system/env" \
    | grep -q '"agent_restart_scheduled":true'

attempt=1
after_pid=""
while [ "$attempt" -le 45 ]; do
    after_pid="$(agent_pid)"
    if [ -n "$after_pid" ] && [ "$after_pid" != "$before_pid" ] \
        && curl -fsS --max-time 5 "http://127.0.0.1:$agent_port/api/tools" >/dev/null 2>&1; then
        break
    fi
    sleep 1
    attempt="$((attempt + 1))"
done
test -n "$after_pid"
test "$after_pid" != "$before_pid"

compose down
compose up -d
wait_for_agent
curl -fsS --max-time 10 "http://127.0.0.1:$config_port/api/config" \
    | grep -q 'AIDEN_DOCKER_SANDBOX_SMOKE=1'

printf 'Docker sandbox smoke test passed (agent pid %s -> %s).\n' "$before_pid" "$after_pid"

# Exercise restart after the bridge session is ready. The service must preserve
# the claimed session, start exactly one Agent, and release the route when the
# container stops.
compose down -v
if [ -z "$bridge_port" ]; then
    bridge_port="$(python3 -c 'import socket; sock = socket.socket(); sock.bind(("", 0)); print(sock.getsockname()[1]); sock.close()')"
fi
bridge_log="$(mktemp -t aiden-docker-bridge.XXXXXX)"
python3 -c '
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

port = int(sys.argv[1])

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        return

    def reply(self):
        payload = json.dumps({"ok": True, "data": {"platform": "ios"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        try:
            self.wfile.write(payload)
        except BrokenPipeError:
            pass

    def do_GET(self):
        print("GET", self.path, flush=True)
        self.reply()

    def do_POST(self):
        size = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(size).decode()
        task_id = self.headers.get("benchmark-task-id", "")
        print("POST", self.path, task_id, body, flush=True)
        if self.path == "/api/setup":
            time.sleep(5)
        self.reply()

ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
' "$bridge_port" >"$bridge_log" 2>&1 &
bridge_pid="$!"
sandbox_bridge_endpoint="http://host.docker.internal:$bridge_port"
sandbox_device_type="iOS"

compose up -d
wait_for_agent

attempt=1
while [ "$attempt" -le 30 ]; do
    if grep -q 'POST /api/setup docker-sandbox-smoke' "$bridge_log"; then
        break
    fi
    sleep 0.2
    attempt="$((attempt + 1))"
done
grep -q 'POST /api/setup docker-sandbox-smoke' "$bridge_log"
setup_count="$(grep -c 'POST /api/setup docker-sandbox-smoke' "$bridge_log" || true)"
test "$setup_count" -eq 1

curl -fsS --max-time 15 -X POST \
    -H 'Content-Type: application/json' \
    --data '{"system_env":"AIDEN_DOCKER_SANDBOX_RACE=1\n"}' \
    "http://127.0.0.1:$config_port/api/system/env" >/dev/null
wait_for_agent

compose exec -T aiden sh -c '
count=0
for process in /proc/[0-9]*; do
    executable="$(readlink "$process/exe" 2>/dev/null || true)"
    if [ "$executable" = /oem/usr/bin/agent ]; then
        count="$((count + 1))"
    fi
done
test "$count" -eq 1
'

release_count="$(grep -c 'POST /api/release docker-sandbox-smoke {}' "$bridge_log" || true)"
test "$release_count" -eq 0
setup_count="$(grep -c 'POST /api/setup docker-sandbox-smoke' "$bridge_log" || true)"
test "$setup_count" -eq 1
compose down
attempt=1
while [ "$attempt" -le 20 ]; do
    if grep -q 'POST /api/release docker-sandbox-smoke {}' "$bridge_log"; then
        break
    fi
    sleep 0.2
    attempt="$((attempt + 1))"
done
setup_count="$(grep -c 'POST /api/setup docker-sandbox-smoke' "$bridge_log" || true)"
release_count="$(grep -c 'POST /api/release docker-sandbox-smoke {}' "$bridge_log" || true)"
test "$setup_count" -eq 1
test "$release_count" -eq 1

printf 'Docker sandbox bridge restart and release test passed.\n'
