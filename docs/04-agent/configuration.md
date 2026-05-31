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
provider = "minimax-ws"
model = "speech-2.8-hd"
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
| `voice_interrupt_on_wakeup` | `true` | session 内再次收到 wakeup 时取消 thinking/TTS 并重新听音；录音中 wakeup 会直接重启录音并丢弃已录音频 |
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

可选。用于 Agent 发起的外部 HTTP/WebSocket 请求（OpenAI-compatible / OpenRouter / Ollama 模型请求、OpenAI Whisper STT、TTS adapters）。留空时使用进程环境变量中的 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`。

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

- `provider = "minimax-ws"`：Minimax WebSocket；
- `provider = "fish-audio"`：Fish Audio WebSocket；
- `provider = "alicloud"`：阿里云 Qwen-TTS Realtime；
- `provider = "volcengine"`：火山引擎 WebSocket 双向流式 V3。当前仅支持新控制台 `X-Api-Key` 鉴权，`api_key` 对应 `X-Api-Key`，`model` 对应 `X-Api-Resource-Id`（默认 `seed-tts-2.0`），`voice_id` 对应 speaker。

`[tts]` 通用字段：

| 字段 | 说明 |
| --- | --- |
| `provider` | 必填。可选 `minimax-ws`、`fish-audio`、`alicloud`、`volcengine` |
| `api_key` | 必填。各 provider 的鉴权密钥；下面示例省略该字段，避免把密钥写入文档 |
| `model` | 可选。Minimax 模型名、Fish Audio model header、阿里云 Realtime 模型名、火山 `X-Api-Resource-Id` |
| `voice_id` | 可选。Minimax voice id、阿里云 voice、火山 speaker；Fish Audio 可用它作为 reference id |
| `reference_id` | 可选。Fish Audio reference id；填写后优先于 `voice_id` |
| `emotion` | 可选。Minimax emotion；火山会透传为 `audio_params.emotion`，需音色支持 |
| `speed` | 可选。语速，默认 `1.0`；不同 provider 支持范围以官方文档为准 |

以下配置示例只展示 adapter 行为相关的非密钥字段；实际运行时仍需要在设备配置中通过 `[tts]` 或 `[tts.credentials.<provider>]` 提供对应 `api_key`。

TTS adapter 常用配置：

| Provider | `model` 示例 | 音色/引用字段 | 说明 |
| --- | --- | --- | --- |
| `minimax-ws` | `speech-2.8-hd` | `voice_id = "male-qn-qingse"` | Minimax WebSocket；`emotion` 会透传给 Minimax |
| `fish-audio` | `s2-pro` | `reference_id = "98655a12fa944e26b274c535e5e03842"` | WebSocket live TTS；`model` 通过握手 header 发送，`reference_id` 优先于 `voice_id` |
| `alicloud` | `qwen3-tts-flash-realtime` | `voice_id = "Cherry"` | DashScope Realtime；adapter 输出 24 kHz PCM，采样率不同时会自动重采样 |
| `volcengine` | `seed-tts-2.0` | `voice_id = "zh_female_vv_uranus_bigtts"` | `model` 对应 `X-Api-Resource-Id`，`voice_id` 对应 speaker，二者必须匹配 |

Minimax WebSocket：

```toml
[tts]
provider = "minimax-ws"
model = "speech-2.8-hd"
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0
```

Fish Audio WebSocket：

```toml
[tts]
provider = "fish-audio"
model = "s2-pro"
reference_id = "98655a12fa944e26b274c535e5e03842"
speed = 1.0
```

Fish Audio `model` 默认是 `s2-pro`，会作为 WebSocket 握手 header 发送；也接受 `voice_id` 作为 reference id。如果同时设置 `reference_id` 和 `voice_id`，使用 `reference_id`。Fish Audio 的公网 endpoint 在部分网络环境可能需要在 `[proxy]` 配置 `all_proxy`。

阿里云 Qwen-TTS Realtime：

```toml
[tts]
provider = "alicloud"
model = "qwen3-tts-flash-realtime"
voice_id = "Cherry"
speed = 1.0
```

阿里云 adapter 使用 DashScope WebSocket Realtime endpoint，输出固定 24 kHz PCM；设备播放采样率不同时会自动重采样。

火山引擎 WebSocket 双向流式 V3：

```toml
[tts]
provider = "volcengine"
model = "seed-tts-2.0"
voice_id = "zh_female_vv_uranus_bigtts"
speed = 1.0
```

火山引擎 `api_key` 是新控制台的 `X-Api-Key`，`model` 是 `X-Api-Resource-Id`，`voice_id` 是 speaker。`voice_id` 必须和 `model` 对应资源匹配；不匹配时服务端会返回 `resource ID is mismatched with speaker related resource`。`seed-tts-2.0` 已验证可用音色示例为 `zh_female_vv_uranus_bigtts`。

运行时切换 provider：

```bash
curl -X POST http://<device-ip>:8080/api/settings/tts \
  -H 'Content-Type: application/json' \
  -d '{"provider":"volcengine","voice":"zh_female_vv_uranus_bigtts"}'
```

如果需要在同一份配置里保存多个 provider 的密钥，可以使用 per-provider credentials。运行时 POST 切换 provider 时会优先读取对应 credentials，再用请求 body 覆盖。

```toml
[tts]
provider = "minimax-ws"
model = "speech-2.8-hd"
voice_id = "male-qn-qingse"

[tts.credentials.fish-audio]
model = "s2-pro"
reference_id = "98655a12fa944e26b274c535e5e03842"

[tts.credentials.alicloud]
model = "qwen3-tts-flash-realtime"
voice_id = "Cherry"

[tts.credentials.volcengine]
model = "seed-tts-2.0"
voice_id = "zh_female_vv_uranus_bigtts"
```

## 已知限制

- Web UI 模式和设备侧语音模式互斥；
- Tencent ASR 仍未完整实现；
- `preferred_model`、`allowed_children`、`model_text` 当前解析但未完全接入执行；
- 示例 skills 可能引用旧工具，生产使用前需检查。
