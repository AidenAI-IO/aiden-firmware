---
sidebar_position: 11
---

# Phone Bridge

## Purpose

The Aiden companion app connects to the hardware board over USB ECM and executes phone-side operations that are available through public iOS and Android APIs.

The board remains responsible for screen observation, task planning, and HID fallback. Phone Bridge is a software fast path for operations such as launching apps, accessing the clipboard, and working with calendars, contacts, and notifications.

## Capabilities

| Capability | iOS | Android | Problem Solved |
| --- | --- | --- | --- |
| Open an app or URL | URL scheme / Universal Link / `openURL` | Package name / Intent / deep link | Avoid searching for an icon when the OS exposes a direct launch path. |
| Clipboard read/write | `UIPasteboard` | Clipboard API | Exchange text without UI-driven copy and paste. |
| Calendar | EventKit | Calendar provider | Query, create, and delete calendar events. |
| Contacts | Contacts framework | Contacts provider | Query, create, and update contacts. |
| Notification | Local notification | Notification API | Deliver a local user notification. |
| Board communication | Foreground WebSocket; limited background recovery | Foreground WebSocket and FGS HTTP polling | Receive commands and return structured results. |

## Communication Method

The board has the fixed USB-network address `192.168.42.1`. The companion app acts as the client and connects to:

```text
Phone relay app -> ws://192.168.42.1:8080/api/phone-bridge
```

The board is the WebSocket server, so it does not need to discover the phone's DHCP address and the app does not need to expose a local server.

The board also exposes `/api/phone-bridge/commands` and `/api/phone-bridge/results` HTTP queue endpoints, but React Native JS, WebSocket, and polling timers in the iOS background must not be treated as a general tool execution path. On iOS, Phone Bridge is normally a foreground fast path: if Aiden is backgrounded and the app has reported `return_entry=dynamic_island`, Agent restores Aiden through Dynamic Island, waits for foreground WebSocket bridge reconnection, then executes the requested tool command. Lock-screen Live Activity entries require visual confirmation rather than fixed-coordinate tapping.

PiP Bridge is a narrow exception. When the app reports `pip_bridge_enabled=true` while backgrounded, iOS gives PiP priority over the Dynamic Island, so the Dynamic Island return entry is not visible. The public tool catalog remains static: `open_app` selects its internal SearchLaunchApp route, while only background-safe data tools (`bridge_clipboard`, `bridge_calendar`, `bridge_contacts`, `bridge_notification`) can execute through the HTTP queue.

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

## Companion App Implementation

The companion app is a bare React Native application. React Native owns the UI, WebSocket client, foreground command dispatch, and shared business logic. Platform capabilities are implemented by native Swift and Kotlin/Java modules where the operating system does not expose the required behavior directly to JavaScript.

Important native areas include:

- iOS local-network permissions, URL launching, calendar, contacts, notifications, Live Activity, and PiP Bridge integration.
- Android package/intent launching, package visibility, notifications, board-network binding, and foreground-service polling.

See the related `aiden-app` repository for the companion-app source and platform permission declarations.

## Why WebSocket on Top of USB Network

USB ECM (`192.168.42.1`) and WebSocket are two layers:

| Layer | Function |
| --- | --- |
| USB ECM | Network link — enables IP packet communication between phone and board |
| WebSocket | Application protocol — transmits commands and acknowledgments on this link |

USB ECM is "road is built", WebSocket is "vehicles running on the road".

The `192.168.42.1:80` config page uses ordinary HTTP request-response behavior. Phone Bridge uses WebSocket because the board must deliver commands as soon as the foreground app is connected, while the app must return results over the same connection.

WebSocket's core value:

- **Bidirectional real-time**: Board can push commands to app anytime, app can reply anytime
- **Long connection**: No need to rebuild connection each time, low latency
- **State awareness**: Connected means online, disconnection immediately known, triggers HID fallback

## Connection and Runtime State

- The app connects to `ws://192.168.42.1:8080/api/phone-bridge` and sends heartbeats while the foreground connection is active.
- On connection and foreground transitions, it reports `phone_environment`, including platform, system version, language/region, timezone, screen information, and confirmed app availability.
- It reports `phone_app_state` when the visible lifecycle changes among `active`, `background`, and `inactive`, including Dynamic Island, Live Activity, PiP Bridge, or Android FGS state when available.
- The board exposes connection, lifecycle, return-entry, background-bridge, and environment data through the Phone Bridge status API. The Agent context receives a compact summary rather than the entire environment payload.

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
- **Notification permission**: iOS requires authorization through `UNUserNotificationCenter`; Android 13+ requires the `POST_NOTIFICATIONS` permission. When authorization is missing, return `ok:false, error:"Notification permission required"`.

### Runtime Routing

- `open_app` reads live companion-app state. It uses the foreground Bridge path when ready and visible system search otherwise.
- `open_url` and bridge data tools may restore a backgrounded iOS Aiden app through a confirmed Dynamic Island entry before sending a command.
- With iOS PiP Bridge or Android FGS Bridge active in the background, only `bridge_clipboard`, `bridge_calendar`, `bridge_contacts`, and `bridge_notification` use the HTTP queue. `open_app` continues to use visible system search, and `open_url` requires foreground Bridge recovery.
- Bridge data tools return a bridge-unavailable error when foreground WebSocket, Dynamic Island recovery, and PiP/FGS polling are all unavailable.

## Control Boundary

Phone Bridge does not replace hardware control. It adds a software fast path:

```text
What can be completed quickly via software, go through relay app;
What software cannot do or is unstable, continue via HDMI + HID.
```

iOS uses a foreground fast path with limited background recovery and hardware fallback. Android additionally supports background-safe commands through foreground-service polling. In both cases, HDMI observation remains the final verification path when task completion depends on visible phone state.
