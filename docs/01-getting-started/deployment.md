---
sidebar_position: 6
---

# Deployment to Device

## Deploy via Complete Firmware

The recommended production deployment method is to build or download a complete firmware image with overlay and flash it to the device. The firmware will place the following content into the target filesystem:

```text
/oem/usr/bin/                  # Application binaries
/etc/init.d/S39hciinit         # AIC8800 UART/HCI initialization
/etc/init.d/S40bluetoothd      # BlueZ with persistent pairing state
/etc/init.d/S41ble_service     # BLE Wake/ANCS service watchdog
/etc/init.d/S43wlan_guard      # WLAN connectivity guard
/etc/init.d/S49ntp             # ntpd daemon
/etc/init.d/S49usbhid          # USB HID gadget initialization
/etc/init.d/S50ntp_watchdog    # NTP sync periodic check, triggers step when not synced
/etc/init.d/S52frame_service   # Frame Service watchdog
/etc/init.d/S53adb_server      # Delayed adb host server bootstrap
/etc/init.d/S53audio_service   # Audio Service watchdog
/etc/init.d/S53agent           # Go Agent watchdog
/etc/init.d/S56config_web      # Configuration web page
/etc/init.d/S99rtcinit         # RTC default time calibration, overrides SDK default script
/userdata/agent/agent.toml     # Agent default configuration
/userdata/wpa_supplicant.conf  # Wi-Fi default configuration
```

## Manual Binary Copy

After `./build.sh binaries` completes, the main artifacts are in `build/bin/`. The ones directly related to device-resident services are:

- `build/bin/frame_service`
- `build/bin/audio_service`
- `build/bin/ble_service`
- `build/bin/config_web`
- `build/bin/agent`
- `overlay/oem/usr/bin/aiden-env-run`

If the target device has already been flashed with an Aiden firmware version, the init scripts and `/etc/*.conf` typically already exist. In this case, the minimal deployment set is the above runtime files:

```bash
./build.sh binaries

scp build/bin/frame_service root@<device-ip>:/oem/usr/bin/
scp build/bin/audio_service root@<device-ip>:/oem/usr/bin/
scp build/bin/ble_service root@<device-ip>:/oem/usr/bin/
scp build/bin/config_web root@<device-ip>:/oem/usr/bin/
scp build/bin/agent root@<device-ip>:/oem/usr/bin/
scp overlay/oem/usr/bin/aiden-env-run root@<device-ip>:/oem/usr/bin/
```

If you also need device-side troubleshooting/debugging tools, optionally copy:

```bash
scp build/bin/frame_service_cli root@<device-ip>:/oem/usr/bin/
scp build/bin/audio_service_cli root@<device-ip>:/oem/usr/bin/
scp build/bin/ota root@<device-ip>:/oem/usr/bin/
scp build/bin/abctl root@<device-ip>:/oem/usr/bin/
```

If the target device is a more bare system lacking Aiden's init scripts and configuration files, you'll also need to copy these runtime files:

```bash
scp overlay/etc/init.d/S52frame_service root@<device-ip>:/etc/init.d/
scp overlay/etc/init.d/S53adb_server root@<device-ip>:/etc/init.d/
scp overlay/etc/init.d/S53audio_service root@<device-ip>:/etc/init.d/
scp overlay/etc/init.d/S39hciinit root@<device-ip>:/etc/init.d/
scp overlay/etc/init.d/S40bluetoothd root@<device-ip>:/etc/init.d/
scp overlay/etc/init.d/S41ble_service root@<device-ip>:/etc/init.d/
scp overlay/etc/init.d/S53agent root@<device-ip>:/etc/init.d/
scp overlay/etc/init.d/S56config_web root@<device-ip>:/etc/init.d/

scp overlay/etc/aiden_frame_service.conf root@<device-ip>:/etc/
scp overlay/etc/aiden_audio_service.conf root@<device-ip>:/etc/
scp overlay/etc/aiden_ble_service.conf root@<device-ip>:/etc/
scp overlay/etc/bluetooth/main.conf root@<device-ip>:/etc/bluetooth/
```

First deployment typically also requires preparing the default configuration under `/userdata`:

```bash
ssh root@<device-ip> "mkdir -p /userdata/agent /userdata/system"
scp overlay/userdata/agent/agent.toml root@<device-ip>:/userdata/agent/
scp overlay/userdata/system/env root@<device-ip>:/userdata/system/
scp overlay/userdata/wpa_supplicant.conf root@<device-ip>:/userdata/
```

Notes:

- Config Web calls the `/oem/usr/bin/agent` `config`, `config-check`, and `config-meta` subcommands, so copy `agent` whenever copying `config_web`.
- `S52frame_service`, `S53adb_server`, `S53audio_service`, `S53agent`, and `S56config_web` all launch the actual binaries or commands via `/oem/usr/bin/aiden-env-run` when available, so this wrapper must also be present on the device.
- You can also copy binaries to `/root` or `/userdata` for temporary testing, but existing init scripts default to searching `/oem/usr/bin/`.
- If only updating binaries, run `chmod +x /oem/usr/bin/*` once after copying.

After copying, common restart commands:

```bash
ssh root@<device-ip> "chmod +x /etc/init.d/S39hciinit /etc/init.d/S40bluetoothd /etc/init.d/S41ble_service /etc/init.d/S52frame_service /etc/init.d/S53adb_server /etc/init.d/S53audio_service /etc/init.d/S53agent /etc/init.d/S56config_web /oem/usr/bin/frame_service /oem/usr/bin/audio_service /oem/usr/bin/ble_service /oem/usr/bin/config_web /oem/usr/bin/agent /oem/usr/bin/aiden-env-run"
ssh root@<device-ip> "/etc/init.d/S52frame_service restart"
ssh root@<device-ip> "/etc/init.d/S53audio_service restart"
ssh root@<device-ip> "/etc/init.d/S53agent restart"
ssh root@<device-ip> "/etc/init.d/S56config_web restart"
```

## Service Startup Order

When starting with the firmware, the main service relationships are as follows:

1. `S35wifidrv` loads the AIC8800 combo firmware;
2. `S39hciinit` attaches `/dev/ttyS1` and brings up `hci0`;
3. `S40bluetoothd` starts BlueZ with its pairing state under `/userdata`;
4. `S41ble_service` registers the Wake GATT service and ANCS consumer;
5. `S43wlan_guard` monitors WLAN connectivity and recovers automatically;
6. `S49ntp` starts `ntpd` in daemon mode, using direct IP connection to NTP server (bypassing DNS startup order);
7. `S50ntp_watchdog` periodically checks clock sync status, triggers `S49ntp step` to force sync when not synced, exits after sync;
8. `S49usbhid` / `S50usbdevice` configures USB gadget;
9. `S52frame_service` exclusively uses `/dev/video0` and provides screenshot/frame service;
10. `S53adb_server` waits 3 seconds, then runs `adb start-server` once so adb-based Android capture is ready;
11. `S53audio_service` provides audio recording/playback service;
12. `S53agent` starts the Go Agent;
13. `S55aiden_usb_dhcp` configures USB network DHCP / dnsmasq related capabilities;
14. `S56config_web` provides the configuration page;
15. `S99rtcinit` overrides the SDK default RTC script; when RTC is abnormal, only writes default time when system time is still earlier than baseline date, avoiding overwriting system time already calibrated by NTP.
16. `S99usb0config` performs USB network interface post-configuration.

## Common Service Commands

```bash
/etc/init.d/S52frame_service status
/etc/init.d/S52frame_service restart
/etc/init.d/S53adb_server status
/etc/init.d/S53audio_service status
/etc/init.d/S53audio_service restart
/etc/init.d/S39hciinit status
/etc/init.d/S40bluetoothd status
/etc/init.d/S41ble_service status
/etc/init.d/S53agent status
/etc/init.d/S53agent restart
/etc/init.d/S56config_web restart
```

## Key Configuration Files

| File | Description |
| --- | --- |
| `/etc/aiden_frame_service.conf` | frame service binary, socket, log, pid file paths |
| `/etc/aiden_audio_service.conf` | audio service binary, socket, log, pid file paths |
| `/etc/aiden_ble_service.conf` | BLE service binary, socket, log, device-name base, event capacity, and pairing-window duration |
| `/userdata/agent/agent.toml` | Go Agent runtime configuration |
| `/userdata/wpa_supplicant.conf` | Wi-Fi configuration |

## Log Locations

| Service | Log |
| --- | --- |
| Frame Service | `/var/log/frame_service/frame_service.log` |
| adb delayed startup | `/var/log/adb/adb-startup.log` |
| Audio Service | `/var/log/audio_service/audio_service.log` |
| Bluetooth HCI | `/var/log/aiden-hciattach.log` |
| BlueZ | `/var/log/bluetoothd/bluetoothd.log` |
| BLE Service | `/var/log/ble_service/ble_service.log` |
| Agent | `/userdata/agent/log/agent.log` |

## Notes

- While `frame_service` is running, it exclusively uses `/dev/video0`. Directly running `example_camera_capture` will result in `Device or resource busy`; the service must be stopped first.
- The `agent`'s `screenshot` tool depends on `frame_service`; voice and volume tools depend on `audio_service`.
- After modifying `/userdata/agent/agent.toml`, the Agent must be restarted.
