# Agent-Driven Phone-Control Benchmark — Design

Date: 2026-05-21
Status: approved (pending implementation plan)

## Goal

Replace the existing benchmark (`scripts/aiden_benchmark.py` + `benchmark/suites/full_smoke.json`) with one whose purpose is to measure the agent's actual phone-control capability. Every task is driven by `agent_main`, not by direct CLI tool invocations. Success is judged by what happened on the phone, not by which substrings appeared in stdout.

## Non-Goals (v1)

- Per-step screenshots fed to the judge (terminal state only).
- Token / cost accounting parsed from the agent (no machine-readable usage line yet).
- Parallel execution (single hardware rig).
- CI integration and visualized web reports.
- Replacing the low-level CLI smoke utility outside the benchmark — the existing CLIs remain available for ad-hoc use.

## Architecture Overview

```
runner (Python, on the test rig)
  ├── global reset           (tool sequence → home screen)
  ├── per-task setup         (optional tool sequence, or sub agent_prompt; failures → skipped)
  ├── pre-screenshot         (frame_service_cli)
  ├── agent_main --mode=text --once   ← the only system under test
  │     stdin:  task prompt
  │     stdout: trace lines parsed by runner
  ├── post-screenshot
  ├── hard assertions        (returncode, tool-call bounds, timeout, post-shot exists)
  ├── LLM judge              (artifact-only; can run offline / on another host)
  └── JSONL row + summary.md
```

Every task is its own `agent_main` subprocess (LLM session is fresh). Phone state is reset between tasks. Runner is strictly serial.

## Suite / Task Format

Suites are JSON files under `benchmark/suites/`.

```jsonc
{
  "name": "phone_control_v1",
  "global_reset": {
    "tool_sequence": [
      { "tool": "keyboard_tap", "args": { "keys": ["ESCAPE"] } },
      { "tool": "keyboard_tap", "args": { "keys": ["HOME"] } },
      { "tool": "wait_ms", "args": { "ms": 500 } },
    ],
  },
  "tasks": [
    {
      "id": "open_settings",
      "category": "single_step",
      "description_for_judge": "Agent should open the system Settings app from the home screen.",
      "prompt": "请打开系统设置。",
      "setup": null,
      "rubric": [
        {
          "id": "in_settings",
          "check": "Post-screenshot shows the Settings app main page.",
        },
        {
          "id": "no_error_state",
          "check": "No error dialog or crash overlay is visible.",
        },
      ],
      "hard_assertions": {
        "min_tool_calls": 1,
        "max_tool_calls": 8,
        "must_complete_within_sec": 90,
        "post_screenshot_required": true,
      },
      "repeats": 1,
    },
  ],
}
```

Field conventions:

- `description_for_judge` (judge-facing) and `prompt` (agent-facing) are intentionally separate so the judge isn't biased by the same wording the agent saw.
- `category` ∈ {`diagnostic`, `single_step`, `multi_step`, `recovery`}. Drives summary grouping.
- `rubric[*]` are independent yes/no checks. Task-level pass requires all yes. The summary also reports rubric-step pass rate.
- `hard_assertions` short-circuit the judge: out-of-bounds runs are `failed` without consuming judge calls.
- `setup` is either `null`, a `tool_sequence`, or `{"type": "agent_prompt", "prompt": "...", "no_judge": true}`. Setup failures mark the task `skipped`, not `failed`.
- `repeats` defaults to 1. Higher values cover the "consistency" dimension by running the same task N times and reporting `passed/N`.

## Runner Layout

```
benchmark/
├── runner/
│   ├── __init__.py
│   ├── main.py           # CLI: run | rejudge | compare
│   ├── suite.py          # load + validate suite JSON
│   ├── reset.py          # global reset + per-task setup
│   ├── agent_session.py  # spawn agent_main, capture stdout, manage timeout
│   ├── capture.py        # pre/post screenshots
│   ├── trace.py          # parse stdout into structured tool-call list
│   ├── assertions.py     # hard assertions
│   ├── judge.py          # LLM judge (pure: takes artifact dir, writes judge.json)
│   ├── metrics.py        # efficiency aggregation
│   └── report.py         # JSONL + summary.md
├── suites/
│   ├── phone_control_v1.json
│   └── perception_v1.json
└── runs/
    └── <run_id>/
        ├── manifest.json
        ├── results.jsonl
        ├── summary.md
        └── tasks/<task_id>[/attempt_N]/
            ├── pre.png
            ├── post.png
            ├── agent_stdout.log
            ├── agent_stderr.log
            ├── trace.json
            └── judge.json
```

The CLI entry points:

- `python -m benchmark.runner.main run --suite ... [--config agent.conf] [--judge-config judge.conf] [--repeats N]`
- `python -m benchmark.runner.main rejudge --run-dir runs/<id>` — re-runs the judge over existing artifacts; never touches hardware.
- `python -m benchmark.runner.main compare --runs A B` — diffs pass/fail and efficiency between two runs.

The old `scripts/aiden_benchmark.py` is rewritten as a thin shim that delegates to `benchmark.runner.main run` so existing invocations keep working. The legacy `full_smoke.json` stays on disk but is marked deprecated in `docs/BENCHMARK.md`.

### Agent Invocation Detail

`agent_main --mode=text` currently loops on stdin. To make it benchmark-friendly we add `--once`: read one line from stdin, run the full tool-call loop until the LLM stops calling tools, then exit. The runner spawns `agent_main --mode=text --once`, writes the prompt + newline, closes stdin, waits with a wall-clock timeout, and SIGTERMs on timeout.

Implementation note: `--once` is a small addition in `src/agent_main.cpp` around the `MODE_TEXT` loop (`agent_main.cpp:615-630`). No other agent behavior changes.

### Trace Parsing

`agent_main` already prints `[tool] name(json_args)` per call and `[tools] Executing N tool call(s)...` per round. The runner regex-parses these into:

```jsonc
{
  "rounds": [
    {"round": 1, "tool_calls": [
      {"step": 1, "tool": "frame_capture_screenshot", "args": {...}, "duration_ms": null},
      {"step": 2, "tool": "hid_touch_click",          "args": {"x": 16384, "y": 4096}}
    ]}
  ],
  "final_response": "...",
  "total_tool_calls": 2,
  "llm_rounds": 1
}
```

If the regex turns out brittle, a follow-up small change to `agent_main.cpp` adds a single `[trace] {json}` line per tool call. Out of scope for v1 unless required.

## LLM Judge

A pure function over a task's artifact directory. One call per task per attempt.

Inputs to the judge:

- `description_for_judge`
- `rubric[*].check`
- `pre.png`, `post.png` (multimodal attachments)
- `trace.json` (truncated if very long)
- `final_response` extracted from agent stdout

Deliberately **not** given:

- The agent's `prompt` (avoids same-wording bias).
- Full raw stdout (noise).

Judge prompt template (frozen in code; bumped via `judge_prompt_version`):

```
You are evaluating whether a phone-control agent completed a task.

TASK GOAL: {description_for_judge}

The agent had access to a phone via screenshot+HID tools. Below are:
- Pre-screenshot: phone state before the agent acted
- Post-screenshot: phone state after the agent finished
- Tool trace: every action the agent took
- Agent's final reply: what the agent said it did

For each rubric item, answer ONLY "yes" or "no" with a one-sentence reason
grounded in the screenshots/trace. Do not be lenient. If the post-screenshot
does not clearly show the required state, answer "no".

RUBRIC:
1. {rubric[0].check}
2. {rubric[1].check}
...

Respond as JSON:
{
  "items": [
    {"id": "...", "verdict": "yes"|"no", "reason": "..."},
    ...
  ],
  "overall_notes": "any observations not captured by rubric"
}
```

Defaults and behavior:

- Judge provider/model defaults to a different vendor than the agent under test, configured via `--judge-config`.
- Judge errors (network/parse) yield `judge_error` status — distinct from `failed`, kept out of pass-rate denominator and called out separately in summary.
- Cache key: `sha256(artifact files + judge model + prompt version)`. Repeated `rejudge` over identical inputs is free.

## Metrics & Reporting

Per-task JSONL row (`results.jsonl`):

```jsonc
{
  "suite": "phone_control_v1",
  "run_id": "2026-05-21_153012",
  "task_id": "open_settings",
  "category": "single_step",
  "attempt": 1,
  "status": "passed", // passed | failed | skipped | judge_error | timeout | crashed
  "rubric": [
    { "id": "in_settings", "verdict": "yes", "reason": "..." },
    { "id": "no_error_state", "verdict": "yes", "reason": "..." },
  ],
  "rubric_pass_count": 2,
  "rubric_total": 2,
  "hard_assertions": {
    "min_tool_calls": true,
    "max_tool_calls": true,
    "timeout": false,
    "post_screenshot": true,
  },
  "metrics": {
    "wall_ms": 12300,
    "tool_calls": 4,
    "llm_rounds": 3,
    "screenshots_taken": 2,
    "approx_input_tokens": null,
    "approx_output_tokens": null,
  },
  "artifact_dir": "runs/2026-05-21_153012/tasks/open_settings",
  "started_at": "...",
  "finished_at": "...",
}
```

Token fields are `null` in v1 (agent stdout doesn't expose them yet); `wall_ms` and `tool_calls` are the proxy efficiency signals.

Per-run `manifest.json`:

```jsonc
{
  "run_id": "2026-05-21_153012",
  "git_sha": "1abe4db",
  "git_dirty": false,
  "suite_path": "benchmark/suites/phone_control_v1.json",
  "suite_sha256": "...",
  "agent_config": { "provider": "openrouter", "model": "..." },
  "judge_config": { "provider": "anthropic", "model": "..." },
  "judge_prompt_version": "v1",
  "host": { "hostname": "...", "os": "..." },
  "started_at": "...",
  "finished_at": "...",
  "totals": {
    "tasks": 10,
    "passed": 7,
    "failed": 2,
    "skipped": 1,
    "judge_error": 0,
  },
}
```

`summary.md` is a human-readable digest: totals, per-category pass rate, rubric-step pass rate, median/p95 wall time and tool-call count, and a "Failures" section listing each failure with the judge's reason and the offending trace excerpt.

`compare` diffs two runs: which tasks flipped, efficiency deltas, manifest diff (model/suite/prompt version).

## Task Set v1

Main suite `phone_control_v1.json` — 10 tasks:

| ID                           | category    | Goal (Chinese prompt)                                                     | Rubric items |
| ---------------------------- | ----------- | ------------------------------------------------------------------------- | ------------ |
| `open_settings`              | single_step | 打开系统设置                                                              | 2            |
| `open_clock`                 | single_step | 打开时钟 app                                                              | 2            |
| `tap_back_from_settings`     | single_step | 当前在设置某子页（setup 进入），返回上一层                                | 2            |
| `type_in_search`             | single_step | 在屏幕上的搜索框里输入指定文字                                            | 3            |
| `scroll_and_describe`        | single_step | 向下滚动一屏并说明页面变化                                                | 2            |
| `settings_search_bluetooth`  | multi_step  | 进设置 → 搜 Bluetooth → 进入蓝牙页                                        | 3            |
| `toggle_wifi_off_then_on`    | multi_step  | 进设置 → 关 WiFi → 再开回来                                               | 4            |
| `add_clock_alarm`            | multi_step  | 时钟 → 新建 7:30 闹钟 → 保存                                              | 4            |
| `recover_from_unknown_app`   | recovery    | 起点在任意 app 深层页（setup 推入），agent 需识别并回到桌面               | 2            |
| `recover_from_blocked_state` | recovery    | 起点是无法操作的全屏弹窗/锁屏（setup 制造），agent 需说明无法继续而非乱点 | 2            |

Diagnostic suite `perception_v1.json` (separate, does not contribute to main score):

| ID                  | Goal                                                             |
| ------------------- | ---------------------------------------------------------------- |
| `name_current_page` | "看截图，告诉我当前在哪个 app/页面"，judge 比对真值              |
| `find_button`       | "找到屏幕上的'保存'按钮，给出归一化坐标"，judge 看 post 是否点中 |

Setup conventions:

- All `multi_step` tasks start from home (relying on `global_reset` only).
- `recovery` tasks declare a `tool_sequence` setup that pushes the phone into the off-baseline state.
- The blocked-state task uses whichever overlay can be triggered reliably on the rig; if neither lock-screen nor permission dialog is reproducible, it degrades to a full-screen modal/dialog scenario.

`repeats` defaults to 1 in v1. The `--repeats N` CLI flag overrides per-run for consistency studies once everything else is stable.

## Failure Modes & Edge Cases

- **`agent_main --once` doesn't exit cleanly**: runner SIGTERMs after wall timeout, marks `timeout`. Exit code captured.
- **`global_reset` itself fails** (e.g. frame_service down): runner aborts the run before any task with a clear error in `manifest.json` rather than producing misleading task results.
- **Setup fails**: the offending task is `skipped`. Other tasks proceed.
- **Hard assertion fails**: status `failed`, judge skipped (saves cost; judge wouldn't be informative anyway).
- **Judge errors**: status `judge_error`, excluded from pass-rate denominator, surfaced separately.
- **Phone in unexpected state at start of run**: not detected automatically; mitigated by `global_reset`. A future enhancement could screenshot-diff against a known home-screen baseline before declaring a run valid.

## Out of Scope (v2 candidates)

- Per-step screenshots given to the judge.
- `[usage]` line in agent stdout for token/cost accounting.
- Phone state baseline check before each run.
- Web-based result viewer.
- Suite-level parallelism across multiple rigs.

## Migration Plan Summary

1. Add `--once` to `agent_main` (text mode). No behavior change in other modes.
2. Build the new `benchmark/runner/` Python package; wire `scripts/aiden_benchmark.py` as a shim.
3. Author `benchmark/suites/phone_control_v1.json` and `perception_v1.json`.
4. Update `docs/BENCHMARK.md` to document the new flow; mark `full_smoke.json` deprecated.
5. Run end-to-end on the rig; iterate task rubrics until judge output matches human review.
