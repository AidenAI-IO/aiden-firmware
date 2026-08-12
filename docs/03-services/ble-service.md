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

`S39hciinit` loads the AES/CMAC crypto required by LE SMP, then attaches the
AIC8800 controller on `/dev/ttyS1` at 1.5 Mbaud without powering it through the
legacy `HCIDEVUP` ioctl. `S40bluetoothd` bind-mounts the BlueZ state directory,
starts the daemon, and lets BlueZ power `hci0` through the management API. This
ordering is required for the kernel to register the LE SMP fixed channel used
by encrypted GATT reads. If the controller disappears, the watchdog repeats
the same initialization sequence before restarting BlueZ:

```text
/userdata/ble_service/bluetooth -> /var/lib/bluetooth
```

This preserves the iPhone bond across reboot and firmware rootfs replacement.

## Pairing and Bonding

The advertised name is derived from the configured base name and the final
four hexadecimal digits of the adapter address, for example `Aiden-12AB`.
This is display-only. The full adapter identity is exposed as `board_identity`
over the USB status API and as six bytes of manufacturer-specific data in the BLE
advertisement. The app requires the service UUID, display name, and full
identity to match before pairing, so nearby boards with colliding name suffixes
cannot be selected accidentally.

The Wake characteristic has an encrypted read operation. The companion app
performs that read after connecting, causing iOS to start SMP and show the
system pairing sheet. No HOGP service is registered; USB remains the only HID
control path.

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
The Wake advertisement is registered once per `ble_service`/BlueZ lifetime and
keeps a stable service UUID and board identity. Opening, refreshing, or closing
the pairing window never unregisters or recreates the advertisement; only the
adapter pairing properties and pairing-agent authorization change.

The app reports only the live connection state. A bond is a reconnect cache,
not proof that the App Wake session is connected. Every explicit Connect action
reopens the board window; CoreBluetooth reuses a saved peripheral when possible
and otherwise performs the system pairing flow.

An ACL/CoreBluetooth connection is not reported as an authenticated Bluetooth
connection until the encrypted Wake read succeeds. If iOS has forgotten the
device while BlueZ still has its old key, that read returns insufficient
encryption. The app stops reconnecting after the first such failure and marks a
board-bond reset as required instead of repeatedly showing the system pairing
sheet.

## Wake GATT Contract

| Item | UUID |
| --- | --- |
| Wake Service | `a1de0001-7c4b-4f52-8d9a-6b4f6e6f7469` |
| Wake Characteristic | `a1de0002-7c4b-4f52-8d9a-6b4f6e6f7469` |

The characteristic supports encrypted `read` and standard `notify`. BlueZ 5.65
on the board rejects CCCD writes for `encrypt-notify` even after bonding, so the
read is the authentication and pairing trigger while the notification carries
only a non-sensitive poll signal. A notification is a 12-byte little-endian
value:

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

ANCS subscription is independent of the companion app's Wake subscription.
Wake availability controls only on-demand background Phone Bridge execution;
it must not delay Notification Source or Data Source subscription because that
changes the iOS/BlueZ GATT initialization order and can leave attribute reads
without Data Source responses.

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

Returns adapter, GATT registration, pairing window, trusted bond,
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
and do not cause a conflict; the window closes after the App completes the
encrypted read and subscribes to Wake notifications, or when the deadline
expires.

### `disconnect`

```json
{"op":"disconnect"}
```

Calls BlueZ `Device1.Disconnect` for the current phone connection, clears live
Wake/ANCS state, and keeps the paired/trusted device plus its bond keys for a
later direct reconnect.

### `pairing_forget`

```json
{"op":"pairing_forget"}
```

Calls BlueZ `Adapter1.RemoveDevice` for board-side bonds and returns the removal
count plus the latest Bluetooth status. It remains a local maintenance
operation; the Agent exposes it only through the USB-restricted
`/api/bluetooth/pairing/reset` recovery endpoint. Normal disconnects do not use
it and preserve both sides of the system bond.

## Agent HTTP API

The companion app reaches the pairing operations through the Agent on USB ECM:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/bluetooth/status` | Read BLE runtime and live connection state |
| `POST` | `/api/bluetooth/pairing/start` | Open or refresh the user-initiated connection window |
| `POST` | `/api/bluetooth/pairing/reset` | Remove a confirmed stale board bond before one fresh pairing attempt |
| `POST` | `/api/bluetooth/disconnect` | Disconnect the physical BLE/ANCS link without deleting the bond |

Bluetooth control writes are accepted only over the board's USB ECM address
(`192.168.42.1/24`) or loopback; requests arriving through Wi-Fi and other
listeners receive `403`. The normal app flow first disables its CoreBluetooth
reconnect loop, then asks BlueZ to disconnect the shared physical link. BLE
keys, ANCS bodies, and Phone Bridge command payloads never pass through these
endpoints.

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
