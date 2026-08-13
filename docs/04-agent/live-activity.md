---
sidebar_position: 13
---

# Live Activity / Dynamic Island

Live Activity is a task status panel and an entry point back to the Aiden app. It does not grant the iOS app continuous background execution. The Agent continues executing tasks, while the iOS app and APNs path display the current status.

Live Activity has two independent paths:

- Foreground path: app polls the agent's `live_activity` status and uses ActivityKit to locally create, update, and end Live Activity; does not request push token, does not register with APNs, does not require backend.
- Background path: when the app is in background, the screen is locked, or the app is not open, remote updates can only go through APNs. The Aiden backend/relay stores Apple credentials and ActivityKit tokens. Each board authenticates with a device-scoped credential that the relay binds to that board's `board_id`; the relay rejects attempts to publish for another board.

## Behavior

Live Activity provides a system-level status entry after the user leaves the companion app:

- `ready` / `connected`: app has connected to hardware, starts a "Aiden Ready" Live Activity when switching to background, showing device is available and tapping can return to Aiden. This state is mainly an entry point and connection indicator, does not represent the app gaining background persistence capability.
- `running`: when agent is executing a task, Live Activity displays task title, current step, progress, whether user needs to return to app. Tasks can be initiated from app foreground chat or from agent Web UI.
- `needs_app`: when an App-only operation has no executable foreground or
  background route, Live Activity is the system entry point for restoring
  foreground Phone Bridge. Board-side tools can automatically click Dynamic
  Island when `return_entry=dynamic_island` and `return_entry_available=true`,
  wait for reconnection, and then send the command. A live BLE Wake subscriber
  can execute its narrower calendar, contacts query/create, and notification
  allowlist without this restoration step. Lock-screen Live Activity entries
  require screenshot/HID confirmation instead of blind tapping.
- `completed` / `failed` / `canceled`: displays brief result after task ends. If hardware is still connected, can return to `ready`; if device disconnects, session expires, or user closes entry point, end Live Activity.

Key boundaries:

- Live Activity is a status panel and entry point back to app, not a background agent control console.
- It cannot make RN JS, WebSocket, or phone bridge run long-term in iOS background.
- BLE Wake can open only a short on-demand HTTP Queue window. It does not change
  the long-term background limit, and its command/result payloads still require
  USB ECM.
- If app is in foreground or just switched to background and already created Live Activity, can display last local state first; continuous background refresh must go through APNs.
- If app has already been suspended/killed by system and there is no existing Live Activity, making Live Activity appear remotely requires APNs push-to-start or waiting for user to reopen app.
- USB ECM connectivity only means the phone and board can still exchange IP packets; it does not mean the iOS app is running in background. `phone_bridge.connected=true` primarily means the app WebSocket is still active, usually while the app is foreground or inside the short background window.
- When `phone_bridge.connected=false` but USB is still physically connected, the agent cannot push status to the app over WebSocket. If relay has a valid Live Activity token for the board, the agent should still update Dynamic Island through relay/APNs.
- When `open_url` or a bridge data tool has no executable PiP, FGS, BLE Wake, or
  direct notification-query route, only treat Dynamic Island as the automatic
  recovery entry when `return_entry="dynamic_island"`,
  `return_entry_available=true`, and the corresponding Aiden entry is visible
  in the current frame. Tap back to Aiden, wait for bridge recovery, then send
  the command. `open_app` owns its own
  BridgeOpenApp-versus-SearchLaunchApp routing. For lock-screen Live Activity
  cards, use screenshot/HID fallback or visual confirmation.

## State Model

When an async chat request provides a `request_id`, the agent maintains a `live_activity` snapshot for that request:

```json
{
  "request_id": "chat_...",
  "status": "running",
  "phase": "observing",
  "task_title": "Open Settings",
  "current_step": "Checking the current screen",
  "current_action": "observe_screen",
  "current_target": "",
  "last_tool_name": "screenshot",
  "progress": 0.21,
  "shows_progress": true,
  "can_stop": true,
  "requires_app": false,
  "started_at": "2026-06-12T07:00:00Z",
  "updated_at": "2026-06-12T07:00:02Z"
}
```

Status values:

- `running`: task executing, can display steps and progress.
- `needs_app`: agent needs user to return to Aiden companion app to continue using phone bridge capability.
- `completed`: task completed.
- `failed`: task failed, `last_error` can display failure reason.
- `canceled`: user canceled or request was interrupted.

Structured fields:

- `phase`: agent's current phase, e.g. `planning`, `observing`, `acting`, `phone_bridge`, `waiting_app`, `waiting_user`, `verifying`, `answering`.
- `current_action`: machine-consumable action key, e.g. `open_app`, `observe_screen`, `control_phone`, `clipboard`, `calendar`.
- `current_target`: current action target, e.g. app name, contact, calendar title, search term; can be empty when not suitable to display sensitive input.
- `current_app`: current target app, usually inferred from `open_app` tool input.
- `requires_app`: current action depends on returning to the foreground
  companion app. App-backed operations that are executable through PiP/FGS or
  BLE Wake do not require this foreground-return state merely because the
  WebSocket is disconnected.
- `phone_app_state` events additionally report `return_entry` and `return_entry_available`. When the iOS app is backgrounded or inactive, it usually reports `return_entry="dynamic_island"` to indicate that the user can tap Dynamic Island / lock screen Live Activity to return to Aiden and restore Phone Bridge.

`GET /api/chat/result?request_id=<id>&offset=<n>` maintains original response compatibility and additionally returns a `live_activity` field. iOS app uses this field to locally update Live Activity when polling in foreground. This foreground path does not use APNs.

## API

### Query Status

```http
GET /api/live-activity/status?request_id=<id>
```

Response:

```json
{
  "status": "ok",
  "live_activity": {
    "request_id": "chat_...",
    "status": "running",
    "task_title": "Open Settings",
    "current_step": "Checking the current screen",
    "progress": 0.21,
    "shows_progress": true,
    "can_stop": true
  }
}
```

### Query Current Task

If background relay or debugging tools only care about “what the agent is currently doing”, they can query the most recent active task without a request id:

```http
GET /api/live-activity/current
```

Response is the same as above. When there is no active or retained task, returns:

```json
{"status":"not_found"}
```

### Background Remote Update Token Registration

Foreground local Live Activity does not depend on push tokens. The background remote update path needs ActivityKit/APNs tokens.

In the normal production path, the iOS app registers `push_to_start_token` and `activity_token` with Aiden relay. The agent does not handle those ActivityKit tokens directly:

```text
iOS app -> Aiden relay: /v1/registrations
agent -> Aiden relay: /v1/boards/<board_id>/live-activity/state
Aiden relay -> APNs -> iPhone Live Activity
```

The board still keeps a debug endpoint that can receive a single Activity update token. Ordinary deployments do not use this path, and APNs direct configuration is not exposed in the normal config page:

```http
POST /api/live-activity/registrations
Content-Type: application/json

{
  "request_id": "chat_...",
  "activity_id": "activitykit-id",
  "push_token": "hex-token",
  "platform": "ios"
}
```

In ordinary deployments, the app registers tokens directly with relay, and the agent does not touch ActivityKit tokens. Foreground local updates also do not depend on this interface.

## Configuration

Agent-side state snapshot is enabled by default. App foreground local updates do not read relay config. Background, lock-screen, and Dynamic Island remote updates require an explicitly provisioned relay URL and device-scoped credential. Each board generates a persistent `board_id` in `/userdata/agent/board_id`; empty or `default` board IDs are not valid relay identities.

Advanced deployments can override relay config. Do not put Apple APNs `.p8` files on the board:

```toml
[live_activity]
enabled = true
relay_url = "https://relay.example.com"
relay_api_key = "device-scoped-credential"
# board_id is normally generated automatically at /userdata/agent/board_id.
# Advanced deployments may set a non-default value explicitly.
board_id = "board-001"
```

`relay_api_key` must be unique to one board and bound by the relay to the same `board_id`. The relay must reject a credential used with any other board path. Provision this credential during device enrollment or deployment; never commit a deployment-wide credential to firmware defaults. Credentials previously distributed through firmware must be revoked and replaced at the relay. Board identity comes from the board at runtime, while phone identity comes from the companion app. APNs Auth Key, Team ID, Key ID, and token registry stay on the relay/backend.

Do not put APNs `.p8` files in open-source repos or user boards. Recommended form is:

```text
agent -> Aiden backend/relay -> APNs -> iPhone Live Activity
```

Backend saves APNs Auth Key, Team ID, Key ID, token, and device binding relationships; agent only reports task status.

## Apple Developer Prerequisites

Configure the following capabilities in Apple Developer and Xcode:

- Enable Push Notifications on App ID; this capability is needed for background APNs remote updates, foreground local updates do not depend on it.
- App supports Live Activities.
- Use provisioning profile that includes Push Notifications capability.
- Relay/backend prepares APNs Auth Key `.p8`, Team ID, and Key ID. These credentials do not go on the board.

## App Behavior

iOS app includes `AidenLiveActivityExtension` Widget Extension and `AidenLiveActivityModule` native module.

- After app establishes connection with hardware, it should be able to create a `ready` Live Activity before switching to background, as a quick entry point back to Aiden.
- When foreground polling detects `live_activity.status=running`, app calls custom native helper `AidenLiveActivityModule.startOrUpdate`; this helper internally uses ActivityKit `Activity.request` to create and `Activity.update` to update local Live Activity, without requesting push token.
- While the app is in foreground and connected to hardware, it periodically queries `GET /api/live-activity/current` to sync the current task initiated from agent Web UI / 8080 to local Live Activity. It also runs one best-effort sync when entering foreground, reconnecting, or during the short polling window after switching to background.
- Stop buttons in both app and agent Web UI should use the current `live_activity.request_id` to call `POST /api/chat/cancel`; this way regardless of which end initiated the task, the other end can interrupt the same agent run after seeing running / needs_app.
- When polling detects `completed`, `failed`, or `canceled`, app ends local Live Activity.
- After a task ends, the app can return Live Activity to `ready` when the entry point should remain available. When the iOS app is already backgrounded, an ordinary WebSocket disconnection should not immediately clear this standby state because Dynamic Island may still be the fastest entry back to Aiden.
- Background remote updates after app has been suspended by iOS, continuous background task status refresh, and background display after initiating new task from agent Web UI are handled by APNs/backend path, and do not depend on RN foreground polling path.
- When user or board-side HID taps Dynamic Island / lock screen Live Activity, the system returns to Aiden app, and the foreground WebSocket bridge should reconnect quickly.

## Verification Checklist

1. Install app on iOS real device, confirm Widget Extension is embedded.
2. After the app connects to hardware, switch to background and confirm that a local `ready` Live Activity appears as a Dynamic Island or lock-screen entry point without requiring APNs.
3. Initiate chat in foreground and confirm that Live Activity can enter `running` from `ready` or an empty state and update locally without depending on APNs.
4. With relay/APNs registration disabled, confirm that the foreground-only scenario does not send token-registration requests.
5. Configure the Aiden backend/relay and confirm that the app registers Live Activity push tokens for the background path.
6. Initiate long task from agent Web UI or hardware side, confirm APNs can update Dynamic Island/lock screen status to `running` / `needs_app` / end states when app is in background.
