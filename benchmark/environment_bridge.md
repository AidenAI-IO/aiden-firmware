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

Returns the tool catalog used by agent daemons. Screenshot is not a forwarded
tool; capture goes through `POST /api/providers/screenshot`.

```json
{
  "tools": [
    {
      "name": "touch_gesture",
      "description": "Perform a touch gesture.",
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

### `POST /api/providers/screenshot`

Returns a raw screen frame. The runner uses this for `pre.jpg` / `post.jpg`,
and the agent uses it for its local `screenshot` tool. This is not a forwarded
tool call: when environment-bridge mode is on, the agent keeps running
`screenshot` locally and captures through this provider.

```json
{
  "format": "jpeg",
  "quality": 80,
  "crop_black": true,
  "minimal_width": 608
}
```

```json
{
  "ok": true,
  "data": {
    "meta": {
      "seq": 12,
      "width": 1080,
      "height": 2400,
      "source_width": 1080,
      "source_height": 2400,
      "crop_x": 0,
      "crop_y": 0,
      "crop_width": 1080,
      "crop_height": 2400,
      "pixel_format": "jpeg",
      "stride": 0,
      "bytes": 184320,
      "stale": false
    },
    "capture_info": {
      "capture_backend": "adb"
    },
    "image": "base64..."
  }
}
```

Bridges may ignore unsupported capture options such as `raw` or `crop_black`
and return the format they actually produced in `meta.pixel_format`.

Clients and bridges treat a single capture as a 30 second operation. The agent
HTTP provider, runner pre/post capture, and `POST /api/providers/screenshot`
handlers share that budget so a hung env fails at the caller instead of
continuing after the client has already given up. Action and setup requests
may use a longer timeout.

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
- `/api/providers/screenshot`
- `/api/release`

The bridge should keep requests with the same `benchmark-task-id` on the same
underlying env until `/api/release` is called.

## Agent Daemon Mode

Start the Go agent so selected tools are forwarded to an environment bridge.
`screenshot` is never forwarded; the agent captures it through
`POST /api/providers/screenshot`.

```bash
daemon \
  -dir /config \
  -addr 0.0.0.0:8080 \
  --device-type android \
  --environment-bridge-mode \
  --environment-bridge-endpoint http://bridge:9090 \
  --environment-bridge-tools touch_gesture,keyboard_text,keyboard_tap,mouse_move,mouse_scroll,quick_action \
  --benchmark-task-id suite.json:task-1
```

The agent still exposes its normal `/api/chat` endpoint. Only configured tools
are forwarded to the environment bridge. The agent also exposes
`POST /api/providers/screenshot` when it is itself used as a bridge.
