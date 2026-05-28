# Skills 机制

Agent 支持从配置目录中自动发现 `SKILL.md`，并在运行时通过 `activate_skill` 工具激活。

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
- 通过 `activate_skill` 运行时激活；
- 将 skill instructions 注入 system prompt；
- 支持 `allowed_tools` 限制可用工具；
- 提供 `skill_list` / `skill_read` / `skill_manage` / `skill_mark_used`；
- 记录 `usage.json` 中的 view/use/modify 统计；
- 支持 `active` / `stale` / `archived` lifecycle 状态，`skill_list` 默认过滤 archived；
- 对 `source: agent` 或 `created_by: agent` 的 skill，`skill_list` 会按最近使用时间自动执行 lifecycle：90 天未使用进入 `stale`，180 天未使用进入 `archived`；该自动扫描最多 24 小时运行一次；
- `skill_mark_used` 会把 `stale` / `archived` skill 自动恢复为 `active`。

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
- `allowed_tools` 只能引用当前已注册工具，或 `delegate_<child>` 形式的子 Agent 委派 pseudo-tool。

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
