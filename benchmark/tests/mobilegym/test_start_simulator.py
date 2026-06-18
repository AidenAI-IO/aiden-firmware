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
