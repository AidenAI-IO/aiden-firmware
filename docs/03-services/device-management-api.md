---
sidebar_position: 3
---

# Config Web Management API

Config Web is the `config-web` subcommand of the Go Agent binary. Config Web
and the Agent runtime listen on ports 80 and 8080 respectively, and each owns
its own PID, logs, and restart lifecycle. They interact only through an
internal reload endpoint on `127.0.0.1` after configuration is saved. Config
Web does not proxy Agent chat, session, or Phone Bridge traffic.

## Resource Endpoints

All public endpoints use the `/api` root without an additional version prefix:

| Resource | Method and path | Description |
| --- | --- | --- |
| Configuration | `GET /api/config` | Read the resolved `agent.toml` configuration |
| Configuration | `PATCH /api/config` | Apply the merge patch `{ "config": { ... } }` |
| Configuration | `GET /api/config/schema` | Read field types, defaults, choices, secret markers, and restart hints |
| Configuration | `PUT /api/config/locale` | Update the page language |
| Configuration | `POST /api/config/test` | Validate configuration and the device environment without saving |
| Models | `GET /api/models?provider=...&locale=...` | Query the model catalog through the Agent runtime |
| STT test | `POST /api/config/test/stt/start` | Start a microphone recording test |
| STT test | `POST /api/config/test/stt/stop` | Stop recording and return the transcription result |
| Device | `GET /api/device/snapshot` | Read the aggregated initial-page model: configuration, Wi-Fi, device, firmware, and storage summaries |
| Device | `GET /api/device/status` | Read the model, firmware, process, USB/HID, and capability summary |
| Device | `POST /api/device/reboot` | Reboot the device |
| Device | `POST /api/device/usb/reenumerate` | Re-enumerate USB HID/ECM |
| Network | `POST /api/network/wifi/scan` | Scan for nearby Wi-Fi networks |
| Network | `PUT /api/network/wifi/connection` | Connect to and save a network, rolling back on failure |
| Network | `DELETE /api/network/wifi/connection?ssid=...` | Forget a network without relying on a DELETE request body |
| System | `GET/PUT /api/system/environment` | Read or atomically replace system environment variables |
| OTA | `GET /api/ota/status` | Read the current state, progress, and log summary |
| OTA | `POST /api/ota/updates` | Create an OTA task and return its `task_id` |
| Logs | `GET /api/logs/agent` | Read the Agent log summary |
| Logs | `GET/PUT /api/logs/llm/{name}` | View or import an LLM HTTP log |
| Logs | `GET /api/logs/support` | Export a diagnostic support-log archive |
| Storage | `GET /api/storage/status` | Read SD/eMMC state and formatting tasks |
| Storage | `POST /api/storage/format` | Format the SD card asynchronously |
| Storage | `POST /api/storage/eject` | Sync and safely eject the SD card |

All public endpoints above are served by Config Web on port 80. Config Web may
call the Agent runtime locally through its controlled HTTP facade, but the
frontend and external clients must not access the Agent's model, STT, or
storage endpoints directly on port 8080.

`GET /api/storage/status` and the `storage` field of
`GET /api/device/snapshot` share the following response shape:

```json
{
  "effective_mode": 1,
  "card": {
    "present": false,
    "mounted": false,
    "device": "",
    "total_bytes": 0,
    "free_bytes": 0,
    "reason": ""
  },
  "mount_point": "/mnt/sdcard",
  "format_job": {"status": "idle"},
  "migration": {"status": "idle"}
}
```

`format_job` is asynchronous and its status can be `idle`, `running`,
`success`, or `failed`. `migration` represents the background migration from
eMMC to SD and uses the same status values. A card can be present without being
mounted, so clients must use `card.present` and `card.mounted` independently
when enabling the format and eject actions.

## Configuration Save Response

A successful `PATCH /api/config` response includes:

```json
{
  "ok": true,
  "persisted": true,
  "applied": true,
  "revision": 123,
  "changed_paths": ["model.model"],
  "restart_required": false,
  "restart_reasons": []
}
```

`persisted` means the configuration file was written atomically. `applied`
means the Agent accepted and loaded that revision. Clients must present these
states separately. If reload fails, the endpoint returns HTTP 503 while
retaining `persisted=true`, `applied=false`, and the error details. The page
should explain that the configuration was saved but is not active in the
current Agent process. Fields that require a full restart set
`restart_required` and `restart_reasons`.

## Agent Restart Lifecycle

Updating `/api/system/environment` persists the environment file and schedules
an Agent restart. If an STT configuration test is recording, Config Web defers
the restart until the test stops successfully so recording is not interrupted.
A subsequent STT start waits for an already scheduled restart to finish and
briefly retries the Agent connection while the runtime becomes ready. Restart
launch failures are returned to the caller instead of being reported as a
successfully scheduled restart.

## API Boundary

Retired routes such as `/api/wifi/*`, `/api/system/env`,
`/api/ota/update`, `/api/reboot`, `/api/agent/*`, and `/api/llm-logs/*` have
been removed and no longer return `Deprecation` adapter responses. Clients must
use the canonical resources in the table above. `GET /api/config` has only
configuration semantics; use `/api/device/snapshot` for the aggregated initial
page load.

## Internal Agent Reload

`POST /api/internal/config/reload` accepts loopback requests only and may carry
a `revision`. The Agent validates that revision and reports `applied=true` only
after every affected runtime dependency has been rebuilt successfully. A stale
revision returns HTTP 409. This endpoint is not part of the public management
API.
