import asyncio

import pytest

from mobilegym.bridge.episode import BridgeEpisodeState, StaleEpisodeError


class FakeEnv:
    def __init__(self):
        self.active = 0
        self.max_active = 0
        self.calls = []

    async def serialized_call(self, name):
        self.calls.append(("start", name))
        self.active += 1
        self.max_active = max(self.max_active, self.active)
        await asyncio.sleep(0.01)
        self.active -= 1
        self.calls.append(("end", name))
        return name


def test_episode_state_tracks_active_episode_and_rejects_stale_ids():
    async def scenario():
        state = BridgeEpisodeState(FakeEnv(), owner_loop=asyncio.get_running_loop())

        await state.start_episode("ep1")
        assert state.active_episode_id == "ep1"
        assert state.require_active("ep1") == "ep1"

        with pytest.raises(StaleEpisodeError):
            state.require_active("old")

        await state.end_episode("ep1")
        assert state.active_episode_id is None
        with pytest.raises(StaleEpisodeError):
            state.require_active("ep1")

    asyncio.run(scenario())


def test_episode_state_serializes_env_work_with_one_lock():
    async def scenario():
        env = FakeEnv()
        state = BridgeEpisodeState(env, owner_loop=asyncio.get_running_loop())

        results = await asyncio.gather(
            state.run_env(lambda fake: fake.serialized_call("first")),
            state.run_env(lambda fake: fake.serialized_call("second")),
        )

        assert sorted(results) == ["first", "second"]
        assert env.max_active == 1

    asyncio.run(scenario())


def test_episode_state_logs_actions_with_episode_scoped_ids():
    async def scenario():
        state = BridgeEpisodeState(FakeEnv(), owner_loop=asyncio.get_running_loop())
        await state.start_episode("ep1")

        entry = state.log_action(
            tool_name="tap",
            tool_input={"x": 0.5, "y": 0.25},
            mobilegym_action={"action_type": "CLICK"},
            duration_ms=12,
            error=None,
        )

        assert entry["episode_id"] == "ep1"
        assert entry["action_id"] == "ep1:0001"
        assert entry["tool_name"] == "tap"
        assert state.action_log == [entry]

    asyncio.run(scenario())
