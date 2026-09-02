---
sidebar_position: 2
---

# Config Web: Web-based Configuration Interface

Config Web is the bundled browser client served by the `config-web` subcommand
of the Go Agent binary (`/oem/usr/bin/agent`). Device operations are exposed by
the independently mountable [Device Management API](device-management-api.md),
so the page can be replaced or removed without coupling those operations to the
static UI. Configuration changes are persisted to
`/userdata/agent/agent.toml` and can trigger Agent restart.

## Default Parameters

| Parameter | Default Value |
| --- | --- |
| Port | `80` |
| Config | `/userdata/agent/agent.toml` |
| Binary | `/oem/usr/bin/agent config-web` |

## Startup

```bash
/etc/init.d/S56config_web start
/etc/init.d/S56config_web stop
/etc/init.d/S56config_web restart
/etc/init.d/S56config_web reload
```

The init script supports `start`, `stop`, `restart`, and `reload`. It does not
provide a `status` command; `reload` currently performs the same stop-and-start
sequence as `restart`.

## Access

Connect the device to your computer via USB-C. The device establishes a USB network at `192.168.42.1`. Visit:

```text
http://192.168.42.1
```

The web interface allows:
- Opening the browser terminal exposed by ttyd at `http://192.168.42.1:3000/webtty/`
- Switching the device language between Simplified Chinese (`zh-CN`) and English (`en-US`); this also controls user-facing Agent responses and `<tts>` content
- Switching among registered model provider types such as OpenAI, Anthropic, OpenRouter, Kimi, Volcengine, and Ollama; Google Gemini models are available through compatible providers such as OpenRouter
- Configuring API keys and model names
- Selecting STT/TTS providers
- Testing voice recognition and synthesis
- Restarting the Agent service

The header switch saves only the top-level `locale` through
`PUT /api/config/locale`. The UI updates immediately and rolls back if
persistence fails. The last confirmed value is cached in `localStorage` for
first paint, but `GET /api/device/snapshot` remains authoritative. The Agent
restart creates a new context session when the locale-specific system prompt
changes; it does not rewrite the previous session. `locale` is intentionally
separate from `[stt].language`, which only configures speech recognition.
