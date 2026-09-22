from __future__ import annotations

import argparse
import json
import os
import signal
import subprocess
import sys
from pathlib import Path
from typing import Any, Mapping

from ci.plan import BENCHMARK_ROOT, CatalogError, SuiteCase, load_catalog


def _run_json(command: list[str]) -> dict[str, Any]:
    completed = subprocess.run(
        command,
        cwd=BENCHMARK_ROOT,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
    )
    try:
        payload = json.loads(completed.stdout)
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
) -> tuple[str, dict[str, Any] | None]:
    python = sys.executable
    if case.environment == "isolated":
        return "", None
    if case.environment == "mobilegym":
        payload = _run_json(
            [
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
            ]
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


def run_case(case: SuiteCase, *, run_id: str, environment: Mapping[str, str]) -> int:
    environment_url = ""
    service: dict[str, Any] | None = None
    try:
        environment_url, service = _environment_url(
            case,
            run_id=run_id,
            environment=environment,
        )
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
        returncode = subprocess.run(command, cwd=BENCHMARK_ROOT, check=False).returncode
        return _effective_exit_code(returncode, BENCHMARK_ROOT / "runs" / run_id / "manifest.json")
    finally:
        _stop_service(service)


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
