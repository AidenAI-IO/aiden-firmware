# Agent Benchmark

Agent benchmark is a testing framework for evaluating the Go agent's phone control task capabilities.

## Quick Start

### Running Benchmark

```bash
cd benchmark
uv run python -m runner run --suite suites/memory_v1.json --agent-url http://192.168.1.100:8080
```

### View Reports

After running, it will automatically:

- Open local HTML report in browser
- Upload report to device at `/userdata/agent/benchmark/report.html`
- Accessible via `http://<board-ip>:8080/benchmark`

Report includes:

- Task list (clickable for details)
- Each task's prompt, tool calls, agent response, rubric scoring
- Pass rate, failure count, skip count statistics

### Directory Structure

```text
benchmark/
├── runner/              # Python package
│   ├── main.py          # CLI entry point
│   ├── suite.py         # Suite loading
│   ├── runtask.py       # Task execution
│   ├── judge.py         # LLM judge
│   └── html_report.py   # HTML report generation
├── suites/              # Task suites
│   ├── memory_v1.json   # Memory tests (17 tasks)
│   ├── personamem_lt_recall_v1.json # PersonaMem-derived LT memory recall tests
│   └── phone_control_v1.json  # Phone control tests
└── runs/<run_id>/       # Run results
    ├── manifest.json    # Run metadata
    ├── results.jsonl    # One task per line
    └── tasks/<task_id>/ # Detailed data per task
        ├── history.json # Conversation history
        ├── trace.json   # Tool call trace
        └── steps/       # Step-by-step screenshots
```

## CLI Commands

### run - Run Benchmark

```bash
uv run python -m runner run --suite <suite.json> [options]
```

Options:

- `--suite PATH` - Benchmark suite JSON path
- `--agent-url URL` - Agent HTTP address (default `http://localhost:8080`)
- `--no-judge` - Skip LLM judge, only run hard assertions
- `--repeats N` - Repeat each task N times
- `--filter PATTERN` - Only run tasks matching the ID pattern
- `--verbose` - Enable verbose output
- `--state-file PATH` - Path to state file for resume/cooldown tracking

### rejudge - Re-judge

```bash
uv run python -m runner rejudge --run-dir runs/<run_id>
```

Re-judge all tasks in an existing run without re-executing tasks.

### compare - Compare Two Runs

```bash
uv run python -m runner compare --runs runs/<run_a> runs/<run_b>
```

Output which tasks flipped status, latency differences, etc. between two runs.

## Environment Variables

- `OPENROUTER_API_KEY` - OpenRouter API key (required for judge)
- `ANTHROPIC_API_KEY` - Anthropic API key (optional, judge fallback)

## Related Documentation

- [Architecture Design](./architecture.md) - Benchmark design principles and scoring mechanism
- [Detailed Guide](./quickstart.md) - Complete usage instructions

## Dual-Mode Execution

Benchmark supports two execution modes:

- **Aiden Native** — Runs on local agent via `benchmark/runner/main.py`, serial execution.
- **MobileGym** — Runs on Docker emulator via `benchmark/mobilegym/scripts/run_aiden.py`,
  supports parallelism (`PARALLEL=N ./parallel_run.sh`).

The same `benchmark/suites/*.json` files can execute in both modes. MobileGym built-in suites
(clock, alipay, wechat, etc.) are only available in MobileGym mode, discovered and aggregated from
`benchmark/mobilegym/suites/all_tasks.txt`.

The web UI `/benchmark` page has an "Aiden Native / MobileGym" radio button to switch between modes.
MobileGym mode currently only provides parallelism control; the `/benchmark` page does not provide
a task limit input box and will not send `limit` in the launch payload.
