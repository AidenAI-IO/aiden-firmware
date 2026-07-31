# OTA Overview

This project's production OTA uses A/B partitioning, signed manifests, and boot health confirmation mechanisms. During runtime, devices only write to the inactive slot. After reboot, Rockchip SPL selects the new slot; if the new system does not confirm success within the health window, SPL automatically rolls back to the previous successful slot.

## Scope

- Target hardware: Luckfox Pico Zero / RV1106 + eMMC.
- Distribution: GitHub Releases. Published assets contain `manifest.json` plus compressed image archives: `boot_a.img.tar.gz`, `boot_b.img.tar.gz`, `oem.img.tar.gz`, `rootfs.img.tar.gz`, and `update.img.tar.gz`. The extracted images still use the slot-neutral `oem.img` and `rootfs.img` layout introduced in PR #112; older releases used `oem_a.img`, `oem_b.img`, `rootfs_a.img`, and `rootfs_b.img`.
- Update method: The device-side `/oem/usr/bin/ota` fetches the manifest, verifies signatures, validates SHA256, writes to the inactive slot, switches `misc`, and reboots.
- Rollback method: Rockchip SPL A/B metadata controls boot tries; mark successful only after application health confirmation.

## Documentation Index

### Core Documentation

- [OTA Architecture and Runtime](architecture.md)
- [OTA Key Management](key-management.md)
- [Device Acceptance Process](device-acceptance.md)
- [A/B and `abctl` Verification](verification.md)
- [OTA Reserved Space](no-space-plan.md)

### Openness and External Developers

- [OTA Openness Improvements](OTA_OPEN_SOURCES.md) - manifest supports direct URLs, external developer firmware distribution
- [External Developer Guide](ota-external-developers.md) - how to distribute firmware using custom sources
- [Quick Examples](ota-quick-examples.md) - GitHub Releases, self-hosted backend, hybrid mode examples
- [Release Channel Strategy](ota-release-channels.md) - branch and channel isolation mechanisms

### Technical Analysis

- [Neutral Resource Compatibility Analysis](OTA_COMPATIBILITY_ANALYSIS.md) - PR #112 backward compatibility assessment

## Core Constraints

- OTA does not update `env`, `idblock`, or `uboot`; these are only updated via factory or USB recovery.
- OTA only writes to `boot_*`, `oem_*`, `rootfs_*` of the inactive slot.
- `/userdata` is preserved across upgrades and stores OTA configuration, state, download cache, health markers, and the OTA reserved-space file.
- `boot_a.img` and `boot_b.img` contain different slot bootargs; manifests must use slot-specific boot assets.
- When factory baseline is missing or manifest signature/hash verification fails, devices must fail closed.
- The device reserves a 200 MiB download-cache budget. Release generation rejects a target-slot manifest above 196 MiB, leaving the configured 4 MiB filesystem margin.

## Common Commands

```bash
# View OTA status
/oem/usr/bin/ota status

# Check and perform OTA update immediately
/oem/usr/bin/ota update

# View A/B metadata
/oem/usr/bin/abctl read /dev/block/by-name/misc

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
| `overlay/etc/init.d/S20oemslot` | Mount `/oem` based on `aiden.slot_suffix` |
| `overlay/etc/init.d/S54ota` | One-time OTA health handling at boot |
| `scripts/generate_ota_manifest.sh` | Generate signed OTA manifest |
| `scripts/generate_ota_device_config.sh` | Generate factory configuration from manifest |
| `scripts/repack_ota_update_image.sh` | Repack factory OTA configuration into `userdata.img` and `update.img` |
| `pico-sdk/project/scripts/mk-ab-misc.py` | Generate factory `misc.img` A/B metadata |
