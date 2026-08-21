# MobileGym Environment Bridge API

MobileGym Bridge Server implements the benchmark environment bridge protocol.
The same protocol is also exposed by the Go agent when it is used as a real
device bridge.

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `GET /api/tools` | List legacy tools exposed by the environment. |
| `POST /api/tools/{tool_name}` | Invoke a legacy environment tool. |
| `POST /api/providers/screenshot` | Return the current screen frame for pre/post capture and the agent screenshot tool. |
| `POST /api/providers/mnk` | Execute a Go `mnk.Provider` operation for agent input. |
| `POST /api/setup` | Initialize or reset a task route. |
| `POST /api/release` | Release a task route. |
| `GET /api/concurrent` | Return bridge concurrency capacity. |

MobileGym routes concurrent tasks by the `benchmark-task-id` header. The same id
must be sent to `/api/setup`, `/api/providers/screenshot`, `/api/providers/mnk`, and `/api/release`
for a task worker.

## Tool Catalog

```bash
curl http://localhost:8888/api/tools
```

The response contains a `tools` array. Each entry includes `name`,
`description`, and `args_schema`.

## Tool Invocation

```bash
curl -X POST http://localhost:8888/api/tools/touch_gesture \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: suite.json:task-1" \
  -d '{"input": {"type": "tap", "point": {"x": 500, "y": 800}}}'
```

Request bodies may use either structured `input` or string `raw_input`:

```json
{"input": {"text": "hello world"}}
```

```json
{"raw_input": "{\"text\":\"hello world\"}"}
```

The response matches Go agent `ToolInvokeResponse`:

```json
{
  "tool": {"name": "keyboard_text"},
  "raw_input": "{\"text\":\"hello world\"}",
  "output": "{\"action_output\":\"ok\"}",
  "is_error": false,
  "duration_ms": 123
}
```

## Screen Capture

```bash
curl -X POST http://localhost:8888/api/providers/screenshot \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: suite.json:task-1" \
  -d '{"format": "jpeg", "quality": 80}'
```

The benchmark runner uses this endpoint to save `pre.jpg` and `post.jpg`.

## MNK Provider

The Go agent sends normalized 0-1000 click, double-click, swipe, drag, keypress,
move, and vertical scroll operations to this endpoint. A successful request
returns `{"success": true}`.

```bash
curl -X POST http://localhost:8888/api/providers/mnk \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: suite.json:task-1" \
  -d '{"operation":"click","click":{"x":500,"y":800,"button":"left","hold_ms":0}}'
```

## Setup And Release

```bash
curl -X POST http://localhost:8888/api/setup \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: suite.json:task-1" \
  -d '{"app_ids":["settings"]}'

curl -X POST http://localhost:8888/api/release \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: suite.json:task-1" \
  -d '{}'
```

`/api/setup` claims an env route and resets it. `/api/release` frees that route
for later tasks.

`app_ids` is optional. Missing or empty `app_ids` skips eager app data loading;
non-empty lists preload only the named apps. The app launch path still loads an
app's data on demand.

## Concurrency

```bash
curl http://localhost:8888/api/concurrent
```

MobileGym returns the env pool size:

```json
{
  "ok": true,
  "data": {
    "bridge_type": "mobilegym",
    "concurrent": 5,
    "env_count": 5,
    "active_routes": {}
  }
}
```

The Go agent bridge returns `concurrent: 1`.
