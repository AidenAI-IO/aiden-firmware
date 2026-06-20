# Skills and RoleProfile Mechanism

The Agent automatically discovers `SKILL.md` files from the config directory, displays an Available skills catalog in the system prompt, and loads the complete instructions of relevant skills on demand via `skill_read`. Activated skills enter the multi-role `RoleProfile`: `planner`, `executor`, `verifier`.

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
- Inject skill instructions into the system prompt of the three roles' `RoleProfile`;
- Support `allowed_tools` to restrict ordinary task tools; `skill_list` / `skill_read` / `skill_manage` / `skill_mark_used` are retained by default as skill meta-tools;
- Provide `skill_list` / `skill_read` / `skill_manage` / `skill_mark_used`;
- `skill_read` supports reading `SKILL.md` as well as UTF-8 supporting files under `references/`, `templates/`, `scripts/`, `assets/`;
- Record view/use/modify statistics in `usage.json`;
- Support `active` / `stale` / `archived` lifecycle states; `skill_list` filters out archived by default;
- For skills with `source: agent` or `created_by: agent`, `skill_list` automatically executes lifecycle based on last use time: enter `stale` after 90 days of non-use, enter `archived` after 180 days; this automatic scan runs at most once every 24 hours;
- `skill_mark_used` automatically restores `stale` / `archived` skills to `active`.

## Role Permissions

The Agent loop uses a `default` / `plan` / `execution` three-phase state machine. See [Agent Context Lifecycle](context-lifecycle.md) for details.

- `planner`:
  - In `default` mode, only handles simple tasks (direct answer, single tool call, or at most two steps); when **3 or more steps** are expected, must first `enter_plan_mode`, and must not continue executing directly in default;
  - In `plan` mode, can explore and maintain draft plan, and switch phases via `commit_plan` / `cancel_plan`;
  - Is the only role allowed to create or modify plans;
  - Additionally sees loop meta tools: `use_simple_mode`, `enter_plan_mode`, `commit_plan`, `cancel_plan`.
- `executor`: Runs only in `execution` phase; can only execute the `next_step` already committed by planner, enters verifier review via `finish_step` / `abort_step`, cannot modify plan or decide to end.
- `verifier`: runs only in `execution`; its system prompt contains the current date, `## Role rules`, and optional `## Verifier memory cautions`; each turn verifies only the current executor `step_text`; intermediate step success advances to the next step, and only final-step success may set `can_finish`; step failure sets `needs_replan`; it does not receive the tool catalog, skills, base instruction, runtime context, or global memory.

Both `planner` and `executor` receive callable function tools. `verifier` does not call tools, only performs review based on executor evidence.

## Parsed but Not Fully Enforced Fields

- `preferred_model`
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
- `allowed_tools` should be narrowed as much as possible to avoid activating unrelated tools;
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
