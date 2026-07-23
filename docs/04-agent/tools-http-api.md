# Tools HTTP API

In Web UI mode, the Agent exposes Agent-owned tools that can be safely invoked via HTTP for the browser Tool Lab, external agents, or manual calls. Internal maintenance tools (such as `skill_manage`) are not exposed via HTTP.

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

Once an HID-affecting tool invocation is accepted, its execution is independent
of the client socket and has a five-minute server-side timeout. This is important
when the client reaches the board through USB ECM: iOS keyboard profile isolation
briefly re-enumerates the USB composite and may drop the HTTP connection, but the
tool continues running and still performs its final HID profile restore. The
caller may need to reconnect and observe the resulting screen if the response
socket was lost. Other HTTP tools retain normal client-cancellation behavior.

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

curl -X POST http://127.0.0.1:8080/api/tools/weather \
  -H 'Content-Type: application/json' \
  -d '{"input":{"location":"Shanghai"}}'
```

A successful `screenshot` output typically includes `width`, `height`, `format`, `size`, and base64 JPEG `data`.
A successful `wait_for_stable_screen` output includes stability fields `ok`, `stable`, `elapsed_ms`, `screen_changed`, and also returns a screenshot with `width`, `height`, `format`, `size`, and base64 JPEG `data`; `screen_changed=false` means no visible screen change was observed during the wait window, while `stable=false` indicates the screen is still changing but the screenshot can still be used as a current observation.
After successful execution of `keyboard_tap`, `keyboard_text`, `mouse_click`, `mouse_move`, `mouse_scroll`, and `touch_gesture`, the system waits for screen stability (or until timeout) and automatically takes a screenshot; their `output` is JSON containing the original action result `action_output`, the stability fields `screen_stable`, `stable_wait_ms`, `screen_changed`, and the screenshot's `width`, `height`, `format`, `size`, and base64 JPEG `data`.
For `touch_gesture`, `back` swipes from near the left physical edge, and `home` swipes up from near the bottom physical edge; normalized coordinates use the 0-1000 range. When manually writing `swipe`, also use edge-aligned start points, e.g., `start.x=1` or `start.y=999`.
`weather` supports location names or latitude/longitude coordinates, fetching geocoding, current weather, and short-term forecasts from Open-Meteo at runtime.
`wait_for_wakeup` is a terminating runtime tool. After a successful tool call, it immediately ends the current Agent run and returns the voice interaction to waiting for the next wakeup; it does not ask the model to provide an additional final answer. The run result will set `wait_for_wakeup_requested` / `wait_for_wakeup_reason`; the old fields `sleep_requested` / `sleep_reason` are retained as compatibility aliases only.
`run_script` executes local JSONL demo scripts from the config directory's `scripts/` folder, without calling the LLM between script steps. Input accepts only a filename, such as `{"file":"demo.jsonl"}`; full paths or subdirectories are not allowed. Each non-empty line is a JSON instruction, for example `{"type":"wait","ms":500}`, `{"type":"tts","text":"Opening settings"}`, `{"type":"call","tool":"touch_gesture","input":{"type":"tap","point":{"x":500,"y":500}}}`; shorthand is also supported: `{"wait":500}`, `{"tts":"Opening settings"}`, `{"call":{"tool":"screenshot","input":{}}}`. `tts` starts voice playback asynchronously and immediately continues to the next line without waiting for synthesis or playback to complete; the corresponding step in the returned result will include the original `text` and `output:"queued"`; the script stops at the first synchronous error.

## Recommendations for External Agents

- Prioritize capability discovery via `GET /api/tools`
- When screen operations are needed, `screenshot` first, then click/input
- After successful click/input actions, directly check the post-action screenshot returned by that tool; no need to immediately call `screenshot` again
- For mouse and touch, prefer `coord_space: "normalized"` with 0-1000 coordinates
- When accessing via private IP or USB network adapter, note proxy bypass: set `NO_PROXY` / `no_proxy`
- For long-running `shell` tasks, use background sessions as described in the tool documentation, and stop them when done
- When the user requests "sleep / stop listening / wait for my next wakeup", use `wait_for_wakeup` rather than pretending to return to waiting for wakeup with a normal text reply
