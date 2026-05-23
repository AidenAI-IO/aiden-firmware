# 工具 HTTP API

Agent 在 Web UI 模式下暴露所有 Agent-owned tools，供浏览器 Tool Lab、外部 Agent 或手工调用。

## 端点

| Method | Path | 说明 |
| --- | --- | --- |
| `GET` | `/api/tools` | 列出所有工具及描述、输入模式、示例、HTTP 绑定 |
| `POST` | `/api/tools/{tool_name}` | 调用指定工具 |
| `GET` | `/api/tool-skills` | 生成适合外部 Agent 使用的 `SKILL.md` bundle |

## 请求格式

JSON 对象输入：

```json
{
  "input": {"command": "pwd"}
}
```

原始字符串输入：

```json
{
  "raw_input": "{\"command\":\"pwd\"}"
}
```

纯文本工具（如 `activate_skill`）：

```json
{
  "input": "planner"
}
```

## 响应格式

```json
{
  "tool": {
    "name": "shell",
    "category": "system",
    "description": "...",
    "input_mode": "json",
    "example_input": "{\"command\":\"pwd\"}",
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

工具执行失败也会以 JSON 形式返回，需检查：

- `is_error`
- `output` 中是否包含错误信息
- HTTP transport 是否成功

## 当前工具清单

| 工具 | 类别 | 输入示例 |
| --- | --- | --- |
| `activate_skill` | skills | `"planner"` |
| `audio_volume` | audio | `{}` |
| `current_time` | system | `{"timezone":"Asia/Shanghai"}` |
| `enter_sleep` | system | `{"reason":"user asked me to sleep"}` |
| `keyboard_tap` | input | `{"keys":["ctrl","c"]}` |
| `keyboard_text` | input | `{"text":"hello world"}` |
| `mouse_click` | input | `{"x":0.5,"y":0.5,"button":"left","coord_space":"normalized"}` |
| `mouse_move` | input | `{"x":0.5,"y":0.5,"coord_space":"normalized"}` |
| `mouse_scroll` | input | `{"delta":-3}` |
| `screenshot` | observation | `{}` |
| `shell` | system | `{"command":"pwd"}` |
| `touch_gesture` | input | `{"type":"tap","point":{"x":0.5,"y":0.5}}` |
| `weather` | system | `{"location":"Shanghai"}` |

## curl 示例

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

curl -X POST http://127.0.0.1:8080/api/tools/current_time \
  -H 'Content-Type: application/json' \
  -d '{"input":{"timezone":"Asia/Shanghai"}}'

curl -X POST http://127.0.0.1:8080/api/tools/weather \
  -H 'Content-Type: application/json' \
  -d '{"input":{"location":"Shanghai"}}'
```

`screenshot` 成功输出通常包含 `width`、`height`、`format`、`size` 和 base64 JPEG `data`。
`keyboard_tap`、`keyboard_text`、`mouse_click`、`mouse_move`、`mouse_scroll` 和 `touch_gesture` 成功执行后，会等待 500ms 再自动截屏；其 `output` 为 JSON，包含原动作结果 `action_output`，以及截图的 `width`、`height`、`format`、`size` 和 base64 JPEG `data`。
`current_time` 支持 IANA 时区名（如 `Asia/Shanghai`、`America/New_York`）、`UTC`、`local` 和 UTC offset（如 `+08:00`）。
`weather` 支持地点名或经纬度，运行时通过 Open-Meteo 获取 geocoding、当前天气和短期预报。
`enter_sleep` 会让语音连续对话 session 在当前轮结束后关闭，回到等待下一次 wakeup 的模式。

## 外部 Agent 使用建议

- 优先通过 `GET /api/tools` 做能力发现；
- 需要屏幕操作时，先 `screenshot`，再点击/输入；
- 点击/输入等动作成功后直接检查该工具返回的 post-action screenshot，不需要再立刻调用一次 `screenshot`；
- 鼠标和触控优先使用 `coord_space: "normalized"`；
- 私有 IP 或 USB 网卡访问时注意代理绕过：设置 `NO_PROXY` / `no_proxy`；
- `shell` 的长任务应按工具说明使用后台 session，并在结束时停止。
- 用户要求“休眠 / 停止监听 / 等我下次唤醒”时，使用 `enter_sleep`，不要用普通文本回复假装已经休眠。
