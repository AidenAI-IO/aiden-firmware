# Agent 配置参考

Agent 期望 `-config` 指向一个目录，而不是单个配置文件。本页同时覆盖设备上的 **Config Web 配置页面**（编辑这些字段最常用的方式）和 `agent.toml` 各字段的完整参考。

## 目录布局

```text
/userdata/agent/
├── agent.toml       # 必需
├── skills/          # 可选，自动发现 **/SKILL.md
├── log/             # 运行时日志目录
└── memory/          # 对话记忆持久化目录
```

TOML 是当前支持的配置格式；JSON 配置已废弃。

## Config Web: the device config page

`config_web` is a lightweight C++ web service for maintaining the device Agent configuration, system environment variables, and Wi-Fi configuration. It is the primary way to edit the fields documented on this page without manually editing `agent.toml`.

On a device, open the config page in a browser at the USB-network gateway address:

```text
http://192.168.42.1
```

The firmware starts `config_web` on port 80 (see the default command below).

### Default deployment

Init script:

```text
/etc/init.d/S56config_web
```

Default command:

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/config_web --bind=0.0.0.0 --port=80 --config=/userdata/agent/agent.toml --wifi-config=/userdata/wpa_supplicant.conf --system-env=/userdata/system/env
```

Common commands:

```bash
/etc/init.d/S56config_web start
/etc/init.d/S56config_web stop
/etc/init.d/S56config_web restart
```

### Parameters

Usage from the source:

```text
config_web [--bind=IP] [--port=PORT] [--config=PATH] [--wifi-config=PATH] [--system-env=PATH]
```

| Parameter | Description |
| --- | --- |
| `--bind=IP` | Bind address, default `0.0.0.0` |
| `--port=PORT` | Listen port |
| `--config=PATH` | Agent TOML config path |
| `--wifi-config=PATH` | `wpa_supplicant.conf` path |
| `--system-env=PATH` | System environment file path |

### What the page can configure

The page fields cover the following config sections (all detailed later on this page):

- `agent`: `input_mode`, `trigger_mode`, VAD params, `max_iterations`, `custom_instruction`, `additional_prompt`
- `model`: provider, token_env, model, api_key, base_url, temperature, max_response_tokens, context_window, model_max_output_tokens. `context_window = 0` means auto-discover from OpenRouter/Ollama metadata when available.
- `stt`: provider, api_key, model, base_url, Tencent ASR fields
- `tts`: provider, api_key, model, voice_id, emotion, speed
- `audio`: socket, sample_rate, channels, bit_width
- `hid`: keyboard_device, mouse_device, frame_socket
- `env`: shell-style environment text written to `/userdata/system/env`, including optional proxy variables such as `http_proxy`, `HTTPS_PROXY`, and `NO_PROXY`
- Wi-Fi: SSID / PSK etc. (written to `/userdata/wpa_supplicant.conf`)

### Runtime apply behavior

Config Web writes Agent, Wi-Fi, and system environment files. Saving Agent config still schedules an Agent restart. OTA updates read the effective `/userdata/system/env` file when `ota update` is run, so saving system environment no longer restarts OTA:

```bash
/etc/init.d/S53agent restart
```

## Web UI 最小配置

```toml
custom_instruction = ""
max_iterations = -1
screenshot_keep_n = 3
screenshot_prune_interval = 2
input_mode = "text"

[model]
provider = "openrouter"
model = "bytedance-seed/seed-2.0-lite"
token_env = "OPENROUTER_API_KEY"
temperature = 0.2
max_response_tokens = 1000
# Optional model metadata overrides. Leave unset or 0 for provider metadata auto-discovery when available.
# context_window = 128000
# model_max_output_tokens = 8192

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
custom_instruction = ""
input_mode = "stt"
trigger_mode = "manual"
vad_backend = "rknn"
vad_model_path = "/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn"
vad_helper_path = "/oem/usr/bin/rknn_vad"
vad_speech_threshold = 0.5
silence_ms = 650
min_speech_ms = 300
voice_followup_enabled = false
voice_followup_timeout_ms = 6000
voice_first_turn_timeout_ms = 10000
voice_max_turns = 0
voice_interrupt_on_wakeup = true
voice_streaming_tts_enabled = true
voice_tool_call_speech = true
voice_progress_speech_enabled = true
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
| `custom_instruction` | - | Optional deployment/persona override for the built-in runtime instruction. Leave empty to use the agent binary default; set only for internal testing or deployment-specific behavior. |
| `additional_prompt` | - | 额外 prompt 字段；运行时会追加到 base instruction 后面 |
| `max_iterations` | `-1` | 单次运行最大工具调用循环次数；`-1` 表示不限制 |
| `screenshot_keep_n` | `3` | LLM 上下文中截图裁剪的最近保留数量；未设置或 `0` 使用默认值 |
| `screenshot_prune_interval` | `2` | 截图超过 `screenshot_keep_n + screenshot_prune_interval` 后，按批次把旧截图替换为占位符；未设置或 `0` 使用默认值 |
| `input_mode` | `text` / `stt` / `audio` | 输入模式 |
| `trigger_mode` | `manual` / `wakeup` | 语音模式触发方式 |
| `vad_backend` | `rknn` | VAD 后端：`rknn` 使用 NPU encoder + CPU LSTM/decoder，`cpu` 使用纯 CPU helper |
| `vad_model_path` | `/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn` | Silero VAD RKNN encoder 模型路径；`vad_backend="cpu"` 时不使用 |
| `vad_helper_path` | `/oem/usr/bin/rknn_vad` | VAD helper 可执行文件路径；CPU 后端默认 `/oem/usr/bin/cpu_vad` |
| `vad_speech_threshold` | `0.5` | Silero VAD 语音概率阈值 |
| `silence_ms` | `650` | 多少毫秒静音后认为一句话结束 |
| `min_speech_ms` | `300` | 最短有效语音时长 |
| `voice_followup_enabled` | `false` | wakeup 模式下启用一次唤醒后的连续追问；默认保持一轮一唤醒 |
| `voice_followup_timeout_ms` | `6000` | Agent 回复后等待用户追问的窗口 |
| `voice_first_turn_timeout_ms` | `10000` | wakeup 后等待第一句话的窗口 |
| `voice_max_turns` | `0` | 单个 wakeup session 最大轮数；`0` 表示不限制 |
| `voice_interrupt_on_wakeup` | `true` | session 内再次收到 wakeup 时取消 thinking/TTS 并重新听音；监听或录音阶段的重复 wakeup 会被合并或忽略 |
| `voice_streaming_tts_enabled` | `true` | LLM 流式输出时按句送入 TTS，降低首句播放等待 |
| `voice_tool_call_speech` | `true` | 是否异步朗读 tool-call event 的 `content`；该内容只来自同一次 LLM tool-call 响应中的 assistant content，缺少时保持静默 |
| `voice_progress_speech_enabled` | `true` | 是否在 todo item 进入 `in_progress` 时播报短进度；todo 状态仍会发送给 UI/trace |
| `voice_max_response_tokens` | `400` | 语音回复的单次输出 token 上限（需 `>= 0`） |
| `todo_reminder_tool_calls` | `3` | single-agent/default mode 中连续多少次工具调用后提醒模型更新 todo；设为 `0` 使用默认值 |

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
| `max_response_tokens` | 请求时传给模型的最大输出 token |
| `context_window` | Optional total context window override in tokens. Unset or `0` uses provider metadata for OpenRouter/Ollama when available, then the built-in registry, then memory fallback. |
| `model_max_output_tokens` | Optional advertised max output override in tokens. Unset or `0` uses provider metadata when fetched, then the built-in registry. |

## `memory/extraction.yaml`

Optional. Place `memory/extraction.yaml` under the config directory to control session-memory compaction and chunk extraction. Missing files and invalid fields fall back to defaults. See [session-memory.md](./session-memory.md) for the full flow.

| Field | Default | Description |
| --- | --- | --- |
| `reserve_tokens` | `8192` | Token headroom reserved below the active model context window. Compaction triggers when `prompt_tokens >= context_window - reserve_tokens`. The value is clamped to at most half of the window so small-window models remain usable. |
| `keep_recent_tokens` | `20000` | Approximate token budget for the hot window retained by token-based cut-point selection. It is clamped together with `reserve_tokens` to fit the active window. |
| `hot_window_events` | `30` | Target number of recent events retained by the count fallback. Used only when prompt-token data is unavailable. |
| `count_compress_after_events` | `hot_window_events * 2` | Event-count trigger used only when prompt-token data is unavailable. If omitted, it is derived from the normalized `hot_window_events`; explicit values must be greater than `hot_window_events`. |
| `context_window` | `32000` | Fallback context window for compaction when the active model is not present in `model_specs`. Runtime normally derives this from `ModelResolver.Spec()`; this value is only used for unknown models. |
| `compress_at_percent` | `50` | Percentage trigger: compaction starts when `prompt_tokens / context_window >= compress_at_percent%`. |
| `summary_max_chunks` | `10` | Number of chunk summaries kept in the Recent Chunks section of `summary.md`. Older entries move to the archive and are folded into the Rolling Summary. |
| `session_boundary_enabled` | `true` | Classify each new user turn as continuing the current session or starting a new one. A `new` boundary archives the current `memory/session/` directory and recreates an empty active session. |
| `session_boundary_short_gap_seconds` | `300` | Gap below which a turn is treated as continuation regardless of lexical signals. |
| `session_boundary_long_gap_seconds` | `1800` | Gap above which a turn is treated as a fresh session regardless of lexical signals. |
| `tag_candidates` | see defaults | Candidate keywords matched when tagging chunk summaries. |
| `entity_suffixes` | `["App","app","APP"]` | Suffixes recognized during entity extraction. |

## System Environment Variables

The Agent no longer reads `[proxy]` from `agent.toml`. Outbound HTTP/WebSocket requests, shell tool subprocesses, OTA commands launched through `aiden-env-run`, and SSH login shells all use environment variables from `/userdata/system/env`. The file is loaded with shell syntax, for example:

```sh
HTTP_PROXY=http://127.0.0.1:7890
HTTPS_PROXY=http://127.0.0.1:7890
NO_PROXY=localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
OPENROUTER_API_KEY=...
```

| Variable | Description |
| --- | --- |
| `HTTP_PROXY` / `http_proxy` | HTTP proxy URL, for example `http://127.0.0.1:7890` |
| `HTTPS_PROXY` / `https_proxy` | HTTPS proxy URL, usually the same HTTP proxy endpoint |
| `ALL_PROXY` / `all_proxy` | Generic proxy used by HTTP clients and some WebSocket adapters |
| `NO_PROXY` / `no_proxy` | Comma-separated bypass rules; when a proxy URL is set and no bypass value is present, the launcher injects the default private-network bypass list |

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

## `[live_activity]`

用于 iOS companion app 的 Live Activity / 灵动岛任务状态。agent 侧状态快照默认启用；APNs 相关字段只针对 app 后台、锁屏或未打开时的远程更新。前台本地更新不需要也不会使用 APNs。完整链路见 [Live Activity / Dynamic Island](./live-activity.md)。

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `enabled` | `true` | 是否启用 agent 侧状态快照和 API |
| `bundle_id` | - | iOS app bundle id；仅配置后台 APNs 且未显式设置 `topic` 时必填 |
| `topic` | `<bundle_id>.push-type.liveactivity` | APNs topic；通常不需要手动设置 |
| `environment` | `sandbox` | `sandbox` 或 `production` |
| `team_id` | - | Apple Developer Team ID；仅后台 APNs 使用 |
| `key_id` | - | APNs Auth Key ID；仅后台 APNs 使用 |
| `private_key_path` | - | APNs `.p8` 私钥路径；仅后台 APNs 使用 |
| `private_key_pem` | - | 直接内联 APNs `.p8` PEM；仅开发联调使用，生产不要放到开源配置或用户板子 |
| `timeout_sec` | `10` | 后台 APNs 请求超时 |

## `[stt]` 和 `[tts]`

`input_mode = "stt"` 时需要 `[stt]`；`input_mode = "stt"` 或 `"audio"` 时需要 `[tts]`。

STT：

- `provider = "openai-whisper"`：当前可用；
- `provider = "openrouter"`：当前可用，默认 endpoint 为 `https://openrouter.ai/api/v1/audio/transcriptions`，请求体使用 base64 WAV；
- `provider = "tencent-asr"`：腾讯云一句话识别（SentenceRecognition），使用 `secret_id` / `secret_key`，无需 `base_url`；旧值 `tencent` / `tencent_asr` 仅作为兼容别名保留。

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

Fish Audio `model` defaults to `s2-pro` and is sent as a WebSocket handshake header. `voice_id` is also accepted as the reference id. If both `reference_id` and `voice_id` are set, `reference_id` wins. In some networks, the public Fish Audio endpoint may require `ALL_PROXY` or `HTTPS_PROXY` in `/userdata/system/env`.

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

## Episode 遥测（Langfuse）

可选。任务结束后将完整 episode 异步上报到 Langfuse，详见 [telemetry-langfuse.md](./telemetry-langfuse.md)。

```toml
[telemetry]
enabled = false
provider = "langfuse"
base_url = "http://langfuse.example.com:3000"
public_key = "pk-lf-..."
secret_key = "sk-lf-..."
upload_screenshots = true
upload_timeout_sec = 30
max_retry = 2
environment = "default"
tags = ["aiden-hardware"]
```

## 已知限制

- Web UI 模式和设备侧语音模式互斥；
- Tencent ASR 仍未完整实现；
- `preferred_model`、`allowed_children`、`model_text` 当前解析但未完全接入执行；
- Agent loop 已拆为 `planner`、`executor`、`verifier` 三个 RoleProfile；skill instructions 会进入各角色 profile，function tools 只暴露给 executor，工具目录会给其他角色做规划/复核参考；`verifier` 会按原始任务和完成条件验收；
- 示例 skills 可能引用旧工具，生产使用前需检查。
