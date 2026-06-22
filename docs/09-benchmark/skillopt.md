# SkillOpt Integration Solution

## Positioning

**Internal project development tool**, implementing skill text optimization capabilities based on Aiden's existing benchmark runner, following the algorithm from [Microsoft SkillOpt](https://github.com/microsoft/SkillOpt). Developers run it manually to produce optimized SKILL.md, which is **submitted back to the code repository after manual review**.

Not built into the agent, not triggered via conversation, does not affect end users.

## Relationship with Existing Benchmark

An extension of the same `benchmark/runner`:

- benchmark runner runs tests → produces pass/fail reports
- skillopt runner runs training → produces optimized SKILL.md

**Completely reuses existing benchmark capabilities**:

- `agent_client.py`: HTTP calls to agent
- `suite.py`: Load task sets
- `reset.py`: Task isolation (global_reset + per_task_setup)
- `runtask.py`: Execute individual tasks
- `judge.py`: LLM scoring
- `assertions.py`: Hard assertions

## Developer Usage Flow

```bash
# 1. Start agent (development board, dedicated for optimization)
cd src/agent
./agent --config config/agent.toml

# 2. Run optimization (another terminal)
cd benchmark
python -m runner.skillopt \
    --skill device-operator \
    --backend device \
    --train-suite skillopt/device-operator/device_operator_train \
    --validation-suite skillopt/device-operator/device_operator_verification \
    --budget 10 \
    --output /tmp/device-operator-optimized.md

# 3. View diff
diff src/agent/config/skills/device-operator/SKILL.md /tmp/device-operator-optimized.md

# 4. If satisfied, submit
cp /tmp/device-operator-optimized.md src/agent/config/skills/device-operator/SKILL.md
git add src/agent/config/skills/device-operator/SKILL.md
git commit -m "skillopt: improve device-operator (phone_control_v1, 65% → 78%)"
```

## Core Algorithm (Vendored from Official)

Reference main branch commits from the `microsoft/SkillOpt` repository. Core logic that needs to be ported to this project:

```text
skillopt/optimizer/skill.py        → benchmark/runner/skillopt/patch.py
  apply_edit / apply_patch_with_report
  Support op: append / insert_after / replace / delete

skillopt/gradient/reflect.py       → benchmark/runner/skillopt/reflect.py
  run_error_analyst_minibatch
  run_success_analyst_minibatch
  fmt_minibatch_trajectories

skillopt/evaluation/gate.py        → benchmark/runner/skillopt/score.py
  validation gate (accept candidate only if score > current score + min_delta)

skillopt/prompts/analyst_error.md     → benchmark/runner/skillopt/prompts/analyst_error.md
skillopt/prompts/analyst_success.md   → benchmark/runner/skillopt/prompts/analyst_success.md
```

Version 1 **does not implement** slow update / meta skill / highly parallel analyst workers.

## Data Structures (Aligned with Official)

```python
# Edit operation
@dataclass
class Edit:
    op: Literal["append", "insert_after", "replace", "delete"]
    content: str = ""           # For append/insert_after/replace
    target: str = ""            # For insert_after/replace/delete
    source_type: Literal["failure", "success"] | None = None
    support_count: int | None = None

# A group of edits
@dataclass
class Patch:
    edits: list[Edit]
    reasoning: str = ""

# Reflect output
@dataclass
class RawPatch:
    patch: Patch
    source_type: Literal["failure", "success"]
    batch_size: int
    failure_summary: list[FailureSummaryEntry]
```

## Skill Override Mechanism

**Temporary file replacement** (for development environment, simplest approach):

```python
def with_candidate_skill(skill_path: Path, candidate: str):
    """Context manager: temporarily replace SKILL.md, restore after rollout"""
    original = skill_path.read_text()
    skill_path.write_text(candidate)
    try:
        # Notify agent to reload skills (if this endpoint exists)
        # Or chat directly (if agent reloads on every Run)
        yield
    finally:
        skill_path.write_text(original)
```

**Note**: Aiden's `reloadSkillsIfDirty` mechanism requires triggering `MarkSkillsDirty`. Simple approach: call `/api/clear` after writing file (also clears history), next chat will auto-reload.

No need to add `skill_overrides` field on Go side.

## Backend Selection

SkillOpt has one shared core: optimization loop, scoring/gate, edit application, `best_skill.md`, and reports are shared. The only difference is the rollout backend:

- `--backend device`: use the current Aiden daemon and a real device to run the suite.
- `--backend mobilegym`: use the MobileGym Docker runner to execute the same Aiden suite tasks.

The MobileGym backend does not directly trust raw `passed` / `success` values. It reads Aiden response, chat history, and task metadata from MobileGym artifacts, then runs Aiden-native `TaskResult` evaluation logic (hard assertions, expected answer/memory, trace observations, judge) before converting results to SkillOpt rollout scores. This keeps MobileGym and real-device backends semantically aligned when they enter the SkillOpt core.

## Dataset

SkillOpt suites are organized by skill, with a default train and held-out verification set:

- `benchmark/suites/skillopt/device-operator/device_operator_train.json` — For optimizing device-operator
- `benchmark/suites/skillopt/device-operator/device_operator_verification.json` — device-operator held-out verification
- `benchmark/suites/memory_v1.json` — For optimizing memory-related skills
- Other suites as needed

Each suite is internally split into train / selection within SkillOpt:

```python
# Simple split strategy
train_tasks = suite.tasks[:int(len(suite.tasks) * 0.7)]
selection_tasks = suite.tasks[int(len(suite.tasks) * 0.7):]
```

Or explicitly specify in CLI:

```bash
python -m runner.skillopt \
    --skill device-operator \
    --backend device \
    --train-suite skillopt/device-operator/device_operator_train \
    --validation-suite skillopt/device-operator/device_operator_verification \
    ...
```

In the benchmark UI, selecting `SkillOpt` loads `/benchmark/skillopt-targets`. Choosing a skill auto-populates the matching train and verification suites from `benchmark/suites/skillopt/<skill>/`; then choose the backend (`Real device` or `MobileGym`) and start the run.

## Task Isolation

**Completely reuses existing benchmark mechanism**:

```python
# Before each task
client.clear_history()              # Clear agent history (may also clear memory)
global_reset(client, suite)         # home + back + wait
per_task_setup(client, task.setup)  # Task prerequisite state
```

Exactly the same as benchmark runner.

**No need to protect user data**: development board is for testing, memory being cleared doesn't matter.

## Optimization Loop

```python
def optimize_skill(skill_name, skill_content, suite, budget):
    current = skill_content
    current_score = baseline_eval(current, suite.selection_tasks)
    best = current
    best_score = current_score

    rejected_edits = []

    for step in range(budget):
        # 1. Train rollout (run train tasks with current skill)
        rollouts = run_train_rollouts(current, suite.train_tasks)

        # 2. Group minibatch by hard
        failures = [r for r in rollouts if r.hard == 0]
        successes = [r for r in rollouts if r.hard == 1]

        # 3. Reflect (call LLM)
        failure_patches = run_error_analyst_minibatch(current, failures)
        success_patches = run_success_analyst_minibatch(current, successes)

        # 4. Aggregate + Rank
        all_edits = aggregate(failure_patches, success_patches, rejected_edits)
        selected = rank_and_clip(all_edits, edit_budget=4)

        # 5. Apply edits
        candidate = apply_patch(current, Patch(edits=selected))

        # 6. Selection rollout (run selection tasks with candidate)
        candidate_score = eval_score(candidate, suite.selection_tasks)

        # 7. Gate
        if candidate_score > current_score + MIN_DELTA:
            current = candidate
            current_score = candidate_score
            if current_score > best_score:
                best = current
                best_score = current_score
        else:
            rejected_edits.extend(selected)

        # 8. Early stop
        if no_improvement_for(3):
            break

    return OptimizationResult(
        initial_score=baseline_score,
        best_score=best_score,
        best_skill=best,
    )
```

## Module Organization

```text
benchmark/runner/skillopt/
├── __init__.py
├── main.py              # CLI entry point
├── orchestrator.py      # Optimization main loop
├── reflect.py           # Call LLM to generate edits (vendored)
├── patch.py             # Apply edits (vendored)
├── score.py             # Gate logic (vendored)
├── aggregate.py         # Merge and deduplicate multiple patches
├── types.py             # Edit / Patch / RolloutResult dataclass
├── prompts/
│   ├── analyst_error.md
│   └── analyst_success.md
└── tests/
    ├── test_patch.py
    ├── test_reflect.py    # Using mock LLM
    └── test_orchestrator.py
```

## Output

```text
runs/skillopt/<run_id>/
├── manifest.json                 # Run metadata (skill, suite, budget, model)
├── result.json                   # Optimization results (initial_score, best_score, accepted/rejected)
├── best_skill.md                 # Final best skill
├── diff.patch                    # Diff from original skill
├── rollouts/
│   ├── step_001/
│   │   ├── train/                # train rollout results
│   │   ├── selection/            # selection rollout results
│   │   ├── candidate.md
│   │   ├── patch.json            # edits generated in this step
│   │   ├── decision.json         # accepted / rejected + reasoning
│   │   └── ...
│   └── ...
└── report.html                   # Visual report (optional)
```

## CLI

```bash
python -m runner.skillopt \
    --skill <skill-name> \
    [--backend device|mobilegym] \
    [--mobilegym-parallel 1] \
    [--suite <suite-name> | --train-suite <suite-name> --validation-suite <suite-name>] \
    [--budget 10] \
    [--edit-budget 4] \
    [--min-delta 0.05] \
    [--optimizer-model gpt-4o] \
    [--judge-model claude-opus-4-7] \
    [--agent-url http://localhost:8080] \
    [--output <path>] \
    [--dry-run]              # Don't actually write files, only output diff
```

MobileGym example:

```bash
python -m runner.skillopt \
    --skill device-operator \
    --backend mobilegym \
    --mobilegym-parallel 4 \
    --train-suite skillopt/device-operator/device_operator_train \
    --validation-suite skillopt/device-operator/device_operator_verification \
    --budget 10
```

## Effort Estimation

| Task                                                          | Effort    |
| ------------------------------------------------------------- | --------- |
| Vendor official patch.py                                      | 0.5 days  |
| Vendor official reflect.py + prompts                          | 1 day     |
| Vendor official gate.py                                       | 0.3 days  |
| Implement aggregate.py (merge + rank)                         | 0.5 days  |
| Implement orchestrator.py (main loop + early stop)            | 1 day     |
| CLI main.py                                                   | 0.3 days  |
| Integration with benchmark runner (reuse agent_client, suite, runtask) | 0.5 days |
| Unit tests (patch, aggregate, score)                          | 0.5 days  |
| Real device end-to-end smoke test + tuning                    | 1 day     |
| **Total**                                                     | **~5 days** |

## Things Not To Do (Phase 1)

- ❌ Slow update / meta skill (official epoch-level mechanism)
- ❌ Auto-generate dataset (use existing suites)
- ❌ Built-in agent feature / conversation trigger
- ❌ End-user-facing optimization mode
- ❌ Auto-submit PR (submit manually after review)
- ❌ Go-side `skill_overrides` HTTP field
- ❌ History/Memory isolation mechanism
- ❌ Envelope hash verification (not needed in dev environment)

## References

- Microsoft SkillOpt: https://github.com/microsoft/SkillOpt
- Benchmark Architecture: [architecture.md](./architecture.md)
- Benchmark Quick Start: [quickstart.md](./quickstart.md)
