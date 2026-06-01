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
instruction = "回答要简洁、自然、有帮助。默认用简体中文回答；用户明确使用其他语言时跟随用户语言。Aiden 通常用于控制连接的手机或移动 UI，但必须根据截图、工具结果和用户输入推断当前可见目标，不要假设。"
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
sample_rate = 16000
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
instruction = "回答要简洁、自然、有帮助。默认用简体中文回答；用户明确使用其他语言时跟随用户语言。Aiden 通常用于控制连接的手机或移动 UI，但必须根据截图、工具结果和用户输入推断当前可见目标，不要假设。"
input_mode = "stt"
trigger_mode = "manual"
vad_backend = "rknn"
vad_model_path = "/userdata/agent/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn"
vad_helper_path = "/oem/usr/bin/rknn_vad"
vad_speech_threshold = 0.5
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
| `instruction` | - | Agent deployment/persona instruction；默认只放简短语气、语言和目标设备软默认。工具边界、环境感知、UI 操作约束和 skill 策略由运行时默认 prompt 提供，避免在配置中重复长规则 |
| `additional_prompt` | - | 额外 prompt 字段；运行时会追加到 `instruction` 后面 |
| `max_iterations` | `-1` | 单次运行最大工具调用循环次数；`-1` 表示不限制 |
| `input_mode` | `text` / `stt` / `audio` | 输入模式 |
| `trigger_mode` | `manual` / `wakeup` | 语音模式触发方式 |
| `vad_backend` | `rknn` | VAD 后端：`rknn` 使用 NPU encoder + CPU LSTM/decoder，`cpu` 使用纯 CPU helper |
| `vad_model_path` | `/userdata/agent/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn` | Silero VAD RKNN encoder 模型路径；`vad_backend="cpu"` 时不使用 |
| `vad_helper_path` | `/oem/usr/bin/rknn_vad` | VAD helper 可执行文件路径；CPU 后端默认 `/oem/usr/bin/cpu_vad` |
| `vad_speech_threshold` | `0.5` | Silero VAD 语音概率阈值 |
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

`vad_model_path` 指向的模型需要先在 PC 端用 `silero-vad/convert_silero_vad_to_rknn.py` 从 Silero ONNX 转成 RV1106 RKNN，再放到设备对应路径。CPU 后端需要 `silero_vad_6_2_lstm_decoder_weights.bin` 包含 Conv1d encoder 扩展，可用 `silero-vad/export_silero_vad_v6_2_weights.py` 从随仓库提供的 TorchScript 文件生成。
当 `vad_helper_path` 仍是内置默认值时，切换 `vad_backend` 会自动切换 helper；只有设置成自定义路径时才按自定义路径执行。

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

可选。用于 Agent 发起的外部 HTTP 请求（OpenAI-compatible / OpenRouter / Ollama 模型请求、OpenAI Whisper STT、Minimax TTS），并会注入到 Agent `shell` 工具启动的子进程环境中。所有字段留空时使用进程环境变量中的代理设置。

| 字段 | 说明 |
| --- | --- |
| `http_proxy` | HTTP 请求代理，例如 `http://127.0.0.1:7890` |
| `https_proxy` | HTTPS 请求代理，通常也填写 HTTP 代理地址，例如 `http://127.0.0.1:7890` |
| `all_proxy` | HTTP/HTTPS 未分别配置时使用的通用代理，支持 `http://`、`https://`、`socks5://` |
| `no_proxy` | 逗号/空格分隔的直连规则；支持主机名、域名后缀、`host:port`、CIDR 和 `*`；当显式配置了 `http_proxy` / `https_proxy` / `all_proxy` 且本字段留空时，默认 `localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16` |

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
