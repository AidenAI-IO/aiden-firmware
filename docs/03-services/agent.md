# Agent: AI Assistant Service

`agent` is the core Go service that provides AI interaction capabilities through voice and text modes. It orchestrates LLM calls, tool execution (HID control, screenshots, audio playback), and manages conversation sessions.

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

## Modes

- **Voice mode**: Continuous voice interaction using audio_service for recording, the configured TTS playback backend for speech, and STT/TTS providers
- **Text mode**: HTTP-based text interaction for testing and debugging

## Configuration

Key sections in `config.toml`:

```toml
[llm]
provider = "anthropic"  # or "openai", "gemini"

[audio]
socket = "/run/audio_service/audio_service.sock"
playback_backend = "auto"

[hid]
frame_socket = "/run/frame_service/frame_service.sock"

[stt]
provider = "tencent"  # or "openai"
