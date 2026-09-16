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
| `aiden-wifi-proxy.service` | Run the fixed loopback proxy and select a per-Wi-Fi upstream |
| `aiden-agent.service` | Run the Go Agent |
| `aiden-config-web.service` | Serve the local configuration portal |
| `aiden-ttyd.service` | Serve the ttyd browser terminal on the USB network |
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
`[advanced_settings.hardware.frame_service].keep_streamon` from
`/userdata/agent/agent.toml`, and
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

## Wi-Fi Connectivity and Recovery

`aiden-wifi-driver.service` loads `aic8800_fdrv.ko` with
`he_on=${AIDEN_WIFI_HE:-0}`. The current AIC8800DC driver/firmware can remain
associated and renew DHCP while IPv4 traffic stalls in HE mode with some APs.
The compatibility default disables Wi-Fi 6 HE and leaves HT/VHT enabled.
This restores a pre-existing Buildroot fix: commit
[`2b08d9a9`](https://github.com/AidenAI-IO/aiden-firmware/commit/2b08d9a945846252c3e0d9760187663176cfff57)
(2026-06-11, #177) passed `he_on=0` at both AIC8800 load points in
the retired
Buildroot Wi-Fi loader (`insmod_wifi.sh`). That override was still present in
`9ff24ababfc672ed16711c8b49b21431e685c9b1`, and the Buildroot packaging script
copied it over the SDK loader. The Debian migration
[`4993a135`](https://github.com/AidenAI-IO/aiden-firmware/commit/4993a1354d44922dd79a07b5464c440e8b78ad24)
(#634) introduced this separate loader without the parameter, re-enabling the
driver's default HE mode. The subsequent removal of the legacy overlay did
not cause the regression: Debian already used the new loader.

Both repository versions pin `pico-sdk` at
`d1a279cbb7e29aa0801943cdf21f0575db69eed5`. All 21 files in the affected board's
`/oem/usr/ko/aic8800dc_fw` matched that SDK firmware directory by SHA-256 during
the 2026-09-15 investigation. This establishes the missing load parameter as
the concrete migration regression; it does not assert that separately built
kernel-module binaries are byte-identical.

Set `AIDEN_WIFI_HE=1` in `/etc/aiden_boot.conf` to test HE with another firmware
or AP. The parameter applies on module load, so reboot to apply it; restarting
the driver service alone does not reload an already loaded module.

`aiden-wlan-guard.service` checks the Wi-Fi gateway every 10 seconds. It accepts
either an ICMP response or a fresh ARP reply, so an AP filtering ping does not
trigger repeated disconnects. After five failed checks it reassociates, then
cycles the interface and restarts supplicant if needed. DHCP remains owned by
systemd-networkd. The guard uses `Wants=` for supplicant so restarting supplicant
does not terminate its own recovery sequence.

Recovery is limited to three attempts until six consecutive healthy checks
restore the budget. Once exhausted, the guard keeps monitoring without
disconnecting the interface. `WLAN_GUARD_MAX_RECOVERIES` and
`WLAN_GUARD_HEALTHY_THRESHOLD` in `/etc/aiden_boot.conf` override those defaults;
restart the guard after changing them. Restarting the guard also resets its
budget. `iw ... set power_save off` is not used as a fix: that operation is a
no-op in the bundled driver's `rwnx_cfg80211_set_power_mgmt` implementation.

Use the USB connection while investigating wireless connectivity:

```bash
ssh aiden@192.168.42.1
sudo journalctl -u aiden-wlan-guard -u wpa_supplicant@wlan0 --since '-10 min'
sudo ip neigh show dev wlan0
sudo ping -c 10 -W 1 -I wlan0 <wifi-gateway>
sudo arping -c 3 -w 4 -I wlan0 <wifi-gateway>
```

Validate the wireless path with repeated SSH connections from a LAN client,
including after an idle period; successful DHCP and `wpa_state=COMPLETED` alone
do not establish that unicast traffic works.

On the AidenIOT AP (2.4 GHz, channel 6) on 2026-09-15, the original HE mode
reproduced IPv4/ARP failure even with the guard stopped and `ps_on=0`. Loading
with `he_on=0` restored LAN SSH and gateway traffic, including with the default
`ps_on=1`. This is a compatibility mitigation, not proof of which side of the
driver/firmware/AP interaction is defective. Restoring the old parameter on
Debian passed 12 consecutive LAN SSH connections after deployment. The
original #177 commit also records an HE-only comparison (0/30 ICMP replies
with HE enabled, 374/374 with HE disabled) and a successful reboot check;
those are historical results, not new reboot validation of this patch.

## Wi-Fi Proxy

`aiden-wifi-proxy.service` starts `agent wifi-proxy` on `127.0.0.1:18080`
before the Agent.
Managed services and login shells use that loopback address by default. Setting
`AIDEN_WIFI_PROXY_ENABLED=0` restores direct use of the raw environment proxy
after the affected services or shell restart. The proxy reloads
`/userdata/system/wifi-proxies.json` and the current `wlan0` SSID every two
seconds. Each saved network can use the system upstream from
`/userdata/system/env`, connect directly, or use its own HTTP, HTTPS, or SOCKS5
proxy. Switching Wi-Fi does not require restarting the Agent unless the
generated proxy environment changes, including a change to the selected proxy
protocol or the per-network `NO_PROXY` value.

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
/oem/usr/bin/agent config-web --bind=0.0.0.0 --port=80 --config=/userdata/agent/agent.toml --wifi-config=/userdata/debian/wifi/wpa_supplicant-wlan0.conf --wifi-interface=wlan0 --wifi-backend=systemd-networkd --system-env=/userdata/system/env --web-root=/oem/usr/share/aiden/config-web
```

Config Web uses Debian control helpers for Agent and frame-service restarts.
It never invokes a SysV service script.

## System Environment

`/userdata/system/env` is the persistent user-managed source.
`aiden-environment.service` validates it and writes `/run/aiden/system.env`.
Product units consume that generated file with `EnvironmentFile=`; invalid
external settings cannot replace fixed service-critical values such as the
managed Python userbase. Proxy variables in the persistent source define the
system/default upstream used by the Wi-Fi proxy; managed programs receive its
stable loopback proxy address. Login shells load the same validated runtime
environment through `/etc/profile.d/aiden-env.sh`.

## Development and Debugging

- Replace a binary only after stopping its owning unit.
- Use `journalctl -u <unit>` for unit and helper failures.
- Use the service-specific persistent log for application output.
- Stop `aiden-frame.service` before opening `/dev/video0` directly.
- Rebuild the complete image when rootfs helpers or unit files change.
