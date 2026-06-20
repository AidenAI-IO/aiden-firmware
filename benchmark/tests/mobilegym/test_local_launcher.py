import importlib.util
import sys
from pathlib import Path


BENCHMARK_ROOT = Path(__file__).resolve().parents[2]
LOCAL_LAUNCHER = BENCHMARK_ROOT / "mobilegym" / "scripts" / "local_launcher.py"


def load_local_launcher_module():
    spec = importlib.util.spec_from_file_location("local_launcher_test_module", LOCAL_LAUNCHER)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def test_build_run_command_supports_skillopt_mobilegym_on_host(monkeypatch, tmp_path: Path):
    module = load_local_launcher_module()
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-local")

    command = module.build_run_command(
        tmp_path,
        {
            "mode": "skillopt",
            "skill": "device-operator",
            "skillopt_backend": "mobilegym",
            "train_suite": "skillopt/device-operator/device_operator_train",
            "validation_suite": "skillopt/device-operator/device_operator_verification",
            "budget": 2,
            "edit_budget": 3,
            "min_delta": 0,
            "mobilegym_parallel": 4,
        },
    )

    assert command.cwd == tmp_path
    argv = command.argv
    assert argv[:3] == [sys.executable, "-m", "runner.skillopt"]
    assert "--backend" in argv
    assert argv[argv.index("--backend") + 1] == "mobilegym"
    assert argv[argv.index("--mobilegym-parallel") + 1] == "4"
    assert argv[argv.index("--train-suite") + 1] == "skillopt/device-operator/device_operator_train"
    assert argv[argv.index("--validation-suite") + 1] == "skillopt/device-operator/device_operator_verification"
    assert argv[argv.index("--min-delta") + 1] == "0"
    assert argv[argv.index("--artifact-root") + 1] == str(tmp_path / "runs" / "skillopt")
    assert command.env["OPENROUTER_API_KEY"] == "sk-local"


def test_build_run_command_rejects_skillopt_device_on_host(tmp_path: Path):
    module = load_local_launcher_module()

    try:
        module.build_run_command(
            tmp_path,
            {
                "mode": "skillopt",
                "skill": "device-operator",
                "skillopt_backend": "device",
                "train_suite": "skillopt/device-operator/device_operator_train",
                "validation_suite": "skillopt/device-operator/device_operator_verification",
            },
        )
    except module.LauncherError as exc:
        assert "requires skillopt_backend=mobilegym" in str(exc)
    else:
        raise AssertionError("expected device SkillOpt backend to be rejected by local launcher")


def test_report_file_for_serves_skillopt_artifact(tmp_path: Path):
    module = load_local_launcher_module()
    run_dir = tmp_path / "runs" / "skillopt" / "skillopt-run"
    run_dir.mkdir(parents=True)
    artifact = run_dir / "best_skill.md"
    artifact.write_text("skill contents", encoding="utf-8")

    assert module.report_file_for(tmp_path, "skillopt-run/best_skill.md") == artifact
