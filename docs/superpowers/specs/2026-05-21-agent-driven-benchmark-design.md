# Agent-Driven Phone-Control Benchmark — Design

Date: 2026-05-21
Status: approved (pending implementation plan)

## Goal

Replace the existing benchmark (`scripts/aiden_benchmark.py` + `benchmark/suites/full_smoke.json`) with one whose purpose is to measure the Go agent's actual phone-control capability. Every task is driven by the agent's HTTP API (`POST /api/chat`), not by direct CLI tool invocations. Success is judged by what happened on the phone, not by which substrings appeared in stdout.

## Non-Goals (v1)

- Token / cost accounting (agent API doesn't expose usage yet).
- Parallel execution (single hardware rig).
- CI integration and visualized web reports.
- Replacing the low-level CLI smoke utility outside the benchmark.

## Architecture Overview

The Go agent (`src/agent/`) runs as a persistent HTTP daemon on the test rig. The benchmark runner is a Python client that sends tasks via the agent's API and collects results.

```
runner (Python, on the test rig)
  ├── POST /api/clear              (reset agent conversation state)
  ├── global reset                 (direct HID tool calls to return phone to home screen)
  ├── per-task setup               (optional direct tool calls; failures → skipped)
  ├── pre-screenshot               (POST /api/tools/invoke screenshot)
  ├── POST /api/chat {message}     ← the only system under test
  │     returns: ChatResponse{Response, History}
  │     History contains structured tool_call/tool_result messages
  │     Each input tool auto-returns a post-action screenshot
  ├── GET /api/history             (full structured trace)
  ├── hard assertions              (tool-call bounds, timeout, response exists)
  ├── LLM judge                    (artifact-only; can run offline)
  └── JSONL row + summary.md
```

Key differences from the old C++ agent approach:

- Agent is a long-running daemon, not a subprocess per task.
- Conversation isolation via `POST /api/clear` between tasks.
- Trace is structured JSON from `/api/history`, no regex parsing.
- Every input tool (keyboard_tap, mouse_click, touch_gesture, etc.) automatically captures a post-action screenshot — runner gets rich visual trace for free.
- No `--once` flag needed.

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
- `category` ∈ {`diagnostic`, `single_step`, `multi_step`}. Drives summary grouping.
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
│   ├── reset.py          # global reset + per-task setup (direct tool invocations)
│   ├── agent_client.py   # HTTP client for Go agent API (/api/chat, /api/clear, /api/history)
│   ├── capture.py        # pre-screenshot via /api/tools/invoke
│   ├── trace.py          # extract structured trace from /api/history response
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
            ├── pre.jpg
            ├── trace.json
            ├── history.json       # raw /api/history response
            ├── steps/             # per-step post-action screenshots extracted from tool results
            │   ├── step_01_screenshot.jpg
            │   ├── step_02_mouse_click.jpg
            │   └── ...
            └── judge.json
```

The CLI entry points:

- `python -m benchmark.runner.main run --suite ... [--agent-url http://localhost:8080] [--judge-config judge.conf] [--repeats N]`
- `python -m benchmark.runner.main rejudge --run-dir runs/<id>` — re-runs the judge over existing artifacts; never touches hardware.
- `python -m benchmark.runner.main compare --runs A B` — diffs pass/fail and efficiency between two runs.

The old `scripts/aiden_benchmark.py` is rewritten as a thin shim that delegates to `benchmark.runner.main run` so existing invocations keep working. The legacy `full_smoke.json` stays on disk but is marked deprecated in `docs/BENCHMARK.md`.

### Agent Invocation Detail

The Go agent runs as a persistent daemon (`src/agent/cmd/daemon/main.go`) exposing an HTTP API on `:8080`. The runner interacts via:

- `POST /api/clear` — reset conversation history between tasks (session isolation).
- `POST /api/chat {"message": "..."}` — send the task prompt; blocks until the agent finishes its tool-call loop and returns `ChatResponse{Response, History}`.
- `GET /api/history` — retrieve the full structured conversation (backup; `/api/chat` already returns history).
- `POST /api/tools/invoke` — direct tool invocation for setup/reset steps (e.g., `keyboard_tap HOME`).

The runner sets a wall-clock timeout on the HTTP request. If the agent hangs, the runner aborts the request and marks the task `timeout`. No subprocess management or SIGTERM needed.

### Trace Extraction

The `ChatResponse.History` field contains structured messages:

```jsonc
[
  { "type": "user", "content": "请打开系统设置", "timestamp": "..." },
  {
    "type": "tool_call",
    "tool_name": "screenshot",
    "tool_input": "{}",
    "timestamp": "...",
  },
  {
    "type": "tool_result",
    "tool_name": "screenshot",
    "content": "{\"width\":1080,...}",
    "timestamp": "...",
  },
  {
    "type": "tool_call",
    "tool_name": "mouse_click",
    "tool_input": "{\"x\":540,\"y\":1200}",
    "timestamp": "...",
  },
  {
    "type": "tool_result",
    "tool_name": "mouse_click",
    "content": "{\"width\":1080,...,\"action_output\":\"ok\"}",
    "timestamp": "...",
  },
  { "type": "assistant", "content": "已打开系统设置。", "timestamp": "..." },
]
```

The runner transforms this into `trace.json`:

```jsonc
{
  "tool_calls": [
    { "step": 1, "tool": "screenshot", "input": {}, "has_screenshot": true },
    {
      "step": 2,
      "tool": "mouse_click",
      "input": { "x": 540, "y": 1200 },
      "has_screenshot": true,
    },
  ],
  "final_response": "已打开系统设置。",
  "total_tool_calls": 2,
  "total_duration_ms": 3200,
}
```

No regex parsing needed — the history is already structured JSON. Post-action screenshots are embedded in tool_result messages (base64 JPEG); the runner extracts and saves them as `step_N.jpg` in the task artifact directory.

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

Token fields are `null` in v1 (agent API doesn't expose usage yet); `wall_ms` and `tool_calls` are the proxy efficiency signals.

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
    "tasks": 15,
    "passed": 11,
    "failed": 3,
    "skipped": 1,
    "judge_error": 0,
  },
}
```

`summary.md` is a human-readable digest: totals, per-category pass rate, rubric-step pass rate, median/p95 wall time and tool-call count, and a "Failures" section listing each failure with the judge's reason and the offending trace excerpt.

`compare` diffs two runs: which tasks flipped, efficiency deltas, manifest diff (model/suite/prompt version).

## Task Set v1

The task set exercises the Go agent's full tool set across varied real-world phone scenarios. The agent exposes these tools: `screenshot`, `keyboard_tap`, `keyboard_text`, `mouse_click`, `mouse_move`, `mouse_scroll`, `touch_gesture`, `audio_volume`, `shell`, `web_search`, `wikipedia`, `calculator`, `web_scraper`.

For phone-control benchmarking, the primary tools are: `screenshot`, `keyboard_tap`, `keyboard_text`, `mouse_click`, `mouse_scroll`, and `touch_gesture`. The web/shell tools are exercised in a separate "extended" category.

Main suite `phone_control_v1.json` — 15 tasks:

### single_step (7 tasks) — 考单次操作正确性

| ID                        | Goal                                     | Primary tools exercised                  | Rubric |
| ------------------------- | ---------------------------------------- | ---------------------------------------- | ------ |
| `open_settings`           | 从桌面打开系统设置                       | screenshot → mouse_click                 | 2      |
| `open_clock`              | 从桌面打开时钟 app                       | screenshot → mouse_click                 | 2      |
| `tap_back`                | 当前在设置子页（setup 进入），返回上一层 | screenshot → mouse_click 或 keyboard_tap | 2      |
| `type_in_search`          | 在搜索框输入指定短文字 "hello"           | screenshot → mouse_click → keyboard_text | 3      |
| `scroll_page_down`        | 向下滑动一屏                             | screenshot → touch_gesture(垂直 swipe)   | 2      |
| `swipe_between_pages`     | 在桌面左右滑动切换页面                   | screenshot → touch_gesture(水平 swipe)   | 2      |
| `open_notification_shade` | 从顶部下滑打开通知栏                     | screenshot → touch_gesture(从顶部向下)   | 2      |

### multi_step (8 tasks) — 考多步规划 + 工具组合

| ID                           | Goal                                                 | Primary tools exercised                                                   | Rubric |
| ---------------------------- | ---------------------------------------------------- | ------------------------------------------------------------------------- | ------ |
| `settings_search_bluetooth`  | 进设置 → 找到搜索 → 输入蓝牙 → 进入蓝牙页            | screenshot + mouse_click + keyboard_text + touch_gesture                  | 3      |
| `toggle_wifi`                | 进设置 → 找到 WiFi 开关 → 关闭再打开                 | screenshot → mouse_click 连续                                             | 4      |
| `add_clock_alarm`            | 时钟 → 新建 7:30 闹钟 → 保存                         | screenshot → mouse_click → keyboard_text                                  | 4      |
| `scroll_to_bottom`           | 在设置页反复滑动直到页面到底（agent 需判断何时停止） | screenshot → touch_gesture(循环)                                          | 3      |
| `type_long_mixed_text`       | 在输入框输入中英混合文字 "Aiden测试 benchmark-2026!" | screenshot → mouse_click → keyboard_text                                  | 3      |
| `select_all_and_delete`      | 在已有文字的输入框里全选并删除（清空）               | screenshot → keyboard_tap META+A → keyboard_tap BACKSPACE                 | 3      |
| `copy_paste_text`            | 输入文字 → 全选 → 复制 → 点另一输入框 → 粘贴         | keyboard_text → keyboard_tap META+A/C → mouse_click → keyboard_tap META+V | 4      |
| `find_and_tap_specific_item` | 在设置列表中滑动找到"关于手机"并点击                 | screenshot → touch_gesture(多次) → mouse_click                            | 3      |

### Tool coverage matrix

| Tool            | single_step | multi_step | Total                                             |
| --------------- | ----------- | ---------- | ------------------------------------------------- |
| `screenshot`    | 7/7         | 8/8        | 15 (every task, auto via post-action)             |
| `mouse_click`   | 4           | 7          | 11                                                |
| `touch_gesture` | 3           | 3          | 6                                                 |
| `keyboard_text` | 1           | 4          | 5                                                 |
| `keyboard_tap`  | 1           | 4          | 5                                                 |
| `mouse_scroll`  | 0           | 0          | 0 (covered by touch_gesture swipe; can add in v2) |

Diagnostic suite `perception_v1.json` is deferred to v2. The v1 focus is running all 15 main tasks end-to-end and evaluating execution correctness rate.

Setup conventions:

- All `multi_step` tasks start from home (relying on `global_reset` only), except `select_all_and_delete` and `copy_paste_text` which use a setup that opens a text field and pre-fills content.
- `tap_back` uses a setup that navigates into a settings sub-page.
- `find_and_tap_specific_item` relies on global_reset only (starts from home, agent must navigate to settings first).

`repeats` defaults to 1 in v1. The `--repeats N` CLI flag overrides per-run for consistency studies once everything else is stable.

## Failure Modes & Edge Cases

- **Agent HTTP request times out**: runner aborts the request after wall timeout, marks `timeout`. Agent daemon stays alive for next task.
- **Agent daemon is down**: runner pings `/api/tools` before starting; if unreachable, aborts the run with a clear error in `manifest.json`.
- **`/api/clear` fails**: runner retries once; if still failing, aborts the run (conversation state is polluted).
- **`global_reset` itself fails** (e.g. frame_service down): runner aborts the run before any task with a clear error in `manifest.json`.
- **Setup fails**: the offending task is `skipped`. Other tasks proceed.
- **Hard assertion fails**: status `failed`, judge skipped (saves cost; judge wouldn't be informative anyway).
- **Judge errors**: status `judge_error`, excluded from pass-rate denominator, surfaced separately.
- **Phone in unexpected state at start of run**: not detected automatically; mitigated by `global_reset`. A future enhancement could screenshot-diff against a known home-screen baseline.
- **`copy_paste_text` depends on META+C/V working**: some Android skins remap these shortcuts. If the test rig doesn't support them, this task is marked `skipped`.

## Out of Scope (v2 candidates)

- Token / cost accounting from agent API.
- Phone state baseline check before each run.
- Web-based result viewer.
- Suite-level parallelism across multiple rigs.
- Extended tool coverage (shell, web_search, web_scraper tasks).
- `mouse_scroll` specific tasks (currently covered by touch_gesture swipe).

## Migration Plan Summary

1. Build the new `benchmark/runner/` Python package with HTTP client for the Go agent API.
2. Wire `scripts/aiden_benchmark.py` as a thin shim delegating to the new runner.
3. Author `benchmark/suites/phone_control_v1.json` and `perception_v1.json`.
4. Update `docs/BENCHMARK.md` to document the new flow; mark `full_smoke.json` deprecated.
5. Run end-to-end on the rig; iterate task rubrics until judge output matches human review.
