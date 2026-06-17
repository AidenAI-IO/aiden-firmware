# Bridge Server Unified Tool API

## 概述

MobileGym Bridge Server 现在提供统一的 `/api/tools` endpoint，与 Go agent 的 tool proxy 接口完全兼容。这意味着：

1. **统一接口**：Bridge Server 和硬件板子上的 Go agent 使用相同的 API 格式
2. **无需特殊适配**：Go agent 可以通过 `--tool-proxy-endpoint` 参数连接任何兼容的 tool server
3. **简化架构**：移除了 MobileGym 特定的 bridge client 代码

## API 规范

### GET /api/tools

返回可用工具列表。

**Response:**
```json
{
  "tools": [
    {
      "name": "screenshot",
      "description": "Capture a screenshot...",
      "args_schema": {
        "type": "object",
        "properties": {},
        "additionalProperties": false
      }
    },
    {
      "name": "touch_gesture",
      "description": "Perform touch gestures...",
      "args_schema": {
        "type": "object",
        "properties": {
          "type": {"type": "string", "enum": ["tap", "swipe", ...]},
          "point": {"type": "object", ...}
        }
      }
    }
  ]
}
```

### POST /api/tools/{tool_name}

执行指定工具。

**Headers:**
- `Content-Type: application/json`

**Request Body:**
```json
{
  "input": {"text": "hello world"},
  // 或
  "raw_input": "{\"text\": \"hello world\"}"
}
```

**Response:**
```json
{
  "tool": {"name": "keyboard_text"},
  "raw_input": "{\"text\": \"hello world\"}",
  "output": "{\"action_output\": \"ok\", \"data\": \"...\", \"width\": 1080, \"height\": 2400}",
  "is_error": false,
  "duration_ms": 123
}
```

## 可用工具

### screenshot
获取屏幕截图。

**Input:** `{}` (空对象)

**Output:**
```json
{
  "data": "base64_encoded_jpeg...",
  "width": 1080,
  "height": 2400,
  "format": "jpeg"
}
```

### touch_gesture
执行触摸手势。

**Input:**
```json
{
  "type": "tap",           // tap, double_tap, long_press, swipe, drag, back, home
  "point": {"x": 500, "y": 800},  // 归一化坐标 (0-1000)
  "duration_ms": 300       // 可选
}
```

**Output:** 包含动作结果和截图的 JSON

### keyboard_text
输入文本。

**Input:**
```json
{
  "text": "hello world"
}
```

### keyboard_tap
按键盘按键。

**Input:**
```json
{
  "keys": ["enter"]        // 或 ["meta", "h"] 表示 meta+h
}
```

## 使用方式

### 方式 1: Go Agent 通过 Tool Proxy 连接

```bash
# 启动 Bridge Server
python benchmark/mobilegym/scripts/start_simulator.py \
  --bridge-port 8888 &

# 启动 Go agent 使用 tool proxy
go run src/agent/cmd/daemon/main.go \
  --tool-proxy-endpoint http://localhost:8888 \
  --tool-proxy-forward screenshot,touch_gesture,keyboard_text,keyboard_tap
```

### 方式 2: 直接 HTTP 调用

```bash
# 获取工具列表
curl http://localhost:8888/api/tools

# 执行 screenshot
curl -X POST http://localhost:8888/api/tools/screenshot \
  -H "Content-Type: application/json" \
  -d '{"input": {}}'

# 执行 tap
curl -X POST http://localhost:8888/api/tools/touch_gesture \
  -H "Content-Type: application/json" \
  -d '{"input": {"type": "tap", "point": {"x": 500, "y": 800}}}'
```

### 方式 3: Python Tool Proxy Client

```python
from agent.tool_proxy import ToolProxyClient

client = ToolProxyClient("http://localhost:8888")

# 调用工具
output, is_error, err = client.CallTool(
    ctx, 
    "screenshot", 
    "{}"
)
```

## Episode 管理

在使用工具之前，需要启动一个 episode：

```bash
# 启动 episode
curl -X POST http://localhost:8888/episode/start \
  -H "Content-Type: application/json" \
  -d '{"episode_id": "task-001"}'

# 使用工具...

# 结束 episode
curl -X POST http://localhost:8888/episode/end \
  -H "Content-Type: application/json" \
  -d '{"episode_id": "task-001"}'
```

## 兼容性

- ✅ 与 Go agent tool proxy 完全兼容
- ✅ 支持所有现有的 MobileGym 工具
- ✅ 向后兼容 episode 管理 API

## 测试

运行单元测试验证 API：

```bash
cd benchmark
python -m pytest mobilegym/bridge/test_tools_api.py -v
```

## 故障排查

### 409 Conflict (no active episode)
需要先调用 `/episode/start` 启动 episode。

### 504 Timeout
工具执行超时（默认 30 秒），检查 MobileGym 环境是否正常运行。

### Unknown tool
检查工具名称是否正确，使用 `GET /api/tools` 查看可用工具列表。
