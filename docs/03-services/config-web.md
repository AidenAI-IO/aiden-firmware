# Config Web: Web-based Configuration Interface

`config_web` is a lightweight HTTP service providing a web UI for configuring the Agent's LLM provider, API keys, STT/TTS settings, and other runtime parameters. Changes are persisted to `/userdata/agent/agent.toml` and can trigger Agent restart.

## Default Parameters

| Parameter | Default Value |
| --- | --- |
| Port | `80` |
| Config | `/userdata/agent/agent.toml` |
| Binary | `/oem/usr/bin/config_web` |

## Startup

```bash
/etc/init.d/S56config_web start
/etc/init.d/S56config_web status
/etc/init.d/S56config_web restart
```

## Access

Connect the device to your computer via USB-C. The device establishes a USB network at `192.168.42.1`. Visit:

```text
http://192.168.42.1
```

The web interface allows:
- Switching the device language between Simplified Chinese (`zh-CN`) and English (`en-US`); this also controls user-facing Agent responses and `<tts>` content
- Switching LLM providers (Anthropic, OpenAI, Gemini)
- Configuring API keys and model names
- Selecting STT/TTS providers
- Testing voice recognition and synthesis
- Restarting the Agent service

The header switch saves only the top-level `locale` through `PUT /api/config/locale`. The UI updates immediately and rolls back if persistence fails. The last confirmed value is cached in `localStorage` for first paint, but `GET /api/config` remains authoritative. `locale` is intentionally separate from `[stt].language`, which only configures speech recognition.
