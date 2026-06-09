import importlib.util
import os
import subprocess
import sys
from pathlib import Path


BENCHMARK_ROOT = Path(__file__).resolve().parents[2]
RUN_AIDEN = BENCHMARK_ROOT / "mobilegym" / "scripts" / "run_aiden.py"


def load_run_aiden_module():
    spec = importlib.util.spec_from_file_location("run_aiden_test_module", RUN_AIDEN)
    module = importlib.util.module_from_spec(spec)
    assert spec is not None
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def test_launcher_help_does_not_require_mobilegym_root():
    env = os.environ.copy()
    env.pop("MOBILEGYM_ROOT", None)

    result = subprocess.run(
        [sys.executable, "mobilegym/scripts/run_aiden.py", "--help"],
        cwd=BENCHMARK_ROOT,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 0
    assert "--task-id" in result.stdout
    assert "--mobilegym-root" in result.stdout
    assert "MobileGym root not found" not in result.stderr


def test_launcher_real_run_fails_clearly_when_mobilegym_root_missing(tmp_path):
    missing_root = tmp_path / "missing-mobilegym"

    result = subprocess.run(
        [
            sys.executable,
            "mobilegym/scripts/run_aiden.py",
            "--task-id",
            "clock.CountAlarms",
            "--mobilegym-root",
            str(missing_root),
        ],
        cwd=BENCHMARK_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 2
    assert "MobileGym root not found" in result.stderr
    assert "--mobilegym-root" in result.stderr
    assert "MOBILEGYM_ROOT" in result.stderr
    assert "git submodule update" in result.stderr


def test_resolve_mobilegym_root_precedence(monkeypatch, tmp_path):
    module = load_run_aiden_module()
    cli_root = tmp_path / "cli-root"
    env_root = tmp_path / "env-root"
    monkeypatch.setenv("MOBILEGYM_ROOT", str(env_root))

    root, source = module.resolve_mobilegym_root(cli_root)

    assert root == cli_root
    assert source == "--mobilegym-root"

    root, source = module.resolve_mobilegym_root(None)

    assert root == env_root
    assert source == "MOBILEGYM_ROOT"


def test_prepare_import_paths_keeps_local_mobilegym_before_vendor(monkeypatch, tmp_path):
    module = load_run_aiden_module()
    vendor_root = tmp_path / "vendor" / "mobilegym"
    monkeypatch.setattr(sys, "path", [str(vendor_root), str(BENCHMARK_ROOT), "keep-me"])

    module.prepare_import_paths(vendor_root)

    benchmark_index = sys.path.index(str(BENCHMARK_ROOT))
    vendor_index = sys.path.index(str(vendor_root))

    assert benchmark_index < vendor_index
    assert sys.path.count(str(BENCHMARK_ROOT)) == 1
    assert sys.path.count(str(vendor_root)) == 1
    assert "keep-me" in sys.path
