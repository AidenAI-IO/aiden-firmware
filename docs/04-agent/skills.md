# Skills and RoleProfile Mechanism

The Agent automatically discovers `SKILL.md` files from the config directory, displays an Available skills catalog in the system prompt, and loads the complete instructions of relevant skills on demand via `skill_read`. Activated skills are injected into the Agent's `RoleProfile`, which contains the system prompt, skill instructions, and available tools.

## Directory Structure

```text
/userdata/agent/
└── skills/
    └── my-skill/
        └── SKILL.md
```

Scan rules:

```text
configDir/skills/**/SKILL.md
```

## Example

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

## Currently Implemented Capabilities

- Automatically discover `SKILL.md`;
- Display Available skills in the prompt and load complete `SKILL.md` at runtime via `skill_read`;
- Inject skill instructions into the Agent's system prompt via `RoleProfile`;
- Parse `allowed_tools` metadata for validation and future compatibility, but tool availability is currently controlled by the static Agent tool catalog rather than by active skills;
- Provide `skill_list` / `skill_read` / `skill_manage` / `skill_mark_used` to the Agent; the HTTP Tool API exposes only the non-maintenance ones (`skill_list` / `skill_read`);
- `skill_read` supports reading `SKILL.md` as well as UTF-8 supporting files under `references/`, `templates/`, `scripts/`, `assets/`;
- Record view/use/modify statistics in `usage.json`;
- Support `active` / `stale` / `archived` lifecycle states; `skill_list` filters out archived by default;
- For skills with `source: agent` or `created_by: agent`, `skill_list` automatically executes lifecycle based on last use time: enter `stale` after 90 days of non-use, enter `archived` after 180 days; this automatic scan runs at most once every 24 hours;
- `skill_mark_used` automatically restores `stale` / `archived` skills to `active`.

## Agent Execution

The Agent uses a streamlined execution loop that handles tool calls and generates responses in a single unified flow. All tools are available to the Agent throughout execution, and the Agent directly manages its own task breakdown and execution strategy.

Tool execution is integrated into the main loop with callback handlers for streaming responses and episode recording.

## Parsed but Not Fully Enforced Fields

- `preferred_model`
- `allowed_tools`
- `allowed_children`

These fields can serve as metadata for future extensions but should not be relied upon for current runtime enforcement.

## HTTP Tool Skill Export

The Agent provides:

```text
GET /api/tool-skills
```

This endpoint generates a skill bundle describing the Aiden HTTP Tool API, making it convenient for external Agents like Codex to call device tools in a unified manner.

## Usage Recommendations

- Skill content should describe high-level strategies; do not hardcode volatile coordinates;
- For UI operation skills, recommend requiring screenshot before clicking;
- `allowed_tools` should be kept narrow for documentation and forward compatibility, but current runtime tool availability does not expand based on active skills;
- `allowed_tools` can only reference currently registered tools or `delegate_<child>` form child Agent delegation pseudo-tools;
- Do not write one-time task progress, temporary state, secrets, raw logs, or personal facts into skills; these do not belong to reusable procedures.

## Future Design: Bundled Sync + LLM Merge

The latest design adopts a single runtime source:

```text
Read-only bundled skills
  ↓ seed / sync / merge
configDir/skills as effective skill source of truth
  ↓
Runtime only loads configDir/skills
```

Core rules:

- Bundled skills are released with firmware / code, but runtime does not directly scan the bundled directory;
- On startup or update, sync bundled skills to `configDir/skills`;
- `.bundled_manifest.json` records `origin_hash`, `effective_hash`, `base_path`, and recent merge results;
- `origin_hash` represents the bundled baseline corresponding to the last sync / merge;
- `effective_hash` represents the effective copy hash after the syncer last wrote to the user directory, used to stably identify `merged` status;
- When local effective copy differs from bundled baseline and bundled has also updated, background serial LLM generates merge candidate based on base / upstream / local;
- LLM result is first written to a temp file; only overwritten to `configDir/skills/<name>/SKILL.md` after validation passes and user file has not been asynchronously modified;
- All merge failures, validation failures, or application failures record `last_failed_merge_key` to avoid repeated retry of the same input set;
- MVP does not support restoring historical user versions before overwrite; temp files only prevent failed candidates from polluting user directory, not a rollback mechanism.

For detailed design, see:

```text
docs/04-agent/skills-merge-design.md
```
