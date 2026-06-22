#!/usr/bin/env python3
"""Start MobileGym as a pure simulator with Bridge server.

This script launches MobileGym environment and Bridge HTTP server WITHOUT
using MobileGym's test framework (SerialRunner, agent registration, etc.).
The Bridge exposes device operations via HTTP endpoints that Aiden daemon
can call through the device-operator skill.

Usage:
    python start_simulator.py --env-url http://localhost:4173 --bridge-port 8888
"""
from __future__ import annotations

import argparse
import asyncio
import os
import signal
import sys
from pathlib import Path
from typing import Any

SCRIPT_PATH = Path(__file__).resolve()
MOBILEGYM_PACKAGE_ROOT = SCRIPT_PATH.parents[1]
BENCHMARK_ROOT = SCRIPT_PATH.parents[2]
DEFAULT_MOBILEGYM_ROOT = MOBILEGYM_PACKAGE_ROOT / "vendor" / "mobilegym"
DEFAULT_ENV_URL = "http://localhost:4173"


class SimulatorError(RuntimeError):
    pass


def prepare_import_paths(mobilegym_root: str | Path) -> None:
    """Add MobileGym to Python path."""
    benchmark_entry = str(BENCHMARK_ROOT)
    mobilegym_entry = str(Path(mobilegym_root).expanduser())
    existing = [entry for entry in sys.path if entry not in {benchmark_entry, mobilegym_entry}]
    ordered = [benchmark_entry]
    if mobilegym_entry != benchmark_entry:
        ordered.append(mobilegym_entry)
    sys.path[:] = ordered + existing


def resolve_mobilegym_root(cli_root: str | Path | None) -> tuple[Path, str]:
    """Resolve MobileGym installation path."""
    if cli_root is not None:
        return Path(cli_root).expanduser(), "--mobilegym-root"
    env_root = os.getenv("MOBILEGYM_ROOT")
    if env_root:
        return Path(env_root).expanduser(), "MOBILEGYM_ROOT"
    return DEFAULT_MOBILEGYM_ROOT, "benchmark/mobilegym/vendor/mobilegym"


def validate_mobilegym_root(path: Path, source: str) -> None:
    """Validate that MobileGym installation exists."""
    if not path.exists() or not path.is_dir():
        raise SimulatorError(
            f"MobileGym root not found at {path} (from {source}). "
            "Initialize the submodule with: "
            "`git submodule update --init --recursive benchmark/mobilegym/vendor/mobilegym`"
        )
    bench_env = path / "bench_env"
    if not bench_env.exists():
        raise SimulatorError(
            f"MobileGym root at {path} does not contain bench_env/. "
            "Pass the upstream MobileGym repository root."
        )


async def create_mobilegym_env(env_url: str, headless: bool, device: str = "sim") -> tuple[Any, Any]:
    """Create MobileGym environment without using factory/runner framework.

    Returns:
        Tuple of (env, config) - the environment instance and its config
    """
    try:
        from bench_env.config import EnvConfig
        from bench_env.env.mobile import MobileEnv
    except (ImportError, ModuleNotFoundError):
        try:
            from bench_env.env import MobileGymEnv
        except ModuleNotFoundError as exc:
            raise SimulatorError(
                "Unable to import MobileGym modules. "
                "Install dependencies with: "
                "`pip install -r bench_env/requirements.txt && playwright install chromium`"
            ) from exc

        config = {
            "env_url": env_url,
            "headless": headless,
            "device": device,
            "coord_space": "norm_0_1000",
            "delay_after_action": 1.0,
        }
        env = MobileGymEnv(
            url=env_url,
            headless=headless,
            coord_space="norm_0_1000",
            delay_after_action=1.0,
            verbose=True,
            viewport_size=(360, 800),
            physical_size=(1080, 2400),
            device_scale_factor=3,
        )
        await env.start()
        await env.reset(app_ids=[])
        return env, config
    else:
        config = EnvConfig(
            env_url=env_url,
            headless=headless,
            device=device,
            coord_space="norm_0_1000",
            delay_after_action=1.0,
            screenshot_scale=1.0,
        )
        env = MobileEnv(config)
        await env.reset()
        return env, config


async def create_mobilegym_env_pool(
    env_url: str,
    headless: bool,
    device: str,
    n: int,
    isolation: str = "pages",
    num_browsers: int = 0,
) -> tuple[Any, list[Any], Any]:
    """Create a pool of MobileGym environments for task-id routing."""
    if n <= 1:
        env, config = await create_mobilegym_env(env_url, headless=headless, device=device)
        return None, [env], config
    if device != "sim":
        raise SimulatorError("--parallel-envs > 1 is only supported with --device sim")

    try:
        from bench_env.env import EnvPool
    except ModuleNotFoundError as exc:
        raise SimulatorError(
            "Unable to import MobileGym EnvPool. "
            "Install dependencies with: "
            "`pip install -r bench_env/requirements.txt && playwright install chromium`"
        ) from exc

    pool = EnvPool(
        url=env_url,
        n=n,
        isolation=isolation,
        num_browsers=num_browsers,
        headless=headless,
        coord_space="norm_0_1000",
        delay_after_action=1.0,
        verbose=True,
    )
    await pool.__aenter__()
    envs = list(pool.envs)
    for env in envs:
        try:
            await env.reset(app_ids=[])
        except TypeError:
            await env.reset()
    config = {
        "env_url": env_url,
        "headless": headless,
        "device": "sim",
        "coord_space": "norm_0_1000",
        "delay_after_action": 1.0,
        "parallel_envs": n,
        "isolation": isolation,
        "num_browsers": num_browsers,
    }
    return pool, envs, config


async def run_simulator(args: argparse.Namespace) -> int:
    """Run MobileGym simulator with Bridge server."""
    if args.parallel_envs < 1:
        raise SimulatorError("--parallel-envs must be positive")

    # Resolve and validate MobileGym installation
    mobilegym_root, source = resolve_mobilegym_root(args.mobilegym_root)
    validate_mobilegym_root(mobilegym_root, source)
    prepare_import_paths(mobilegym_root)

    # Import bridge components (no agent registration needed)
    from mobilegym.bridge.episode import BridgeEpisodeState, BridgeTaskRouter
    from mobilegym.bridge.server import BridgeServer

    print(f"Creating MobileGym simulator environment...", flush=True)
    print(f"  Simulator URL: {args.env_url}", flush=True)
    print(f"  Headless: {args.headless}", flush=True)
    print(f"  Parallel envs: {args.parallel_envs}", flush=True)

    # Create environment(s)
    env_pool, envs, env_config = await create_mobilegym_env_pool(
        env_url=args.env_url,
        headless=args.headless,
        device=args.device,
        n=args.parallel_envs,
        isolation=args.env_isolation,
        num_browsers=args.env_browsers,
    )

    print(f"✓ MobileGym environment(s) created: {len(envs)}", flush=True)

    # Create bridge
    owner_loop = asyncio.get_running_loop()
    bridge_states = [BridgeEpisodeState(env, owner_loop) for env in envs]
    bridge_state = bridge_states[0] if len(bridge_states) == 1 else BridgeTaskRouter(bridge_states)
    bridge = BridgeServer(
        bridge_state,
        host=args.bridge_host,
        port=args.bridge_port,
        public_host=args.bridge_public_host or None,
    )

    bridge_url = bridge.start()
    print(f"✓ Bridge server started at {bridge_url}", flush=True)

    # Write bridge URL to file if requested
    if args.bridge_url_file:
        url_file = Path(args.bridge_url_file)
        url_file.parent.mkdir(parents=True, exist_ok=True)
        url_file.write_text(bridge_url)
        print(f"✓ Bridge URL written to {args.bridge_url_file}", flush=True)

    print("\n" + "=" * 60, flush=True)
    print("🚀 MobileGym Simulator Ready", flush=True)
    print("=" * 60, flush=True)
    print(f"Bridge URL: {bridge_url}", flush=True)
    print(f"Health check: curl {bridge_url}/health", flush=True)
    print("\nPress Ctrl+C to stop...", flush=True)
    print("=" * 60 + "\n", flush=True)

    # Setup signal handlers
    stop_event = asyncio.Event()

    def signal_handler(sig, frame):
        print("\n\nReceived shutdown signal, stopping...", flush=True)
        stop_event.set()

    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    # Wait for stop signal
    try:
        await stop_event.wait()
    finally:
        print("Shutting down bridge server...", flush=True)
        bridge.stop()
        print("✓ Bridge stopped", flush=True)
        if env_pool is not None:
            print("Shutting down MobileGym env pool...", flush=True)
            await env_pool.__aexit__(None, None, None)
            print("✓ MobileGym env pool stopped", flush=True)

    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Start MobileGym as a pure simulator with Bridge HTTP server.",
    )

    mobilegym = parser.add_argument_group("MobileGym simulator")
    mobilegym.add_argument(
        "--mobilegym-root",
        type=Path,
        help="Path to MobileGym installation (default: benchmark/mobilegym/vendor/mobilegym)",
    )
    mobilegym.add_argument(
        "--env-url",
        default=os.getenv("MOBILEGYM_ENV_URL", DEFAULT_ENV_URL),
        help="MobileGym web simulator URL (default: http://localhost:4173)",
    )
    mobilegym.add_argument(
        "--headless",
        action="store_true",
        help="Run browser in headless mode",
    )
    mobilegym.add_argument(
        "--device",
        default="sim",
        choices=["sim", "real"],
        help="Device type (default: sim)",
    )
    mobilegym.add_argument(
        "--parallel-envs",
        type=int,
        default=int(os.getenv("MOBILEGYM_PARALLEL_ENVS", "1")),
        help="Number of MobileGym envs behind one bridge (default: 1)",
    )
    mobilegym.add_argument(
        "--env-isolation",
        default=os.getenv("MOBILEGYM_ENV_ISOLATION", "pages"),
        choices=["pages", "contexts", "browsers"],
        help="MobileGym EnvPool isolation mode when --parallel-envs > 1 (default: pages)",
    )
    mobilegym.add_argument(
        "--env-browsers",
        type=int,
        default=int(os.getenv("MOBILEGYM_ENV_BROWSERS", "0")),
        help="Browser process count for EnvPool when --parallel-envs > 1 (default: auto)",
    )

    bridge = parser.add_argument_group("Bridge server")
    bridge.add_argument(
        "--bridge-host",
        default=os.getenv("AIDEN_BRIDGE_BIND_HOST", "127.0.0.1"),
        help="Bridge server bind host (default: 127.0.0.1)",
    )
    bridge.add_argument(
        "--bridge-port",
        type=int,
        default=int(os.getenv("AIDEN_BRIDGE_PORT", "0")),
        help="Bridge server port (default: auto-assign)",
    )
    bridge.add_argument(
        "--bridge-public-host",
        default=os.getenv("AIDEN_BRIDGE_PUBLIC_HOST"),
        help="Bridge server public hostname (for Docker networking)",
    )
    bridge.add_argument(
        "--bridge-url-file",
        type=Path,
        help="Write bridge URL to this file",
    )

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    try:
        return asyncio.run(run_simulator(args))
    except SimulatorError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        print("\nInterrupted", file=sys.stderr)
        return 130


if __name__ == "__main__":
    sys.exit(main())
