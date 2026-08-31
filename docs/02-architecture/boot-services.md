---
sidebar_position: 3
---

# Boot Services and Runtime Layout

Debian uses systemd as PID 1. Product services are grouped by `aiden.target`;
ordering, restart policy, and failure propagation are declared in unit
relationships rather than filename order.

## Core Units

| Unit | Purpose |
| --- | --- |
| `aiden-slot-resolve.service` | Resolve A/B partition devices from the active slot |
| `oem.mount`, `userdata.mount`, `userdata-ota.mount` | Mount product data partitions |
| `aiden-rootfs-grow.service` | Grow first-boot ext4 filesystems to their partition size |
| `aiden-userdata-migrate.service` | Validate and migrate persistent Debian userdata |
| `aiden-machine-id.service` | Provision a stable machine identity |
| `aiden-oem-ldconfig.service` | Register libraries from the active OEM slot |
| `aiden-environment.service` | Generate the strict runtime environment |
| `aiden-media-modules.service` | Load media modules and prepare video device access |
| `aiden-wifi-driver.service` | Load AIC8800 Wi-Fi and Bluetooth firmware |
| `aiden-bluetooth-attach.service` | Attach the AIC8800 UART transport |
| `aiden-usb-gadget.service` | Create keyboard, pointer, Consumer Control, and ECM functions |
| `aiden-usb-dnsmasq.service` | Serve DHCP only on `usb0` |
| `aiden-usb-ecm-watchdog.service` | Recover confirmed stalled ECM sessions |
| `aiden-wlan-guard.service` | Apply bounded Wi-Fi recovery |
| `aiden-frame.service` | Run the HDMI frame service |
| `aiden-audio.service` | Run audio capture and playback |
| `aiden-ble.service` | Run BLE Wake and ANCS integration |
| `aiden-agent.service` | Run the Go Agent |
| `aiden-config-web.service` | Serve the local configuration portal |
| `aiden-ota-health-marker.service` | Aggregate required local health |
| `aiden-ota-health.service` | Process pending A/B OTA state |

Use systemd to inspect the current dependency graph and failures:

```bash
systemctl list-dependencies aiden.target
systemctl --failed
```

## Frame Service

Configuration: `/etc/aiden_frame_service.conf`

The unit starts `/usr/lib/aiden/aiden-frame-start`, which selects the HDMI
bridge, applies EDID and trigger policy, reads
`[frame_service].keep_streamon` from `/userdata/agent/agent.toml`, and
executes `/oem/usr/bin/frame_service`.

```bash
systemctl start aiden-frame.service
systemctl stop aiden-frame.service
systemctl restart aiden-frame.service
systemctl status aiden-frame.service --no-pager
```

The service retries indefinitely when HDMI is not ready. Its persistent output
is `/var/log/frame_service/frame_service.log`.

## Audio Service

Configuration: `/etc/aiden_audio_service.conf`

`aiden-audio.service` executes `/oem/usr/bin/audio_service` in the foreground
and lets systemd own restart and stop behavior.

```bash
systemctl restart aiden-audio.service
systemctl status aiden-audio.service --no-pager
```

## Agent

The Agent unit executes:

```bash
/oem/usr/bin/agent -dir /userdata/agent -addr 0.0.0.0:8080
```

Before startup, `aiden-python-prepare` validates and prepares the persistent
Python userbase. The unit loads `/run/aiden/system.env` and then fixes the pip
environment to `/userdata/agent/python`.

```text
/userdata/agent/
├── agent.toml
├── python/
├── skills/
├── log/
└── memory/
```

Agent output is persisted at `/userdata/agent/log/agent.log`.

## OTA

`aiden-ota-health-marker.service` waits for required local services and writes
a transaction-bound health result. `aiden-ota-health.service` then executes:

```bash
/oem/usr/bin/ota --config /userdata/debian/ota/config.json health
```

The persistent OTA partition is mounted at `/userdata/ota/` and contains state,
downloads, pending-boot data, and health markers. The immutable factory
repository and partition baseline live separately at
`/userdata/debian/ota/config.json`.

See [OTA Overview](../08-ota/README.md) for the full state machine.

## Config Web

The systemd unit executes:

```bash
/oem/usr/bin/config_web   --bind=0.0.0.0   --port=80   --config=/userdata/agent/agent.toml   --wifi-config=/userdata/debian/wifi/wpa_supplicant-wlan0.conf   --wifi-backend=systemd-networkd   --system-env=/userdata/system/env   --web-root=/oem/usr/share/aiden/config-web
```

Config Web uses Debian control helpers for Agent and frame-service restarts.
It never invokes a SysV service script.

## System Environment

`/userdata/system/env` is the persistent user-managed source.
`aiden-environment.service` validates it and writes `/run/aiden/system.env`.
Product units consume that generated file with `EnvironmentFile=`; invalid
external settings cannot replace fixed service-critical values such as the
managed Python userbase.

## Development and Debugging

- Replace a binary only after stopping its owning unit.
- Use `journalctl -u <unit>` for unit and helper failures.
- Use the service-specific persistent log for application output.
- Stop `aiden-frame.service` before opening `/dev/video0` directly.
- Rebuild the complete image when rootfs helpers or unit files change.
