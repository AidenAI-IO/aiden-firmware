# MobileGym Environment Bridge API

MobileGym Bridge Server implements the benchmark environment bridge protocol.
The same protocol is also exposed by the Go agent when it is used as a real
device bridge.

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `GET /api/tools` | List tools exposed by the environment. |
| `POST /api/tools/{tool_name}` | Invoke an environment tool. |
| `GET /api/screen` | Return the current screen snapshot for pre/post capture. |
| `POST /api/setup` | Initialize or reset a task route. |
| `POST /api/release` | Release a task route. |
| `GET /api/concurrent` | Return bridge concurrency capacity. |
| `GET /screen` | Human screen viewer for MobileGym. |

MobileGym routes concurrent tasks by the `benchmark-task-id` header. The same id
must be sent to `/api/setup`, `/api/tools/*`, `/api/screen`, and `/api/release`
for a task worker.

## Tool Catalog

```bash
curl http://localhost:8888/api/tools
```

The response contains a `tools` array. Each entry includes `name`,
`description`, and `args_schema`.

## Tool Invocation

```bash
curl -X POST http://localhost:8888/api/tools/screenshot \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: suite.json:task-1" \
  -d '{"input": {}}'
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
curl -H "benchmark-task-id: suite.json:task-1" \
  http://localhost:8888/api/screen
```

The benchmark runner uses this endpoint to save `pre.jpg` and `post.jpg`.

## Setup And Release

```bash
curl -X POST http://localhost:8888/api/setup \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: suite.json:task-1" \
  -d '{}'

curl -X POST http://localhost:8888/api/release \
  -H "Content-Type: application/json" \
  -H "benchmark-task-id: suite.json:task-1" \
  -d '{}'
```

`/api/setup` claims an env route and resets it. `/api/release` frees that route
for later tasks.

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
