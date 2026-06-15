# SkillOpt 接入方案

## 定位

**项目内部开发工具**，参照 [Microsoft SkillOpt](https://github.com/microsoft/SkillOpt) 的算法，基于 Aiden 现有 benchmark runner 实现 skill 文本优化能力。开发者手动跑，产出优化后的 SKILL.md，**经人工 review 后提交回代码仓库**。

不在 Agent 内置，不通过对话触发，不影响最终用户。

## 与现有 benchmark 的关系

跟 `benchmark/runner` 是同一套东西的延伸：

- benchmark runner 跑测试 → 产出 pass/fail 报告
- skillopt runner 跑训练 → 产出优化后的 SKILL.md

**完全复用 benchmark 已有的能力**：

- `agent_client.py`：HTTP 调用 agent
- `suite.py`：加载任务集
- `reset.py`：任务隔离（global_reset + per_task_setup）
- `runtask.py`：执行单个任务
- `judge.py`：LLM 评分
- `assertions.py`：硬断言

## 开发者使用流程

```bash
# 1. 启动 agent（开发板，专门用于优化）
cd src/agent
./agent --config config/agent.toml

# 2. 跑优化（另一个终端）
cd benchmark
python -m runner.skillopt \
    --skill device-operator \
    --backend device \
    --train-suite skillopt/device-operator/device_operator_train \
    --validation-suite skillopt/device-operator/device_operator_verification \
    --budget 10 \
    --output /tmp/device-operator-optimized.md

# 3. 查看 diff
diff src/agent/config/skills/device-operator/SKILL.md /tmp/device-operator-optimized.md

# 4. 满意则提交
cp /tmp/device-operator-optimized.md src/agent/config/skills/device-operator/SKILL.md
git add src/agent/config/skills/device-operator/SKILL.md
git commit -m "skillopt: improve device-operator (phone_control_v1, 65% → 78%)"
```

## 核心算法（vendor 自官方）

参考 `microsoft/SkillOpt` 仓库主干 commit。需要 port 到本工程的核心逻辑：

```text
skillopt/optimizer/skill.py        → benchmark/runner/skillopt/patch.py
  apply_edit / apply_patch_with_report
  支持 op: append / insert_after / replace / delete

skillopt/gradient/reflect.py       → benchmark/runner/skillopt/reflect.py
  run_error_analyst_minibatch
  run_success_analyst_minibatch
  fmt_minibatch_trajectories

skillopt/evaluation/gate.py        → benchmark/runner/skillopt/score.py
  validation gate（候选分数 > 当前分数 + min_delta 才接受）

skillopt/prompts/analyst_error.md     → benchmark/runner/skillopt/prompts/analyst_error.md
skillopt/prompts/analyst_success.md   → benchmark/runner/skillopt/prompts/analyst_success.md
```

第一版**不实现** slow update / meta skill / 高度并行的 analyst worker。

## 数据结构（对齐官方）

```python
# Edit 操作
@dataclass
class Edit:
    op: Literal["append", "insert_after", "replace", "delete"]
    content: str = ""           # append/insert_after/replace 用
    target: str = ""            # insert_after/replace/delete 用
    source_type: Literal["failure", "success"] | None = None
    support_count: int | None = None

# 一组 edits
@dataclass
class Patch:
    edits: list[Edit]
    reasoning: str = ""

# Reflect 输出
@dataclass
class RawPatch:
    patch: Patch
    source_type: Literal["failure", "success"]
    batch_size: int
    failure_summary: list[FailureSummaryEntry]
```

## Skill Override 机制

**临时文件替换**（开发环境用，最简单）：

```python
def with_candidate_skill(skill_path: Path, candidate: str):
    """Context manager: 临时替换 SKILL.md，rollout 完恢复"""
    original = skill_path.read_text()
    skill_path.write_text(candidate)
    try:
        # 通知 agent reload skills（如果有这个 endpoint）
        # 或直接 chat（如果 agent 每次 Run 都会 reload）
        yield
    finally:
        skill_path.write_text(original)
```

**注意**：Aiden 的 `reloadSkillsIfDirty` 机制需要触发 `MarkSkillsDirty`。简单做法是写完文件后调 `/api/clear`（顺便清掉 history），下次 chat 会自动 reload。

不需要在 Go 侧加 `skill_overrides` 字段。

## Backend 选择

SkillOpt core 只有一套：优化循环、打分/gate、edit 应用、`best_skill.md`、报告都共享。差异只在 rollout backend：

- `--backend device`：使用当前 Aiden daemon 和真实设备执行 suite。
- `--backend mobilegym`：用 MobileGym Docker runner 执行同一批 Aiden suite task。

MobileGym backend 不直接信任 raw `passed` / `success`。它先读取 MobileGym 产物里的 Aiden response、chat history、task metadata，再调用 Aiden-native `TaskResult` 评估逻辑（hard assertions、expected answer/memory、trace observations、judge）后转换为 SkillOpt rollout score。这样 MobileGym 和真机 backend 进入 SkillOpt core 的语义一致。

## 数据集

SkillOpt suite 按 skill 分目录，默认一组 train + held-out verification：

- `benchmark/suites/skillopt/device-operator/device_operator_train.json` — 用于优化 device-operator
- `benchmark/suites/skillopt/device-operator/device_operator_verification.json` — device-operator held-out verification
- `benchmark/suites/memory_v1.json` — 用于优化 memory 相关 skill
- 其他 suite 按需

每个 suite 在 SkillOpt 内部按需 split 成 train / selection：

```python
# 简单的 split 策略
train_tasks = suite.tasks[:int(len(suite.tasks) * 0.7)]
selection_tasks = suite.tasks[int(len(suite.tasks) * 0.7):]
```

或在 CLI 里显式指定：

```bash
python -m runner.skillopt \
    --skill device-operator \
    --backend device \
    --train-suite skillopt/device-operator/device_operator_train \
    --validation-suite skillopt/device-operator/device_operator_verification \
    ...
```

UI 上选择 `SkillOpt` 后，会从 `/benchmark/skillopt-targets` 自动加载 `benchmark/suites/skillopt/<skill>/` 下的 train / verification suite。选择 skill 后，Train suite 和 Verification suite 会自动切到同一 skill 目录下的默认项；再选择 backend（Real device 或 MobileGym）即可启动。

## 任务隔离

**完全复用 benchmark 现有机制**：

```python
# 每个 task 之前
client.clear_history()              # 清 agent history（可能也清 memory）
global_reset(client, suite)         # home + back + wait
per_task_setup(client, task.setup)  # 任务前置状态
```

跟 benchmark runner 一模一样。

**不需要保护用户数据**：开发板就是用来跑测试的，memory 被清也无所谓。

## 优化循环

```python
def optimize_skill(skill_name, skill_content, suite, budget):
    current = skill_content
    current_score = baseline_eval(current, suite.selection_tasks)
    best = current
    best_score = current_score

    rejected_edits = []

    for step in range(budget):
        # 1. Train rollout（用 current skill 跑 train tasks）
        rollouts = run_train_rollouts(current, suite.train_tasks)

        # 2. 按 hard 分组 minibatch
        failures = [r for r in rollouts if r.hard == 0]
        successes = [r for r in rollouts if r.hard == 1]

        # 3. Reflect（调 LLM）
        failure_patches = run_error_analyst_minibatch(current, failures)
        success_patches = run_success_analyst_minibatch(current, successes)

        # 4. Aggregate + Rank
        all_edits = aggregate(failure_patches, success_patches, rejected_edits)
        selected = rank_and_clip(all_edits, edit_budget=4)

        # 5. Apply edits
        candidate = apply_patch(current, Patch(edits=selected))

        # 6. Selection rollout（用 candidate 跑 selection tasks）
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

## 模块组织

```text
benchmark/runner/skillopt/
├── __init__.py
├── main.py              # CLI 入口
├── orchestrator.py      # 优化主循环
├── reflect.py           # 调 LLM 生成 edits（vendor）
├── patch.py             # Apply edits（vendor）
├── score.py             # Gate 逻辑（vendor）
├── aggregate.py         # 合并去重多个 patch
├── types.py             # Edit / Patch / RolloutResult dataclass
├── prompts/
│   ├── analyst_error.md
│   └── analyst_success.md
└── tests/
    ├── test_patch.py
    ├── test_reflect.py    # 用 mock LLM
    └── test_orchestrator.py
```

## 产出

```text
runs/skillopt-<run_id>/
├── manifest.json                 # 运行元信息（skill, suite, budget, model）
├── result.json                   # 优化结果（initial_score, best_score, accepted/rejected）
├── best_skill.md                 # 最终最佳 skill
├── diff.patch                    # 与原 skill 的 diff
├── rollouts/
│   ├── step_001/
│   │   ├── train/                # train rollout 结果
│   │   ├── selection/            # selection rollout 结果
│   │   ├── candidate.md
│   │   ├── patch.json            # 这一步生成的 edits
│   │   ├── decision.json         # accepted / rejected + 原因
│   │   └── ...
│   └── ...
└── report.html                   # 可视化报告（optional）
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
    [--dry-run]              # 不真正写文件，只输出 diff
```

MobileGym 示例：

```bash
python -m runner.skillopt \
    --skill device-operator \
    --backend mobilegym \
    --mobilegym-parallel 4 \
    --train-suite skillopt/device-operator/device_operator_train \
    --validation-suite skillopt/device-operator/device_operator_verification \
    --budget 10
```

## 工作量

| 任务                                                          | 工作量    |
| ------------------------------------------------------------- | --------- |
| Vendor 官方 patch.py                                          | 0.5 天    |
| Vendor 官方 reflect.py + prompts                              | 1 天      |
| Vendor 官方 gate.py                                           | 0.3 天    |
| 实现 aggregate.py（merge + rank）                             | 0.5 天    |
| 实现 orchestrator.py（主循环 + early stop）                   | 1 天      |
| CLI main.py                                                   | 0.3 天    |
| 与 benchmark runner 集成（复用 agent_client、suite、runtask） | 0.5 天    |
| 单元测试（patch、aggregate、score）                           | 0.5 天    |
| 真机端到端 smoke 测试 + 调优                                  | 1 天      |
| **总计**                                                      | **~5 天** |

## 不做的事情（Phase 1）

- ❌ Slow update / meta skill（官方 epoch-level 机制）
- ❌ 自动生成 dataset（用现有 suite）
- ❌ Agent 内置功能 / 对话触发
- ❌ 用户可见的优化模式
- ❌ 自动提交 PR（手动 review 后提交）
- ❌ Go 侧 `skill_overrides` HTTP 字段
- ❌ History/Memory 隔离机制
- ❌ Envelope hash 校验（开发环境不需要）

## 参考

- Microsoft SkillOpt: https://github.com/microsoft/SkillOpt
- Benchmark 架构: [architecture.md](./architecture.md)
- Benchmark 快速开始: [quickstart.md](./quickstart.md)
