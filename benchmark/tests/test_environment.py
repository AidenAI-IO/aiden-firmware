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
