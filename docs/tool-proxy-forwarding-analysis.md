# Tool Proxy Mode

## 背景

Tool proxy mode 用于方便远程连接实机进行测试：agent 运行的机器（thin client）将选定的 tool call 转发到另一台连接了实际设备的 daemon 上执行。LLM 循环在 thin client 运行，设备操作在实机执行。

之前的实现会转发**所有** tool call。现在改为只转发**与设备相关**的 tool，由调用方通过 `--forward-tools` 显式指定，不在代码中硬编码默认列表。

## 配置（仅 CLI）

Tool proxy 相关配置**仅通过 CLI 参数**传递，不从配置文件读取（`ToolProxyConfig` 字段标记为 `toml:"-"`）。

| 参数 | 说明 |
| --- | --- |
| `--tool-proxy-mode` | 启用 tool proxy mode |
| `--tool-proxy-endpoint` | 远程 daemon endpoint，如 `http://192.168.50.123:8080`（必填） |
| `--forward-tools` | 要转发的 tool 名称或 glob 模式，逗号分隔（启用 proxy mode 时必填） |

启用 `--tool-proxy-mode` 时，`--tool-proxy-endpoint` 和 `--forward-tools` 都必填，缺少任一项 daemon 会报错退出。没有任何硬编码的默认转发列表。

## --forward-tools 匹配规则

每个逗号分隔项是一个 [`path.Match`](https://pkg.go.dev/path#Match) 风格的 glob 模式，与 tool 名称匹配：

- `*` — 匹配所有 tool（转发全部，等同旧行为）
- `keyboard_*` — 匹配所有 `keyboard_` 前缀的 tool
- `*_click` — 匹配所有 `_click` 后缀的 tool
- `screenshot` — 精确匹配单个 tool

只要 tool 名称匹配列表中**任意一个**模式即转发。空白项被忽略；空列表转发任何 tool。

## 使用示例

```bash
# 转发所有设备相关 tool（HID + 屏幕 + 音频 + phone bridge）
./daemon --config /userdata \
  --tool-proxy-mode \
  --tool-proxy-endpoint http://192.168.50.123:8080 \
  --forward-tools "keyboard_*,mouse_*,touch_gesture,quick_action,screenshot,wait_for_stable_screen,audio_volume,open_app,clipboard,calendar,contacts,notification"

# 只测试截图和触摸
./daemon --config /userdata \
  --tool-proxy-mode \
  --tool-proxy-endpoint http://192.168.50.123:8080 \
  --forward-tools "screenshot,touch_gesture"

# 转发所有 tool（旧行为）
./daemon --config /userdata \
  --tool-proxy-mode \
  --tool-proxy-endpoint http://192.168.50.123:8080 \
  --forward-tools "*"
```

启动日志：

```
🔀 Tool proxy mode: forwarding to http://192.168.50.123:8080
   Forward tools: [keyboard_* mouse_* screenshot ...]
```

## Tool 分类参考

转发哪些 tool 由 `--forward-tools` 决定，下表是推荐的设备相关 tool 参考。

### 设备交互类（建议转发）

| 类别 | Tool |
| --- | --- |
| HID 输入 | `keyboard_tap`, `keyboard_text`, `mouse_click`, `mouse_move`, `mouse_scroll`, `touch_gesture`, `quick_action` |
| 屏幕捕获 | `screenshot`, `wait_for_stable_screen` |
| 音频控制 | `audio_volume` |
| Phone Bridge | `open_app`, `clipboard`, `calendar`, `contacts`, `notification` |

对应的 glob 简写：`keyboard_*,mouse_*,touch_gesture,quick_action,screenshot,wait_for_stable_screen,audio_volume,open_app,clipboard,calendar,contacts,notification`

### 非设备类（不建议转发）

| 类别 | Tool | 原因 |
| --- | --- | --- |
| 纯计算 | `current_time`, `calculator`, `image_diff` | 无设备依赖，本地执行结果相同 |
| 网络请求 | `weather`, `web_search`, `wikipedia`, `web_scraper` | 与设备无关，转发只增加额外网络跳转 |
| Shell | `shell` | 已有独立 proxy 机制 |
| Memory/State | `recall_*`, `save_memory`, `forget_memory`, `inspect_episode`, `enter_sleep`, `request_human_handoff` | 管理 thin client 自身状态 |
| Skill 管理 | `skill_list`, `skill_read`, `skill_manage`, `skill_mark_used` | agent 自身能力，应在 thin client 管理 |

## 实现细节

### 代码改动

- **`config.go`**: `ToolProxyConfig` 添加 `ForwardTools []string`，所有字段标记 `toml:"-"`（仅 CLI）
- **`cmd/daemon/main.go`**: 添加 `--forward-tools` 参数与 `parseCommaSeparated()`；proxy mode 下校验 endpoint 和 forward-tools 必填
- **`tool_execution.go`**: `shouldProxyTool(toolName, forwardTools)` 用 `path.Match` 做 glob 匹配；`ToolCallExecution` 添加 `ForwardTools` 字段；`executeToolCall` 仅在匹配时转发
- **`role_executor.go`**: `roleCollaborativeExecutor` 添加 `ForwardTools`，传递给 `ToolCallExecution`
- **`runtime.go`**: 从 `cfg.ToolProxy.ForwardTools` 读取并设置到 executor

### API 行为不变

`/api/tools/{name}` HTTP API 继续暴露所有 tool。API handler 本身永远不会转发（无 ProxyClient），远程 daemon 收到转发请求后本地执行，不会再次转发，避免成对 daemon 之间的转发循环。

## 测试

`tool_proxy_test.go`:

- `TestToolProxyMatchesLocal*` — 转发结果与本地执行一致（成功 / tool error / error-like 输出）
- `TestToolProxyTransportFailureIsError` — 网络错误被格式化为 tool error
- `TestShouldProxyToolWithExplicitList` — 精确名称列表匹配
- `TestShouldProxyToolEmptyListForwardsNothing` — 空列表不转发任何 tool（无硬编码默认）
- `TestShouldProxyToolWildcard` — `*` / 前缀 / 后缀 / 多模式 / 空白项等 glob 行为
- `TestToolProxyForwardsByWildcard` — `keyboard_*` 转发 `keyboard_tap`，`calculator` 本地执行
- `TestToolProxyOnlyForwardsSpecifiedTools` — 只转发指定 tool

## MobileGym 说明

MobileGym backend 本身就通过网络通信，不建议用 tool proxy，直接配置 MobileGym endpoint 即可。
