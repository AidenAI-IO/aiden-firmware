# Phone Bridge: Phone Relay App Solution

## Purpose

Install a relay app on the controlled phone that connects to the hardware board via USB ECM network channel, receives quick commands from the board, and executes system-allowed local operations to compensate for the slowness of pure hardware "find icon, swipe, tap, wait for animation" approach.

Hardware handles screen viewing, decision-making, and HID fallback; relay app handles fast execution of public system capabilities.

## Phase One Capabilities

| Capability | iOS | Android | Problem Solved |
| --- | --- | --- | --- |
| Open specified App | URL Scheme / Universal Link / `openURL` | Package name / Intent | Skip icon finding and tapping process |
| Clipboard read/write | UIPasteboard | Clipboard API | Serve as cross-app content relay |
| Calendar / Reminders | EventKit | Calendar / Alarm / Reminder capability | Quickly write system items |
| Contacts | Contacts | Contacts Provider | Query / add contacts |
| Notification | Local Notification | Notification | Remind user or bring relay app to foreground |
| Communicate with board | WebSocket client | WebSocket client / foreground service | Receive board quick commands |

## Communication Method

The board's fixed address in the USB network is `192.168.42.1`, so it's recommended that the phone relay app actively connects to the board, rather than having the board reverse-connect to the phone app.

Recommended path:

```text
Phone relay app -> ws://192.168.42.1:8080/api/phone-bridge
```

Alternative with separate port:

```text
Phone relay app -> ws://192.168.42.1:18080/phone-bridge
```

In other words: the board acts as WebSocket server, the app as WebSocket client.

This way there's no need to guess the phone IP, no need for the phone app to open a local HTTP service, and reconnection and network configuration are simpler. The existing `192.168.42.1:80` config page and `192.168.42.1:8080` agent test page can be retained; just add a new bridge path or port.

The board also exposes `/api/phone-bridge/commands` and `/api/phone-bridge/results` HTTP queue endpoints, but React Native JS, WebSocket, and polling timers in the iOS background must not be treated as a general tool execution path. On iOS, Phone Bridge is normally a foreground fast path: if Aiden is backgrounded and the app has reported `return_entry=dynamic_island`, Agent restores Aiden through Dynamic Island, waits for foreground WebSocket bridge reconnection, then executes the requested tool command. Lock-screen Live Activity entries require visual confirmation rather than fixed-coordinate tapping.

PiP Bridge is a narrow exception. When the app reports `pip_bridge_enabled=true` while backgrounded, iOS gives PiP priority over the Dynamic Island, so the Dynamic Island return entry is not visible. The HTTP/Tool Lab catalog remains complete for direct diagnostics, while the conversational Agent catalog is filtered from live runtime capabilities before each run. `open_app` remains exposed because it can fall back to SearchLaunchApp; only executable background-safe data tools (`bridge_clipboard`, `bridge_calendar`, `bridge_contacts`, `bridge_notification`) are exposed through the HTTP queue, and unavailable App actions are omitted.

BLE Wake provides an on-demand iOS background route for a narrower command set: calendar create/query/delete, contacts query/create, and local notification send. Clipboard read/write is not reliable in the iOS background, and contacts update is not exposed through BLE Wake; those calls return an explicit `app_backgrounded` error unless foreground Phone Bridge, PiP, or Dynamic Island restoration provides another executable route. Before each Agent run, runtime capability filtering checks whether `ble_service` reports a connected Wake subscriber and exposes only executable tools and actions. The command is then placed in the existing HTTP queue and the board makes a best-effort `wake` call. BLE carries only the wake hint: the app polls with its `phone_id`, executes the native module, and posts the structured result over HTTP. If BLE is unavailable, those App-only tools are omitted unless another route such as foreground Phone Bridge, PiP/FGS polling, or Dynamic Island restoration is usable; a failed Wake notify never removes an already queued command.

Android FGS Bridge follows the same HTTP queue contract without using WebSocket as a background transport. When the Android foreground service polls `/api/phone-bridge/commands` with `app_state=background` and `fgs_bridge_enabled=true`, the Agent keeps `open_app` unavailable and routes only background-safe data tools through the HTTP queue.

## Desktop Agent With ADB Reverse

When running the Agent on a development computer instead of the Luckfox board, the phone cannot reach `192.168.42.1` because the USB ECM board network does not exist. For Android development, use the ADB input backend and let the phone app connect through ADB reverse:

```text
Phone relay app -> ws://127.0.0.1:8080/api/phone-bridge
ADB reverse     -> host computer 127.0.0.1:8080
```

When `[hid].input_backend = "adb"` is active, the desktop Agent attempts to configure the reverse mapping automatically at startup:

```bash
adb reverse tcp:8080 tcp:8080
```

The companion app keeps `192.168.42.1` as the first target, then falls back to the desktop ADB reverse target when the board API is unavailable. Android board-network binding is skipped for loopback URLs so the app can reach the ADB reverse socket.

For TTS replies in this desktop/PC Agent mode, keep `audio.playback_backend = "auto"` or set it to `"local"`. The Agent will synthesize TTS normally, wrap the PCM as a temporary WAV, and play it through the host OS player instead of `audio_service`.

## App Opening Flow

```text
Board HDMI sees screen / user issues task
        │
        ▼
AI identifies target app, e.g., "WeChat"
        │
        ▼
Look up mapping table
iOS: weixin://
Android: com.tencent.mm
        │
        ▼
Public open_app reads the live relay-app state:
If foreground Phone Bridge is ready:
    Send semantic open_app command
Otherwise:
    Use SearchLaunchApp through the visible system UI
        │
        ▼
Relay app executes:
iOS: openURL("weixin://")
Android: Intent launch package name
        │
        ├─ HDMI verification success -> continue next step
        └─ Failure / timeout -> fallback to hardware simulation: find icon coordinates + HID tap
```

## Key Boundaries

On iOS, the relay app is not a background-resident system agent. It's more like a foreground fast-path executor; Dynamic Island can be used as the automatic entry point back to Aiden, while lock-screen Live Activity cards need visual confirmation:

```text
Aiden App foreground
-> Receives board command
-> openURL opens WeChat
-> Aiden App enters background
-> Subsequent operations continue via hardware HDMI observation + HID operation
```

So iOS cannot promise long-term background command reception. Android can be more stable through foreground service, with stronger background long-connection capability.

## iOS Key Points

- Don't use `canOpenURL` for large-scale pre-checking.
- Directly call `openURL(url)` to attempt opening.
- `LSApplicationQueriesSchemes` mainly limits `canOpenURL` queries, not dynamic `openURL` attempts.
- Maintain scheme mapping table on board side or cloud for easy updates.
- `openURL` returning success doesn't mean target page is fully usable; ultimately verify via HDMI visually.

## Android Key Points

- Prioritize launching by package name: `getLaunchIntentForPackage("com.tencent.mm")`.
- Android 11+ has package visibility restrictions, need to configure `<queries>`, or evaluate `QUERY_ALL_PACKAGES` for specific scenarios.
- Background Activity launching has system restrictions, but availability after foreground service, notifications, and user authorization is stronger than iOS.

## Technology Selection

React Native can serve as UI and business framework, but cannot assume completely native-free:

- Simple `openURL`, WebSocket, UI: RN can cover directly.
- Android package name launch, foreground service, package visibility: need Kotlin / Java native module.
- iOS local network permissions, calendar, contacts, HealthKit, App Intents: need Swift / native configuration.
- Not recommended Expo Go; recommend bare React Native, or Expo Prebuild + config plugin. Long-term bare RN is more stable.

### Whether to Modify Existing Template

The final form will evolve to "chat + config + Skill + memory management + bridge execution", a complete app, not a simple dispatcher. But **not recommended to directly fork AI conversation templates from GitHub**:

- Most are tied to OpenAI / direct LLM connection, conflicting with "connect to board WebSocket" positioning
- Often bundled with login, subscription, cloud sync and other unrelated features; removal cost higher than building from scratch
- Most based on Expo Go, conflicting with this project's need for native modules
- Tutorial-level projects mostly, not production-grade code

Can **reference** structure and fragments (like `react-native-gifted-chat` official example, chatbot-ui project message flow handling ideas), but not directly as code baseline.

### Recommended Combination: bare RN + mature component libraries

Don't look for complete templates; stand on wheels and assemble:

| Need | Recommendation |
| --- | --- |
| Project skeleton | `npx @react-native-community/cli init AidenBridge` (bare RN) |
| Navigation | `@react-navigation/native` + native-stack |
| Chat UI | `react-native-gifted-chat`, or build with `FlashList` |
| State management | `zustand` (lightweight, not as heavy as Redux) |
| Config / memory storage | `react-native-mmkv` (tens of times faster than AsyncStorage) |
| Markdown rendering | `react-native-markdown-display` |
| WebSocket | RN built-in `WebSocket` API |

Rationale:

1. Phase one only needs WebSocket + `openURL`, can get on board for joint debugging in a few days, won't be blocked by UI
2. Chat interface can be added incrementally later, doesn't affect core bridge link
3. Native modules (package name launch, Calendar, Contacts, foreground service) must be written yourself, templates can't help
4. Maintaining your own understood codebase > maintaining a modified someone else's codebase

## Why WebSocket on Top of USB Network

USB ECM (`192.168.42.1`) and WebSocket are two layers:

| Layer | Function |
| --- | --- |
| USB ECM | Network link — enables IP packet communication between phone and board |
| WebSocket | Application protocol — transmits commands and acknowledgments on this link |

USB ECM is "road is built", WebSocket is "vehicles running on the road".

The existing `192.168.42.1:80` config page is HTTP request-response mode, suitable for human web operations. But the board needs to **actively push commands to phone app** (like "open WeChat now"), which HTTP cannot do — HTTP is client-initiated, server cannot actively talk to client.

WebSocket's core value:

- **Bidirectional real-time**: Board can push commands to app anytime, app can reply anytime
- **Long connection**: No need to rebuild connection each time, low latency
- **State awareness**: Connected means online, disconnection immediately known, triggers HID fallback

## Phase One Implementation Suggestions

1. Board agent adds `phone_bridge` WebSocket channel.
2. Relay app auto-connects to `ws://192.168.42.1:8080/api/phone-bridge` after startup.
3. App sends periodic heartbeat.
4. App actively reports `phone_environment` upon connection success and returning from background to foreground, including system version, language/region, timezone, screen/battery, system apps, third-party candidate app availability, etc.
5. App reports `phone_app_state` when the visible lifecycle state changes among `active`, `background`, and `inactive`, including any available Dynamic Island / Live Activity return entry and PiP Bridge state. Android FGS Bridge reports `fgs_bridge_enabled` through HTTP queue polling.
6. Board maintains `bridge_connected`, `platform`, `last_heartbeat_at`, `app_state`, `return_entry`, `return_entry_available`, `pip_bridge_enabled`, `fgs_bridge_enabled`, and `environment` status. Complete environment is exposed through status API; Agent runtime context only injects summarized connection state, app foreground/background state, return entry visibility, background bridge state, system type/version, language/region/timezone, screen dimensions, confirmed openable third-party candidate apps, etc.

`bridge_connected` only means the WebSocket is currently active. It is not equivalent to USB cable connectivity. After the iOS app enters background, WebSocket may disconnect while USB ECM remains reachable; real-time background Dynamic Island updates should go through Live Activity relay/APNs, not the phone bridge WebSocket.

When `app_state=background|inactive`, `return_entry=dynamic_island`, `return_entry_available=true`, and PiP Bridge mode is not enabled, `open_url` and bridge data tools can click the Aiden Dynamic Island entry, wait for Phone Bridge recovery, then send their commands. `open_app` instead selects SearchLaunchApp whenever foreground Bridge app launch is unavailable. Lock-screen Live Activity entries are not blind-tapped because their screen position is not stable; use screenshot/HID fallback or visual confirmation instead. When `pip_bridge_enabled=true` on iOS or `fgs_bridge_enabled=true` on Android in the background, only background-safe data tools use the HTTP command queue.

### Command Protocol

Board sends `BridgeCommand` to app via WebSocket, app executes and replies with `BridgeCommandResponse`.

#### Common Fields

**BridgeCommand (board → app)**:
```json
{
  "id": "cmd_001",
  "type": "open_app | clipboard_read | clipboard_write | calendar_create | calendar_query | calendar_delete",
  "timeout_ms": 5000,
  "payload": { }  // Optional, command-related JSON (clipboard text, calendar event, etc.)
}
```

**BridgeCommandResponse (app → board)**:
```json
{
  "id": "cmd_001",
  "ok": true,
  "method": "open_url | clipboard | calendar",
  "error": "...",  // Filled when ok=false
  "data": { }      // Optional, returned JSON (clipboard content read, calendar event list, etc.)
}
```

**App Active Event (app → board)**:
```json
{
  "id": "phone_environment",
  "ok": true,
  "method": "phone_environment",
  "data": {
    "platform": "ios",
    "system_name": "iOS",
    "system_version": "18.5",
    "locale": "zh-Hans-CN",
    "time_zone": "Asia/Shanghai",
    "utc_offset": "+08:00",
    "manufacturer": "Apple",
    "model": "iPhone16,2",
    "screen": {"width_pixels": 1179, "height_pixels": 2556, "scale": 3},
    "battery": {"level": 0.87, "charging": true},
    "system_apps": [{"name": "Camera", "available": true, "category": "system", "availability_source": "builtin"}],
    "third_party_apps": [{"name": "WeChat", "available": true, "category": "third_party", "availability_source": "can_open_url"}],
    "available_apps": [{"name": "WeChat", "available": true, "category": "third_party"}]
  }
}
```

`phone_environment` does not correspond to board command ID; board only updates bridge status, won't treat it as tool call acknowledgment.
`system_apps` is system built-in app/capability list; `third_party_apps` is installation/openability probe result. `available_apps` retained only as legacy field for old boards.

#### Command Types

##### 1. `open_app` — Open App

```json
{
  "id": "open_001",
  "type": "open_app",
  "app": "微信",
  "timeout_ms": 10000
}
```

Reply:
```json
{
  "id": "open_001",
  "ok": true,
  "method": "ios_url_scheme"
}
```

App-side `method` represents the underlying mechanism (for example `ios_url_scheme`, `ios_shortcut`, `android_intent`, `android_deeplink`, `launch_package`, or `open_url`). The public `open_app` tool routes semantic app launches to the internal Phone Bridge launcher or visible system search. The public `open_url` tool sends `http`, `https`, `sms`, `mailto`, and `tel` URLs through Phone Bridge. Their results normalize task semantics into `method:"open_app"` or `method:"open_url"`, while the underlying app-side method is returned as `mechanism`.

App-launch and URL semantics are separated: call `open_app` with `{"app":"browser"}` to launch the browser itself, and call `open_url` with a supported URL such as `{"url":"https://example.com"}` or `{"url":"tel:+15551234567"}`. The companion app owns platform-specific URL/package/intent mapping.

##### 2. `clipboard_read` — Read Clipboard

```json
{
  "id": "clip_read_001",
  "type": "clipboard_read",
  "timeout_ms": 5000
}
```

Reply:
```json
{
  "id": "clip_read_001",
  "ok": true,
  "data": {
    "text": "clipboard content"
  }
}
```

##### 3. `clipboard_write` — Write Clipboard

```json
{
  "id": "clip_write_001",
  "type": "clipboard_write",
  "payload": {
    "text": "content to copy"
  },
  "timeout_ms": 5000
}
```

Reply:
```json
{
  "id": "clip_write_001",
  "ok": true
}
```

##### 4. `calendar_create` — Create Calendar Event

```json
{
  "id": "cal_create_001",
  "type": "calendar_create",
  "payload": {
    "title": "Dentist appointment",
    "start": "2026-06-02T15:00:00+08:00",
    "end": "2026-06-02T16:00:00+08:00",
    "all_day": false,
    "location": "Clinic",
    "notes": "Bring insurance card",
    "alarm_minutes_before": 30
  },
  "timeout_ms": 8000
}
```

Reply:
```json
{
  "id": "cal_create_001",
  "ok": true,
  "data": {
    "event_id": "ios_calendar_id_123"
  }
}
```

##### 5. `calendar_query` — Query Calendar Events

```json
{
  "id": "cal_query_001",
  "type": "calendar_query",
  "payload": {
    "start": "2026-06-02T00:00:00+08:00",
    "end": "2026-06-03T00:00:00+08:00"
  },
  "timeout_ms": 8000
}
```

Reply:
```json
{
  "id": "cal_query_001",
  "ok": true,
  "data": {
    "events": [
      {
        "event_id": "...",
        "title": "Dentist appointment",
        "start": "2026-06-02T15:00:00+08:00",
        "end": "2026-06-02T16:00:00+08:00",
        "location": "Clinic"
      }
    ]
  }
}
```

##### 6. `calendar_delete` — Delete Calendar Event

```json
{
  "id": "cal_delete_001",
  "type": "calendar_delete",
  "payload": {
    "event_id": "ios_calendar_id_123"
  },
  "timeout_ms": 8000
}
```

Reply:
```json
{
  "id": "cal_delete_001",
  "ok": true
}
```

##### 7. `contacts_query` — Query Contacts

```json
{
  "id": "contacts_query_001",
  "type": "contacts_query",
  "payload": {
    "query": "Zhang San",
    "limit": 20
  },
  "timeout_ms": 8000
}
```

Reply:
```json
{
  "id": "contacts_query_001",
  "ok": true,
  "data": {
    "contacts": [
      {
        "contact_id": "contact_123",
        "name": "Zhang San",
        "phone_numbers": ["+86 138 1234 5678"],
        "emails": ["zhangsan@example.com"]
      }
    ]
  }
}
```

##### 8. `contacts_create` — Add Contact

```json
{
  "id": "contacts_create_001",
  "type": "contacts_create",
  "payload": {
    "name": "Li Si",
    "phone_numbers": ["+86 139 8765 4321"],
    "emails": ["lisi@example.com"],
    "organization": "Company name",
    "notes": "Notes"
  },
  "timeout_ms": 8000
}
```

Reply:
```json
{
  "id": "contacts_create_001",
  "ok": true,
  "data": {
    "contact_id": "new_contact_id_123"
  }
}
```

##### 9. `contacts_update` — Update Contact

```json
{
  "id": "contacts_update_001",
  "type": "contacts_update",
  "payload": {
    "contact_id": "contact_123",
    "name": "Li Si (updated)",
    "phone_numbers": ["+86 139 8765 4321", "+86 010 1234 5678"],
    "emails": ["lisi_new@example.com"]
  },
  "timeout_ms": 8000
}
```

Reply:
```json
{
  "id": "contacts_update_001",
  "ok": true
}
```

##### 10. `notification_send` — Send Notification

```json
{
  "id": "notification_001",
  "type": "notification_send",
  "payload": {
    "title": "Reminder",
    "body": "Time to take medicine",
    "schedule_at": "2026-06-04T18:00:00+08:00",
    "sound": true,
    "badge": 1
  },
  "timeout_ms": 5000
}
```

The public `bridge_notification` tool supports both companion-app local
notification sending and board-side shared system-notification querying:

```json
{"action":"send","title":"Reminder","body":"Time to take medicine","sound":true}
```

```json
{"action":"query","limit":20}
```

`action=send` continues to use `notification_send` through the companion app.
`action=query` reads `ble_service` directly and returns notification changes,
the current generation, and cursor fields. Omitting `since` returns the latest
retained events. Pass `since=0` to page forward from the oldest retained event;
incremental queries pass the prior `last_id` as `since` together with the prior
`generation`. Querying does not require Aiden to be foregrounded or Phone
Bridge to be connected. On iOS the
events come from ANCS; the same ring can contain Android events forwarded over
the phone-notification ingestion path when that feature is installed.

Reply:
```json
{
  "id": "notification_001",
  "ok": true,
  "data": {
    "notification_id": "notification_123"
  }
}
```

### Time Format

All time fields must be **RFC3339 format with timezone offset**, e.g.:
- `2026-06-02T15:00:00+08:00` (3pm GMT+8)
- `2026-06-02T07:00:00Z` (7am UTC)

Use the phone environment timezone when it is available. The Agent can use `shell` for a controller-time baseline, but must not assume the controller timezone matches the phone.

### Permissions and Privacy

- **Clipboard read**: iOS 16+ shows one-time authorization banner, frequent reads affect experience. Android 10+ requires foreground app or foreground service.
- **Calendar read/write**: Both iOS and Android need runtime permissions, authorization popup on first call. When app receives command and permission not granted, should return `ok:false, error:"Calendar permission required"`; timeout controlled by board-side `timeout_ms`.
- **Contacts read/write**: iOS needs `NSContactsUsageDescription` permission, Android needs `READ_CONTACTS` and `WRITE_CONTACTS` permissions. When unauthorized return `ok:false, error:"Contacts permission required"`.
- **Notification permission**: iOS needs to request authorization via `UNUserNotificationCenter`, Android 13+ needs `POST_NOTIFICATIONS` permission. When unauthorized return `ok:false, error:"Notification permission required"`.

### Implementation Notes

7. `open_app` reads the live companion-app state. When foreground Phone Bridge commands are ready it uses the internal BridgeOpenApp path; otherwise it uses SearchLaunchApp through the visible system UI.
8. `open_url` and bridge data tools may restore a backgrounded iOS Aiden app through a confirmed Dynamic Island entry before sending their command.
9. If `pip_bridge_enabled=true` on iOS or `fgs_bridge_enabled=true` on Android while Aiden is backgrounded, only `bridge_clipboard`, `bridge_calendar`, `bridge_contacts`, and `bridge_notification` use the HTTP command queue. `open_app` uses visible system search, and `open_url` still requires foreground Bridge recovery.
10. Bridge data tools have no reliable HID/API fallback when neither foreground WebSocket, Dynamic Island recovery, nor PiP/FGS HTTP polling is available; they return a clear bridge-unavailable error.

## Final Positioning

This solution doesn't replace hardware control, but adds a software fast path:

```text
What can be completed quickly via software, go through relay app;
What software cannot do or is unstable, continue via HDMI + HID.
```

iOS focuses on "foreground fast path + hardware fallback". Android can gradually enhance to "background resident relay + hardware fallback".
