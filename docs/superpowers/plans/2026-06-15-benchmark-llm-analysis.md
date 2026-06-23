# Benchmark LLM Analysis Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional post-run LLM RCA analyst that automatically runs after benchmark reports are generated, analyzes full run artifacts plus relevant code/log context, and writes analysis artifacts alongside the report.

**Architecture:** Add a focused `benchmark/runner/analysis.py` module that owns context collection, redaction, LLM calls, parsing, markdown rendering, and atomic artifact writes. Native runner and MobileGym report generation call the shared API after their normal reports are written, then report HTML includes an analysis section/link without changing benchmark pass/fail semantics.

**Tech Stack:** Python 3.10+, pytest, stdlib `urllib.request` OpenRouter-compatible chat completions, existing benchmark runner/report modules, bash MobileGym wrapper.

---

## File Structure

- Create `benchmark/runner/analysis.py`
  - `AnalysisConfig`, `AnalysisResult`, `AnalysisError` dataclasses/classes.
  - Env/CLI config helpers and credential fallback.
  - Deterministic redaction.
  - Native and MobileGym run artifact collectors.
  - Code-context snippet collector with denylist and byte budgets.
  - LLM client, JSON extraction, markdown rendering, atomic writes.
- Modify `benchmark/runner/main.py`
  - Add native runner flags.
  - Invoke `analyze_run()` after report generation when enabled.
  - Preserve original benchmark exit code if analysis fails.
- Modify `benchmark/runner/html_report.py`
  - Add report analysis HTML rendering from `llm_analysis.md` or `llm_analysis_error.txt`.
  - Upload analysis artifacts when present.
- Modify `benchmark/mobilegym/report.py`
  - Read analysis config from `AIDEN_BENCHMARK_*` env vars.
  - Trigger `analyze_run()` after direct/batch report generation.
  - Include analysis section/link in MobileGym report HTML.
- Modify `benchmark/mobilegym/scripts/local_launcher.py`
  - Optionally map launcher payload keys to analysis env vars.
  - Serve safe report artifacts under `/benchmark/report/<run_id>/<artifact>` if links are used.
- Modify `benchmark/mobilegym/docker/parallel_run.sh`
  - No heavy rewrite expected; verify env vars inherit into `uv run python -m mobilegym.report`.
  - Add explicit export/pass-through only if tests show the current shell environment is insufficient.
- Add tests in `benchmark/tests/test_analysis.py`.
- Add/extend tests in `benchmark/tests/test_main.py`.
- Add/extend tests in `benchmark/tests/test_html_report.py`.
- Add/extend tests in `benchmark/tests/mobilegym/test_report.py`.
- Add/extend tests in `tests/benchmark/test_mobilegym_local_launcher.py`.

## Task 1: Analysis Config, Credential Fallback, Redaction, and Rendering

**Files:**
- Create: `benchmark/runner/analysis.py`
- Test: `benchmark/tests/test_analysis.py`

- [ ] **Step 1: Write failing tests for config and credential fallback**

Add `benchmark/tests/test_analysis.py`:

```python
import json

import pytest

from runner import analysis


def test_config_from_env_enables_analysis_and_reads_limits(monkeypatch):
    monkeypatch.setenv("AIDEN_BENCHMARK_LLM_ANALYSIS", "1")
    monkeypatch.setenv("AIDEN_BENCHMARK_ANALYSIS_MODEL", "anthropic/claude-sonnet-4-6")
    monkeypatch.setenv("AIDEN_BENCHMARK_ANALYSIS_MAX_LOG_BYTES", "1234")
    monkeypatch.setenv("AIDEN_BENCHMARK_ANALYSIS_MAX_CODE_BYTES", "5678")
    monkeypatch.setenv("AIDEN_BENCHMARK_ANALYSIS_TIMEOUT_SEC", "9")

    cfg = analysis.config_from_env()

    assert cfg.enabled is True
    assert cfg.model == "anthropic/claude-sonnet-4-6"
    assert cfg.max_log_bytes == 1234
    assert cfg.max_code_bytes == 5678
    assert cfg.timeout_sec == 9


def test_resolve_analysis_api_key_uses_expected_precedence(monkeypatch):
    monkeypatch.setenv("AIDEN_BENCHMARK_ANALYSIS_API_KEY_ENV", "CUSTOM_ANALYSIS_KEY")
    monkeypatch.setenv("CUSTOM_ANALYSIS_KEY", "custom-secret")
    monkeypatch.setenv("OPENROUTER_API_KEY", "openrouter-secret")
    monkeypatch.setenv("MODEL_API_KEY", "model-secret")
    monkeypatch.setenv("AIDEN_MODEL_API_KEY", "aiden-secret")

    cfg = analysis.AnalysisConfig(enabled=True)

    assert analysis.resolve_analysis_api_key(cfg) == ("CUSTOM_ANALYSIS_KEY", "custom-secret")

    monkeypatch.delenv("AIDEN_BENCHMARK_ANALYSIS_API_KEY_ENV")
    monkeypatch.delenv("CUSTOM_ANALYSIS_KEY")
    assert analysis.resolve_analysis_api_key(cfg) == ("OPENROUTER_API_KEY", "openrouter-secret")

    monkeypatch.delenv("OPENROUTER_API_KEY")
    assert analysis.resolve_analysis_api_key(cfg) == ("MODEL_API_KEY", "model-secret")

    monkeypatch.delenv("MODEL_API_KEY")
    assert analysis.resolve_analysis_api_key(cfg) == ("AIDEN_MODEL_API_KEY", "aiden-secret")
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py -v`

Expected: FAIL because `runner.analysis` does not exist.

- [ ] **Step 3: Implement config dataclasses and credential fallback**

Add to `benchmark/runner/analysis.py`:

```python
from __future__ import annotations

import dataclasses as dc
import html
import json
import os
import re
import socket
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


DEFAULT_MODEL = "anthropic/claude-sonnet-4-6"
DEFAULT_MAX_LOG_BYTES = 64 * 1024
DEFAULT_MAX_CODE_BYTES = 128 * 1024
DEFAULT_TOTAL_CONTEXT_BYTES = 320 * 1024
DEFAULT_TIMEOUT_SEC = 180


@dc.dataclass
class AnalysisConfig:
    enabled: bool = False
    model: str = DEFAULT_MODEL
    max_log_bytes: int = DEFAULT_MAX_LOG_BYTES
    max_code_bytes: int = DEFAULT_MAX_CODE_BYTES
    total_context_bytes: int = DEFAULT_TOTAL_CONTEXT_BYTES
    timeout_sec: int = DEFAULT_TIMEOUT_SEC
    api_key_env: str | None = None


@dc.dataclass
class AnalysisResult:
    ok: bool
    markdown_path: Path | None = None
    json_path: Path | None = None
    error_path: Path | None = None
    warning: str = ""


class AnalysisError(RuntimeError):
    pass


def _int_env(name: str, default: int) -> int:
    try:
        value = int(os.environ.get(name, "") or default)
    except ValueError:
        return default
    return value if value > 0 else default


def config_from_env() -> AnalysisConfig:
    enabled = os.environ.get("AIDEN_BENCHMARK_LLM_ANALYSIS", "").strip().lower() in {"1", "true", "yes", "on"}
    return AnalysisConfig(
        enabled=enabled,
        model=os.environ.get("AIDEN_BENCHMARK_ANALYSIS_MODEL") or DEFAULT_MODEL,
        max_log_bytes=_int_env("AIDEN_BENCHMARK_ANALYSIS_MAX_LOG_BYTES", DEFAULT_MAX_LOG_BYTES),
        max_code_bytes=_int_env("AIDEN_BENCHMARK_ANALYSIS_MAX_CODE_BYTES", DEFAULT_MAX_CODE_BYTES),
        total_context_bytes=_int_env("AIDEN_BENCHMARK_ANALYSIS_TOTAL_CONTEXT_BYTES", DEFAULT_TOTAL_CONTEXT_BYTES),
        timeout_sec=_int_env("AIDEN_BENCHMARK_ANALYSIS_TIMEOUT_SEC", DEFAULT_TIMEOUT_SEC),
        api_key_env=os.environ.get("AIDEN_BENCHMARK_ANALYSIS_API_KEY_ENV") or None,
    )


def resolve_analysis_api_key(cfg: AnalysisConfig) -> tuple[str, str]:
    names = []
    if cfg.api_key_env:
        names.append(cfg.api_key_env)
    names.extend(["OPENROUTER_API_KEY", "MODEL_API_KEY", "AIDEN_MODEL_API_KEY"])
    for name in names:
        value = os.environ.get(name, "").strip()
        if value:
            return name, value
    raise AnalysisError("missing analysis API key: set OPENROUTER_API_KEY, MODEL_API_KEY, or AIDEN_MODEL_API_KEY")
```

- [ ] **Step 4: Run config tests to verify they pass**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py::test_config_from_env_enables_analysis_and_reads_limits benchmark/tests/test_analysis.py::test_resolve_analysis_api_key_uses_expected_precedence -v`

Expected: PASS.

- [ ] **Step 5: Write failing redaction and markdown rendering tests**

Append to `benchmark/tests/test_analysis.py`:

```python
def test_redact_removes_known_and_custom_secrets(monkeypatch):
    monkeypatch.setenv("AIDEN_BENCHMARK_ANALYSIS_API_KEY_ENV", "CUSTOM_ANALYSIS_KEY")
    monkeypatch.setenv("CUSTOM_ANALYSIS_KEY", "custom-secret-value")
    text = "OPENROUTER_API_KEY=sk-or-v1-secret bearer Bearer abcdefghijklmnop jwt abcdefgh.ijklmnop.qrstuvwx CUSTOM_ANALYSIS_KEY=custom-secret-value"

    redacted = analysis.redact_text(text, analysis.AnalysisConfig(enabled=True, api_key_env="CUSTOM_ANALYSIS_KEY"))

    assert "sk-or-v1-secret" not in redacted
    assert "abcdefghijklmnop" not in redacted
    assert "abcdefgh.ijklmnop.qrstuvwx" not in redacted
    assert "custom-secret-value" not in redacted
    assert "[REDACTED" in redacted


def test_render_markdown_includes_clusters_and_recommendations():
    payload = {
        "summary": "Two failures share a timeout pattern.",
        "classification_counts": {"project_code_issue": 1},
        "failure_clusters": [
            {
                "title": "Chat timeout",
                "task_ids": ["suite.task_a"],
                "suspected_cause": "Daemon did not answer",
                "category": "project_code_issue",
                "confidence": "medium",
                "evidence": ["console.log: timed out"],
            }
        ],
        "recommendations": [
            {"priority": "high", "target": "src/agent", "suggestion": "Add timeout logging"}
        ],
        "evidence_gaps": ["No daemon stderr log"],
    }

    md = analysis.render_markdown(payload)

    assert "# LLM Benchmark Analysis" in md
    assert "Two failures" in md
    assert "suite.task_a" in md
    assert "Add timeout logging" in md
    assert "No daemon stderr log" in md
```

- [ ] **Step 6: Run redaction/rendering tests to verify they fail**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py::test_redact_removes_known_and_custom_secrets benchmark/tests/test_analysis.py::test_render_markdown_includes_clusters_and_recommendations -v`

Expected: FAIL because `redact_text()` and `render_markdown()` are not implemented.

- [ ] **Step 7: Implement redaction and markdown rendering**

Add to `benchmark/runner/analysis.py`:

```python
SECRET_NAME_RE = re.compile(
    r"(?i)\b(api[_-]?key|token|password|secret|bearer|authorization|OPENROUTER_API_KEY|MODEL_API_KEY|AIDEN_MODEL_API_KEY|AIDEN_CONTROL_TOKEN)\b\s*[:=]\s*[^\s,'\"]+"
)
BEARER_RE = re.compile(r"(?i)bearer\s+[A-Za-z0-9._~+/=-]{12,}")
SK_KEY_RE = re.compile(r"\bsk-[A-Za-z0-9._-]{8,}\b")
JWTISH_RE = re.compile(r"\b[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b")


def redact_text(text: str, cfg: AnalysisConfig) -> str:
    if not text:
        return text
    redacted = SECRET_NAME_RE.sub(lambda m: m.group(0).split("=", 1)[0].split(":", 1)[0] + "=[REDACTED]", text)
    redacted = BEARER_RE.sub("Bearer [REDACTED]", redacted)
    redacted = SK_KEY_RE.sub("[REDACTED_SK_KEY]", redacted)
    redacted = JWTISH_RE.sub("[REDACTED_JWT]", redacted)
    names = [cfg.api_key_env] if cfg.api_key_env else []
    names.extend(["OPENROUTER_API_KEY", "MODEL_API_KEY", "AIDEN_MODEL_API_KEY", "AIDEN_CONTROL_TOKEN"])
    for name in names:
        if not name:
            continue
        value = os.environ.get(name, "")
        if value:
            redacted = redacted.replace(value, "[REDACTED_VALUE]")
    return redacted


def render_markdown(payload: dict[str, Any]) -> str:
    lines = ["# LLM Benchmark Analysis", "", str(payload.get("summary") or "No summary returned."), ""]
    counts = payload.get("classification_counts")
    if isinstance(counts, dict) and counts:
        lines += ["## Classification Counts", ""]
        for key, value in sorted(counts.items()):
            lines.append(f"- `{key}`: {value}")
        lines.append("")
    clusters = payload.get("failure_clusters") if isinstance(payload.get("failure_clusters"), list) else []
    lines += ["## Failure Clusters", ""]
    if not clusters:
        lines.append("- No failure clusters returned.")
    for cluster in clusters:
        if not isinstance(cluster, dict):
            continue
        title = cluster.get("title") or cluster.get("suspected_cause") or "Cluster"
        task_ids = ", ".join(str(t) for t in cluster.get("task_ids") or [])
        lines += [f"### {title}", "", f"- Category: `{cluster.get('category', 'unknown')}`", f"- Confidence: `{cluster.get('confidence', 'unknown')}`"]
        if task_ids:
            lines.append(f"- Tasks: {task_ids}")
        if cluster.get("suspected_cause"):
            lines.append(f"- Suspected cause: {cluster['suspected_cause']}")
        evidence = cluster.get("evidence") if isinstance(cluster.get("evidence"), list) else []
        for item in evidence:
            lines.append(f"- Evidence: {item}")
        lines.append("")
    recommendations = payload.get("recommendations") if isinstance(payload.get("recommendations"), list) else []
    lines += ["## Recommendations", ""]
    if not recommendations:
        lines.append("- No recommendations returned.")
    for rec in recommendations:
        if isinstance(rec, dict):
            lines.append(f"- **{rec.get('priority', 'medium')}** `{rec.get('target', '')}`: {rec.get('suggestion', '')}")
        else:
            lines.append(f"- {rec}")
    gaps = payload.get("evidence_gaps") if isinstance(payload.get("evidence_gaps"), list) else []
    if gaps:
        lines += ["", "## Evidence Gaps", ""]
        lines.extend(f"- {gap}" for gap in gaps)
    return "\n".join(lines).rstrip() + "\n"
```

- [ ] **Step 8: Run all analysis foundation tests**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py -v`

Expected: PASS for tests written so far.

- [ ] **Step 9: Checkpoint**

Run: `git diff -- benchmark/runner/analysis.py benchmark/tests/test_analysis.py`

Expected: Only Task 1 files changed. Do not commit unless the user has explicitly requested commits.

## Task 2: Native and MobileGym Context Collection with Budgets

**Files:**
- Modify: `benchmark/runner/analysis.py`
- Test: `benchmark/tests/test_analysis.py`

- [ ] **Step 1: Write failing native context collection test**

Append to `benchmark/tests/test_analysis.py`:

```python
def test_collect_context_native_run_includes_failed_task_suite_trace_and_code(tmp_path):
    repo = tmp_path / "repo"
    run_dir = repo / "benchmark" / "runs" / "run-1"
    task_dir = run_dir / "tasks" / "task_a"
    task_dir.mkdir(parents=True)
    suite_path = repo / "benchmark" / "suites" / "demo.json"
    suite_path.parent.mkdir(parents=True)
    suite_path.write_text(json.dumps({"name": "demo", "tasks": [{"id": "task_a", "prompt": "do it"}]}), encoding="utf-8")
    (run_dir / "manifest.json").write_text(json.dumps({"run_id": "run-1", "suite_path": str(suite_path), "totals": {"tasks": 1, "failed": 1}}), encoding="utf-8")
    (run_dir / "results.jsonl").write_text(json.dumps({"task_id": "task_a", "status": "failed", "metrics": {"agent_error": "WidgetError in widget_handler"}}) + "\n", encoding="utf-8")
    (task_dir / "trace.json").write_text(json.dumps({"final_response": "failed", "tool_calls": [{"tool": "shell", "input": {"command": "widget_handler"}}]}), encoding="utf-8")
    (task_dir / "history.json").write_text(json.dumps([{"type": "tool_call", "tool_name": "shell", "input": "widget_handler"}]), encoding="utf-8")
    code = repo / "src" / "agent" / "widget_handler.go"
    code.parent.mkdir(parents=True)
    code.write_text("package agent\nfunc widget_handler() {}\n", encoding="utf-8")

    ctx = analysis.collect_context(run_dir, repo, analysis.AnalysisConfig(enabled=True))

    assert ctx["run"]["kind"] == "native"
    assert ctx["suite"]["path"].endswith("demo.json")
    assert ctx["failures"][0]["task_id"] == "task_a"
    assert "WidgetError" in json.dumps(ctx)
    assert any(item["path"].endswith("widget_handler.go") for item in ctx["code"])
```

- [ ] **Step 2: Run native context test to verify it fails**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py::test_collect_context_native_run_includes_failed_task_suite_trace_and_code -v`

Expected: FAIL because `collect_context()` is not implemented.

- [ ] **Step 3: Implement native artifact discovery and basic code matching**

Add helpers to `benchmark/runner/analysis.py`:

```python
FAIL_STATUSES = {"failed", "timeout", "error", "judge_error", "unknown", "worker_failed"}
DENYLIST_NAMES = {".env", "agent.toml", "control_token", "token", "id_rsa", "id_dsa", "id_ed25519"}
DENYLIST_SUFFIXES = {".jpg", ".jpeg", ".png", ".gif", ".webp", ".pdf", ".zip", ".tar", ".gz", ".pyc", ".bin", ".pem", ".key", ".crt", ".p12"}
CODE_SUFFIXES = {".py", ".go", ".cpp", ".c", ".h", ".hpp", ".ts", ".tsx", ".js", ".jsx", ".md", ".toml", ".json", ".sh"}


def collect_context(run_dir: Path, repo_root: Path, cfg: AnalysisConfig) -> dict[str, Any]:
    warnings: list[str] = []
    if (run_dir / "manifest.json").exists():
        ctx = _collect_native_context(run_dir, repo_root, cfg, warnings)
    else:
        ctx = _collect_mobilegym_context(run_dir, repo_root, cfg, warnings)
    ctx["collection_warnings"] = warnings
    _enforce_context_budget(ctx, cfg)
    return ctx


def _read_json(path: Path, warnings: list[str]) -> dict[str, Any]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except Exception as exc:
        warnings.append(f"failed to read JSON {path}: {exc}")
        return {}
    return data if isinstance(data, dict) else {}


def _read_jsonl(path: Path, warnings: list[str]) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        warnings.append(f"failed to read JSONL {path}: {exc}")
        return rows
    for line in lines:
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except json.JSONDecodeError as exc:
            warnings.append(f"invalid JSONL row in {path}: {exc}")
            continue
        if isinstance(row, dict):
            rows.append(row)
    return rows


def _read_excerpt(path: Path, max_bytes: int, cfg: AnalysisConfig, warnings: list[str]) -> tuple[str, bool]:
    try:
        data = path.read_bytes()
    except OSError as exc:
        warnings.append(f"failed to read {path}: {exc}")
        return "", False
    truncated = len(data) > max_bytes
    if truncated:
        data = data[:max_bytes]
    return redact_text(data.decode("utf-8", errors="replace"), cfg), truncated


def _collect_native_context(run_dir: Path, repo_root: Path, cfg: AnalysisConfig, warnings: list[str]) -> dict[str, Any]:
    manifest = _read_json(run_dir / "manifest.json", warnings)
    results = _read_jsonl(run_dir / "results.jsonl", warnings) if (run_dir / "results.jsonl").exists() else []
    summary_excerpt = ""
    if (run_dir / "summary.md").exists():
        summary_excerpt, _ = _read_excerpt(run_dir / "summary.md", cfg.max_log_bytes, cfg, warnings)
    suite_path = Path(str(manifest.get("suite_path") or ""))
    suite_excerpt = ""
    if suite_path.exists():
        suite_excerpt, _ = _read_excerpt(suite_path, cfg.max_log_bytes, cfg, warnings)
    failures = []
    terms: set[str] = set()
    for row in results:
        status = str(row.get("status") or "unknown")
        if status not in FAIL_STATUSES:
            continue
        task_id = str(row.get("task_id") or row.get("id") or "")
        task_dir = run_dir / "tasks" / task_id
        trace_excerpt = ""
        history_excerpt = ""
        if (task_dir / "trace.json").exists():
            trace_excerpt, _ = _read_excerpt(task_dir / "trace.json", cfg.max_log_bytes, cfg, warnings)
        if (task_dir / "history.json").exists():
            history_excerpt, _ = _read_excerpt(task_dir / "history.json", cfg.max_log_bytes, cfg, warnings)
        judge_excerpt = ""
        if (task_dir / "judge.json").exists():
            judge_excerpt, _ = _read_excerpt(task_dir / "judge.json", cfg.max_log_bytes, cfg, warnings)
        screenshot_refs = [
            str(path.relative_to(run_dir))
            for path in sorted(task_dir.glob("**/*"))
            if path.suffix.lower() in {".jpg", ".jpeg", ".png", ".webp"}
        ]
        errors = _extract_errors(row)
        terms.update(_terms_from_text(json.dumps(row, ensure_ascii=False) + "\n" + trace_excerpt + "\n" + history_excerpt + "\n" + judge_excerpt))
        failures.append({
            "task_id": task_id,
            "status": status,
            "rubric": row.get("rubric") or [],
            "hard_assertions": row.get("hard_assertions") or {},
            "errors": errors,
            "trace_excerpt": trace_excerpt,
            "history_excerpt": history_excerpt,
            "judge_excerpt": judge_excerpt,
            "log_refs": [],
            "artifact_refs": [str((task_dir / name).relative_to(run_dir)) for name in ("trace.json", "history.json", "judge.json") if (task_dir / name).exists()] + screenshot_refs,
            "screenshot_refs": screenshot_refs,
        })
    return {
        "run": {"id": str(manifest.get("run_id") or run_dir.name), "kind": "native", "suite": str(manifest.get("suite_path") or ""), "totals": manifest.get("totals") or {}},
        "suite": {"path": str(suite_path), "content_excerpt": suite_excerpt},
        "summary_excerpt": summary_excerpt,
        "failures": failures,
        "logs": [],
        "code": _collect_code_context(repo_root, terms, cfg, warnings),
    }
```

- [ ] **Step 4: Implement term extraction, errors, code snippets, and budget enforcement**

Continue in `benchmark/runner/analysis.py`:

```python
def _extract_errors(row: dict[str, Any]) -> list[str]:
    errors = []
    metrics = row.get("metrics") if isinstance(row.get("metrics"), dict) else {}
    for key in ("error", "agent_error", "judge_error"):
        value = metrics.get(key) or row.get(key)
        if value:
            errors.append(f"{key}: {value}")
    execution = row.get("execution") if isinstance(row.get("execution"), dict) else {}
    for key in ("error", "stop_reason"):
        if execution.get(key):
            errors.append(f"execution.{key}: {execution[key]}")
    return errors


def _terms_from_text(text: str) -> set[str]:
    terms = set()
    for token in re.findall(r"[A-Za-z_][A-Za-z0-9_./-]{3,80}", text):
        if "/" in token:
            terms.add(token.rsplit("/", 1)[-1])
        if token.lower() in {"error", "failed", "timeout", "status", "tool_call", "response"}:
            continue
        terms.add(token)
    return {term for term in terms if len(term) >= 4}


def _safe_source_file(path: Path, repo_root: Path) -> bool:
    if path.name in DENYLIST_NAMES or path.suffix.lower() in DENYLIST_SUFFIXES:
        return False
    parts = set(path.relative_to(repo_root).parts)
    if {"runs", "__pycache__", ".git", "node_modules", "memory", "skill-state", "log"} & parts:
        return False
    if any(part.lower() in {"secrets", "credentials", "private", "tokens"} for part in parts):
        return False
    if path.suffix.lower() not in CODE_SUFFIXES:
        return False
    return True


def _collect_code_context(repo_root: Path, terms: set[str], cfg: AnalysisConfig, warnings: list[str]) -> list[dict[str, Any]]:
    if not terms:
        return []
    roots = [repo_root / "src" / "agent", repo_root / "benchmark" / "runner", repo_root / "benchmark" / "mobilegym", repo_root / "src"]
    snippets = []
    used = 0
    lowered_terms = {term.lower() for term in terms}
    seen: set[Path] = set()
    for root in roots:
        if not root.exists():
            continue
        for path in root.rglob("*"):
            if path in seen or not path.is_file() or not _safe_source_file(path, repo_root):
                continue
            seen.add(path)
            try:
                text = path.read_text(encoding="utf-8", errors="replace")
            except OSError as exc:
                warnings.append(f"failed to read source {path}: {exc}")
                continue
            lower = text.lower()
            matched = next((term for term in lowered_terms if term in lower or term in path.name.lower()), "")
            if not matched:
                continue
            excerpt = redact_text(text[: min(len(text), 8000)], cfg)
            used += len(excerpt.encode("utf-8"))
            snippets.append({"path": str(path.relative_to(repo_root)), "excerpt": excerpt, "reason": f"matched {matched}"})
            if used >= cfg.max_code_bytes:
                return snippets
    return snippets


def _enforce_context_budget(ctx: dict[str, Any], cfg: AnalysisConfig) -> None:
    encoded = json.dumps(ctx, ensure_ascii=False).encode("utf-8")
    if len(encoded) <= cfg.total_context_bytes:
        return
    warning = "analysis context truncated to fit total byte budget"
    def over_budget() -> bool:
        return len(json.dumps(ctx, ensure_ascii=False).encode("utf-8")) > cfg.total_context_bytes
    if isinstance(ctx.get("suite"), dict) and over_budget():
        ctx["suite"]["content_excerpt"] = str(ctx["suite"].get("content_excerpt") or "")[:1000]
    if over_budget() and ctx.get("summary_excerpt"):
        ctx["summary_excerpt"] = str(ctx.get("summary_excerpt") or "")[:1000]
    for failure in ctx.get("failures", []):
        if not isinstance(failure, dict) or not over_budget():
            continue
        for key in ("trace_excerpt", "history_excerpt", "judge_excerpt"):
            if over_budget() and failure.get(key):
                failure[key] = str(failure.get(key) or "")[:1000]
    for item in ctx.get("logs", []):
        if isinstance(item, dict) and over_budget():
            item["excerpt"] = str(item.get("excerpt") or "")[:1000]
            item["truncated"] = True
    while over_budget() and ctx.get("code"):
        ctx["code"].pop()
    for failure in ctx.get("failures", []):
        if not isinstance(failure, dict) or not over_budget():
            continue
        failure["trace_excerpt"] = str(failure.get("trace_excerpt") or "")[:300]
        failure["history_excerpt"] = str(failure.get("history_excerpt") or "")[:300]
        failure["judge_excerpt"] = str(failure.get("judge_excerpt") or "")[:300]
    if over_budget():
        ctx["logs"] = []
        ctx["code"] = []
    if over_budget():
        compact_failures = []
        for failure in ctx.get("failures", []):
            if not isinstance(failure, dict):
                continue
            compact_failures.append({
                "task_id": failure.get("task_id"),
                "status": failure.get("status"),
                "errors": failure.get("errors") or [],
                "artifact_refs": failure.get("artifact_refs") or [],
            })
        ctx["failures"] = compact_failures
        if isinstance(ctx.get("suite"), dict):
            ctx["suite"]["content_excerpt"] = ""
        ctx["summary_excerpt"] = ""
    ctx.setdefault("collection_warnings", []).append(warning)
```

- [ ] **Step 5: Run native context test to verify it passes**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py::test_collect_context_native_run_includes_failed_task_suite_trace_and_code -v`

Expected: PASS.

- [ ] **Step 6: Write failing MobileGym context and malformed artifact tests**

Append to `benchmark/tests/test_analysis.py`:

```python
def test_collect_context_mobilegym_run_includes_errors_runner_logs_and_bridge_actions(tmp_path):
    repo = tmp_path / "repo"
    run_dir = repo / "benchmark" / "runs" / "mobilegym" / "batch-1"
    shard = run_dir / "clock" / "shard-0"
    raw = shard / "raw" / "run"
    raw.mkdir(parents=True)
    (run_dir / "summary.json").write_text(json.dumps({"batch_id": "batch-1", "tasks": 1, "error": 1}), encoding="utf-8")
    (shard / "shard.json").write_text(json.dumps({"suite": "clock", "selected_task_ids": ["clock.Task"], "selected_task_count": 1}), encoding="utf-8")
    (shard / "runner.log").write_text("runner saw ClockBoom in clock_runner", encoding="utf-8")
    (shard / "compose.log").write_text("compose ok", encoding="utf-8")
    (raw / "console.log").write_text("console ClockBoom", encoding="utf-8")
    (raw / "errors.jsonl").write_text(json.dumps({"id": "clock.Task", "error": "ClockBoom"}) + "\n", encoding="utf-8")
    (raw / "aiden_bridge_actions.json").write_text(json.dumps([{"tool_name": "tap", "error": "ClockBoom"}]), encoding="utf-8")
    code = repo / "benchmark" / "mobilegym" / "clock_runner.py"
    code.parent.mkdir(parents=True)
    code.write_text("class ClockBoom(Exception): pass\n", encoding="utf-8")

    ctx = analysis.collect_context(run_dir, repo, analysis.AnalysisConfig(enabled=True))

    assert ctx["run"]["kind"] == "mobilegym"
    assert ctx["failures"][0]["task_id"] == "clock.Task"
    assert any("runner saw ClockBoom" in item["excerpt"] for item in ctx["logs"])
    assert any(item["path"].endswith("clock_runner.py") for item in ctx["code"])


def test_collect_context_records_warnings_for_malformed_artifacts(tmp_path):
    repo = tmp_path / "repo"
    run_dir = repo / "benchmark" / "runs" / "mobilegym" / "batch-bad"
    run_dir.mkdir(parents=True)
    (run_dir / "summary.json").write_text("{bad", encoding="utf-8")

    ctx = analysis.collect_context(run_dir, repo, analysis.AnalysisConfig(enabled=True))

    assert ctx["run"]["kind"] == "mobilegym"
    assert ctx["collection_warnings"]


def test_collect_context_redacts_sensitive_files_and_enforces_budget(tmp_path):
    repo = tmp_path / "repo"
    run_dir = repo / "benchmark" / "runs" / "run-budget"
    task_dir = run_dir / "tasks" / "task_a"
    task_dir.mkdir(parents=True)
    suite_path = repo / "benchmark" / "suites" / "demo.json"
    suite_path.parent.mkdir(parents=True)
    suite_path.write_text(json.dumps({"name": "demo", "tasks": [{"id": "task_a", "prompt": "BudgetBoom"}]}), encoding="utf-8")
    (run_dir / "manifest.json").write_text(json.dumps({"run_id": "run-budget", "suite_path": str(suite_path), "totals": {"failed": 1}}), encoding="utf-8")
    (run_dir / "summary.md").write_text("BudgetBoom " + "s" * 5000, encoding="utf-8")
    (run_dir / "results.jsonl").write_text(json.dumps({"task_id": "task_a", "status": "failed", "metrics": {"error": "BudgetBoom"}}) + "\n", encoding="utf-8")
    (task_dir / "trace.json").write_text(json.dumps({"final_response": "BudgetBoom " + "x" * 5000}), encoding="utf-8")
    sensitive = repo / "src" / "agent" / "agent.toml"
    sensitive.parent.mkdir(parents=True)
    sensitive.write_text('api_key = "sk-sensitive"\n# BudgetBoom\n', encoding="utf-8")
    code = repo / "src" / "agent" / "budget_boom.go"
    code.write_text("package agent\n// BudgetBoom\n" + "x" * 5000, encoding="utf-8")

    ctx = analysis.collect_context(run_dir, repo, analysis.AnalysisConfig(enabled=True, total_context_bytes=2000, max_code_bytes=2000))
    encoded = json.dumps(ctx, ensure_ascii=False)

    assert len(encoded.encode("utf-8")) <= 2500
    assert "agent.toml" not in encoded
    assert "sk-sensitive" not in encoded
    assert ctx["collection_warnings"]
```

- [ ] **Step 7: Run MobileGym context tests to verify they fail**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py::test_collect_context_mobilegym_run_includes_errors_runner_logs_and_bridge_actions benchmark/tests/test_analysis.py::test_collect_context_records_warnings_for_malformed_artifacts benchmark/tests/test_analysis.py::test_collect_context_redacts_sensitive_files_and_enforces_budget -v`

Expected: FAIL because `_collect_mobilegym_context()` and strict budget enforcement are not implemented.

- [ ] **Step 8: Implement MobileGym context collection**

Add to `benchmark/runner/analysis.py`:

```python
def _collect_mobilegym_context(run_dir: Path, repo_root: Path, cfg: AnalysisConfig, warnings: list[str]) -> dict[str, Any]:
    summary = _read_json(run_dir / "summary.json", warnings) if (run_dir / "summary.json").exists() else {}
    rows_by_task: dict[str, dict[str, Any]] = {}
    terms: set[str] = set()
    logs = []
    failures = []
    task_metadata: dict[str, dict[str, Any]] = {}
    for log_path in sorted(run_dir.glob("**/*.log")):
        if log_path.name not in {"runner.log", "console.log", "compose.log"}:
            continue
        excerpt, truncated = _read_excerpt(log_path, cfg.max_log_bytes, cfg, warnings)
        logs.append({"path": str(log_path.relative_to(run_dir)), "excerpt": excerpt, "truncated": truncated})
        terms.update(_terms_from_text(excerpt))
    for path in sorted(run_dir.glob("**/results.jsonl")) + sorted(run_dir.glob("**/errors.jsonl")):
        for row in _read_jsonl(path, warnings):
            task_id = str(row.get("id") or row.get("task_id") or row.get("name") or path.parent.name)
            rows_by_task.setdefault(task_id, {}).update(row)
            rows_by_task[task_id].setdefault("artifact_refs", []).append(str(path.relative_to(run_dir)))
            terms.update(_terms_from_text(json.dumps(row, ensure_ascii=False)))
    selected_ids = []
    for shard_json in sorted(run_dir.glob("**/shard.json")):
        meta = _read_json(shard_json, warnings)
        selected_ids.extend(str(task_id) for task_id in meta.get("selected_task_ids") or [])
        embedded = meta.get("task_metadata") if isinstance(meta.get("task_metadata"), dict) else {}
        for task_id, value in embedded.items():
            if isinstance(value, dict):
                task_metadata[str(task_id)] = value
        terms.update(_terms_from_text(json.dumps(meta, ensure_ascii=False)))
    for action_path in sorted(run_dir.glob("**/aiden_bridge_actions.json")):
        excerpt, truncated = _read_excerpt(action_path, cfg.max_log_bytes, cfg, warnings)
        logs.append({"path": str(action_path.relative_to(run_dir)), "excerpt": excerpt, "truncated": truncated})
        terms.update(_terms_from_text(excerpt))
    task_ids = list(dict.fromkeys(selected_ids + sorted(rows_by_task)))
    for task_id in task_ids:
        row = dict(task_metadata.get(task_id) or {})
        row.update(rows_by_task.get(task_id, {}))
        status, reason = _mobilegym_status(row)
        if status not in FAIL_STATUSES:
            continue
        failures.append({
            "task_id": task_id,
            "status": status,
            "rubric": row.get("rubric") or row.get("rubric_spec") or [],
            "hard_assertions": row.get("hard_assertions") or {},
            "errors": _extract_errors(row) or ([reason] if reason else []),
            "trace_excerpt": redact_text(json.dumps(row, ensure_ascii=False)[: cfg.max_log_bytes], cfg),
            "history_excerpt": redact_text(json.dumps(row.get("aiden_last_chat_history") or [], ensure_ascii=False)[: cfg.max_log_bytes], cfg),
            "log_refs": [item["path"] for item in logs if task_id in item.get("excerpt", "") or reason in item.get("excerpt", "")],
            "artifact_refs": row.get("artifact_refs") or [],
        })
    return {
        "run": {"id": str(summary.get("batch_id") or run_dir.name), "kind": "mobilegym", "suite": str(summary.get("suite") or "mobilegym"), "totals": {k: summary.get(k) for k in ("tasks", "passed", "failed", "timeout", "error", "unknown", "worker_failed") if k in summary}},
        "suite": {"path": "", "content_excerpt": ""},
        "failures": failures,
        "logs": logs,
        "code": _collect_code_context(repo_root, terms, cfg, warnings),
    }


def _mobilegym_status(row: dict[str, Any]) -> tuple[str, str]:
    error_text = str(row.get("error") or row.get("message") or "")
    execution = row.get("execution") if isinstance(row.get("execution"), dict) else {}
    stop_reason = str(execution.get("stop_reason") or row.get("stop_reason") or "")
    if "timeout" in (error_text + stop_reason).lower() or "aidenadaptertimeout" in (error_text + stop_reason).lower():
        return "timeout", error_text or stop_reason
    if error_text or row.get("is_error") is True:
        return "error", error_text or stop_reason or "is_error"
    if row.get("is_success") is True or row.get("status") == "passed":
        return "passed", "passed"
    if row.get("is_success") is False or row.get("status") == "failed":
        return "failed", stop_reason or "failed"
    return "unknown", "missing or unrecognized result"
```

- [ ] **Step 9: Run all context tests**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py -v`

Expected: PASS for Task 1 and Task 2 tests.

- [ ] **Step 10: Checkpoint**

Run: `git diff -- benchmark/runner/analysis.py benchmark/tests/test_analysis.py`

Expected: Context collection is isolated to analysis module and tests. Do not commit unless explicitly requested.

## Task 3: LLM Call, JSON Parsing, Atomic Artifact Writes, and Error Artifacts

**Files:**
- Modify: `benchmark/runner/analysis.py`
- Test: `benchmark/tests/test_analysis.py`

- [ ] **Step 1: Write failing tests for successful `analyze_run()` with mocked LLM**

Append to `benchmark/tests/test_analysis.py`:

```python
def test_analyze_run_writes_json_and_markdown_with_mocked_llm(monkeypatch, tmp_path):
    repo = tmp_path / "repo"
    run_dir = repo / "benchmark" / "runs" / "run-1"
    run_dir.mkdir(parents=True)
    (run_dir / "manifest.json").write_text(json.dumps({"run_id": "run-1", "totals": {"tasks": 0}}), encoding="utf-8")
    (run_dir / "results.jsonl").write_text("", encoding="utf-8")

    def fake_chat(cfg, context, api_key):
        assert api_key == "test-key"
        assert context["run"]["id"] == "run-1"
        return json.dumps({"summary": "Looks stable", "failure_clusters": [], "recommendations": [], "classification_counts": {}, "evidence_gaps": []})

    monkeypatch.setenv("OPENROUTER_API_KEY", "test-key")
    monkeypatch.setattr(analysis, "chat_analysis_model", fake_chat)

    result = analysis.analyze_run(run_dir, repo, analysis.AnalysisConfig(enabled=True))

    assert result.ok is True
    assert (run_dir / "llm_analysis.json").exists()
    assert (run_dir / "llm_analysis.md").exists()
    assert "Looks stable" in (run_dir / "llm_analysis.md").read_text(encoding="utf-8")
```

- [ ] **Step 2: Write failing tests for error artifact and non-throwing behavior**

Append to `benchmark/tests/test_analysis.py`:

```python
def test_analyze_run_writes_error_artifact_without_raising(monkeypatch, tmp_path):
    repo = tmp_path / "repo"
    run_dir = repo / "benchmark" / "runs" / "run-1"
    run_dir.mkdir(parents=True)
    (run_dir / "manifest.json").write_text(json.dumps({"run_id": "run-1"}), encoding="utf-8")
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.delenv("MODEL_API_KEY", raising=False)
    monkeypatch.delenv("AIDEN_MODEL_API_KEY", raising=False)

    result = analysis.analyze_run(run_dir, repo, analysis.AnalysisConfig(enabled=True))

    assert result.ok is False
    assert (run_dir / "llm_analysis_error.txt").exists()
    assert "missing analysis API key" in (run_dir / "llm_analysis_error.txt").read_text(encoding="utf-8")
```

- [ ] **Step 3: Run artifact tests to verify they fail**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py::test_analyze_run_writes_json_and_markdown_with_mocked_llm benchmark/tests/test_analysis.py::test_analyze_run_writes_error_artifact_without_raising -v`

Expected: FAIL because `analyze_run()` is not implemented.

- [ ] **Step 4: Implement prompt, LLM client, JSON extraction, atomic writes, and `analyze_run()`**

Add to `benchmark/runner/analysis.py`:

```python
SYSTEM_PROMPT = """You are a benchmark root-cause analyst. You are not a judge.
Analyze benchmark results, trace/history excerpts, runtime logs, and source snippets.
Ground every claim in evidence. If evidence is missing, say so. Return JSON only."""


def _analysis_user_prompt(context: dict[str, Any]) -> str:
    return """Analyze this benchmark run and return JSON with keys:
summary, failure_clusters, recommendations, classification_counts, evidence_gaps.
Root-cause categories must be one of: suite_issue, project_code_issue, agent_behavior_issue,
benchmark_infra_issue, environment_issue, evaluation_issue, insufficient_evidence.

CONTEXT JSON:
""" + json.dumps(context, ensure_ascii=False, indent=2)


def chat_analysis_model(cfg: AnalysisConfig, context: dict[str, Any], api_key: str) -> str:
    payload = json.dumps({
        "model": cfg.model,
        "messages": [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": _analysis_user_prompt(context)},
        ],
        "max_tokens": 4096,
    }).encode("utf-8")
    req = urllib.request.Request(
        "https://openrouter.ai/api/v1/chat/completions",
        data=payload,
        headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=cfg.timeout_sec) as resp:
            if resp.status != 200:
                raise AnalysisError(f"analysis HTTP {resp.status}")
            body = json.loads(resp.read())
    except urllib.error.HTTPError as exc:
        raise AnalysisError(f"analysis HTTP {exc.code}: {exc.read()[:200]!r}") from exc
    except (socket.timeout, urllib.error.URLError) as exc:
        raise AnalysisError(f"analysis network error: {exc}") from exc
    try:
        content = body["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError) as exc:
        raise AnalysisError(f"unexpected analysis response shape: {exc}") from exc
    if not isinstance(content, str):
        raise AnalysisError("unexpected analysis response content type")
    return content


def extract_analysis_json(raw: str) -> dict[str, Any]:
    text = raw.strip()
    if text.startswith("```"):
        parts = text.split("```", 2)
        if len(parts) >= 2:
            text = parts[1]
            if text.startswith("json\n"):
                text = text[5:]
    start = text.find("{")
    end = text.rfind("}")
    if start == -1 or end == -1 or end <= start:
        raise AnalysisError(f"analysis response has no JSON object: {raw[:200]!r}")
    try:
        parsed = json.loads(text[start:end + 1])
    except json.JSONDecodeError as exc:
        raise AnalysisError(f"analysis response JSON parse failed: {exc}: {raw[:200]!r}") from exc
    return parsed if isinstance(parsed, dict) else {"summary": str(parsed)}


def _atomic_write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(text, encoding="utf-8")
    tmp.replace(path)


def analyze_run(run_dir: Path, repo_root: Path, cfg: AnalysisConfig) -> AnalysisResult:
    if not cfg.enabled:
        return AnalysisResult(ok=False, warning="analysis disabled")
    try:
        key_name, api_key = resolve_analysis_api_key(cfg)
        context = collect_context(run_dir, repo_root, cfg)
        context = json.loads(redact_text(json.dumps(context, ensure_ascii=False), cfg))
        raw = chat_analysis_model(cfg, context, api_key)
        payload = extract_analysis_json(redact_text(raw, cfg))
        payload.setdefault("metadata", {})
        payload["metadata"].update({"model": cfg.model, "api_key_env": key_name, "run_dir": str(run_dir)})
        json_path = run_dir / "llm_analysis.json"
        md_path = run_dir / "llm_analysis.md"
        _atomic_write(json_path, json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n")
        _atomic_write(md_path, render_markdown(payload))
        return AnalysisResult(ok=True, markdown_path=md_path, json_path=json_path)
    except Exception as exc:
        message = redact_text(str(exc), cfg)
        error_path = run_dir / "llm_analysis_error.txt"
        _atomic_write(error_path, message + "\n")
        return AnalysisResult(ok=False, error_path=error_path, warning=message)
```

- [ ] **Step 5: Run all analysis tests**

Run: `uv run --project benchmark pytest benchmark/tests/test_analysis.py -v`

Expected: PASS.

- [ ] **Step 6: Checkpoint**

Run: `git diff -- benchmark/runner/analysis.py benchmark/tests/test_analysis.py`

Expected: Analysis module has complete standalone behavior. Do not commit unless explicitly requested.

## Task 4: Native Runner Trigger, HTML Integration, and Board Upload Artifacts

**Files:**
- Modify: `benchmark/runner/main.py`
- Modify: `benchmark/runner/html_report.py`
- Test: `benchmark/tests/test_main.py`
- Test: `benchmark/tests/test_html_report.py`

- [ ] **Step 1: Write failing HTML report test for inline analysis section**

Append to `benchmark/tests/test_html_report.py`:

```python
def test_generate_report_includes_llm_analysis_section(tmp_path: Path):
    run_dir = tmp_path / "run"
    run_dir.mkdir()
    (run_dir / "manifest.json").write_text('{"run_id":"run","totals":{"tasks":0,"passed":0,"failed":0}}')
    (run_dir / "results.jsonl").write_text("")
    (run_dir / "llm_analysis.md").write_text("# LLM Benchmark Analysis\n\nRoot cause summary")

    html = generate_report_html(run_dir)

    assert "LLM Analysis" in html
    assert "Root cause summary" in html
```

- [ ] **Step 2: Write failing upload test for analysis artifacts**

Append to `benchmark/tests/test_html_report.py`:

```python
def test_upload_report_uploads_analysis_artifacts(tmp_path: Path):
    run_dir = tmp_path / "run-1"
    run_dir.mkdir()
    (run_dir / "manifest.json").write_text('{"run_id":"run-1"}')
    (run_dir / "llm_analysis.md").write_text("analysis md")
    (run_dir / "llm_analysis.json").write_text('{"summary":"analysis"}')
    commands = []

    class Client:
        def invoke_tool(self, name, payload):
            commands.append(payload["command"])
            return type("Result", (), {"is_error": False})()

    assert upload_report(Client(), "<html></html>", run_dir) is True
    command = commands[0]
    assert "llm_analysis.md" in command
    assert "llm_analysis.json" in command
```

- [ ] **Step 3: Run HTML/upload tests to verify they fail**

Run: `uv run --project benchmark pytest benchmark/tests/test_html_report.py::test_generate_report_includes_llm_analysis_section benchmark/tests/test_html_report.py::test_upload_report_uploads_analysis_artifacts -v`

Expected: FAIL because HTML report ignores analysis artifacts and upload does not include them.

- [ ] **Step 4: Implement analysis section in `html_report.py`**

Modify `benchmark/runner/html_report.py`:

```python
def _analysis_html(run_dir: Path) -> str:
    md = run_dir / "llm_analysis.md"
    err = run_dir / "llm_analysis_error.txt"
    if md.exists():
        text = md.read_text("utf-8")[:20000]
        return f'<section class="analysis"><h2>LLM Analysis</h2><pre>{_esc(text)}</pre></section>'
    if err.exists():
        text = err.read_text("utf-8")[:4000]
        return f'<section class="analysis warning"><h2>LLM Analysis</h2><pre>{_esc(text)}</pre></section>'
    return ""
```

Add `analysis_html=_analysis_html(run_dir)` to the `HTML_TEMPLATE.format(...)` call in `generate_report_html()`.

Add `{analysis_html}` to `HTML_TEMPLATE` after the progress block and before task table panel.

Add minimal CSS:

```css
.analysis {{ border: 1px solid var(--border); border-radius: 16px; background: var(--surface); padding: 16px; margin-bottom: 20px }}
.analysis h2 {{ font-size: 14px; margin-bottom: 10px }}
.analysis pre {{ white-space: pre-wrap; font-family: var(--font-mono); font-size: 12px; line-height: 1.5; color: var(--fg) }}
.analysis.warning {{ border-color: color-mix(in oklch, oklch(60% 0.18 28) 35%, var(--border)) }}
```

Expose a public wrapper for MobileGym report reuse:

```python
def analysis_html_for_run_dir(run_dir: Path) -> str:
    return _analysis_html(run_dir)
```

- [ ] **Step 5: Extend `upload_report()` to upload analysis artifacts**

Modify `benchmark/runner/html_report.py` inside `upload_report()` when `run_dir is not None`:

```python
analysis_parts = []
for name in ("llm_analysis.md", "llm_analysis.json", "llm_analysis_error.txt"):
    path = run_dir / name
    if not path.exists():
        continue
    text = path.read_text("utf-8")
    encoded_part = base64.b64encode(text.encode("utf-8")).decode("ascii")
    analysis_parts.append(f"printf '%s' '{encoded_part}' | base64 -d > {board_run_dir}/{name}")
if analysis_parts:
    cmd += " && " + " && ".join(analysis_parts)
```

- [ ] **Step 6: Run HTML/upload tests**

Run: `uv run --project benchmark pytest benchmark/tests/test_html_report.py::test_generate_report_includes_llm_analysis_section benchmark/tests/test_html_report.py::test_upload_report_uploads_analysis_artifacts -v`

Expected: PASS.

- [ ] **Step 7: Write failing native runner trigger tests**

Append to `benchmark/tests/test_main.py`:

```python
def test_run_triggers_llm_analysis_when_enabled(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(json.dumps({"name": "empty_suite", "tasks": []}), encoding="utf-8")
    calls = []

    class FakeClient:
        def __init__(self, base_url):
            self.base_url = base_url
        def health(self):
            return True
        def close(self):
            pass

    def fake_analyze(run_dir, repo_root, cfg):
        calls.append((run_dir, repo_root, cfg))
        (run_dir / "llm_analysis.md").write_text("analysis", encoding="utf-8")
        return main.AnalysisResult(ok=True, markdown_path=run_dir / "llm_analysis.md")

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "upload_report", lambda *args, **kwargs: False)
    monkeypatch.setattr(main, "analyze_run", fake_analyze)

    rc = main.cli(["run", "--suite", str(suite_path), "--out", str(tmp_path / "runs"), "--no-judge", "--llm-analysis"])

    assert rc == 0
    assert calls and calls[0][2].enabled is True


def test_run_keeps_exit_code_when_analysis_fails(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(json.dumps({"name": "empty_suite", "tasks": []}), encoding="utf-8")

    class FakeClient:
        def __init__(self, base_url):
            pass
        def health(self):
            return True
        def close(self):
            pass

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "upload_report", lambda *args, **kwargs: False)
    monkeypatch.setattr(main, "analyze_run", lambda run_dir, repo_root, cfg: main.AnalysisResult(ok=False, warning="boom"))

    rc = main.cli(["run", "--suite", str(suite_path), "--out", str(tmp_path / "runs"), "--no-judge", "--llm-analysis"])

    assert rc == 0
```

- [ ] **Step 8: Run native trigger tests to verify they fail**

Run: `uv run --project benchmark pytest benchmark/tests/test_main.py::test_run_triggers_llm_analysis_when_enabled benchmark/tests/test_main.py::test_run_keeps_exit_code_when_analysis_fails -v`

Expected: FAIL because `main` does not import or call analysis.

- [ ] **Step 9: Wire native runner flags and trigger**

Modify `benchmark/runner/main.py` imports:

```python
from runner.analysis import AnalysisConfig, AnalysisResult, analyze_run
```

Add run flags:

```python
p_run.add_argument("--llm-analysis", action="store_true", help="Run post-run LLM RCA analysis")
p_run.add_argument("--analysis-model", default=os.environ.get("AIDEN_BENCHMARK_ANALYSIS_MODEL", "anthropic/claude-sonnet-4-6"))
p_run.add_argument("--analysis-max-log-bytes", type=int, default=int(os.environ.get("AIDEN_BENCHMARK_ANALYSIS_MAX_LOG_BYTES", 64 * 1024)))
p_run.add_argument("--analysis-max-code-bytes", type=int, default=int(os.environ.get("AIDEN_BENCHMARK_ANALYSIS_MAX_CODE_BYTES", 128 * 1024)))
p_run.add_argument("--analysis-timeout-sec", type=int, default=int(os.environ.get("AIDEN_BENCHMARK_ANALYSIS_TIMEOUT_SEC", 180)))
```

After writing the initial `report.html`, call analysis and regenerate HTML:

```python
    if args.llm_analysis:
        analysis_result = analyze_run(run_dir, REPO_ROOT, AnalysisConfig(
            enabled=True,
            model=args.analysis_model,
            max_log_bytes=args.analysis_max_log_bytes,
            max_code_bytes=args.analysis_max_code_bytes,
            timeout_sec=args.analysis_timeout_sec,
            api_key_env=os.environ.get("AIDEN_BENCHMARK_ANALYSIS_API_KEY_ENV") or None,
        ))
        if not analysis_result.ok:
            print(f"Warning: benchmark LLM analysis failed: {analysis_result.warning}", file=sys.stderr, flush=True)
        html = generate_report_html(run_dir)
        (run_dir / "report.html").write_text(html, encoding="utf-8")
```

- [ ] **Step 10: Run native runner and HTML tests**

Run: `uv run --project benchmark pytest benchmark/tests/test_main.py::test_run_triggers_llm_analysis_when_enabled benchmark/tests/test_main.py::test_run_keeps_exit_code_when_analysis_fails benchmark/tests/test_html_report.py::test_generate_report_includes_llm_analysis_section benchmark/tests/test_html_report.py::test_upload_report_uploads_analysis_artifacts -v`

Expected: PASS.

- [ ] **Step 11: Checkpoint**

Run: `git diff -- benchmark/runner/main.py benchmark/runner/html_report.py benchmark/tests/test_main.py benchmark/tests/test_html_report.py`

Expected: Native runner integration and HTML/upload changes only. Do not commit unless explicitly requested.

## Task 5: MobileGym Report Trigger and HTML Integration

**Files:**
- Modify: `benchmark/mobilegym/report.py`
- Test: `benchmark/tests/mobilegym/test_report.py`

- [ ] **Step 1: Write failing MobileGym report trigger test**

Append to `benchmark/tests/mobilegym/test_report.py`:

```python
def test_generate_reports_triggers_llm_analysis_when_env_enabled(monkeypatch, tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-analysis"
    shard = batch / "clock" / "shard-0"
    write_json(shard / "shard.json", {"batch_id": "batch-analysis", "suite": "clock", "selected_task_count": 1, "selected_task_ids": ["clock.Task"], "exit_code": 0})
    write_jsonl(shard / "raw" / "run" / "results.jsonl", [{"id": "clock.Task", "is_success": True}])
    calls = []

    def fake_analyze(run_dir, repo_root, cfg):
        calls.append((run_dir, repo_root, cfg))
        (run_dir / "llm_analysis.md").write_text("mobilegym analysis", encoding="utf-8")
        return report.AnalysisResult(ok=True, markdown_path=run_dir / "llm_analysis.md")

    monkeypatch.setenv("AIDEN_BENCHMARK_LLM_ANALYSIS", "1")
    monkeypatch.setattr(report, "analyze_run", fake_analyze)

    report.generate_reports(batch)

    assert calls and calls[0][0] == batch
    assert "mobilegym analysis" in (batch / "index.html").read_text(encoding="utf-8")
```

- [ ] **Step 2: Write failing MobileGym analysis failure test**

Append to `benchmark/tests/mobilegym/test_report.py`:

```python
def test_generate_reports_keeps_summary_when_llm_analysis_fails(monkeypatch, tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-analysis-fail"
    shard = batch / "clock" / "shard-0"
    write_json(shard / "shard.json", {"batch_id": "batch-analysis-fail", "suite": "clock", "selected_task_count": 1, "selected_task_ids": ["clock.Task"], "exit_code": 0})
    write_jsonl(shard / "raw" / "run" / "results.jsonl", [{"id": "clock.Task", "is_success": True}])

    monkeypatch.setenv("AIDEN_BENCHMARK_LLM_ANALYSIS", "1")
    monkeypatch.setattr(report, "analyze_run", lambda run_dir, repo_root, cfg: report.AnalysisResult(ok=False, warning="boom", error_path=run_dir / "llm_analysis_error.txt"))

    summary = report.generate_reports(batch)

    assert summary["passed"] == 1
    assert (batch / "index.html").exists()
```

- [ ] **Step 3: Run MobileGym trigger tests to verify they fail**

Run: `uv run --project benchmark pytest benchmark/tests/mobilegym/test_report.py::test_generate_reports_triggers_llm_analysis_when_env_enabled benchmark/tests/mobilegym/test_report.py::test_generate_reports_keeps_summary_when_llm_analysis_fails -v`

Expected: FAIL because `mobilegym.report` does not import or call analysis.

- [ ] **Step 4: Import analysis helpers and add report trigger wrapper**

Modify `benchmark/mobilegym/report.py` imports:

```python
from runner.analysis import AnalysisResult, analyze_run, config_from_env
```

Add near report-writing helpers:

```python
BENCHMARK_ROOT = Path(__file__).resolve().parents[1]


def _run_analysis_if_enabled(run_dir: Path) -> AnalysisResult | None:
    cfg = config_from_env()
    if not cfg.enabled:
        return None
    result = analyze_run(run_dir, BENCHMARK_ROOT.parent, cfg)
    if not result.ok:
        print(f"warning: MobileGym LLM analysis failed for {run_dir}: {result.warning}", file=sys.stderr)
    return result
```

In `_generate_direct_run_report()` after `_write_direct_run_report(...)`:

```python
    if _run_analysis_if_enabled(run_dir):
        _write_direct_run_report(run_dir, rows, summary)
```

In `generate_reports()` after `_write_batch_report(...)`:

```python
    if _run_analysis_if_enabled(batch):
        _write_batch_report(batch, suite_summaries, batch_summary)
```

- [ ] **Step 5: Reuse analysis HTML section in MobileGym drawer reports**

Concrete change in `benchmark/mobilegym/report.py` imports:

```python
from runner.html_report import HTML_TEMPLATE, analysis_html_for_run_dir
```

Update the write helpers so they pass the physical directory containing the analysis artifacts into `_drawer_html()`:

```python
def _write_suite_report(suite_dir: Path, rows: list[dict[str, Any]], summary: dict[str, Any]) -> None:
    (suite_dir / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")
    (suite_dir / "index.html").write_text(_drawer_html(summary["suite"], summary, rows, suite_dir), encoding="utf-8")


def _write_batch_report(batch_dir: Path, suite_summaries: list[dict[str, Any]], summary: dict[str, Any]) -> None:
    serializable = dict(summary)
    serializable.pop("rows", None)
    (batch_dir / "summary.json").write_text(json.dumps(serializable, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")
    rows = summary.get("rows") or []
    (batch_dir / "index.html").write_text(_drawer_html(summary["batch_id"], serializable, rows, batch_dir), encoding="utf-8")


def _write_direct_run_report(run_dir: Path, rows: list[dict[str, Any]], summary: dict[str, Any]) -> None:
    serializable = dict(summary)
    serializable.pop("rows", None)
    (run_dir / "summary.json").write_text(json.dumps(serializable, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")
    (run_dir / "index.html").write_text(_drawer_html(summary["batch_id"], serializable, rows, run_dir), encoding="utf-8")
```

Update `_drawer_html()` signature and `HTML_TEMPLATE.format(...)`:

```python
def _drawer_html(title: str, summary: dict[str, Any], rows: list[dict[str, Any]], run_dir: Path | None = None) -> str:
    ...
    return HTML_TEMPLATE.format(
        ...,
        analysis_html=analysis_html_for_run_dir(run_dir) if run_dir else "",
    )
```

This avoids duplicate markdown parsing and ensures batch/direct `index.html` is rewritten after `llm_analysis.md` or `llm_analysis_error.txt` exists.

- [ ] **Step 6: Run MobileGym report trigger tests**

Run: `uv run --project benchmark pytest benchmark/tests/mobilegym/test_report.py::test_generate_reports_triggers_llm_analysis_when_env_enabled benchmark/tests/mobilegym/test_report.py::test_generate_reports_keeps_summary_when_llm_analysis_fails -v`

Expected: PASS.

- [ ] **Step 7: Run existing MobileGym report test suite**

Run: `uv run --project benchmark pytest benchmark/tests/mobilegym/test_report.py -v`

Expected: PASS.

- [ ] **Step 8: Checkpoint**

Run: `git diff -- benchmark/mobilegym/report.py benchmark/runner/html_report.py benchmark/tests/mobilegym/test_report.py`

Expected: MobileGym report analysis integration only. Do not commit unless explicitly requested.

## Task 6: Local Launcher Payload Env and Safe Artifact Serving

**Files:**
- Modify: `benchmark/mobilegym/scripts/local_launcher.py`
- Test: `tests/benchmark/test_mobilegym_local_launcher.py`

- [ ] **Step 1: Write failing tests for launcher payload analysis env mapping**

Append to `tests/benchmark/test_mobilegym_local_launcher.py`:

```python
def test_build_run_command_maps_llm_analysis_payload_to_env(launcher_module, tmp_path):
    docker_dir = tmp_path / "mobilegym" / "docker"
    docker_dir.mkdir(parents=True)
    (docker_dir / "parallel_run.sh").write_text("#!/usr/bin/env bash\n")

    command = launcher_module.build_run_command(
        tmp_path,
        {
            "suite": "clock",
            "suite_type": "mobilegym_builtin",
            "llm_analysis": True,
            "analysis_model": "anthropic/claude-sonnet-4-6",
        },
    )

    assert command.env["AIDEN_BENCHMARK_LLM_ANALYSIS"] == "1"
    assert command.env["AIDEN_BENCHMARK_ANALYSIS_MODEL"] == "anthropic/claude-sonnet-4-6"
```

- [ ] **Step 2: Write failing tests for serving safe report artifacts**

Append to `tests/benchmark/test_mobilegym_local_launcher.py`:

```python
def test_handler_serves_mobilegym_analysis_artifact(launcher_module, tmp_path):
    report_dir = tmp_path / "runs" / "mobilegym" / "batch-20260611-010101"
    report_dir.mkdir(parents=True)
    (report_dir / "index.html").write_text("<html>MobileGym report</html>")
    (report_dir / "llm_analysis.md").write_text("analysis body")
    server = HTTPServer(("127.0.0.1", 0), launcher_module.make_handler(tmp_path))
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with urllib.request.urlopen(
            f"http://127.0.0.1:{server.server_port}/benchmark/report/batch-20260611-010101/llm_analysis.md",
            timeout=2,
        ) as resp:
            assert resp.status == 200
            assert "text/markdown" in resp.headers["Content-Type"]
            assert resp.read().decode() == "analysis body"
    finally:
        server.shutdown()
        thread.join(timeout=2)


def test_handler_rejects_unsafe_report_artifact(launcher_module, tmp_path):
    server = HTTPServer(("127.0.0.1", 0), launcher_module.make_handler(tmp_path))
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with pytest.raises(urllib.error.HTTPError) as exc:
            urllib.request.urlopen(
                f"http://127.0.0.1:{server.server_port}/benchmark/report/batch-20260611-010101/../../secret",
                timeout=2,
            )
        assert exc.value.code == 404
    finally:
        server.shutdown()
        thread.join(timeout=2)
```

- [ ] **Step 3: Run launcher tests to verify they fail**

Run: `uv run --project benchmark pytest tests/benchmark/test_mobilegym_local_launcher.py::test_build_run_command_maps_llm_analysis_payload_to_env tests/benchmark/test_mobilegym_local_launcher.py::test_handler_serves_mobilegym_analysis_artifact tests/benchmark/test_mobilegym_local_launcher.py::test_handler_rejects_unsafe_report_artifact -v`

Expected: FAIL because env mapping and artifact route do not exist.

- [ ] **Step 4: Implement launcher payload env mapping**

Modify `build_run_command()` in `benchmark/mobilegym/scripts/local_launcher.py` after model env setup:

```python
    if payload.get("llm_analysis") is True:
        env["AIDEN_BENCHMARK_LLM_ANALYSIS"] = "1"
    for payload_key, env_key in {
        "analysis_model": "AIDEN_BENCHMARK_ANALYSIS_MODEL",
        "analysis_max_log_bytes": "AIDEN_BENCHMARK_ANALYSIS_MAX_LOG_BYTES",
        "analysis_max_code_bytes": "AIDEN_BENCHMARK_ANALYSIS_MAX_CODE_BYTES",
        "analysis_timeout_sec": "AIDEN_BENCHMARK_ANALYSIS_TIMEOUT_SEC",
    }.items():
        value = payload.get(payload_key)
        if value not in (None, ""):
            env[env_key] = str(value)
```

- [ ] **Step 5: Implement safe artifact route**

Modify `LocalLauncherHandler.do_GET()` path handling:

```python
            elif path.startswith("/benchmark/report/"):
                self.send_report(path.removeprefix("/benchmark/report/"))
```

Replace `send_report()` implementation with split run/artifact handling:

```python
        def send_report(self, value: str) -> None:
            run_id, _, artifact = value.partition("/")
            if not is_safe_run_id(run_id):
                self.send_error(404, "not found")
                return
            if not artifact:
                artifact = "index.html"
            if artifact not in {"index.html", "llm_analysis.md", "llm_analysis.json", "llm_analysis_error.txt"}:
                self.send_error(404, "not found")
                return
            report_path = root / "runs" / "mobilegym" / run_id / artifact
            try:
                body = report_path.read_bytes()
            except OSError:
                self.send_error(404, "not found")
                return
            content_type = {
                ".html": "text/html; charset=utf-8",
                ".md": "text/markdown; charset=utf-8",
                ".json": "application/json; charset=utf-8",
                ".txt": "text/plain; charset=utf-8",
            }.get(Path(artifact).suffix, "application/octet-stream")
            self.send_response(200)
            self.send_common_headers(content_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
```

- [ ] **Step 6: Run launcher tests**

Run: `uv run --project benchmark pytest tests/benchmark/test_mobilegym_local_launcher.py -v`

Expected: PASS.

- [ ] **Step 7: Checkpoint**

Run: `git diff -- benchmark/mobilegym/scripts/local_launcher.py tests/benchmark/test_mobilegym_local_launcher.py`

Expected: Local launcher env/serving changes only. Do not commit unless explicitly requested.

## Task 7: Shell Path Verification and Documentation Update

**Files:**
- Modify: `benchmark/mobilegym/docker/parallel_run.sh` only if needed
- Modify: `benchmark/LOGGING.md`
- Test: `tests/benchmark/test_mobilegym_local_launcher.py` or `benchmark/tests/mobilegym/test_parallel_run.py`

- [ ] **Step 1: Inspect whether `parallel_run.sh` needs code changes**

Read the current report generation call:

```bash
(cd "$SCRIPT_DIR/../.." && uv run python -m mobilegym.report "$BATCH_DIR")
```

Expected: It inherits parent shell environment, so `AIDEN_BENCHMARK_*` variables from local launcher or user shell should already reach `mobilegym.report`.

- [ ] **Step 2: Add a test only if explicit pass-through is added**

If implementation changes `parallel_run.sh`, update `benchmark/tests/mobilegym/test_parallel_run.py` with a text assertion, for example:

```python
def test_parallel_run_preserves_llm_analysis_env_for_report_generation():
    script = PARALLEL_RUN_PATH.read_text()
    assert "AIDEN_BENCHMARK_LLM_ANALYSIS" in script
```

Expected: Only add this if the script is modified. Do not add brittle assertions for inherited shell behavior.

- [ ] **Step 3: Document the new controls**

Append a concise section to `benchmark/LOGGING.md`:

```markdown
### LLM Post-Run Analysis

Use `--llm-analysis` with the native runner to write `llm_analysis.md` and `llm_analysis.json` next to the benchmark report. The analysis reads suite results, traces, runtime logs, and bounded source snippets to suggest likely root causes.

MobileGym report generation can be enabled with `AIDEN_BENCHMARK_LLM_ANALYSIS=1`. Optional controls: `AIDEN_BENCHMARK_ANALYSIS_MODEL`, `AIDEN_BENCHMARK_ANALYSIS_MAX_LOG_BYTES`, `AIDEN_BENCHMARK_ANALYSIS_MAX_CODE_BYTES`, and `AIDEN_BENCHMARK_ANALYSIS_TIMEOUT_SEC`.

Analysis failures do not change benchmark exit codes; failures are written to `llm_analysis_error.txt`.
```

- [ ] **Step 4: Run focused docs-related tests**

Run: `uv run --project benchmark pytest benchmark/tests/mobilegym/test_parallel_run.py tests/benchmark/test_mobilegym_local_launcher.py -v`

Expected: PASS.

- [ ] **Step 5: Checkpoint**

Run: `git diff -- benchmark/LOGGING.md benchmark/mobilegym/docker/parallel_run.sh benchmark/tests/mobilegym/test_parallel_run.py`

Expected: Docs changed; shell/test changed only if needed. Do not commit unless explicitly requested.

## Task 8: Full Verification and Cleanup

**Files:**
- All changed files from previous tasks

- [ ] **Step 1: Run the complete benchmark Python test suite**

Run: `uv run --project benchmark pytest benchmark/tests tests/benchmark -v`

Expected: PASS.

- [ ] **Step 2: Run targeted import smoke checks**

Run: `uv run --project benchmark python -m runner.main --help`

Expected: Command exits 0 and shows `--llm-analysis`.

Run: `uv run --project benchmark python -m mobilegym.report --help`

Expected: Command exits 0 and still shows MobileGym report help.

- [ ] **Step 3: Verify no secrets or generated artifacts were accidentally added**

Run: `git status --short`

Expected: Only planned source, test, spec, plan, and docs files changed.

Run: `git diff --stat`

Expected: Diff size is reasonable for the planned feature.

- [ ] **Step 4: Review generated plan/spec docs**

Run: `git diff -- docs/superpowers/specs/2026-06-15-benchmark-llm-analysis-design.md docs/superpowers/plans/2026-06-15-benchmark-llm-analysis.md`

Expected: Spec and plan are intentional. Do not commit unless explicitly requested.

- [ ] **Step 5: Summarize implementation outcome**

Report:

- Analysis artifacts written by native runner and MobileGym when enabled.
- HTML reports show analysis or warning inline.
- LLM failures do not change benchmark exit code.
- Tests run and result.
