# OTA A/B Verification

This guide verifies the Rockchip SPL A/B metadata and the `abctl` diagnostic tool used by production OTA.

## Metadata Contract

- A/B metadata lives in the `misc` partition at byte offset `2048`.
- The metadata object is 32 bytes and uses Android AVB A/B layout version `1.0`.
- CRC32 is big-endian IEEE over metadata bytes `0..27`.
- Reserved bytes remain reserved. Application OTA state lives in `/userdata/ota`, not in `misc`.
- Factory `misc.img` starts slot A as priority 15, successful, and slot B disabled.

## Host Image Checks

Use a regular file for safe host testing:

```sh
build/bin/abctl init /tmp/misc.img --size 4M
build/bin/abctl read /tmp/misc.img
```

Expected output includes slot A successful, slot B priority 0, and `last_boot=A`.

Manual state transitions:

```sh
build/bin/abctl set-active /tmp/misc.img B --tries 3 --successful 0
build/bin/abctl read /tmp/misc.img
build/bin/abctl mark-successful /tmp/misc.img B
build/bin/abctl read /tmp/misc.img
```

Explicit test states:

```sh
build/bin/abctl write /tmp/misc.img \
  --a-priority 14 --a-tries 0 --a-successful 1 \
  --b-priority 15 --b-tries 3 --b-successful 0
build/bin/abctl read /tmp/misc.img
```

`abctl write` rejects invalid states, including successful slots with nonzero tries.

## Device Checks

On device, use the real `misc` block device:

```sh
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

Manual slot switch test:

```sh
/oem/usr/bin/abctl set-active /dev/block/by-name/misc B --tries 3 --successful 0
sync
reboot
```

After boot, confirm the active slot from kernel cmdline and metadata:

```sh
cat /proc/cmdline
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

Commit the booted slot only after the device is known good:

```sh
/oem/usr/bin/abctl mark-successful /dev/block/by-name/misc B
sync
```

Rollback test:

```sh
/oem/usr/bin/abctl set-active /dev/block/by-name/misc B --tries 1 --successful 0
sync
reboot
```

Do not mark B successful. Reboot until SPL exhausts tries and returns to the prior successful slot. Confirm with `cat /proc/cmdline` and `abctl read`.

## Expected Diagnostics

- `cat /proc/cmdline` contains `aiden.slot_suffix=_a` or `_b`.
- `/oem` is mounted from `/dev/block/by-name/oem_a` or `oem_b` matching the active suffix.
- `abctl read` reports only valid AVB A/B metadata. CRC, layout version, and slot fields must parse without error.
- `ota status` reports the OTA state, active slot, pending boot data if any, and the raw A/B data.
