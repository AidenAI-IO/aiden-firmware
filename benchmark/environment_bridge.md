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
should expose one of the canonical lowercase `platform` values: `ios`,
`android`, `mac`, `windows`, or `linux`:

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

Benchmark orchestration uses this field for environment discovery, task
filtering, and platform consistency checks. When benchmark starts an agent
daemon, it passes the resolved platform through the process-local
`--device-type` option. The daemon applies it as a process-local override of
`[device].device_type` without modifying `agent.toml`, and reports the effective
value as `device_type`. A pre-started external daemon must receive the same
option from its caller. For compatibility with older bridges, known
`bridge_type` values may still be used as a fallback.

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

Optional request body:

```json
{"episode_id": "task-episode", "setup_token": "optional-idempotency-token"}
```

For bridges that do not need setup, return success with `setup: false`.

`setup_token` is optional. When present, a bridge runs at most one concurrent
setup operation for the same `benchmark-task-id` and token. Concurrent
duplicates wait for the original operation, and successful results are replayed
without another reset. Failed operations are not cached, so a later request may
retry. Calls without a token retain the traditional behavior: every request
performs setup/reset. The cache is process-local, so a restarted bridge may
execute the same token once to recreate a lost route.
After `/api/release`, a client that starts a new logical session must use a new
token. Bridges may discard completed token entries for the released task, so an
old token no longer identifies an idempotent operation after release.

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
  --device-type android \
  --environment-bridge-mode \
  --environment-bridge-endpoint http://bridge:9090 \
  --environment-bridge-tools screenshot,touch_gesture,keyboard_text,keyboard_tap,mouse_move,mouse_scroll,quick_action \
  --benchmark-task-id suite.json:task-1
```

The agent still exposes its normal `/api/chat` endpoint. Only configured tools
are forwarded to the environment bridge.
