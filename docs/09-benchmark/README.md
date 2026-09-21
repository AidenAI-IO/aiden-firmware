---
sidebar_label: Overview
sidebar_position: 0
---

# Agent Benchmark

Agent benchmark evaluates the Aiden Go agent on phone UI, memory, planning,
perception, and environment-control tasks. The current recommended entry points
are:

- WebUI for day-to-day runs, MobileGym concurrency, task-level logs/screens, and reports.
- CLI for scripted runs, single-suite debugging, rejudge, and compare.

For the full manual, see [`benchmark/manual.md`](../../benchmark/manual.md).

## Quick Start

### Dedicated Agent

Start a benchmark daemon first. `start-agent-daemon` creates a dedicated config
directory under `runs/cli-services`, copies only static configuration from
`config`, and starts the daemon with fresh memory and other runtime state. Reusing
a normal agent daemon would reuse its persistent state.

```bash
cd benchmark
uv sync

uv run python -m runner start-agent-daemon \
  --name memory-quickstart \
  --port 18081 \
  --base-config-dir config

uv run python -m runner run \
  --suite suites/memory_v1.json \
  --agent-url http://127.0.0.1:18081
```

Use the `stop_command` printed by `start-agent-daemon` when the run is complete.

For notification memory, run the dedicated suite against a fresh daemon with a
benchmark token. It injects deterministic raw notification fixtures, processes
them through the real notification processor, and checks temporary-memory
recall, OTP/marketing filtering, and multi-batch cursor drain:

```bash
uv run python -m runner run \
  --suite suites/notification_memory_v1.json \
  --auto-agent-setup \
  --agent-config /path/to/agent.toml
```

This run enables the judge so the useful-notification answer and noise-filtering
behavior are scored for effect. Add `--no-judge` when checking only the
deterministic gates (fixture persistence through the benchmark endpoint, cursor
advancement, memory count/scope, and required/forbidden tool calls). Raw JSONL
preservation is covered by the Go integration tests rather than an Agent shell
task. See the full manual for the corresponding Go performance benchmarks.

The notification suite also preloads existing temporary memories and validates
the externally observable `update`, `reinforce`, `remove`, and `promote` paths.
It uses ordered generic setup primitives and `assert_memory`; the runner does not
contain action-specific notification logic. Source/evidence reference checks use
the generic `source_refs_contain`/`evidence_refs_contain` assertion fields.

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

### Aiden App Policy Without a Phone

Use suite-level defaults or task-level `mock_environment` fixtures for
deterministic Phone Bridge strategy tests. The runner simulates platform/app state
and tool results, so contacts and similar app-side data do not require a physical
phone or emulator. Task-level fixtures let one suite hold a runtime policy matrix
without creating a JSON file for every case:

In the WebUI, select one or more mock suites and click `Run selected suites`. The
run configuration changes to `Mock Aiden App environment`, skips the device
picker, and starts the task-level fixtures automatically. Mock and external-device
suites must be run as separate jobs.

```bash
cd benchmark
uv run python -m runner run \
  --suite suites/aiden_app/phone_bridge_data_policy_v1.json \
  --auto-agent-setup \
  --no-judge \
  --verbose
```

The Aiden App cases are consolidated into two suites:

- `notes_entry_policy_v1.json`: Notes is already open, its icon is visible, or
  neither is visible. The Agent respectively enters text directly, clicks the
  visible icon, or uses `search_launch_app`.
- `phone_bridge_data_policy_v1.json`: contacts, calendar query/create, clipboard
  read/write, and notification cases for iOS Dynamic Island restoration, iOS PiP,
  and Android FGS.

These mock fixtures do not emulate `ble_service` or a live iOS Wake subscriber,
so they do not cover the BLE Wake-specific allowlist or runtime tool filtering.

For iOS background without PiP, a reachable Dynamic Island return entry keeps the
data tools visible. The Agent calls the requested `bridge_*` tool directly; the
tool restores the Aiden App internally before executing, so the Agent must not click the
Dynamic Island or call `bridge_open_app`. With iOS PiP or Android FGS enabled,
background-safe data tools execute directly through the background queue.
`bridge_open_app` remains excluded because PiP/FGS do not provide background app
launching.

The Notes cases use a fixed `bridge_contacts` result and require
`enter_text` without a separate `bridge_clipboard` call. The generated
screen is retained for runner pre/post artifacts and fixture state transitions;
the policy tests do not require the Agent to inspect it. Scripted
`screen_contains` preconditions prevent text entry before the fixture has actually
reached the Notes editor.

The mock checks that the Agent chooses `enter_text`; it does not run
the tool's internal paste fallbacks. In the real Go tool the order is Phone Bridge
clipboard, `quick_action` paste, direct keyboard paste if the action errors,
visual verification, then long-press Paste if the shortcut had no visible
effect. Ordinary typing fallback belongs to `enter_text`.

Use real devices separately for iOS BLE pairing/Wake, the narrower BLE Wake
allowlist, iOS PiP/Android FGS lifecycle, USB ECM, native permissions, actual
background queue delivery, app launch behavior, and HID paste validation. Mock
suites test their modeled policy and tool selection, not OS integration.

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
│   ├── judge.py         # OpenAI-compatible LLM judge
│   └── html_report.py   # HTML report generation
├── suites/              # Benchmark suites
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

Suites with suite-level or task-level `mock_environment` require
`--auto-agent-setup` and must not also pass `--environment-url`; the runner starts
the scripted bridge itself and activates the matching fixture before each task.

Auto-started Docker workers receive a host-resolved, read-only `agent.toml`.
Credential references such as `api_key = "$AIDEN_BENCHMARK_AGENT_API_KEY"` must resolve on the
host before the daemon starts. MobileGym endpoints also pass a functional
`setup` / `release` preflight before workers are created.

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

### Publish a completed run to Langfuse

Publishing is a separate post-run step, so a failed upload can be retried without
rerunning the Agent or device environment. Each stable suite name maps to one
Langfuse Dataset, every task attempt maps to a stable Dataset Item, and each
benchmark execution creates an Experiment Run with item-level and aggregate
benchmark scores. Updating a suite upserts new Dataset Item versions instead of
creating a new Dataset. Each new run also stores `suite.json` beside its manifest,
so it remains publishable after the source suite changes.

```bash
cd benchmark
export LANGFUSE_PUBLIC_KEY="pk-lf-..."
export LANGFUSE_SECRET_KEY="sk-lf-..."
export LANGFUSE_BASE_URL="http://127.0.0.1:3010"

uv run python -m runner publish-langfuse --run-dir runs/<run-id>
```

Runs with the same suite name appear in the same Dataset. The experiment metadata
records the Git SHA, suite hash, workload hash, model, judge, platform, metrics
schema, and fixed `k`. The workload hash covers only task and attempt keys. For a
like-for-like regression, require matching suite hash, workload hash, `k`, Agent
model, judge provider/model/prompt version, target platform, and active skills.
When comparing models or judges intentionally, treat that changed field as the
experiment axis and keep the other score-affecting metadata fixed. Runs from
different suite versions can still be inspected in Langfuse, but their aggregate
scores should not be treated as a like-for-like regression unless the unchanged
item intersection is used or the old product version is rerun against the new
suite definition. When an Agent episode ID is present, the experiment trace also
records the deterministic Agent trace ID for correlation with Aiden telemetry.

Publishing is idempotent by run ID. A retry verifies the Dataset Run Item set,
adds any missing items, and rewrites scores with stable IDs. A conflicting run ID
fails instead of silently mixing results. Langfuse's native Latency and Cost
columns describe the artifact replay used to construct the Experiment Run; use
the `benchmark.*`, `efficiency.*`, `reliability.*`, and
`cost_to_first_success.*` scores for real benchmark measurements.

The separate publish command is the CI integration point. A GitHub Actions job
can run a suite with a unique run ID and publish the completed artifacts in a
second step:

```yaml
- name: Run benchmark
  working-directory: benchmark
  run: |
    RUN_ID="ci-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${GITHUB_SHA::8}"
    echo "RUN_ID=$RUN_ID" >> "$GITHUB_ENV"
    uv run python -m runner run \
      --suite suites/<suite>.json \
      --run-id "$RUN_ID" \
      <environment-options>

- name: Publish benchmark to Langfuse
  working-directory: benchmark
  env:
    LANGFUSE_PUBLIC_KEY: ${{ secrets.LANGFUSE_PUBLIC_KEY }}
    LANGFUSE_SECRET_KEY: ${{ secrets.LANGFUSE_SECRET_KEY }}
    LANGFUSE_BASE_URL: ${{ vars.LANGFUSE_BASE_URL }}
  run: uv run python -m runner publish-langfuse --run-dir "runs/$RUN_ID"
```

The repository does not yet schedule benchmark suites in GitHub Actions; runner,
device environment, suite selection, and trigger policy remain to be configured.

## Environment Variables

- `AIDEN_BENCHMARK_AGENT_PROVIDER` - Agent provider type for the default config template.
- `AIDEN_BENCHMARK_AGENT_MODEL` - Agent model for the default config template and run manifest.
- `AIDEN_BENCHMARK_AGENT_BASE_URL` - Agent model endpoint for the default config template.
- `AIDEN_BENCHMARK_AGENT_API_KEY` - Agent credential embedded in the generated read-only config.
- `AIDEN_BENCHMARK_JUDGE_MODEL` - Default judge model.
- `AIDEN_BENCHMARK_JUDGE_BASE_URL` - Default OpenAI-compatible judge endpoint.
- `AIDEN_BENCHMARK_JUDGE_API_KEY` - Required when judge is enabled.
- `AIDEN_BENCHMARK_ANALYSIS_API_KEY` - Optional post-run analysis credential; defaults to the Judge key.
- `AIDEN_AGENT_URL` - Default `--agent-url`.
- `AIDEN_ENVIRONMENT_URL` - Default `--environment-url`.
- `AIDEN_DAEMON_IMAGE` - Default daemon worker image for auto agent setup.
- `LANGFUSE_PUBLISH_VERIFY_TIMEOUT_SECONDS` - Maximum publication read-back wait; defaults to 660 seconds to cover Langfuse asynchronous ingestion lag.

## Execution Modes

- Existing agent: CLI talks to one already-running Go agent daemon.
- Auto agent setup: CLI starts isolated daemon workers and uses an environment bridge for tools/screens.
- WebUI: manages suites, jobs, environments, workers, task screens, logs, and persisted job records.

The benchmark suite format is shared across physical devices and MobileGym.
SkillOpt is independent and lives under `skillopt/`; it may call benchmark
runner APIs, but benchmark does not expose SkillOpt runs, suites, or reports.

## Related Documentation

- [Architecture Design](./architecture.md)
- [Implementation Backlog](./implementation-backlog.md)
- [Detailed Guide](./quickstart.md)
- [Environment Bridge Protocol](../../benchmark/environment_bridge.md)
- [Full Manual](../../benchmark/manual.md)
