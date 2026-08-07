# BLE Service: iOS Notifications and Background Wake

`ble_service` owns Aiden's Bluetooth Low Energy integration. It uses BlueZ on
`hci0` in two roles at the same time:

- BLE Peripheral: advertises an Aiden Wake service that the iOS companion app
  subscribes to.
- BLE HID Peripheral: exposes a minimal Consumer Control HOGP service so iOS
  keeps a manageable system device entry and can automatically reconnect after
  the app-orchestrated first pairing.
- ANCS Consumer: subscribes to Apple's Notification Center Service exposed by
  a paired iPhone and normalizes system notification events.

BLE is intentionally a narrow transport. It does not carry Phone Bridge tool
commands or results, and ANCS events are not written to Agent memory. Phone
Bridge commands continue to use WebSocket or the existing HTTP queue.

## Boot and Persistence

The Pico Zero kernel must merge both fragments:

```text
aiden-zram.config rv1106-bt.config
```

Runtime startup order is:

```text
S35wifidrv -> S39hciinit -> S40bluetoothd -> S41ble_service
```

`S39hciinit` attaches the AIC8800 controller on `/dev/ttyS1` at 1.5 Mbaud and
brings up `hci0`. `S40bluetoothd` bind-mounts the BlueZ state directory before
starting the daemon, then supervises both `hci0` and `bluetoothd`. If the
controller disappears it reruns HCI initialization before restarting BlueZ:

```text
/userdata/ble_service/bluetooth -> /var/lib/bluetooth
```

This preserves the iPhone bond across reboot and firmware rootfs replacement.

## Pairing and HOGP

The advertised name is derived from the configured base name and the final
four hexadecimal digits of the adapter address, for example `Aiden-12AB`.
This gives the phone app and the user a stable way to distinguish nearby Aiden
devices.

The GATT application always publishes the standard HID Service
`00001812-0000-1000-8000-00805f9b34fb`. Its report map contains only one
Consumer Control input report. It does not expose a keyboard Usage Page and
never emits input, so it is a pairing/reconnect anchor rather than a second
control channel competing with USB HID.

`ble_service` starts non-pairable and non-discoverable even when no bond exists.
The iOS app explicitly calls the Agent pairing API over USB ECM; only then does
the service open a five-minute pairing window. The app reads the board's stable
`device_name` first and only connects a Wake-service advertiser with that exact
name, so it does not bind an arbitrary nearby Aiden. The first paired phone is
marked trusted, the window closes immediately, and new devices are rejected by
the BlueZ pairing agent. Existing bonds can reconnect while the adapter remains
non-discoverable. `PAIRING_WINDOW_SECONDS` in `/etc/aiden_ble_service.conf`
controls the maximum user-initiated window.

The five-minute value is an upper bound that tolerates Bluetooth permission and
iOS confirmation delays; the app starts scanning immediately and successful
pairing closes the window early. A service restart never opens the window by
itself.

If a bond predates the app's saved CoreBluetooth peripheral identifier, the app
can attach to that existing bond without opening a new pairing window. After a
bond exists, HOGP reconnection and board-side ANCS continue even when the app is
not running.

## Wake GATT Contract

| Item | UUID |
| --- | --- |
| Wake Service | `a1de0001-7c4b-4f52-8d9a-6b4f6e6f7469` |
| Wake Characteristic | `a1de0002-7c4b-4f52-8d9a-6b4f6e6f7469` |

The characteristic supports encrypted `read` and `notify`. A notification is
a 12-byte little-endian value:

```text
byte 0      protocol version (1)
byte 1      reason (0 unknown, 1 Phone Bridge queue, 2 manual, 3 system)
bytes 2-3   reserved
bytes 4-11  uint64 wake sequence
```

The iOS app treats this only as a native wake hook and immediately polls the
HTTP command queue when backgrounded. No command payload is decoded from BLE.

## ANCS Event Shape

The service subscribes to ANCS Notification Source and Data Source, requests
notification attributes through Control Point, and stores a bounded in-memory
ring. Events include:

- monotonic string `id` for UDS cursors;
- ANCS `notification_uid`, event type, flags, category, and category count;
- app identifier, title, subtitle, message, and date when attribute retrieval
  succeeds;
- `metadata_complete` and `metadata_error` when a disconnect or protocol error
  prevents a complete attribute response.

The ring defaults to 512 events. Reboot clears events but does not clear the
Bluetooth bond.

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

Returns adapter, HOGP registration, pairing window, trusted bond,
advertisement, Wake subscriber, connected iPhone, ANCS subscription, cursor,
and last-error state.

### `wake`

```json
{"op":"wake","reason":"phone_bridge"}
```

Returns a string `wake_id` and `delivered`. `delivered=false` means no iOS
central was subscribed; the HTTP queue remains intact.

### `events_since`

```json
{"op":"events_since","since":"42","limit":50}
```

Returns events after the cursor. `truncated=true` means the requested cursor is
older than the bounded ring's retained history.

### `pairing_start`

```json
{"op":"pairing_start"}
```

Opens the configured pairing window only when no trusted bond exists. An
existing trusted phone returns `FAILED_PRECONDITION`.

### `pairing_forget`

```json
{"op":"pairing_forget"}
```

Calls BlueZ `Adapter1.RemoveDevice` for board-side bonds and returns the removal
count plus the latest Bluetooth status. The app also clears its saved
CoreBluetooth identifier. iOS does not expose a public API for deleting the
phone-side bond, so the UI then instructs the user to choose **Forget This
Device** in iOS Bluetooth Settings.

## Agent HTTP API

The companion app reaches the pairing operations through the Agent on USB ECM:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/bluetooth/status` | Read BLE runtime and bond state |
| `POST` | `/api/bluetooth/pairing/start` | Open the user-initiated pairing window |
| `POST` | `/api/bluetooth/pairing/forget` | Remove board-side bonds |

These endpoints only orchestrate state. BLE keys, ANCS bodies, and Phone Bridge
command payloads never pass through them.

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

Changing the HOGP report map after a phone has bonded requires clearing the old
bond and pairing again. Do not evolve this profile silently on deployed units.
