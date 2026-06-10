#!/usr/bin/env python3
from __future__ import annotations

import argparse
import asyncio
import os
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


class LauncherError(RuntimeError):
    pass


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Run the Aiden Go agent against MobileGym tasks.",
    )
    target = parser.add_argument_group("task selection")
    target.add_argument("--task-id", help="Run one MobileGym task id, for example clock.CountAlarms.")
    target.add_argument("--suite", help="Run tasks from one or more comma-separated suites.")
    target.add_argument("--split", help="Restrict task selection to a MobileGym split, for example test.")
    target.add_argument("--limit", type=_non_negative_int, help="Limit selected tasks for smoke runs.")

    execution = parser.add_argument_group("execution")
    execution.add_argument("--parallel", type=_positive_int, default=1, help="Number of concurrent MobileGym workers.")
    execution.add_argument("--env-url", default=os.getenv("MOBILEGYM_ENV_URL", DEFAULT_ENV_URL), help="MobileGym simulator URL.")
    execution.add_argument("--headless", action="store_true", help="Run browser workers headless.")
    execution.add_argument("--runs-dir", type=Path, default=DEFAULT_RUNS_DIR, help="Directory for MobileGym run artifacts.")
    execution.add_argument("--max-steps", type=_positive_int, help="Override MobileGym max steps per episode.")
    execution.add_argument("--quiet", "-q", action="store_true", help="Reduce MobileGym runner output.")

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

    tasks = factory.load_tasks(config)
    if args.limit is not None:
        tasks = tasks[: args.limit]
    if not tasks:
        raise LauncherError("No MobileGym tasks selected after applying filters and --limit.")

    recorder = factory.create_recorder(config)
    env = await factory.create_env(config)
    bridge = None
    try:
        bridge_control_token = secrets.token_urlsafe(32)
        bridge_device_token = secrets.token_urlsafe(32)
        bridge_state = BridgeEpisodeState(env, asyncio.get_running_loop())
        bridge = BridgeServer(
            bridge_state,
            BridgeTokens(control_token=bridge_control_token, device_token=bridge_device_token),
            host=os.getenv("AIDEN_BRIDGE_BIND_HOST", "127.0.0.1"),
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
            model_name="aiden-go",
            extra_meta=SerialRunner.build_run_meta(config, tasks),
        )
        if recorder.run_dir:
            add_log_file(recorder.run_dir / "console.log")
        runner = SerialRunner(env, agent, tasks, config, recorder, evaluator)
        await runner.run()
        return 0
    finally:
        if bridge is not None:
            bridge.stop()


def _runner_args(args: argparse.Namespace) -> argparse.Namespace:
    return argparse.Namespace(
        agent="aiden_go",
        model_name="aiden-go",
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
        coord_space="norm_0_1000",
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
    if not args.task_id and not args.suite and not args.split:
        raise LauncherError("select at least one task with --task-id, --suite, or --split")


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
