---
sidebar_position: 1
---

# Agent: AI Assistant Service

`agent` is the core Go service that provides the Agent Web UI, HTTP APIs, and optional device-side voice interaction. It orchestrates model calls, tool execution (HID control, screenshots, audio playback), and conversation sessions.

## Default Parameters

| Parameter | Default Value |
| --- | --- |
| Config | `/userdata/agent/agent.toml` |
| Log | `/userdata/agent/log/agent.log` |
| Binary | `/oem/usr/bin/agent` |
| Session data | `/userdata/agent/` |

## Startup

```bash
/etc/init.d/S53agent start
/etc/init.d/S53agent status
/etc/init.d/S53agent restart
```

## Input Modes

The HTTP server and Web UI run in every input mode:

- **`input_mode = "text"`**: HTTP-based interaction only, mainly for testing and debugging.
- **`input_mode = "stt"`**: Runs the device audio loop in parallel with the HTTP server, using `audio_service`, VAD, the selected STT provider, the model, and the selected TTS provider.

## Configuration

The service reads `/userdata/agent/agent.toml`. Provider tables define named provider instances, and the corresponding selection table references one of those names:

```toml
input_mode = "stt"

[model_providers.openai-main]
type = "openai"
api_key = "$OPENAI_API_KEY"

[model]
provider = "openai-main"
model = "gpt-5.5"
# base_url = "https://api.openai.com/v1"
# temperature = 0.2
# max_response_tokens = 1000

[audio]
# socket = "/run/audio_service/audio_service.sock"
# playback_backend = "auto"

[hid]
# keyboard_device = "/dev/hidg0"
# mouse_device = "/dev/hidg1"
# android_keyboard_device = "/dev/hidg2"
# frame_socket = "/run/frame_service/frame_service.sock"

[stt_providers.openai-main]
type = "openai-whisper"
api_key = "$OPENAI_API_KEY"
model = "whisper-1"

[stt]
provider = "openai-main"

[tts_providers.minimax-main]
type = "minimax-cn"
api_key = "$MINIMAX_API_KEY"
voice_id = "male-qn-qingse"

[tts]
provider = "minimax-main"
speed = 1.0
```

Built-in model provider types include `openai`, `openrouter`, `kimi`, `kimi-cn`, `volcengine`, and `ollama`. There are no native `anthropic` or `gemini` provider types; Anthropic Claude and Google Gemini models can be selected through a registered compatible provider such as `openrouter`.
