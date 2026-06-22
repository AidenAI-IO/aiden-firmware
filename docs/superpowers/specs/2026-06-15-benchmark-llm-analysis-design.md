# Benchmark LLM Post-Run Analysis Design

## Context

Benchmark currently has two reporting paths:

- Native Aiden runner: `benchmark/runner/main.py` writes `manifest.json`, `results.jsonl`, `summary.md`, `report.html`, and per-task artifacts under `tasks/<task_id>/`.
- MobileGym runner: `benchmark/mobilegym/scripts/run_aiden.py` and `benchmark/mobilegym/report.py` write batch/shard summaries, `index.html`, raw `results.jsonl/errors.jsonl`, `console.log`, `runner.log`, `compose.log`, and bridge/action artifacts.

The requested feature is not another judge pass. It should behave like a post-run RCA analyst: after suites finish, it reads benchmark results, trace logs, code/runtime logs, suite definitions, and relevant project code, then produces likely root causes and modification suggestions.

## Goals

- Automatically trigger LLM analysis after a benchmark suite finishes and reports are generated.
- Analyze the complete execution chain, not just rubric verdicts.
- Classify likely causes: suite issue, project code issue, agent behavior issue, benchmark infra issue, environment issue, evaluation issue, or insufficient evidence.
- Store the analysis next to the run report so users can review both together.
- Keep benchmark result semantics stable: analysis failure must not change the benchmark pass/fail exit code.

## Non-Goals

- Do not automatically modify suite files or project code.
- Do not replace existing judge/rubric evaluation.
- Do not require a fully agentic code-search loop in the first version.
- Do not upload source code or unlimited logs; context must be bounded and explainable.

## User Experience

When enabled, a completed run directory contains:

- `llm_analysis.md`: human-readable RCA report.
- `llm_analysis.json`: structured report for UI or automation.
- `llm_analysis_error.txt`: best-effort failure details if analysis cannot run.

The HTML report should expose the analysis either as an inline section or a visible link. Users can read the normal benchmark report and the LLM analysis together.

Suggested CLI controls:

- `--llm-analysis`: enable post-run LLM analysis.
- `--analysis-model`: OpenRouter model ID, defaulting to a capable analysis model.
- `--analysis-max-log-bytes`: cap raw logs included per file.
- `--analysis-max-code-bytes`: cap source code snippets included.
- `--analysis-timeout-sec`: cap the LLM request duration.

For MobileGym and local launcher flows, the same controls should be propagated through environment variables because the report generator may run from shell scripts or containers:

- `AIDEN_BENCHMARK_LLM_ANALYSIS=1`
- `AIDEN_BENCHMARK_ANALYSIS_MODEL`
- `AIDEN_BENCHMARK_ANALYSIS_MAX_LOG_BYTES`
- `AIDEN_BENCHMARK_ANALYSIS_MAX_CODE_BYTES`
- `AIDEN_BENCHMARK_ANALYSIS_TIMEOUT_SEC`
- `AIDEN_BENCHMARK_ANALYSIS_API_KEY_ENV` if a non-default key variable is needed.

The local launcher request payload may expose an `llm_analysis` boolean later, but v1 can rely on environment propagation from `build_run_command` into `parallel_run.sh` and the report generation step.

Analysis credential lookup must work in both native and MobileGym flows. Use this precedence:

1. Env var named by `AIDEN_BENCHMARK_ANALYSIS_API_KEY_ENV`.
2. `OPENROUTER_API_KEY`.
3. `MODEL_API_KEY`.
4. `AIDEN_MODEL_API_KEY`.

This is required because MobileGym launcher flows may populate `MODEL_API_KEY` from the board model config instead of `OPENROUTER_API_KEY`.

## Architecture

Add a native analysis module, for example `benchmark/runner/analysis.py`, with four responsibilities:

1. Discover run artifacts.
2. Build a bounded analysis context.
3. Call the LLM using the existing OpenRouter-compatible HTTP style.
4. Write markdown and JSON artifacts.

Use a small shared API:

```python
analyze_run(run_dir: Path, repo_root: Path, cfg: AnalysisConfig) -> AnalysisResult
```

The native runner calls this after `report.html` is written. MobileGym report generation calls the same API after `index.html` and `summary.json` are written for the batch or direct run directory.

Native runner wiring:

- Add flags to `benchmark.runner run`.
- Build `AnalysisConfig` in `benchmark/runner/main.py`.
- Call `analyze_run()` after `report.html` is generated and before report upload.
- Regenerate or patch the HTML report link after analysis artifacts exist.
- Upload analysis artifacts with the report so board-hosted links are not broken.
- Avoid relative links that resolve differently on the board. If the board serves the latest report as `/benchmark/report.html`, either inline the analysis summary or link to an absolute run-scoped URL such as `/benchmark/runs/<run_id>/llm_analysis.md` after uploading that artifact.

MobileGym wiring:

- `parallel_run.sh` forwards analysis environment variables to the report generation process.
- `benchmark/mobilegym/report.py` reads those variables and calls `analyze_run()` when enabled.
- `run_aiden.py` direct serial runs inherit the same environment and therefore get analysis after `_generate_run_report_best_effort()`.
- `local_launcher.py` does not call the LLM directly; it only passes environment into the benchmark process and lists reports that already contain analysis links.
- `local_launcher.py` must serve safe report artifacts under `/benchmark/report/<run_id>/<artifact>` or inline the analysis in `index.html`; a link to `llm_analysis.md` must be reachable from the served report.
- MobileGym report links should use `/benchmark/report/<run_id>/llm_analysis.md` or an inline section instead of plain relative links, because `/benchmark/report/<run_id>` has no trailing slash in the existing launcher route.

## Analysis Inputs

For native Aiden runs, collect:

- `manifest.json`
- `results.jsonl`
- `summary.md`
- suite JSON from `manifest.suite_path`
- per-task `history.json`, `trace.json`, `judge.json`, screenshots metadata when present

For MobileGym runs, collect:

- batch/direct `summary.json`
- suite/shard summaries
- raw `results.jsonl` and `errors.jsonl`
- `runner.log`, `console.log`, `compose.log`
- `aiden_bridge_actions.json`
- shard metadata including selected task IDs and embedded Aiden task metadata

For project code context, derive search terms from:

- stack traces and exception names
- file paths and function-like tokens in logs
- API routes and tool names
- failing task categories, suite names, and assertion IDs
- known artifact fields such as `agent_error`, `judge_error`, `execution.error`, `stop_reason`

Then include bounded snippets from matching source files under likely code roots such as `src/agent`, `benchmark/runner`, `benchmark/mobilegym`, and relevant `src/*.cpp` files. Avoid binary artifacts and generated run directories.

## Context Schema and Prioritization

The analysis context should be deterministic and structured. A compact JSON payload is preferable to free-form concatenation:

```json
{
  "run": {"id": "...", "kind": "native|mobilegym", "suite": "...", "totals": {}},
  "suite": {"path": "...", "content_excerpt": "..."},
  "failures": [
    {
      "task_id": "...",
      "status": "failed|timeout|error|judge_error|unknown",
      "rubric": [],
      "hard_assertions": [],
      "errors": [],
      "trace_excerpt": "...",
      "history_excerpt": "...",
      "log_refs": ["relative/path.log:line-range"],
      "artifact_refs": ["relative/path"]
    }
  ],
  "logs": [{"path": "...", "excerpt": "...", "truncated": true}],
  "code": [{"path": "...", "excerpt": "...", "reason": "matched symbol or error"}],
  "collection_warnings": []
}
```

Context priority:

1. Failed, timed out, errored, unknown, and judge-error tasks.
2. Their task specs, rubric, hard assertions, final responses, traces, and errors.
3. Logs near matching error lines or task IDs.
4. Relevant source snippets matched by extracted symbols and paths.
5. Successful tasks only as contrast samples when a failure cluster needs comparison.

Truncation must preserve evidence references. Every excerpt should include its source path and line range when available. If context exceeds the total budget, prefer preserving all failure summaries over long raw logs.

## Prompt Contract

The model prompt should make clear that it is a root-cause analyst, not a judge. It should be instructed to ground every claim in provided evidence and mark uncertainty when evidence is incomplete.

Required structured output fields:

- `summary`: short overall conclusion.
- `failure_clusters`: grouped failures with task IDs, symptoms, evidence, suspected cause, confidence.
- `recommendations`: concrete suggested changes with target area and priority.
- `classification_counts`: counts by root-cause category.
- `evidence_gaps`: missing logs or artifacts that would improve diagnosis.

The markdown report should be generated from the structured JSON so the two artifacts stay consistent.

## Report Integration

HTML report integration is part of v1 acceptance criteria:

- Native `report.html` shows an `LLM Analysis` link or section when `llm_analysis.md` exists.
- MobileGym `index.html` shows the same link or section for direct runs and batch reports.
- If analysis fails, reports may show a small warning link to `llm_analysis_error.txt`, but the main benchmark report remains usable.
- The markdown and JSON artifacts are the source of truth; HTML should not contain a separate, divergent analysis copy unless generated from those artifacts.
- Native board upload must include `llm_analysis.md`, `llm_analysis.json`, and `llm_analysis_error.txt` when present, in addition to `report.html` and `manifest.json`.
- MobileGym local launcher must make linked analysis artifacts reachable from the same report URL namespace, or it must render the markdown/JSON summary inline with no broken links.
- Report HTML must be tested in the same URL shape users open it from. Plain relative links are not acceptable unless the serving route guarantees trailing-slash directory semantics.

## Error Handling

- Missing logs should degrade analysis quality but not fail the benchmark.
- Missing API key or network errors should write `llm_analysis_error.txt` and print a warning.
- Invalid LLM JSON should preserve the raw response in the error artifact.
- Analysis must run after benchmark reports are written, so users still get normal reports if analysis fails.
- LLM HTTP 429/5xx and network timeouts should not retry indefinitely; use one bounded request and report the failure.
- Oversized context should fall back to a smaller context that keeps all failure summaries and drops lower-priority logs/code snippets.
- Artifact writes should be atomic: write `*.tmp` and replace the final file after success.
- Partial context collection errors should be included in `collection_warnings` and surfaced as `evidence_gaps` when relevant.

## Security and Cost Controls

- Limit log bytes per file.
- Limit source snippets per file and total code bytes.
- Limit total request bytes before calling the model.
- Apply deterministic redaction before context is sent to the LLM.
- Redact common secret keys and token-like values, including `api_key`, `API_KEY`, `OPENROUTER_API_KEY`, `MODEL_API_KEY`, `AIDEN_MODEL_API_KEY`, `AIDEN_CONTROL_TOKEN`, bearer tokens, `sk-*` keys, long JWT-like strings, and passwords.
- Redact the env var named by `AIDEN_BENCHMARK_ANALYSIS_API_KEY_ENV` and the resolved secret value from logs, code excerpts, context JSON, error files, and debug output.
- Denylist sensitive files and paths such as `.env`, private key files, token files, `agent.toml` values containing credentials, generated run caches, binaries, images, and archives.
- Do not include binary artifacts or full screenshots in the first version.
- Prefer failed tasks and suspicious logs over all successful-task details.

## Testing Plan

- Unit test native context collection from a synthetic run directory.
- Unit test MobileGym context collection from synthetic batch/shard artifacts.
- Unit test code-context extraction excludes run artifacts and binary files.
- Unit test LLM response parsing and markdown rendering.
- Unit test native runner triggers analysis after report generation when enabled.
- Unit test MobileGym report generation triggers analysis when enabled.
- Unit test LLM failure writes error artifact and preserves original benchmark exit behavior.
- Unit test MobileGym environment propagation through the batch/report path.
- Unit test local launcher does not directly call the LLM and preserves report listing behavior.
- Unit test redaction removes API keys, bearer tokens, and control tokens from logs/code context.
- Unit test byte-budget enforcement preserves failure summaries while truncating lower-priority logs/code.
- Unit test native and MobileGym HTML reports link to `llm_analysis.md` or `llm_analysis_error.txt`.
- Unit test malformed artifacts produce `collection_warnings` instead of failing report generation.
- Unit test analysis credential fallback precedence: custom env var, `OPENROUTER_API_KEY`, `MODEL_API_KEY`, then `AIDEN_MODEL_API_KEY`.
- Unit test custom credential env var name and resolved secret value are redacted.
- Unit test native board upload writes analysis artifacts and generated links are reachable in the board URL shape, or the analysis is inlined.
- Unit test MobileGym local launcher serves `/benchmark/report/<run_id>/<artifact>` when using artifact links, or verifies the report contains an inline analysis section without artifact links.

## Rollout Plan

1. Implement the shared analysis module behind explicit flags/environment controls.
2. Wire native runner and HTML link integration.
3. Wire MobileGym report generation and HTML link integration through the same module.
4. Validate report usefulness on real failed benchmark runs.
5. Consider default-on behavior after runtime cost and security behavior are acceptable.
