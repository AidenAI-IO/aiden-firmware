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
- Applying saved settings, with an automatic Agent restart when the running Agent cannot accept the new configuration in place
- Saving a system-default, direct, or custom proxy policy with each Wi-Fi network; custom proxy credentials are never returned to the browser

The Wi-Fi dialog accepts `http://`, `https://`, and `socks5://` proxy URLs. A
saved policy is activated automatically when that SSID becomes current. The
Agent, OTA commands, managed subprocesses, and login shells continue to use
the fixed local address `127.0.0.1:18080`. That address accepts both HTTP and
SOCKS5, and the local URL scheme matches the selected upstream scheme. Switching
between HTTP and SOCKS5 restarts the Agent so its long-lived clients use the
matching protocol; switching between proxies of the same type does not. The
local SOCKS5 URL is rendered as `socks5h://` to resolve target hostnames through
the proxy; this does not change the SOCKS5 wire protocol.

`NO_PROXY` in the Wi-Fi dialog belongs only to that SSID's custom proxy. When
`Use system default` is selected, both the upstream proxy and `NO_PROXY` come
from `/userdata/system/env`; no Wi-Fi-specific `NO_PROXY` value is stored or
merged into the system setting.

The header switch saves only the top-level `locale` through
`PUT /api/config/locale`. The UI updates immediately and rolls back if
persistence fails. The last confirmed value is cached in `localStorage` for
first paint, but `GET /api/device/snapshot` remains authoritative. The Agent
restart creates a new context session when the locale-specific system prompt
changes; it does not rewrite the previous session. `locale` is intentionally
separate from `[stt].language`, which only configures speech recognition.
