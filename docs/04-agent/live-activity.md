# Live Activity / Dynamic Island

`【Aiden】Live Activity` is positioned as a task status panel and entry point back to the Aiden app, not as iOS background execution capability. The agent continues executing tasks, and the iOS app is responsible for displaying status.

Live Activity has two independent paths:

- Foreground path: app polls the agent's `live_activity` status and uses ActivityKit to locally create, update, and end Live Activity; does not request push token, does not register with APNs, does not require backend.
- Background path: when app is in background, screen is locked, or not open, remote updates can only go through APNs. Production form should have Aiden backend/relay save Apple credentials and tokens, then push agent-reported status to APNs.

## Expected Behavior

The product goal of Live Activity is to give Aiden a system-level status entry point after the user leaves the companion app. It should behave as:

- `ready` / `connected`: app has connected to hardware, starts a "Aiden Ready" Live Activity when switching to background, showing device is available and tapping can return to Aiden. This state is mainly an entry point and connection indicator, does not represent the app gaining background persistence capability.
- `running`: when agent is executing a task, Live Activity displays task title, current step, progress, whether user needs to return to app. Tasks can be initiated from app foreground chat or from agent Web UI.
- `needs_app`: when agent needs phone bridge, clipboard, contacts, calendar, or open app capabilities from companion app, Live Activity should prompt user to tap back to Aiden to restore foreground bridge.
- `completed` / `failed` / `canceled`: displays brief result after task ends. If hardware is still connected, can return to `ready`; if device disconnects, session expires, or user closes entry point, end Live Activity.

Key boundaries:

- Live Activity is a status panel and entry point back to app, not a background agent control console.
- It cannot make RN JS, WebSocket, or phone bridge run long-term in iOS background.
- If app is in foreground or just switched to background and already created Live Activity, can display last local state first; continuous background refresh must go through APNs.
- If app has already been suspended/killed by system and there is no existing Live Activity, making Live Activity appear remotely requires APNs push-to-start or waiting for user to reopen app.

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

Foreground local Live Activity does not register push token. Only the background remote update path needs tokens, e.g. when closed-source iOS app or Aiden backend prepares to push task status to APNs for lock screen/Dynamic Island, then register ActivityKit/APNs token:

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

If APNs is configured, the agent will send `apns-push-type: liveactivity` updates on subsequent status changes. When APNs is not configured, registration will still succeed, but only indicates the agent received the token; foreground local updates do not depend on this interface.

## Configuration

Agent-side state snapshot is enabled by default. APNs configuration is only for background/lock screen/Dynamic Island remote updates; app foreground local updates do not read these fields.

During development, the board can connect directly to APNs for joint debugging:

```toml
[live_activity]
enabled = true
bundle_id = "com.qing.aidenbridgedaily"
environment = "sandbox" # production for TestFlight/App Store
team_id = "APPLE_TEAM_ID"
key_id = "APNS_AUTH_KEY_ID"
private_key_path = "/userdata/agent/AuthKey_APNS.p8"
timeout_sec = 10
```

`topic` defaults to `<bundle_id>.push-type.liveactivity`, only needs to be explicitly set for special bundle/topic requirements.

Production environment should not put APNs `.p8` in open-source repos or user boards. Recommended form is:

```text
agent -> Aiden backend/relay -> APNs -> iPhone Live Activity
```

Backend saves APNs Auth Key, Team ID, Key ID, token, and device binding relationships; agent only reports task status.

## Apple Developer Prerequisites

Need to complete in Apple Developer / Xcode signing:

- Enable Push Notifications on App ID; this capability is needed for background APNs remote updates, foreground local updates do not depend on it.
- App supports Live Activities.
- Use provisioning profile that includes Push Notifications capability.
- Prepare APNs Auth Key `.p8`, record Team ID and Key ID.
- Use `environment = "sandbox"` for development debugging; use `production` for TestFlight/App Store.

## App Behavior

iOS app includes `AidenLiveActivityExtension` Widget Extension and `AidenLiveActivityModule` native module.

- After app establishes connection with hardware, it should be able to create a `ready` Live Activity before switching to background, as a quick entry point back to Aiden.
- When foreground polling detects `live_activity.status=running`, app calls custom native helper `AidenLiveActivityModule.startOrUpdate`; this helper internally uses ActivityKit `Activity.request` to create and `Activity.update` to update local Live Activity, without requesting push token.
- When app enters foreground, connection is restored, or within the short polling window after just switching to background, it will best-effort query `GET /api/live-activity/current` to sync the current task initiated from agent Web UI / 8080 to local Live Activity.
- Stop buttons in both app and agent Web UI should use the current `live_activity.request_id` to call `POST /api/chat/cancel`; this way regardless of which end initiated the task, the other end can interrupt the same agent run after seeing running / needs_app.
- When polling detects `completed`, `failed`, or `canceled`, app ends local Live Activity.
- After task ends, if hardware is still connected and entry point still needs to be retained, app can fall back Live Activity to `ready`; otherwise end Live Activity.
- Background remote updates after app has been suspended by iOS, continuous background task status refresh, and background display after initiating new task from agent Web UI are handled by APNs/backend path, and do not depend on RN foreground polling path.
- When user taps Dynamic Island/lock screen Live Activity, system returns to Aiden app.

## Joint Debugging Sequence

1. Install app on iOS real device, confirm Widget Extension is embedded.
2. After app connects to hardware, switch to background and confirm `ready` Live Activity can appear as Dynamic Island/lock screen entry point; this step can initially not depend on APNs.
3. Initiate chat in foreground, confirm Live Activity can enter `running` from `ready` or empty state and update; foreground local path does not need APNs, and should not see token registration requests.
4. Prepare background path: configure Aiden backend/relay or development-period agent direct connection to APNs, and have app register Live Activity push token.
5. Initiate long task from agent Web UI or hardware side, confirm APNs can update Dynamic Island/lock screen status to `running` / `needs_app` / end states when app is in background.
