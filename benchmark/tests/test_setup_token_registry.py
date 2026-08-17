import threading
import time

import pytest

from setup_token_registry import SetupTokenRegistry


def test_failed_setup_token_can_retry_and_then_caches_success():
    registry = SetupTokenRegistry()
    calls = 0

    def setup():
        nonlocal calls
        calls += 1
        if calls == 1:
            raise RuntimeError("transient setup failure")
        return {"setup": True}

    with pytest.raises(RuntimeError, match="transient setup failure"):
        registry.run(("task-1", "token-1"), setup)

    assert registry.run(("task-1", "token-1"), setup) == {"setup": True}
    assert registry.run(("task-1", "token-1"), setup) == {"setup": True}
    assert calls == 2


def test_clear_completed_for_task_keeps_in_flight_entry():
    registry = SetupTokenRegistry()
    calls = 0
    entered = threading.Event()
    allow_setup = threading.Event()
    results = []

    def setup():
        nonlocal calls
        calls += 1
        entered.set()
        allow_setup.wait(timeout=5)
        return {"setup": True}

    first = threading.Thread(
        target=lambda: results.append(registry.run(("task-1", "token-1"), setup))
    )
    second = threading.Thread(
        target=lambda: results.append(registry.run(("task-1", "token-1"), setup))
    )
    first.start()
    assert entered.wait(timeout=2)

    assert registry.clear_completed_for_task("task-1") == 0
    second.start()
    time.sleep(0.1)
    assert calls == 1

    allow_setup.set()
    first.join(timeout=5)
    second.join(timeout=5)
    assert results == [{"setup": True}, {"setup": True}]
    assert registry.run(("task-1", "token-1"), setup) == {"setup": True}
    assert calls == 2

    assert registry.clear_completed_for_task("task-1") == 1
