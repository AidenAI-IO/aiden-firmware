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
STOPPING=0
FAILED=0

require_positive_int() {
    local name="$1"
    local value="$2"
    if [[ ! "$value" =~ ^[1-9][0-9]*$ ]]; then
        echo "Error: $name must be a positive integer (got: '$value')" >&2
        exit 2
    fi
}

require_positive_int PARALLEL "$PARALLEL"
if [[ -n "${MAX_JOBS:-}" ]]; then
    require_positive_int MAX_JOBS "$MAX_JOBS"
fi

usage() {
    cat <<'EOF'
Usage: ./parallel_run.sh <task-id> [task-id...] | --suite <suite> | --suites <suite-a,suite-b>

Examples:
  ./parallel_run.sh clock.CountAlarms clock.ToggleAlarm
  PARALLEL=4 ./parallel_run.sh --suite phone_control_v1
  PARALLEL=2 MAX_JOBS=2 ./parallel_run.sh --suites clock,phone_control_v1
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

compose_for_worker() {
    local project="$1"
    local config_dir="$2"
    shift 2
    COMPOSE_PROJECT_NAME="$project" AIDEN_CONFIG_DIR="$config_dir" docker compose "${COMPOSE_ARGS[@]}" "$@"
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

write_shard_json_initial() {
    local path="$1"
    local suite="$2"
    local shard_index="$3"
    local shard_count="$4"
    local project="$5"
    local task_id="$6"
    local task_slug="$7"
    mkdir -p "$(dirname "$path")"
    python3 - "$path" "$BATCH_ID" "$suite" "$shard_index" "$shard_count" "$project" "$task_id" "$task_slug" "$(now_iso)" <<'PY'
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
    if [[ "$kind" == "suite" ]]; then
        compose_for_worker "$project" "$config_dir" --profile test run --rm test \
            --suite "$suite" \
            --shard-index "$shard_index" \
            --shard-count "$shard_count" \
            --env-url http://mobilegym:4173 \
            --chat-timeout-sec "$CHAT_TIMEOUT_SEC" \
            --runs-dir "$container_raw_dir" \
            --shard-metadata-file "$container_shard_json" \
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

build_compose_args
mkdir -p "$HOST_RUNS_ROOT" "$CONFIG_TMP_ROOT"
HOST_RUNS_ROOT="$(cd "$HOST_RUNS_ROOT" && pwd)"
BATCH_DIR="$HOST_RUNS_ROOT/$BATCH_ID"
mkdir -p "$BATCH_DIR"

WORK_ITEMS=()
if [[ "$1" == "--suite" ]]; then
    if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: --suite requires a suite name" >&2
        exit 1
    fi
    for i in $(seq 0 $((PARALLEL - 1))); do
        WORK_ITEMS+=("suite|$2|$i|$PARALLEL||shard-$i")
    done
elif [[ "$1" == "--suites" ]]; then
    if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: --suites requires a comma-separated suite list" >&2
        exit 1
    fi
    suites="${2//,/ }"
    for suite in $suites; do
        for i in $(seq 0 $((PARALLEL - 1))); do
            WORK_ITEMS+=("suite|$suite|$i|$PARALLEL||shard-$i")
        done
    done
else
    index=0
    for task in "$@"; do
        slug="$(slugify "$task")-$(short_hash "$task")"
        WORK_ITEMS+=("task|tasks|0|1|$task|$slug")
        index=$((index + 1))
    done
fi

TOTAL=${#WORK_ITEMS[@]}
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
(cd "$SCRIPT_DIR/../.." && uv run python -m mobilegym.report "$BATCH_DIR")
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
