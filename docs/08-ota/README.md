---
sidebar_label: Overview
sidebar_position: 0
---

# OTA Overview

This project's production OTA uses A/B partitioning, signed manifests, and boot health confirmation mechanisms. During runtime, devices only write to the inactive slot. After reboot, Rockchip SPL selects the new slot; if the new system does not confirm success within the health window, SPL automatically rolls back to the previous successful slot.

## Scope

- Target hardware: Luckfox Pico Zero / RV1106 + eMMC.
- Distribution: GitHub Releases. Published assets contain `manifest.json` plus compressed image archives: `boot_a.img.tar.gz`, `boot_b.img.tar.gz`, `oem.img.tar.gz`, `rootfs.img.tar.gz`, and `update.img.tar.gz`.
- Update method: The device-side `/oem/usr/bin/ota` fetches the manifest, verifies signatures, validates SHA256, writes to the inactive slot, switches `misc`, and reboots.
- Rollback method: Rockchip SPL A/B metadata controls boot tries; mark successful only after application health confirmation.

## Documentation Index

- [OTA Architecture and Runtime](architecture.md)
- [OTA Key Management](key-management.md)
- [Device Acceptance Process](device-acceptance.md)
- [A/B and `abctl` Verification](verification.md)
- [OTA Dedicated Storage Partition](no-space-plan.md)
- [GitHub Proxy Configuration](ota-github-proxy.md)
- [External Developer Guide](ota-external-developers.md)
- [Distribution Quick Examples](ota-quick-examples.md)
- [Release Channel Strategy](ota-release-channels.md)

## Core Constraints

- OTA does not update `env`, `idblock`, or `uboot`; these are only updated via factory or USB recovery.
- OTA only writes to `boot_*`, `oem_*`, `rootfs_*` of the inactive slot.
- A dedicated 300 MiB `ota` partition is mounted at `/userdata/ota` and stores OTA state, download cache, and health markers. The factory configuration remains in the persistent userdata filesystem at `/userdata/debian/ota/config.json`.
- `boot_a.img` and `boot_b.img` contain different slot bootargs; manifests must use slot-specific boot assets.
- When factory baseline is missing or manifest signature/hash verification fails, devices must fail closed.
- OTA commands fail closed unless `/userdata/ota` is the ext4 mount rooted at `/dev/disk/by-partlabel/ota`, and require actual free bytes for remaining downloads plus a 16 MiB margin. For the current 300 MiB partition, `debian_build.sh` additionally caps a target-slot download set at 254 MiB.

## Common Commands

```bash
# View OTA status
/oem/usr/bin/ota status

# Check and perform OTA update immediately
/oem/usr/bin/ota update

# View A/B metadata
/oem/usr/bin/abctl read /dev/disk/by-partlabel/misc

# View current slot and rootfs
cat /proc/cmdline
mount | grep ' /oem '
```

`check-now` is still retained as a compatibility alias; new scripts and documentation should use `update`.

## Related Source Code

| Path | Description |
| --- | --- |
| `src/agent/cmd/ota` | OTA CLI entry point, including manual update and health handling |
| `src/agent/cmd/abctl` | A/B metadata diagnostic tool |
| `src/agent/internal/ota` | OTA core logic for manifest, download, state machine, slot, health, etc. |
| `overlay-debian/etc/systemd/system/aiden-slot-resolve.service` | Resolve the active A/B slot before local mounts |
| `overlay-debian/etc/systemd/system/oem.mount` | Mount the active `/oem` partition |
| `overlay-debian/etc/systemd/system/aiden-ota-health.service` | Process pending OTA health state at boot |
| `scripts/generate_ota_manifest.sh` | Generate signed OTA manifest |
| `scripts/generate_ota_device_config.sh` | Generate factory configuration from manifest |
| `scripts/ota_partition_layout.sh` | Reads the Debian Stage 3 OTA partition size and derives release capacity |
| `scripts/debian-stage3/container-install-ota-config.sh` | Install factory OTA configuration and repack `update.img` |
| `pico-sdk/project/scripts/mk-ab-misc.py` | Generate factory `misc.img` A/B metadata |
