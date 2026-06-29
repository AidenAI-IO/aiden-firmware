# Agent: AI Assistant Service

`agent` is the core Go service that provides AI interaction capabilities through voice and text modes. It orchestrates LLM calls, tool execution (HID control, screenshots, audio playback), and manages conversation sessions.

## Default Parameters

| Parameter | Default Value |
| --- | --- |
| Config | `/oem/usr/etc/agent/config.toml` |
| Log | `/var/log/agent/agent.log` |
| Binary | `/oem/usr/bin/agent` |
| Session data | `/userdata/agent/` |

## Startup

```bash
/etc/init.d/S57agent start
/etc/init.d/S57agent status
/etc/init.d/S57agent restart
```

## Modes

- **Voice mode**: Continuous voice interaction using audio_service for recording/playback and STT/TTS providers
- **Text mode**: HTTP-based text interaction for testing and debugging

## Configuration

Key sections in `config.toml`:

```toml
[llm]
provider = "anthropic"  # or "openai", "gemini"

[audio]
socket = "/run/audio_service/audio_service.sock"

[hid]
frame_socket = "/run/frame_service/frame_service.sock"

[stt]
provider = "tencent"  # or "openai"
