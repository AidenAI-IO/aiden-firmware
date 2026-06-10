# MobileGym Parallel Benchmark Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build isolated parallel execution for one or more MobileGym benchmark suites, with per-suite and batch-level result summaries and HTML reports.

**Architecture:** `parallel_run.sh` becomes the host-side orchestrator: it expands suites/tasks into work items, creates a clean worker config, launches each work item in a unique Docker Compose project, captures logs, cleans up, and calls a report generator. `run_aiden.py` remains the in-container launcher and gains shard metadata output after MobileGym task resolution. A new MobileGym report module reads shard metadata plus MobileGym raw output and writes suite/batch summaries and HTML.

**Tech Stack:** Bash, Docker Compose, Python 3.10+, pytest, MobileGym runner/recorder output.

---

## File Map

- Modify: `benchmark/mobilegym/scripts/run_aiden.py`
  - Add `--shard-metadata-file`.
  - Write selected task IDs/count after task sharding.
  - Preserve empty-shard success behavior.
- Modify: `benchmark/mobilegym/docker/parallel_run.sh`
  - Parse `--suite`, `--suites`, and positional tasks.
  - Create batch/suite/shard result directories.
  - Create clean per-worker config dirs with unique control tokens.
  - Launch isolated compose projects with bounded concurrency and cleanup.
  - Capture `runner.log` and `compose.log`.
  - Call report generation at the end.
- Modify: `benchmark/mobilegym/docker/docker-compose.yml`
  - Remove fixed container names if present.
  - Add stable `image:` names.
  - Mount `${AIDEN_CONFIG_DIR:-../config}` at `/config`.
- Modify: `benchmark/mobilegym/docker/docker-compose.cn.yml`
  - Mirror standard compose changes.
- Create: `benchmark/mobilegym/docker/docker-compose.parallel.yml`
  - Reset host port bindings for parallel workers.
- Create: `benchmark/mobilegym/report.py`
  - Normalize MobileGym shard outputs.
  - Write suite `summary.json`/`index.html`.
  - Write batch `summary.json`/`index.html`.
  - Provide a CLI entrypoint usable as `python -m mobilegym.report <batch-dir>` from the `benchmark` project.
- Modify: `benchmark/tests/mobilegym/test_run_aiden.py`
  - Cover shard metadata file output.
- Modify: `benchmark/tests/mobilegym/test_parallel_run.py`
  - Cover suite expansion, bounded scheduling, config isolation, logs, cleanup, no host ports, and `COMPOSE_FILES` handling.
- Create: `benchmark/tests/mobilegym/test_report.py`
  - Cover result normalization, missing task rows, errors, empty shards, and summaries.

Do not commit during this plan unless the user explicitly asks for a commit.

---

### Task 1: Add Shard Metadata Output To `run_aiden.py`

**Files:**
- Modify: `benchmark/mobilegym/scripts/run_aiden.py`
- Test: `benchmark/tests/mobilegym/test_run_aiden.py`

- [ ] **Step 1: Write failing tests**

Add tests for a helper that writes metadata without importing MobileGym runtime:

```python
def test_write_shard_metadata_records_selected_task_ids(tmp_path):
    module = load_run_aiden_module()
    metadata = tmp_path / "shard.json"
    tasks = [type("Task", (), {"id": "clock.CountAlarms"})(), {"id": "clock.ToggleAlarm"}, "raw.Task"]

    module._write_shard_metadata(metadata, tasks, shard_index=1, shard_count=4)

    payload = json.loads(metadata.read_text())
    assert payload["selected_task_count"] == 3
    assert payload["selected_task_ids"] == ["clock.CountAlarms", "clock.ToggleAlarm", "raw.Task"]
    assert payload["shard_index"] == 1
    assert payload["shard_count"] == 4
```

Also test that existing JSON is merged instead of replaced:

```python
def test_write_shard_metadata_preserves_existing_worker_fields(tmp_path):
    module = load_run_aiden_module()
    metadata = tmp_path / "shard.json"
    metadata.write_text('{"batch_id":"batch-x","exit_code":99}')

    module._write_shard_metadata(metadata, ["task.A"], shard_index=0, shard_count=1)

    payload = json.loads(metadata.read_text())
    assert payload["batch_id"] == "batch-x"
    assert payload["exit_code"] == 99
    assert payload["selected_task_ids"] == ["task.A"]
```

- [ ] **Step 2: Verify tests fail**

Run:

```bash
cd benchmark && uv run pytest tests/mobilegym/test_run_aiden.py -q
```

Expected: fail because `--shard-metadata-file` and `_write_shard_metadata` do not exist.

- [ ] **Step 3: Implement minimal code**

In `build_parser()`, add:

```python
execution.add_argument("--shard-metadata-file", type=Path, help="Write selected shard task metadata to this JSON file.")
```

In `_run_serial()`, after `_shard_tasks(...)` and before the empty-shard return:

```python
tasks = _shard_tasks(tasks, shard_index=args.shard_index, shard_count=args.shard_count)
if args.shard_metadata_file:
    _write_shard_metadata(args.shard_metadata_file, tasks, shard_index=args.shard_index, shard_count=args.shard_count)
if not tasks:
    return 0
```

Add helpers:

```python
def _write_shard_metadata(path: Path, tasks: list[Any], *, shard_index: int, shard_count: int) -> None:
    payload: dict[str, Any] = {}
    if path.exists():
        try:
            existing = json.loads(path.read_text(encoding="utf-8"))
            if isinstance(existing, dict):
                payload.update(existing)
        except (OSError, json.JSONDecodeError):
            pass
    payload.update(
        {
            "shard_index": shard_index,
            "shard_count": shard_count,
            "selected_task_count": len(tasks),
            "selected_task_ids": [_task_id(task) for task in tasks],
            "empty": not tasks,
        }
    )
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")


def _task_id(task: Any) -> str:
    if isinstance(task, str):
        return task
    if isinstance(task, dict):
        for key in ("id", "task_id", "name"):
            if task.get(key):
                return str(task[key])
    for name in ("id", "task_id", "name"):
        value = getattr(task, name, None)
        if value:
            return str(value)
    return str(task)
```

Add `import json` at the top.

- [ ] **Step 4: Verify**

Run:

```bash
cd benchmark && uv run pytest tests/mobilegym/test_run_aiden.py -q
```

Expected: pass.

---

### Task 2: Add MobileGym Report Aggregator

**Files:**
- Create: `benchmark/mobilegym/report.py`
- Test: `benchmark/tests/mobilegym/test_report.py`

- [ ] **Step 1: Write failing tests for normalization**

Create `test_report.py` with synthetic shard directories:

```python
import json

from mobilegym import report


def write_jsonl(path, rows):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(json.dumps(row) for row in rows) + "\n")


def test_report_normalizes_mobilegym_results_and_missing_tasks(tmp_path):
    shard = tmp_path / "batch-x" / "clock" / "shard-0"
    shard.mkdir(parents=True)
    (shard / "shard.json").write_text(json.dumps({
        "batch_id": "batch-x",
        "suite": "clock",
        "shard_index": 0,
        "shard_count": 1,
        "selected_task_count": 3,
        "selected_task_ids": ["task.pass", "task.fail", "task.missing"],
        "exit_code": 1,
    }))
    write_jsonl(shard / "raw" / "run-1" / "results.jsonl", [
        {"id": "task.pass", "is_success": True, "is_error": False},
        {"id": "task.fail", "is_success": False, "is_error": False, "execution": {"stop_reason": "false_complete"}},
    ])

    summary = report.generate_reports(tmp_path / "batch-x")

    assert summary["tasks"] == 3
    assert summary["passed"] == 1
    assert summary["failed"] == 1
    assert summary["worker_failed"] == 1
    assert (tmp_path / "batch-x" / "clock" / "summary.json").exists()
    assert (tmp_path / "batch-x" / "clock" / "index.html").exists()
    assert (tmp_path / "batch-x" / "index.html").exists()
```

Add separate tests for:

- fallback fields: `status`, `success`, and `passed`.
- `errors.jsonl` precedence over non-error result rows.
- `execution.stop_reason` values including `false_complete` and `overdue_termination`.
- assertion/evaluation failure fields if present.
- missing selected task IDs becoming `worker_failed` or `unknown`.
- empty shards becoming shard-level `empty` without reducing pass rate.
- suite summary fields: `tasks`, `passed`, `failed`, `error`, `empty`, `unknown`, `worker_failed`, `cleanup_failed`, `pass_rate`.
- batch summary `suites` aggregation.
- positional `tasks` synthetic suite aggregation.
- CLI invocation through `python -m mobilegym.report <batch-dir>`.
- HTML includes links to `runner.log`, `compose.log`, raw `results.jsonl`, raw `errors.jsonl`, and optional raw `console.log` when those files exist.

- [ ] **Step 2: Verify tests fail**

Run:

```bash
cd benchmark && uv run pytest tests/mobilegym/test_report.py -q
```

Expected: fail because `mobilegym.report` does not exist.

- [ ] **Step 3: Implement minimal report module**

Implement focused functions in `benchmark/mobilegym/report.py`:

- `generate_reports(batch_dir: Path) -> dict[str, Any]`
- `_load_shards(batch_dir)`
- `_normalize_shard(shard_dir)`
- `_read_results(raw_dir)`
- `_read_errors(raw_dir)`
- `_write_suite_report(suite_dir, rows, summary)`
- `_write_batch_report(batch_dir, suite_summaries)`
- `main(argv: list[str] | None = None) -> int`

Keep HTML simple and self-contained. It only needs summary cards, suite links, task rows, and links to `runner.log`/`compose.log`.

Status rules:

- `passed`: `is_success is True`.
- `error`: `is_error is True`, task appears in `errors.jsonl`, malformed row, or `execution.stop_reason == "overdue_termination"`.
- `failed`: `is_success is False` without `is_error`, including `execution.stop_reason == "false_complete"`.
- fallback result fields: `status == "passed"`, `success is True`, and `passed is True` map to `passed`.
- fallback result fields: `status == "failed"`, `success is False`, `passed is False`, failed assertion fields, and failed evaluation fields map to `failed`.
- missing expected task: `worker_failed` if shard exit code non-zero, otherwise `unknown`.
- `empty`: shard-level status when `selected_task_count == 0`.

At the bottom of `report.py`, add:

```python
if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 4: Verify**

Run:

```bash
(cd benchmark && uv run pytest tests/mobilegym/test_report.py -q)
if (cd benchmark && uv run python -m mobilegym.report /tmp/nonexistent-mobilegym-batch 2>/tmp/mobilegym-report-error.txt); then exit 1; fi
grep -qi "not found\|missing" /tmp/mobilegym-report-error.txt
```

Expected: pytest passes. The module invocation exits non-zero with a clear missing-directory error for the nonexistent path, proving the entrypoint resolves.

---

### Task 3: Make Compose Safe For Isolated Parallel Workers

**Files:**
- Modify: `benchmark/mobilegym/docker/docker-compose.yml`
- Modify: `benchmark/mobilegym/docker/docker-compose.cn.yml`
- Create: `benchmark/mobilegym/docker/docker-compose.parallel.yml`
- Test: `benchmark/tests/mobilegym/test_parallel_run.py`

- [ ] **Step 1: Write failing tests**

Add tests that call `docker compose -f docker-compose.yml -f docker-compose.parallel.yml --profile test config` and `docker compose -f docker-compose.cn.yml -f docker-compose.parallel.yml --profile test config`, then assert:

- no `container_name:` appears in compose files.
- `aiden-mobilegym-test-runner`, `aiden-mobilegym-daemon`, and `aiden-mobilegym-simulator` image names appear.
- `published:` does not appear in the parallel config.
- `/config` source uses `${AIDEN_CONFIG_DIR:-../config}`.
- the script always appends `docker-compose.parallel.yml` for parallel runs, even when `COMPOSE_FILES="docker-compose.cn.yml"`.

- [ ] **Step 2: Verify tests fail**

Run:

```bash
cd benchmark && uv run pytest tests/mobilegym/test_parallel_run.py -q
```

Expected: fail for missing override/config variable/image names.

- [ ] **Step 3: Implement compose changes**

In standard and CN compose files:

- remove any `container_name`.
- add stable image names:
  - `image: aiden-mobilegym-simulator:local`
  - `image: aiden-mobilegym-daemon:local`
  - `image: aiden-mobilegym-test-runner:local`
- change config volume to `${AIDEN_CONFIG_DIR:-../config}:/config:ro`.

Create `docker-compose.parallel.yml`:

```yaml
services:
  mobilegym:
    ports: !reset []
  daemon:
    ports: !reset []
```

In `parallel_run.sh`, treat `COMPOSE_FILES` as the base compose file list. Always append `docker-compose.parallel.yml` for parallel workers unless it is already present. This prevents `COMPOSE_FILES="docker-compose.cn.yml"` from accidentally publishing host ports.

- [ ] **Step 4: Verify**

Run:

```bash
(cd benchmark && uv run pytest tests/mobilegym/test_parallel_run.py -q)
(cd benchmark/mobilegym/docker && docker compose -f docker-compose.yml -f docker-compose.parallel.yml --profile test config)
(cd benchmark/mobilegym/docker && docker compose -f docker-compose.cn.yml -f docker-compose.parallel.yml --profile test config)
```

Expected: tests pass and config output contains no published host ports in parallel mode.

---

### Task 4: Rework `parallel_run.sh` Into A Batch Orchestrator

**Files:**
- Modify: `benchmark/mobilegym/docker/parallel_run.sh`
- Test: `benchmark/tests/mobilegym/test_parallel_run.py`

- [ ] **Step 1: Write failing tests for CLI expansion**

Use fake `docker` and fake `uv`/`python` if needed. Cover:

- `PARALLEL=2 ./parallel_run.sh --suite clock` starts shard `0/2` and `1/2`.
- `PARALLEL=2 ./parallel_run.sh --suites clock,phone_control_v1` creates four work items.
- positional tasks create `tasks/<slug-hash>` directories and one worker each.
- `MAX_JOBS=1` starts the second worker only after the first exits.
- failed worker does not prevent queued work from running.
- every started project gets `logs` and `down --volumes --remove-orphans`.
- generated config dirs contain unique `control_token` and no copied `memory`, `log`, or `skill-state` state.
- generated config dirs are outside the result artifact tree, have `0700` permissions, contain no static `bridge_token`, and are removed after worker cleanup.
- every per-worker `docker compose run`, `docker compose logs`, and `docker compose down` invocation receives that worker's `AIDEN_CONFIG_DIR` in its environment.
- initial suite-worker `shard.json` includes `batch_id`, `suite`, `shard_index`, `shard_count`, `compose_project`, `started_at`, and paths for raw/log artifacts.
- final suite-worker `shard.json` includes `finished_at`, `exit_code`, and `cleanup_failed` while preserving fields merged by `run_aiden.py`.
- `run_aiden.py` metadata merge preserves wrapper fields and adds `selected_task_count`, `selected_task_ids`, and `empty`.
- `runner.log` captures `docker compose run` stdout/stderr.
- `compose.log` is written before `docker compose down`.
- cleanup failure marks the worker failed and produces a non-zero final exit code.
- default `MAX_JOBS` equals `PARALLEL` and is capped by total work item count.
- `INT`/`TERM` cleanup tears down all active projects and prevents queued workers from starting afterward.
- positional task `shard.json` includes `suite: "tasks"`, `task_id`, `task_slug`, `shard_index: 0`, and `shard_count: 1`.
- after all workers finish, the orchestrator invokes `python -m mobilegym.report <batch-dir>` exactly once, even when one worker failed.

- [ ] **Step 2: Verify tests fail**

Run:

```bash
(cd benchmark && uv run pytest tests/mobilegym/test_parallel_run.py -q)
```

Expected: fail because current script only handles one suite shape and does not create batch artifacts/configs.

- [ ] **Step 3: Implement helpers**

In `parallel_run.sh`, add small helpers:

- `compose_args()` from `COMPOSE_FILES` or default `docker-compose.yml docker-compose.parallel.yml`.
- `slugify()` and `short_hash()`.
- `new_token()` using `python3 -c 'import secrets; print(secrets.token_urlsafe(32))'`.
- `prepare_worker_config(worker_dir)` under a temp root outside `benchmark/runs`, using `chmod 700`, copying only `agent.toml` and `skills/`, creating clean state dirs and `control_token`, and never creating a static `bridge_token`.
- `write_shard_json_initial(...)`.
- `update_shard_json_final(...)` that merges final fields into existing JSON without dropping `run_aiden.py` metadata.
- `run_worker(...)`.
- `cleanup_project(project, shard_dir, config_dir)` that writes `compose.log`, runs `down --volumes --remove-orphans`, removes `config_dir`, and records cleanup status in `shard.json`.
- `generate_reports(batch_dir)` via `(cd ../.. && uv run python -m mobilegym.report "$batch_dir")` from `benchmark/mobilegym/docker`.

All compose commands must be run through a wrapper that exports worker-specific environment:

```bash
compose_for_worker() {
    local project="$1"
    local config_dir="$2"
    shift 2
    COMPOSE_PROJECT_NAME="$project" AIDEN_CONFIG_DIR="$config_dir" docker compose "${compose_args[@]}" "$@"
}
```

Use this wrapper for `run`, `logs`, and `down`; do not call raw `docker compose` from worker execution paths.

- [ ] **Step 4: Implement scheduler**

Keep it simple:

- Build an array of work item records separated by tabs.
- Start workers until `running < MAX_JOBS`.
- Track `pid -> project/shard_dir` in parallel arrays.
- Use `wait -n` if available, otherwise poll `kill -0` with short sleep.
- Preserve final failure count.
- Add `trap` for `INT TERM EXIT` that sets a stopping flag, prevents queued workers from starting, and cleans active projects.
- Ensure `run_worker` redirects `docker compose run` stdout/stderr to `runner.log`:

```bash
compose_for_worker "$project" "$config_dir" --profile test run --rm test "$@" >"$runner_log" 2>&1
```

- Ensure `cleanup_project` captures logs before teardown:

```bash
compose_for_worker "$project" "$config_dir" logs --no-color >"$compose_log" 2>&1 || cleanup_failed=1
compose_for_worker "$project" "$config_dir" --profile test down --volumes --remove-orphans >>"$compose_log" 2>&1 || cleanup_failed=1
```

- [ ] **Step 5: Verify shell tests**

Run:

```bash
(cd benchmark && uv run pytest tests/mobilegym/test_parallel_run.py -q)
bash -n benchmark/mobilegym/docker/parallel_run.sh
```

Expected: pass.

---

### Task 5: Wire Reports And Metadata Through The Worker Command

**Files:**
- Modify: `benchmark/mobilegym/docker/parallel_run.sh`
- Modify: `benchmark/mobilegym/scripts/run_aiden.py`
- Test: `benchmark/tests/mobilegym/test_parallel_run.py`
- Test: `benchmark/tests/mobilegym/test_run_aiden.py`

- [ ] **Step 1: Write failing tests for worker command arguments**

Assert fake docker receives commands containing:

```text
--runs-dir /app/benchmark/runs/mobilegym/<batch>/<suite>/shard-0/raw
--shard-metadata-file /app/benchmark/runs/mobilegym/<batch>/<suite>/shard-0/shard.json
--shard-index 0 --shard-count 2
```

Assert positional tasks do not pass suite shard args unless needed and include `--task-id`.

- [ ] **Step 2: Verify tests fail**

Run:

```bash
(cd benchmark && uv run pytest tests/mobilegym/test_parallel_run.py -q)
```

- [ ] **Step 3: Implement argument wiring**

In `run_worker(...)`, pass:

```bash
--runs-dir "$container_raw_dir"
--shard-metadata-file "$container_shard_json"
```

For suite workers also pass:

```bash
--suite "$suite" --shard-index "$index" --shard-count "$count" --parallel 1
```

For positional tasks pass:

```bash
--task-id "$task" --parallel 1
```

- [ ] **Step 4: Verify**

Run targeted tests again:

```bash
(cd benchmark && uv run pytest tests/mobilegym/test_parallel_run.py tests/mobilegym/test_run_aiden.py -q)
```

---

### Task 6: Documentation Update

**Files:**
- Modify: `benchmark/mobilegym/README.md`
- Modify: `benchmark/mobilegym/docker/README.md`
- Modify: `benchmark/mobilegym/docker/TEST_FLOW.md`

- [ ] **Step 1: Update docs**

Document:

```bash
PARALLEL=4 ./parallel_run.sh --suite phone_control_v1
PARALLEL=2 MAX_JOBS=2 ./parallel_run.sh --suites clock,phone_control_v1
COMPOSE_FILES=docker-compose.cn.yml PARALLEL=2 ./parallel_run.sh --suite clock
./parallel_run.sh clock.CountAlarms clock.ToggleAlarm
```

Document result location:

```bash
open ../../runs/mobilegym/<batch>/index.html
open ../../runs/mobilegym/<batch>/<suite>/index.html
```

Mention that each worker has isolated simulator/daemon/config/network/volume and that only the result root is shared.

Also document:

- `MAX_JOBS` limits concurrent compose projects and defaults to `PARALLEL`.
- failed workers do not cancel queued work, but the final exit code is non-zero.
- `Ctrl-C` attempts to capture logs and tear down active projects.
- `COMPOSE_FILES` selects base compose files; the parallel no-ports override is still applied automatically.

- [ ] **Step 2: Verify docs references**

Run:

```bash
(cd benchmark && uv run pytest tests/mobilegym -q)
```

Expected: pass.

---

### Task 7: Final Verification

**Files:**
- All touched files.

- [ ] **Step 1: Run focused tests**

```bash
(cd benchmark && uv run pytest tests/mobilegym/test_run_aiden.py tests/mobilegym/test_parallel_run.py tests/mobilegym/test_report.py -q)
```

Expected: pass.

- [ ] **Step 2: Run all MobileGym tests**

```bash
(cd benchmark && uv run pytest tests/mobilegym -q)
```

Expected: pass.

- [ ] **Step 3: Run benchmark regression**

```bash
(cd benchmark && uv run pytest -q)
```

Expected: pass.

- [ ] **Step 4: Validate compose configs**

```bash
(cd benchmark/mobilegym/docker && docker compose -f docker-compose.yml -f docker-compose.parallel.yml --profile test config)
(cd benchmark/mobilegym/docker && docker compose -f docker-compose.cn.yml -f docker-compose.parallel.yml --profile test config)
```

Expected: both configs render; parallel config has no host-published ports.

- [ ] **Step 5: Inspect diff**

```bash
git status --short
git diff --stat
```

Expected: only intended MobileGym parallel benchmark/report files changed. Do not commit unless requested.
