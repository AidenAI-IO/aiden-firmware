---
sidebar_position: 4
---

# Benchmark Metrics Backlog

This document tracks the work required to turn the existing benchmark runner
into a Capability Benchmark. P0 improves measurement with the existing suites;
it does not add or freeze a new task set.

## Existing foundation

Repeated execution already exists. A task may define `repeats`, and the CLI may
override it with `--repeats N`. Each execution has an `attempt` number and its
own artifact directory.

P0 builds on that mechanism. `repeat` is execution configuration, not a metric.

## P0: reliable offline metrics

P0 adds one machine-readable `metrics.json` artifact and a concise English
section in `summary.md`. The existing HTML report and `compare` command remain
unchanged.

### Outcome and denominator

Each attempt records:

| Field | Definition |
|---|---|
| `success` | Whether the enabled evaluation contract passed. With `--no-judge`, this is the deterministic hard-assertion result. |
| `agent_eligible` | Whether the Agent execution produced an outcome that can fairly enter the success denominator. |
| `failure_class` | `agent`, `environment`, `evaluation`, `skipped`, or `unknown`; empty on success. |
| `quality_score` | Reproducible score in `[0,1]`; `null` when evaluation is invalid. |

Setup failures, unavailable providers, judge failures, and deliberate skips do
not enter Agent success or efficiency denominators. They remain in the raw
results and coverage counts.

### Repeated-run metrics

Every completed run declares a fixed `k` from its execution plan, never from
the largest attempt that happened to finish. If tasks request different repeat
counts, the runner uses the common planned prefix (the smallest repeat count)
and records that value in the manifest.

| Metric | Definition |
|---|---|
| `pass@1` | Fraction of eligible tasks whose first planned attempt succeeds. |
| `pass@k` | Fraction with at least one success when the first `k` planned attempts are eligible. |
| `pass^k` | Fraction whose first `k` planned attempts are eligible and all succeed. |
| `oracle_best_score@k` | Offline maximum `quality_score` among the first `k` attempts. |
| `first_success_attempt` | Attempt number of the first success. |
| `cost_to_first_success` | Cumulative wall time, tokens, and cost through the first success. |

Tasks missing any of their first `k` eligible attempts are excluded from
`pass@k` and `pass^k`, and eligible-task coverage is reported. Success rates
include 95% Wilson confidence intervals.

`oracle_best_score@k` is diagnostic only. A real best-of-k system in which a
verifier selects one candidate, including selector latency and cost, remains
P1.

### Telemetry with an authoritative source

P0 records the following values when their source is available and aggregates
numeric fields when applicable:

| Field | Source |
|---|---|
| `task_wall_ms` | Monotonic time from prompt submission to Agent termination. |
| `setup_ms` | Runner setup timing, reported separately from task wall time. |
| `tool_calls` | Parsed Agent history. |
| `device_actions` | Known device-action tool calls; read-only queries and screenshots are excluded. |
| `llm_calls` | History messages carrying model usage. |
| `input_tokens`, `output_tokens`, `total_tokens` | Episode run metrics, with history usage as a fallback. |
| `cached_input_tokens`, `reasoning_tokens` | Episode run metrics when reported by the provider. |
| `time_to_first_token_ms` | Episode run metrics. |
| `device_execution_ms` | Complete device-action durations from episode events. |
| `screenshot_capture_ms` | Runner-measured environment screenshot capture time. |
| `tool_errors`, `replan_count` | Explicit history or episode events. |
| `recovery_attempted`, `recovery_succeeded` | Runner timeout recovery. |

Missing telemetry is `null`, not zero. Aggregate latency, call, and token
statistics use eligible attempts only; setup timing may include invalid attempts
because it measures benchmark infrastructure.

P0 records a failure stage only when deterministic evidence identifies one.
Current reliable stages are setup, evaluation, and device execution. Other
failures remain `unknown`; the runner must not infer planner or grounding errors
from the final screenshot or a tool name. Evidence references must point to an
artifact that actually exists, such as `episode.json#/events/3`.

## Deferred work

The following metrics remain `null` or are not reported until the runtime
provides a reliable source:

- Per-request LLM latency and vision-LLM latency. The public history currently
  contains usage but not request duration.
- Provider cost. Do not estimate cost without a versioned price source and an
  explicit `cost_source`.
- In-task retry count. Do not infer retries from tool names; outer `attempt` and
  runner `repeat` are separate concepts.
- Automated root-cause labels such as grounding, planning, stale state, or
  keyboard/input errors.

Additional P1 work includes selector best-of-k, comparison compatibility and
warnings, report UI, and slices by platform, app, model, and configuration.

After the metrics pipeline is stable, P2 may freeze 30–50 representative mobile
tasks with pinned initial state, accounts, permissions, app/OS versions,
language, region, network fixtures, and cleanup rules.

## P0 acceptance

P0 is ready to use when:

1. An existing suite can run with a fixed repeat count.
2. `results.jsonl`, `manifest.json`, `metrics.json`, and `summary.md` agree.
3. `pass@1`, `pass@k`, `pass^k`, confidence intervals, and coverage can be
   recomputed from raw attempt results.
4. Missing telemetry remains `null`, and invalid attempts do not depress Agent
   success or efficiency metrics.
5. Agent, environment, evaluation, skipped, and unknown outcomes stay distinct.
