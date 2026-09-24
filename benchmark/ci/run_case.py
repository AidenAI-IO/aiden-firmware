from __future__ import annotations

import argparse
import json
import os
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Any, Mapping

from ci.plan import BENCHMARK_ROOT, CatalogError, SuiteCase, load_catalog
from runner.process import terminate_process_tree


ACTION_ENVIRONMENT_ALIASES = {
    "BENCHMARK_AGENT_PROVIDER": "AIDEN_BENCHMARK_AGENT_PROVIDER",
    "BENCHMARK_AGENT_MODEL": "AIDEN_BENCHMARK_AGENT_MODEL",
    "BENCHMARK_AGENT_BASE_URL": "AIDEN_BENCHMARK_AGENT_BASE_URL",
    "BENCHMARK_AGENT_API_KEY": "AIDEN_BENCHMARK_AGENT_API_KEY",
    "BENCHMARK_JUDGE_MODEL": "AIDEN_BENCHMARK_JUDGE_MODEL",
    "BENCHMARK_JUDGE_BASE_URL": "AIDEN_BENCHMARK_JUDGE_BASE_URL",
    "BENCHMARK_JUDGE_API_KEY": "AIDEN_BENCHMARK_JUDGE_API_KEY",
    "DAEMON_IMAGE": "AIDEN_DAEMON_IMAGE",
    "BENCHMARK_PHONE_ENVIRONMENT_URL": "AIDEN_BENCHMARK_PHONE_ENVIRONMENT_URL",
    "BENCHMARK_IOS_ENVIRONMENT_URL": "AIDEN_BENCHMARK_IOS_ENVIRONMENT_URL",
    "BENCHMARK_MAC_ENVIRONMENT_URL": "AIDEN_BENCHMARK_MAC_ENVIRONMENT_URL",
    "BENCHMARK_VPHONE_ENVIRONMENT_URL": "AIDEN_BENCHMARK_VPHONE_ENVIRONMENT_URL",
    "BENCHMARK_AIDEN_APP_IOS_ENVIRONMENT_URL": (
        "AIDEN_BENCHMARK_AIDEN_APP_IOS_ENVIRONMENT_URL"
    ),
    "BENCHMARK_AIDEN_APP_ANDROID_ENVIRONMENT_URL": (
        "AIDEN_BENCHMARK_AIDEN_APP_ANDROID_ENVIRONMENT_URL"
    ),
}
IMAGES_PREPARED_ENV = "BENCHMARK_CI_IMAGES_PREPARED"


def _runtime_environment(environment: Mapping[str, str]) -> dict[str, str]:
    runtime = dict(environment)
    for action_name, runtime_name in ACTION_ENVIRONMENT_ALIASES.items():
        if action_name in environment:
            runtime[runtime_name] = environment[action_name]
    return runtime


def _images_prepared(environment: Mapping[str, str]) -> bool:
    return environment.get(IMAGES_PREPARED_ENV, "").strip().lower() in {
        "1",
        "true",
        "yes",
    }


def _run_json(
    command: list[str],
    *,
    environment: Mapping[str, str],
    log_file: Any | None = None,
    timeout_seconds: float | None = None,
) -> dict[str, Any]:
    popen_kwargs: dict[str, Any] = {
        "cwd": BENCHMARK_ROOT,
        "env": environment,
        "text": True,
        "stdout": subprocess.PIPE,
        "stderr": log_file,
    }
    if timeout_seconds is not None:
        popen_kwargs["start_new_session"] = os.name == "posix"
    proc = subprocess.Popen(command, **popen_kwargs)
    try:
        stdout, _ = proc.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        terminate_process_tree(proc)
        raise
    if proc.returncode:
        raise subprocess.CalledProcessError(
            proc.returncode,
            command,
            output=stdout,
        )
    try:
        payload = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(
            f"service command returned invalid JSON: {' '.join(command)}"
        ) from exc
    if not isinstance(payload, dict):
        raise RuntimeError("service command did not return a JSON object")
    return payload


def _environment_url(
    case: SuiteCase,
    *,
    run_id: str,
    environment: Mapping[str, str],
    log_file: Any | None = None,
    timeout_seconds: float | None = None,
) -> tuple[str, dict[str, Any] | None]:
    python = sys.executable
    if case.environment == "isolated":
        return "", None
    if case.environment == "mobilegym":
        command = [
            python,
            "-m",
            "runner",
            "start-mobilegym-env",
            "--name",
            run_id,
            "--envs",
            str(case.max_concurrency),
            "--json",
        ]
        if _images_prepared(environment):
            command.append("--no-build-mobilegym-image")
        payload = _run_json(
            command,
            environment=environment,
            log_file=log_file,
            timeout_seconds=timeout_seconds,
        )
        return str(payload["environment_url"]), payload
    if case.environment == "adb":
        serial = environment.get("ANDROID_SERIAL", "").strip()
        if not serial:
            raise RuntimeError("ADB case requires ANDROID_SERIAL")
        payload = _run_json(
            [
                python,
                "-m",
                "runner",
                "start-adb-android-env",
                "--name",
                run_id,
                "--adb-serial",
                serial,
                "--json",
            ],
            environment=environment,
            log_file=log_file,
            timeout_seconds=timeout_seconds,
        )
        return str(payload["environment_url"]), payload
    variable = case.environment_variable
    value = environment.get(variable, "").strip()
    if not value:
        raise RuntimeError(f"{case.id} requires {variable}")
    return value, None


def _stop_service(payload: dict[str, Any] | None) -> None:
    if not payload:
        return
    container_name = str(payload.get("container_name") or "").strip()
    if container_name:
        subprocess.run(
            ["docker", "rm", "-f", container_name],
            cwd=BENCHMARK_ROOT,
            check=False,
        )
    pid = payload.get("pid")
    if isinstance(pid, int) and pid > 0:
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass


def _stop_mobilegym_by_run_id(run_id: str) -> None:
    subprocess.run(
        ["docker", "rm", "-f", f"aiden-mobilegym-env-mobilegym-{run_id}"],
        cwd=BENCHMARK_ROOT,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )


def _effective_exit_code(returncode: int, manifest_path: Path) -> int:
    """Treat infrastructure skips as CI failures instead of false greens."""
    if not manifest_path.is_file():
        return returncode or 1
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        totals = manifest["totals"]
        tasks = int(totals["tasks"])
        skipped = int(totals["skipped"])
        accounted_failures = sum(
            int(totals.get(key, 0))
            for key in ("failed", "judge_error", "timeout")
        )
        results_path = manifest_path.with_name("results.jsonl")
        unexpected_skips = 0
        for line in results_path.read_text(encoding="utf-8").splitlines():
            row = json.loads(line)
            if row.get("status") != "skipped":
                continue
            reason = str((row.get("metrics") or {}).get("error") or "")
            if not (
                reason.startswith("task platforms ")
                or "target platform constraint does not match source" in reason
            ):
                unexpected_skips += 1
    except (OSError, KeyError, TypeError, ValueError, json.JSONDecodeError):
        return returncode or 1
    if unexpected_skips or (tasks > 0 and skipped == tasks and accounted_failures == 0):
        return 1
    return returncode


def run_case(
    case: SuiteCase,
    *,
    run_id: str,
    environment: Mapping[str, str],
    log_path: Path | None = None,
    timeout_seconds: float | None = None,
) -> int:
    runtime_environment = _runtime_environment(environment)
    environment_url = ""
    service: dict[str, Any] | None = None
    deadline = (
        time.monotonic() + timeout_seconds
        if timeout_seconds is not None
        else None
    )

    def remaining_timeout() -> float | None:
        if deadline is None:
            return None
        return max(0.001, deadline - time.monotonic())

    # Concurrent cases interleave unreadably on a shared stdout, so each one can
    # capture into its own file instead. Without log_path the output stays inherited,
    # which is what the single-case CLI wants.
    log_file = None
    if log_path is not None:
        log_path.parent.mkdir(parents=True, exist_ok=True)
        log_file = log_path.open("w", encoding="utf-8")
    try:
        try:
            environment_url, service = _environment_url(
                case,
                run_id=run_id,
                environment=runtime_environment,
                log_file=log_file,
                timeout_seconds=remaining_timeout(),
            )
        except subprocess.TimeoutExpired:
            message = f"case {case.id} exceeded {timeout_seconds:.0f}s during environment startup"
            if log_file is not None:
                log_file.write(f"\n{message}\n")
            print(f"Error: {message}", file=sys.stderr, flush=True)
            return 1
        command = [
            sys.executable,
            "-m",
            "runner",
            "run",
            "--suite",
            case.suite,
            "--run-id",
            run_id,
            "--auto-agent-setup",
            "--max-concurrency",
            str(case.max_concurrency),
            "--verbose",
        ]
        if environment_url:
            command.extend(["--environment-url", environment_url])
        if case.target_platform != "auto":
            command.extend(["--target-platform", case.target_platform])
        if _images_prepared(runtime_environment):
            command.append("--no-build-daemon-image")
        if timeout_seconds is None:
            returncode = subprocess.run(
                command,
                cwd=BENCHMARK_ROOT,
                env=runtime_environment,
                check=False,
                stdout=log_file,
                stderr=subprocess.STDOUT if log_file is not None else None,
            ).returncode
        else:
            popen_kwargs: dict[str, Any] = {
                "cwd": BENCHMARK_ROOT,
                "env": runtime_environment,
                "stdout": log_file,
                "stderr": subprocess.STDOUT,
                "start_new_session": os.name == "posix",
            }
            proc = subprocess.Popen(command, **popen_kwargs)
            try:
                returncode = proc.wait(timeout=remaining_timeout())
            except subprocess.TimeoutExpired:
                # One overrunning case must not consume the whole job's budget;
                # terminate the runner and every daemon it spawned.
                terminate_process_tree(proc)
                message = f"case {case.id} exceeded {timeout_seconds:.0f}s and was killed"
                if log_file is not None:
                    log_file.write(f"\n{message}\n")
                print(f"Error: {message}", file=sys.stderr, flush=True)
                return 1
        return _effective_exit_code(returncode, BENCHMARK_ROOT / "runs" / run_id / "manifest.json")
    finally:
        _stop_service(service)
        if case.environment == "mobilegym" and service is None:
            _stop_mobilegym_by_run_id(run_id)
        if log_file is not None:
            log_file.close()


def cli(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run one catalogued benchmark CI case")
    parser.add_argument("--case-id", required=True)
    parser.add_argument("--run-id", required=True)
    args = parser.parse_args(argv)
    try:
        catalog = load_catalog()
        case = next((item for item in catalog.cases if item.id == args.case_id), None)
        if case is None:
            raise CatalogError(f"unknown benchmark CI case: {args.case_id}")
        return run_case(case, run_id=args.run_id, environment=os.environ)
    except (CatalogError, RuntimeError, KeyError, subprocess.CalledProcessError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(cli())
