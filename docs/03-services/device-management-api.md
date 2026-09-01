---
sidebar_position: 3
---

# Device Management API

The Device Management API owns the device-side operations currently used by
Config Web. Config Web is one client of this API, not the owner of its routing
or business logic. A different UI or an automation client can use the same API,
and the bundled static portal can be removed without removing device management
capabilities.

## Current topology

The firmware installs one `/oem/usr/bin/agent` executable and starts two
independent processes from it:

- `S53agent` runs the Agent daemon and its tool API on port 8080.
- `S56config_web` runs `agent config-web` and the Device Management API on port
  80.

Some management operations proxy to the Agent daemon, while configuration,
Wi-Fi, OTA, log, and system operations remain owned by the management server.
The Go implementation exposes `Server.APIHandler()` separately from the static
portal handler so the API can be mounted without serving the page.

## Version 1 routes

All canonical routes are under `/api/v1`. Successful and handler-generated
error responses retain the existing payload shapes during the v1 migration.
This keeps the change focused on resource naming, HTTP methods, and ownership;
response normalization that would break clients belongs in `/api/v2`.

| Operation | Canonical route | Legacy alias |
| --- | --- | --- |
| Read device bootstrap snapshot | `GET /api/v1/device/snapshot` | `GET /api/config` |
| Read configuration schema | `GET /api/v1/config/schema` | `GET /api/config-meta`, `GET /api/config/meta` |
| Apply configuration merge patch | `PATCH /api/v1/config` | `POST /api/config` |
| Set device locale | `PUT /api/v1/config/locale` | `PUT /api/config/locale` |
| Run a configuration test | `POST /api/v1/config/tests` | `POST /api/config/test` |
| Start an STT test session | `POST /api/v1/config/tests/stt-session` | `POST /api/config/test/stt/start` |
| Stop an STT test session | `DELETE /api/v1/config/tests/stt-session` | `POST /api/config/test/stt/stop` |
| List provider models | `GET /api/v1/models` | `GET /api/models` |
| Scan Wi-Fi networks | `POST /api/v1/wifi/scans` | `POST /api/wifi/scan` |
| Connect Wi-Fi | `PUT /api/v1/wifi/connection` | `POST /api/wifi/connect` |
| Forget a Wi-Fi connection | `DELETE /api/v1/wifi/connection` | `POST /api/wifi/forget` |
| Read system environment | `GET /api/v1/system/environment` | None |
| Replace system environment | `PUT /api/v1/system/environment` | `POST /api/system/env` |
| Read Agent status | `GET /api/v1/agent/status` | `GET /api/agent/status` |
| Read Agent logs | `GET /api/v1/agent/logs` | `GET /api/agent/logs` |
| Read storage status | `GET /api/v1/storage/status` | `GET /api/storage/status` |
| Start storage format | `POST /api/v1/storage/format` | `POST /api/storage/format` |
| Eject storage | `POST /api/v1/storage/eject` | `POST /api/storage/eject` |
| Read OTA status and logs | `GET /api/v1/ota/status` | `GET /api/ota/logs` |
| Start an OTA update | `POST /api/v1/ota/updates` | `POST /api/ota/update`, `POST /api/ota/check-now` |
| Reboot the device | `POST /api/v1/device/reboot` | `POST /api/reboot` |
| Re-enumerate USB HID | `POST /api/v1/hid/usb-reenumeration` | `POST /api/hid/usb-reenumerate` |
| Download a support archive | `GET /api/v1/support/archive` | `GET /api/logs/export` |
| List LLM HTTP logs | `GET /api/v1/logs/llm` | `GET /api/llm-logs` |
| Download an LLM HTTP log | `GET /api/v1/logs/llm/{name}` | `GET /api/llm-logs/export/{name}` |
| Import an LLM HTTP log | `PUT /api/v1/logs/llm/{name}` | `POST /api/llm-logs/import/{name}` |

The snapshot is intentionally a bootstrap aggregate for clients that need the
configuration, Wi-Fi state, Agent state, firmware state, and system environment
for their first render. Resource-specific endpoints should be used for later
reads and writes.

## Request and response contract

- JSON request bodies are limited to 64 KiB unless an endpoint explicitly
  streams data, such as LLM log import.
- Configuration updates use the existing merge-patch body:
  `{"config": {"section": {"field": "value"}}}`. A `null` field deletes that
  field where the configuration updater supports deletion.
- Handler errors generally use `{"ok": false, "error": "..."}` with an
  appropriate HTTP status. Proxied endpoints retain the Agent daemon response.
- Every matched canonical or legacy route returns
  `X-Aiden-API-Version: 1`.
- URL path segments such as LLM log names must be percent-encoded.

## Compatibility lifecycle

Legacy `/api/...` routes remain aliases for one migration window. They return:

```text
Deprecation: true
Link: </api/v1/...>; rel="successor-version"
```

The bundled Config Web client uses only canonical v1 routes, so legacy aliases
exist for older firmware clients and integration scripts rather than for the
current page. Remove them only after those consumers have migrated. Add a v2
route instead of silently changing an established v1 request or response
contract.

## Future extraction

The API boundary allows these changes independently:

1. Replace or remove the bundled static portal while continuing to run the
   management API.
2. Mount `APIHandler()` in another Go HTTP server inside the Agent codebase.
3. Move the management server into a separate binary if lifecycle, permissions,
   or image size later justify it.

Hardware and operating-system actions stay server-side. A client should not
need filesystem paths, init-script knowledge, or direct access to device nodes.

## Security boundary

The API is currently intended for the trusted USB ECM/device network. It does
not provide an authentication boundary suitable for exposure to a general LAN
or the public Internet. Authentication, authorization, origin policy, and
transport security must be designed before widening network access.
