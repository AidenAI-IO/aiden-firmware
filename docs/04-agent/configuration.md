# Agent 配置参考

Agent 期望 `-config` 指向一个目录，而不是单个配置文件。

## 目录布局

```text
/userdata/agent/
├── agent.toml       # 必需
├── skills/          # 可选，自动发现 **/SKILL.md
├── log/             # 运行时日志目录
└── memory/          # 对话记忆持久化目录
```

TOML 是当前支持的配置格式；JSON 配置已废弃。

## Web UI 最小配置

```toml
instruction = "默认用简体中文回答，语气要像真人说话，简短自然，适合 TTS 播放。需要读取或改变手机、外部设备或服务状态时必须使用工具；可以连续组合多个工具完成任务。每次截图或输入工具返回 post-action screenshot 后，都要先根据最新画面判断上一步是否已经生效、焦点是否改变、页面是否跳转；不要连续重复同一个点击、手势或按键。在手机上打开 App、查找联系人、设置项、商品或页面内容时，优先使用系统搜索、App 内搜索或页面上的搜索框；不要先靠连续滑动、翻页来碰运气。keyboard_text 是模拟美式键盘按键，必须传 JSON，例如 {\"text\":\"App Store\"}；不要传裸字符串；只能输入 ASCII 可键入字符，不能直接输入中文、emoji 或其他非键盘字符，需要中文时改用拼音/英文关键词并从候选或搜索结果中选择。点击要以最新截图为准，选择可见目标的中心点，并优先使用 coord_space:\"normalized\" 的 0..1 坐标；手机投屏/截图可能被缩放，pixel 坐标容易和实际触控坐标偏移。除非用户明确要求或坐标系已经校准，不要使用 coord_space:\"pixel\"。坐标不确定时先截图确认，不要用大概位置连续试点。用户要求拨打电话时，把它当作手机 UI 自动化任务：先用截图确认状态，再用 touch_gesture、mouse_click、keyboard_text、keyboard_tap 等工具打开拨号或联系人、输入号码并点击拨号；不要因为没有单独的拨打电话工具就说做不到。手机边缘手势要从物理边缘附近开始，返回优先用 touch_gesture 的 type back，回主屏优先用 type home；手写 swipe 时左边缘返回用 start.x=0.001 左右，底边回主页用 start.y=0.999 左右。"
max_iterations = -1
input_mode = "text"

[model]
provider = "openrouter"
model = "bytedance-seed/seed-2.0-lite"
token_env = "OPENROUTER_API_KEY"
temperature = 0.2
max_tokens = 1000

[proxy]
http_proxy = ""
https_proxy = ""
all_proxy = ""
no_proxy = ""

[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 32000
channels = 1
bit_width = 16

[hid]
keyboard_device = "/dev/hidg0"
mouse_device = "/dev/hidg1"
frame_socket = "/run/frame_service/frame_service.sock"
```

> `token_env` 表示从环境变量读取密钥。overlay 示例配置中也可能直接写 `api_key` 字段；生产环境建议使用环境变量或设备侧安全注入方式。

## STT 语音模式最小配置

```toml
instruction = "默认用简体中文回答，语气要像真人说话，简短自然，适合 TTS 播放。需要读取或改变手机、外部设备或服务状态时必须使用工具；可以连续组合多个工具完成任务。每次截图或输入工具返回 post-action screenshot 后，都要先根据最新画面判断上一步是否已经生效、焦点是否改变、页面是否跳转；不要连续重复同一个点击、手势或按键。在手机上打开 App、查找联系人、设置项、商品或页面内容时，优先使用系统搜索、App 内搜索或页面上的搜索框；不要先靠连续滑动、翻页来碰运气。keyboard_text 是模拟美式键盘按键，必须传 JSON，例如 {\"text\":\"App Store\"}；不要传裸字符串；只能输入 ASCII 可键入字符，不能直接输入中文、emoji 或其他非键盘字符，需要中文时改用拼音/英文关键词并从候选或搜索结果中选择。点击要以最新截图为准，选择可见目标的中心点，并优先使用 coord_space:\"normalized\" 的 0..1 坐标；手机投屏/截图可能被缩放，pixel 坐标容易和实际触控坐标偏移。除非用户明确要求或坐标系已经校准，不要使用 coord_space:\"pixel\"。坐标不确定时先截图确认，不要用大概位置连续试点。用户要求拨打电话时，把它当作手机 UI 自动化任务：先用截图确认状态，再用 touch_gesture、mouse_click、keyboard_text、keyboard_tap 等工具打开拨号或联系人、输入号码并点击拨号；不要因为没有单独的拨打电话工具就说做不到。手机边缘手势要从物理边缘附近开始，返回优先用 touch_gesture 的 type back，回主屏优先用 type home；手写 swipe 时左边缘返回用 start.x=0.001 左右，底边回主页用 start.y=0.999 左右。"
input_mode = "stt"
trigger_mode = "manual"
energy_threshold = 500
silence_ms = 650
min_speech_ms = 300
voice_session_enabled = true
voice_followup_timeout_ms = 6000
voice_first_turn_timeout_ms = 10000
voice_max_turns = 0
voice_interrupt_on_wakeup = true
voice_streaming_tts_enabled = true
voice_tool_call_speech = true
voice_max_response_tokens = 400

[model]
provider = "openrouter"
model = "bytedance-seed/seed-2.0-lite"
token_env = "OPENROUTER_API_KEY"

[stt]
provider = "openrouter"
api_key = "OPENROUTER_API_KEY"
model = "qwen/qwen3-asr-flash-2026-02-10"

[tts]
provider = "minimax"
api_key = "..."
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0

[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 16000
channels = 1
bit_width = 16

[hid]
keyboard_device = "/dev/hidg0"
mouse_device = "/dev/hidg1"
frame_socket = "/run/frame_service/frame_service.sock"
```

## 顶层字段

| 字段 | 默认/可选值 | 说明 |
| --- | --- | --- |
| `instruction` | - | Agent system instruction；默认建议用中文、口语化、强调外部状态必须用工具，并约束 UI 自动化先看截图、优先搜索、键盘只输入 ASCII、点击取目标中心且优先 normalized 坐标 |
| `additional_prompt` | - | 额外 prompt 字段；运行时会追加到 `instruction` 后面 |
| `max_iterations` | `-1` | 单次运行最大工具调用循环次数；`-1` 表示不限制 |
| `input_mode` | `text` / `stt` / `audio` | 输入模式 |
| `trigger_mode` | `manual` / `wakeup` | 语音模式触发方式 |
| `energy_threshold` | `500` | VAD 能量阈值 |
| `silence_ms` | `650` | 多少毫秒静音后认为一句话结束 |
| `min_speech_ms` | `300` | 最短有效语音时长 |
| `voice_session_enabled` | `true` | wakeup 模式下启用一次唤醒后的连续对话；设为 `false` 保持一轮一唤醒 |
| `voice_followup_timeout_ms` | `6000` | Agent 回复后等待用户追问的窗口 |
| `voice_first_turn_timeout_ms` | `10000` | wakeup 后等待第一句话的窗口 |
| `voice_max_turns` | `0` | 单个 wakeup session 最大轮数；`0` 表示不限制 |
| `voice_interrupt_on_wakeup` | `true` | session 内再次收到 wakeup 时取消 thinking/TTS 并重新听音 |
| `voice_streaming_tts_enabled` | `true` | LLM 流式输出时按句送入 TTS，降低首句播放等待 |
| `voice_tool_call_speech` | `true` | 是否异步朗读工具调用说明；默认开启以避免工具执行期间长时间沉默 |
| `voice_max_response_tokens` | `400` | 语音回复的单次输出 token 上限（需 `>= 0`） |

## `[model]`

| 字段 | 说明 |
| --- | --- |
| `provider` | `openai`、`openrouter`、`ollama`、`fake` |
| `model` | 模型名；`fake` 之外通常必需 |
| `base_url` | 自定义 OpenAI-compatible endpoint |
| `api_key` | 直接填写 API key |
| `token_env` | 从指定环境变量读取 API key；仅 `[model]` 支持 |
| `temperature` | 采样温度 |
| `max_tokens` | 最大输出 token |

## `[proxy]`

可选。用于 Agent 发起的外部 HTTP 请求（OpenAI-compatible / OpenRouter / Ollama 模型请求、OpenAI Whisper STT、Minimax TTS）。留空时使用进程环境变量中的 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`。

| 字段 | 说明 |
| --- | --- |
| `http_proxy` | HTTP 请求代理，例如 `http://127.0.0.1:7890` |
| `https_proxy` | HTTPS 请求代理，通常也填写 HTTP 代理地址，例如 `http://127.0.0.1:7890` |
| `all_proxy` | HTTP/HTTPS 未分别配置时使用的通用代理，支持 `http://`、`https://`、`socks5://` |
| `no_proxy` | 逗号/空格分隔的直连规则；支持主机名、域名后缀、`host:port`、CIDR 和 `*` |

## `[audio]`

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `socket` | `/run/audio_service/audio_service.sock` | Audio Service socket |
| `sample_rate` | `16000` | 采样率 |
| `channels` | `1` | 声道数 |
| `bit_width` | `16` | 位宽 |

## `[hid]`

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `keyboard_device` | `/dev/hidg0` | 键盘 HID 设备 |
| `mouse_device` | `/dev/hidg1` | 鼠标/触控 HID 设备 |
| `frame_socket` | `/run/frame_service/frame_service.sock` | 截图工具使用的 Frame Service socket |

## `[stt]` 和 `[tts]`

`input_mode = "stt"` 时需要 `[stt]`；`input_mode = "stt"` 或 `"audio"` 时需要 `[tts]`。

STT：

- `provider = "openai-whisper"`：当前可用；
- `provider = "openrouter"`：当前可用，默认 endpoint 为 `https://openrouter.ai/api/v1/audio/transcriptions`，请求体使用 base64 WAV；
- `provider = "tencent"`：字段已声明，Tencent ASR 仍属于待完善项。

TTS：

- `provider = "minimax"`：当前实现；
- 依赖 `ffmpeg` 将 MP3 转为 PCM 并流式写入 `audio_service`。

## 已知限制

- Web UI 模式和设备侧语音模式互斥；
- Tencent ASR 仍未完整实现；
- `preferred_model`、`allowed_children`、`model_text` 当前解析但未完全接入执行；
- 示例 skills 可能引用旧工具，生产使用前需检查。
