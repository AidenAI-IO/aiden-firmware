# Paths, Artifacts, and Configuration Quick Reference

## Build Artifacts

| Path | Description |
| --- | --- |
| `build/lib/libaiden.a` | C++ SDK static library |
| `build/lib/libaiden_image.a` | Image processing static library |
| `build/lib/libcjson.a` | cJSON static library |
| `build/bin/` | Application binaries directory |
| `build-host/tests/aiden_tests` | host-native unit test binary |
| `pico-sdk/output/image/` | Full firmware build output directory |

## Important Source Paths

| Path | Description |
| --- | --- |
| `src/aiden_sdk.h` | C++ SDK API |
| `src/frame_service_main.cpp` | Frame Service entry point |
| `src/audio_service_main.cpp` | Audio Service entry point |
| `src/config_web.cpp` | Config Web entry point |
| `src/agent/cmd/daemon` | Go Agent daemon |
| `src/agent/cmd/ota` | OTA CLI |
| `src/agent/cmd/abctl` | A/B metadata diagnostic tool |
| `src/agent/internal/agent` | Agent runtime and tool implementation |
| `src/agent/internal/ota` | OTA manifest, download, slot, health and state machine |
| `tests/` | Unit tests |

## Device Default Paths

| Path | Description |
| --- | --- |
| `/oem/usr/bin/` | Application binary installation directory |
| `/oem/usr/model/` | VAD models and weights updated with OEM/OTA |
| `/userdata/agent/agent.toml` | Agent main configuration |
| `/userdata/agent/skills/` | Agent skills directory |
| `/userdata/agent/memory/` | Agent memory persistence directory |
| `/userdata/system/env` | Device-wide environment file loaded by service launchers and SSH login shells |
| `/userdata/ota/` | OTA configuration, state, download cache and health marker |
| `/oem/etc/ota_pubkey.pem` | OTA manifest Ed25519 public key |
| `/userdata/wpa_supplicant.conf` | Wi-Fi configuration |
| `/run/frame_service/frame_service.sock` | Frame Service socket |
| `/run/audio_service/audio_service.sock` | Audio Service socket |
| `/var/log/frame_service/frame_service.log` | Frame Service log |
| `/var/log/adb/adb-startup.log` | adb delayed startup log |
| `/var/log/audio_service/audio_service.log` | Audio Service log |
| `/userdata/agent/log/agent.log` | Agent log (includes init script and runtime output) |
| `/userdata/agent/log/llm-http-YYYYMMDDHHMMSSmmm.log` | LLM HTTP request/response log (JSONL format, organized by session) |
| `/run/agent/storage_level` | Current StorageMonitor level used by deployment-side log guards |

## Configuration Files

| File | Description |
| --- | --- |
| `overlay/etc/aiden_boot.conf` | System boot behavior configuration (controls service startup order, network settings, debug flags) |
| `overlay/etc/aiden_frame_service.conf` | Frame Service init configuration template |
| `overlay/etc/aiden_audio_service.conf` | Audio Service init configuration template |
| `overlay/etc/init.d/S20oemslot` | Slot-aware `/oem` mount script |
| `overlay/etc/init.d/S49ntp` | ntpd daemon startup + `step` one-shot sync subcommand |
| `overlay/etc/init.d/S50ntp_watchdog` | NTP sync periodic check, triggers `S49ntp step` when not synced |
| `overlay/etc/init.d/S53adb_server` | Delayed one-shot `adb start-server` bootstrap |
| `overlay/etc/init.d/S54ota` | Boot-time OTA health one-shot |
| `overlay/etc/init.d/S99rtcinit` | RTC invalid-date calibration script replacing the SDK default |
| `overlay/etc/profile.d/aiden-env.sh` | SSH/login shell environment loader snippet |
| `overlay/oem/usr/bin/aiden-env-run` | Service environment launcher |
| `overlay/oem/usr/model/` | VAD models and weights, updated with OEM partition |
| `overlay/oem/usr/share/aiden/audio/voice-notifications/` | Bundled Chinese and English PCM WAV fallback played when final TTS is unavailable |
| `overlay/userdata/agent/agent.toml` | Agent default configuration template |
| `overlay/userdata/system/env` | Default system environment template |
| `overlay/userdata/wpa_supplicant.conf` | Wi-Fi default configuration template |

## Common Commands

```bash
# Build
./build.sh
make test

# Full firmware
./build_image.sh
./upgrade_tool/upgrade_tool uf ./update.img

# Service status
/etc/init.d/S52frame_service status
/etc/init.d/S53audio_service status
/etc/init.d/S53agent status

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
/oem/usr/bin/abctl read /dev/block/by-name/misc

# Log viewing
tail -f /userdata/agent/log/agent.log
jq . /userdata/agent/log/llm-http-$(date +%Y%m%d)*.log
```

## Agent Logs

### /userdata/agent/log/agent.log

Agent main log, contains all output from init script and runtime.

The path follows the runtime config directory: `<CONFIG_DIR>/log/agent.log`. The default config directory is `/userdata/agent`. The former `/var/log/agent/agent.log` is not migrated or used as a fallback.

When StorageMonitor reports `critical` or `emergency`, `S53agent` trims this file to the configured `storage.degraded_mode.max_agent_log_mb` limit while preserving the newest content.

**Session separator marker**:
```
2026/06/20 15:06:16 [INFO] ========================================
2026/06/20 15:06:16 [INFO] NEW SESSION STARTED
2026/06/20 15:06:16 [INFO] Session ID: abc123def456
2026/06/20 15:06:16 [INFO] ========================================
```

### /userdata/agent/log/llm-http-{YYYYMMDDHHMMSSmmm}.log

LLM HTTP request/response log (JSONL format), separate file per session.

**Format**:
```json
{"ts":"15:00:00","kind":"request","status":0,"body":"{\"model\":\"...\",\"messages\":[...]}"}
{"ts":"15:00:00","kind":"response","status":200,"body":"{\"choices\":[...]}"}
```

**Fields**:
- `ts` - Timestamp (HH:MM:SS)
- `kind` - `request`, `response`, `stream`, `error`
- `status` - HTTP status code (0 for requests)
- `body` - Request/response body (JSON string)

**Common queries**:
```bash
# View specific session
jq . /userdata/agent/log/llm-http-20260620090405123.log

# Only view responses
jq 'select(.kind | test("response|stream"))' llm-http-*.log

# Extract non-streaming response content
jq -r 'select(.kind == "response") | .body | fromjson' llm-http-*.log

# Count request types
jq -r '.kind' llm-http-*.log | sort | uniq -c
```

## EDID Files

`edid/` directory contains commonly used EDIDs:

- `1080p30.hex`
- `720p30.hex`
- `720p60.hex`
- `hdmi_1080p30_cta.hex`
- `hdmi_720p60_cta.hex`
- `hdmi_720p60_1080p30_cta.hex`
- `phone_vrt_552x1200p30.hex`
- `phone_vrt_640x1200p30.hex`
