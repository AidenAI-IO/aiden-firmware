# MobileGym Parallel Benchmark Design

## Goal

Run one or more MobileGym benchmark suites concurrently while keeping every concurrent worker isolated from the others. Isolation must include the MobileGym simulator, Aiden daemon, bridge server, network, volumes, tokens, logs, and run artifacts.

## Recommended Model

MobileGym upstream supports local `--parallel` execution without Docker. This design uses one Docker Compose project per worker specifically to isolate Aiden Go daemon state: the current integration has one active MobileGym session per daemon, and conversation/memory are daemon-global.

Use one Docker Compose project per worker. Each worker runs exactly one shard of one suite inside its own compose project:

```text
suite clock, shard 0 -> project mobilegym-...-clock-0 -> mobilegym + daemon + test
suite clock, shard 1 -> project mobilegym-...-clock-1 -> mobilegym + daemon + test
suite phone_control_v1, shard 0 -> project mobilegym-...-phone-control-v1-0
suite phone_control_v1, shard 1 -> project mobilegym-...-phone-control-v1-1
```

The test runner inside each worker still uses `--parallel 1`. Overall concurrency comes from running multiple isolated compose projects at the same time.

## CLI Shape

Support these common commands:

```bash
./parallel_run.sh --suite phone_control_v1
PARALLEL=4 ./parallel_run.sh --suite phone_control_v1
PARALLEL=2 ./parallel_run.sh --suites clock,phone_control_v1,message_v1
MAX_JOBS=4 PARALLEL=2 ./parallel_run.sh --suites clock,phone_control_v1
./parallel_run.sh clock.CountAlarms clock.ToggleAlarm
```

Semantics:

- `--suite NAME` runs one suite split into `PARALLEL` shards.
- `--suites A,B,C` runs each suite separately, each split into `PARALLEL` shards.
- positional task IDs run as independent isolated workers, one task per worker.
- `MAX_JOBS` caps how many compose projects run at once across all suites. Default is `PARALLEL`.
- workers continue running after a peer fails; the final script exit code is non-zero if any worker fails.
- `Ctrl-C` stops queued work and tears down any running compose projects.
- `COMPOSE_FILES` may override the compose file set, for example `COMPOSE_FILES="docker-compose.cn.yml"`.
- every `run`, `logs`, `config`, and `down` command must use the same compose file set.

## Scheduler

The shell runner maintains a queue of work items and an active project registry.

- Work item fields: `suite`, `shard_index`, `shard_count`, `project`, `shard_dir`, `config_dir`, and command args.
- Default `MAX_JOBS` is `PARALLEL`, capped by the total number of generated work items. Users can explicitly raise or lower it.
- The scheduler starts at most `MAX_JOBS` workers at once and starts queued workers as running workers finish.
- Failed workers do not cancel queued work.
- The final exit code is `0` only when every started worker exits `0` and cleanup succeeds.
- `INT`, `TERM`, and `EXIT` traps iterate the active project registry, capture logs for any started project, and run compose `down --volumes --remove-orphans`.

## Task Sharding

`run_aiden.py` should accept `--shard-index`, `--shard-count`, and `--shard-metadata-file`. After MobileGym resolves the selected task list, the launcher keeps only tasks where:

```text
task_index % shard_count == shard_index
```

This keeps sharding stable and avoids requiring MobileGym upstream changes.

`run_aiden.py` writes the metadata file after task resolution and sharding. It includes `selected_task_count`, `selected_task_ids`, and whether the shard was empty. When `shard_count` is greater than the number of selected tasks, empty shards exit successfully and write `selected_task_count: 0`. The report renders those shards as `empty`, not failed or unknown.

## Result Layout

Batch runs should write to a top-level batch directory under `benchmark/runs/mobilegym/`:

```text
benchmark/runs/mobilegym/
  batch-20260610-153012/
    index.html
    summary.json
    clock/
      index.html
      summary.json
      shard-0/
        shard.json
        compose.log
        runner.log
        raw/
      shard-1/
    phone_control_v1/
      index.html
      summary.json
      shard-0/
      shard-1/
```

Each worker receives an explicit runs directory:

```bash
--runs-dir /app/benchmark/runs/mobilegym/<batch>/<suite>/shard-<n>/raw
```

The script also writes a `shard.json` next to `raw/` with at least:

```json
{
  "batch_id": "batch-20260610-153012",
  "suite": "clock",
  "shard_index": 0,
  "shard_count": 2,
  "compose_project": "mobilegym-...",
  "started_at": "...",
  "finished_at": "...",
  "selected_task_count": 3,
  "selected_task_ids": ["clock.CountAlarms", "clock.ToggleAlarm"],
  "exit_code": 0
}
```

The shell wrapper initializes `shard.json` with worker identity and timestamps. `run_aiden.py --shard-metadata-file` updates it with resolved task metadata. The wrapper records the final `exit_code` and cleanup status.

Before cleanup, the script saves `docker compose logs --no-color` to `compose.log`. Test runner stdout/stderr should be saved to `runner.log`. Suite-level `index.html` summarizes shards for that suite. Batch-level `index.html` links all suites and highlights failures.

## Visualization

MobileGym currently writes machine-readable results under `benchmark/runs/mobilegym/`, but this path is not wired to the existing generic `runner/html_report.py`. Add a lightweight MobileGym-specific HTML report generator that reads shard outputs and produces:

- per-suite pass/fail totals
- per-task rows grouped by shard
- links to shard raw files and logs
- failed task highlighting

The generator should tolerate missing optional files and still render partial results.

Report input schema:

- `shard.json`: worker metadata and exit code.
- `compose.log`: captured compose service logs.
- `runner.log`: captured test-runner stdout/stderr.
- `raw/**/results.jsonl`: MobileGym task results if present.
- `raw/**/errors.jsonl`: MobileGym task errors if present.
- `raw/**/console.log`: MobileGym runner console logs if present.

Task statuses are normalized from MobileGym `results.jsonl` fields when available. MobileGym rows use `id` as the task ID and commonly include `is_success`, `is_error`, and `execution.stop_reason`. `errors.jsonl` is joined by task id when present and takes precedence over a non-error result row. If expected task rows are missing, the report expands missing entries from `selected_task_ids`: missing tasks become `worker_failed` when `exit_code != 0`, `empty` when `selected_task_count == 0`, otherwise `unknown`.

Status normalization:

- `passed`: `is_success == true`, or fallback fields `status == "passed"`, `success == true`, or `passed == true`.
- `error`: `is_error == true`, malformed result, `errors.jsonl` entry for the task, or `execution.stop_reason` indicating crash/timeout such as `overdue_termination`.
- `failed`: `is_success == false` without `is_error`, `execution.stop_reason == "false_complete"`, explicit `status == "failed"`, `success == false`, `passed == false`, or a failed assertion/evaluation field.
- `empty`: `selected_task_count == 0` and worker exit code is `0`.
- `unknown`: worker exit code is `0` but no parseable per-task result exists.
- `worker_failed`: worker exit code is non-zero and no parseable per-task result exists.

## Summary JSON

Each suite `summary.json` contains:

```json
{
  "batch_id": "batch-20260610-153012",
  "suite": "clock",
  "shards": 2,
  "tasks": 12,
  "passed": 10,
  "failed": 1,
  "error": 1,
  "empty": 0,
  "unknown": 0,
  "worker_failed": 0,
  "cleanup_failed": 0,
  "pass_rate": 0.8333
}
```

Batch `summary.json` contains the same aggregate fields plus a `suites` array with one entry per suite. Pass-rate denominator is task-bearing statuses only: `passed + failed + error + unknown + worker_failed`, computed per expected task ID, not per shard. Empty shards do not reduce pass rate. Shard-level failures and cleanup failures are reported separately but are expanded into per-task `worker_failed` rows for any selected task ID that lacks a parseable result.

## Isolation Requirements

- Compose files must not pin `container_name`, because fixed names break concurrent projects.
- Each worker must set a unique `COMPOSE_PROJECT_NAME`.
- Parallel workers should not publish host ports. Preferred implementation: remove `ports` from the base compose files and put host port publishing in an optional debug override. If keeping ports in base files, the parallel override must explicitly reset them, for example with Compose `!reset []`, and tests must verify `docker compose config` contains no host port bindings.
- Each worker gets its own generated config directory mounted at `/config` via a compose variable such as `${AIDEN_CONFIG_DIR:-../config}`.
- Generated worker config starts from a clean template/base, not a mutable prior run directory.
- Generated worker config builder uses `0700` directories, copies only `agent.toml` plus required `skills/`, creates empty `memory`, `log`, and `skill-state` directories, and never copies previous state.
- Generated worker config includes a unique `control_token` file. Bridge/device tokens remain per-episode values generated by `run_aiden.py` and passed to the daemon via `/api/mobilegym/episode/start`; no static bridge token file is required for the current runtime.
- Worker config may contain model credentials, so it must stay outside result artifacts and should be cleaned up after worker teardown.
- The test runner uses the worker token through `/config/control_token`.
- The daemon entrypoint may rewrite token paths to `/config/...` because `/config` is worker-specific.
- Each worker should capture logs, then run `docker compose --profile test down --volumes --remove-orphans` after completion.
- Runtime/state volumes are isolated per compose project. The only shared mount is the result root, and each worker writes only into its unique shard directory.
- Compose services must use stable `image:` names or an explicit prebuild step so multiple project names do not trigger duplicate builds.

## Positional Task Results

Positional task batches use a synthetic suite group named `tasks`:

```text
benchmark/runs/mobilegym/<batch>/tasks/<task-slug>/
```

Each task still runs in one isolated compose project and gets its own `shard.json`, `compose.log`, `runner.log`, and `raw/` directory.

Task slugs must be collision-safe. Use a readable slug plus a short hash, for example `clock-countalarms-a1b2c3d4`. `shard.json` for positional tasks must include `suite: "tasks"`, `task_id`, `task_slug`, `shard_index: 0`, and `shard_count: 1`.

The `tasks` group gets the same suite-level `summary.json` and `index.html` model as named suites. Each task directory is treated like a one-task shard, and the batch-level `index.html` links the `tasks` group alongside named suites.

## Testing Strategy

- Unit-test `parallel_run.sh` with a fake `docker` executable to verify project names, no host port bindings in parallel mode, suite sharding args, multiple suite expansion, and cleanup.
- Unit-test `run_aiden.py` sharding logic.
- Unit-test result aggregation/report generation using synthetic shard directories.
- Unit-test worker config generation for unique tokens and clean memory/log/skill-state directories.
- Unit-test `MAX_JOBS`, `--suites`, failed worker continuation, signal cleanup, and missing raw files.
- Validate compose config for standard and CN compose files.
- Run `cd benchmark && uv run pytest` for benchmark regression.
