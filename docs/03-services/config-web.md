---
sidebar_position: 2
---

# Config Web: Web-based Configuration Interface

Config Web is the bundled browser client served by the `config-web` subcommand
of the Go Agent binary (`/oem/usr/bin/agent`). Device operations are exposed by
the independently mountable [Device Management API](device-management-api.md),
so the page can be replaced or removed without coupling those operations to the
static UI. Configuration changes are persisted to
`/userdata/agent/agent.toml` and applied online. The page shows whether settings
are pending, applied, failed, or waiting for a device reboot.

Model discovery, STT configuration tests, storage operations, and all other
device-management calls are served directly by Config Web on port `80`. The
page does not construct cross-port Agent URLs, so Docker host-port overrides
and USB-ECM access use the same API behavior.

## Default Parameters

| Parameter | Default Value |
| --- | --- |
| Port | `80` |
| Config | `/userdata/agent/agent.toml` |
| Binary | `/oem/usr/bin/agent config-web` |
| Web root | `/oem/usr/share/aiden/config-web` |

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
- Applying saved settings online, retrying failures, and explicitly rebooting only when USB settings require it

The header switch saves only the top-level `locale` through
`PUT /api/config/locale`. The UI updates immediately and rolls back if
persistence fails. The last confirmed value is cached in `localStorage` for
first paint, but `GET /api/device/snapshot` remains authoritative. The next Agent task
creates a new context session when the locale-specific system prompt changes; it does not rewrite the previous session. `locale` is intentionally
separate from `[stt].language`, which only configures speech recognition.

## Configuration Application Policy

| Category changed by this implementation | Application boundary |
| --- | --- |
| Ordinary settings: locale/prompts, iteration and context limits, screenshot retention, telemetry, log retention, notifications, Live Activity, quick-capture TTL | Published online for subsequent tasks/operations; shared managers keep their identity |
| Components: input mode, selected models/provider credentials, STT/TTS/realtime voice, audio/VAD, HID clients, capture backend, search, quick-capture GPIO, storage policies | Drain affected work, prepare replacements, and switch components without restarting Agent; HTTP and Phone Bridge remain alive |
| `frame_service.keep_streamon` | Restart only `frame_service`, check its exit status, then queue the Agent snapshot; capture is briefly unavailable |

Existing exceptions remain: Android/non-Android USB pointer descriptor changes
and `hid.keyboard_layout` need a device reboot. Other device-type changes can
apply online. Updating system environment variables restarts Agent. Deployment
of a new binary still requires restarting Agent and Config Web; that is separate
from changing runtime settings.

Voice/model changes wait for the current voice session. Failed replacements
retain the previous runtime configuration and drained dialog. A held drag must
be released before HID component replacement can succeed. Old model requests
and TTS sessions retain their current provider until completion. Prompt/provider
changes open a new context session at the next task/session boundary and leave
existing transcript files intact.
