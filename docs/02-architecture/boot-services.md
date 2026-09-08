---
sidebar_position: 3
---

# Boot Services and Runtime Layout

Firmware integration is done through scripts in `overlay/etc/init.d/`. Most long-running services come with a lightweight watchdog: the script itself starts a loop that waits 2 seconds and restarts the child process after it exits.

## Boot Scripts

| Script | Purpose |
| --- | --- |
| `S20oemslot` | Mount the slot-specific `/oem` based on `aiden.slot_suffix` |
| `S43wlan_guard` | WLAN connectivity guard |
| `S49ntp` | Start the `ntpd` daemon; periodic sync is triggered by `S50ntp_watchdog` |
| `S49usbhid` | Initialize the USB HID gadget |
| `S50ntp_watchdog` | Periodically check clock sync status, and trigger `S49ntp step` when not synced |
| `S50usbdevice` | USB device related initialization |
| `S51wifi_proxy` | Start the fixed loopback proxy and select an upstream from the active Wi-Fi SSID |
| `S52frame_service` | Start and supervise the HDMI frame service |
| `S53adb_server` | Wait 3 seconds, then run `adb start-server` once |
| `S53audio_service` | Start and supervise the audio service |
| `S53agent` | Start and supervise the Go Agent |
| `S54ota` | Run the OTA health handling once at boot |
| `S55aiden_usb_dhcp` | USB network DHCP / dnsmasq related services |
| `S56config_web` | Start the config web page |
| `S57ttyd` | Start the ttyd browser terminal on the USB network |
| `S99rtcinit` | Override the SDK default RTC script; write the default date when the RTC is abnormal and the system time is still earlier than the baseline |
| `S99usb0config` | USB network interface post-configuration |

## Frame Service

Config file: `/etc/aiden_frame_service.conf`

Default values:

```sh
FRAME_SERVICE_BIN=/oem/usr/bin/frame_service
SOCKET_PATH=/run/frame_service/frame_service.sock
LOG_PATH=/var/log/frame_service/frame_service.log
PID_FILE=/run/frame_service/frame_service.pid
WATCHDOG_PID_FILE=/run/frame_service/frame_service_watchdog.pid
```

Common commands:

```bash
/etc/init.d/S52frame_service start
/etc/init.d/S52frame_service stop
/etc/init.d/S52frame_service restart
/etc/init.d/S52frame_service status
```

## Audio Service

Config file: `/etc/aiden_audio_service.conf`

Default values:

```sh
AUDIO_SERVICE_BIN=/oem/usr/bin/audio_service
SOCKET_PATH=/run/audio_service/audio_service.sock
LOG_PATH=/var/log/audio_service/audio_service.log
PID_FILE=/run/audio_service/audio_service.pid
WATCHDOG_PID_FILE=/run/audio_service/audio_service_watchdog.pid
```

## Agent

Default startup command:

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/agent -dir /userdata/agent -addr :8080
```

Runtime directory:

```text
/userdata/agent/
├── agent.toml
├── skills/       # optional
├── log/          # Agent runtime log directory, used by the program
└── memory/       # conversation memory persistence directory
```

Agent init script and runtime output: `/userdata/agent/log/agent.log`.

## Wi-Fi Proxy

`S51wifi_proxy` starts `agent wifi-proxy` on `127.0.0.1:18080` before the Agent.
Managed services and login shells use that loopback address by default. Setting
`AIDEN_WIFI_PROXY_ENABLED=0` restores direct use of the raw environment proxy
after the affected services or shell restart. The proxy reloads
`/userdata/system/wifi-proxies.json` and the current `wlan0` SSID every two
seconds. Each saved network can use the system upstream from
`/userdata/system/env`, connect directly, or use its own HTTP, HTTPS, or SOCKS5
proxy. Switching Wi-Fi does not require restarting the Agent unless the selected
proxy protocol changes between HTTP and SOCKS5.

## OTA

Default startup command:

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/ota health
```

Runtime directory:

```text
/userdata/ota/
├── config.json          # repo/channel/factory baseline built into the first-flashed image
├── state.json           # OTA state machine and per-slot partition hashes
├── downloads/           # download cache
├── pending_boot.json    # target slot health pending status
└── health.ok            # health confirmation marker written after the app is ready
```

`/userdata/ota` is a dedicated 300 MiB ext4 partition mounted by the
SDK-generated `S20linkmount` service. `S54ota` fails closed until that mount is
active.

For more details see [OTA Overview](../08-ota/README.md).

## Config Web

Default startup command:

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/agent config-web --bind=0.0.0.0 --port=80 --config=/userdata/agent/agent.toml --wifi-config=/userdata/wpa_supplicant.conf --system-env=/userdata/system/env
```

Purpose: maintain the Agent configuration and Wi-Fi configuration through a web page. For the default bind / port see [Config Web](../04-agent/configuration.md#config-web-the-device-config-page).

## System Environment

`/userdata/system/env` is the device-wide environment file. `aiden-env-run` loads it before starting services, and SSH login shells load the same file through `/etc/profile.d/aiden-env.sh`. Proxy variables in this file define the system/default upstream used by the Wi-Fi proxy; managed programs receive the stable loopback proxy address. API keys and other shell-style environment values can live there instead of in `agent.toml`.

## Development and Debugging Tips

- After modifying a binary, you can directly replace `/oem/usr/bin/<name>` and restart the corresponding service.
- If a service keeps restarting, first check `/var/log/<service>/<service>.log`.
- If you need exclusive access to the camera for a one-shot test, stop `S52frame_service` first.
