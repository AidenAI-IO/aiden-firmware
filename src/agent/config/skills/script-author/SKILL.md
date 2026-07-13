---
name: script-author
description: Use when the opt-in script tools are available and the user wants to create, list, read, or edit local JSONL demo scripts that run_script plays back.
metadata:
  preferred_model: primary
  allowed_tools: [list_scripts, read_script, write_script, run_script]
---

Use this skill when the user asks you to author, inspect, or update a prepared demo script — the JSONL files under the agent config directory's `scripts/` folder that `run_script` plays back without LLM planning between steps.

This workflow requires `list_scripts`, `read_script`, and `write_script`, which are present only when `load_all_tools = true`. If they are not in the current tool catalog, do not invent calls to them; explain that script authoring is not enabled for this Agent configuration.

Four tools work together:

- `list_scripts` — list every script file with its description. Input is `{}`. Use it first to see what already exists before writing or running.
- `read_script` — read a script file's full content. Input is `{"file":"name.jsonl"}`. Use it to inspect the header and steps before editing or running.
- `write_script` — create or overwrite a script file. Input is `{"file":"name.jsonl","content":"..."}`. Writing replaces any existing file with the same name.
- `run_script` — execute a script file. Input is `{"file":"name.jsonl"}`.

## File naming

- Pass a bare file name only, for example `open-settings.jsonl`. Paths, `..`, and absolute paths are rejected.
- Use the `.jsonl` extension and a short kebab-case name describing the demo.

## Description header

Start every script with one or more comment lines beginning with `#`. These lines are the script's description: `list_scripts` joins them into the `description` field, and `run_script` skips them during execution.

- Put the description before the first step. A blank line ends the header.
- Keep it short and goal-oriented (what the demo does), not implementation detail.
- Comment lines that appear after the first step are not part of the description.

## Step format

After the header, each non-empty, non-comment line is exactly one JSON object — one step. Steps run top to bottom and stop on the first error. Three step types are supported:

- Wait: `{"type":"wait","ms":500}` pauses for the given milliseconds. Short form: `{"wait":500}`.
- Speak: `{"type":"tts","text":"正在打开设置"}` starts TTS playback asynchronously and immediately continues to the next line; it does not wait for speech to finish. Short form: `{"tts":"正在打开设置"}`.
- Call a tool: `{"type":"call","tool":"touch_gesture","input":{"type":"tap","point":{"x":500,"y":500}}}` invokes an existing tool with the given input. Short form: `{"call":{"tool":"screenshot","input":{}}}`.

The `tool` of a call step must be one of the script-callable device or utility tools, such as screenshot, touch/mouse/keyboard controls, quick_action, wait_for_stable_screen, image_diff, or audio_volume. `run_script` cannot call itself or administrative tools such as shell, memory, skill management, web, or script-file tools. Use the same input shape each allowed tool expects on its own.

## Example

```text
# 打开设置并停留演示
# 先提示用户，再点击设置图标
{"type":"tts","text":"正在打开设置"}
{"type":"wait","ms":800}
{"type":"call","tool":"touch_gesture","input":{"type":"tap","point":{"x":500,"y":500}}}
{"type":"wait","ms":1200}
```

## Authoring flow

1. Call `list_scripts` to check existing scripts and avoid clobbering one unintentionally.
2. When editing an existing script, call `read_script` first to inspect its current header and steps.
3. Build the content: a `#` description header, then one JSON step per line.
4. Call `write_script` with the file name and content. Confirm `ok:true` and that the reported `description` matches what you intended.
5. Only run the script with `run_script` when the user asks to play or test it.

## Cautions

- `tts` lines do not block, so insert a following `wait` step if later actions should happen after speech.
- A `wait` duration must be greater than zero and no more than 30 seconds.
- Coordinate-based call steps follow the same coordinate discipline as direct device operation: prefer `coord_space: "normalized"` (0-1000) and target the visible control center.
- Treat scripts as internal automation. When running them for ordinary user tasks, describe progress in terms of the user goal; do not expose script files, JSONL, or step details unless the user is explicitly debugging or authoring scripts.
