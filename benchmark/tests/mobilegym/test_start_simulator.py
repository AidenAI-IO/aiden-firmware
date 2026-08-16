import asyncio
import sys
import types

import pytest
from mobilegym.scripts import start_simulator


def test_create_mobilegym_env_uses_normalized_coordinate_space(monkeypatch):
    captured = {}

    bench_env = types.ModuleType("bench_env")
    bench_env.__path__ = []
    config_mod = types.ModuleType("bench_env.config")
    env_mod = types.ModuleType("bench_env.env")
    env_mod.__path__ = []
    mobile_mod = types.ModuleType("bench_env.env.mobile")

    class EnvConfig:
        def __init__(self, **kwargs):
            captured["config_kwargs"] = kwargs
            for key, value in kwargs.items():
                setattr(self, key, value)

    class MobileEnv:
        def __init__(self, config):
            captured["env_config"] = config

        async def reset(self):
            captured["reset_called"] = True

    config_mod.EnvConfig = EnvConfig
    mobile_mod.MobileEnv = MobileEnv
    bench_env.config = config_mod
    bench_env.env = env_mod
    env_mod.mobile = mobile_mod

    monkeypatch.setitem(sys.modules, "bench_env", bench_env)
    monkeypatch.setitem(sys.modules, "bench_env.config", config_mod)
    monkeypatch.setitem(sys.modules, "bench_env.env", env_mod)
    monkeypatch.setitem(sys.modules, "bench_env.env.mobile", mobile_mod)

    _, config = asyncio.run(start_simulator.create_mobilegym_env("http://simulator", headless=True))

    assert config.coord_space == "norm_0_1000"
    assert captured["config_kwargs"]["coord_space"] == "norm_0_1000"
    assert captured["env_config"] is config
    assert captured["reset_called"] is True


def test_create_mobilegym_env_pool_uses_envpool(monkeypatch):
    captured = {}

    bench_env = types.ModuleType("bench_env")
    bench_env.__path__ = []
    env_mod = types.ModuleType("bench_env.env")

    class FakeEnv:
        def __init__(self):
            self.reset_called = False

        async def reset(self, app_ids=None):
            self.reset_called = True
            captured.setdefault("reset_app_ids", []).append(app_ids)

    class EnvPool:
        def __init__(self, **kwargs):
            captured["pool_kwargs"] = kwargs
            self.envs = [FakeEnv(), FakeEnv()]

        async def __aenter__(self):
            captured["entered"] = True
            return self

        async def __aexit__(self, *exc):
            captured["exited"] = True

    env_mod.EnvPool = EnvPool
    bench_env.env = env_mod

    monkeypatch.setitem(sys.modules, "bench_env", bench_env)
    monkeypatch.setitem(sys.modules, "bench_env.env", env_mod)

    pool, envs, config = asyncio.run(
        start_simulator.create_mobilegym_env_pool(
            "http://simulator",
            headless=True,
            device="sim",
            n=2,
            isolation="contexts",
            num_browsers=1,
        )
    )

    assert pool is not None
    assert len(envs) == 2
    assert all(env.reset_called for env in envs)
    assert captured["entered"] is True
    assert captured["reset_app_ids"] == [[], []]
    assert captured["pool_kwargs"]["n"] == 2
    assert captured["pool_kwargs"]["isolation"] == "contexts"
    assert captured["pool_kwargs"]["num_browsers"] == 1
    assert config["parallel_envs"] == 2


def test_reset_mobilegym_env_supports_legacy_reset_signature():
    class LegacyEnv:
        def __init__(self):
            self.reset_calls = 0

        async def reset(self):
            self.reset_calls += 1

    env = LegacyEnv()

    asyncio.run(start_simulator._reset_mobilegym_env(env, app_ids=[]))

    assert env.reset_calls == 1


def test_reset_mobilegym_env_does_not_retry_internal_type_error():
    class BrokenEnv:
        def __init__(self):
            self.reset_calls = 0

        async def reset(self, app_ids=None):
            self.reset_calls += 1
            raise TypeError("reset implementation failed")

    env = BrokenEnv()

    with pytest.raises(TypeError, match="reset implementation failed"):
        asyncio.run(start_simulator._reset_mobilegym_env(env, app_ids=[]))

    assert env.reset_calls == 1


def test_resilient_mobilegym_reset_supports_legacy_reset_signature():
    class LegacyMobileGymEnv:
        def __init__(self):
            self.reset_calls = 0

        async def reset(self):
            self.reset_calls += 1

    start_simulator.install_resilient_mobilegym_reset(LegacyMobileGymEnv)

    env = LegacyMobileGymEnv()
    asyncio.run(env.reset(app_ids=[]))

    assert env.reset_calls == 1


def test_resilient_mobilegym_reset_restarts_after_crashed_page():
    class FakeMobileGymEnv:
        def __init__(self):
            self.original_reset_calls = []
            self.close_calls = 0
            self.start_calls = 0

        async def reset(self, app_ids=None, timeout_ms=40000):
            self.original_reset_calls.append((app_ids, timeout_ms))
            if len(self.original_reset_calls) == 1:
                raise RuntimeError("Page.goto: Page crashed")

        async def close(self):
            self.close_calls += 1

        async def start(self):
            self.start_calls += 1
            return self

    start_simulator.install_resilient_mobilegym_reset(FakeMobileGymEnv)

    env = FakeMobileGymEnv()
    asyncio.run(env.reset(app_ids=[], timeout_ms=1234))

    assert env.original_reset_calls == [([], 1234), ([], 1234)]
    assert env.close_calls == 1
    assert env.start_calls == 1


def test_resilient_mobilegym_reset_does_not_restart_after_navigation_timeout():
    class FakeMobileGymEnv:
        def __init__(self):
            self.close_calls = 0
            self.start_calls = 0

        async def reset(self, app_ids=None, timeout_ms=40000):
            raise TimeoutError("Page.goto: Timeout 40000ms exceeded")

        async def close(self):
            self.close_calls += 1

        async def start(self):
            self.start_calls += 1
            return self

    start_simulator.install_resilient_mobilegym_reset(FakeMobileGymEnv)

    env = FakeMobileGymEnv()
    try:
        asyncio.run(env.reset(app_ids=[], timeout_ms=1234))
    except TimeoutError as exc:
        assert "Page.goto: Timeout" in str(exc)
    else:
        raise AssertionError("navigation timeout should propagate")

    assert env.close_calls == 0
    assert env.start_calls == 0


def test_resilient_mobilegym_reset_restarts_after_target_closed_error():
    class FakeMobileGymEnv:
        def __init__(self):
            self.reset_calls = 0
            self.restart_calls = 0

        async def reset(self, app_ids=None, timeout_ms=40000):
            self.reset_calls += 1
            if self.reset_calls == 1:
                raise RuntimeError(
                    "TargetClosedError: Page.goto: "
                    "Target page, context or browser has been closed"
                )

        async def restart(self):
            self.restart_calls += 1
            return self

    start_simulator.install_resilient_mobilegym_reset(FakeMobileGymEnv)

    env = FakeMobileGymEnv()
    asyncio.run(env.reset(app_ids=[], timeout_ms=1234))

    assert env.reset_calls == 2
    assert env.restart_calls == 1


def test_bridge_request_timeout_default_covers_mobilegym_reset_retries():
    args = start_simulator.build_parser().parse_args([])

    assert args.bridge_request_timeout_sec >= 180
