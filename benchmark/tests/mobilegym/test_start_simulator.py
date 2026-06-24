import asyncio
import sys
import types

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
