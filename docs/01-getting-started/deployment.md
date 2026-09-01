---
sidebar_position: 6
---

# Deployment to Device

## Complete Debian Firmware

The production deployment path is a complete Debian image. Build the audited
Stage 2 application bundle, Debian rootfs, BSP, A/B images, and signed local OTA
metadata with:

```bash
./debian_build.sh
```

The directly flashable image is:

```text
output/debian/image/update.img
```

On Linux, flash it with the guarded helper below. It checks the image digest,
requires Loader/Maskrom mode, and makes the destructive userdata overwrite
explicit:

```bash
FLASH_TOOL=output/debian-stage3/luckfox-pico-sdk/tools/linux/Linux_Upgrade_Tool/upgrade_tool
IMAGE=output/debian/image/update.img
SHA256=$(awk '{print $1}' "${IMAGE}.sha256")
scripts/debian-stage1/flash.sh inspect --tool "${FLASH_TOOL}"
sudo scripts/debian-stage1/flash.sh flash \
  --tool "${FLASH_TOOL}" \
  --image "${IMAGE}" \
  --sha256 "${SHA256}" \
  --confirm-erase-all-data
```

The repository-root `upgrade_tool/upgrade_tool` is a macOS Mach-O binary; use
the Linux tool from the Stage 3 SDK on Linux. On macOS, flash a locally built
image with:

```bash
./upgrade_tool/upgrade_tool uf ./output/debian/image/update.img
```

The image installs these main runtime trees:
```text
/oem/usr/bin/                                  # Audited production applications
/oem/usr/lib/                                  # Vendor and application libraries
/oem/usr/model/                                # VAD model and weights
/oem/usr/share/aiden/                          # Config Web, skills, audio, and EDID assets
/usr/lib/aiden/                                # Debian rootfs service helpers
/etc/systemd/system/aiden-*.service            # Product systemd units
/userdata/agent/agent.toml                     # External build-time Agent configuration
/userdata/debian/wifi/wpa_supplicant-wlan0.conf
/userdata/debian/ota/config.json               # Debian OTA factory configuration
```

`overlay-debian/` owns the Debian rootfs additions. `overlay-debian-oem/`, the
audited Stage 2 bundle, SDK kernel modules, and generated web assets form the OEM
image. Neither Debian image stage consumes the legacy root overlay.

## Development Binary Update

Build and audit applications without rebuilding the firmware:

```bash
scripts/debian-stage2/build-apps.sh all
```

Production binaries are under `output/debian-stage2/apps/bin/`. For a temporary
device-side test, stop the owning systemd unit before replacing its binary:

```bash
ssh root@<device-ip> 'systemctl stop aiden-frame.service'
scp output/debian-stage2/apps/bin/frame_service root@<device-ip>:/oem/usr/bin/frame_service
ssh root@<device-ip> 'chmod 0755 /oem/usr/bin/frame_service && systemctl start aiden-frame.service'
```

Use the same pattern for `audio_service`, `ble_service`, `agent`, or
`config_web`. Copying a binary directly mutates only the active OEM slot and can
invalidate the factory hash expected by OTA diagnostics, so use it for short
development cycles only. Rebuild and flash a complete image for a reproducible
deployment.

Diagnostic executables such as `frame_service_cli`, `audio_service_cli`, and
`example_*` are present in the Stage 2 output but excluded from the production
OEM allowlist. Copy only the tool needed for a bounded test to a directory under
`/userdata`, then remove it when the test is complete.

Do not deploy service definitions by copying individual files into `/etc`.
Rootfs helpers and systemd units are versioned with the Debian rootfs image and
must move together.

## Service Relationships

`aiden.target` groups the product services. Important ordering is expressed by
systemd dependencies rather than filename order:

1. Slot resolution and rootfs growth expose the active OEM, userdata, and OTA partitions.
2. Userdata migration, machine identity, OEM library registration, and strict environment generation complete.
3. Media, Wi-Fi, Bluetooth, USB gadget, DHCP, and time services prepare hardware and networking.
4. Frame, audio, BLE, Agent, Config Web, adb host, and WLAN recovery services start independently.
5. The OTA health aggregator checks required local services before committing a pending slot.

Inspect the dependency graph with:

```bash
systemctl list-dependencies aiden.target
systemctl --failed
```

## Common Service Commands

```bash
systemctl status aiden-frame.service --no-pager
systemctl restart aiden-frame.service
systemctl status aiden-audio.service --no-pager
systemctl restart aiden-audio.service
systemctl status aiden-ble.service --no-pager
systemctl status aiden-agent.service --no-pager
systemctl restart aiden-agent.service
systemctl restart aiden-config-web.service
systemctl status aiden-usb-gadget.service --no-pager
```

## Key Configuration Files

| File | Description |
| --- | --- |
| `/etc/aiden_boot.conf` | Product feature gates loaded by systemd units |
| `/etc/aiden_frame_service.conf` | Frame service launch and capture settings |
| `/etc/aiden_audio_service.conf` | Audio service socket and volume-state settings |
| `/etc/aiden_ble_service.conf` | BLE socket, name, event capacity, and pairing window |
| `/userdata/agent/agent.toml` | Agent runtime configuration |
| `/userdata/system/env` | Persistent device environment source |
| `/run/aiden/system.env` | Validated, generated environment consumed by services |
| `/userdata/debian/wifi/wpa_supplicant-wlan0.conf` | Wi-Fi configuration managed by Config Web |
| `/userdata/debian/ota/config.json` | Debian OTA repository and factory baseline |

## Log Locations

| Service | Log |
| --- | --- |
| Frame Service | `/var/log/frame_service/frame_service.log` |
| Audio Service | `/var/log/audio_service/audio_service.log` |
| BLE Service | `/var/log/ble_service/ble_service.log` |
| adb host startup | `/var/log/adb/adb-startup.log` |
| OTA health | `/var/log/ota/ota.log` |
| Agent | `/userdata/agent/log/agent.log` |

Use `journalctl -u <unit>` for systemd lifecycle and helper failures. Service
stdout/stderr that is intentionally persisted remains in the files above.

`frame_service` exclusively owns `/dev/video0`; stop `aiden-frame.service`
before a direct camera diagnostic. The Agent screenshot tool depends on the
frame service, while voice and volume tools depend on the audio service.
