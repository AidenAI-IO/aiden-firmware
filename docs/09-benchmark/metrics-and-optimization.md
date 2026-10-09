---
sidebar_position: 4
---

# Benchmark Metrics and Optimization Playbook

[中文版](./metrics-and-optimization.zh-CN.md)

This guide explains the metrics currently produced by Aiden's benchmark runner,
how to turn them into engineering decisions, and how to verify an improvement.
The worked scenario is updating a remembered user preference from dark mode to
light mode. It uses the existing memory benchmark and needs no phone simulator.

## 1. What the pipeline measures

```text
GitHub Actions / CLI
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

**The native Langfuse Latency and Cost columns describe artifact replay during
publication. They do not measure the original Agent execution.** Use the exported
scores below. Publication success also does not mean that the benchmark passed.

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
| `input_tokens`, `output_tokens`, `total_tokens` | Reported Agent usage; episode totals can override history-derived totals. Does not measure the entire CI bill or judge/setup consumption. |
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
quality scores are absent. At the current scheduled runnable configuration all
suite runs use k=1, including notification memory with one five-repeat task and
other one-repeat tasks. Separate scheduled runs are not automatically combined
into k attempts. Verify the actual manifest for each run.

## 3. How the team uses these metrics

1. **Validate the measurement.** Match suite hash, workload hash, k, metric schema,
   Agent model, judge model/provider/prompt version, platform, and active skills.
   Record the code SHA, actual image identity, skill contents/configuration,
   environment version, and concurrency. Change only the declared experiment
   axis. A stable image tag or skill name does not prove identical contents.
2. **Choose a failure cluster.** Group by suite, task, rubric, and failure class.
   Prioritize frequent, user-important failures with a plausible common cause.
   Audit invalid runs separately rather than hiding them in an improved denominator.
3. **Inspect evidence.** Read several failures and nearby successes. Record the
   first incorrect decision, its evidence, and what remains uncertain.
4. **State one hypothesis.** Specify the code/prompt/skill change and the expected
   effect on a primary quality metric, with latency and resource guardrails.
5. **Run baseline and candidate.** Use the same tasks, clean setup, repeat count,
   model, judge, and resource allocation. Alternate run order when practical to
   reduce provider load or environment drift effects.
6. **Decide: accept, reject, or inconclusive.** Examine task-level regressions and
   uncertainty, not just averages. Evaluate an untouched task set before broad
   rollout. Preserve run IDs and the exact change so another teammate can repeat it.

For reliability work, prioritize `pass_at_1` and `pass_pow_k`. For efficiency
work, maintain quality and compare latency/tokens on matched tasks that both
versions successfully complete, while still reporting all failures separately.
An early failure can otherwise look like a speed improvement.

The current `runner compare` prints status changes and selected summaries; it
returns success and does not enforce a statistical regression gate. The existing
SkillOpt loop can propose skill edits, but its weighted hard-success acceptance
score is not the same as benchmark `pass_at_1`. Add coverage and efficiency review
and retain an untouched test set when using it for optimization.

## 4. Worked scenario: update a remembered preference

### Existing task and real failure evidence

Use `update_changed_preference` in
[`memory_v1.json`](../../benchmark/suites/memory_v1.json):

- Setup asks the Agent to remember a dark-mode preference and clears visible
  conversation history afterward.
- The task asks it to update that preference to light mode.
- Hard assertions require 2–12 tool calls, completion within 120 seconds, and a response.
- The judge checks recall of the old preference, saving a light-mode preference,
  and a response confirming the update.

A local historical artifact (`2026-05-29_044024`, task
`update_changed_preference`) contains one `save_memory` call for light mode, no
`recall_memory` call, and a final response claiming the update was complete.
It records `status=failed`, `tool_calls=1`, and a failed minimum-tool-call check.
The judge did not run because the hard gate already failed.

This is evidence of a historical trace/contract mismatch, not evidence that the
current Agent still has the bug. That old run lacks the current `metrics.json`
and cannot be used as a like-for-like modern baseline. Its recorded wall time
must not be compared directly to today's task-only timer. Local run artifacts
are not required to use this guide and should not be committed.

### What optimization would mean

Hypothesis: when a user explicitly replaces a remembered preference, the Agent
must identify the existing relevant memory and use the current supported
replacement/update semantics, rather than merely create another statement and
claim completion. Verify which semantics the current memory tools provide before
choosing a prompt change or a storage/retrieval fix.

An example candidate policy to investigate is:

> When the user replaces an existing preference, retrieve the relevant stored
> preference, apply the supported replacement operation, and confirm the new
> preference only after the operation succeeds.

Only introduce or change this policy if baseline traces show it is missing or
ineffective. Do not add arbitrary calls just to satisfy `min_tool_calls`.

The existing task checks the update interaction. **It does not prove that old
and new records cannot conflict, or that the new preference survives a later
conversation.** To validate the product outcome, also check persisted active
memory state and ask a fresh conversation which display mode the user prefers.
The answer must select light mode, and the old dark preference must not remain
an active conflicting preference. Inspect storage/retrieval evidence, not just
the assistant's acknowledgement. An inactive historical record can be legitimate.

If these checks become a new benchmark definition, freeze it before running
both versions. Compare both versions on that same definition; changing the judge
or suite only for the candidate would invalidate the comparison.

### Validate metric behavior independently of model quality

Use a controlled example with two tasks and three attempts each. These are
**teaching fixtures, not measured Agent improvements**:

| Task | Baseline attempts 1–3 | Candidate attempts 1–3 |
| --- | --- | --- |
| Update preference | fail, pass, fail | pass, pass, pass |
| Recall a saved fact | pass, pass, pass | pass, pass, pass |

With all attempts eligible and `k=3`, the expected scores are:

| Score | Baseline | Candidate | Interpretation |
| --- | --- | --- | --- |
| `pass_at_1` | 1/2 = 50% | 2/2 = 100% | First-attempt outcome improved. |
| `pass_at_k` | 2/2 = 100% | 2/2 = 100% | Both could already succeed at least once. |
| `pass_pow_k` | 1/2 = 50% | 2/2 = 100% | Repeated execution became consistent. |
| `attempt_success_rate` | 4/6 | 6/6 | More executions succeeded. |
| `coverage.pass_at_k` | 100% | 100% | Improvement did not come from exclusion. |

As a negative control, change the first update attempt to an ineligible
evaluation failure. Baseline `pass_at_1` becomes 100% with only 50% task coverage,
and `pass_at_k`/`pass_pow_k` also have 50% coverage. That apparent success gain
comes entirely from losing evidence. This is why coverage is part of acceptance.

Existing automated tests exercise these mechanisms:

| Verification layer | Existing tests |
| --- | --- |
| Fixed-k outcomes, eligibility, percentiles, first-success cost, missing usage | `benchmark/tests/test_metrics.py` |
| Hard-gate failures, timeout eligibility, missing evidence, judge behavior | `benchmark/tests/test_runtask.py` |
| Memory setup and deterministic memory assertions | `benchmark/tests/test_reset.py` |
| Attempt/run score mapping, idempotent publication, incomplete uploads | `benchmark/tests/test_langfuse_reporter.py` |

Run from the repository root using the pinned Docker environment:

```bash
make check
bash scripts/run_tests_in_docker.sh --suite benchmark-python
```

The quick profile only samples benchmark tests; the focused `benchmark-python`
suite is needed for the metric and publisher tests above. Publisher unit tests
use a fake client: they do not prove that a live Langfuse deployment ingested
the scores correctly.

### Run a current baseline and candidate

Prerequisites: working Docker, initialized repository submodules, configured
benchmark Agent/Judge credentials, and an isolated benchmark Agent configuration.
Use the [quickstart](./quickstart.md) for setup. Do not reuse a personal Agent's
memory. Keep configuration files and credentials out of commits.

From `benchmark/`, first record a baseline on two tasks with three attempts each:

```bash
uv run python -m runner run \
  --suite suites/memory_v1.json \
  --task-ids update_changed_preference,recall_saved_fact_after_setup \
  --repeats 3 \
  --auto-agent-setup \
  --agent-config /absolute/path/to/benchmark-agent.toml \
  --agent-model YOUR_FIXED_MODEL \
  --max-concurrency 1 \
  --run-id memory-update-baseline-01
```

Keep the judge enabled: `--no-judge` skips the semantic rubric and cannot prove
that this task performs the intended preference update. Inspect setup evidence
too: the old preference is created through an Agent prompt, so setup success
alone does not guarantee it was actually stored.

After inspecting the baseline, make one targeted candidate change. Repeat the
same command with `--run-id memory-update-candidate-01`, using the candidate's
actual code/configuration/image and keeping other settings fixed. Verify that
the daemon loaded the intended change; record the diff and image identity.
Use fresh run IDs for additional rounds and rebuild changed code into the image.

```bash
uv run python -m runner compare --runs \
  runs/memory-update-baseline-01 runs/memory-update-candidate-01

uv run python -m runner publish-langfuse \
  --run-dir runs/memory-update-baseline-01
uv run python -m runner publish-langfuse \
  --run-dir runs/memory-update-candidate-01
```

Read `metrics.json` and the per-attempt evidence as well as the comparison output.
In Langfuse, open the `memory_v1` Dataset and the two Experiment Runs, match item
IDs, and verify `benchmark.success`, eligibility, and tool counts against
`results.jsonl`. Compare run scores against `metrics.json`. Check missing metrics
remain missing. A publication retry should preserve item counts and scores.

The scheduled workflow runs the runnable catalog; its dispatch inputs currently
select profile and suite, not task IDs or repeat overrides. Use the CLI above for
this focused experiment rather than assuming a manual dispatch changes k.

### Acceptance criteria

Agree on the criteria before inspecting the candidate results:

1. All six planned attempts have usable evidence and matching comparison metadata.
2. The update task satisfies the existing assertions and rubric on all three
   candidate attempts, with an inspected trace showing the intended behavior.
3. Fresh-conversation recall and active-memory checks confirm the new preference
   without an active conflict. Otherwise only the interaction contract is verified.
4. The saved-fact control does not regress. Also run relevant update, forget,
   rule, and ephemeral-memory tasks before claiming broader memory improvement.
5. Review task wall time and total tokens on comparable successful attempts.
   For this pilot, a team could preselect a 20% median budget increase as a review
   threshold; this is a proposed guardrail, not an existing CI rule. Extra work
   may be justified to make an incorrect update correct.
6. Repeat independent baseline/candidate rounds and evaluate untouched preference
   variations before a release decision. Two tasks cannot establish general
   reliability, even when every attempt passes.

If the current baseline already passes, there is no demonstrated improvement
opportunity in this case. Keep it as a regression/control case and select a
different measured failure. Do not intentionally weaken the baseline to claim
a product gain.

## 5. Experiment record and next engineering steps

Use this record in the optimization PR or review document:

| Field | Required evidence |
| --- | --- |
| Problem and hypothesis | User-visible failure, representative trace, proposed cause |
| Experiment axis | Exact code, skill, model, or configuration change |
| Comparison identity | Run IDs, code/image identity, suite/workload hash, k, judge, platform, concurrency |
| Measurement quality | Planned vs observed attempts, eligibility, numeric field counts |
| Quality result | Task-level flips, first-attempt success, repeated success, rubric changes |
| Efficiency result | Matched-success latency/tokens and overall failure/resource totals |
| Product outcome | Independent state/recall evidence, not only a benchmark score |
| Generalization | Untouched cases and broader regression results |
| Decision | Accept, reject, or inconclusive, with limitations |

Recommended follow-up work is an automated compatible-baseline report with
coverage checks and task-level deltas, followed by empirically calibrated
regression thresholds. Add explicit retry/cost instrumentation only when those
measurements are needed for a decision; their current empty fields cannot guide
optimization. Use `make check-full` for changes spanning components or before
merging, as required by the contribution guide.

Implementation references:
[task outcomes](../../benchmark/runner/runtask.py),
[metric aggregation](../../benchmark/runner/metrics.py),
[Langfuse score mapping](../../benchmark/runner/langfuse_reporter.py),
[comparison command](../../benchmark/runner/compare.py),
[CI workflow](../../.github/workflows/benchmark.yml), and
[SkillOpt](../10-skillopt/README.md).
