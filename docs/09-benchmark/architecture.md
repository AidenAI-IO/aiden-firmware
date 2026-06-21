# Benchmark Architecture Design

## Design Principles

Agent benchmark uses an **HTTP API-driven + offline scoring** architecture, separating task execution from evaluation.

### Core Components

```text
┌─────────────┐
│   Runner    │  Python CLI, runs locally
│  (Python)   │
└──────┬──────┘
       │ HTTP
       ▼
┌─────────────┐
│ Go Agent    │  Resident daemon, listens on :8080
│  (Daemon)   │
└──────┬──────┘
       │ UDS/HID
       ▼
┌─────────────┐
│   Phone     │  Device under test
└─────────────┘
```

### Execution Flow

Execution flow for each task:

1. **Clear history** - `POST /api/clear` isolates task
2. **Global reset** - Call HID tools directly via `/api/tools/invoke` to return to home screen
3. **Per-task setup** (optional) - Construct specific starting state
4. **Pre screenshot** - `POST /api/tools/invoke screenshot`
5. **Execute task** - `POST /api/chat` sends prompt
6. **Hard assertions** - Check tool call count, timeout, response existence
7. **LLM judge** (optional) - Offline evaluation, can be re-run

### Scoring Mechanism

Uses **hard assertions + LLM judge** two-stage approach:

For multiple-choice questions like PersonaMem, tasks can declare `expected_answer` and `answer_format: "option_letter"`. The runner extracts `(a)`/`(b)`/`(c)`/`(d)` from the final response and performs deterministic scoring first; on mismatch, it fails directly; on match, it can continue to LLM judge to evaluate explanation quality.

#### Hard Assertions

Used for fast failure to avoid wasting judge costs:

- `response_required: true` - Agent must have a response
- `min_tool_calls: N` - At least N tool calls required
- `max_tool_calls: N` - At most N tool calls allowed
- `must_complete_within_sec: N` - Must complete within N seconds

Hard assertion failure → directly mark as `failed`, no judge call.

#### LLM Judge

Consumes artifacts (screenshots + trace), does not touch hardware, can be re-run offline:

**Input:**

- Task description
- Rubric (2-4 independent acceptance criteria)
- Pre/post screenshots
- Tool call trace
- Agent final response

**Output:**

- yes/no + reasoning for each rubric item
- Overall notes

**Judgment Rules:**

- All rubric items are yes → `passed`
- Any item is no → `failed`

**Caching Mechanism:**

Judge results are cached by `(pre, post, trace, description, final_response, model)` to avoid duplicate API calls.

### State Isolation

Each task executes independently without interference:

1. **Conversation history isolation** - `POST /api/clear` before each task
2. **Phone state isolation** - Global reset to home screen
3. **Per-task setup** - Optional task-specific starting state construction

### Structured Trace

`ChatResponse.History` directly returns structured message list:

```json
[
  { "type": "user", "content": "Open settings" },
  { "type": "tool_call", "tool_name": "screenshot", "tool_input": "{}" },
  {
    "type": "tool_result",
    "tool_name": "screenshot",
    "content": "{\"data\": \"...base64...\"}"
  },
  {
    "type": "tool_call",
    "tool_name": "mouse_click",
    "tool_input": "{\"x\": 540, \"y\": 960}"
  },
  { "type": "tool_result", "tool_name": "mouse_click", "content": "clicked" },
  { "type": "assistant", "content": "Settings opened" }
]
```

Each input tool automatically includes post-action screenshot.

### Cross-run Comparison

The `compare` command compares two runs:

- Which tasks flipped status (pass → fail or fail → pass)
- Latency differences (wall_ms changes exceeding threshold)
- Pass rate changes

Used for regression detection and performance monitoring.

## Directory Structure

```text
benchmark/
├── runner/              # Python package
│   ├── main.py          # CLI entry point
│   ├── suite.py         # Suite loading and validation
│   ├── runtask.py       # Task execution core
│   ├── agent_client.py  # Go agent HTTP client
│   ├── capture.py       # Screenshot capture
│   ├── trace.py         # Trace extraction
│   ├── assertions.py    # Hard assertions
│   ├── judge.py         # LLM judge
│   ├── metrics.py       # Metrics aggregation
│   ├── report.py        # JSONL report
│   ├── html_report.py   # HTML report
│   ├── rejudge.py       # Re-judge
│   ├── compare.py       # Cross-run comparison
│   └── reset.py         # Global reset + setup
├── suites/              # Task suites
│   ├── memory_v1.json
│   ├── full_smoke.json
│   └── phone_control_v1.json
└── runs/<run_id>/       # Run results
    ├── manifest.json    # Run metadata
    ├── results.jsonl    # One task per line
    └── tasks/<task_id>/
        ├── history.json # Raw conversation history
        ├── trace.json   # Structured trace
        ├── steps/       # Step-by-step screenshots
        └── judge.json   # Judge output
```

## Design Decisions

### Why HTTP API?

- Agent is already a resident daemon, no need to spawn subprocess
- HTTP interface is stable, no stdout parsing dependency
- Can execute remotely (runner on local machine, agent on device)

### Why Separate Execution and Scoring?

- **Re-runnable** - Can rejudge after judge failure or rubric adjustment without re-executing tasks
- **Cacheable** - Judge results for same input are cached, saving API costs
- **Offline** - Judge does not need hardware in the loop, can run in CI or locally

### Why LLM Judge?

- **Flexible** - Can evaluate complex visual and semantic goals
- **Explainable** - Each rubric item has yes/no + reasoning
- **Adjustable** - Just rejudge after modifying rubric, no code changes needed

### Why Not Docker?

- Hardware-in-the-loop (HID devices, frame_service socket) are all on test machine
- Dockerization only adds complexity without isolation benefits
- Python dependencies can be version-locked with uv

## Related Documentation

- [Quick Start](./README.md)
- [Detailed Guide](./quickstart.md)
