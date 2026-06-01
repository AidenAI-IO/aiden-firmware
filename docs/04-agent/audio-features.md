# 语音能力：VAD / STT / TTS

Go Agent 支持设备侧语音交互，主要由 `internal/agent/audio_client.go`、`audio_dialog.go`、`vad.go`、`stt.go` 和 `tts/` provider 组成。

## 架构

```text
┌─────────────┐
│   daemon    │
└──────┬──────┘
       │
       ├─ input_mode=text ──────► HTTP Server / Web UI
       │
       └─ input_mode=stt/audio ─► Audio Dialog Loop
                                   │
                                   ├─ AudioServiceClient
                                   ├─ VAD
                                   ├─ STT（stt 模式）
                                   ├─ LLM
                                   └─ TTS
```

## 组件

| 组件 | 文件 | 说明 |
| --- | --- | --- |
| Audio client | `audio_client.go` | 连接 `audio_service`，启动录音/播放 session，读写 PCM chunk |
| VAD | `vad.go` + `/oem/usr/bin/rknn_vad` 或 `/oem/usr/bin/cpu_vad` | Silero VAD 推理；输入固定为 16 kHz、512 samples/32 ms，并在 helper 中维护 `state` 状态 |
| STT | `stt.go` | OpenAI Whisper / OpenRouter 实现；Tencent ASR 字段已预留 |
| TTS | `tts/`、`tts_helpers.go` | 可插拔 TTS provider，输出 PCM 后通过 `audio_service` 播放；必要时自动重采样到设备播放采样率 |
| Dialog manager | `audio_dialog.go` | 编排录音、VAD、STT/LLM/TTS 流程 |

## 输入模式

### `input_mode = "text"`

启动 HTTP server 和 Web UI。浏览器可以上传/录音：

- 若 `[stt]` 已配置，浏览器音频会先转写为文本；
- 否则音频作为模型附件传递。

### `input_mode = "stt"`

设备侧音频 loop：

1. 通过 `audio_service` 录 PCM；
2. VAD 判断一句话的边界；
3. 将 WAV 发给 STT；
4. STT 文本送给 Agent runtime；
5. TTS 合成回复并通过 `audio_service` 播放。

### `input_mode = "audio"`

设备侧录音后直接作为 audio attachment 送给模型，再 TTS 回复。仅在所选 provider/model path 支持音频输入时使用。

## 触发方式

### `trigger_mode = "manual"`

按 Enter 开始录音，再按 Enter 停止，或由 VAD 判断静音结束。

### `trigger_mode = "wakeup"`

等待 GPIO 33 或 GPIO 32 falling edge 触发后录音，两路触发进入同一套 wakeup 流程。`input_mode = "stt"` 且 `voice_session_enabled = true` 时，wakeup 默认打开一个连续语音 session：首轮仍需要 GPIO，Agent 回复后会在 `voice_followup_timeout_ms` 窗口内继续听追问，`voice_first_turn_timeout_ms` 控制首轮等待窗口，`voice_max_turns` 控制单个 session 的轮数上限。录音中再次触发 wakeup 会打断当前录音、丢弃已录音频，并重新开始录音计时；session 内再次触发 wakeup 是否取消当前 LLM 请求由 `voice_interrupt_on_wakeup` 控制；TTS 播放期间默认不开麦，播放结束后才继续录音；播放中再次触发 wakeup 会打断当前轮并立即开始录音。需要 Linux GPIO sysfs 可用，并完成硬件连线。

## 配置片段

```toml
input_mode = "stt"
trigger_mode = "wakeup"
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

[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 16000
channels = 1
bit_width = 16

[stt]
provider = "openai-whisper"
api_key = "sk-..."
model = "whisper-1"

# OpenRouter alternative:
# provider = "openrouter"
# api_key = "OPENROUTER_API_KEY"
# model = "qwen/qwen3-asr-flash-2026-02-10"

[tts]
provider = "alicloud"
model = "qwen3-tts-flash-realtime"
voice_id = "Cherry"
emotion = "happy"
speed = 1.0
```

## TTS provider 使用方式

`[tts]` 的通用字段是 `provider`、`api_key`、`model`、`voice_id`、`emotion`、`speed` 和 `reference_id`。不同 provider 对字段的解释不同，完整说明见 [Agent 配置参考](configuration.md#stt-和-tts)。以下示例省略 `api_key`，只展示 adapter 行为相关配置。

所有 TTS provider 都通过统一的 streaming session 调用：Agent 把 LLM 输出片段写入 adapter，adapter 再决定何时向后端发送。Fish Audio、阿里云和火山引擎是真流式 WebSocket 链路；Minimax WebSocket adapter 会在内部按句子边界缓冲后发送，上层不需要区分“真流式”或“句子级流式”。运行时可通过 `POST /api/settings/tts` 切换 provider，已开始的播放会继续使用旧 provider，后续请求使用新 provider。

```toml
# Minimax WebSocket
[tts]
provider = "minimax-ws"
model = "speech-2.8-hd"
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0
```

```toml
# Fish Audio WebSocket
[tts]
provider = "fish-audio"
model = "s2-pro"
reference_id = "98655a12fa944e26b274c535e5e03842"
speed = 1.0
```

```toml
# 阿里云 Qwen-TTS Realtime
[tts]
provider = "alicloud"
model = "qwen3-tts-flash-realtime"
voice_id = "Cherry"
speed = 1.0
```

```toml
# 火山引擎 WebSocket 双向流式 V3
[tts]
provider = "volcengine"
model = "seed-tts-2.0"
voice_id = "zh_female_vv_uranus_bigtts"
speed = 1.0
```

## 依赖

- `audio_service` 必须运行；
- TTS adapter 直接输出 PCM 并写入 `audio_service`；
- `rknn_vad` / `cpu_vad` helper 必须可执行；`vad_backend="rknn"` 时 `vad_model_path` 指向已转换好的 encoder RKNN，`vad_backend="cpu"` 时 helper 默认切到 `/oem/usr/bin/cpu_vad`；
- STT/TTS 需要外部 API key；
- `wakeup` 模式需要 GPIO 33 或 GPIO 32 硬件触发条件。

可在板端直接验证 VAD helper：

```bash
/oem/usr/bin/rknn_vad --model /userdata/agent/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn --weights /userdata/agent/model/silero_vad_6_2_lstm_decoder_weights.bin --self-test
/oem/usr/bin/cpu_vad --weights /userdata/agent/model/silero_vad_6_2_lstm_decoder_weights.bin --self-test
```

成功时会输出一行 `P <probability>`；如果仍有 RKNN 输入配置问题，会直接输出 `ERR ...`。

## 已知限制

- Web UI 和设备语音 loop 当前不能在同一 daemon 实例同时运行；
- Tencent ASR 尚未完整实现；
- Direct audio 模式依赖模型/provider 支持音频输入。
