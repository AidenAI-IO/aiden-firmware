import importlib.util
import asyncio
import json
import os
import subprocess
import sys
import types
from pathlib import Path


BENCHMARK_ROOT = Path(__file__).resolve().parents[2]
RUN_AIDEN = BENCHMARK_ROOT / "mobilegym" / "scripts" / "run_aiden.py"


def load_run_aiden_module():
    spec = importlib.util.spec_from_file_location("run_aiden_test_module", RUN_AIDEN)
    module = importlib.util.module_from_spec(spec)
    assert spec is not None
    assert spec.loader is not None
    sys.modules[spec.name] = module
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


def test_default_aiden_control_token_reads_env_or_file(monkeypatch, tmp_path):
    module = load_run_aiden_module()
    token_file = tmp_path / "control_token"
    token_file.write_text("file-token\n")

    monkeypatch.delenv("AIDEN_CONTROL_TOKEN", raising=False)
    monkeypatch.setenv("AIDEN_CONTROL_TOKEN_FILE", str(token_file))

    assert module.default_aiden_control_token() == "file-token"

    monkeypatch.setenv("AIDEN_CONTROL_TOKEN", "env-token")

    assert module.default_aiden_control_token() == "env-token"


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


def test_shard_tasks_selects_stable_round_robin_subset():
    module = load_run_aiden_module()
    tasks = ["task-0", "task-1", "task-2", "task-3", "task-4"]

    assert module._shard_tasks(tasks, shard_index=0, shard_count=2) == ["task-0", "task-2", "task-4"]
    assert module._shard_tasks(tasks, shard_index=1, shard_count=2) == ["task-1", "task-3"]
    assert module._shard_tasks(tasks, shard_index=0, shard_count=1) == tasks


def test_write_shard_metadata_records_selected_task_ids(tmp_path):
    module = load_run_aiden_module()
    metadata = tmp_path / "shard.json"
    object_task = type("Task", (), {"id": "clock.CountAlarms"})()
    dict_task = {"id": "clock.ToggleAlarm"}

    module._write_shard_metadata(metadata, [object_task, dict_task, "raw.Task"], shard_index=1, shard_count=4)

    payload = json.loads(metadata.read_text())
    assert payload["selected_task_count"] == 3
    assert payload["selected_task_ids"] == ["clock.CountAlarms", "clock.ToggleAlarm", "raw.Task"]
    assert payload["shard_index"] == 1
    assert payload["shard_count"] == 4
    assert payload["empty"] is False


def test_write_shard_metadata_preserves_existing_worker_fields(tmp_path):
    module = load_run_aiden_module()
    metadata = tmp_path / "shard.json"
    metadata.write_text('{"batch_id":"batch-x","exit_code":99}')

    module._write_shard_metadata(metadata, ["task.A"], shard_index=0, shard_count=1)

    payload = json.loads(metadata.read_text())
    assert payload["batch_id"] == "batch-x"
    assert payload["exit_code"] == 99
    assert payload["selected_task_count"] == 1
    assert payload["selected_task_ids"] == ["task.A"]


def test_mobilegym_task_adapter_exposes_runner_interface():
    module = load_run_aiden_module()
    task = module.MobileGymTaskAdapter(
        task_id="perception_v1.find_settings_iphone",
        instruction="Find Settings",
        metadata={"aiden_suite_name": "perception_v1", "category": "perception"},
    )

    class FakeEnv:
        async def get_observation(self):
            return "initial observation"

    assert task.id == "perception_v1.find_settings_iphone"
    assert task.name == "find_settings_iphone"
    assert task.suite == "perception_v1"
    assert task.description == "Find Settings"
    assert task.goal == "Find Settings"
    assert task.apps == []
    assert task.params == {}
    assert task.teardown(object()) is None
    assert asyncio.run(task.setup(FakeEnv())) == "initial observation"
    judge = task.evaluate(object())
    assert judge.success is True
    assert judge.clean is True
    assert judge.progress == 1.0


def test_generate_run_report_best_effort_writes_index(tmp_path):
    module = load_run_aiden_module()
    run_dir = tmp_path / "run"
    run_dir.mkdir()
    (run_dir / "meta.json").write_text(json.dumps({"suite": ["clock"]}))
    (run_dir / "results.jsonl").write_text(json.dumps({"id": "clock.CountAlarms", "is_success": True}) + "\n")

    module._generate_run_report_best_effort(run_dir)

    assert (run_dir / "index.html").exists()


def test_run_serial_sets_agent_artifact_dir_to_recorder_run_dir(monkeypatch, tmp_path):
    module = load_run_aiden_module()
    captured = {}

    class FakeRecorder:
        run_dir = tmp_path / "run"

        def start_run(self, **kwargs):
            self.run_dir.mkdir()

    class FakeFactory:
        @staticmethod
        def load_tasks(config):
            return ["task.A"]

        @staticmethod
        def create_recorder(config):
            return FakeRecorder()

        @staticmethod
        async def create_env(config):
            return object()

        @staticmethod
        def create_evaluator(config, value):
            return object()

    class FakeBridge:
        def __init__(self, *args, **kwargs):
            pass

        def start(self):
            return "http://bridge.local"

        def stop(self):
            pass

    class FakeAgent:
        def __init__(self, **kwargs):
            self.artifact_dir = None
            captured["agent"] = self

    class FakeRunner:
        @staticmethod
        def build_run_meta(config, tasks):
            return {"tasks": tasks}

        def __init__(self, *args):
            pass

        async def run(self):
            pass

    monkeypatch.setitem(sys.modules, "bench_env.logger", types.SimpleNamespace(add_log_file=lambda path: None))
    monkeypatch.setitem(sys.modules, "mobilegym.adapter.aiden_go_agent", types.SimpleNamespace(AidenGoAgent=FakeAgent))
    monkeypatch.setitem(sys.modules, "mobilegym.adapter.daemon", types.SimpleNamespace(AidenDaemonHandle=lambda **kwargs: object()))
    monkeypatch.setitem(sys.modules, "mobilegym.bridge.episode", types.SimpleNamespace(BridgeEpisodeState=lambda *args, **kwargs: object()))
    monkeypatch.setitem(sys.modules, "mobilegym.bridge.protocol", types.SimpleNamespace(BridgeTokens=lambda **kwargs: object()))
    monkeypatch.setitem(sys.modules, "mobilegym.bridge.server", types.SimpleNamespace(BridgeServer=FakeBridge))
    monkeypatch.setattr(module, "_generate_run_report_best_effort", lambda run_dir: None)

    args = types.SimpleNamespace(
        aiden_suite=None,
        limit=None,
        shard_index=0,
        shard_count=1,
        shard_metadata_file=None,
        aiden_daemon_url="http://daemon.local",
        aiden_control_token="token",
        chat_timeout_sec=1,
        episode_timeout_sec=1,
    )

    assert asyncio.run(module._run_serial(args, object(), FakeFactory, FakeRunner)) == 0
    assert captured["agent"].artifact_dir == tmp_path / "run"
