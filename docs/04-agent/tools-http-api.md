---
sidebar_position: 3
---

# Tools HTTP API

In every input mode, the Agent exposes Agent-owned tools that can be safely invoked via HTTP for the browser Tool Lab, external agents, or manual calls. Internal maintenance tools (such as `skill_manage`) are not exposed via HTTP.

## Endpoints

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/api/tools` | List HTTP-visible tools with descriptions, input modes, examples, and HTTP bindings |
| `POST` | `/api/tools/{tool_name}` | Invoke the specified tool |
| `GET` | `/api/tool-skills` | Generate a `SKILL.md` bundle suitable for external agents |

## Request Format

JSON object input:

```json
{
  "input": {"command": "pwd"}
}
```

Raw string input:

```json
{
  "raw_input": "{\"command\":\"pwd\"}"
}
```

Use `raw_input` when you need to pass a raw string. Most tools (including skill tools) describe their input with JSON examples in the catalog:

```json
{
  "raw_input": "planner"
}
```

## Response Format

```json
{
  "tool": {
    "name": "shell",
    "category": "system",
    "description": "...",
    "input_mode": "json",
    "example_input": "{\"command\":\"pwd\"}",
    "args_schema": {
      "type": "object",
      "properties": {
        "command": {"type": "string"}
      },
      "additionalProperties": false
    },
    "http": {
      "method": "POST",
      "path": "/api/tools/shell"
    }
  },
  "raw_input": "{\"command\":\"pwd\"}",
  "output": "...",
  "is_error": false,
  "duration_ms": 12,
  "called_at": "2026-05-18T12:34:56Z"
}
```

Tool execution failures are also returned in JSON format. Check:

- `is_error`
- Whether `output` contains error information
- Whether the HTTP transport succeeded

## Catalog Scope

The HTTP catalog is generated from registered Agent-owned tools at runtime. It can include diagnostic, browser Tool Lab, and external-agent tools that are intentionally absent from the default conversational Agent prompt.

`current_time` and `calculator` are not registered and therefore do not appear in either the conversational or HTTP tool catalogs. Use `shell` for controller-local precise time, timezone, and deterministic calculations. The conversational Agent omits `list_scripts`, `read_script`, and `write_script` from its default LLM `tools` request; configure `load_all_tools = true` to include those three script-authoring tools.

## Platform-Specific Conversational Catalog

The conversational Agent tool catalog is filtered by the global `[device].device_type` state. Valid configured device type values are `iOS`, `Android`, `macOS`, `windows`, and `linux`; the runtime derives lowercase platform identifiers (`ios`, `android`, `macos`, `windows`, `linux`) from those values for tool-catalog filtering. Tools with no platform metadata are treated as portable.

Platform-specific tools should stay registered and HTTP-visible for Tool Lab and manual diagnostics unless they are unsafe or impossible to invoke directly. The platform split applies to the model-facing catalog so the model is not taught tools that cannot run for the current device.

When adding or changing a tool, use this rule:

- Prefer one semantic tool that reads runtime `device_type` internally when the capability is the same across platforms.
- Add platform metadata in `builtInToolSpecMetadata` when the tool cannot run on every configured device type, its schema or description would imply the wrong platform behavior, or the supported action set is materially different.
- In `builtInToolSpecMetadata`, use derived platform identifiers: `ios` and `android` for phone-companion capabilities, `macos` for Mac desktop bindings, and add `windows` or `linux` only after the tool has verified bindings for those platforms. Do not use these lowercase identifiers as documented `[device].device_type` values.
- Do not ask the model to pass platform/device/os arguments for platform selection; the runtime derives the platform from global `device_type` state.
- If a tool has platform-specific subcommands, keep the execution path backward-compatible but make its runtime `ArgsSchema()` list only the subcommands active for the current `device_type` (for example `quick_action.action` and `touch_gesture.type`).
- When a nominally platform-specific namespace has a portable subset, expose only that subset on other platforms instead of hiding the namespace entirely; for example non-Android `keyboard_tap` may list only the absolute pointer-mode `KEYCODE_*` media, volume, screenshot, and brightness aliases.
- Add focused tests for `AgentToolsForPlatform`, `Runtime.availableTools()`, or platform-specific tool schemas whenever a tool is introduced or moved between platform groups.

Once an HID-affecting tool invocation is accepted, its execution is independent
of the client socket and has a five-minute server-side timeout. This is important
when the client reaches the board through USB ECM: iOS keyboard profile isolation
briefly re-enumerates the USB composite and may drop the HTTP connection, but the
tool continues running and still performs its final HID profile restore. The
caller may need to reconnect and observe the resulting screen if the response
socket was lost. This applies to keyboard, pointer/touch, composite text/search,
quick-action, and script tools that can participate in the serialized HID flow.
Within one HTTP invocation, consecutive modifier-bearing actions share one
pointer-free phase; pointer/touch input restores the mouse before acting.
Separate HTTP invocations each manage their own isolation scope. Other HTTP
tools retain normal client-cancellation behavior.

The HTTP catalog is a separate policy. It exposes registered operational and specialized tools together with their `args_schema`, but internal maintenance tools such as `skill_manage` and `skill_mark_used` are never listed or callable through the default HTTP Tool API.

## curl Examples

```bash
curl http://127.0.0.1:8080/api/tools

curl -X POST http://127.0.0.1:8080/api/tools/shell \
  -H 'Content-Type: application/json' \
  -d '{"input":{"command":"pwd"}}'

curl -X POST http://127.0.0.1:8080/api/tools/keyboard_text \
  -H 'Content-Type: application/json' \
  -d '{"input":{"text":"hello from API"}}'

curl -X POST http://127.0.0.1:8080/api/tools/screenshot \
  -H 'Content-Type: application/json' \
  -d '{"input":{}}'

# Replace the placeholders with the base64 JPEG data returned by two screenshot
# or post-action tool results.
curl -X POST http://127.0.0.1:8080/api/tools/image_diff \
  -H 'Content-Type: application/json' \
  -d '{"input":{"before":"<before-base64-jpeg>","after":"<after-base64-jpeg>"}}'

curl -X POST http://127.0.0.1:8080/api/tools/weather \
  -H 'Content-Type: application/json' \
  -d '{"input":{"location":"Shanghai"}}'
```

A successful `screenshot` output typically includes `width`, `height`, `format`, `size`, and base64 JPEG `data`.
A successful `wait_for_stable_screen` output includes stability fields `ok`, `stable`, `elapsed_ms`, `screen_changed`, and also returns a screenshot with `width`, `height`, `format`, `size`, and base64 JPEG `data`; `screen_changed=false` means no visible frame change was observed during the wait window, while `stable=false` indicates the screen is still changing but the screenshot can still be used as a current observation.
After successful execution of `keyboard_tap`, `keyboard_text`, `mouse_move`, `mouse_scroll`, and `touch_gesture`, the system waits for screen stability (or until timeout) and automatically takes a screenshot; their `output` is JSON containing the original action result `action_output`, the stability fields `screen_stable`, `stable_wait_ms`, `screen_changed`, and the screenshot's `width`, `height`, `format`, `size`, and base64 JPEG `data`. Direct HTTP callers pass the two JPEG `data` values to `image_diff`. Inside an Agent run, persisted visual observations are instead labeled with unique `screenshot_attachment_id` values that can be passed in the same `before` and `after` fields.
For `touch_gesture`, `back` swipes from near the left physical edge, and `home` swipes up from near the bottom physical edge; normalized coordinates use the 0-1000 range. When manually writing `swipe`, also use edge-aligned start points, e.g., `start.x=1` or `start.y=999`.
`weather` supports location names or latitude/longitude coordinates, fetching geocoding, current weather, and short-term forecasts from Open-Meteo at runtime.
`wait_for_wakeup` is a terminating runtime tool. After a successful tool call, it immediately ends the current Agent run and returns the voice interaction to waiting for the next wakeup; it does not ask the model to provide an additional final answer. The run result will set `wait_for_wakeup_requested` / `wait_for_wakeup_reason`; the old fields `sleep_requested` / `sleep_reason` are retained as compatibility aliases only.
`run_script` executes local JSONL demo scripts from the config directory's `scripts/` folder, without calling the LLM between script steps. Input accepts only a filename, such as `{"file":"demo.jsonl"}`; full paths or subdirectories are not allowed. Each non-empty line is a JSON instruction, for example `{"type":"wait","ms":500}`, `{"type":"tts","text":"Opening settings"}`, `{"type":"call","tool":"touch_gesture","input":{"type":"tap","point":{"x":500,"y":500}}}`; shorthand is also supported: `{"wait":500}`, `{"tts":"Opening settings"}`, `{"call":{"tool":"screenshot","input":{}}}`. `tts` starts voice playback asynchronously and immediately continues to the next line without waiting for synthesis or playback to complete; the corresponding step in the returned result will include the original `text` and `output:"queued"`; the script stops at the first synchronous error.

## Recommendations for External Agents

- Prioritize capability discovery via `GET /api/tools`
- When screen operations are needed, `screenshot` first, then click/input
- After successful click/input actions, directly check the post-action screenshot returned by that tool; no need to immediately call `screenshot` again
- For mouse and touch, use normalized 0-1000 coordinates
- When accessing via private IP or USB network adapter, note proxy bypass: set `NO_PROXY` / `no_proxy`
- For long-running `shell` tasks, use background sessions as described in the tool documentation, and stop them when done
- When the user requests "sleep / stop listening / wait for my next wakeup", use `wait_for_wakeup` rather than pretending to return to waiting for wakeup with a normal text reply
