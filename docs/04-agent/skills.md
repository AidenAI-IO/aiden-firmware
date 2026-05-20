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
- 支持 `allowed_tools` 限制可用工具。

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
- `src/agent/config/skills` 下的示例可能引用旧工具，生产使用前需核对当前工具清单。
