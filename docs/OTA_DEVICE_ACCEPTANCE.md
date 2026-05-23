# OTA Device Acceptance

Run these checks on representative hardware before enabling production OTA rollout.

## Prerequisites

- A production image built with a production Ed25519 public key.
- A GitHub Release containing `manifest.json`, `boot_a.img`, `boot_b.img`, `oem_a.img`, `oem_b.img`, `rootfs_a.img`, `rootfs_b.img`, and `update.img`.
- Factory `update.img` must already seed `/userdata/ota/config.json` with repo/channel settings and slot-aware factory partition hashes.
- UART access for bootloader and SPL rollback observation where possible.

`ota` processes `/userdata/ota/pending_boot.json` health before starting network/GitHub update checks. Do not add a network wait before pending health handling; a newly booted slot must be able to mark itself successful even if the network is unavailable.

## 1. USB Flash Acceptance

1. Flash `pico-sdk/output/image/update.img` by the normal USB recovery flow.
2. Boot the device and confirm it reaches the application ready state.
3. Confirm slot and mounts:

```sh
cat /proc/cmdline
mount | grep ' /oem '
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

Expected:

- `aiden.slot_suffix=_a` on the factory boot.
- `/oem` is mounted from `oem_a`.
- `misc` metadata parses from offset `2048` and slot A is successful.
- `/userdata/ota/config.json` exists and `/oem/usr/bin/ota status` initializes state without a missing factory baseline error.

## 2. Manual Slot Switching

Switch to the inactive slot:

```sh
/oem/usr/bin/abctl set-active /dev/block/by-name/misc B --tries 3 --successful 0
sync
reboot
```

After boot:

```sh
cat /proc/cmdline
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

Expected:

- Kernel cmdline reports `aiden.slot_suffix=_b`.
- `/oem` is mounted from `oem_b`.
- B has remaining tries until marked successful.

## 3. Mark Successful

Commit the manually booted slot:

```sh
/oem/usr/bin/abctl mark-successful /dev/block/by-name/misc B
sync
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

Expected: slot B is successful with zero tries. The previous slot remains available for rollback tests unless explicitly disabled.

## 4. Try Rollback

Force a trial boot and do not mark it successful:

```sh
/oem/usr/bin/abctl set-active /dev/block/by-name/misc A --tries 1 --successful 0
sync
reboot
```

Prevent health success if needed by stopping `ota`/application readiness during the trial. Reboot again after tries are exhausted.

Expected: SPL returns to the previous successful slot. Confirm with UART logs if available, `cat /proc/cmdline`, and `abctl read`.

## 5. OTA Happy Path

1. Confirm `/userdata/ota/config.json` points at the release repo and `stable` channel, and contains `factory_partition_hashes.a` and `factory_partition_hashes.b` entries for `boot`, `oem`, and `rootfs`.
2. Start a one-shot update:

```sh
/oem/usr/bin/ota check-now
```

3. Device downloads the signed manifest and selected inactive-slot assets.
4. Device writes only inactive partitions, creates `/userdata/ota/pending_boot.json`, switches `misc`, and reboots.
5. After the application reaches ready state, it writes `/userdata/ota/health.ok` with slot, version, build time, nonce, and boot ID.
6. `ota` marks the new slot successful.

Expected checks:

```sh
/oem/usr/bin/ota status
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

- `ota status` shows committed state and the new version/build time.
- `pending_boot.json` is removed after success.
- The active slot is successful with zero tries.

## 6. Bad Update Rollback

Publish or stage a test release that boots but never writes a matching health marker, or corrupt one inactive image in a controlled lab release.

Expected outcomes:

- Bad signatures or bad SHA256 are rejected before any partition write.
- Oversized or unknown partition assets are rejected.
- If a written trial slot boots but fails health, `ota` reboots at the end of each health window so SPL decrements tries.
- After tries are exhausted, the device returns to the previous successful slot.
- `ota status` records the failed phase/error under `/userdata/ota/state.json`.

## 7. Network Interruption

Interrupt network during manifest or image download:

```sh
ifconfig wlan0 down 2>/dev/null || true
```

Expected:

- No `misc` switch occurs until all selected images are downloaded, verified by size/SHA256, written, and synced.
- Partial downloads remain retryable under `/userdata/ota/downloads`.
- On the next check with network restored, downloads resume or restart safely.
- Pending health processing still runs before network update checks on boot.

## Acceptance Record

Record for each tested device:

- serial number and hardware revision
- starting slot and target slot
- release version `YYYYMMDD-HHMMSS-<shortcommit>`
- `abctl read` before and after each slot switch
- `ota status` after success or rollback
- UART/SPL rollback evidence when available
