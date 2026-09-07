---
sidebar_position: 3
---

# Config Web Management API

Config Web is the `config-web` subcommand of the Go Agent binary. Config Web
and the Agent runtime listen on ports 80 and 8080 respectively, and each owns
its own PID and service lifecycle. Config Web calls the Agent's loopback-only
reload endpoint after configuration is saved, while the Agent reads Config
Web's storage-state mirror for archive lookup. Config Web does not proxy Agent
chat, session, or Phone Bridge traffic.

## Resource Endpoints

All public endpoints use the `/api` root without an additional version prefix:

| Resource | Method and path | Description |
| --- | --- | --- |
| Configuration | `GET /api/config` | Read the resolved `agent.toml` configuration |
| Configuration | `PATCH /api/config` | Persist a merge patch and queue application |
| Configuration | `GET /api/config/application` | Read pending, applied, failed, or reboot-required state |
| Configuration | `GET /api/config/schema` | Read field types, defaults, choices, secret markers, and restart hints |
| Configuration | `PUT /api/config/locale` | Update the page language |
| Configuration | `POST /api/config/test` | Validate configuration and the device environment without saving |
| Models | `GET /api/models?provider=...&locale=...` | Return the localized model catalog |
| STT test | `POST /api/config-test/stt/start` | Start a microphone recording test using the submitted unsaved settings |
| STT test | `POST /api/config-test/stt/stop` | Stop recording and return the transcription result |
| Storage | `GET /api/storage/status` | Read SD/eMMC state and formatting tasks |
| Storage | `POST /api/storage/format` | Format the SD card asynchronously |
| Storage | `POST /api/storage/eject` | Sync and safely eject the SD card |
| Device | `GET /api/device/snapshot` | Read the aggregated initial-page model: configuration, Wi-Fi, device, firmware, and storage summaries |
| Device | `GET /api/device/status` | Read the device, firmware, Agent process, USB/HID, and capability summary |
| Device | `POST /api/device/reboot` | Reboot the device |
| Device | `POST /api/device/usb/reenumerate` | Re-enumerate USB HID/ECM |
| Network | `POST /api/network/wifi/scan` | Scan for nearby Wi-Fi networks |
| Network | `PUT /api/network/wifi/connection` | Start a bounded asynchronous connect-and-save task |
| Network | `GET /api/network/wifi/connection?task_id=...` | Poll connection, persistence, and rollback status |
| Network | `DELETE /api/network/wifi/connection?ssid=...` | Forget a network without relying on a DELETE request body |
| System | `GET/PUT /api/system/environment` | Read or atomically replace system environment variables |
| OTA | `GET /api/ota/status` | Read the current state, progress, and log summary |
| OTA | `POST /api/ota/updates` | Create an OTA task and return its `task_id` |
| Logs | `GET /api/logs/agent` | Read the Agent log summary |
| Logs | `GET /api/logs/llm` | List available LLM HTTP log files |
| Logs | `GET/PUT /api/logs/llm/{name}` | Export a raw LLM HTTP log or import one with the same name |
| Logs | `GET /api/logs/support` | Export a diagnostic support-log archive |

All endpoints above are served by Config Web on port 80. The page uses only
same-origin management requests. Agent port mappings therefore do not affect
the configuration portal, and the Agent does not expose duplicate models, STT
test, or storage-management handlers on port 8080.

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

A successful `PATCH /api/config` persists the file and queues runtime application:

```json
{
  "ok": true,
  "persisted": true,
  "applied": false,
  "pending": true,
  "state": "pending",
  "revision": 123,
  "changed_paths": ["model.model"],
  "reboot_required": false,
  "restart_required": false,
  "restart_reasons": [],
  "agent_restart_scheduled": false
}
```

Poll `GET /api/config/application` for `state`, `pending`, `applied`,
`reboot_required`, `revision`, `applied_revision`, and optional `error`.
`persisted` confirms the atomic file write; `applied` confirms all settings in
that revision are active. Revisions are opaque 64-bit values; browser clients
should use the returned state rather than perform numeric revision arithmetic.

| State | Meaning |
| --- | --- |
| `pending` | Waiting for the current task, tool operation, or affected voice session to finish |
| `applied` | The saved revision is active |
| `reboot_required` | Online settings are active; USB identity/layout changes await an explicit device reboot (`applied=false`) |
| `failed` | Application failed; inspect `error`, correct the configuration, and retry |

The initial status can have an empty state before any save in this Agent process.
The UI keeps persisted values after an application failure and offers **Retry
apply** (`PATCH /api/config` with `{"config":{}}`). Synchronous persistence,
service, or reload-request failures return an error HTTP status; asynchronous
component failures are reported by the status endpoint. Neither schedules an
Agent restart. Frame/storage application may have completed before a later
component fails. Failed frame/storage work is retried on the next save.

`restart_required` mirrors `reboot_required` for USB descriptor changes
(Android versus non-Android device type) and `hid.keyboard_layout`. The runtime
keeps the current USB settings until reboot and remembers the exception across
subsequent saves; reverting those settings clears it. Reboot is an explicit
user action and is never triggered by saving. CLI `--device-type` continues to
override the file for the current process.

## Agent Restart Lifecycle

Updating `/api/system/environment` still persists the environment file and
schedules an Agent restart. Config changes use the online application lifecycle
above. STT configuration tests remain owned by Config Web. Restart launch
failures are returned to the caller instead of reporting success.

## Storage Ownership

Config Web is the sole owner of `StorageManager`: it detects and mounts the SD
card, runs format/eject operations, migrates governed data, and writes
`/run/aiden/storage.state`. The Agent runtime never performs those hardware
operations. It uses a read-only state view to include the SD archive root when
serving previously migrated audio.

## Wi-Fi Task Lifecycle

`PUT /api/network/wifi/connection` returns HTTP 202 with a `task_id` immediately.
The task has a 120-second total deadline covering candidate application,
verification, persistence, and rollback. Clients poll the GET form until the
status is `succeeded` or `failed`; failure responses distinguish the candidate
apply result from the rollback result.

## API Boundary

Retired routes such as `/api/wifi/*`, `/api/system/env`,
`/api/ota/update`, `/api/reboot`, `/api/agent/*`, and `/api/llm-logs/*` have
been removed and return `404 Not Found`; no compatibility adapters are
provided. Clients must use the canonical resources in the table above.
`GET /api/config` has only configuration semantics; use
`/api/device/snapshot` for the aggregated initial page load.

## Internal Agent Reload

`POST /api/internal/config/reload` accepts loopback requests only and may carry
a `revision`. The Agent validates the saved file revision, queues application,
and returns HTTP 202 with `pending=true`. `GET` on the same loopback-only route
returns application status. A stale revision returns HTTP 409. Pending saves
are coalesced; the latest queued snapshot wins after the current application.
These endpoints are not part of the public management API.
