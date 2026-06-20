# Agent-Driven Benchmark

Measures the Go agent's phone-control capability by sending tasks via the HTTP API and judging results with an LLM.

## Prerequisites

1. Go agent daemon running: `cd src/agent && go run ./cmd/daemon -addr :8080`
2. Python environment set up: `cd benchmark && uv sync`
3. Judge API key: `export ANTHROPIC_API_KEY=sk-ant-...` or `export OPENROUTER_API_KEY=...`

## Running

```bash
cd benchmark

# Full run with LLM judge
uv run python -m runner run --suite suites/phone_control_v1.json

# Dry run (no judge, just collect traces and screenshots)
uv run python -m runner run --suite suites/phone_control_v1.json --no-judge

# Override agent URL
uv run python -m runner run --suite suites/phone_control_v1.json --agent-url http://192.168.1.100:8080
```

## Output

Each run creates `runs/<run_id>/`:

```text
<run_id>/
├── manifest.json       # Environment info (git sha, model, suite version)
├── results.jsonl       # One JSON row per task
├── summary.md          # Human-readable digest
└── tasks/<task_id>/
    ├── pre.jpg         # Phone state before agent acted
    ├── history.json    # Raw /api/history response
    ├── trace.json      # Structured tool-call sequence
    ├── steps/          # Per-step post-action screenshots
    │   ├── step_01_screenshot.jpg
    │   ├── step_02_mouse_click.jpg
    │   └── ...
    └── judge.json      # LLM judge rubric verdicts
```

## Re-judging

Change rubric phrasing or judge model without re-running on hardware:

```bash
uv run python -m runner rejudge --run-dir runs/<id> --judge-model claude-sonnet-4-6
```

## Comparing runs

```bash
uv run python -m runner compare --runs runs/<id_a> runs/<id_b>
```

## Legacy

The previous benchmark (`benchmark/suites/full_smoke.json`) is deprecated and retained for reference only. The legacy entry point `scripts/aiden_benchmark.py` now forwards to the new runner.
