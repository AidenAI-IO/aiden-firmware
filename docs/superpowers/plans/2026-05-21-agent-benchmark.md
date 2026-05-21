# Agent-Driven Benchmark Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Python benchmark runner that sends tasks to the Go agent's HTTP API, collects structured traces and screenshots, runs hard assertions + LLM judge, and produces JSONL results with a human-readable summary.

**Architecture:** Python package at `benchmark/runner/` talks to the Go agent daemon at `http://localhost:8080`. Each task: clear history → reset phone → optional setup → pre-screenshot → POST /api/chat → extract trace → assert → judge → write artifacts.

**Tech Stack:** Python 3.10+, `httpx` (HTTP client), `anthropic` SDK (judge), `Pillow` (image handling), no framework.

---

## File Structure

| Path                                     | Responsibility                                |
| ---------------------------------------- | --------------------------------------------- |
| `benchmark/runner/__init__.py`           | Package marker                                |
| `benchmark/runner/__main__.py`           | `python -m benchmark.runner` entry            |
| `benchmark/runner/main.py`               | CLI (argparse): `run`, `rejudge`, `compare`   |
| `benchmark/runner/agent_client.py`       | HTTP client wrapping Go agent API             |
| `benchmark/runner/suite.py`              | Load + validate suite JSON                    |
| `benchmark/runner/reset.py`              | Global reset + per-task setup via tool invoke |
| `benchmark/runner/capture.py`            | Pre-screenshot via tool invoke                |
| `benchmark/runner/trace.py`              | Extract structured trace from history         |
| `benchmark/runner/assertions.py`         | Hard assertion checks                         |
| `benchmark/runner/judge.py`              | LLM judge (multimodal, rubric-based)          |
| `benchmark/runner/metrics.py`            | Aggregate efficiency metrics                  |
| `benchmark/runner/report.py`             | Write JSONL + summary.md + manifest.json      |
| `benchmark/runner/models.py`             | Shared dataclasses (TaskResult, Trace, etc.)  |
| `benchmark/suites/phone_control_v1.json` | Main suite (15 tasks)                         |
| `benchmark/requirements.txt`             | Python dependencies                           |
| `tests/benchmark/__init__.py`            | Test package                                  |
| `tests/benchmark/test_suite.py`          | Suite loading tests                           |
| `tests/benchmark/test_trace.py`          | Trace extraction tests                        |
| `tests/benchmark/test_assertions.py`     | Hard assertion tests                          |
| `tests/benchmark/test_agent_client.py`   | Agent client tests (mocked HTTP)              |

---

## Task 1: Python package skeleton + models

**Files:**

- Create: `benchmark/runner/__init__.py`
- Create: `benchmark/runner/__main__.py`
- Create: `benchmark/runner/models.py`
- Create: `benchmark/requirements.txt`
- Create: `tests/benchmark/__init__.py`

- [ ] **Step 1: Create requirements.txt**

```
httpx>=0.27
anthropic>=0.40
Pillow>=10.0
```

- [ ] **Step 2: Create package files**

`benchmark/runner/__init__.py`: empty file.

`benchmark/runner/__main__.py`:

```python
from benchmark.runner.main import cli

if __name__ == "__main__":
    cli()
```

- [ ] **Step 3: Create models.py with shared dataclasses**

```python
from __future__ import annotations
import dataclasses as dc
from typing import Any

@dc.dataclass
class ToolCall:
    step: int
    tool: str
    input: dict[str, Any]
    has_screenshot: bool = False

@dc.dataclass
class Trace:
    tool_calls: list[ToolCall]
    final_response: str
    total_tool_calls: int
    total_duration_ms: int

@dc.dataclass
class RubricVerdict:
    id: str
    verdict: str  # "yes" | "no"
    reason: str

@dc.dataclass
class HardAssertionResults:
    min_tool_calls: bool | None = None
    max_tool_calls: bool | None = None
    timeout: bool = True
    response_exists: bool = False

@dc.dataclass
class TaskResult:
    suite: str
    run_id: str
    task_id: str
    category: str
    attempt: int
    status: str  # passed|failed|skipped|judge_error|timeout
    rubric: list[RubricVerdict]
    rubric_pass_count: int = 0
    rubric_total: int = 0
    hard_assertions: HardAssertionResults | None = None
    metrics: dict[str, Any] = dc.field(default_factory=dict)
    artifact_dir: str = ""
    started_at: str = ""
    finished_at: str = ""
```

- [ ] **Step 4: Create test package marker**

`tests/benchmark/__init__.py`: empty file.

- [ ] **Step 5: Commit**

```bash
git add benchmark/runner/__init__.py benchmark/runner/__main__.py benchmark/runner/models.py benchmark/requirements.txt tests/benchmark/__init__.py
git commit -m "feat(benchmark): add runner package skeleton and models"
```

---

## Task 2: Suite loader and validation

**Files:**

- Create: `benchmark/runner/suite.py`
- Create: `tests/benchmark/test_suite.py`
- Create: `benchmark/suites/_example_minimal.json` (fixture for tests, kept in tree)

- [ ] **Step 1: Write failing test for valid suite loading**

`tests/benchmark/test_suite.py`:

```python
import json
import pytest
from pathlib import Path
from benchmark.runner.suite import load_suite, SuiteValidationError

FIXTURE = {
    "name": "test_suite",
    "global_reset": {"tool_sequence": [{"tool": "keyboard_tap", "args": {"keys": ["escape"]}}]},
    "tasks": [
        {
            "id": "open_settings",
            "category": "single_step",
            "description_for_judge": "Agent should open Settings.",
            "prompt": "请打开系统设置。",
            "rubric": [{"id": "in_settings", "check": "Post-screenshot shows Settings."}],
            "hard_assertions": {"min_tool_calls": 1, "max_tool_calls": 8, "must_complete_within_sec": 90},
        }
    ],
}

def test_load_suite_returns_parsed(tmp_path: Path):
    p = tmp_path / "s.json"
    p.write_text(json.dumps(FIXTURE), encoding="utf-8")
    suite = load_suite(p)
    assert suite.name == "test_suite"
    assert len(suite.tasks) == 1
    assert suite.tasks[0].id == "open_settings"
    assert suite.tasks[0].category == "single_step"
    assert suite.tasks[0].rubric[0].id == "in_settings"

def test_load_suite_missing_tasks_raises(tmp_path: Path):
    p = tmp_path / "s.json"
    p.write_text(json.dumps({"name": "x"}), encoding="utf-8")
    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_invalid_category_raises(tmp_path: Path):
    bad = {**FIXTURE, "tasks": [{**FIXTURE["tasks"][0], "category": "weird"}]}
    p = tmp_path / "s.json"
    p.write_text(json.dumps(bad), encoding="utf-8")
    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_duplicate_ids_raise(tmp_path: Path):
    bad = {**FIXTURE, "tasks": [FIXTURE["tasks"][0], FIXTURE["tasks"][0]]}
    p = tmp_path / "s.json"
    p.write_text(json.dumps(bad), encoding="utf-8")
    with pytest.raises(SuiteValidationError):
        load_suite(p)
```

- [ ] **Step 2: Run test to verify it fails**

```
pytest tests/benchmark/test_suite.py -v
```

Expected: ImportError (suite module not found).

- [ ] **Step 3: Implement suite.py**

`benchmark/runner/suite.py`:

```python
from __future__ import annotations
import dataclasses as dc
import hashlib
import json
from pathlib import Path
from typing import Any

VALID_CATEGORIES = {"diagnostic", "single_step", "multi_step"}

class SuiteValidationError(ValueError):
    pass

@dc.dataclass
class RubricItem:
    id: str
    check: str

@dc.dataclass
class HardAssertions:
    min_tool_calls: int = 0
    max_tool_calls: int = 50
    must_complete_within_sec: int = 180
    response_required: bool = True

@dc.dataclass
class TaskSpec:
    id: str
    category: str
    description_for_judge: str
    prompt: str
    rubric: list[RubricItem]
    hard_assertions: HardAssertions
    setup: dict[str, Any] | None = None
    repeats: int = 1

@dc.dataclass
class Suite:
    name: str
    global_reset: dict[str, Any]
    tasks: list[TaskSpec]
    sha256: str
    source_path: Path

def load_suite(path: Path) -> Suite:
    raw_bytes = Path(path).read_bytes()
    sha = hashlib.sha256(raw_bytes).hexdigest()
    try:
        data = json.loads(raw_bytes.decode("utf-8"))
    except json.JSONDecodeError as e:
        raise SuiteValidationError(f"invalid JSON: {e}") from e
    if not isinstance(data.get("tasks"), list):
        raise SuiteValidationError("suite must contain a 'tasks' array")
    seen = set()
    tasks: list[TaskSpec] = []
    for raw in data["tasks"]:
        tid = raw.get("id")
        if not tid or tid in seen:
            raise SuiteValidationError(f"missing or duplicate task id: {tid!r}")
        seen.add(tid)
        cat = raw.get("category")
        if cat not in VALID_CATEGORIES:
            raise SuiteValidationError(f"task {tid}: invalid category {cat!r}")
        rubric_raw = raw.get("rubric") or []
        if not rubric_raw:
            raise SuiteValidationError(f"task {tid}: empty rubric")
        rubric = [RubricItem(id=r["id"], check=r["check"]) for r in rubric_raw]
        ha = raw.get("hard_assertions") or {}
        hard = HardAssertions(
            min_tool_calls=int(ha.get("min_tool_calls", 0)),
            max_tool_calls=int(ha.get("max_tool_calls", 50)),
            must_complete_within_sec=int(ha.get("must_complete_within_sec", 180)),
            response_required=bool(ha.get("response_required", True)),
        )
        tasks.append(TaskSpec(
            id=tid, category=cat,
            description_for_judge=raw["description_for_judge"],
            prompt=raw["prompt"],
            rubric=rubric, hard_assertions=hard,
            setup=raw.get("setup"),
            repeats=int(raw.get("repeats", 1)),
        ))
    return Suite(
        name=data.get("name", Path(path).stem),
        global_reset=data.get("global_reset") or {},
        tasks=tasks,
        sha256=sha,
        source_path=Path(path),
    )
```

- [ ] **Step 4: Run tests, expect PASS**

```
pytest tests/benchmark/test_suite.py -v
```

Expected: 4 passed.

- [ ] **Step 5: Commit**

```bash
git add benchmark/runner/suite.py tests/benchmark/test_suite.py
git commit -m "feat(benchmark): suite loader with validation"
```

---

## Task 3: Agent HTTP client

**Files:**

- Create: `benchmark/runner/agent_client.py`
- Create: `tests/benchmark/test_agent_client.py`

- [ ] **Step 1: Write failing tests using httpx mock transport**

`tests/benchmark/test_agent_client.py`:

```python
import httpx
import pytest
from benchmark.runner.agent_client import AgentClient, AgentTimeoutError

def make_client(handler):
    transport = httpx.MockTransport(handler)
    return AgentClient(base_url="http://test", transport=transport)

def test_clear_history_posts_correct_path():
    seen = {}
    def handler(req: httpx.Request) -> httpx.Response:
        seen["url"] = str(req.url)
        seen["method"] = req.method
        return httpx.Response(200, json={"status": "ok"})
    client = make_client(handler)
    client.clear_history()
    assert seen["method"] == "POST"
    assert seen["url"].endswith("/api/clear")

def test_chat_returns_response_and_history():
    history = [{"type": "assistant", "content": "done"}]
    def handler(req: httpx.Request) -> httpx.Response:
        assert req.method == "POST"
        assert req.url.path == "/api/chat"
        body = req.read().decode()
        assert "请打开" in body
        return httpx.Response(200, json={"response": "ok", "history": history})
    client = make_client(handler)
    resp = client.chat("请打开设置")
    assert resp.response == "ok"
    assert resp.history == history

def test_chat_timeout_raises():
    def handler(req: httpx.Request) -> httpx.Response:
        raise httpx.ReadTimeout("timeout", request=req)
    client = make_client(handler)
    with pytest.raises(AgentTimeoutError):
        client.chat("hi", timeout_sec=1)

def test_invoke_tool_returns_output():
    def handler(req: httpx.Request) -> httpx.Response:
        assert req.url.path == "/api/tools/keyboard_tap"
        body = req.read().decode()
        assert "escape" in body
        return httpx.Response(200, json={"output": "{}", "is_error": False, "duration_ms": 12})
    client = make_client(handler)
    out = client.invoke_tool("keyboard_tap", {"keys": ["escape"]})
    assert out.is_error is False
    assert out.duration_ms == 12

def test_health_returns_true_when_tools_endpoint_ok():
    def handler(req: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"tools": []})
    client = make_client(handler)
    assert client.health() is True
```

- [ ] **Step 2: Run tests, expect ImportError**

```
pytest tests/benchmark/test_agent_client.py -v
```

- [ ] **Step 3: Implement agent_client.py**

`benchmark/runner/agent_client.py`:

```python
from __future__ import annotations
import dataclasses as dc
import json
from typing import Any
import httpx

class AgentTimeoutError(TimeoutError):
    pass

class AgentRequestError(RuntimeError):
    pass

@dc.dataclass
class ChatResponse:
    response: str
    history: list[dict[str, Any]]

@dc.dataclass
class ToolInvokeResult:
    output: str
    is_error: bool
    duration_ms: int

class AgentClient:
    def __init__(
        self,
        base_url: str = "http://localhost:8080",
        transport: httpx.BaseTransport | None = None,
        default_timeout_sec: int = 180,
    ):
        self._client = httpx.Client(base_url=base_url, transport=transport, timeout=default_timeout_sec)
        self._default_timeout = default_timeout_sec

    def health(self) -> bool:
        try:
            r = self._client.get("/api/tools", timeout=5)
            return r.status_code == 200
        except httpx.HTTPError:
            return False

    def clear_history(self) -> None:
        r = self._client.post("/api/clear", timeout=10)
        r.raise_for_status()

    def chat(self, message: str, timeout_sec: int | None = None) -> ChatResponse:
        try:
            r = self._client.post(
                "/api/chat",
                json={"message": message},
                timeout=timeout_sec or self._default_timeout,
            )
        except httpx.ReadTimeout as e:
            raise AgentTimeoutError(str(e)) from e
        except httpx.HTTPError as e:
            raise AgentRequestError(str(e)) from e
        if r.status_code != 200:
            raise AgentRequestError(f"chat returned {r.status_code}: {r.text}")
        body = r.json()
        return ChatResponse(response=body.get("response", ""), history=body.get("history", []))

    def invoke_tool(self, name: str, args: dict[str, Any]) -> ToolInvokeResult:
        r = self._client.post(
            f"/api/tools/{name}",
            json={"input": args},
            timeout=30,
        )
        if r.status_code != 200:
            raise AgentRequestError(f"invoke {name} returned {r.status_code}: {r.text}")
        body = r.json()
        return ToolInvokeResult(
            output=body.get("output", ""),
            is_error=bool(body.get("is_error")),
            duration_ms=int(body.get("duration_ms", 0)),
        )

    def close(self) -> None:
        self._client.close()
```

- [ ] **Step 4: Run tests, expect PASS**

```
pytest tests/benchmark/test_agent_client.py -v
```

- [ ] **Step 5: Commit**

```bash
git add benchmark/runner/agent_client.py tests/benchmark/test_agent_client.py
git commit -m "feat(benchmark): agent HTTP client with timeout and tool invoke"
```

---

## Task 4: Trace extraction from history

**Files:**

- Create: `benchmark/runner/trace.py`
- Create: `tests/benchmark/test_trace.py`

- [ ] **Step 1: Write failing tests**

`tests/benchmark/test_trace.py`:

```python
from benchmark.runner.trace import extract_trace, extract_step_screenshots

HISTORY = [
    {"type": "user", "content": "请打开设置"},
    {"type": "tool_call", "tool_name": "screenshot", "tool_input": "{}"},
    {"type": "tool_result", "tool_name": "screenshot",
     "content": '{"width":1080,"height":1920,"format":"jpeg","size":4,"data":"AAAA"}'},
    {"type": "tool_call", "tool_name": "mouse_click", "tool_input": '{"x":540,"y":1200}'},
    {"type": "tool_result", "tool_name": "mouse_click",
     "content": '{"width":1080,"height":1920,"format":"jpeg","size":4,"data":"BBBB","action_output":"ok"}'},
    {"type": "assistant", "content": "已打开。"},
]

def test_extract_trace_collects_tool_calls_in_order():
    trace = extract_trace(HISTORY)
    assert trace.total_tool_calls == 2
    assert trace.tool_calls[0].tool == "screenshot"
    assert trace.tool_calls[1].tool == "mouse_click"
    assert trace.tool_calls[1].input == {"x": 540, "y": 1200}
    assert trace.final_response == "已打开。"

def test_extract_trace_marks_has_screenshot_when_data_present():
    trace = extract_trace(HISTORY)
    assert trace.tool_calls[0].has_screenshot is True
    assert trace.tool_calls[1].has_screenshot is True

def test_extract_step_screenshots_returns_base64_payloads():
    shots = extract_step_screenshots(HISTORY)
    assert len(shots) == 2
    assert shots[0] == ("screenshot", "AAAA")
    assert shots[1] == ("mouse_click", "BBBB")

def test_extract_trace_handles_malformed_input_gracefully():
    history = [
        {"type": "tool_call", "tool_name": "screenshot", "tool_input": "not-json"},
        {"type": "tool_result", "tool_name": "screenshot", "content": "also-not-json"},
        {"type": "assistant", "content": ""},
    ]
    trace = extract_trace(history)
    assert trace.total_tool_calls == 1
    assert trace.tool_calls[0].input == {}
    assert trace.tool_calls[0].has_screenshot is False
```

- [ ] **Step 2: Run tests, expect ImportError**

```
pytest tests/benchmark/test_trace.py -v
```

- [ ] **Step 3: Implement trace.py**

`benchmark/runner/trace.py`:

```python
from __future__ import annotations
import json
from typing import Any
from benchmark.runner.models import ToolCall, Trace

def _safe_loads(s: str) -> Any:
    try:
        return json.loads(s) if s else {}
    except (json.JSONDecodeError, TypeError):
        return None

def extract_trace(history: list[dict[str, Any]]) -> Trace:
    tool_calls: list[ToolCall] = []
    final_response = ""
    step = 0
    pending: dict[str, Any] | None = None
    for msg in history:
        mtype = msg.get("type")
        if mtype == "tool_call":
            step += 1
            args = _safe_loads(msg.get("tool_input", "")) or {}
            if not isinstance(args, dict):
                args = {}
            pending = {"step": step, "tool": msg.get("tool_name", ""), "input": args, "has_screenshot": False}
        elif mtype == "tool_result" and pending is not None:
            content = _safe_loads(msg.get("content", ""))
            if isinstance(content, dict) and content.get("data"):
                pending["has_screenshot"] = True
            tool_calls.append(ToolCall(**pending))
            pending = None
        elif mtype == "assistant":
            final_response = msg.get("content", "")
    if pending is not None:
        tool_calls.append(ToolCall(**pending))
    return Trace(
        tool_calls=tool_calls,
        final_response=final_response,
        total_tool_calls=len(tool_calls),
        total_duration_ms=0,
    )

def extract_step_screenshots(history: list[dict[str, Any]]) -> list[tuple[str, str]]:
    """Returns list of (tool_name, base64_jpeg) pairs from tool_result messages."""
    result: list[tuple[str, str]] = []
    last_tool_name = ""
    for msg in history:
        if msg.get("type") == "tool_call":
            last_tool_name = msg.get("tool_name", "")
        elif msg.get("type") == "tool_result":
            content = _safe_loads(msg.get("content", ""))
            if isinstance(content, dict):
                data = content.get("data")
                if data:
                    result.append((last_tool_name or msg.get("tool_name", ""), data))
    return result
```

- [ ] **Step 4: Run tests, expect PASS**

```
pytest tests/benchmark/test_trace.py -v
```

- [ ] **Step 5: Commit**

```bash
git add benchmark/runner/trace.py tests/benchmark/test_trace.py
git commit -m "feat(benchmark): trace extraction from agent history"
```

---

## Task 5: Hard assertions

**Files:**

- Create: `benchmark/runner/assertions.py`
- Create: `tests/benchmark/test_assertions.py`

- [ ] **Step 1: Write failing tests**

`tests/benchmark/test_assertions.py`:

```python
from benchmark.runner.assertions import evaluate_hard_assertions, AssertionOutcome
from benchmark.runner.suite import HardAssertions
from benchmark.runner.models import Trace, ToolCall

def make_trace(n: int, response: str = "ok") -> Trace:
    return Trace(
        tool_calls=[ToolCall(step=i+1, tool="x", input={}) for i in range(n)],
        final_response=response, total_tool_calls=n, total_duration_ms=0,
    )

def test_within_bounds_passes():
    spec = HardAssertions(min_tool_calls=1, max_tool_calls=10)
    out = evaluate_hard_assertions(make_trace(3), spec, timed_out=False)
    assert out.all_passed is True

def test_below_min_tool_calls_fails():
    spec = HardAssertions(min_tool_calls=2, max_tool_calls=10)
    out = evaluate_hard_assertions(make_trace(1), spec, timed_out=False)
    assert out.all_passed is False
    assert out.results.min_tool_calls is False

def test_above_max_tool_calls_fails():
    spec = HardAssertions(min_tool_calls=1, max_tool_calls=2)
    out = evaluate_hard_assertions(make_trace(5), spec, timed_out=False)
    assert out.all_passed is False
    assert out.results.max_tool_calls is False

def test_timeout_fails():
    spec = HardAssertions(min_tool_calls=0, max_tool_calls=10)
    out = evaluate_hard_assertions(make_trace(3), spec, timed_out=True)
    assert out.all_passed is False
    assert out.results.timeout is False

def test_missing_response_fails_when_required():
    spec = HardAssertions(min_tool_calls=0, max_tool_calls=10, response_required=True)
    out = evaluate_hard_assertions(make_trace(1, response=""), spec, timed_out=False)
    assert out.all_passed is False
    assert out.results.response_exists is False
```

- [ ] **Step 2: Run tests, expect ImportError**

```
pytest tests/benchmark/test_assertions.py -v
```

- [ ] **Step 3: Implement assertions.py**

`benchmark/runner/assertions.py`:

```python
from __future__ import annotations
import dataclasses as dc
from benchmark.runner.models import HardAssertionResults, Trace
from benchmark.runner.suite import HardAssertions

@dc.dataclass
class AssertionOutcome:
    all_passed: bool
    results: HardAssertionResults

def evaluate_hard_assertions(trace: Trace, spec: HardAssertions, timed_out: bool) -> AssertionOutcome:
    results = HardAssertionResults(
        min_tool_calls=trace.total_tool_calls >= spec.min_tool_calls,
        max_tool_calls=trace.total_tool_calls <= spec.max_tool_calls,
        timeout=not timed_out,
        response_exists=bool(trace.final_response) if spec.response_required else True,
    )
    all_passed = (
        results.min_tool_calls
        and results.max_tool_calls
        and results.timeout
        and results.response_exists
    )
    return AssertionOutcome(all_passed=bool(all_passed), results=results)
```

- [ ] **Step 4: Run tests, expect PASS**

```
pytest tests/benchmark/test_assertions.py -v
```

- [ ] **Step 5: Commit**

```bash
git add benchmark/runner/assertions.py tests/benchmark/test_assertions.py
git commit -m "feat(benchmark): hard assertion evaluator"
```

---

## Task 6: Reset and setup runner

**Files:**

- Create: `benchmark/runner/reset.py`

- [ ] **Step 1: Implement reset.py**

`benchmark/runner/reset.py`:

```python
from __future__ import annotations
import time
from typing import Any
from benchmark.runner.agent_client import AgentClient

class ResetError(RuntimeError):
    pass

def run_tool_sequence(client: AgentClient, sequence: list[dict[str, Any]]) -> None:
    for step in sequence:
        tool = step.get("tool")
        args = step.get("args") or {}
        if tool == "wait_ms":
            time.sleep(int(args.get("ms", 0)) / 1000.0)
            continue
        if not tool:
            raise ResetError(f"reset step missing 'tool': {step!r}")
        result = client.invoke_tool(tool, args)
        if result.is_error:
            raise ResetError(f"tool {tool} failed: {result.output}")

def global_reset(client: AgentClient, suite_global_reset: dict[str, Any]) -> None:
    seq = suite_global_reset.get("tool_sequence") or []
    run_tool_sequence(client, seq)

def per_task_setup(client: AgentClient, setup: dict[str, Any] | None) -> None:
    if setup is None:
        return
    seq = setup.get("tool_sequence")
    if seq:
        run_tool_sequence(client, seq)
        return
    if setup.get("type") == "agent_prompt":
        prompt = setup.get("prompt")
        if not prompt:
            raise ResetError(f"agent_prompt setup missing prompt: {setup!r}")
        timeout = int(setup.get("timeout_sec", 90))
        try:
            from benchmark.runner.agent_client import AgentTimeoutError
            client.chat(prompt, timeout_sec=timeout)
        except AgentTimeoutError as e:
            raise ResetError(f"setup agent_prompt timed out: {e}") from e
        # Clear the setup conversation so it does not pollute the actual task chat.
        client.clear_history()
        return
    raise ResetError(f"unsupported setup form: {setup!r}")
```

- [ ] **Step 2: Commit**

```bash
git add benchmark/runner/reset.py
git commit -m "feat(benchmark): global reset and per-task setup via tool invoke"
```

---

## Task 7: Pre-screenshot capture

**Files:**

- Create: `benchmark/runner/capture.py`

- [ ] **Step 1: Implement capture.py**

`benchmark/runner/capture.py`:

```python
from __future__ import annotations
import base64
import json
from pathlib import Path
from benchmark.runner.agent_client import AgentClient

class CaptureError(RuntimeError):
    pass

def take_screenshot(client: AgentClient, out_path: Path) -> tuple[int, int]:
    """Invoke the screenshot tool and write the JPEG bytes to out_path. Returns (width, height)."""
    result = client.invoke_tool("screenshot", {})
    if result.is_error:
        raise CaptureError(f"screenshot failed: {result.output}")
    try:
        payload = json.loads(result.output)
    except json.JSONDecodeError as e:
        raise CaptureError(f"screenshot returned non-JSON: {result.output[:120]}") from e
    data = payload.get("data")
    if not data:
        raise CaptureError("screenshot returned no data field")
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_bytes(base64.b64decode(data))
    return int(payload.get("width", 0)), int(payload.get("height", 0))

def write_step_screenshot(out_path: Path, base64_data: str) -> None:
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_bytes(base64.b64decode(base64_data))
```

- [ ] **Step 2: Commit**

```bash
git add benchmark/runner/capture.py
git commit -m "feat(benchmark): pre-screenshot and per-step screenshot writers"
```

---

## Task 8: LLM judge

**Files:**

- Create: `benchmark/runner/judge.py`

- [ ] **Step 1: Implement judge.py**

`benchmark/runner/judge.py`:

```python
from __future__ import annotations
import base64
import dataclasses as dc
import hashlib
import json
import os
from pathlib import Path
from typing import Any
import anthropic

from benchmark.runner.models import RubricVerdict
from benchmark.runner.suite import RubricItem

JUDGE_PROMPT_VERSION = "v1"

@dc.dataclass
class JudgeConfig:
    provider: str = "anthropic"
    model: str = "claude-sonnet-4-6"
    api_key_env: str = "ANTHROPIC_API_KEY"

@dc.dataclass
class JudgeOutput:
    verdicts: list[RubricVerdict]
    overall_notes: str
    cache_key: str
    raw_response: str

JUDGE_TEMPLATE = """You are evaluating whether a phone-control agent completed a task.

TASK GOAL: {description}

The agent had access to a phone via screenshot+HID tools. Below are:
- Pre-screenshot: phone state before the agent acted
- Post-screenshot: phone state after the agent finished (last step screenshot)
- Tool trace: every action the agent took
- Agent's final reply: what the agent said it did

For each rubric item, answer ONLY "yes" or "no" with a one-sentence reason
grounded in the screenshots/trace. Do not be lenient. If the post-screenshot
does not clearly show the required state, answer "no".

RUBRIC:
{rubric_lines}

Respond as JSON only, no prose:
{{
  "items": [{{"id": "...", "verdict": "yes" or "no", "reason": "..."}}, ...],
  "overall_notes": "..."
}}"""

def _read_image_b64(p: Path) -> str:
    return base64.b64encode(p.read_bytes()).decode("ascii")

def _cache_key(pre: Path, post: Path, trace_json: str, rubric: list[RubricItem],
               description: str, model: str) -> str:
    h = hashlib.sha256()
    h.update(pre.read_bytes())
    h.update(post.read_bytes())
    h.update(trace_json.encode("utf-8"))
    h.update(description.encode("utf-8"))
    for r in rubric:
        h.update(r.id.encode()); h.update(r.check.encode())
    h.update(model.encode())
    h.update(JUDGE_PROMPT_VERSION.encode())
    return h.hexdigest()

def judge_task(
    description: str,
    rubric: list[RubricItem],
    pre_screenshot: Path,
    post_screenshot: Path,
    trace: dict[str, Any],
    final_response: str,
    cfg: JudgeConfig,
    cache_dir: Path | None = None,
) -> JudgeOutput:
    trace_json = json.dumps(trace, ensure_ascii=False, sort_keys=True)
    key = _cache_key(pre_screenshot, post_screenshot, trace_json, rubric, description, cfg.model)
    if cache_dir is not None:
        cached = cache_dir / f"{key}.json"
        if cached.exists():
            data = json.loads(cached.read_text("utf-8"))
            verdicts = [RubricVerdict(**v) for v in data["verdicts"]]
            return JudgeOutput(verdicts=verdicts, overall_notes=data["overall_notes"],
                               cache_key=key, raw_response=data["raw_response"])
    client = anthropic.Anthropic(api_key=os.environ[cfg.api_key_env])
    rubric_lines = "\n".join(f"{i+1}. {{\"id\": \"{r.id}\", \"check\": \"{r.check}\"}}"
                              for i, r in enumerate(rubric))
    prompt = JUDGE_TEMPLATE.format(
        description=description, rubric_lines=rubric_lines,
    )
    user_content: list[dict[str, Any]] = [
        {"type": "text", "text": prompt},
        {"type": "text", "text": "PRE-SCREENSHOT:"},
        {"type": "image", "source": {"type": "base64", "media_type": "image/jpeg",
                                       "data": _read_image_b64(pre_screenshot)}},
        {"type": "text", "text": "POST-SCREENSHOT:"},
        {"type": "image", "source": {"type": "base64", "media_type": "image/jpeg",
                                       "data": _read_image_b64(post_screenshot)}},
        {"type": "text", "text": f"TOOL TRACE:\n{trace_json}"},
        {"type": "text", "text": f"FINAL RESPONSE:\n{final_response}"},
    ]
    msg = client.messages.create(
        model=cfg.model, max_tokens=1024,
        messages=[{"role": "user", "content": user_content}],
    )
    raw = "".join(block.text for block in msg.content if block.type == "text")
    parsed = _parse_judge_json(raw)
    verdicts = [RubricVerdict(id=v["id"], verdict=v["verdict"], reason=v["reason"])
                for v in parsed["items"]]
    out = JudgeOutput(verdicts=verdicts, overall_notes=parsed.get("overall_notes", ""),
                      cache_key=key, raw_response=raw)
    if cache_dir is not None:
        cache_dir.mkdir(parents=True, exist_ok=True)
        (cache_dir / f"{key}.json").write_text(json.dumps({
            "verdicts": [dc.asdict(v) for v in verdicts],
            "overall_notes": out.overall_notes,
            "raw_response": raw,
        }), encoding="utf-8")
    return out

def _parse_judge_json(raw: str) -> dict[str, Any]:
    s = raw.strip()
    start = s.find("{")
    end = s.rfind("}")
    if start == -1 or end == -1:
        raise ValueError(f"judge response has no JSON object: {raw[:200]}")
    return json.loads(s[start:end+1])
```

- [ ] **Step 2: Commit**

```bash
git add benchmark/runner/judge.py
git commit -m "feat(benchmark): LLM judge with rubric scoring and cache"
```

---

## Task 9: Metrics aggregation

**Files:**

- Create: `benchmark/runner/metrics.py`

- [ ] **Step 1: Implement metrics.py**

`benchmark/runner/metrics.py`:

```python
from __future__ import annotations
import statistics
from collections import Counter
from benchmark.runner.models import TaskResult

def aggregate(results: list[TaskResult]) -> dict[str, object]:
    if not results:
        return {"tasks": 0}
    by_status: Counter[str] = Counter(r.status for r in results)
    by_category: dict[str, dict[str, int]] = {}
    for r in results:
        cat = by_category.setdefault(r.category, {"passed": 0, "total": 0,
                                                    "rubric_pass": 0, "rubric_total": 0})
        cat["total"] += 1
        if r.status == "passed":
            cat["passed"] += 1
        cat["rubric_pass"] += r.rubric_pass_count
        cat["rubric_total"] += r.rubric_total
    judge_eligible = [r for r in results if r.status not in {"judge_error", "skipped"}]
    pass_count = sum(1 for r in judge_eligible if r.status == "passed")
    walls = [r.metrics.get("wall_ms", 0) for r in results if r.metrics.get("wall_ms")]
    tool_counts = [r.metrics.get("tool_calls", 0) for r in results
                   if r.metrics.get("tool_calls") is not None]
    return {
        "tasks": len(results),
        "passed": pass_count,
        "by_status": dict(by_status),
        "by_category": by_category,
        "wall_ms_median": int(statistics.median(walls)) if walls else None,
        "wall_ms_p95": int(_percentile(walls, 95)) if walls else None,
        "tool_calls_median": int(statistics.median(tool_counts)) if tool_counts else None,
        "tool_calls_p95": int(_percentile(tool_counts, 95)) if tool_counts else None,
    }

def _percentile(values: list[float], pct: float) -> float:
    if not values:
        return 0
    s = sorted(values)
    k = (len(s) - 1) * pct / 100
    f = int(k)
    c = min(f + 1, len(s) - 1)
    return s[f] + (s[c] - s[f]) * (k - f)
```

- [ ] **Step 2: Commit**

```bash
git add benchmark/runner/metrics.py
git commit -m "feat(benchmark): metrics aggregation"
```

---

## Task 10: Report writer

**Files:**

- Create: `benchmark/runner/report.py`

- [ ] **Step 1: Implement report.py**

`benchmark/runner/report.py`:

```python
from __future__ import annotations
import dataclasses as dc
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from benchmark.runner.models import TaskResult
from benchmark.runner.metrics import aggregate

def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()

def git_sha(repo_root: Path) -> tuple[str, bool]:
    try:
        sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repo_root, text=True).strip()
        dirty = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=repo_root, text=True).strip())
        return sha, dirty
    except Exception:
        return "", False

def write_jsonl(path: Path, results: list[TaskResult]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as fp:
        for r in results:
            fp.write(json.dumps(dc.asdict(r), ensure_ascii=False, sort_keys=True) + "\n")

def write_manifest(path: Path, manifest: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")

def write_summary(path: Path, suite_name: str, manifest: dict[str, Any], results: list[TaskResult]) -> None:
    agg = aggregate(results)
    lines = [
        f"# {suite_name} — {manifest.get('run_id', '')}",
        "",
        f"Agent: {manifest.get('agent_url', '')}",
        f"Judge: {manifest.get('judge_config', {}).get('provider')} / {manifest.get('judge_config', {}).get('model')}",
        f"Total: {agg['passed']}/{agg['tasks']} passed",
        "",
        "## By category",
        "",
        "| category | passed | total | rubric step % |",
        "|---|---|---|---|",
    ]
    for cat, c in agg["by_category"].items():
        pct = (100.0 * c["rubric_pass"] / c["rubric_total"]) if c["rubric_total"] else 0
        lines.append(f"| {cat} | {c['passed']} | {c['total']} | {pct:.0f}% |")
    lines += [
        "",
        "## Efficiency",
        "",
        f"median wall: {agg.get('wall_ms_median')} ms    p95 wall: {agg.get('wall_ms_p95')} ms",
        f"median tool calls: {agg.get('tool_calls_median')}    p95: {agg.get('tool_calls_p95')}",
        "",
        "## Failures",
        "",
    ]
    for r in results:
        if r.status == "passed":
            continue
        bad = [v for v in r.rubric if v.verdict == "no"]
        reasons = "; ".join(f"{v.id}: {v.reason}" for v in bad) or r.status
        lines.append(f"- **{r.task_id}** ({r.status}) — {reasons}")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
```

- [ ] **Step 2: Commit**

```bash
git add benchmark/runner/report.py
git commit -m "feat(benchmark): JSONL, manifest, and summary writer"
```

---

## Task 11: CLI run command — orchestrate one task end-to-end

**Files:**

- Create: `benchmark/runner/runtask.py`
- Modify: `benchmark/runner/main.py`

This task introduces the per-task orchestration helper, isolated so it can be unit-reasoned about. The CLI in `main.py` then loops over tasks and calls it.

- [ ] **Step 1: Implement runtask.py**

`benchmark/runner/runtask.py`:

```python
from __future__ import annotations
import dataclasses as dc
import json
import time
from pathlib import Path
from benchmark.runner.agent_client import AgentClient, AgentTimeoutError
from benchmark.runner.assertions import evaluate_hard_assertions
from benchmark.runner.capture import take_screenshot, write_step_screenshot
from benchmark.runner.judge import judge_task, JudgeConfig
from benchmark.runner.models import TaskResult, RubricVerdict, HardAssertionResults
from benchmark.runner.reset import global_reset, per_task_setup, ResetError
from benchmark.runner.suite import Suite, TaskSpec
from benchmark.runner.trace import extract_trace, extract_step_screenshots
from benchmark.runner.report import now_iso

def run_one_task(
    client: AgentClient,
    suite: Suite,
    task: TaskSpec,
    attempt: int,
    artifact_dir: Path,
    judge_cfg: JudgeConfig | None,
    judge_cache_dir: Path | None,
    run_id: str,
) -> TaskResult:
    artifact_dir.mkdir(parents=True, exist_ok=True)
    started = now_iso()
    started_mono = time.monotonic()
    base = TaskResult(
        suite=suite.name, run_id=run_id, task_id=task.id, category=task.category,
        attempt=attempt, status="failed",
        rubric=[], rubric_pass_count=0, rubric_total=len(task.rubric),
        artifact_dir=str(artifact_dir), started_at=started,
    )
    try:
        client.clear_history()
        global_reset(client, suite.global_reset)
        per_task_setup(client, task.setup)
    except ResetError as e:
        base.status = "skipped"
        base.metrics = {"error": f"setup: {e}"}
        base.finished_at = now_iso()
        return base
    pre_path = artifact_dir / "pre.jpg"
    take_screenshot(client, pre_path)
    timed_out = False
    try:
        chat = client.chat(task.prompt, timeout_sec=task.hard_assertions.must_complete_within_sec)
        history = chat.history
    except AgentTimeoutError:
        timed_out = True
        history = client_history_or_empty(client)
    (artifact_dir / "history.json").write_text(
        json.dumps(history, ensure_ascii=False, indent=2), encoding="utf-8")
    trace = extract_trace(history)
    wall_ms = int((time.monotonic() - started_mono) * 1000)
    trace_dict = {
        "tool_calls": [dc.asdict(tc) for tc in trace.tool_calls],
        "final_response": trace.final_response,
        "total_tool_calls": trace.total_tool_calls,
    }
    (artifact_dir / "trace.json").write_text(
        json.dumps(trace_dict, ensure_ascii=False, indent=2), encoding="utf-8")
    steps_dir = artifact_dir / "steps"
    last_shot_path: Path | None = None
    for i, (tool_name, b64) in enumerate(extract_step_screenshots(history), start=1):
        p = steps_dir / f"step_{i:02d}_{tool_name}.jpg"
        write_step_screenshot(p, b64)
        last_shot_path = p
    base.metrics = {"wall_ms": wall_ms, "tool_calls": trace.total_tool_calls,
                    "screenshots_taken": sum(1 for tc in trace.tool_calls if tc.has_screenshot)}
    outcome = evaluate_hard_assertions(trace, task.hard_assertions, timed_out=timed_out)
    base.hard_assertions = outcome.results
    if not outcome.all_passed:
        base.status = "timeout" if timed_out else "failed"
        base.finished_at = now_iso()
        return base
    if judge_cfg is None or last_shot_path is None:
        base.status = "judge_error" if judge_cfg is not None else "failed"
        base.finished_at = now_iso()
        return base
    try:
        verdict = judge_task(
            description=task.description_for_judge,
            rubric=task.rubric,
            pre_screenshot=pre_path,
            post_screenshot=last_shot_path,
            trace=trace_dict,
            final_response=trace.final_response,
            cfg=judge_cfg,
            cache_dir=judge_cache_dir,
        )
    except Exception as e:
        base.status = "judge_error"
        base.metrics["judge_error"] = str(e)
        base.finished_at = now_iso()
        return base
    base.rubric = verdict.verdicts
    base.rubric_pass_count = sum(1 for v in verdict.verdicts if v.verdict == "yes")
    (artifact_dir / "judge.json").write_text(json.dumps({
        "verdicts": [dc.asdict(v) for v in verdict.verdicts],
        "overall_notes": verdict.overall_notes,
        "cache_key": verdict.cache_key,
    }, ensure_ascii=False, indent=2), encoding="utf-8")
    base.status = "passed" if base.rubric_pass_count == base.rubric_total else "failed"
    base.finished_at = now_iso()
    return base

def client_history_or_empty(client: AgentClient) -> list[dict]:
    try:
        r = client._client.get("/api/history", timeout=5)
        if r.status_code == 200:
            return r.json()
    except Exception:
        pass
    return []
```

- [ ] **Step 2: Implement main.py**

`benchmark/runner/main.py`:

```python
from __future__ import annotations
import argparse
import os
import sys
from datetime import datetime, timezone
from pathlib import Path
from benchmark.runner.agent_client import AgentClient
from benchmark.runner.judge import JudgeConfig
from benchmark.runner.report import git_sha, write_jsonl, write_manifest, write_summary, now_iso
from benchmark.runner.runtask import run_one_task
from benchmark.runner.suite import load_suite

REPO_ROOT = Path(__file__).resolve().parents[2]

def cli(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="benchmark.runner")
    sub = parser.add_subparsers(dest="cmd", required=True)
    p_run = sub.add_parser("run")
    p_run.add_argument("--suite", required=True)
    p_run.add_argument("--agent-url", default=os.environ.get("AIDEN_AGENT_URL", "http://localhost:8080"))
    p_run.add_argument("--judge-model", default="claude-sonnet-4-6")
    p_run.add_argument("--no-judge", action="store_true")
    p_run.add_argument("--repeats", type=int, default=None)
    p_run.add_argument("--out", default=str(REPO_ROOT / "benchmark" / "runs"))
    p_rejudge = sub.add_parser("rejudge")
    p_rejudge.add_argument("--run-dir", required=True)
    p_rejudge.add_argument("--judge-model", default="claude-sonnet-4-6")
    p_compare = sub.add_parser("compare")
    p_compare.add_argument("--runs", nargs=2, required=True)
    args = parser.parse_args(argv)
    if args.cmd == "run":
        return _cmd_run(args)
    if args.cmd == "rejudge":
        from benchmark.runner.rejudge import rejudge_run
        return rejudge_run(Path(args.run_dir), args.judge_model)
    if args.cmd == "compare":
        from benchmark.runner.compare import compare_runs
        return compare_runs(Path(args.runs[0]), Path(args.runs[1]))
    return 2

def _cmd_run(args: argparse.Namespace) -> int:
    suite = load_suite(Path(args.suite))
    run_id = datetime.now(timezone.utc).strftime("%Y-%m-%d_%H%M%S")
    run_dir = Path(args.out) / run_id
    client = AgentClient(base_url=args.agent_url)
    if not client.health():
        print(f"agent at {args.agent_url} is not reachable", file=sys.stderr)
        return 2
    judge_cfg = None if args.no_judge else JudgeConfig(model=args.judge_model)
    judge_cache = run_dir / "_judge_cache"
    sha, dirty = git_sha(REPO_ROOT)
    started = now_iso()
    results = []
    try:
        for task in suite.tasks:
            n = args.repeats or task.repeats
            for attempt in range(1, n + 1):
                art_dir = run_dir / "tasks" / task.id / (f"attempt_{attempt}" if n > 1 else "")
                r = run_one_task(client, suite, task, attempt, art_dir,
                                 judge_cfg, judge_cache, run_id)
                print(f"{r.status.upper():10s} {task.id} attempt={attempt} "
                      f"rubric={r.rubric_pass_count}/{r.rubric_total} "
                      f"wall={r.metrics.get('wall_ms')}ms", flush=True)
                results.append(r)
    finally:
        client.close()
    manifest = {
        "run_id": run_id, "git_sha": sha, "git_dirty": dirty,
        "suite_path": str(suite.source_path), "suite_sha256": suite.sha256,
        "agent_url": args.agent_url,
        "judge_config": {"provider": "anthropic", "model": args.judge_model} if judge_cfg else None,
        "judge_prompt_version": "v1",
        "started_at": started, "finished_at": now_iso(),
        "totals": {"tasks": len(results),
                   "passed": sum(1 for r in results if r.status == "passed"),
                   "failed": sum(1 for r in results if r.status == "failed"),
                   "skipped": sum(1 for r in results if r.status == "skipped"),
                   "judge_error": sum(1 for r in results if r.status == "judge_error"),
                   "timeout": sum(1 for r in results if r.status == "timeout")},
    }
    write_manifest(run_dir / "manifest.json", manifest)
    write_jsonl(run_dir / "results.jsonl", results)
    write_summary(run_dir / "summary.md", suite.name, manifest, results)
    return 0 if manifest["totals"]["passed"] == manifest["totals"]["tasks"] else 1

if __name__ == "__main__":
    sys.exit(cli())
```

- [ ] **Step 3: Commit**

```bash
git add benchmark/runner/runtask.py benchmark/runner/main.py
git commit -m "feat(benchmark): per-task orchestrator and run CLI"
```

---

## Task 12: Rejudge and compare CLI

**Files:**

- Create: `benchmark/runner/rejudge.py`
- Create: `benchmark/runner/compare.py`

- [ ] **Step 1: Implement rejudge.py**

`benchmark/runner/rejudge.py`:

```python
from __future__ import annotations
import dataclasses as dc
import json
from pathlib import Path
from benchmark.runner.judge import JudgeConfig, judge_task
from benchmark.runner.suite import RubricItem
from benchmark.runner.models import RubricVerdict
from benchmark.runner.report import write_jsonl, now_iso

def rejudge_run(run_dir: Path, judge_model: str) -> int:
    manifest = json.loads((run_dir / "manifest.json").read_text("utf-8"))
    cfg = JudgeConfig(model=judge_model)
    cache = run_dir / "_judge_cache"
    new_results = []
    for line in (run_dir / "results.jsonl").read_text("utf-8").splitlines():
        row = json.loads(line)
        td = run_dir / "tasks" / row["task_id"]
        attempt_dir = td / f"attempt_{row['attempt']}" if (td / f"attempt_{row['attempt']}").exists() else td
        pre = attempt_dir / "pre.jpg"
        steps = sorted((attempt_dir / "steps").glob("*.jpg")) if (attempt_dir / "steps").exists() else []
        if not pre.exists() or not steps:
            row["status"] = "judge_error"
            row["metrics"] = {**row.get("metrics", {}), "rejudge_error": "missing artifacts"}
            new_results.append(row); continue
        post = steps[-1]
        trace = json.loads((attempt_dir / "trace.json").read_text("utf-8"))
        rubric = [RubricItem(id=r["id"], check=r["check"]) for r in row.get("rubric_spec", [])]
        if not rubric:
            row["status"] = "judge_error"
            row["metrics"] = {**row.get("metrics", {}), "rejudge_error": "missing rubric_spec"}
            new_results.append(row); continue
        verdict = judge_task(
            description=row.get("description_for_judge", ""),
            rubric=rubric, pre_screenshot=pre, post_screenshot=post,
            trace=trace, final_response=trace.get("final_response", ""),
            cfg=cfg, cache_dir=cache,
        )
        row["rubric"] = [dc.asdict(v) for v in verdict.verdicts]
        row["rubric_pass_count"] = sum(1 for v in verdict.verdicts if v.verdict == "yes")
        row["status"] = "passed" if row["rubric_pass_count"] == row["rubric_total"] else "failed"
        row["finished_at"] = now_iso()
        new_results.append(row)
    out = run_dir / "results.rejudged.jsonl"
    out.write_text("\n".join(json.dumps(r, ensure_ascii=False, sort_keys=True) for r in new_results) + "\n",
                   encoding="utf-8")
    print(f"wrote {out}")
    return 0
```

Note: this requires `results.jsonl` rows to carry `rubric_spec` and `description_for_judge`. Update `runtask.py` Step 2 in Task 11 to include these on each `TaskResult` (extend `models.TaskResult` with optional fields and populate them during `run_one_task`).

- [ ] **Step 2: Patch models.py + runtask.py for rejudge support**

In `benchmark/runner/models.py`, append to `TaskResult`:

```python
    description_for_judge: str = ""
    rubric_spec: list[dict] = dc.field(default_factory=list)
```

In `benchmark/runner/runtask.py`, after creating `base`, set:

```python
    base.description_for_judge = task.description_for_judge
    base.rubric_spec = [dc.asdict(r) for r in task.rubric]
```

- [ ] **Step 3: Implement compare.py**

`benchmark/runner/compare.py`:

```python
from __future__ import annotations
import json
from pathlib import Path

def compare_runs(a: Path, b: Path) -> int:
    rows_a = _load(a)
    rows_b = _load(b)
    keys = set(rows_a) | set(rows_b)
    print(f"=== {a.name}  vs  {b.name} ===")
    flips = 0
    for k in sorted(keys):
        ra = rows_a.get(k); rb = rows_b.get(k)
        if not ra:
            print(f"+ {k}  added in B  status={rb['status']}"); continue
        if not rb:
            print(f"- {k}  removed in B  status={ra['status']}"); continue
        if ra["status"] != rb["status"]:
            flips += 1
            print(f"~ {k}  {ra['status']} -> {rb['status']}")
        wa = ra.get("metrics", {}).get("wall_ms")
        wb = rb.get("metrics", {}).get("wall_ms")
        if wa and wb and abs(wb - wa) > 1000:
            print(f"   wall {wa}ms -> {wb}ms")
    print(f"flips: {flips}")
    return 0

def _load(run_dir: Path) -> dict[str, dict]:
    out: dict[str, dict] = {}
    for line in (run_dir / "results.jsonl").read_text("utf-8").splitlines():
        r = json.loads(line)
        out[f"{r['task_id']}#{r['attempt']}"] = r
    return out
```

- [ ] **Step 4: Commit**

```bash
git add benchmark/runner/rejudge.py benchmark/runner/compare.py benchmark/runner/models.py benchmark/runner/runtask.py
git commit -m "feat(benchmark): rejudge and compare CLI commands"
```

---

## Task 13: Author phone_control_v1 suite

**Files:**

- Create: `benchmark/suites/phone_control_v1.json`

- [ ] **Step 1: Write the suite JSON**

`benchmark/suites/phone_control_v1.json`:

```json
{
  "name": "phone_control_v1",
  "description": "Agent-driven phone control benchmark v1.",
  "global_reset": {
    "tool_sequence": [
      { "tool": "keyboard_tap", "args": { "keys": ["escape"] } },
      { "tool": "keyboard_tap", "args": { "keys": ["home"] } },
      { "tool": "wait_ms", "args": { "ms": 800 } }
    ]
  },
  "tasks": [
    {
      "id": "open_settings",
      "category": "single_step",
      "description_for_judge": "Agent must open the system Settings app from home screen.",
      "prompt": "请打开系统设置。",
      "rubric": [
        {
          "id": "in_settings",
          "check": "Post-screenshot shows the Settings app main page (header text or recognisable layout)."
        },
        {
          "id": "no_error",
          "check": "No crash dialog or error overlay is visible in the post-screenshot."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 1,
        "max_tool_calls": 8,
        "must_complete_within_sec": 90
      }
    },
    {
      "id": "open_clock",
      "category": "single_step",
      "description_for_judge": "Agent must open the Clock app from the home screen.",
      "prompt": "请打开时钟应用。",
      "rubric": [
        {
          "id": "in_clock",
          "check": "Post-screenshot shows the Clock app (alarm/world clock/timer tabs visible)."
        },
        {
          "id": "no_error",
          "check": "No crash dialog or error overlay is visible."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 1,
        "max_tool_calls": 8,
        "must_complete_within_sec": 90
      }
    },
    {
      "id": "tap_back",
      "category": "single_step",
      "description_for_judge": "Agent must navigate back from a settings sub-page to the parent settings page.",
      "prompt": "请返回上一层。",
      "setup": {
        "type": "agent_prompt",
        "prompt": "打开系统设置，并进入'显示'或任意一个二级子页面。完成后停在该子页面。",
        "timeout_sec": 120
      },
      "rubric": [
        {
          "id": "page_changed",
          "check": "Post-screenshot shows a different page than would be reached without navigating back (top-level settings or app list)."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 1,
        "max_tool_calls": 5,
        "must_complete_within_sec": 60
      }
    },
    {
      "id": "type_in_search",
      "category": "single_step",
      "description_for_judge": "Agent must locate a search input on the screen and type the literal string 'hello' into it.",
      "prompt": "在搜索框里输入 hello。",
      "rubric": [
        {
          "id": "search_focused",
          "check": "Post-screenshot shows a focused text input (cursor or filled field)."
        },
        {
          "id": "text_present",
          "check": "The string 'hello' appears in the visible input field."
        },
        {
          "id": "no_extra_garbage",
          "check": "No unrelated typed characters appear before/after 'hello'."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 2,
        "max_tool_calls": 10,
        "must_complete_within_sec": 90
      }
    },
    {
      "id": "scroll_page_down",
      "category": "single_step",
      "description_for_judge": "Agent must perform a vertical scroll downward on the current page.",
      "prompt": "请向下滑动一屏。",
      "rubric": [
        {
          "id": "page_scrolled",
          "check": "Post-screenshot content differs from pre-screenshot in a way consistent with downward scrolling."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 1,
        "max_tool_calls": 4,
        "must_complete_within_sec": 45
      }
    },
    {
      "id": "swipe_between_pages",
      "category": "single_step",
      "description_for_judge": "Agent must swipe horizontally on the home screen to switch to a different home screen page.",
      "prompt": "请在桌面左右滑动切换到下一页。",
      "rubric": [
        {
          "id": "different_page",
          "check": "Post-screenshot shows a different home-screen page than the pre-screenshot."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 1,
        "max_tool_calls": 4,
        "must_complete_within_sec": 45
      }
    },
    {
      "id": "open_notification_shade",
      "category": "single_step",
      "description_for_judge": "Agent must open the notification shade by swiping down from the top of the screen.",
      "prompt": "请打开通知栏。",
      "rubric": [
        {
          "id": "shade_visible",
          "check": "Post-screenshot shows the notification shade pulled down (system toggles or notifications visible)."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 1,
        "max_tool_calls": 4,
        "must_complete_within_sec": 45
      }
    },
    {
      "id": "settings_search_bluetooth",
      "category": "multi_step",
      "description_for_judge": "Agent must open Settings, find the search box, search for Bluetooth, and end on the Bluetooth settings page.",
      "prompt": "打开系统设置，搜索 Bluetooth 并进入蓝牙设置页面。",
      "rubric": [
        {
          "id": "in_settings",
          "check": "Agent navigated into Settings at some step (visible in trace screenshots)."
        },
        {
          "id": "searched",
          "check": "Agent typed 'Bluetooth' or 蓝牙 into a search field."
        },
        {
          "id": "on_bluetooth_page",
          "check": "Post-screenshot shows the Bluetooth settings page (Bluetooth toggle and device list)."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 3,
        "max_tool_calls": 20,
        "must_complete_within_sec": 240
      }
    },
    {
      "id": "toggle_wifi",
      "category": "multi_step",
      "description_for_judge": "Agent must open Settings, navigate to Wi-Fi, turn it off, and then turn it back on.",
      "prompt": "打开系统设置，进入 Wi-Fi，先关闭再打开。",
      "rubric": [
        {
          "id": "wifi_page_reached",
          "check": "Trace screenshots show a Wi-Fi settings page at some step."
        },
        {
          "id": "wifi_off_observed",
          "check": "At least one mid-task screenshot shows Wi-Fi disabled."
        },
        {
          "id": "wifi_on_final",
          "check": "Post-screenshot shows Wi-Fi enabled."
        },
        {
          "id": "no_extra_changes",
          "check": "No unrelated settings appear toggled."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 4,
        "max_tool_calls": 20,
        "must_complete_within_sec": 240
      }
    },
    {
      "id": "add_clock_alarm",
      "category": "multi_step",
      "description_for_judge": "Agent must open the Clock app, create a new alarm for 7:30, and save it.",
      "prompt": "打开时钟应用，新建一个 7:30 的闹钟并保存。",
      "rubric": [
        {
          "id": "in_clock_app",
          "check": "Trace shows agent inside the Clock app."
        },
        {
          "id": "alarm_creation_ui",
          "check": "Trace shows an alarm creation/time-picker UI."
        },
        {
          "id": "alarm_saved",
          "check": "Post-screenshot shows alarms list including a 7:30 entry."
        },
        {
          "id": "alarm_enabled",
          "check": "The newly created alarm is shown as enabled (toggle on)."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 4,
        "max_tool_calls": 25,
        "must_complete_within_sec": 300
      }
    },
    {
      "id": "scroll_to_bottom",
      "category": "multi_step",
      "description_for_judge": "Agent must repeatedly scroll a settings page until the bottom of the list is reached, and stop scrolling once the bottom is detected.",
      "prompt": "在设置页面持续向下滑动，直到滑到底部为止。",
      "rubric": [
        {
          "id": "multiple_scrolls",
          "check": "Trace contains at least three swipe/scroll tool calls."
        },
        {
          "id": "stopped_at_bottom",
          "check": "Post-screenshot shows page contents that no longer change relative to the prior step (bottom reached)."
        },
        {
          "id": "agent_acknowledged",
          "check": "Agent's final reply indicates the bottom was reached or no more content."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 3,
        "max_tool_calls": 25,
        "must_complete_within_sec": 240
      }
    },
    {
      "id": "type_long_mixed_text",
      "category": "multi_step",
      "description_for_judge": "Agent must locate a text input and type the literal string 'Aiden测试 benchmark-2026!' into it.",
      "prompt": "在当前可输入位置输入：Aiden测试 benchmark-2026!",
      "rubric": [
        { "id": "input_focused", "check": "Trace shows a focused text input." },
        {
          "id": "english_part",
          "check": "Post-screenshot shows the English portion 'Aiden ... benchmark-2026!' in the input."
        },
        {
          "id": "chinese_part",
          "check": "Post-screenshot shows the Chinese characters '测试' in the input."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 2,
        "max_tool_calls": 10,
        "must_complete_within_sec": 120
      }
    },
    {
      "id": "select_all_and_delete",
      "category": "multi_step",
      "description_for_judge": "Agent must clear an input field that already contains text by selecting all and deleting.",
      "prompt": "请把输入框里已有的文字全部清空。",
      "setup": {
        "type": "agent_prompt",
        "prompt": "请打开任意一个有文本输入框的应用（例如备忘录、便签、或浏览器搜索框），点击该输入框使其获得焦点，并输入文本 'temporary note text'。完成后停在该输入框已聚焦且包含该文本的状态。",
        "timeout_sec": 180
      },
      "rubric": [
        {
          "id": "select_used",
          "check": "Trace shows a select-all action (META+A or equivalent)."
        },
        {
          "id": "input_empty",
          "check": "Post-screenshot shows the input field is empty."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 2,
        "max_tool_calls": 8,
        "must_complete_within_sec": 90
      }
    },
    {
      "id": "copy_paste_text",
      "category": "multi_step",
      "description_for_judge": "Agent must type a phrase, select all and copy it, then move to a different input field and paste it.",
      "prompt": "在第一个输入框输入 hello-aiden，然后选中复制，切到第二个输入框粘贴。",
      "rubric": [
        {
          "id": "first_input_typed",
          "check": "Trace shows hello-aiden being typed into an input."
        },
        { "id": "copy_action", "check": "Trace shows a copy action (META+C)." },
        {
          "id": "second_input_focused",
          "check": "Trace shows agent moving to a different input field."
        },
        {
          "id": "paste_visible",
          "check": "Post-screenshot shows hello-aiden in the second input field."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 4,
        "max_tool_calls": 15,
        "must_complete_within_sec": 180
      }
    },
    {
      "id": "find_and_tap_specific_item",
      "category": "multi_step",
      "description_for_judge": "Agent must navigate into Settings and scroll to find a specific item ('关于手机' / 'About phone'), then tap it.",
      "prompt": "打开系统设置，向下滑动找到'关于手机'并点击进入。",
      "rubric": [
        { "id": "in_settings", "check": "Trace shows agent inside Settings." },
        {
          "id": "scrolled",
          "check": "Trace contains at least one scroll/swipe action inside Settings."
        },
        {
          "id": "on_about_page",
          "check": "Post-screenshot shows the About-phone / device-info page."
        }
      ],
      "hard_assertions": {
        "min_tool_calls": 3,
        "max_tool_calls": 20,
        "must_complete_within_sec": 240
      }
    }
  ]
}
```

- [ ] **Step 2: Validate the suite loads cleanly**

```
python -c "from benchmark.runner.suite import load_suite; print(len(load_suite('benchmark/suites/phone_control_v1.json').tasks))"
```

Expected: `15`.

- [ ] **Step 3: Commit**

```bash
git add benchmark/suites/phone_control_v1.json
git commit -m "feat(benchmark): phone_control_v1 suite (15 tasks)"
```

---

## Task 14: Legacy shim and docs update

**Files:**

- Modify: `scripts/aiden_benchmark.py` (rewrite as shim)
- Modify: `docs/BENCHMARK.md` (rewrite for new flow)
- Modify: `benchmark/suites/full_smoke.json` (mark deprecated)

- [ ] **Step 1: Replace scripts/aiden_benchmark.py with a shim**

```python
#!/usr/bin/env python3
"""Legacy entry point. Forwards to benchmark.runner."""
import sys
from benchmark.runner.main import cli

if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] in {"run", "rejudge", "compare"}:
        sys.exit(cli(sys.argv[1:]))
    sys.exit(cli(["run", *sys.argv[1:]]))
```

- [ ] **Step 2: Rewrite docs/BENCHMARK.md**

Replace the file with content describing the new agent-driven flow: prerequisites (Go agent daemon running), how to run (`python -m benchmark.runner run --suite benchmark/suites/phone_control_v1.json`), how to interpret results (run dir layout, summary.md, rejudge), and a "Legacy" section noting `full_smoke.json` is deprecated and kept for reference only.

- [ ] **Step 3: Mark full_smoke.json deprecated**

Add at the top of `benchmark/suites/full_smoke.json`:

```json
"_deprecated": "Replaced by phone_control_v1.json. This file is retained for historical reference and is no longer maintained.",
```

- [ ] **Step 4: Commit**

```bash
git add scripts/aiden_benchmark.py docs/BENCHMARK.md benchmark/suites/full_smoke.json
git commit -m "docs(benchmark): legacy shim and updated user guide"
```

---

## Task 15: End-to-end smoke run on the rig

This task is manual verification, not new code. Run the full suite against a real device and tune rubric phrasing where the judge disagrees with human review.

- [ ] **Step 1: Start the Go agent daemon**

```
cd src/agent
go run ./cmd/daemon -config /path/to/agent/config -addr :8080
```

Verify: `curl http://localhost:8080/api/tools` returns a JSON list of tools.

- [ ] **Step 2: Install Python deps**

```
python3 -m venv .venv-bench
.venv-bench/bin/pip install -r benchmark/requirements.txt
```

- [ ] **Step 3: Set judge API key**

```
export ANTHROPIC_API_KEY=sk-ant-...
```

- [ ] **Step 4: Dry-run with --no-judge first**

```
.venv-bench/bin/python -m benchmark.runner run \
  --suite benchmark/suites/phone_control_v1.json \
  --no-judge
```

Expected: a `benchmark/runs/<run_id>/` directory with manifest.json, results.jsonl, summary.md, and per-task `pre.jpg` + `steps/*.jpg` + `trace.json` + `history.json`.

Inspect at least 3 task artifact dirs manually. If trace.json or screenshots look wrong, fix the runner before continuing.

- [ ] **Step 5: Full run with judge**

```
.venv-bench/bin/python -m benchmark.runner run \
  --suite benchmark/suites/phone_control_v1.json
```

Expected: `summary.md` shows pass/fail per category. For each FAILED task, manually compare the judge's reason against the screenshots. If the judge is wrong (rubric phrasing too strict or too loose), edit the rubric in `phone_control_v1.json` and `rejudge` rather than re-running on the rig.

- [ ] **Step 6: Iterate rubric until judge agrees with human review**

For tasks where judge disagrees with human:

1. Tighten or loosen the rubric `check` text.
2. Run `python -m benchmark.runner rejudge --run-dir benchmark/runs/<id>`.
3. Verify new `results.rejudged.jsonl` matches human assessment.
4. Once stable, commit the rubric updates.

- [ ] **Step 7: Commit any rubric tweaks**

```bash
git add benchmark/suites/phone_control_v1.json
git commit -m "fix(benchmark): tune phone_control_v1 rubrics from rig run"
```

---

## Done criteria

- All 15 tasks in `phone_control_v1.json` execute end-to-end against a real Go agent + phone rig.
- `benchmark/runs/<id>/summary.md` reports a pass rate that matches human review (no systematic judge errors).
- `python -m benchmark.runner run`, `rejudge`, and `compare` all work.
- All unit tests pass: `pytest tests/benchmark/ -v`.
- `scripts/aiden_benchmark.py` continues to work as a shim.
