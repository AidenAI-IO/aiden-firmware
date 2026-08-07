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
This is display-only. The full adapter identity is exposed as `board_identity`
over the USB status API and as six bytes of manufacturer-specific data in the BLE
advertisement. The app requires the service UUID, display name, and full
identity to match before pairing, so nearby boards with colliding name suffixes
cannot be selected accidentally.

The GATT application always publishes the standard HID Service
`00001812-0000-1000-8000-00805f9b34fb`. Its report map contains only one
Consumer Control input report. It does not expose a keyboard Usage Page and
never emits input, so it is a pairing/reconnect anchor rather than a second
control channel competing with USB HID.

`ble_service` starts non-pairable and non-discoverable even when no bond exists.
The iOS app explicitly calls the Agent pairing API over USB ECM; only then does
the service open a five-minute pairing window. The app reads the board's stable
`device_name` and collision-resistant `board_identity` first and only connects
a Wake-service advertiser carrying both values. A successful encrypted Wake
subscription closes the window; an existing bond does not prevent an explicit
Connect action from reopening it. Outside that window, only the selected
trusted phone is authorized. `PAIRING_WINDOW_SECONDS` in
`/etc/aiden_ble_service.conf` controls the maximum user-initiated window.

The five-minute value is an upper bound that tolerates Bluetooth permission and
iOS confirmation delays; the app starts scanning immediately and successful
pairing closes the window early. A service restart never opens the window by
itself. While a user-initiated window is active, the service also reconciles the
actual BlueZ `Pairable` and `Discoverable` properties because bond removal and
other BlueZ state changes can reset them independently of the service state.

The app reports only the live connection state. A bond is a reconnect cache,
not proof that the App Wake session is connected. Every explicit Connect action
reopens the board window; CoreBluetooth reuses a saved peripheral when possible
and otherwise performs the system pairing flow.

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
the last Wake delivery result, and last-error state. A disconnected trusted
device always reports `wake_subscriber=false` and `ancs_subscribed=false`.

### `wake`

```json
{"op":"wake","reason":"phone_bridge"}
```

Returns a string `wake_id` and `delivered`. `delivered=false` means no iOS
central was subscribed; the HTTP queue remains intact.

### `events_since`

```json
{"op":"events_since","since":"42","generation":"<service-generation>","limit":50}
```

Returns the current `generation` with events after the cursor. Start with
`since=0`, save the returned generation, and include it on incremental reads.
After `ble_service` restarts, a stale generation returns `reset_required=true`
with no events; retry with `since=0`. Within one generation, `truncated=true`
means the requested cursor is older than the bounded ring's retained history.

### `pairing_start`

```json
{"op":"pairing_start"}
```

Opens or refreshes the configured connection window. Existing bonds are kept
and do not cause a conflict; the window closes after the App establishes the
encrypted Wake subscription or when the deadline expires.

### `pairing_forget`

```json
{"op":"pairing_forget"}
```

Calls BlueZ `Adapter1.RemoveDevice` for board-side bonds and returns the removal
count plus the latest Bluetooth status. This is a local UDS maintenance
operation and is not exposed through the companion App HTTP API. The normal App
disconnect flow preserves both sides of the system bond.

## Agent HTTP API

The companion app reaches the pairing operations through the Agent on USB ECM:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/bluetooth/status` | Read BLE runtime and live connection state |
| `POST` | `/api/bluetooth/pairing/start` | Open or refresh the user-initiated connection window |

The pairing write is accepted only over the board's USB ECM address
(`192.168.42.1/24`) or loopback; requests arriving through Wi-Fi and other
listeners receive `403`. The normal app flow disconnects its CoreBluetooth
session without deleting the system bond. BLE keys, ANCS bodies, and Phone
Bridge command payloads never pass through these endpoints.

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
