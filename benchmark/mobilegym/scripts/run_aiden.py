#!/usr/bin/env python3
from __future__ import annotations

import argparse
import asyncio
import dataclasses as dc
import json
import os
import re
import secrets
import sys
from pathlib import Path
from typing import Any


SCRIPT_PATH = Path(__file__).resolve()
MOBILEGYM_PACKAGE_ROOT = SCRIPT_PATH.parents[1]
BENCHMARK_ROOT = SCRIPT_PATH.parents[2]
DEFAULT_MOBILEGYM_ROOT = MOBILEGYM_PACKAGE_ROOT / "vendor" / "mobilegym"
DEFAULT_RUNS_DIR = BENCHMARK_ROOT / "runs" / "mobilegym"
DEFAULT_ENV_URL = "http://localhost:4173"

_SAFE_SUITE_SEGMENT = re.compile(r"^[A-Za-z0-9_.\-]+$")


def _valid_suite_name(suite_name: str) -> bool:
    if not suite_name or suite_name.startswith("/"):
        return False
    for part in suite_name.split("/"):
        if part in {"", ".", ".."} or not _SAFE_SUITE_SEGMENT.match(part):
            return False
    return True


@dc.dataclass
class MobileGymTaskAdapter:
    """Duck-typed Task object returned to MobileGym SerialRunner when running
    an Aiden JSON suite. Exposes the attributes the runner and adapter read.
    """
    task_id: str
    instruction: str
    metadata: dict[Any, Any]
    apps: list[str] = dc.field(default_factory=list)
    params: dict[str, Any] = dc.field(default_factory=dict)
    # MobileGym sometimes reads `.id` / `.goal`; alias them.

    @property
    def id(self) -> str:
        return self.task_id

    @property
    def goal(self) -> str:
        return self.instruction

    @property
    def description(self) -> str:
        return self.instruction

    @property
    def suite(self) -> str:
        value = self.metadata.get("aiden_suite_name") or self.task_id.rsplit(".", 1)[0]
        return str(value)

    @property
    def name(self) -> str:
        return self.task_id.rsplit(".", 1)[-1]

    @property
    def category(self) -> str:
        return str(self.metadata.get("category") or "aiden")

    @property
    def scope(self) -> str:
        return "S1"

    @property
    def objective(self) -> str:
        return "operate"

    @property
    def composition(self) -> str:
        return "atomic"

    @property
    def difficulty(self) -> str:
        return "L1"

    @property
    def capabilities(self) -> list[str]:
        return []

    async def setup(self, env: Any) -> Any:
        return await env.get_observation()

    def teardown(self, env: Any) -> None:
        del env

    def evaluate(self, evaluation_input: Any) -> Any:
        metadata = _metadata_with_evaluation_input(self.metadata, evaluation_input)
        failures = _evaluate_aiden_metadata(metadata)
        self.metadata.update(metadata)
        if failures:
            return _judge_result(False, "; ".join(failures), progress=0.0)
        return _judge_result(True, "Aiden JSON suite deterministic checks passed", progress=1.0)


@dc.dataclass
class _FallbackJudgeResult:
    success: bool = False
    clean: bool = True
    progress: float = 0.0
    issues: list[dict[str, Any]] = dc.field(default_factory=list)

    @classmethod
    def fail(cls, reason: str) -> "_FallbackJudgeResult":
        return cls(success=False, clean=True, issues=[{"reason": reason}])

    @classmethod
    def pass_(cls, reason: str) -> "_FallbackJudgeResult":
        return cls(success=True, clean=True, progress=1.0, issues=[{"reason": reason}])


def _evaluate_aiden_metadata(metadata: dict[Any, Any]) -> list[str]:
    benchmark_root_str = str(BENCHMARK_ROOT)
    if benchmark_root_str not in sys.path:
        sys.path.insert(0, benchmark_root_str)
    from runner.assertions import (  # type: ignore[import-not-found]
        evaluate_expected_answer,
        evaluate_expected_recalled_memory_ids,
    )

    failures: list[str] = []
    response = str(metadata.get("aiden_last_response") or "")
    history = metadata.get("aiden_last_chat_history")
    if not isinstance(history, list):
        history = []

    expected_answer = metadata.get("expected_answer")
    if expected_answer is not None:
        answer_outcome = evaluate_expected_answer(
            response,
            str(expected_answer),
            str(metadata.get("answer_format") or "option_letter"),
        )
        metadata.update(
            {
                "expected_answer_match": answer_outcome.passed,
                "predicted_answer": answer_outcome.predicted_answer,
                "normalized_expected_answer": answer_outcome.expected_answer,
            }
        )
        if not answer_outcome.passed:
            failures.append(
                "expected answer mismatch: "
                f"expected {answer_outcome.expected_answer}, got {answer_outcome.predicted_answer}"
            )

    expected_memory_ids = metadata.get("expected_recalled_memory_ids") or []
    if expected_memory_ids:
        recall_outcome = evaluate_expected_recalled_memory_ids(history, list(expected_memory_ids))
        metadata.update(
            {
                "expected_recalled_memory_match": recall_outcome.passed,
                "recalled_memory_ids": recall_outcome.recalled_memory_ids,
            }
        )
        if not recall_outcome.passed:
            missing = [
                memory_id
                for memory_id in recall_outcome.expected_memory_ids
                if memory_id not in recall_outcome.recalled_memory_ids
            ]
            failures.append(f"missing expected recalled memory ids: {', '.join(missing)}")

    return failures


def _metadata_with_evaluation_input(metadata: dict[Any, Any], evaluation_input: Any) -> dict[Any, Any]:
    result = dict(metadata)
    if not result.get("aiden_last_response"):
        response = _response_from_evaluation_input(evaluation_input)
        if response:
            result["aiden_last_response"] = response
    if not isinstance(result.get("aiden_last_chat_history"), list):
        history = _history_from_evaluation_input(evaluation_input)
        if history:
            result["aiden_last_chat_history"] = history
    return result


def _response_from_evaluation_input(evaluation_input: Any) -> str:
    for source in _evaluation_payloads(evaluation_input):
        if not isinstance(source, dict):
            continue
        for key in ("aiden_last_response", "response", "agent_message", "agent_answer", "answer", "message", "output", "result"):
            value = source.get(key)
            if isinstance(value, (dict, list)) or value is None:
                continue
            text = str(value).strip()
            if text:
                return text
    return ""


def _history_from_evaluation_input(evaluation_input: Any) -> list[dict[str, Any]]:
    for source in _evaluation_payloads(evaluation_input):
        if not isinstance(source, dict):
            continue
        for key in ("aiden_last_chat_history", "history"):
            value = source.get(key)
            if isinstance(value, list):
                return [entry for entry in value if isinstance(entry, dict)]
    return []


def _evaluation_payloads(evaluation_input: Any) -> list[Any]:
    payloads = [evaluation_input]
    if isinstance(evaluation_input, dict):
        for key in ("data", "execution", "metadata", "result"):
            value = evaluation_input.get(key)
            if isinstance(value, dict):
                payloads.append(value)
        return payloads
    for name in ("data", "execution", "metadata", "result"):
        value = getattr(evaluation_input, name, None)
        if isinstance(value, dict):
            payloads.append(value)
    return payloads


def _judge_result(success: bool, reason: str, *, progress: float) -> Any:
    try:
        from bench_env.task.judge import JudgeResult
    except ModuleNotFoundError:
        return _FallbackJudgeResult(
            success=success,
            clean=True,
            progress=progress,
            issues=[{"reason": reason}],
        )

    if success:
        for name in ("pass_", "passed", "success", "ok"):
            method = getattr(JudgeResult, name, None)
            if callable(method):
                try:
                    return method(reason)
                except TypeError:
                    try:
                        return method()
                    except TypeError:
                        pass
        try:
            return JudgeResult(success=True, clean=True, progress=progress, issues=[])
        except TypeError:
            return _FallbackJudgeResult.pass_(reason)

    fail = getattr(JudgeResult, "fail", None)
    if callable(fail):
        return fail(reason)
    try:
        return JudgeResult(success=False, clean=True, progress=progress, issues=[{"reason": reason}])
    except TypeError:
        return _FallbackJudgeResult.fail(reason)


def _load_aiden_suite_as_mobilegym_tasks(suite_name: str) -> list[MobileGymTaskAdapter]:
    """Load benchmark/suites/<suite_name>.json and convert tasks for MobileGym."""
    if not _valid_suite_name(suite_name):
        raise LauncherError(
            f"invalid suite name: {suite_name!r} "
            "(must be a safe relative path)"
        )
    suite_path = BENCHMARK_ROOT / "suites" / f"{suite_name}.json"
    if not suite_path.exists():
        raise LauncherError(f"Aiden suite not found: {suite_path}")

    benchmark_root_str = str(BENCHMARK_ROOT)
    if benchmark_root_str not in sys.path:
        sys.path.insert(0, benchmark_root_str)
    from runner.suite import load_suite  # type: ignore[import-not-found]

    aiden_suite = load_suite(suite_path)
    return [_convert_task(aiden_suite, t) for t in aiden_suite.tasks]


def _convert_task(suite: Any, task: Any) -> MobileGymTaskAdapter:
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


class LauncherError(RuntimeError):
    pass


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Run the Aiden Go agent against MobileGym tasks.",
    )
    target = parser.add_argument_group("task selection")
    target.add_argument("--task-id", help="Run one MobileGym task id, for example clock.CountAlarms.")
    target.add_argument("--suite", help="Run tasks from one or more comma-separated suites.")
    target.add_argument(
        "--aiden-suite",
        help="Run an Aiden JSON suite from benchmark/suites/<name>.json. "
             "Tasks are converted to MobileGym format on the fly. "
             "Mutually exclusive with --task-id/--suite/--split.",
    )
    target.add_argument(
        "--aiden-task-ids",
        help="Comma-separated Aiden task ids to keep when using --aiden-suite. "
             "Accepts full <suite>.<task> ids or short task ids.",
    )
    target.add_argument("--split", help="Restrict task selection to a MobileGym split, for example test.")
    target.add_argument("--limit", type=_non_negative_int, help="Limit selected tasks for smoke runs.")

    execution = parser.add_argument_group("execution")
    execution.add_argument("--parallel", type=_positive_int, default=1, help="Number of concurrent MobileGym workers.")
    execution.add_argument("--env-url", default=os.getenv("MOBILEGYM_ENV_URL", DEFAULT_ENV_URL), help="MobileGym simulator URL.")
    execution.add_argument("--headless", action="store_true", help="Run browser workers headless.")
    execution.add_argument("--runs-dir", type=Path, default=DEFAULT_RUNS_DIR, help="Directory for MobileGym run artifacts.")
    execution.add_argument("--max-steps", type=_positive_int, help="Override MobileGym max steps per episode.")
    execution.add_argument("--quiet", "-q", action="store_true", help="Reduce MobileGym runner output.")
    execution.add_argument("--shard-index", type=_non_negative_int, default=0, help="Zero-based task shard index.")
    execution.add_argument("--shard-count", type=_positive_int, default=1, help="Total number of task shards.")
    execution.add_argument("--shard-metadata-file", type=Path, help="Write selected shard task metadata to this JSON file.")

    mobilegym = parser.add_argument_group("MobileGym checkout")
    mobilegym.add_argument("--mobilegym-root", type=Path, help="Path to an upstream MobileGym checkout.")

    aiden = parser.add_argument_group("Aiden daemon")
    aiden.add_argument(
        "--aiden-daemon-url",
        default=os.getenv("AIDEN_DAEMON_URL"),
        help="Existing Aiden Go daemon base URL for serial smoke runs. Can also use AIDEN_DAEMON_URL.",
    )
    aiden.add_argument(
        "--aiden-control-token",
        default=default_aiden_control_token(),
        help=(
            "Bearer token for Aiden daemon control endpoints. Can also use AIDEN_CONTROL_TOKEN "
            "or AIDEN_CONTROL_TOKEN_FILE."
        ),
    )
    aiden.add_argument("--chat-timeout-sec", type=float, default=300.0, help="Timeout for one Aiden /api/chat episode.")
    aiden.add_argument("--episode-timeout-sec", type=float, default=30.0, help="Timeout for episode cleanup calls.")
    return parser


def resolve_mobilegym_root(cli_root: str | Path | None) -> tuple[Path, str]:
    if cli_root is not None:
        return Path(cli_root).expanduser(), "--mobilegym-root"
    env_root = os.getenv("MOBILEGYM_ROOT")
    if env_root:
        return Path(env_root).expanduser(), "MOBILEGYM_ROOT"
    return DEFAULT_MOBILEGYM_ROOT, "benchmark/mobilegym/vendor/mobilegym"


def default_aiden_control_token() -> str:
    token = os.getenv("AIDEN_CONTROL_TOKEN")
    if token:
        return token
    token_file = os.getenv("AIDEN_CONTROL_TOKEN_FILE")
    if not token_file:
        return ""
    try:
        return Path(token_file).read_text().strip()
    except OSError:
        return ""


def prepare_import_paths(mobilegym_root: str | Path) -> None:
    benchmark_entry = str(BENCHMARK_ROOT)
    mobilegym_entry = str(Path(mobilegym_root).expanduser())
    existing = [entry for entry in sys.path if entry not in {benchmark_entry, mobilegym_entry}]
    ordered = [benchmark_entry]
    if mobilegym_entry != benchmark_entry:
        ordered.append(mobilegym_entry)
    sys.path[:] = ordered + existing


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return asyncio.run(run(args))
    except LauncherError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        print("Interrupted", file=sys.stderr)
        return 130


async def run(args: argparse.Namespace) -> int:
    _validate_selection(args)
    mobilegym_root, source = resolve_mobilegym_root(args.mobilegym_root)
    _validate_mobilegym_root(mobilegym_root, source)
    prepare_import_paths(mobilegym_root)
    register_aiden_go()
    factory, RunnerConfig, SerialRunner = _import_mobilegym_runtime(mobilegym_root)
    config = RunnerConfig.from_args(_runner_args(args))

    if not args.aiden_daemon_url:
        raise LauncherError(
            "MobileGym imported and aiden_go registered, but no Aiden daemon endpoint is configured. "
            "Start an Aiden Go daemon with a MobileGym agent.toml and pass --aiden-daemon-url "
            "or set AIDEN_DAEMON_URL."
        )

    if args.parallel != 1:
        raise LauncherError(
            "Parallel execution with --parallel > 1 shares a single daemon, which may cause "
            "state interference between workers. For true isolation, use docker/parallel_run.sh "
            "to run tasks in separate containers. Use --parallel 1 for serial execution."
        )

    return await _run_serial(args, config, factory, SerialRunner)


def register_aiden_go() -> None:
    try:
        from mobilegym.adapter.register import register_with_mobilegym

        register_with_mobilegym()
    except ModuleNotFoundError as exc:
        if _module_root(exc) == "bench_env":
            raise LauncherError(
                "Unable to import MobileGym bench_env while registering aiden_go. "
                "Verify --mobilegym-root/MOBILEGYM_ROOT points at an upstream MobileGym checkout "
                "and install its Python dependencies."
            ) from exc
        raise LauncherError(f"Import error while registering aiden_go: {exc}") from exc
    except Exception as exc:
        raise LauncherError(f"failed to register aiden_go with MobileGym: {exc}") from exc


def _import_mobilegym_runtime(mobilegym_root: Path) -> tuple[Any, type[Any], type[Any]]:
    try:
        from bench_env import factory
        from bench_env.config import RunnerConfig
        from bench_env.runner import SerialRunner

        return factory, RunnerConfig, SerialRunner
    except ModuleNotFoundError as exc:
        raise LauncherError(
            f"Unable to import MobileGym runtime modules from {mobilegym_root}. "
            "Install MobileGym dependencies with `pip install -r bench_env/requirements.txt` "
            "from that checkout and run `playwright install chromium`."
        ) from exc


async def _run_serial(args: argparse.Namespace, config: Any, factory: Any, SerialRunner: type[Any]) -> int:
    from bench_env.logger import add_log_file
    from mobilegym.adapter.aiden_go_agent import AidenGoAgent
    from mobilegym.adapter.daemon import AidenDaemonHandle
    from mobilegym.bridge.episode import BridgeEpisodeState
    from mobilegym.bridge.protocol import BridgeTokens
    from mobilegym.bridge.server import BridgeServer

    tasks = factory.load_tasks(config) if not args.aiden_suite else _load_aiden_suite_as_mobilegym_tasks(args.aiden_suite)
    if args.aiden_suite and args.aiden_task_ids:
        tasks = _filter_aiden_tasks(tasks, args.aiden_task_ids)
    if args.limit is not None:
        tasks = tasks[: args.limit]
    if not tasks:
        raise LauncherError("No MobileGym tasks selected after applying filters and --limit.")
    tasks = _shard_tasks(tasks, shard_index=args.shard_index, shard_count=args.shard_count)
    if args.shard_metadata_file:
        _write_shard_metadata(args.shard_metadata_file, tasks, shard_index=args.shard_index, shard_count=args.shard_count)
    if not tasks:
        return 0

    recorder = factory.create_recorder(config)
    env = await factory.create_env(config)
    bridge = None
    run_dir = None
    try:
        bridge_control_token = secrets.token_urlsafe(32)
        bridge_device_token = secrets.token_urlsafe(32)
        bridge_state = BridgeEpisodeState(env, asyncio.get_running_loop())
        bridge_port = int(os.getenv("AIDEN_BRIDGE_PORT", "0"))
        bridge = BridgeServer(
            bridge_state,
            BridgeTokens(control_token=bridge_control_token, device_token=bridge_device_token),
            host=os.getenv("AIDEN_BRIDGE_BIND_HOST", "127.0.0.1"),
            port=bridge_port,
            public_host=os.getenv("AIDEN_BRIDGE_PUBLIC_HOST") or None,
        )
        bridge_url = bridge.start()

        daemon = AidenDaemonHandle(
            base_url=str(args.aiden_daemon_url).rstrip("/"),
            control_token=str(args.aiden_control_token or ""),
            bridge_device_token=bridge_device_token,
        )
        agent = AidenGoAgent(
            bridge_url=bridge_url,
            bridge_control_token=bridge_control_token,
            daemon=daemon,
            chat_timeout_sec=args.chat_timeout_sec,
            episode_timeout_sec=args.episode_timeout_sec,
        )
        evaluator = factory.create_evaluator(config, None)
        recorder.start_run(
            agent="AidenGoAgent",
            model_name=_current_model_name(),
            extra_meta=SerialRunner.build_run_meta(config, tasks),
        )
        if recorder.run_dir:
            run_dir = recorder.run_dir
            agent.artifact_dir = recorder.run_dir
            add_log_file(recorder.run_dir / "console.log")
        runner = SerialRunner(env, agent, tasks, config, recorder, evaluator)
        await runner.run()
        return 0
    finally:
        if run_dir is not None:
            _generate_run_report_best_effort(run_dir)
        if bridge is not None:
            bridge.stop()


def _runner_args(args: argparse.Namespace) -> argparse.Namespace:
    return argparse.Namespace(
        agent="aiden_go",
        model_name=_current_model_name(),
        model_base_url=None,
        model_api_key="",
        task_id=args.task_id,
        task_ids=None,
        suite=args.suite,
        split=args.split,
        env_url=args.env_url,
        headless=args.headless,
        parallel=args.parallel,
        max_steps=args.max_steps,
        quiet=args.quiet,
        runs_dir=args.runs_dir,
        no_save_trajectory=False,
        screenshot_scale=1.0,
        device="sim",
        coord_space="physical",
        delay_after_action=1.0,
        repeat_n=1,
        pass_k=None,
        judge_mode="auto",
        eval_mode="grounded",
        loop_detect=0,
        processes=1,
        isolation="pages",
        num_browsers=0,
        monitor=False,
    )


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
    if args.aiden_task_ids and not args.aiden_suite:
        raise LauncherError("--aiden-task-ids requires --aiden-suite")
    if args.shard_index >= args.shard_count:
        raise LauncherError("--shard-index must be less than --shard-count")


def _filter_aiden_tasks(tasks: list[Any], raw_ids: str) -> list[Any]:
    requested = [part.strip() for part in raw_ids.split(",") if part.strip()]
    if not requested:
        raise LauncherError("--aiden-task-ids must include at least one non-empty id")
    requested_set = set(requested)
    found: set[str] = set()
    selected = []
    for task in tasks:
        full_id = _task_id(task)
        short_id = full_id.rsplit(".", 1)[-1]
        matches = ({full_id, short_id} & requested_set)
        if matches:
            found.update(matches)
            selected.append(task)
    missing = [task_id for task_id in requested if task_id not in found]
    if missing:
        raise LauncherError(f"Aiden task ids not found: {', '.join(missing)}")
    return selected


def _shard_tasks(tasks: list[Any], *, shard_index: int, shard_count: int) -> list[Any]:
    if shard_count == 1:
        return tasks
    return [task for index, task in enumerate(tasks) if index % shard_count == shard_index]


def _write_shard_metadata(path: Path, tasks: list[Any], *, shard_index: int, shard_count: int) -> None:
    payload: dict[str, Any] = {}
    if path.exists():
        try:
            existing = json.loads(path.read_text(encoding="utf-8"))
            if isinstance(existing, dict):
                payload.update(existing)
        except (OSError, json.JSONDecodeError):
            pass
    payload.update(
        {
            "shard_index": shard_index,
            "shard_count": shard_count,
            "selected_task_count": len(tasks),
            "selected_task_ids": [_task_id(task) for task in tasks],
            "task_metadata": _task_report_metadata(tasks),
            "empty": not tasks,
        }
    )
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")


def _generate_run_report_best_effort(run_dir: Path) -> None:
    try:
        from mobilegym.report import generate_reports

        generate_reports(run_dir)
    except Exception as exc:
        print(f"warning: failed to generate MobileGym report for {run_dir}: {exc}", file=sys.stderr)


def _task_id(task: Any) -> str:
    if isinstance(task, str):
        return task
    if isinstance(task, dict):
        for key in ("id", "task_id", "name"):
            if task.get(key):
                return str(task[key])
    for name in ("id", "task_id", "name"):
        value = getattr(task, name, None)
        if value:
            return str(value)
    return str(task)


def _task_report_metadata(tasks: list[Any]) -> dict[str, dict[str, Any]]:
    result: dict[str, dict[str, Any]] = {}
    for task in tasks:
        metadata = getattr(task, "metadata", None)
        if not isinstance(metadata, dict):
            continue
        fields = {
            key: metadata[key]
            for key in ("description_for_judge", "rubric", "hard_assertions")
            if key in metadata
        }
        if fields:
            result[_task_id(task)] = fields
    return result


def _current_model_name() -> str:
    return os.getenv("MODEL_NAME") or os.getenv("AIDEN_MODEL") or os.getenv("OPENAI_MODEL") or "aiden-go"


def _validate_mobilegym_root(path: Path, source: str) -> None:
    if not path.exists() or not path.is_dir():
        raise LauncherError(
            f"MobileGym root not found at {path} (from {source}). "
            "Pass --mobilegym-root, set MOBILEGYM_ROOT, or initialize the vendored submodule with "
            "`git submodule update --init --recursive benchmark/mobilegym/vendor/mobilegym`."
        )
    bench_env = path / "bench_env"
    if not bench_env.exists():
        raise LauncherError(
            f"MobileGym root at {path} does not contain bench_env/. "
            "Pass the upstream MobileGym repository root, not this benchmark/mobilegym package."
        )


def _positive_int(value: str) -> int:
    number = int(value)
    if number < 1:
        raise argparse.ArgumentTypeError("value must be >= 1")
    return number


def _non_negative_int(value: str) -> int:
    number = int(value)
    if number < 0:
        raise argparse.ArgumentTypeError("value must be >= 0")
    return number


def _module_root(exc: ModuleNotFoundError) -> str:
    return str(getattr(exc, "name", "") or "").split(".", 1)[0]


if __name__ == "__main__":
    sys.exit(main())
