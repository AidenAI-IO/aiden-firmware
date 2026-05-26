# 语音能力：VAD / STT / TTS

Go Agent 支持设备侧语音交互，主要由 `internal/agent/audio_client.go`、`audio_dialog.go`、`vad.go`、`stt.go`、`tts.go` 组成。

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
| VAD | `vad.go` + `/oem/usr/bin/rknn_vad` | Silero VAD RKNN 推理；输入固定为 16 kHz、512 samples/32 ms，并在 helper 中维护 `state` 状态 |
| STT | `stt.go` | OpenAI Whisper / OpenRouter 实现；Tencent ASR 字段已预留 |
| TTS | `tts.go` | Minimax TTS，使用 `ffmpeg` 转 PCM 后播放 |
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
vad_model_path = "/userdata/agent/silero_vad_rv1106.rknn"
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
provider = "minimax"
api_key = "..."
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0
```

## 依赖

- `audio_service` 必须运行；
- `rknn_vad` helper 必须可执行，`vad_model_path` 指向已转换好的 `silero_vad_rv1106.rknn`；可用 `scripts/convert_silero_vad_to_rknn.py --onnx silero_vad.onnx --output silero_vad_rv1106.rknn` 生成模型；
- TTS 需要 `ffmpeg` 完成 MP3 → PCM 转换；
- STT/TTS 需要外部 API key；
- `wakeup` 模式需要 GPIO 33 或 GPIO 32 硬件触发条件。

可在板端直接验证 RKNN VAD helper：

```bash
/oem/usr/bin/rknn_vad --model /userdata/agent/silero_vad_rv1106.rknn --self-test
```

成功时会输出一行 `P <probability>`；如果仍有 RKNN 输入配置问题，会直接输出 `ERR ...`。

## 已知限制

- Web UI 和设备语音 loop 当前不能在同一 daemon 实例同时运行；
- Tencent ASR 尚未完整实现；
- Direct audio 模式依赖模型/provider 支持音频输入。
