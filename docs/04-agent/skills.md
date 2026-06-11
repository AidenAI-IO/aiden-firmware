# Skills 和 RoleProfile 机制

Agent 支持从配置目录中自动发现 `SKILL.md`，在 system prompt 中展示 Available skills catalog，并通过 `skill_read` 按需加载相关 skill 的完整说明。激活后的 skill 进入多角色 `RoleProfile`：`planner`、`executor`、`verifier`。

## 目录

```text
/userdata/agent/
└── skills/
    └── my-skill/
        └── SKILL.md
```

扫描规则：

```text
configDir/skills/**/SKILL.md
```

## 示例

```markdown
---
name: ui-operator
description: Prefer screenshot-driven UI inspection before clicking.
metadata:
  allowed_tools: [screenshot, mouse_click, mouse_move, keyboard_text]
---

Take a screenshot before interacting with an unfamiliar UI.
Prefer describing what you see before clicking.
```

## 当前已实现能力

- 自动发现 `SKILL.md`；
- 在 prompt 中展示 Available skills，并通过 `skill_read` 运行时加载完整 `SKILL.md`；
- 将 skill instructions 注入三个角色的 `RoleProfile` 的 system prompt；
- 支持 `allowed_tools` 限制普通任务工具；`skill_list` / `skill_read` / `skill_manage` / `skill_mark_used` 作为 skill meta-tools 默认保留；
- 提供 `skill_list` / `skill_read` / `skill_manage` / `skill_mark_used`；
- `skill_read` 支持读取 `SKILL.md`，以及 `references/`、`templates/`、`scripts/`、`assets/` 下的 UTF-8 supporting files；
- 记录 `usage.json` 中的 view/use/modify 统计；
- 支持 `active` / `stale` / `archived` lifecycle 状态，`skill_list` 默认过滤 archived；
- 对 `source: agent` 或 `created_by: agent` 的 skill，`skill_list` 会按最近使用时间自动执行 lifecycle：90 天未使用进入 `stale`，180 天未使用进入 `archived`；该自动扫描最多 24 小时运行一次；
- `skill_mark_used` 会把 `stale` / `archived` skill 自动恢复为 `active`。

## 角色权限

Agent loop 采用 `default` / `plan` / `execution` 三阶段状态机，详见 [Agent Context Lifecycle](context-lifecycle.md)。

- `planner`：
  - 在 `default` 模式下仅处理简单任务（直接回答、单次工具调用或最多两步）；预计需要 **3 步及以上** 时必须先 `enter_plan_mode`，不得继续在 default 中直执；
  - 在 `plan` 模式下可探索、维护 draft plan，并通过 `commit_plan` / `cancel_plan` 切换阶段；
  - 是唯一允许创建或修改计划的角色；
  - 额外可见 loop meta tools：`enter_plan_mode`、`commit_plan`、`cancel_plan`。
- `executor`：仅在 `execution` 阶段运行；只能执行 planner 已提交的 `next_step`，最多发起一个工具调用，不能修改计划或决定结束。
- `verifier`：仅在 `execution` 阶段运行；是唯一允许结束已提交计划执行的角色；最终回复必须由它确认；它会在最终判定前重新看到原始任务和完成条件；system prompt 中不再包含工具目录。

`planner` 和 `executor` 都会收到可调用的 function tools。`verifier` 不调用工具，只基于 executor 证据做复核。

## 已解析但未完全执行的字段

- `preferred_model`
- `allowed_children`

这些字段可以作为未来扩展的元数据，但不要依赖当前 runtime 强制执行。

## HTTP Tool Skill Export

Agent 提供：

```text
GET /api/tool-skills
```

该端点会生成一个描述 Aiden HTTP Tool API 的 skill bundle，便于 Codex 等外部 Agent 按统一方式调用设备工具。

## 使用建议

- skill 内容应描述高层策略，不要硬编码易变坐标；
- 对 UI 操作类 skill，建议要求先截图再点击；
- `allowed_tools` 应尽量收敛，避免无关工具被激活；
- `allowed_tools` 只能引用当前已注册工具，或 `delegate_<child>` 形式的子 Agent 委派 pseudo-tool；
- 不要把一次性任务进度、临时状态、秘密、原始日志或个人事实写进 skill；这些不属于可复用流程。

## 后续设计：内置同步 + LLM 合并

最新方案采用单一运行时来源：

```text
内置只读 skills
  ↓ seed / sync / merge
configDir/skills 作为 effective skill 真源
  ↓
运行时只加载 configDir/skills
```

核心规则：

- 内置 skills 随固件 / 代码发布，但运行时不直接扫描内置目录；
- 启动或更新时，把内置 skills 同步到 `configDir/skills`；
- `.bundled_manifest.json` 记录 `origin_hash`、`effective_hash`、`base_path` 和最近合并结果；
- `origin_hash` 表示上次同步 / 合并对应的 bundled baseline；
- `effective_hash` 表示同步器上次写入用户目录后的 effective copy hash，用于稳定识别 `merged` 状态；
- 本地 effective copy 与 bundled baseline 有差异且 bundled 也更新时，后台串行 LLM 根据 base / upstream / local 生成合并候选；
- LLM 结果先写临时文件，校验通过且用户文件未被异步改动时才覆盖 `configDir/skills/<name>/SKILL.md`；
- 所有合并失败、校验失败或应用失败都记录 `last_failed_merge_key`，避免同一组输入反复重试；
- MVP 不支持恢复覆盖前的历史用户版本；临时文件只用于防止失败候选污染用户目录，不是回滚机制。

详细方案见：

```text
docs/04-agent/skills-merge-design.md
```
