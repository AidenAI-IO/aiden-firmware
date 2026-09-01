---
sidebar_position: 4
---

# Paths, Artifacts, and Configuration Quick Reference

## Build Artifacts

| Path | Description |
| --- | --- |
| `build-host/bin/aiden_tests` | Host-native C++ test binary |
| `output/debian-stage2/apps/bin/` | Cross-compiled application and diagnostic binaries |
| `output/debian-stage2/apps/lib/` | Runtime libraries staged for the OEM image |
| `output/debian-stage2/apps-audit/` | ELF, dependency, and allowlist audit results |
| `output/debian-stage3/rootfs.ext4` | Reproducible Debian armhf rootfs image |
| `output/debian-stage3/image/` | Audited Stage 3 partition and factory images |
| `output/debian/image/update.img` | Final directly flashable local firmware |
| `output/debian/image/manifest.json` | Locally signed OTA manifest |

## Important Source Paths

| Path | Description |
| --- | --- |
| `src/aiden_sdk.h` | C++ SDK API |
| `src/frame_service_main.cpp` | Frame service entry point |
| `src/audio_service_main.cpp` | Audio service entry point |
| `src/config_web.cpp` | Config Web entry point |
| `src/agent/cmd/daemon` | Go Agent daemon |
| `src/agent/cmd/ota` | OTA CLI |
| `src/agent/cmd/abctl` | A/B metadata diagnostic tool |
| `src/agent/internal/agent` | Agent runtime and tools |
| `src/agent/internal/ota` | OTA download, slot, health, and state machine |
| `overlay-debian/` | Debian rootfs overlay and systemd integration |
| `overlay-debian-oem/` | Debian OEM-owned scripts and assets |
| `scripts/debian-stage2/` | Application cross-build and audit |
| `scripts/debian-stage3/` | Rootfs, BSP, image assembly, and audit |
| `tests/` | Host-native C++ tests |

## Device Default Paths

| Path | Description |
| --- | --- |
| `/oem/usr/bin/` | Audited production applications |
| `/oem/usr/lib/` | Vendor and application runtime libraries |
| `/oem/usr/model/` | VAD models and weights |
| `/oem/usr/share/aiden/` | Web, skill, audio, and EDID assets |
| `/usr/lib/aiden/` | Debian service helpers |
| `/userdata/agent/agent.toml` | Agent configuration |
| `/userdata/agent/python/` | Persistent pip userbase |
| `/userdata/agent/skills/` | Agent skills |
| `/userdata/agent/memory/` | Agent memory |
| `/userdata/system/env` | Persistent device environment source |
| `/run/aiden/system.env` | Validated runtime environment |
| `/userdata/debian/wifi/wpa_supplicant-wlan0.conf` | Wi-Fi configuration |
| `/userdata/debian/ota/config.json` | Debian OTA repository and factory baseline |
| `/userdata/ota/` | Dedicated OTA state and download partition |
| `/oem/etc/ota_pubkey.pem` | OTA manifest Ed25519 public key |
| `/run/frame_service/frame_service.sock` | Frame service socket |
| `/run/audio_service/audio_service.sock` | Audio service socket |
| `/run/ble_service/ble_service.sock` | BLE service socket |
| `/run/agent/storage_level` | Current StorageMonitor level |

## Configuration Sources

| File | Description |
| --- | --- |
| `overlay-debian/etc/aiden_boot.conf` | Product feature gates |
| `overlay-debian/etc/aiden_frame_service.conf` | Frame launch and capture policy |
| `overlay-debian/etc/aiden_audio_service.conf` | Audio socket and volume state |
| `overlay-debian/etc/aiden_ble_service.conf` | BLE service parameters |
| `overlay-debian/etc/systemd/system/` | Debian service and mount units |
| `overlay-debian/etc/systemd/network/` | systemd-networkd configuration |
| `overlay-debian/etc/profile.d/aiden-python.sh` | Fixed persistent Python userbase |
| `overlay-debian-oem/usr/model/` | OEM VAD models |
| `overlay-debian-oem/usr/share/aiden/audio/` | VQE and fallback audio assets |
| `AGENT_CONFIG_PATH` | External Agent configuration required by image assembly |
| `OTA_PUBLIC_KEY_PATH` | External OTA verification key required by image assembly |

## Common Commands

```bash
# Build and test
make test
scripts/debian-stage2/build-apps.sh all
./debian_build.sh

# Flash on Linux (after entering Loader/Maskrom)
FLASH_TOOL=output/debian-stage3/luckfox-pico-sdk/tools/linux/Linux_Upgrade_Tool/upgrade_tool
IMAGE=output/debian/image/update.img
scripts/debian-stage1/flash.sh inspect --tool "${FLASH_TOOL}"
sudo scripts/debian-stage1/flash.sh flash --tool "${FLASH_TOOL}" \
  --image "${IMAGE}" --sha256 "$(awk '{print $1}' "${IMAGE}.sha256")" \
  --confirm-erase-all-data

# Flash on macOS
./upgrade_tool/upgrade_tool uf ./output/debian/image/update.img

# Service status
systemctl status aiden-frame.service --no-pager
systemctl status aiden-audio.service --no-pager
systemctl status aiden-agent.service --no-pager
systemctl --failed

# Frame debugging
frame_service_cli --socket /run/frame_service/frame_service.sock health
frame_service_cli --socket /run/frame_service/frame_service.sock screenshot --out /tmp/screenshot.bmp

# Audio debugging
audio_service_cli --socket /run/audio_service/audio_service.sock health
audio_service_cli --socket /run/audio_service/audio_service.sock get-volume

# Agent API
curl http://<device-ip>:8080/api/tools
curl http://<device-ip>:8080/api/storage/status
curl -X POST -H 'Content-Type: application/json' -d '{"force":false,"targets":[]}' http://<device-ip>:8080/api/storage/cleanup

# OTA
/oem/usr/bin/ota status
/oem/usr/bin/ota update
/oem/usr/bin/abctl read /dev/disk/by-partlabel/misc

# Logs
tail -f /userdata/agent/log/agent.log
journalctl -u aiden-frame.service -u aiden-agent.service
```

The diagnostic CLIs are Stage 2 artifacts and are not part of the production OEM
allowlist. Copy a required CLI to `/userdata` for a bounded device test.

## Persistent Logs

| Path | Description |
| --- | --- |
| `/userdata/agent/log/agent.log` | Agent supervisor and runtime output |
| `/userdata/agent/log/llm-http-*.log` | Session-partitioned LLM HTTP JSONL |
| `/var/log/frame_service/frame_service.log` | Frame service output |
| `/var/log/audio_service/audio_service.log` | Audio service output |
| `/var/log/ble_service/ble_service.log` | BLE service output |
| `/var/log/ota/ota.log` | OTA health output |
| `/var/log/adb/adb-startup.log` | adb host startup output |

When StorageMonitor reports `critical` or `emergency`, the Agent runtime
trims managed logs and Python temporary data according to the configured
storage policy.

## EDID Files

Development EDIDs live in `edid/`. The Debian OEM image installs its production
TC358743 EDID at:

```text
/oem/usr/share/aiden/edid/hdmi_1080p30_cta.hex
```
