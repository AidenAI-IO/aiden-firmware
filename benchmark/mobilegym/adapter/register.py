from __future__ import annotations


def register_with_mobilegym() -> None:
    from bench_env.agent import register_agent
    from mobilegym.adapter.aiden_go_agent import AidenGoAgent

    register_agent("aiden_go", AidenGoAgent)
