# Deployment to Device

## Deploy via Complete Firmware

The recommended production deployment method is to build or download a complete firmware image with overlay and flash it to the device. The firmware will place the following content into the target filesystem:

```text
/oem/usr/bin/                  # Application binaries
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

After `./build.sh` completes, the main artifacts are in `build/bin/`. The ones directly related to device resident services are:

- `build/bin/frame_service`
- `build/bin/audio_service`
- `build/bin/config_web`
- `build/bin/agent`
- `overlay/oem/usr/bin/aiden-env-run`

If the target device has already been flashed with an Aiden firmware version, the init scripts and `/etc/*.conf` typically already exist. In this case, the minimal deployment set is the above 5 files:

```bash
./build.sh

scp build/bin/frame_service root@<device-ip>:/oem/usr/bin/
scp build/bin/audio_service root@<device-ip>:/oem/usr/bin/
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
scp overlay/etc/init.d/S53agent root@<device-ip>:/etc/init.d/
scp overlay/etc/init.d/S56config_web root@<device-ip>:/etc/init.d/

scp overlay/etc/aiden_frame_service.conf root@<device-ip>:/etc/
scp overlay/etc/aiden_audio_service.conf root@<device-ip>:/etc/
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
ssh root@<device-ip> "chmod +x /oem/usr/bin/frame_service /oem/usr/bin/audio_service /oem/usr/bin/config_web /oem/usr/bin/agent /oem/usr/bin/aiden-env-run"
ssh root@<device-ip> "/etc/init.d/S52frame_service restart"
ssh root@<device-ip> "/etc/init.d/S53audio_service restart"
ssh root@<device-ip> "/etc/init.d/S53agent restart"
ssh root@<device-ip> "/etc/init.d/S56config_web restart"
```

## Service Startup Order

When starting with the firmware, the main service relationships are as follows:

1. `S43wlan_guard` monitors WLAN connectivity and recovers automatically;
2. `S49ntp` starts `ntpd` in daemon mode, using direct IP connection to NTP server (bypassing DNS startup order);
3. `S50ntp_watchdog` periodically checks clock sync status, triggers `S49ntp step` to force sync when not synced, exits after sync;
4. `S49usbhid` / `S50usbdevice` configures USB gadget;
5. `S52frame_service` exclusively uses `/dev/video0` and provides screenshot/frame service;
6. `S53adb_server` waits 3 seconds, then runs `adb start-server` once so adb-based Android capture is ready;
7. `S53audio_service` provides audio recording/playback service;
8. `S53agent` starts the Go Agent;
9. `S55aiden_usb_dhcp` configures USB network DHCP / dnsmasq related capabilities;
10. `S56config_web` provides the configuration page;
11. `S99rtcinit` overrides the SDK default RTC script; when RTC is abnormal, only writes default time when system time is still earlier than baseline date, avoiding overwriting system time already calibrated by NTP.
12. `S99usb0config` performs USB network interface post-configuration.

## Common Service Commands

```bash
/etc/init.d/S52frame_service status
/etc/init.d/S52frame_service restart
/etc/init.d/S53adb_server status
/etc/init.d/S53audio_service status
/etc/init.d/S53audio_service restart
/etc/init.d/S53agent status
/etc/init.d/S53agent restart
/etc/init.d/S56config_web restart
```

## Key Configuration Files

| File | Description |
| --- | --- |
| `/etc/aiden_frame_service.conf` | frame service binary, socket, log, pid file paths |
| `/etc/aiden_audio_service.conf` | audio service binary, socket, log, pid file paths |
| `/userdata/agent/agent.toml` | Go Agent runtime configuration |
| `/userdata/wpa_supplicant.conf` | Wi-Fi configuration |

## Log Locations

| Service | Log |
| --- | --- |
| Frame Service | `/var/log/frame_service/frame_service.log` |
| adb delayed startup | `/var/log/adb/adb-startup.log` |
| Audio Service | `/var/log/audio_service/audio_service.log` |
| Agent | `/userdata/agent/log/agent.log` |

## Notes

- While `frame_service` is running, it exclusively uses `/dev/video0`. Directly running `example_camera_capture` will result in `Device or resource busy`; the service must be stopped first.
- The `agent`'s `screenshot` tool depends on `frame_service`; voice and volume tools depend on `audio_service`.
- After modifying `/userdata/agent/agent.toml`, the Agent must be restarted.
