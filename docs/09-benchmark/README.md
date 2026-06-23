# Agent Benchmark

Agent benchmark evaluates the Aiden Go agent on phone UI, memory, planning,
perception, and environment-control tasks. The current recommended entry points
are:

- WebUI for day-to-day runs, MobileGym concurrency, task-level logs/screens, and reports.
- CLI for scripted runs, single-suite debugging, rejudge, compare, and unit tool tests.

For the full manual, see [`benchmark/manual.md`](../../benchmark/manual.md).

## Quick Start

### Existing Agent

```bash
cd benchmark
uv sync
uv run python -m runner run \
  --suite suites/memory_v1.json \
  --agent-url http://192.168.1.100:8080
```

### WebUI

```bash
cd benchmark
uv run python -m runner webui
```

Open:

```text
http://127.0.0.1:8765
```

The WebUI can start MobileGym environments, read bridge concurrency from
`/api/concurrent`, start isolated agent daemon workers, and show task-level
screen/log records.

### MobileGym From CLI

```bash
cd benchmark
uv run python -m runner start-mobilegym-env --envs 5 --bridge-port 19090

uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --environment-url http://127.0.0.1:19090 \
  --auto-agent-setup
```

`--auto-agent-setup` ignores `--agent-url`, reads bridge capacity from
`/api/concurrent`, and starts one isolated agent daemon per active task worker.

## Reports

Runs write a self-contained report under `benchmark/runs/<run-id>/` or, for the
WebUI, under `benchmark/runs/webui/<job-id>/`.

Reports include:

- Suite/task status and pass rate.
- Prompt, final response, hard assertion failures, rubric verdicts.
- Tool trace extracted from agent history.
- `pre.jpg` and `post.jpg` screenshots when an environment bridge is configured.
- A `View full trace` action for manual inspection.

Judge uses only the pre/post screenshots plus trace/final response. It does not
consume every intermediate screenshot.

## Directory Structure

```text
benchmark/
├── runner/              # Python package
│   ├── main.py          # CLI entry point
│   ├── suite.py         # Suite loading
│   ├── runtask.py       # Task execution
│   ├── judge.py         # OpenRouter-compatible LLM judge
│   └── html_report.py   # HTML report generation
├── suites/              # Benchmark and unit suites
├── environment_bridge.md
├── manual.md
└── runs/<run_id>/       # CLI run results
    ├── manifest.json
    ├── results.jsonl
    ├── summary.md
    ├── report.html
    └── tasks/<task_id>/
        ├── pre.jpg
        ├── post.jpg
        ├── history.json
        ├── trace.json
        └── judge.json
```

## CLI Commands

### `run`

```bash
uv run python -m runner run --suite <suite.json> [options]
```

Common options:

- `--suite PATH` - Benchmark suite JSON path.
- `--agent-url URL` - Existing agent daemon URL.
- `--environment-url URL` - Environment bridge URL for setup, pre/post screen capture, and release.
- `--auto-agent-setup` - Start isolated agent daemon workers and schedule by bridge concurrency.
- `--agent-config PATH` - Agent config used for auto-started daemon workers.
- `--no-judge` - Skip LLM judge and only run hard assertions.
- `--task-id ID` / `--task-ids A,B` - Run selected tasks.
- `--repeats N` - Override task repeats.
- `--state-file PATH` - Write progress JSON for WebUI or scripts.
- `--verbose` - Print detailed rubric results.

### `unit`

```bash
uv run python -m runner unit --suite suites/unit/tools/<suite>.json
```

Unit suites call tools directly and do not use LLM judge.

### `rejudge`

```bash
uv run python -m runner rejudge --run-dir runs/<run_id>
```

Rejudge existing artifacts without re-executing tasks.

### `compare`

```bash
uv run python -m runner compare --runs runs/<run_a> runs/<run_b>
```

Compare task status flips, latency, and pass-rate changes between two runs.

## Environment Variables

- `OPENROUTER_API_KEY` - Required when judge is enabled.
- `AIDEN_AGENT_URL` - Default `--agent-url`.
- `AIDEN_ENVIRONMENT_URL` - Default `--environment-url`.
- `AIDEN_DAEMON_IMAGE` - Default daemon worker image for auto agent setup.

## Execution Modes

- Existing agent: CLI talks to one already-running Go agent daemon.
- Auto agent setup: CLI starts isolated daemon workers and uses an environment bridge for tools/screens.
- WebUI: manages suites, jobs, environments, workers, task screens, logs, and persisted job records.

The benchmark suite format is shared across physical devices and MobileGym.
SkillOpt is independent and lives under `skillopt/`; it may call benchmark
runner APIs, but benchmark does not expose SkillOpt runs, suites, or reports.

## Related Documentation

- [Architecture Design](./architecture.md)
- [Detailed Guide](./quickstart.md)
- [Environment Bridge Protocol](../../benchmark/environment_bridge.md)
- [Full Manual](../../benchmark/manual.md)
