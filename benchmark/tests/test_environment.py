import threading
import time
from datetime import datetime, timezone
from pathlib import Path

from runner import environment


def _manager_without_discovery(monkeypatch, tmp_path: Path) -> environment.EnvironmentManager:
    monkeypatch.setattr(environment.EnvironmentManager, "_discover_existing_containers", lambda self: None)
    return environment.EnvironmentManager(runs_dir=tmp_path / "runs")


def test_list_all_coalesces_concurrent_docker_sync(monkeypatch, tmp_path: Path):
    manager = _manager_without_discovery(monkeypatch, tmp_path)
    calls = 0
    calls_lock = threading.Lock()
    start = threading.Barrier(6)

    def fake_list_docker_containers():
        nonlocal calls
        with calls_lock:
            calls += 1
        time.sleep(0.05)
        return []

    monkeypatch.setattr(manager, "_list_docker_containers", fake_list_docker_containers)

    def worker():
        start.wait(timeout=1)
        manager.list_all()

    threads = [threading.Thread(target=worker) for _ in range(5)]
    for thread in threads:
        thread.start()
    start.wait(timeout=1)
    for thread in threads:
        thread.join(timeout=1)

    assert calls == 1


def test_format_container_age_accepts_docker_inspect_rfc3339nano():
    created = datetime.now(timezone.utc).replace(microsecond=123456).isoformat().replace("+00:00", "Z")

    assert environment._format_container_age(created).endswith(" old")


def test_format_container_age_returns_unknown_for_future_timestamp():
    assert environment._format_container_age("2999-01-01T00:00:00Z") == "age unknown"


def test_recovered_mobilegym_container_is_not_reusable_for_new_runs(monkeypatch, tmp_path: Path):
    manager = _manager_without_discovery(monkeypatch, tmp_path)
    monkeypatch.setattr(
        environment,
        "_docker_published_port_safe",
        lambda container_name, container_port: 19090 if container_port == 9090 else 18173,
    )
    monkeypatch.setattr(environment, "_check_endpoint_health", lambda *args, **kwargs: True)
    monkeypatch.setattr(
        environment,
        "_docker_inspect_created",
        lambda container_name: "2026-08-14T00:00:00Z",
    )

    recovered = manager._build_recovered_env(
        {
            "name": "aiden-mobilegym-env-mg-old",
            "id": "container-id",
            "created_at": "2026-08-14 08:00:00 +0800 CST",
            "image": "aiden-mobilegym-simulator:test",
        }
    )

    assert recovered is not None
    assert recovered.status == "stale"
    assert "start a fresh environment" in recovered.message
