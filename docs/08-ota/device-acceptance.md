# OTA Device Acceptance Process

Before enabling production OTA rollout, the following acceptance tests should be completed on representative hardware. It is recommended to record device serial number, hardware version, starting slot, target release version, and `abctl read` and `ota status` output at each step.

## Prerequisites

- The production image is built with the production Ed25519 public key.
- GitHub Release contains `manifest.json` plus compressed image archives: `boot_a.img.tar.gz`, `boot_b.img.tar.gz`, `oem.img.tar.gz`, `rootfs.img.tar.gz`, and `update.img.tar.gz`.
- The `update.img` inside `update.img.tar.gz` has `/userdata/ota/config.json` embedded.
- When UART is available, it is recommended to record SPL rollback logs simultaneously.

`S54ota` only handles `/userdata/ota/pending_boot.json` health at startup, does not perform network or GitHub update checks. Manual updates must be triggered via `ota update`.

## 1. USB Factory Flash Acceptance

Download the release `update.img.tar.gz`, extract `update.img`, then flash it with the normal USB recovery flow:

```bash
tar -xzf update.img.tar.gz update.img
./upgrade_tool/upgrade_tool uf ./update.img
```

After device boots, check:

```bash
cat /proc/cmdline
mount | grep ' /oem '
/oem/usr/bin/abctl read /dev/block/by-name/misc
/oem/usr/bin/ota status
```

Expected:

- Factory boot is in slot A.
- `/proc/cmdline` contains `aiden.slot_suffix=_a` and `root=PARTLABEL=rootfs_a`.
- `/oem` is mounted from `/dev/block/by-name/oem_a`.
- `misc` metadata can be parsed normally from byte offset `2048`, slot A is successful.
- `/userdata/ota/config.json` exists, `ota status` does not report missing factory baseline.

## 2. Manual Slot Switch

Switch to inactive slot:

```bash
/oem/usr/bin/abctl set-active /dev/block/by-name/misc b --tries 3
sync
reboot
```

After reboot, check:

```bash
cat /proc/cmdline
mount | grep ' /oem '
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

Expected:

- `/proc/cmdline` contains `aiden.slot_suffix=_b` and `root=PARTLABEL=rootfs_b`.
- `/oem` is mounted from `/dev/block/by-name/oem_b`.
- Slot B has remaining tries before mark successful.

## 3. Mark Successful

After confirming the new slot is usable, commit:

```bash
/oem/usr/bin/abctl mark-successful /dev/block/by-name/misc b
sync
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

Expected: slot B successful with tries 0; previous slot is still retained as a fallback slot.

## 4. Rollback Trial Boot

Force trial boot to another slot, but do not mark successful:

```bash
/oem/usr/bin/abctl set-active /dev/block/by-name/misc a --tries 1
sync
reboot
```

To prevent health success, stop `ota` or prevent application readiness during trial boot. Reboot again after tries are consumed.

Expected: SPL returns to the previous successful slot. Confirm with UART, `cat /proc/cmdline`, and `abctl read`.

## 5. OTA Happy Path

Confirm configuration:

```bash
cat /userdata/ota/config.json
```

Configuration should point to target repo/channel and include `boot`, `oem`, `rootfs` hashes in `factory_partition_hashes.a` and `factory_partition_hashes.b`.

Execute OTA once:

```bash
/oem/usr/bin/ota update
```

Expected process:

1. Download and verify signed manifest.
2. Select inactive slot assets.
3. Download, verify, and write to inactive partitions.
4. Write `/userdata/ota/pending_boot.json`.
5. Switch `misc` and reboot.
6. After new slot boots, write matching `/userdata/ota/health.ok`.
7. `ota` marks successful and deletes pending files.

After success, check:

```bash
/oem/usr/bin/ota status
/oem/usr/bin/abctl read /dev/block/by-name/misc
ls -l /userdata/ota/pending_boot.json /userdata/ota/health.ok 2>&1 || true
```

Expected:

- `ota status` shows committed version/build time.
- Active slot successful with tries 0.
- `pending_boot.json` and `health.ok` are cleaned up.

## 6. Failure Scenarios

At least cover the following failure scenarios:

| Scenario | Expected |
| --- | --- |
| Invalid signature or invalid manifest | Reject update, do not write partitions, do not switch slot |
| SHA256 or size mismatch | Reject update, do not write target partition |
| Downgrade release | Reject update, do not switch slot |
| Health marker missing or mismatched | Target slot not marked successful, rollback after tries consumed |
| Inactive boot image corrupted | SPL should not hang, should fall back to previous successful slot |
| Download interrupted | Do not switch slot; can retry or re-download after network recovery |

Power interruption during partition write should be done via controlled power supply or HIL rig; manual random power disconnection is not recommended.
