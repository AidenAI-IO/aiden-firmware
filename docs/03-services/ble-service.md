# BLE Service: iOS Notifications and Background Wake

`ble_service` owns Aiden's Bluetooth Low Energy integration. It uses BlueZ on
`hci0` in two roles at the same time:

- BLE Peripheral: advertises an Aiden Wake service that the iOS companion app
  subscribes to.
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
starting the daemon:

```text
/userdata/ble_service/bluetooth -> /var/lib/bluetooth
```

This preserves the iPhone bond across reboot and firmware rootfs replacement.

## Wake GATT Contract

| Item | UUID |
| --- | --- |
| Wake Service | `a1de0001-7c4b-4f52-8d9a-6b4f6e6f7469` |
| Wake Characteristic | `a1de0002-7c4b-4f52-8d9a-6b4f6e6f7469` |

The characteristic supports `read` and `notify`. A notification is a 12-byte
little-endian value:

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

Returns adapter, advertisement, Wake subscriber, connected iPhone, ANCS
subscription, cursor, and last-error state.

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
