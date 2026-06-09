"""SkillOpt: text-space skill optimizer for Aiden.

Internal developer tool. Runs against a real device through the existing
benchmark/runner HTTP client, reflects on rollouts using a separate
optimizer LLM, and produces a candidate SKILL.md that beats the current
one on a held-out selection split.

Algorithm vendored from microsoft/SkillOpt with the following adaptations:
  - Rollout uses benchmark/runner/runtask.run_one_task (HTTP).
  - Skill swap is a temporary file replacement under
    src/agent/config/skills/<name>/SKILL.md plus a reload signal to the
    agent via POST /api/skills/reload.
  - We do not implement slow_update / meta_skill in the first version.
"""
