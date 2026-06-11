# Aiden + MobileGym Suite Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow Aiden's JSON benchmark suites to run on MobileGym (with concurrency), and surface both Aiden-native and MobileGym execution modes—plus MobileGym built-in suites—through the existing `/benchmark` Web UI.

**Architecture:** Add a Go-side dispatch layer that picks between the existing Aiden Python runner and a new MobileGym launcher (which shells out to `parallel_run.sh`). Add a Python-side adapter in `run_aiden.py` that loads Aiden JSON suites and converts each `TaskSpec` into a MobileGym `Task` at runtime. The Web UI gains a mode toggle and (in MobileGym mode) parallel/limit controls plus a separate built-in-suite group.

**Tech Stack:** Go 1.22+ (`net/http`, `os/exec`), Python 3.11+ (existing `runner.suite`, MobileGym `bench_env`), vanilla JS in embedded HTML, Docker Compose v2.24+.

**Spec:** `docs/superpowers/specs/2026-06-11-aiden-mobilegym-suite-compatibility-design.md`

---

## File Structure

**Go server changes** (`src/agent/internal/agent/`):

- `benchmark.go` — extend `handleBenchmarkSuites` (mode-aware listing, MobileGym built-in scan); update `handleBenchmarkLog` for per-mode log path
- `benchmark_runner.go` — extend `handleBenchmarkRun` (mode/suite_type/parallel/limit dispatch); add `launchMobileGymRunner`
- `benchmark_html.go` — UI: mode radio, parallel/limit inputs, suite groups, JS dispatch
- `benchmark_test.go` — extend with mode/dispatch tests

**Python adapter** (`benchmark/mobilegym/scripts/`):

- `run_aiden.py` — add `--aiden-suite` CLI flag; new helpers `_load_aiden_suite_as_mobilegym_tasks` and `_convert_task`; integrate into `_run_serial`

**Shell wrapper** (`benchmark/mobilegym/docker/`):

- `parallel_run.sh` — pass through `--aiden-suite`

**New tests**:

- `tests/benchmark/test_mobilegym_aiden_suite_adapter.py` — Python adapter unit tests

---

## Task 0: Worktree Setup

**Files:** none

- [ ] **Step 1: Confirm working in feature worktree**

Run: `git rev-parse --show-toplevel && git status --short && git rev-parse --abbrev-ref HEAD`
Expected: working tree under `/Users/jacob/.prowl/repos/aiden-hardware-demo/feat/benchmark_suites`, clean tree, branch `feat/benchmark_suites`.

If branch is `main` or another base branch, stop and re-create a worktree via `superpowers:using-git-worktrees`.

---

## Task 1: Python adapter — load Aiden JSON suite (failing test first)

**Files:**

- Create: `tests/benchmark/__init__.py` (empty file if not present)
- Create: `tests/benchmark/test_mobilegym_aiden_suite_adapter.py`
- Modify: `benchmark/mobilegym/scripts/run_aiden.py` (after Task 2)

This task adds a unit test for the not-yet-implemented `_load_aiden_suite_as_mobilegym_tasks` helper. We're using TDD: write the failing test, then implement.

- [ ] **Step 1: Ensure test package init exists**

```bash
test -f tests/benchmark/__init__.py || : > tests/benchmark/__init__.py
```

- [ ] **Step 2: Write the failing test**

Create `tests/benchmark/test_mobilegym_aiden_suite_adapter.py`:

```python
"""Unit tests for the Aiden→MobileGym suite adapter in run_aiden.py."""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path

import pytest


REPO_ROOT = Path(__file__).resolve().parents[2]
RUN_AIDEN_PATH = REPO_ROOT / "benchmark" / "mobilegym" / "scripts" / "run_aiden.py"


@pytest.fixture
def run_aiden_module(tmp_path, monkeypatch):
    """Import run_aiden.py as a module without executing main()."""
    benchmark_root = REPO_ROOT / "benchmark"
    monkeypatch.syspath_prepend(str(benchmark_root))
    spec = importlib.util.spec_from_file_location("run_aiden", RUN_AIDEN_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _write_minimal_suite(suites_dir: Path, name: str = "test_suite") -> Path:
    suite = {
        "name": name,
        "description": "test suite",
        "prompt_prefix": "PREFIX",
        "global_reset": {"tool_sequence": [{"tool": "wait_ms", "args": {"ms": 100}}]},
        "tasks": [
            {
                "id": "task_one",
                "category": "single_step",
                "description_for_judge": "judge desc",
                "prompt": "do thing",
                "rubric": [{"id": "r1", "check": "x"}],
                "hard_assertions": {"min_tool_calls": 1, "max_tool_calls": 5},
                "setup": {"tool_sequence": [{"tool": "shell", "args": {"command": "true"}}]},
            }
        ],
    }
    suite_path = suites_dir / f"{name}.json"
    suite_path.write_text(json.dumps(suite))
    return suite_path


def test_load_aiden_suite_returns_one_task_per_taskspec(
    run_aiden_module, tmp_path, monkeypatch
):
    suites_dir = tmp_path / "suites"
    suites_dir.mkdir()
    _write_minimal_suite(suites_dir, "demo")

    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    tasks = run_aiden_module._load_aiden_suite_as_mobilegym_tasks("demo")

    assert len(tasks) == 1
    assert tasks[0].task_id == "demo.task_one"


def test_load_aiden_suite_prepends_prompt_prefix(
    run_aiden_module, tmp_path, monkeypatch
):
    suites_dir = tmp_path / "suites"
    suites_dir.mkdir()
    _write_minimal_suite(suites_dir, "demo")

    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    tasks = run_aiden_module._load_aiden_suite_as_mobilegym_tasks("demo")

    assert tasks[0].instruction.startswith("PREFIX")
    assert "do thing" in tasks[0].instruction


def test_load_aiden_suite_preserves_metadata(
    run_aiden_module, tmp_path, monkeypatch
):
    suites_dir = tmp_path / "suites"
    suites_dir.mkdir()
    _write_minimal_suite(suites_dir, "demo")

    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    tasks = run_aiden_module._load_aiden_suite_as_mobilegym_tasks("demo")

    md = tasks[0].metadata
    assert md["category"] == "single_step"
    assert md["rubric"][0]["id"] == "r1"
    assert md["hard_assertions"]["min_tool_calls"] == 1
    assert md["setup"] is not None
    assert md["global_reset"]["tool_sequence"][0]["tool"] == "wait_ms"


def test_load_aiden_suite_missing_raises(run_aiden_module, tmp_path, monkeypatch):
    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    with pytest.raises(run_aiden_module.LauncherError, match="Aiden suite not found"):
        run_aiden_module._load_aiden_suite_as_mobilegym_tasks("does_not_exist")
```

- [ ] **Step 3: Run the test to verify it fails**

Run:

```bash
python3 -m pytest tests/benchmark/test_mobilegym_aiden_suite_adapter.py -v
```

Expected: FAIL with `AttributeError: module 'run_aiden' has no attribute '_load_aiden_suite_as_mobilegym_tasks'`.

- [ ] **Step 4: Commit the failing test**

```bash
git add tests/benchmark/__init__.py tests/benchmark/test_mobilegym_aiden_suite_adapter.py
git commit -m "test(benchmark): add failing tests for Aiden→MobileGym suite adapter"
```

---

## Task 2: Python adapter — implement `_load_aiden_suite_as_mobilegym_tasks` + `_convert_task`

**Files:**

- Modify: `benchmark/mobilegym/scripts/run_aiden.py`

The MobileGym `Task` class lives in `bench_env` and has a complex constructor. To keep the adapter independent of MobileGym internals, define a lightweight `MobileGymTaskAdapter` dataclass that exposes the same attributes the agent reads (`task_id`, `instruction`, plus our `metadata`). The MobileGym `SerialRunner` uses duck-typed access on tasks; this adapter will be substituted at runtime when `--aiden-suite` is used.

If a downstream MobileGym call requires a specific `Task` subclass at runtime (e.g., `isinstance` check), Task 2.1 below adds a fallback. For now we trust the duck-typing path and verify in Task 6 (e2e).

- [ ] **Step 1: Add the adapter dataclass and helpers near the top of `run_aiden.py`**

Locate the imports block at the top of `benchmark/mobilegym/scripts/run_aiden.py` (lines 1-20). After the existing `import` lines and the constants block (`BENCHMARK_ROOT`, etc.), insert:

```python
import dataclasses as dc
from typing import Any as _Any
```

(Skip if these are already imported.)

Then **after** the line `DEFAULT_ENV_URL = "http://localhost:4173"` (around line 19), add:

```python


@dc.dataclass
class MobileGymTaskAdapter:
    """Duck-typed Task object returned to MobileGym SerialRunner when running
    an Aiden JSON suite. Exposes the attributes the runner and adapter read.
    """
    task_id: str
    instruction: str
    metadata: dict[_Any, _Any]
    # MobileGym sometimes reads `.id` / `.goal`; alias them.

    @property
    def id(self) -> str:
        return self.task_id

    @property
    def goal(self) -> str:
        return self.instruction


def _load_aiden_suite_as_mobilegym_tasks(suite_name: str) -> list[MobileGymTaskAdapter]:
    """Load benchmark/suites/<suite_name>.json and convert tasks for MobileGym."""
    suite_path = BENCHMARK_ROOT / "suites" / f"{suite_name}.json"
    if not suite_path.exists():
        raise LauncherError(f"Aiden suite not found: {suite_path}")

    benchmark_root_str = str(BENCHMARK_ROOT)
    if benchmark_root_str not in sys.path:
        sys.path.insert(0, benchmark_root_str)
    from runner.suite import load_suite  # type: ignore[import-not-found]

    aiden_suite = load_suite(suite_path)
    return [_convert_task(aiden_suite, t) for t in aiden_suite.tasks]


def _convert_task(suite: _Any, task: _Any) -> MobileGymTaskAdapter:
    """Convert one Aiden TaskSpec into a MobileGymTaskAdapter."""
    full_id = f"{suite.name}.{task.id}"
    if suite.prompt_prefix:
        instruction = f"{suite.prompt_prefix}\n\n{task.prompt}"
    else:
        instruction = task.prompt

    return MobileGymTaskAdapter(
        task_id=full_id,
        instruction=instruction,
        metadata={
            "category": task.category,
            "description_for_judge": task.description_for_judge,
            "rubric": [dc.asdict(r) for r in task.rubric],
            "hard_assertions": dc.asdict(task.hard_assertions),
            "setup": task.setup,
            "global_reset": suite.global_reset,
            "expected_answer": task.expected_answer,
            "answer_format": task.answer_format,
            "expected_recalled_memory_ids": task.expected_recalled_memory_ids,
            "aiden_suite_name": suite.name,
            "aiden_task_id": task.id,
        },
    )
```

- [ ] **Step 2: Run the test to verify it passes**

Run:

```bash
python3 -m pytest tests/benchmark/test_mobilegym_aiden_suite_adapter.py -v
```

Expected: 4 PASS.

- [ ] **Step 3: Verify run_aiden.py still parses (no syntax error)**

Run:

```bash
python3 -c "import importlib.util; importlib.util.spec_from_file_location('m', 'benchmark/mobilegym/scripts/run_aiden.py').loader.exec_module(importlib.util.module_from_spec(importlib.util.spec_from_file_location('m', 'benchmark/mobilegym/scripts/run_aiden.py')))" && echo OK
```

Expected: `OK`.

- [ ] **Step 4: Commit**

```bash
git add benchmark/mobilegym/scripts/run_aiden.py
git commit -m "feat(benchmark): add Aiden suite adapter helpers in run_aiden.py"
```

---

## Task 3: Python adapter — wire `--aiden-suite` into `run_aiden.py`

**Files:**

- Modify: `benchmark/mobilegym/scripts/run_aiden.py`

- [ ] **Step 1: Add `--aiden-suite` CLI flag**

In `build_parser()` (around line 26), find the `target` argument group and add a new argument **after** the existing `--suite` line:

```python
target.add_argument(
    "--aiden-suite",
    help="Run an Aiden JSON suite from benchmark/suites/<name>.json. "
         "Tasks are converted to MobileGym format on the fly. "
         "Mutually exclusive with --task-id/--suite/--split.",
)
```

- [ ] **Step 2: Update `_validate_selection` to accept `--aiden-suite`**

Replace the body of `_validate_selection` (around line 273) with:

```python
def _validate_selection(args: argparse.Namespace) -> None:
    selectors = [args.task_id, args.suite, args.split, args.aiden_suite]
    if not any(selectors):
        raise LauncherError(
            "select at least one task with --task-id, --suite, --split, or --aiden-suite"
        )
    if args.aiden_suite and (args.task_id or args.suite or args.split):
        raise LauncherError(
            "--aiden-suite is mutually exclusive with --task-id/--suite/--split"
        )
    if args.shard_index >= args.shard_count:
        raise LauncherError("--shard-index must be less than --shard-count")
```

- [ ] **Step 3: Use the adapter in `_run_serial` task loading**

Find `_run_serial` (around line 172). Replace the line `tasks = factory.load_tasks(config)` with:

```python
    if args.aiden_suite:
        tasks = _load_aiden_suite_as_mobilegym_tasks(args.aiden_suite)
    else:
        tasks = factory.load_tasks(config)
```

- [ ] **Step 4: Add a CLI smoke test**

Append to `tests/benchmark/test_mobilegym_aiden_suite_adapter.py`:

```python


def test_validate_selection_rejects_combined_flags(run_aiden_module):
    import argparse
    args = argparse.Namespace(
        task_id="x", suite=None, split=None, aiden_suite="demo",
        shard_index=0, shard_count=1,
    )
    with pytest.raises(run_aiden_module.LauncherError, match="mutually exclusive"):
        run_aiden_module._validate_selection(args)


def test_validate_selection_accepts_aiden_suite_alone(run_aiden_module):
    import argparse
    args = argparse.Namespace(
        task_id=None, suite=None, split=None, aiden_suite="demo",
        shard_index=0, shard_count=1,
    )
    # Should not raise
    run_aiden_module._validate_selection(args)
```

- [ ] **Step 5: Run the tests**

Run:

```bash
python3 -m pytest tests/benchmark/test_mobilegym_aiden_suite_adapter.py -v
```

Expected: 6 PASS.

- [ ] **Step 6: Verify the CLI parses the new flag**

Run:

```bash
python3 benchmark/mobilegym/scripts/run_aiden.py --help 2>&1 | grep aiden-suite
```

Expected: a line beginning with `--aiden-suite ...`.

- [ ] **Step 7: Commit**

```bash
git add benchmark/mobilegym/scripts/run_aiden.py tests/benchmark/test_mobilegym_aiden_suite_adapter.py
git commit -m "feat(benchmark): add --aiden-suite flag to run_aiden.py"
```

---

## Task 4: Shell wrapper — pass through `--aiden-suite` in `parallel_run.sh`

**Files:**

- Modify: `benchmark/mobilegym/docker/parallel_run.sh`

The shell script currently dispatches `--task-id`, `--suite`, and `--suites`. Add a new `--aiden-suite` mode that calls `run_aiden.py` once per worker shard.

- [ ] **Step 1: Read the current dispatch block**

Run:

```bash
grep -n '"--suite"\|"--suites"\|"--task-id"' benchmark/mobilegym/docker/parallel_run.sh
```

Note the line numbers around the `if [[ "$1" == "--suite" ]]` block (~line 416).

- [ ] **Step 2: Add `--aiden-suite` dispatch**

In `benchmark/mobilegym/docker/parallel_run.sh`, find the block:

```bash
if [[ "$1" == "--suite" ]]; then
```

**Immediately before** that line, insert a new branch:

```bash
if [[ "$1" == "--aiden-suite" ]]; then
    if [[ -z "${2:-}" ]]; then
        echo "Error: --aiden-suite requires a suite name" >&2
        exit 2
    fi
    AIDEN_SUITE="$2"
    SUITE_NAME="aiden_${AIDEN_SUITE}"
    EXTRA_ARGS=("--aiden-suite" "$AIDEN_SUITE")
    DISPATCH_MODE="aiden_suite"
elif [[ "$1" == "--suite" ]]; then
```

Then change the closing `fi` of the original `if/elif` chain to keep matching (the `elif`s for `--suite`/`--suites` already exist). Verify `EXTRA_ARGS` flows into the per-worker invocation in the existing dispatcher; if the script uses `--suite "$suite"` directly inside the worker loop, add a parallel branch using `EXTRA_ARGS`.

- [ ] **Step 3: Read the worker invocation to confirm wiring**

Run:

```bash
grep -n -A3 'task_id\|--suite' benchmark/mobilegym/docker/parallel_run.sh | head -60
```

Identify how each worker is launched (e.g. `docker compose run --rm test --suite "$suite"`). For the new mode, ensure the worker invocation passes `--aiden-suite "$AIDEN_SUITE"` instead.

- [ ] **Step 4: Adjust the worker invocation for `aiden_suite` mode**

Locate the `--suite` worker invocation (around line 282). Add a sibling branch above it:

```bash
        if [[ "${DISPATCH_MODE:-}" == "aiden_suite" ]]; then
            docker compose "${COMPOSE_FILE_ARGS[@]}" run --rm test \
                --aiden-suite "$AIDEN_SUITE" \
                $LIMIT_ARG \
                --shard-index "$shard_index" \
                --shard-count "$PARALLEL" \
                --aiden-control-token "$(cat ../config/control_token)"
        elif [[ -n "${suite:-}" ]]; then
```

(Adapt the wrapping `if/elif/fi` so the existing `--suite` branch becomes the `elif`.)

If the existing structure differs from the assumption above, **read the file first and adapt**: the goal is one new branch that fires when `DISPATCH_MODE=aiden_suite`, otherwise unchanged.

- [ ] **Step 5: Update usage / examples in the script**

Find the usage line:

```bash
echo "Usage: ./parallel_run.sh <task-id> [task-id...] | --suite <suite> | --suites <suite-a,suite-b>"
```

Replace with:

```bash
echo "Usage: ./parallel_run.sh <task-id> [task-id...] | --suite <suite> | --suites <suite-a,suite-b> | --aiden-suite <name>"
```

Add an example:

```bash
echo "  PARALLEL=4 ./parallel_run.sh --aiden-suite memory_v1"
```

- [ ] **Step 6: Syntax check**

Run:

```bash
bash -n benchmark/mobilegym/docker/parallel_run.sh && echo OK
```

Expected: `OK`.

- [ ] **Step 7: Commit**

```bash
git add benchmark/mobilegym/docker/parallel_run.sh
git commit -m "feat(benchmark): pass --aiden-suite through parallel_run.sh"
```

---

## Task 5: Go server — extend `handleBenchmarkSuites` with `mode` param

**Files:**

- Modify: `src/agent/internal/agent/benchmark.go`
- Modify: `src/agent/internal/agent/benchmark_test.go`

The current handler always returns Aiden JSON suites. Add `?mode=mobilegym` support: when set, also include MobileGym built-in suites parsed from `benchmark/mobilegym/suites/all_tasks.txt`.

- [ ] **Step 1: Write failing tests first**

Open `src/agent/internal/agent/benchmark_test.go`. Append:

```go
func TestHandleBenchmarkSuites_AidenModeOmitsBuiltins(t *testing.T) {
	root := t.TempDir()
	suites := filepath.Join(root, "suites")
	os.MkdirAll(suites, 0o755)
	os.WriteFile(filepath.Join(suites, "memory_v1.json"),
		[]byte(`{"name":"memory_v1","tasks":[]}`), 0o644)

	// MobileGym all_tasks.txt should be ignored in aiden mode
	mgDir := filepath.Join(root, "mobilegym", "suites")
	os.MkdirAll(mgDir, 0o755)
	os.WriteFile(filepath.Join(mgDir, "all_tasks.txt"),
		[]byte("clock.AddAlarm\nclock.CountAlarms\nalipay.CheckBalance\n"), 0o644)

	s := &Server{benchmarkDir: root}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/benchmark/suites?mode=aiden", nil)
	s.handleBenchmarkSuites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	for _, item := range got {
		if item["type"] == "mobilegym_builtin" {
			t.Fatalf("aiden mode should not include builtins, got %+v", got)
		}
	}
	if len(got) != 1 || got[0]["name"] != "memory_v1" {
		t.Fatalf("expected only memory_v1, got %+v", got)
	}
}

func TestHandleBenchmarkSuites_MobileGymModeIncludesBuiltins(t *testing.T) {
	root := t.TempDir()
	suites := filepath.Join(root, "suites")
	os.MkdirAll(suites, 0o755)
	os.WriteFile(filepath.Join(suites, "memory_v1.json"),
		[]byte(`{"name":"memory_v1","tasks":[]}`), 0o644)

	mgDir := filepath.Join(root, "mobilegym", "suites")
	os.MkdirAll(mgDir, 0o755)
	os.WriteFile(filepath.Join(mgDir, "all_tasks.txt"),
		[]byte("clock.AddAlarm\nclock.CountAlarms\nalipay.CheckBalance\n"), 0o644)

	s := &Server{benchmarkDir: root}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/benchmark/suites?mode=mobilegym", nil)
	s.handleBenchmarkSuites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)

	types := map[string]int{}
	names := map[string]int{}
	for _, item := range got {
		types[item["type"].(string)]++
		names[item["name"].(string)] = int(item["task_count"].(float64))
	}
	if types["aiden"] != 1 {
		t.Fatalf("expected 1 aiden suite, got %d (%+v)", types["aiden"], got)
	}
	if types["mobilegym_builtin"] != 2 {
		t.Fatalf("expected 2 builtin suites (clock, alipay), got %d (%+v)",
			types["mobilegym_builtin"], got)
	}
	if names["clock"] != 2 || names["alipay"] != 1 {
		t.Fatalf("task counts wrong: %+v", names)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd src/agent && go test ./internal/agent/ -run 'TestHandleBenchmarkSuites_(AidenMode|MobileGymMode)' -v
```

Expected: FAIL — both new tests fail because the handler ignores `?mode=` and never reads `all_tasks.txt`.

- [ ] **Step 3: Update `suiteListItem` struct in `benchmark.go`**

Find the `suiteListItem` struct (around line 54). Replace it with:

```go
type suiteListItem struct {
	Name        string `json:"name"`
	Path        string `json:"path,omitempty"`
	Custom      bool   `json:"custom"`
	Type        string `json:"type"`                 // "aiden" | "mobilegym_builtin"
	TaskCount   int    `json:"task_count,omitempty"`
	Description string `json:"description,omitempty"`
	Concurrent  bool   `json:"concurrent"`
}
```

- [ ] **Step 4: Rewrite `handleBenchmarkSuites` to handle `mode`**

Replace the body of `handleBenchmarkSuites` (around line 60) with:

```go
func (s *Server) handleBenchmarkSuites(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "aiden"
	}

	items := []suiteListItem{}
	items = append(items, scanAidenSuites(s.benchmarkDir, mode == "mobilegym")...)
	if mode == "mobilegym" {
		items = append(items, scanMobileGymBuiltinSuites(s.benchmarkDir)...)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Type != items[j].Type {
			// aiden suites first, builtins after
			return items[i].Type == "aiden"
		}
		return items[i].Name < items[j].Name
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func scanAidenSuites(benchmarkDir string, concurrent bool) []suiteListItem {
	suitesDir := filepath.Join(benchmarkDir, "suites")
	var items []suiteListItem
	filepath.Walk(suitesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "._") || !strings.HasSuffix(base, ".json") {
			return nil
		}
		rel, _ := filepath.Rel(suitesDir, path)
		items = append(items, suiteListItem{
			Name:       strings.TrimSuffix(base, ".json"),
			Path:       path,
			Custom:     strings.HasPrefix(rel, "custom"+string(filepath.Separator)),
			Type:       "aiden",
			Concurrent: concurrent,
			TaskCount:  countAidenTasks(path),
		})
		return nil
	})
	return items
}

func countAidenTasks(suitePath string) int {
	data, err := os.ReadFile(suitePath)
	if err != nil {
		return 0
	}
	var raw struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return 0
	}
	return len(raw.Tasks)
}

func scanMobileGymBuiltinSuites(benchmarkDir string) []suiteListItem {
	allTasks := filepath.Join(benchmarkDir, "mobilegym", "suites", "all_tasks.txt")
	data, err := os.ReadFile(allTasks)
	if err != nil {
		return nil
	}
	counts := map[string]int{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ".", 2)
		if len(parts) != 2 {
			continue
		}
		counts[parts[0]]++
	}
	items := make([]suiteListItem, 0, len(counts))
	for name, n := range counts {
		items = append(items, suiteListItem{
			Name:       name,
			Type:       "mobilegym_builtin",
			TaskCount:  n,
			Concurrent: true,
		})
	}
	return items
}
```

- [ ] **Step 5: Run new tests + existing suite tests**

Run:

```bash
cd src/agent && go test ./internal/agent/ -run 'TestHandleBenchmarkSuites' -v
```

Expected: all PASS (4 tests: ListsJSON, NoBenchmarkDir, AidenModeOmitsBuiltins, MobileGymModeIncludesBuiltins).

- [ ] **Step 6: Run the whole package**

Run:

```bash
cd src/agent && go test ./internal/agent/ -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go
git commit -m "feat(benchmark): mode-aware suite discovery (aiden + mobilegym_builtin)"
```

---

## Task 6: Go server — extend `handleBenchmarkRun` with mode dispatch

**Files:**

- Modify: `src/agent/internal/agent/benchmark_runner.go`
- Modify: `src/agent/internal/agent/benchmark_test.go`

- [ ] **Step 1: Write failing dispatch tests**

Append to `src/agent/internal/agent/benchmark_test.go`:

```go
func TestHandleBenchmarkRun_AidenModeDefault(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	captured := struct {
		called bool
		suite  string
	}{}
	s := &Server{
		benchmarkDir:        root,
		benchmarkStatePath:  statePath,
		benchmarkLauncher:   func(suite, judge, apiKey string) error { captured.called = true; captured.suite = suite; return nil },
	}

	body := `{"suite":"memory_v1.json"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !captured.called || captured.suite != "memory_v1.json" {
		t.Fatalf("expected aiden launcher invoked with memory_v1.json, got %+v", captured)
	}
}

func TestHandleBenchmarkRun_MobileGymMode(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	captured := struct {
		called    bool
		suite     string
		suiteType string
		parallel  int
		limit     int
	}{}
	s := &Server{
		benchmarkDir:           root,
		benchmarkStatePath:     statePath,
		benchmarkMobileGymLauncher: func(suite, suiteType string, parallel, limit int) error {
			captured.called = true
			captured.suite = suite
			captured.suiteType = suiteType
			captured.parallel = parallel
			captured.limit = limit
			return nil
		},
	}

	body := `{"suite":"memory_v1","suite_type":"aiden","mode":"mobilegym","parallel":4,"limit":10}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !captured.called {
		t.Fatalf("expected mobilegym launcher invoked, got %+v", captured)
	}
	if captured.suite != "memory_v1" || captured.suiteType != "aiden" ||
		captured.parallel != 4 || captured.limit != 10 {
		t.Fatalf("unexpected mobilegym launch args: %+v", captured)
	}
}

func TestHandleBenchmarkRun_MobileGymBuiltin(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	captured := struct {
		suite     string
		suiteType string
	}{}
	s := &Server{
		benchmarkDir:               root,
		benchmarkStatePath:         statePath,
		benchmarkMobileGymLauncher: func(suite, suiteType string, parallel, limit int) error {
			captured.suite = suite
			captured.suiteType = suiteType
			return nil
		},
	}

	body := `{"suite":"clock","suite_type":"mobilegym_builtin","mode":"mobilegym","parallel":2}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if captured.suite != "clock" || captured.suiteType != "mobilegym_builtin" {
		t.Fatalf("unexpected mobilegym launch args: %+v", captured)
	}
}
```

- [ ] **Step 2: Run failing tests**

Run:

```bash
cd src/agent && go test ./internal/agent/ -run 'TestHandleBenchmarkRun_(AidenModeDefault|MobileGymMode|MobileGymBuiltin)' -v
```

Expected: FAIL (compile error — `benchmarkMobileGymLauncher` field doesn't exist).

- [ ] **Step 3: Add `benchmarkMobileGymLauncher` field to `Server`**

Open `src/agent/internal/agent/server.go` and find the `Server` struct field `benchmarkLauncher`. Add a sibling line below it:

```go
	benchmarkMobileGymLauncher func(suite, suiteType string, parallel, limit int) error
```

- [ ] **Step 4: Rewrite `handleBenchmarkRun` to dispatch by mode**

Replace the existing `handleBenchmarkRun` in `src/agent/internal/agent/benchmark_runner.go` with:

```go
func (s *Server) handleBenchmarkRun(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Suite     string `json:"suite"`
		SuiteType string `json:"suite_type"`
		Mode      string `json:"mode"`
		Parallel  int    `json:"parallel"`
		Limit     int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Suite == "" {
		http.Error(w, `{"error":"missing suite field"}`, http.StatusBadRequest)
		return
	}

	if req.Mode == "" {
		req.Mode = "aiden"
	}

	statePath := s.benchmarkStatePath
	if statePath == "" {
		statePath = filepath.Join(s.benchmarkDir, "state.json")
	}

	stateJSON, _ := json.Marshal(map[string]any{
		"status":     "running",
		"mode":       req.Mode,
		"suite":      req.Suite,
		"suite_type": req.SuiteType,
		"parallel":   req.Parallel,
	})
	if err := os.WriteFile(statePath, stateJSON, 0o644); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}

	switch req.Mode {
	case "aiden":
		launch := s.benchmarkLauncher
		if launch == nil {
			launch = s.launchBenchmarkRunner
		}
		apiKey := ""
		judge := ""
		if s.runtime != nil {
			apiKey = s.runtime.config.Model.APIKey
			judge = s.runtime.config.Benchmark.JudgeModel
		}
		if err := launch(req.Suite, judge, apiKey); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"launch failed: %s"}`, err), http.StatusInternalServerError)
			return
		}
	case "mobilegym":
		if req.Parallel < 1 {
			req.Parallel = 1
		}
		launch := s.benchmarkMobileGymLauncher
		if launch == nil {
			launch = s.launchMobileGymRunner
		}
		if err := launch(req.Suite, req.SuiteType, req.Parallel, req.Limit); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"mobilegym launch failed: %s"}`, err), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, fmt.Sprintf(`{"error":"unknown mode %q"}`, req.Mode), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"status":"running"}`))
}
```

- [ ] **Step 5: Add `launchMobileGymRunner` (the real implementation)**

In the same file, add **after** `launchBenchmarkRunner`:

```go
func (s *Server) launchMobileGymRunner(suite, suiteType string, parallel, limit int) error {
	var suiteFlag string
	switch suiteType {
	case "aiden":
		suiteFlag = fmt.Sprintf("--aiden-suite %s", shellQuote(suite))
	case "mobilegym_builtin", "":
		suiteFlag = fmt.Sprintf("--suite %s", shellQuote(suite))
	default:
		return fmt.Errorf("unknown suite_type %q", suiteType)
	}

	limitFlag := ""
	if limit > 0 {
		limitFlag = fmt.Sprintf("--limit %d", limit)
	}

	statePath := filepath.Join(s.benchmarkDir, "state.json")

	script := fmt.Sprintf(`cd %s/benchmark/mobilegym/docker && `+
		`(`+
		`echo 'Starting MobileGym benchmark...' > /tmp/mobilegym_run.log; `+
		`PARALLEL=%d ./parallel_run.sh %s %s `+
		`>> /tmp/mobilegym_run.log 2>&1; `+
		`echo '{"status":"idle"}' > %s) &`,
		s.benchmarkDir, parallel, suiteFlag, limitFlag, shellQuote(statePath))

	return exec.Command("sh", "-c", script).Start()
}
```

- [ ] **Step 6: Run the dispatch tests**

Run:

```bash
cd src/agent && go test ./internal/agent/ -run 'TestHandleBenchmarkRun' -v
```

Expected: all PASS.

- [ ] **Step 7: Run the full agent package tests**

Run:

```bash
cd src/agent && go test ./internal/agent/ -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add src/agent/internal/agent/benchmark_runner.go src/agent/internal/agent/server.go src/agent/internal/agent/benchmark_test.go
git commit -m "feat(benchmark): dispatch run requests by mode (aiden vs mobilegym)"
```

---

## Task 7: Go server — per-mode log path in `handleBenchmarkLog`

**Files:**

- Modify: `src/agent/internal/agent/benchmark.go`

- [ ] **Step 1: Update `handleBenchmarkLog` to read `?mode=`**

Find `handleBenchmarkLog` (around line 238). Replace the early section that picks `logPath`:

Existing:

```go
func (s *Server) handleBenchmarkLog(w http.ResponseWriter, r *http.Request) {
	logPath := s.benchmarkLogPath
	if logPath == "" {
		logPath = "/tmp/benchmark_run.log"
	}
```

Replace with:

```go
func (s *Server) handleBenchmarkLog(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	logPath := s.benchmarkLogPath
	if logPath == "" {
		if mode == "mobilegym" {
			logPath = "/tmp/mobilegym_run.log"
		} else {
			logPath = "/tmp/benchmark_run.log"
		}
	}
```

- [ ] **Step 2: Add a unit test**

Append to `src/agent/internal/agent/benchmark_test.go`:

```go
func TestHandleBenchmarkLog_MobileGymMode(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "mobilegym_run.log")
	os.WriteFile(logFile, []byte("hello mobilegym"), 0o644)

	s := &Server{benchmarkLogPath: logFile}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/benchmark/log?mode=mobilegym", nil)
	s.handleBenchmarkLog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hello mobilegym") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
```

- [ ] **Step 3: Run the test**

Run:

```bash
cd src/agent && go test ./internal/agent/ -run 'TestHandleBenchmarkLog' -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add src/agent/internal/agent/benchmark.go src/agent/internal/agent/benchmark_test.go
git commit -m "feat(benchmark): per-mode log path in /benchmark/log"
```

---

## Task 8: Web UI — mode toggle and conditional controls

**Files:**

- Modify: `src/agent/internal/agent/benchmark_html.go`

The current page only shows a flat `<select>`. We add: (1) mode radio, (2) suite-group rendering, (3) parallel/limit inputs visible only in MobileGym mode, and update `loadSuites` and `startRun` accordingly.

- [ ] **Step 1: Add the mode toggle and config row in HTML**

In `benchmark_html.go`, find the toolbar block (around line 47-52):

```html
<div class="card">
  <div class="row toolbar">
    <select id="suiteSelect">
      <option value="">Loading...</option>
    </select>
    <button id="runBtn" onclick="startRun()">Run</button>
    <button
      id="delBtn"
      class="del"
      onclick="deleteSuite()"
      style="display:none"
    >
      Delete
    </button>
    <span id="statusText" class="status">idle</span>
  </div>
</div>
```

Replace it with:

```html
<div class="card">
  <div class="row toolbar">
    <label
      ><input
        type="radio"
        name="mode"
        value="aiden"
        checked
        onchange="onModeChange()"
      />
      Aiden Native</label
    >
    <label
      ><input
        type="radio"
        name="mode"
        value="mobilegym"
        onchange="onModeChange()"
      />
      MobileGym</label
    >
  </div>
  <div class="row toolbar">
    <select id="suiteSelect">
      <option value="">Loading...</option>
    </select>
    <span id="mgConfig" style="display:none">
      <label class="muted"
        >Parallel
        <input
          type="number"
          id="parallelInput"
          value="4"
          min="1"
          max="16"
          style="width:60px"
      /></label>
      <label class="muted"
        >Limit
        <input
          type="number"
          id="limitInput"
          placeholder="(all)"
          min="1"
          style="width:80px"
      /></label>
    </span>
    <button id="runBtn" onclick="startRun()">Run</button>
    <button
      id="delBtn"
      class="del"
      onclick="deleteSuite()"
      style="display:none"
    >
      Delete
    </button>
    <span id="statusText" class="status">idle</span>
  </div>
  <div id="progressBox" class="progress">
    <div class="progress-bar">
      <div id="progressFill" class="progress-fill"></div>
    </div>
  </div>
</div>
```

- [ ] **Step 2: Add JS functions for mode handling**

In `benchmark_html.go`, find the `<script>` block (line 95) and add new functions **before** the existing `function load(){...}` line. Specifically, locate `function load(){loadSuites();loadRuns();loadStatus()}` and insert above it:

```javascript
function getMode() {
  var els = document.getElementsByName("mode");
  for (var i = 0; i < els.length; i++) {
    if (els[i].checked) return els[i].value;
  }
  return "aiden";
}
function onModeChange() {
  var mg = getMode() === "mobilegym";
  document.getElementById("mgConfig").style.display = mg
    ? "inline-block"
    : "none";
  loadSuites();
}
```

- [ ] **Step 3: Update `loadSuites` to honor mode and group rendering**

Replace `loadSuites` (around line 126) with:

```javascript
function loadSuites() {
  var mode = getMode();
  fetch("/benchmark/suites?mode=" + encodeURIComponent(mode))
    .then((r) => r.json())
    .then((d) => {
      var s = document.getElementById("suiteSelect");
      s.innerHTML = "";
      suiteIndex = {};
      if (!d || !d.length) {
        var o = document.createElement("option");
        o.value = "";
        o.textContent = "(no suites)";
        s.appendChild(o);
        syncDelBtn();
        return;
      }
      var groups = { aiden: [], mobilegym_builtin: [] };
      d.forEach(function (x) {
        (groups[x.type] || groups.aiden).push(x);
      });
      function appendGroup(label, arr) {
        if (!arr.length) return;
        var og = document.createElement("optgroup");
        og.label = label;
        arr.forEach(function (x) {
          var o = document.createElement("option");
          o.value = x.path || x.name;
          var n =
            x.name +
            (x.task_count ? " (" + x.task_count + " tasks)" : "") +
            (x.custom ? " (custom)" : "");
          o.textContent = n;
          og.appendChild(o);
          suiteIndex[x.path || x.name] = x;
        });
        s.appendChild(og);
      }
      appendGroup("Aiden Suites", groups.aiden);
      if (mode === "mobilegym")
        appendGroup("MobileGym Built-in", groups.mobilegym_builtin);
      syncDelBtn();
    });
}
```

- [ ] **Step 4: Update `startRun` to send mode + config**

Replace `startRun` (around line 175) with:

```javascript
function startRun() {
  var sel = document.getElementById("suiteSelect");
  var key = sel.value;
  if (!key) {
    alert("Select a suite");
    return;
  }
  var item = suiteIndex[key];
  var mode = getMode();
  var payload = {};
  if (mode === "aiden") {
    payload.suite = item.path || key;
    payload.mode = "aiden";
  } else {
    payload.suite = item.name;
    payload.suite_type = item.type;
    payload.mode = "mobilegym";
    payload.parallel =
      Number(document.getElementById("parallelInput").value) || 4;
    var lim = document.getElementById("limitInput").value;
    if (lim) payload.limit = Number(lim);
  }
  document.getElementById("runBtn").disabled = true;
  document.getElementById("statusText").textContent = "running";
  document.getElementById("statusText").className = "status running";
  fetch("/benchmark/run", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
    .then((r) => r.json())
    .then(function () {
      loadLog();
      polling = setInterval(pollStatus, 3000);
      if (!logPolling) logPolling = setInterval(loadLog, 1000);
    });
}
```

- [ ] **Step 5: Update `loadLog` to pass current mode**

Replace `loadLog` (around line 117) with:

```javascript
function loadLog() {
  fetch("/benchmark/log?mode=" + encodeURIComponent(getMode()))
    .then((r) => r.text())
    .then(function (t) {
      var box = document.getElementById("logBox");
      var atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 24;
      box.textContent = t || "No benchmark log yet.";
      if (atBottom) box.scrollTop = box.scrollHeight;
    })
    .catch(function () {});
}
```

- [ ] **Step 6: Build the agent binary to verify HTML compiles**

Run:

```bash
cd src/agent && go build ./...
```

Expected: no errors (the HTML is a Go string literal — backtick quoting must remain balanced).

- [ ] **Step 7: Verify go vet**

Run:

```bash
cd src/agent && go vet ./...
```

Expected: no warnings.

- [ ] **Step 8: Commit**

```bash
git add src/agent/internal/agent/benchmark_html.go
git commit -m "feat(benchmark): web UI mode toggle (aiden vs mobilegym) with parallel/limit"
```

---

## Task 9: Documentation updates

**Files:**

- Modify: `benchmark/mobilegym/README.md`
- Modify: `benchmark/mobilegym/suites/README.md`
- Modify: `docs/10-benchmark/README.md`

- [ ] **Step 1: Update `benchmark/mobilegym/README.md`**

Add (or replace if a similar section exists) under "测试套件":

````markdown
### 运行 Aiden JSON suite

`run_aiden.py` 现在支持 `--aiden-suite` 直接加载 `benchmark/suites/<name>.json`：

```bash
docker compose run --rm test --aiden-suite memory_v1 --limit 5
```
````

并发执行：

```bash
PARALLEL=4 ./parallel_run.sh --aiden-suite memory_v1
```

````

- [ ] **Step 2: Update `benchmark/mobilegym/suites/README.md`**

Replace the warning block at the top (lines 1-5) with:

```markdown
# 自定义测试套件

`run_aiden.py --aiden-suite <name>` 会从 `benchmark/suites/<name>.json` 加载 Aiden JSON
suite 并即时转换为 MobileGym Task。Web UI（`/benchmark`，MobileGym 模式）也会列出
这些 suite。本目录下的 YAML 暂不被加载。
````

- [ ] **Step 3: Update `docs/10-benchmark/README.md`**

Add a new section at the bottom:

```markdown
## 双模式执行

Benchmark 支持两种执行模式：

- **Aiden Native** — 通过 `benchmark/runner/main.py` 在本地 agent 上跑，串行。
- **MobileGym** — 通过 `benchmark/mobilegym/scripts/run_aiden.py` 在 Docker 模拟器上跑，
  支持并发（`PARALLEL=N ./parallel_run.sh`）。

同一份 `benchmark/suites/*.json` 在两种模式下都可执行。MobileGym 内置 suite
（clock、alipay、wechat 等）只在 MobileGym 模式可用，从 `benchmark/mobilegym/suites/all_tasks.txt`
聚合发现。

Web UI `/benchmark` 上的「Aiden Native / MobileGym」单选切换两种模式，MobileGym 模式
下额外提供并发数与任务数限制输入框。
```

- [ ] **Step 4: Commit**

```bash
git add benchmark/mobilegym/README.md benchmark/mobilegym/suites/README.md docs/10-benchmark/README.md
git commit -m "docs(benchmark): document --aiden-suite and dual-mode web UI"
```

---

## Task 10: End-to-end verification

**Files:** none (manual verification)

Because MobileGym requires Docker simulators that aren't necessarily up during plan execution, this task focuses on what _can_ be verified locally without spinning up the full stack.

- [ ] **Step 1: Run full Go test suite**

```bash
cd src/agent && go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Python test suite**

```bash
python3 -m pytest tests/ -v
```

Expected: all tests PASS (or skip if env-dependent).

- [ ] **Step 3: Smoke-test `run_aiden.py` argument parsing**

```bash
python3 benchmark/mobilegym/scripts/run_aiden.py --aiden-suite nonexistent 2>&1 | head -5
```

Expected: `Aiden suite not found:` error (or selection-validation error path) — _not_ a Python traceback. This proves `--aiden-suite` is wired into `_validate_selection` and the loader.

- [ ] **Step 4: Smoke-test the Web UI HTML output**

If the agent runs locally, restart it and `curl -s http://localhost:8080/benchmark | head -100` to verify the new mode radio renders. If the agent isn't running, just confirm the HTML compiles via `go build ./...` (already done in Task 8).

- [ ] **Step 5: Verify `parallel_run.sh` syntax and usage line**

```bash
bash -n benchmark/mobilegym/docker/parallel_run.sh && \
  benchmark/mobilegym/docker/parallel_run.sh --help 2>&1 | grep aiden-suite || \
  grep aiden-suite benchmark/mobilegym/docker/parallel_run.sh
```

Expected: at least one match line containing `--aiden-suite`.

- [ ] **Step 6: Final commit if any docstring/comment cleanup happened**

If no changes are pending:

```bash
git status
```

Expected: clean tree. Otherwise commit any leftover whitespace/doc changes.

---

## Out of Scope (follow-up tickets)

These items appear in the spec but are intentionally deferred to keep this plan reviewable:

- **Aiden judge / hard_assertions integration in MobileGym mode** (spec §3.5). The current adapter preserves rubric/hard_assertions in `metadata` but MobileGym still uses its own evaluator. A follow-up plan will add a post-task hook that calls `runner.judge` against the trace and writes a unified report.
- **`setup` / `global_reset` execution in MobileGym mode** (spec §3.4). Metadata is preserved; actually invoking the Aiden daemon `tool_sequence` per task is a separate concern that depends on confirming the right MobileGym `SerialRunner` hook (or extending `aiden_go_agent.py::reset`).
- **Live progress reporting for MobileGym runs in `/benchmark/status`** — the current state.json is binary running/idle; granular progress lives in MobileGym's batch report.

These are tracked in the spec under §3.4 and §3.5 but are intentionally not implemented in this plan to keep the dispatch wiring landable on its own.
