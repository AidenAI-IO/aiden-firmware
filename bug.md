# Bug: PiP visible and JavaScript alive, but Phone Bridge HTTP polling stops

## Summary

On iOS, the PiP window remained visible and the Phone Bridge WebSocket heartbeat
continued, but the companion app stopped polling the board's background command
queue. A background-safe Bridge command was enqueued and then timed out because
the app never fetched it. Once the board's 15-second background-state freshness
window expired, Bridge data tools such as `bridge_contacts`, `bridge_clipboard`,
`bridge_calendar`, and `bridge_notification` disappeared from the dynamic tool
catalog.

This is not consistent with general iOS JavaScript suspension: the WebSocket
heartbeat uses a JavaScript `setInterval` and remained active throughout the
incident.

## Environment

- Date: 2026-07-17
- Board Agent: `20260717-063131-d9911f2`
- Board firmware base: `20260717-050131-da3bc85`
- Phone platform: iOS 26.5.2
- Phone Bridge mode: PiP enabled and visibly active
- App code inspected: `/Users/qing/Documents/project/aiden-app-pip-bridge-mode-persistence`
- App branch inspected: `fix/pip-bridge-mode-persistence` at `c88c596`
- Installed app commit/build: not reported by the current Phone Bridge status payload

## Expected behavior

While the PiP bridge is active in the background:

1. JavaScript remains active.
2. The app polls `GET /api/phone-bridge/commands` every 3 seconds.
3. Background-safe Bridge tools remain available.
4. Queued clipboard, contacts, calendar, and notification commands are fetched,
   executed, and acknowledged before their tool context expires.

## Actual behavior

1. PiP remained visible.
2. WebSocket heartbeat remained current, proving that JavaScript timers were
   still executing.
3. No new HTTP command polls reached the board after the app entered the
   background.
4. A queued contacts command was never fetched and was canceled after 10 seconds.
5. After `phoneBridgeBackgroundStateMaxAge` elapsed, the board hid the affected
   Bridge tools because it no longer had evidence of a fresh background polling
   transport.

## Relevant board status

Captured at approximately 2026-07-17 14:37 China Standard Time:

```json
{
  "connected": true,
  "platform": "ios",
  "last_heartbeat_at": "2026-07-17T06:37:30.980226138Z",
  "app_state": "background",
  "app_state_updated_at": "2026-07-17T06:35:58.07Z",
  "return_entry": "none",
  "return_entry_available": false,
  "pip_bridge_enabled": true
}
```

The important contradiction is that `last_heartbeat_at` was fresh while
`app_state_updated_at` had been stale for more than 90 seconds. In this setup,
HTTP queue polling normally refreshes the background state every few seconds.

## Relevant logs

Board Agent log excerpt:

```text
2026/07/17 06:35:32 [INFO] phone-bridge: client connected (platform=ios ...)
2026/07/17 06:35:32 [INFO] phone-bridge: app state updated (background)
2026/07/17 06:35:55 [INFO] phone-bridge: client disconnected
2026/07/17 06:35:55 [INFO] phone-bridge: client connected (platform=ios ...)
2026/07/17 06:35:55 [INFO] phone-bridge: app state updated (active)
2026/07/17 06:35:58 [INFO] phone-bridge: app state updated (inactive)
2026/07/17 06:35:58 [INFO] phone-bridge: app state updated (background)
2026/07/17 06:35:59 [INFO] Starting agent run: input="query a contact and write it to Notes"
2026/07/17 06:36:04 [INFO] phone-bridge-queue: enqueued command contacts_query_... (type=contacts_query)
2026/07/17 06:36:14 [INFO] phone-bridge-queue: canceled command contacts_query_...
```

There is no `phone-bridge-queue: polled ...` entry between enqueue and cancel.

Dynamic HTTP tool catalog after the freshness window expired:

```text
enter_text_in_field
enter_text_via_bridge
```

The following tools were absent:

```text
bridge_clipboard
bridge_calendar
bridge_contacts
bridge_notification
```

`bridge_open_app` being absent in PiP mode is expected because it is not a
background-safe command. The absence of the data tools is a consequence of the
stale background polling evidence.

## Board-side timeout sequence

The command was submitted during the short period in which the most recent PiP
background state was still considered fresh:

1. Background state reported at `06:35:58`.
2. Command enqueued at `06:36:04`.
3. App did not request `/api/phone-bridge/commands`.
4. Tool context expired and canceled the command at `06:36:14`.
5. After 15 seconds without a fresh poll, `phoneBridgeToolAvailable` hid the
   background data tools.

Relevant board code:

- `src/agent/internal/agent/phone_bridge_policy.go`
  - `phoneBridgeBackgroundStateMaxAge = 15 * time.Second`
  - `phoneBridgeCanUsePiPBackground`
  - `phoneBridgeToolAvailable`
- `src/agent/internal/agent/phone_bridge_restore.go`
  - `sendRoutedBridgeCommand`
  - PiP background commands use `SendQueuedCommand`

The board's decision to hide stale tools is protective and should not be removed
without another reliable transport-health signal. Otherwise calls would remain
visible but continue timing out.

## Likely app-side failure mode

The exact stuck promise still needs confirmation from an iOS console log, but
the current code has a high-probability deadlock path.

`PhoneBridge.pollHttpCommands()` sets an in-flight latch before performing Live
Activity work:

```ts
if (this.isHttpPolling) {
  return;
}

this.isHttpPolling = true;
try {
  const syncedLiveActivity = await this.syncCurrentLiveActivity();
  if (!syncedLiveActivity) {
    await this.ensureBackgroundReturnEntry();
  }
  const commands = await this.httpClient.pollCommands(...);
  // ...
} finally {
  this.isHttpPolling = false;
}
```

`syncCurrentLiveActivity()` calls `fetch()` without an `AbortController` timeout
and may also wait on native Live Activity synchronization. If that chain never
settles:

1. `isHttpPolling` remains `true`.
2. Every later 3-second timer tick exits at the `isHttpPolling` guard.
3. `/api/phone-bridge/commands` is never reached again.
4. The separate WebSocket heartbeat timer continues normally.

This matches the observed combination of fresh JavaScript heartbeat, visible
PiP, and no HTTP queue polls.

Relevant app code:

- `src/services/PhoneBridge.ts`
  - `startHttpPolling`
  - `pollHttpCommands`
  - `syncCurrentLiveActivity`
  - `ensureBackgroundReturnEntry`
- `src/services/PhoneBridgeHTTP.ts`
  - `pollCommands`
- `src/services/PipBridgeMode.ts`
- `ios/AidenBridge/AidenPipBridgeModule.swift`

## Why this occurrence may have been intermittent

The board Agent had recently restarted during deployment. The app then
reconnected and transitioned from foreground to PiP background mode. A transient
HTTP or Live Activity synchronization stall during the first background poll
could wedge the in-flight latch permanently. Previous tests would pass whenever
that pre-poll work completed normally.

This explanation is strongly consistent with the code and board logs, but an
iOS console excerpt containing `Starting HTTP background polling`, Live Activity
sync warnings, or missing subsequent poll messages is still needed to confirm
the exact awaited operation.

## Recommended fix

1. Make command delivery independent from Live Activity maintenance.
   - Poll `/api/phone-bridge/commands` first.
   - Run Live Activity synchronization separately as best-effort work.
2. Add explicit timeouts to:
   - `syncCurrentLiveActivity()` HTTP fetch;
   - background return-entry synchronization;
   - the overall `pollHttpCommands()` iteration.
3. Add a watchdog for `isHttpPolling` so one stuck iteration cannot block all
   future polls.
4. Track and report a dedicated transport-health field such as
   `last_background_poll_at` or `pip_transport_active`.
5. Keep board tool availability based on recent successful command polling,
   rather than PiP visibility alone.
6. Include the installed app build version in the Phone Bridge environment/status
   payload to make future cross-version diagnosis possible.

## Suggested verification

1. Start PiP and background Aiden.
2. Confirm `/api/phone-bridge/commands` reaches the board every 3 seconds for at
   least 2 minutes.
3. Restart the board Agent while PiP remains visible.
4. Confirm polling recovers automatically after reconnect.
5. Introduce an artificial timeout/failure in Live Activity synchronization.
6. Confirm command polling continues and Bridge data tools remain available.
7. Queue contacts, clipboard, calendar, and notification commands and verify each
   is fetched and acknowledged.
8. Confirm `bridge_open_app` remains unavailable while Aiden is backgrounded in
   PiP mode.

