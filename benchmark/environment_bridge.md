# Environment Bridge Protocol

Environment bridge is the HTTP contract between benchmark runners, agent
daemons, and any controllable environment. A bridge can represent a physical
device, a simulator, a browser, or another UI target.

Implementations in this repository:

- Go agent device bridge: fixed concurrency `1`.
- MobileGym bridge: concurrency equals the started env pool size.
- ADB Android bridge (`benchmark/adbandroid/`, see its README): drives an
  Android emulator or physical device through adb; fixed concurrency `1`.

## Required Endpoints

### `GET /health`

Returns bridge readiness and the controlled platform. Fixed-platform bridges
should expose canonical lowercase `platform` values such as `ios` or `android`:

```json
{
  "ok": true,
  "data": {
    "status": "ok",
    "bridge_type": "vphone_ios",
    "platform": "ios"
  }
}
```

Agent daemons running in environment bridge mode use this field as the runtime
device platform authority. The daemon derives its effective `device_type`
without rewriting `agent.toml`; an explicitly configured, conflicting
`[device].device_type` causes startup to fail. Benchmark runners may also use
the field to filter platform-specific tasks. For compatibility with older
bridges, known `bridge_type` values may still be used as a fallback.

### `GET /api/tools`

Returns the tool catalog used by agent daemons.

```json
{
  "tools": [
    {
      "name": "screenshot",
      "description": "Capture the current screen.",
      "args_schema": {"type": "object"}
    }
  ]
}
```

### `POST /api/tools/{tool_name}`

Invokes a tool. The request body accepts either structured `input` or string
`raw_input`.

```json
{"input": {"type": "tap", "point": {"x": 500, "y": 800}}}
```

The response should follow the Go agent tool response shape:

```json
{
  "tool": {"name": "touch_gesture"},
  "raw_input": "{\"type\":\"tap\"}",
  "output": "{\"ok\":true}",
  "is_error": false,
  "duration_ms": 42
}
```

### `GET /api/screen`

Returns the current screen snapshot for runner pre/post capture.
The task route is normally selected with the `benchmark-task-id` header. Screen
viewers may also pass `benchmark-task-id` as a query parameter when setting
custom headers is not practical.

```json
{
  "ok": true,
  "data": {
    "status": "running",
    "screenshot": {
      "format": "jpeg",
      "width": 1080,
      "height": 2400,
      "data": "base64..."
    }
  }
}
```

### `POST /api/setup`

Initializes or resets the environment route for a benchmark task.

```json
{"ok": true, "data": {"setup": true}}
```

For bridges that do not need setup, return success with `setup: false`.

### `POST /api/release`

Releases any resources claimed by a benchmark task.

```json
{"ok": true, "data": {"released": true}}
```

### `GET /api/concurrent`

Returns the number of task workers this bridge can support at the same time.

```json
{
  "ok": true,
  "data": {
    "bridge_type": "mobilegym",
    "concurrent": 5
  }
}
```

Schedulers should treat missing or invalid values as `1`.

## Task Routing

Concurrent bridges must route all task-scoped requests by the
`benchmark-task-id` HTTP header. Runners and agent daemons send the same id for:

- `/api/setup`
- `/api/tools/{tool_name}`
- `/api/screen`
- `/api/release`

The bridge should keep requests with the same `benchmark-task-id` on the same
underlying env until `/api/release` is called.

## Agent Daemon Mode

Start the Go agent so selected tools are forwarded to an environment bridge:

```bash
daemon \
  -dir /config \
  -addr 0.0.0.0:8080 \
  --environment-bridge-mode \
  --environment-bridge-endpoint http://bridge:9090 \
  --environment-bridge-tools screenshot,touch_gesture,keyboard_text,keyboard_tap,mouse_click,mouse_move,mouse_scroll,quick_action \
  --benchmark-task-id suite.json:task-1
```

The agent still exposes its normal `/api/chat` endpoint. Only configured tools
are forwarded to the environment bridge.
