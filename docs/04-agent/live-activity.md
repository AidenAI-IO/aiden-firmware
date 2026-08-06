# Live Activity / Dynamic Island

`【Aiden】Live Activity` is positioned as a task status panel and entry point back to the Aiden app, not as iOS background execution capability. The agent continues executing tasks, and the iOS app is responsible for displaying status.

Live Activity has two independent paths:

- Foreground path: app polls the agent's `live_activity` status and uses ActivityKit to locally create, update, and end Live Activity; does not request push token, does not register with APNs, does not require backend.
- Background path: when the app is in background, the screen is locked, or the app is not open, remote updates can only go through APNs. Production form has Aiden backend/relay save Apple credentials and tokens; the app and agent call the relay with the same shared relay token, and the relay pushes updates to APNs.

## Expected Behavior

The product goal of Live Activity is to give Aiden a system-level status entry point after the user leaves the companion app. It should behave as:

- `ready` / `connected`: app has connected to hardware, starts a "Aiden Ready" Live Activity when switching to background, showing device is available and tapping can return to Aiden. This state is mainly an entry point and connection indicator, does not represent the app gaining background persistence capability.
- `running`: when agent is executing a task, Live Activity displays task title, current step, progress, whether user needs to return to app. Tasks can be initiated from app foreground chat or from agent Web UI.
- `needs_app`: when agent needs Phone Bridge, clipboard, contacts, calendar, open app, or other companion app capabilities, Live Activity is the system entry point for restoring foreground bridge. Board-side tools can automatically click Dynamic Island when `return_entry=dynamic_island` and `return_entry_available=true`, wait for reconnection, and then send the command. Lock-screen Live Activity entries require screenshot/HID confirmation instead of blind tapping.
- `completed` / `failed` / `canceled`: displays brief result after task ends. If hardware is still connected, can return to `ready`; if device disconnects, session expires, or user closes entry point, end Live Activity.

Key boundaries:

- Live Activity is a status panel and entry point back to app, not a background agent control console.
- It cannot make RN JS, WebSocket, or phone bridge run long-term in iOS background.
- If app is in foreground or just switched to background and already created Live Activity, can display last local state first; continuous background refresh must go through APNs.
- If app has already been suspended/killed by system and there is no existing Live Activity, making Live Activity appear remotely requires APNs push-to-start or waiting for user to reopen app.
- USB ECM connectivity only means the phone and board can still exchange IP packets; it does not mean the iOS app is running in background. `phone_bridge.connected=true` primarily means the app WebSocket is still active, usually while the app is foreground or inside the short background window.
- When `phone_bridge.connected=false` but USB is still physically connected, the agent cannot push status to the app over WebSocket. If relay has a valid Live Activity token for the board, the agent should still update Dynamic Island through relay/APNs.
- When `open_url` or a bridge data tool needs Phone Bridge capability and a visible Aiden Dynamic Island entry exists, treat it as the automatic recovery entry: tap back to Aiden, wait for bridge recovery, then send the command. `open_app` owns its own BridgeOpenApp-versus-SearchLaunchApp routing. For lock-screen Live Activity cards, use screenshot/HID fallback or visual confirmation.

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
- `requires_app`: current action depends on companion app/Phone Bridge. If bridge is unavailable, `status` switches to `needs_app`, and Dynamic Island should prompt user to return to Aiden.
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
{“status”:”not_found”}
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
  “request_id”: “chat_...”,
  “activity_id”: “activitykit-id”,
  “push_token”: “hex-token”,
  “platform”: “ios”
}
```

In ordinary deployments, the app registers tokens directly with relay, and the agent does not touch ActivityKit tokens. Foreground local updates also do not depend on this interface.

## Configuration

Agent-side state snapshot is enabled by default. Background/lock-screen/Dynamic Island remote updates go through Aiden Live Activity relay. App foreground local updates do not read relay config. Official firmware preconfigures `relay_url` and `relay_api_key` in `overlay/userdata/agent/agent.toml`, so users do not need to know or enter the key after flashing the board. Each board generates a persistent `board_id` in `/userdata/agent/board_id`; empty or `default` board IDs are not valid relay identities.

Advanced deployments can override relay config. Do not put Apple APNs `.p8` files on the board:

```toml
[live_activity]
enabled = true
relay_url = "https://apns-test.aidenai.io"
relay_api_key = "shared-relay-token"
# board_id is normally generated automatically at /userdata/agent/board_id.
# Advanced deployments may set a non-default value explicitly.
board_id = "board-001"
```

`relay_api_key` is a relay-deployment shared Bearer token. It should match the iOS app build config `LIVE_ACTIVITY_RELAY_API_KEY` and relay server environment variable `AIDEN_RELAY_API_KEY`. It only gates app token registration and agent state reporting; do not treat it as a per-board identity. Board identity comes from the board at runtime; the app should read it from the board instead of hard-coding a shared value. Phone identity comes from the companion app at runtime; the board does not expose a manual `phone_id` relay fallback. APNs Auth Key, Team ID, Key ID, and token registry stay on relay/backend.

Do not put APNs `.p8` files in open-source repos or user boards. Recommended form is:

```text
agent -> Aiden backend/relay -> APNs -> iPhone Live Activity
```

Backend saves APNs Auth Key, Team ID, Key ID, token, and device binding relationships; agent only reports task status.

## Apple Developer Prerequisites

Need to complete in Apple Developer / Xcode signing:

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
- After task ends, if the entry point still needs to be retained, app can fall back Live Activity to `ready`. When the iOS app is already backgrounded, ordinary WebSocket disconnection should not immediately clear standby, otherwise Agent loses the fastest entry back to Aiden.
- Background remote updates after app has been suspended by iOS, continuous background task status refresh, and background display after initiating new task from agent Web UI are handled by APNs/backend path, and do not depend on RN foreground polling path.
- When user or board-side HID taps Dynamic Island / lock screen Live Activity, the system returns to Aiden app, and the foreground WebSocket bridge should reconnect quickly.

## Joint Debugging Sequence

1. Install app on iOS real device, confirm Widget Extension is embedded.
2. After app connects to hardware, switch to background and confirm `ready` Live Activity can appear as Dynamic Island/lock screen entry point; this step can initially not depend on APNs.
3. Initiate chat in foreground, confirm Live Activity can enter `running` from `ready` or empty state and update; foreground local path does not need APNs, and should not see token registration requests.
4. Prepare background path: configure Aiden backend/relay and have the app register Live Activity push tokens.
5. Initiate long task from agent Web UI or hardware side, confirm APNs can update Dynamic Island/lock screen status to `running` / `needs_app` / end states when app is in background.
