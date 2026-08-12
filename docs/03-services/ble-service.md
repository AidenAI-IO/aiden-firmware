# BLE Service: Pairing and Phone System Notifications

`ble_service` owns Aiden's Bluetooth Low Energy integration. It uses BlueZ on
`hci0` in two roles at the same time:

- BLE Peripheral: advertises a small Aiden pairing service used to establish an
  encrypted bond with the iOS companion app.
- ANCS Consumer: subscribes to Apple's Notification Center Service exposed by
  the paired iPhone and normalizes system notification events.
- Android notification sink: accepts normalized notification changes forwarded
  by the companion app over the USB-restricted Agent HTTP API.

BLE is intentionally narrow. It does not carry Phone Bridge tool commands or
results, and phone notification events are not written to Agent memory.

## Boot and Persistence

The Pico Zero kernel must merge both fragments:

```text
aiden-zram.config rv1106-bt.config
```

Runtime startup order is:

```text
S35wifidrv -> S39hciinit -> S40bluetoothd -> S41ble_service
```

`S39hciinit` loads the AES/CMAC crypto required by LE SMP, then attaches the
AIC8800 controller on `/dev/ttyS1` at 1.5 Mbaud without powering it through the
legacy `HCIDEVUP` ioctl. `S40bluetoothd` bind-mounts the BlueZ state directory,
starts the daemon, and lets BlueZ power `hci0` through the management API. This
ordering is required for the kernel to register the LE SMP fixed channel used
by encrypted GATT reads. If the controller disappears, the watchdog repeats
the same initialization sequence before restarting BlueZ.

BlueZ state is persisted through:

```text
/userdata/ble_service/bluetooth -> /var/lib/bluetooth
```

This preserves the iPhone bond across reboot and firmware rootfs replacement.

## Pairing and Bonding

The advertised name is derived from the configured base name and the final
four hexadecimal digits of the adapter address, for example `Aiden-12AB`.
This is display-only. The full adapter identity is exposed as `board_identity`
over the USB status API and as six bytes of manufacturer-specific data in the
BLE advertisement. The app requires the service UUID, display name, and full
identity to match before pairing so nearby boards with colliding name suffixes
cannot be selected accidentally.

The pairing characteristic supports an encrypted read. The companion app
performs that read after connecting, causing iOS to start SMP and show the
system pairing sheet. A successful read closes the user-initiated pairing
window. The characteristic has no notify capability and carries no application
payload. No HOGP service is registered; USB remains the only HID control path.

`ble_service` starts non-pairable and non-discoverable even when no bond exists.
The iOS app explicitly calls the Agent pairing API over USB ECM; only then does
the service open a five-minute pairing window. Outside that window, only the
selected trusted phone is authorized. `PAIRING_WINDOW_SECONDS` in
`/etc/aiden_ble_service.conf` controls the maximum window.

The window is an upper bound that tolerates Bluetooth permission and iOS
confirmation delays. A service restart never opens it by itself. While a
window is active, the service reconciles the actual BlueZ `Pairable` and
`Discoverable` properties because BlueZ state changes can reset them
independently. The advertisement remains registered for the complete
`ble_service`/BlueZ lifetime; opening or closing a pairing window changes only
adapter and pairing-agent state.

A bond is a reconnect cache, not proof that a live connection exists. Every
explicit Connect action reopens the board window; CoreBluetooth reuses a saved
peripheral when possible and otherwise performs the system pairing flow. If
iOS has forgotten the board while BlueZ still has its old key, the encrypted
read fails and the app can request the USB-restricted pairing reset endpoint
before one fresh attempt.

## Pairing GATT Contract

| Item | UUID |
| --- | --- |
| Pairing Service | `a1de0001-7c4b-4f52-8d9a-6b4f6e6f7469` |
| Pairing Characteristic | `a1de0002-7c4b-4f52-8d9a-6b4f6e6f7469` |

The characteristic supports only `encrypt-read`. Its purpose is to trigger and
verify the encrypted system bond before ANCS is used.

## Phone Notification Event Shape

After the trusted iPhone is connected and its services are resolved,
`ble_service` discovers ANCS and subscribes directly to Notification Source and
Data Source. It requests notification attributes through Control Point and
stores a bounded in-memory ring. Events include:

- monotonic string `id` for UDS cursors;
- `source`, source notification/event IDs, and the companion `device_id` when
  the event came from Android;
- ANCS `notification_uid`, event type, flags, category, and category count;
- app identifier, title, subtitle, message, and date when attribute retrieval
  succeeds;
- `metadata_complete` and `metadata_error` when a disconnect or protocol error
  prevents a complete attribute response.

The ring defaults to 512 events. Reboot clears events but does not clear the
Bluetooth bond.

On Android, the companion app uses `NotificationListenerService` after the
user grants Notification Access. It filters the Aiden app's own notifications
and group summaries, queues added/modified/removed events locally, and posts
batches to `/api/phone-notifications/events` over USB ECM. Each source event has
a stable `source_event_id`; `ble_service` deduplicates retries while that event
remains in the bounded ring. Android notification forwarding does not require
Bluetooth pairing.

## UDS API

Default socket:

```text
/run/ble_service/ble_service.sock
```

The framing is the common 12-byte UDS envelope documented in
[Unix Domain Socket Protocol](../06-protocols/uds-protocol.md).

### `status`

```json
{"op":"status"}
```

Returns adapter and GATT registration, advertisement state, pairing window,
trusted bond, live iPhone connection, ANCS subscription, event cursor,
`pairing_service_uuid`, `pairing_characteristic_uuid`, and the last error. A
disconnected trusted device always reports `ancs_subscribed=false`.

### `events_since`

```json
{"op":"events_since","since":"42","generation":"<service-generation>","limit":50}
```

Returns the current `generation` with events after the cursor. Start with
`since=0`, save the returned generation, and include it on incremental reads.
After `ble_service` restarts, a stale generation returns `reset_required=true`
with no events; retry with `since=0`. Within one generation, `truncated=true`
means the requested cursor is older than the bounded ring's retained history.

### `notification_publish`

```json
{
  "op": "notification_publish",
  "phone_id": "android-...",
  "events": [
    {
      "source_id": "0|com.example.mail|42|null|1000",
      "source_event_id": "<sha256>",
      "event": "added",
      "app_identifier": "com.example.mail",
      "title": "New message",
      "message": "Hello"
    }
  ]
}
```

Accepts 1-8 Android notification events, validates bounded metadata fields,
sets `source=android` and the request `device_id`, and returns accepted and
duplicate counts. This operation is intended for the Agent HTTP bridge rather
than arbitrary local publishers.

### `pairing_start`

```json
{"op":"pairing_start"}
```

Opens or refreshes the configured connection window. Existing bonds are kept
and do not cause a conflict; the window closes after the app completes the
encrypted pairing-characteristic read or when the deadline expires.

### `disconnect`

```json
{"op":"disconnect"}
```

Calls BlueZ `Device1.Disconnect` for the current phone connection, clears live
ANCS state, and keeps the paired/trusted device plus its bond keys for a later
direct reconnect.

### `pairing_forget`

```json
{"op":"pairing_forget"}
```

Calls BlueZ `Adapter1.RemoveDevice` for board-side bonds and returns the removal
count plus the latest Bluetooth status. It remains a local maintenance
operation; the Agent exposes it only through the USB-restricted
`/api/bluetooth/pairing/reset` recovery endpoint. Normal disconnects preserve
both sides of the system bond.

## Agent HTTP API

The companion app reaches the pairing operations through the Agent on USB ECM:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/bluetooth/status` | Read BLE runtime and live connection state |
| `POST` | `/api/bluetooth/pairing/start` | Open or refresh the user-initiated connection window |
| `POST` | `/api/bluetooth/pairing/reset` | Remove a confirmed stale board bond before one fresh pairing attempt |
| `POST` | `/api/bluetooth/disconnect` | Disconnect the physical BLE/ANCS link without deleting the bond |
| `POST` | `/api/phone-notifications/events` | Ingest a batch of Android notification changes |

Bluetooth control and phone-notification writes are accepted only over the board's USB ECM address
(`192.168.42.1/24`) or loopback; requests arriving through Wi-Fi and other
listeners receive `403`. Android notification bodies use this local USB path;
BLE keys and iOS ANCS bodies never pass through these endpoints.

## Operations

```bash
/etc/init.d/S39hciinit status
/etc/init.d/S40bluetoothd status
/etc/init.d/S41ble_service status
```

Logs:

```text
/var/log/aiden-hciattach.log
/var/log/bluetoothd/bluetoothd.log
/var/log/ble_service/ble_service.log
```

Useful checks:

```bash
hciconfig -a
bluetoothctl show
ls -l /userdata/ble_service/bluetooth
```

Before the app requests pairing, `bluetoothctl show` should report
`Pairable: no` and `Discoverable: no`. After `pairing_start` both become `yes`
until pairing succeeds or the configured deadline expires.
