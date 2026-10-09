---
sidebar_position: 4
---

# Benchmark Metrics Reference

This reference defines benchmark result fields and exported scores, including
measurement scope, denominators, units, and missing-value semantics.

## 1. What the pipeline measures

```text
Benchmark runner
  → isolated task setup → Agent execution → assertions and optional LLM judge
  → results.jsonl → metrics.json, summary.md, report.html
  → Langfuse Dataset Experiment and benchmark scores
```

A **task** is one benchmark definition. An **attempt** is one planned execution
opportunity with its own number; setup failure or skipping can produce a result
without Agent execution. A **run** contains the selected tasks and their attempts.
Repeating a task is different from the Agent retrying a tool inside one attempt.

Use these artifacts together:

| Artifact | Question it answers |
| --- | --- |
| `manifest.json`, `suite.json` | Which code, suite, model, judge, platform, active skills, and repeat plan were evaluated? |
| `results.jsonl` | What happened in each task attempt, including eligibility, assertions, and scores? |
| `metrics.json` | What are the aggregate and per-task results? |
| `tasks/<task>/history.json`, `trace.json`, optional `episode.json` | What did the Agent observe, decide, and execute? Use the row's `artifact_dir` for repeated attempts. |
| `judge.json`, `hard_assertion_failures`, optional `environment_state.json` | What evidence made an attempt pass or fail? |
| `summary.md`, `report.html` | Where should a reviewer start investigating? |

Langfuse stores one Dataset per stable suite name, a Dataset Item per task/attempt,
and an Experiment Run per benchmark run. Agent episode IDs, when available, link
the experiment to the underlying Agent telemetry.

### Experiment output and execution evidence

| Field | Meaning and boundary |
| --- | --- |
| `final_response` | Final response from the attempt's saved `trace.json`. Null means evidence is unavailable; an empty string means the recorded response is empty. |
| `tool_calls` in Experiment Output | Ordered tool names and arguments from the saved trace, not tool results. This list differs from the numeric `benchmark.tool_calls` score. |
| `trace_artifact.status` | `available`: valid saved trace; `missing`: absent; `invalid`: malformed or unexpected structure; `unreadable`: file could not be read. |
| `trace_artifact.path` | Evidence path relative to the run directory; may be null when unavailable. |
| `aiden_episode_id` | Unambiguous Agent episode ID, or null if unavailable. |
| `aiden_trace_id` | Execution trace's 32-character hexadecimal OTLP ID. A known ID does not prove successful ingestion. |
| `aiden_trace_url` | Optional link to the execution trace; null when the episode ID or project lookup is unavailable. |
| `execution_trace_status` | `available`: a benchmark execution root was found and linked; `pending_or_missing`: the episode is known but its execution root was not found; `no_episode`: no unambiguous episode ID. Older items may omit this field. |

When `execution_trace_status=available`, the Experiment drawer uses the execution
trace. `agent-run` contains the goal and final answer, `agent-response` contains
model inputs and outputs, and tool observations contain arguments and results.
The `benchmark/<task>#attempt-<n>` node contains the evaluation output. Finding a
root does not guarantee all child observations were ingested. Unavailable
execution evidence uses a separate result replay; existing items retain their
original binding on publication retries. Replay parent and child observations
do not represent separate Agent attempts.

The Agent's `success` score describes its own outcome; `benchmark.success`
records the benchmark outcome. The evaluation node's duration measures
publication, so native Experiment Latency/Cost must not be substituted for the
benchmark execution metrics below. Publication success is not task success.

## 2. Metric reference

### Outcomes and eligibility

Attempt fields below are exported as `benchmark.<field>` scores unless noted.

| Field | Current meaning | How to use it |
| --- | --- | --- |
| `status` | `passed`, `failed`, `timeout`, `judge_error`, or `skipped` | Start the investigation here, then inspect eligibility and failure class. |
| `success` | `true`, `false`, or unknown | A known task outcome; unknown must not become zero. |
| `agent_eligible` | Whether this attempt participates in Agent capability metrics | Always inspect it alongside success rates. |
| `failure_class` | `agent`, `environment`, `evaluation`, `skipped`, or `unknown` | Separate capability failures from invalid measurements. |
| `quality_score` | Usually the fraction of judge rubric checks passed; hard-gate failures receive zero; unavailable evaluation receives unknown | Indicates partial completion, not necessarily task success. With `--no-judge`, a passing attempt receives 1 without semantic rubric evaluation. |
| `rubric_pass_rate` | Passed rubric checks divided by rubric count | Inspect individual verdicts: hard-gate failure may prevent the judge from running. |
| `expected_answer_match`, `expected_recalled_memory_match` | Deterministic checks when the suite configures them | Not automatically present in every memory task. |

The runner explicitly sets eligibility. Do not rebuild the denominator from
`status` alone: a `failed` setup assertion can still be ineligible, and a timeout
without evidence that execution started can have an unknown outcome. Inspect
the setup error instead of silently treating exclusion as product success.

### Status, failure class, stage, and eligibility are different dimensions

`status` records the runner's terminal result. `failure_class` is a coarse
classification, not a proven root cause or assignment of team responsibility.
`first_failure_stage` locates available failure evidence. `agent_eligible` controls
inclusion in capability denominators; `success` records the outcome value.

| Status | Meaning and boundary |
| --- | --- |
| `passed` | Enabled evaluations passed; disabled checks were not verified. |
| `failed` | A failure was recorded, including some setup failures; not necessarily an eligible Agent failure. |
| `timeout` | Task execution was recorded as timed out; setup timeouts can instead be skipped. |
| `judge_error` | Evaluation could not complete, including missing required memory or environment-state evidence; not limited to the LLM judge service. |
| `skipped` | Recorded as skipped or unable to complete normal execution; outer exception handlers can use it after partial execution. |

| Failure class | Current interpretation |
| --- | --- |
| `agent` | Eligible assertion/rubric/answer failure or ordinary timeout with execution evidence; audit the evaluator before concluding the model is wrong. |
| `environment` | Primarily setup assertion failures, initialization failures with consolidation evidence, or missing setup memory IDs; does not capture every environment incident. |
| `evaluation` | Judge failure or unavailable evidence needed to evaluate the attempt. |
| `skipped` | Coarse fallback for skips, preparation failures, and outer exceptions; inspect `metrics.error` and logs for the actual cause. |
| `unknown` | Insufficient classification evidence, for example chat request errors; can be eligible or ineligible. |
| null/absent | Usually no failure class on success, or missing legacy data; not the explicit `unknown` class. |

| Example | Status | Class | Raw success | Eligible |
| --- | --- | --- | --- | --- |
| Enabled checks passed | `passed` | null | true | true |
| Ordinary assertion/rubric failure | `failed` | `agent` | false | true |
| Ordinary timeout with execution evidence | `timeout` | `agent` | false | true |
| Timeout without execution evidence | `timeout` | `unknown` | null | false |
| Chat error with execution evidence | `failed` | `unknown` | false | true |
| Chat error without execution evidence | `failed` | `unknown` | null | false |
| Agent not ready, input screenshot missing, ordinary setup error | `skipped` | `skipped` | null | false |
| Setup assertion or specific consolidation initialization failure | `failed` | `environment` | false | false |
| Judge or required evidence unavailable | `judge_error` | `evaluation` | null | false |

Thus `status=skipped` and `failure_class=skipped` describe the same attempt on
two axes. Do not add `status.skipped` to `failures.class.skipped`. A skipped
classification alone does not identify the cause or prove no work took place.
Class counts include all classified result rows; stage counts include only
eligible failures. They cannot be directly compared as the same population.

Null means unavailable, unknown, or inapplicable depending on the field; numeric
zero means recorded zero under the instrumentation's scope. Boolean false is an
explicit negative. `recovery_attempted=false` means the runner did not enter its
timeout-recovery branch, and `recovery_succeeded=null` is not a failed recovery.

For legacy compatibility, the aggregator derives success from status whenever
the raw value is not Boolean, including null: passed becomes true, failed/timeout
become false. Explicit `agent_eligible=false` remains authoritative, excluding
such attempts from capability scores. Inspect raw results for event semantics;
this fallback supplies no new evidence. Missing eligibility is also inferred for
legacy rows, so old and new classifications are not necessarily comparable.

Hard-assertion Boolean values describe whether a check passed. In particular,
`hard_assertions.timeout=true` means the timeout check passed, not that a timeout
occurred. Required-tool checks mean required behavior appeared; forbidden-tool
and prohibited-action checks mean prohibited behavior did not appear. Null can
mean unconfigured, unevaluated, or missing evidence. Consult the specific error
and `hard_assertion_failures` (ID, requirement, actual value).

`rubric_spec` is the definition; `rubric` holds evaluated verdicts and reasons.
Nonzero `rubric_total` with zero `rubric_pass_count` can occur when the judge never
ran after a hard-gate failure. Check verdicts and `judge.json` before interpreting
that ratio as an actual semantic evaluation.

### Capability and consistency

Run scores use the `capability.` prefix. Let `k` be `manifest.metrics_k`, currently
the minimum planned repeat count across the selected tasks. Use `--repeats N`
to give every selected task the same repeat count. There is no separate
`--metrics-k` CLI option.

| Run score | Definition in this repository |
| --- | --- |
| `pass_at_1` | Fraction of tasks whose actual attempt 1 succeeds, among tasks with eligible attempt 1. |
| `pass_at_k` | Fraction of tasks with at least one success in attempts 1 through k; requires all k attempts to exist and be eligible. |
| `pass_pow_k` | Fraction of tasks whose first k attempts all succeed, using the same complete-task denominator. |
| `attempt_success_rate` | Successful eligible attempts / all eligible attempts, including attempts beyond k. |
| `oracle_best_score_at_k` | Mean of each complete task's best available quality score in the first k attempts. It describes best observed performance, not typical experience. |
| `first_success_attempt.mean`, `.p50`, `.count` | First successful attempt within each task's continuous eligible prefix starting at attempt 1. Can include attempts beyond k. Tasks with no success are absent. |

These are observed fixed-k outcomes, not an estimate from arbitrary combinations
of attempts. At `k=1`, `pass_at_1`, `pass_at_k`, and `pass_pow_k` coincide on the
same eligible task set; a single attempt cannot demonstrate repeatability.

The three pass metrics include `successes`, `eligible_tasks`, `total_tasks`, and
Wilson 95% confidence bounds. Attempt success also has confidence bounds, but
repeats of the same task are correlated: its simple interval does not account for
that dependence. For release decisions, compare paired tasks and use repeated
runs or uncertainty analysis grouped by task. Do not infer significance merely
from a small percentage increase or from overlap of separate confidence bounds.

### Coverage and diagnostics

| Score or artifact field | Meaning and limitation |
| --- | --- |
| `coverage.pass_at_1`, `.pass_at_k`, `.pass_pow_k` | Eligible tasks / observed unique tasks for that metric. Compare the manifest's planned task/attempt list as well: entirely missing results can escape this denominator. |
| `coverage.unique_tasks`, `.attempts`, `.metrics_k` | Scope of the observed workload. Legacy aggregate `tasks` counts attempts, not unique tasks. |
| `coverage.agent_eligible_attempts`, `.invalid_attempts` | How many observations can or cannot measure Agent capability. |
| `capability.attempt_success_rate.coverage` | Eligible attempts / observed attempts. |
| `failures.class.<class>` | Counts by failure class. These are counts, not normalized rates. |
| `failures.stage.<stage>` | Counts for eligible failed attempts, including `unknown`. |
| `diagnostics.failure_stage_coverage` | Fraction of eligible failures assigned a known stage. Unknown when there are no eligible failures. |
| `benchmark.first_failure_stage` | Evidence-based label where available; not a complete root-cause diagnosis. |
| `failure_event_ref` in an attempt's metrics | Pointer into history or episode evidence; not a separate Langfuse score. |

Stage vocabulary includes setup, perception, planning, action selection, device
execution, state verification, recovery, final response, evaluation, and unknown.
Having these labels available does not mean the runtime identifies every stage.
Many semantic failures currently remain `unknown`. Find the first meaningful
deviation in the trace; the first tool error can be a downstream symptom.

Category scores (`category.<category>.pass_rate`) use the category's total
attempt count, including invalid attempts, unlike eligible-only capability
scores. Observation scores (`observations.<id>.*`) count evaluated attempt rows
despite their historical `passed_tasks`/`observed_tasks` names. Trace observations
describe behavior, such as tool usage, and do not themselves make a task fail.

### Time, work, and cost

Per-attempt numeric fields appear as `benchmark.<field>`. Most run distributions
appear as `efficiency.<field>.{count,sum,mean,p50,p90,p95}`. Tool errors, replans,
and retries instead use the `reliability.` prefix.

| Field | Measurement boundary and interpretation |
| --- | --- |
| `task_wall_ms` | Elapsed time around the task's Agent chat request. Excludes setup, judge time, post-run artifact collection, and timeout recovery. Legacy `wall_ms` mirrors it in current results. |
| `setup_ms` | Task isolation/setup work. Report separately from task execution. |
| `llm_time_ms` | Sum of recorded durations on usage-bearing assistant/tool-call messages; unavailable when those durations are incomplete. |
| `vision_llm_calls`, `vision_llm_time_ms` | Calls inferred from image/screenshot observations preceding model messages. This is an instrumentation heuristic, not an exact provider billing category. |
| `time_to_first_token_ms` | Episode's reported first-token timing, when available. |
| `device_execution_ms` | Sum of episode durations for recognized device-action results; unavailable if required durations are missing. Tool classification is an explicit allowlist. |
| `screenshot_capture_ms` | Runner-side timed screenshot captures; not all screenshots taken internally by the Agent. Inspect `screenshot_capture_source`. |
| `tool_calls`, `device_actions` | All traced tool calls versus recognized device-changing calls. Read-only bridge queries are excluded from device actions. |
| `llm_calls` | Count derived from messages carrying usage; unavailable when no usage is present. Validate telemetry completeness before treating it as every provider call. |
| `input_tokens`, `output_tokens`, `total_tokens` | Reported Agent usage; episode totals can override history-derived totals. Does not measure the entire run cost or judge/setup consumption. |
| `cached_input_tokens`, `reasoning_tokens` | Optional usage details; unknown when not reported. Do not add them to totals as independent extra usage. |
| `tool_errors` | Recorded tool-result errors, including errors the Agent later recovered from. |
| `replan_count` | Explicit episode `needs_replan` evidence; not inferred from tool names. |
| `retry_count`, `cost_usd` | Schema/export support exists, but the current task runner does not populate a retry counter or calculate monetary cost. Expect unknown unless an explicit source supplies them. |
| `recovery_attempted`, `recovery_succeeded` | Runner recovery after an Agent timeout; not success of the user's task or a count of Agent strategy retries. |

Efficiency distributions use eligible attempts with numeric values; `setup_ms`
uses all observed attempts with values. Always read `count`: a lower mean based
on fewer measurements is not necessarily an improvement. Missing scores are
omitted from Langfuse, not published as zero. Percentiles use interpolation and
are weak evidence with only a few samples. Timing fields overlap or have different
boundaries; do not add them together as a complete wall-time decomposition.

`cost_to_first_success.{task_wall_ms,total_tokens,cost_usd}.{count,mean,p50}` sums
each field from attempt 1 through the first success, stopping at missing or
ineligible attempts. A field must be complete through success to contribute.
Tasks that never succeed contribute no value. Pair these metrics with success,
coverage, and total resource consumption; they cannot alone show the cost of
serving every user request.

### Numeric suffixes and remaining interpretation details

| Field or suffix | Meaning |
| --- | --- |
| `value` | Primary JSON metric value; proportions are 0–1. Langfuse usually omits `.value` from the primary score name. |
| `count` | Number of numeric samples, typically attempts for efficiency and tasks for first-success aggregates. |
| `sum`, `mean` | Sum and arithmetic mean of available values; partial coverage does not produce a complete bill. |
| `p50`, `p90`, `p95` | Interpolated percentiles in the metric's unit, not confidence levels. |
| `ci95.lower/upper` | JSON Wilson interval bounds; Langfuse suffixes are `ci95_lower/ci95_upper`. |
| `successes` | Successful tasks for pass metrics, successful attempts for attempt success. |
| `eligible_tasks/total_tasks` | Eligible versus observed unique tasks for the metric. |
| `eligible_attempts/total_attempts` | Eligible versus observed attempts. |
| `_ms`, `_tokens`, `_usd` | Milliseconds, token counts, and dollars respectively; divide milliseconds by 1000 to display seconds. |
| `screenshots_taken` | Tool calls marked `has_screenshot`, not every Agent/runner screenshot. |
| `pre_screenshot_file/post_screenshot_file` | Whether local screenshot files exist, not whether their content is correct. |
| `screenshot_capture_source/cost_source` | Source metadata in results, not independent numeric Langfuse scores. |
| `error/agent_error/judge_error/environment_state_error` | Details needed to interpret coarse failure labels. |
| `episode_error/pre_screenshot_error/post_screenshot_error` | Artifact acquisition errors; may reduce evidence/metrics without necessarily failing every task. |
| `episode_id/active_skills` | Telemetry correlation and activated skill names; names alone do not establish content identity. |
| `started_at/finished_at` | Result timestamps, whose difference is not interchangeable with task-only wall time. |
| `environment_state_assertions/trace_observations` | Individual configured state checks / behavioral observations. |

`aggregate.per_task` reports total attempts including invalid attempts, but uses
eligible attempts for passed/failed/pass rate and known numeric values for best
and average quality and average wall time. Its current `first_attempt_passed`
means the first **eligible** attempt after sorting, unlike `pass_at_1`, which
requires actual attempt 1. Do not substitute one for the other.

`oracle_best_score_at_k.count` can be smaller than its `eligible_tasks` when
quality scores are absent. Read `manifest.metrics_k` for each run; separate runs
are not automatically combined into k attempts.
