#!/usr/bin/env bash
# parallel_run.sh - Run MobileGym benchmarks with isolated Docker workers.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

PARALLEL="${PARALLEL:-4}"
CHAT_TIMEOUT_SEC="${CHAT_TIMEOUT_SEC:-300}"
BATCH_ID="${MOBILEGYM_BATCH_ID:-batch-$(date +%Y%m%d-%H%M%S)}"
HOST_RUNS_ROOT="${MOBILEGYM_RUNS_ROOT:-$SCRIPT_DIR/../../runs/mobilegym}"
CONTAINER_RUNS_ROOT="${MOBILEGYM_CONTAINER_RUNS_ROOT:-/app/benchmark/runs/mobilegym}"
SOURCE_CONFIG_DIR="${AIDEN_SOURCE_CONFIG_DIR:-$SCRIPT_DIR/../config}"
CONFIG_TMP_ROOT="${MOBILEGYM_CONFIG_TMP_ROOT:-${TMPDIR:-/tmp}/mobilegym-parallel-configs-$BATCH_ID}"
LIMIT=""
AIDEN_TASK_IDS=""
STOPPING=0
FAILED=0
PIDS=()
ACTIVE_PIDS=()
ACTIVE_PROJECTS=()
ACTIVE_CONFIGS=()
ACTIVE_SHARDS=()

require_positive_int() {
    local name="$1"
    local value="$2"
    if [[ ! "$value" =~ ^[1-9][0-9]*$ ]]; then
        echo "Error: $name must be a positive integer (got: '$value')" >&2
        exit 2
    fi
}

validate_suite_name() {
    local suite="$1"
    local part
    local -a parts
    if [[ -z "$suite" || "$suite" == /* ]]; then
        echo "Error: invalid suite name: '$suite'" >&2
        exit 2
    fi
    IFS='/' read -r -a parts <<< "$suite"
    for part in "${parts[@]}"; do
        if [[ -z "$part" || "$part" == "." || "$part" == ".." || ! "$part" =~ ^[A-Za-z0-9_.-]+$ ]]; then
            echo "Error: invalid suite name: '$suite'" >&2
            exit 2
        fi
    done
}

validate_task_id_list() {
    local value="$1"
    if [[ -z "$value" || ! "$value" =~ ^[A-Za-z0-9_.-]+(,[A-Za-z0-9_.-]+)*$ ]]; then
        echo "Error: invalid Aiden task id list: '$value'" >&2
        exit 2
    fi
}

require_positive_int PARALLEL "$PARALLEL"
if [[ -n "${MAX_JOBS:-}" ]]; then
    require_positive_int MAX_JOBS "$MAX_JOBS"
fi

usage() {
    cat <<'EOF'
Usage: ./parallel_run.sh <task-id> [task-id...] | --suite <suite> [--limit N] | --suites <suite-a,suite-b> [--limit N] | --aiden-suite <name> [--aiden-task-ids id[,id...]] [--limit N] | --aiden-suites <name-a,name-b> [--limit N]

Examples:
  ./parallel_run.sh clock.CountAlarms clock.ToggleAlarm
  PARALLEL=4 ./parallel_run.sh --suite phone_control_v1
  PARALLEL=2 MAX_JOBS=2 ./parallel_run.sh --suites clock,phone_control_v1
  PARALLEL=4 ./parallel_run.sh --aiden-suite memory_v1
  PARALLEL=4 ./parallel_run.sh --aiden-suites memory_v1,perception/perception_v1
EOF
}

slugify() {
    printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9_-' '-' | sed 's/--*/-/g; s/^-//; s/-$//'
}

short_hash() {
    python3 - "$1" <<'PY'
import hashlib
import sys
print(hashlib.sha1(sys.argv[1].encode("utf-8")).hexdigest()[:8])
PY
}

new_token() {
    python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(32))
PY
}

current_model_name() {
    printf '%s' "${MODEL_NAME:-${AIDEN_MODEL:-${OPENAI_MODEL:-aiden-go}}}"
}

now_iso() {
    python3 - <<'PY'
from datetime import datetime, timezone
print(datetime.now(timezone.utc).isoformat())
PY
}

project_name() {
    local index="$1"
    local label="$2"
    printf 'mobilegym-%s-%s-%s' "$BATCH_ID" "$index" "$(slugify "$label")"
}

build_compose_args() {
    local raw="${COMPOSE_FILES:-docker-compose.yml}"
    local found_parallel=0
    local files=()
COMPOSE_ARGS=()
REQUIRED_IMAGES=(
    "aiden-mobilegym-simulator:local"
    "aiden-mobilegym-daemon:local"
    "aiden-mobilegym-test-runner:local"
)
    IFS=',' read -r -a files <<<"$raw"
    for file in "${files[@]}"; do
        if [[ -z "$file" ]]; then
            continue
        fi
        if [[ "$file" == "docker-compose.parallel.yml" ]]; then
            found_parallel=1
        fi
        COMPOSE_ARGS+=("-f" "$file")
    done
    if [[ $found_parallel -eq 0 ]]; then
        COMPOSE_ARGS+=("-f" "docker-compose.parallel.yml")
    fi
}

build_arg_values() {
    local proxy="${MOBILEGYM_DOCKER_PROXY:-}"
    local http_proxy="${HTTP_PROXY:-${http_proxy:-}}"
    local https_proxy="${HTTPS_PROXY:-${https_proxy:-}}"
    local no_proxy="${NO_PROXY:-${no_proxy:-localhost,127.0.0.1,daemon,mobilegym,test,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16}}"
    local playwright_host="${PLAYWRIGHT_DOWNLOAD_HOST:-${MOBILEGYM_PLAYWRIGHT_DOWNLOAD_HOST:-}}"
    local chromium_host="${PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST:-${MOBILEGYM_PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST:-}}"
    if [[ -n "$proxy" ]]; then
        http_proxy="$proxy"
        https_proxy="$proxy"
    fi
    BUILD_ARGS=()
    if [[ -n "$http_proxy" ]]; then
        BUILD_ARGS+=(--build-arg "HTTP_PROXY=$http_proxy" --build-arg "http_proxy=$http_proxy")
    fi
    if [[ -n "$https_proxy" ]]; then
        BUILD_ARGS+=(--build-arg "HTTPS_PROXY=$https_proxy" --build-arg "https_proxy=$https_proxy")
    fi
    if [[ -n "$no_proxy" ]]; then
        BUILD_ARGS+=(--build-arg "NO_PROXY=$no_proxy" --build-arg "no_proxy=$no_proxy")
    fi
    if [[ -n "$playwright_host" ]]; then
        BUILD_ARGS+=(--build-arg "PLAYWRIGHT_DOWNLOAD_HOST=$playwright_host")
    fi
    if [[ -n "$chromium_host" ]]; then
        BUILD_ARGS+=(--build-arg "PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST=$chromium_host")
    fi
}

compose_for_worker() {
    local project="$1"
    local config_dir="$2"
    shift 2
    COMPOSE_PROJECT_NAME="$project" AIDEN_CONFIG_DIR="$config_dir" docker compose "${COMPOSE_ARGS[@]}" "$@"
}

preflight_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        echo "Error: Docker CLI not found. Install Docker Desktop and make sure 'docker' is on PATH." >&2
        exit 2
    fi
    if ! docker compose version >/dev/null 2>&1; then
        echo "Error: Docker Compose v2 not available. Install/update Docker Desktop." >&2
        exit 2
    fi
}

ensure_images_built() {
    if docker image inspect "${REQUIRED_IMAGES[@]}" >/dev/null 2>&1 && images_are_fresh; then
        return 0
    fi
    echo "Building MobileGym Docker images..."
    build_arg_values
    if docker compose "${COMPOSE_ARGS[@]}" --profile test build "${BUILD_ARGS[@]}"; then
        return 0
    fi
    if should_try_cn_compose; then
        echo "Standard Docker Hub build failed; retrying with docker-compose.cn.yml..."
        if ! docker compose -f docker-compose.cn.yml -f docker-compose.parallel.yml --profile test build "${BUILD_ARGS[@]}"; then
            return 1
        fi
        COMPOSE_ARGS=(-f docker-compose.cn.yml -f docker-compose.parallel.yml)
        return 0
    fi
    return 1
}

should_try_cn_compose() {
    local arg
    for arg in "${COMPOSE_ARGS[@]}"; do
        if [[ "$arg" == "docker-compose.cn.yml" ]]; then
            return 1
        fi
    done
    [[ -f "$SCRIPT_DIR/docker-compose.cn.yml" ]]
}

run_report() {
    local batch_dir="$1"
    if command -v uv >/dev/null 2>&1; then
        (cd "$SCRIPT_DIR/../.." && uv run python -m mobilegym.report "$batch_dir")
        return $?
    fi
    if command -v python3 >/dev/null 2>&1; then
        (cd "$SCRIPT_DIR/../.." && python3 -m mobilegym.report "$batch_dir")
        return $?
    fi
    echo "Error: neither uv nor python3 found; cannot generate MobileGym report." >&2
    return 127
}

write_preflight_failure_report() {
    local message="$1"
    local suite="preflight"
    if [[ ${#WORK_ITEMS[@]} -gt 0 ]]; then
        IFS='|' read -r _kind suite _shard_index _shard_count _task_id _task_slug <<EOF
${WORK_ITEMS[0]}
EOF
    fi
    local shard_dir="$BATCH_DIR/$suite/shard-0"
    mkdir -p "$shard_dir"
    write_shard_json_initial "$shard_dir/shard.json" "$suite" 0 1 "preflight" "" "shard-0"
    printf '%s\n' "$message" > "$shard_dir/runner.log"
    update_shard_json_final "$shard_dir/shard.json" 2 0
    set +e
    run_report "$BATCH_DIR"
    set -e
}

images_are_fresh() {
    local newest
    newest="$(latest_source_mtime)"
    local image
    for image in "${REQUIRED_IMAGES[@]}"; do
        if [[ "$(image_created_epoch "$image")" -lt "$newest" ]]; then
            return 1
        fi
    done
    return 0
}

image_created_epoch() {
    local image="$1"
    local created
    created="$(docker image inspect --format '{{.Created}}' "$image" 2>/dev/null || true)"
    if [[ -z "$created" ]]; then
        echo 0
        return
    fi
    python3 - "$created" <<'PY'
from datetime import datetime, timezone
import sys

raw = sys.argv[1].strip()
try:
    value = raw.replace("Z", "+00:00")
    print(int(datetime.fromisoformat(value).timestamp()))
except Exception:
    print(0)
PY
}

latest_source_mtime() {
    python3 - "$SCRIPT_DIR" <<'PY'
import os
import sys
from pathlib import Path

docker_dir = Path(sys.argv[1]).resolve()
mobilegym_root = docker_dir.parent
benchmark_root = mobilegym_root.parent
paths = [
    docker_dir / "Dockerfile",
    docker_dir / "Dockerfile.cn",
    docker_dir / "docker-compose.yml",
    docker_dir / "docker-compose.cn.yml",
    docker_dir / "docker-compose.parallel.yml",
    mobilegym_root / "adapter",
    mobilegym_root / "bridge",
    mobilegym_root / "config",
    mobilegym_root / "scripts" / "run_aiden.py",
    mobilegym_root / "report.py",
    benchmark_root / "runner",
]
newest = 0
for path in paths:
    if not path.exists():
        continue
    if path.is_file():
        newest = max(newest, int(path.stat().st_mtime))
        continue
    for root, dirs, files in os.walk(path):
        dirs[:] = [name for name in dirs if name != "__pycache__"]
        for name in files:
            if name.endswith((".py", ".toml", ".yml", ".yaml")) or name.startswith("Dockerfile"):
                newest = max(newest, int((Path(root) / name).stat().st_mtime))
print(newest)
PY
}

prepare_worker_config() {
    local config_dir="$1"
    rm -rf "$config_dir"
    mkdir -p "$config_dir" "$config_dir/memory" "$config_dir/log" "$config_dir/skill-state"
    chmod 700 "$config_dir"

    if [[ -f "$SOURCE_CONFIG_DIR/agent.toml" ]]; then
        cp "$SOURCE_CONFIG_DIR/agent.toml" "$config_dir/agent.toml"
    elif [[ -f "$SOURCE_CONFIG_DIR/agent.toml.template" ]]; then
        cp "$SOURCE_CONFIG_DIR/agent.toml.template" "$config_dir/agent.toml"
        render_agent_template "$config_dir/agent.toml" "$config_dir/control_token"
    else
        echo "missing agent.toml or agent.toml.template in $SOURCE_CONFIG_DIR" >&2
        return 1
    fi

    if [[ -d "$SOURCE_CONFIG_DIR/skills" ]]; then
        cp -R "$SOURCE_CONFIG_DIR/skills" "$config_dir/skills"
    fi

    python3 - "$config_dir/agent.toml" <<'PY'
import sys
from pathlib import Path

try:
    import tomllib
except ModuleNotFoundError:
    import tomli as tomllib  # type: ignore[no-redef]

path = Path(sys.argv[1])
data = tomllib.loads(path.read_text(encoding="utf-8"))
device = data.get("device")
if isinstance(device, dict):
    device.pop("bridge_token_file", None)


def dump(value):
    if isinstance(value, str):
        escaped = value.replace("\\", "\\\\").replace('"', '\\"')
        return f'"{escaped}"'
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return repr(value)
    if isinstance(value, list):
        return "[" + ", ".join(dump(item) for item in value) + "]"
    raise TypeError(f"unsupported TOML value: {value!r}")


lines: list[str] = []
top_level = {key: val for key, val in data.items() if not isinstance(val, dict)}
for key, val in top_level.items():
    lines.append(f"{key} = {dump(val)}")

for key, val in data.items():
    if not isinstance(val, dict):
        continue
    if not val:
        continue
    lines.append("")
    lines.append(f"[{key}]")
    for sub_key, sub_val in val.items():
        lines.append(f"{sub_key} = {dump(sub_val)}")

path.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

    new_token > "$config_dir/control_token"
    chmod 600 "$config_dir/control_token"
}

render_agent_template() {
    local path="$1"
    local control_token_file="$2"
    python3 - "$path" "$control_token_file" <<'PY'
import os
import sys
from pathlib import Path

path = Path(sys.argv[1])
control_token_file = sys.argv[2]
replacements = {
    "MODEL_PROVIDER": os.getenv("MODEL_PROVIDER") or os.getenv("AIDEN_MODEL_PROVIDER") or "openrouter",
    "MODEL_NAME": os.getenv("MODEL_NAME") or os.getenv("AIDEN_MODEL") or "google/gemini-3.5-flash",
    "MODEL_BASE_URL": os.getenv("MODEL_BASE_URL") or os.getenv("AIDEN_MODEL_BASE_URL") or "https://openrouter.ai/api/v1",
    "MODEL_API_KEY": os.getenv("MODEL_API_KEY") or os.getenv("OPENROUTER_API_KEY") or os.getenv("AIDEN_MODEL_API_KEY") or "",
    "CONTROL_TOKEN_FILE": control_token_file,
}
text = path.read_text(encoding="utf-8")
for key, value in replacements.items():
    text = text.replace("{{" + key + "}}", value.replace('"', '\\"'))
path.write_text(text, encoding="utf-8")
PY
}

write_shard_json_initial() {
    local path="$1"
    local suite="$2"
    local shard_index="$3"
    local shard_count="$4"
    local project="$5"
    local task_id="$6"
    local task_slug="$7"
    mkdir -p "$(dirname "$path")"
    python3 - "$path" "$BATCH_ID" "$suite" "$shard_index" "$shard_count" "$project" "$task_id" "$task_slug" "$(now_iso)" "$(current_model_name)" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
task_id = sys.argv[7]
task_slug = sys.argv[8]
payload = {
    "batch_id": sys.argv[2],
    "suite": sys.argv[3],
    "shard_index": int(sys.argv[4]),
    "shard_count": int(sys.argv[5]),
    "compose_project": sys.argv[6],
    "started_at": sys.argv[9],
    "runner_log": "runner.log",
    "compose_log": "compose.log",
    "raw_dir": "raw",
    "model": sys.argv[10],
}
if task_id:
    payload.update({
        "task_id": task_id,
        "task_slug": task_slug,
        "selected_task_count": 1,
        "selected_task_ids": [task_id],
        "empty": False,
    })
path.write_text(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")
PY
}

update_shard_json_final() {
    local path="$1"
    local exit_code="$2"
    local cleanup_failed="$3"
    python3 - "$path" "$exit_code" "$cleanup_failed" "$(now_iso)" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
try:
    payload = json.loads(path.read_text(encoding="utf-8"))
except Exception:
    payload = {}
payload.update({
    "exit_code": int(sys.argv[2]),
    "cleanup_failed": int(sys.argv[3]),
    "finished_at": sys.argv[4],
})
path.write_text(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")
PY
}

cleanup_project() {
    local project="$1"
    local config_dir="$2"
    local shard_dir="$3"
    local compose_log="$shard_dir/compose.log"
    local lock="$shard_dir/.cleanup.lock"
    local cleanup_failed=0

    mkdir -p "$shard_dir"
    if ! ( set -o noclobber; : > "$lock" ) 2>/dev/null; then
        return 0
    fi

    compose_for_worker "$project" "$config_dir" logs --no-color > "$compose_log" 2>&1 || cleanup_failed=1
    compose_for_worker "$project" "$config_dir" --profile test down --volumes --remove-orphans >> "$compose_log" 2>&1 || cleanup_failed=1
    rm -rf "$config_dir"
    return "$cleanup_failed"
}

run_worker() {
    local kind="$1"
    local suite="$2"
    local shard_index="$3"
    local shard_count="$4"
    local task_id="$5"
    local task_slug="$6"
    local project="$7"
    local shard_dir="$8"
    local container_shard_dir="$9"
    local config_dir="${10}"

    local raw_dir="$shard_dir/raw"
    local runner_log="$shard_dir/runner.log"
    local shard_json="$shard_dir/shard.json"
    local container_raw_dir="$container_shard_dir/raw"
    local container_shard_json="$container_shard_dir/shard.json"
    local status=0
    local cleanup_failed=0

    mkdir -p "$raw_dir"
    prepare_worker_config "$config_dir"
    write_shard_json_initial "$shard_json" "$suite" "$shard_index" "$shard_count" "$project" "$task_id" "$task_slug"

    set +e
    if [[ "$kind" == "aiden_suite" ]]; then
        local -a aiden_task_id_args=()
        if [[ -n "$AIDEN_TASK_IDS" ]]; then
            aiden_task_id_args+=(--aiden-task-ids "$AIDEN_TASK_IDS")
        fi
        compose_for_worker "$project" "$config_dir" --profile test run --rm test \
            --aiden-suite "$suite" \
            ${aiden_task_id_args[@]+"${aiden_task_id_args[@]}"} \
            --shard-index "$shard_index" \
            --shard-count "$shard_count" \
            --env-url http://mobilegym:4173 \
            --chat-timeout-sec "$CHAT_TIMEOUT_SEC" \
            --runs-dir "$container_raw_dir" \
            --shard-metadata-file "$container_shard_json" \
            ${LIMIT:+--limit "$LIMIT"} \
            --parallel 1 \
            --headless > "$runner_log" 2>&1
        status=$?
    elif [[ "$kind" == "suite" ]]; then
        compose_for_worker "$project" "$config_dir" --profile test run --rm test \
            --suite "$suite" \
            --shard-index "$shard_index" \
            --shard-count "$shard_count" \
            --env-url http://mobilegym:4173 \
            --chat-timeout-sec "$CHAT_TIMEOUT_SEC" \
            --runs-dir "$container_raw_dir" \
            --shard-metadata-file "$container_shard_json" \
            ${LIMIT:+--limit "$LIMIT"} \
            --parallel 1 \
            --headless > "$runner_log" 2>&1
        status=$?
    else
        compose_for_worker "$project" "$config_dir" --profile test run --rm test \
            --task-id "$task_id" \
            --env-url http://mobilegym:4173 \
            --chat-timeout-sec "$CHAT_TIMEOUT_SEC" \
            --runs-dir "$container_raw_dir" \
            --shard-metadata-file "$container_shard_json" \
            --parallel 1 \
            --headless > "$runner_log" 2>&1
        status=$?
    fi
    cleanup_project "$project" "$config_dir" "$shard_dir"
    cleanup_failed=$?
    set -e

    update_shard_json_final "$shard_json" "$status" "$cleanup_failed"
    if [[ $status -ne 0 ]]; then
        return "$status"
    fi
    return "$cleanup_failed"
}

reap_pid() {
    local pid="$1"
    local rc="$2"
    if [[ $rc -ne 0 ]]; then
        FAILED=$((FAILED + 1))
    fi
    if [[ ${#ACTIVE_PIDS[@]} -gt 0 ]]; then
        local i
        for i in "${!ACTIVE_PIDS[@]}"; do
            if [[ "${ACTIVE_PIDS[$i]}" == "$pid" ]]; then
                unset 'ACTIVE_PIDS[i]'
                unset 'ACTIVE_PROJECTS[i]'
                unset 'ACTIVE_CONFIGS[i]'
                unset 'ACTIVE_SHARDS[i]'
            fi
        done
        ACTIVE_PIDS=(${ACTIVE_PIDS[@]+"${ACTIVE_PIDS[@]}"})
        ACTIVE_PROJECTS=(${ACTIVE_PROJECTS[@]+"${ACTIVE_PROJECTS[@]}"})
        ACTIVE_CONFIGS=(${ACTIVE_CONFIGS[@]+"${ACTIVE_CONFIGS[@]}"})
        ACTIVE_SHARDS=(${ACTIVE_SHARDS[@]+"${ACTIVE_SHARDS[@]}"})
    fi
    if [[ ${#PIDS[@]} -gt 0 ]]; then
        local kept=()
        local p
        for p in "${PIDS[@]}"; do
            if [[ "$p" != "$pid" ]]; then
                kept+=("$p")
            fi
        done
        PIDS=(${kept[@]+"${kept[@]}"})
    fi
}

wait_one() {
    if [[ ${#PIDS[@]} -eq 0 ]]; then
        return 0
    fi
    local pid=""
    local rc=0
    while [[ -z "$pid" ]]; do
        local p
        for p in "${PIDS[@]}"; do
            if ! kill -0 "$p" 2>/dev/null; then
                pid="$p"
                break
            fi
        done
        if [[ -z "$pid" ]]; then
            sleep 0.05
        fi
    done
    set +e
    wait "$pid" 2>/dev/null
    rc=$?
    set -e
    reap_pid "$pid" "$rc"
}

stop_active() {
    STOPPING=1
    if [[ ${#ACTIVE_PIDS[@]} -gt 0 ]]; then
        local pid
        for pid in "${ACTIVE_PIDS[@]}"; do
            if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
                kill -TERM "$pid" 2>/dev/null || true
            fi
        done
    fi
    if [[ ${#ACTIVE_PROJECTS[@]} -gt 0 ]]; then
        local idx
        for idx in "${!ACTIVE_PROJECTS[@]}"; do
            cleanup_project "${ACTIVE_PROJECTS[$idx]}" "${ACTIVE_CONFIGS[$idx]}" "${ACTIVE_SHARDS[$idx]}" || true
        done
    fi
}

on_signal() {
    stop_active
    exit 130
}

on_exit() {
    if [[ $STOPPING -eq 0 ]]; then
        stop_active
    fi
}

trap on_signal INT TERM
trap on_exit EXIT

if [[ $# -eq 0 ]]; then
    usage
    exit 1
fi

ARGS=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        --limit)
            if [[ $# -lt 2 || -z "$2" ]]; then
                echo "Error: --limit requires a positive integer" >&2
                exit 2
            fi
            require_positive_int limit "$2"
            LIMIT="$2"
            shift 2
            ;;
        --aiden-task-ids)
            if [[ $# -lt 2 || -z "$2" ]]; then
                echo "Error: --aiden-task-ids requires a comma-separated task id list" >&2
                exit 2
            fi
            validate_task_id_list "$2"
            AIDEN_TASK_IDS="$2"
            shift 2
            ;;
        *)
            ARGS+=("$1")
            shift
            ;;
    esac
done
set -- "${ARGS[@]}"

if [[ $# -eq 0 ]]; then
    usage
    exit 1
fi

build_compose_args
preflight_docker
mkdir -p "$HOST_RUNS_ROOT" "$CONFIG_TMP_ROOT"
HOST_RUNS_ROOT="$(cd "$HOST_RUNS_ROOT" && pwd)"
BATCH_DIR="$HOST_RUNS_ROOT/$BATCH_ID"
mkdir -p "$BATCH_DIR"

WORK_ITEMS=()
if [[ "$1" == "--aiden-suite" ]]; then
    if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: --aiden-suite requires a suite name" >&2
        exit 2
    fi
    validate_suite_name "$2"
    for i in $(seq 0 $((PARALLEL - 1))); do
        WORK_ITEMS+=("aiden_suite|$2|$i|$PARALLEL||shard-$i")
    done
elif [[ "$1" == "--aiden-suites" ]]; then
    if [[ -n "$AIDEN_TASK_IDS" ]]; then
        echo "Error: --aiden-task-ids requires --aiden-suite" >&2
        exit 2
    fi
    if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: --aiden-suites requires a comma-separated suite list" >&2
        exit 2
    fi
    suites="${2//,/ }"
    for suite in $suites; do
        validate_suite_name "$suite"
        for i in $(seq 0 $((PARALLEL - 1))); do
            WORK_ITEMS+=("aiden_suite|$suite|$i|$PARALLEL||shard-$i")
        done
    done
elif [[ "$1" == "--suite" ]]; then
    if [[ -n "$AIDEN_TASK_IDS" ]]; then
        echo "Error: --aiden-task-ids requires --aiden-suite" >&2
        exit 2
    fi
    if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: --suite requires a suite name" >&2
        exit 1
    fi
    validate_suite_name "$2"
    for i in $(seq 0 $((PARALLEL - 1))); do
        WORK_ITEMS+=("suite|$2|$i|$PARALLEL||shard-$i")
    done
elif [[ "$1" == "--suites" ]]; then
    if [[ -n "$AIDEN_TASK_IDS" ]]; then
        echo "Error: --aiden-task-ids requires --aiden-suite" >&2
        exit 2
    fi
    if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: --suites requires a comma-separated suite list" >&2
        exit 1
    fi
    suites="${2//,/ }"
    for suite in $suites; do
        validate_suite_name "$suite"
        for i in $(seq 0 $((PARALLEL - 1))); do
            WORK_ITEMS+=("suite|$suite|$i|$PARALLEL||shard-$i")
        done
    done
else
    if [[ -n "$AIDEN_TASK_IDS" ]]; then
        echo "Error: --aiden-task-ids requires --aiden-suite" >&2
        exit 2
    fi
    index=0
    for task in "$@"; do
        slug="$(slugify "$task")-$(short_hash "$task")"
        WORK_ITEMS+=("task|tasks|0|1|$task|$slug")
        index=$((index + 1))
    done
fi

TOTAL=${#WORK_ITEMS[@]}
set +e
ensure_images_built
preflight_status=$?
set -e
if [[ $preflight_status -ne 0 ]]; then
    write_preflight_failure_report "Docker image build failed before starting workers. See launcher log for build output."
    echo "Docker image build failed."
    echo "Report: $BATCH_DIR/index.html"
    exit "$preflight_status"
fi

MAX_JOBS_VALUE="${MAX_JOBS:-$PARALLEL}"
if [[ "$MAX_JOBS_VALUE" -gt "$TOTAL" ]]; then
    MAX_JOBS_VALUE="$TOTAL"
fi
if [[ "$MAX_JOBS_VALUE" -lt 1 ]]; then
    MAX_JOBS_VALUE=1
fi

echo "Running $TOTAL isolated MobileGym worker(s) in batch $BATCH_ID (max jobs: $MAX_JOBS_VALUE)..."

PIDS=()
ACTIVE_PIDS=()
ACTIVE_PROJECTS=()
ACTIVE_CONFIGS=()
ACTIVE_SHARDS=()
worker_index=0
for item in "${WORK_ITEMS[@]}"; do
    if [[ $STOPPING -ne 0 ]]; then
        break
    fi
    while [[ ${#PIDS[@]} -ge "$MAX_JOBS_VALUE" ]]; do
        wait_one
    done

    IFS='|' read -r kind suite shard_index shard_count task_id task_slug <<EOF
$item
EOF
    label="$suite-$shard_index"
    if [[ "$kind" == "task" ]]; then
        label="$task_slug"
        shard_dir="$BATCH_DIR/tasks/$task_slug"
        container_shard_dir="$CONTAINER_RUNS_ROOT/$BATCH_ID/tasks/$task_slug"
    else
        shard_dir="$BATCH_DIR/$suite/shard-$shard_index"
        container_shard_dir="$CONTAINER_RUNS_ROOT/$BATCH_ID/$suite/shard-$shard_index"
    fi
    project="$(project_name "$worker_index" "$label")"
    config_dir="$CONFIG_TMP_ROOT/$project"
    echo "Starting $project..."
    run_worker "$kind" "$suite" "$shard_index" "$shard_count" "$task_id" "$task_slug" "$project" "$shard_dir" "$container_shard_dir" "$config_dir" &
    new_pid=$!
    PIDS+=("$new_pid")
    ACTIVE_PIDS+=("$new_pid")
    ACTIVE_PROJECTS+=("$project")
    ACTIVE_CONFIGS+=("$config_dir")
    ACTIVE_SHARDS+=("$shard_dir")
    worker_index=$((worker_index + 1))
done

while [[ ${#PIDS[@]} -gt 0 ]]; do
    wait_one
done

set +e
run_report "$BATCH_DIR"
report_status=$?
set -e
if [[ $report_status -ne 0 ]]; then
    FAILED=$((FAILED + 1))
fi

if [[ $FAILED -eq 0 ]]; then
    echo "All MobileGym workers completed successfully."
    echo "Report: $BATCH_DIR/index.html"
    exit 0
fi

echo "$FAILED worker(s) failed."
echo "Report: $BATCH_DIR/index.html"
exit 1
