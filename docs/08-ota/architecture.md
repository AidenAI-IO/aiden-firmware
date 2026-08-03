# OTA Architecture and Runtime

OTA is accomplished through three layers: `pico-sdk` generates A/B images and factory `misc.img`, GitHub Actions publishes signed releases, and the device-side `ota` completes download, write, and switching on manual trigger, with one-shot health handling at boot responsible for health commitment after startup.

## Partition Layout

Production images use A/B layout:

```text
32K(env),512K@32K(idblock),256K(uboot),4M(misc),32M(boot_a),32M(boot_b),256M(oem_a),256M(oem_b),1536M(rootfs_a),1536M(rootfs_b),3G(userdata),300M(ota)
```

| Partition | A/B | OTA Behavior |
| --- | --- | --- |
| `env` | No | Not updated via OTA; factory or USB recovery only |
| `idblock` | No | Not updated via OTA; avoid brick risk |
| `uboot` | No | Not updated via OTA; old bootloader requires full flash update |
| `misc` | No | Stores A/B metadata, OTA only modifies slot state |
| `boot` | Yes | Write to inactive `boot_a` or `boot_b` |
| `oem` | Yes | Write to inactive `oem_a` or `oem_b` |
| `rootfs` | Yes | Write to inactive `rootfs_a` or `rootfs_b` |
| `userdata` | No | Preserved across upgrades, stores non-OTA persistent data |
| `ota` | No | Dedicated OTA config, state, health markers, and download cache; factory flash only |

## Boot Process

1. ROM loads SPL.
2. SPL reads Android AVB A/B metadata from `misc` partition at byte offset `2048`.
3. SPL selects the slot with highest priority and bootable status, loading `boot_a` or `boot_b`.
4. Slot-specific FIT boot image provides `root=PARTLABEL=rootfs_a|rootfs_b` and `aiden.slot_suffix=_a|_b`.
5. Linux mounts the matching `rootfs_*`.
6. SDK `S20linkmount` mounts `/dev/block/by-name/ota` at `/userdata/ota`.
7. `S20oemslot` mounts `/dev/block/by-name/oem_a|oem_b` to `/oem` based on `aiden.slot_suffix`.
8. `S54ota` verifies the dedicated OTA mount, runs `ota health` once, and exits.

## Update Process

1. `ota` reads `/userdata/ota/config.json` and `/oem/etc/ota_pubkey.pem`.
2. Fetch manifest: if `manifest_url` is configured, fetch that URL directly; otherwise query the GitHub Release `releases/latest` endpoint (i.e., `DefaultReleaseURL`, can be overridden by config's release URL), and retrieve `manifest.json` from release assets.
3. Download `manifest.json`, remove `signature.value`, and perform canonical JSON Ed25519 signature verification.
4. Reject downgrades with older `build_time` or different version with same build time.
5. Select inactive slot and parse corresponding slot assets from manifest.
6. Clean stale download cache and calculate the remaining bytes after verified cache and resumable partials.
7. Read actual available bytes from the dedicated OTA filesystem and require the remaining downloads plus the configured safety margin.
8. Download images and verify archive size, SHA256, extracted image hash, and target partition size.
9. Write to inactive `boot_*`, `oem_*`, `rootfs_*`, and fsync.
10. Delete old `health.ok`, write `/userdata/ota/pending_boot.json`.
11. Modify `misc`, set target slot as active trial slot with default tries of 3.
12. Reboot into target slot.

## Health Confirmation and Rollback

After the new slot boots, the Go daemon calls OTA health write logic after runtime init completes. `/userdata/ota/health.ok` is only written when all the following conditions are met:

- `pending_boot.json` exists.
- Current `aiden.slot_suffix` equals pending target slot.
- Current rootfs slot equals pending target slot.
- Health marker write includes version, build time, nonce, and current boot ID from pending.

When `ota` sees a matching marker:

1. Call `abctl`/slot logic to mark successful.
2. Update committed version/build time and per-slot partition hashes in `/userdata/ota/state.json`.
3. Delete `pending_boot.json` and `health.ok`.

If the health window times out, `ota health` actively reboots, allowing SPL to consume tries. When tries are exhausted and the target slot is not successful, SPL falls back to the previous successful slot. When `ota health` observes a rollback in the old slot, it cleans up pending state and marks the state phase as `rolled-back`.

## Manifest Convention

`parts[].name` in the manifest can only be `boot`, `oem`, or `rootfs`. Each part uses one of the following asset forms:

- `asset`: slot-neutral `{name,size,sha256}`, only applicable to byte-identical images on both sides.
- `asset_a` and `asset_b`: slot-specific `{name,size,sha256}`.

For `.img.tar.gz` assets, `size` and `sha256` describe the downloaded archive. The required `image_sha256` field describes the extracted `.img`; OTA state and `requires_partitions` compare this extracted image hash.

`boot` must use `asset_a` and `asset_b` because the boot image contains slot-specific DTB bootargs. `oem` and `rootfs` can use slot-specific assets or slot-neutral assets when confirmed as byte-identical.

## Factory Baseline

Release `update.img` must include `ota.img`, which provides `/userdata/ota/config.json` after mounting. After generating the signed manifest, CI calls `scripts/generate_ota_device_config.sh` to generate this file, then rebuilds only `ota.img` and `update.img` via `scripts/repack_ota_update_image.sh`.

`config.json` must contain at least:

- `factory_version` - factory flash version number, used for downgrade protection and selective update verification
- `factory_build_time` - factory flash build time
- `factory_partition_hashes.a.boot|oem|rootfs` - SHA256 of each slot A partition
- `factory_partition_hashes.b.boot|oem|rootfs` - SHA256 of each slot B partition

Optional configuration fields:

- `manifest_url` - directly specify manifest URL (skip GitHub Release API)
- `public_key_path` - override default public key path (default `/oem/etc/ota_pubkey.pem`)
- `github_token_path` - GitHub token file path (required for private repositories)
- `download_safety_margin_bytes` - free bytes retained beyond remaining downloads (default 16 MiB)

The dedicated storage identity is not configurable: production OTA always
requires `/dev/block/by-name/ota` mounted as an ext4 filesystem rooted at `/`
on `/userdata/ota`. Test code can inject synthetic mount information without
exposing a device-side configuration bypass.

Release CI derives its target-slot download limit from the same layout contract.
The current 300 MiB partition reserves a conservative 30 MiB for ext4 metadata
and reserved blocks plus the 16 MiB runtime safety margin, producing a 254 MiB
maximum compressed download set.

Factory baseline must be slot-aware because `boot_a.img` and `boot_b.img` have different hashes. When baseline is missing, OTA initialization must fail; it should not guess current partition versions.

Note: The `config.json` generated by `generate_ota_device_config.sh` also includes `repo` and `channel` fields, but these fields are only for human readability and are not read by OTA code. Actual channel verification comes from the manifest itself.

## Private Repository Token

Public GitHub releases do not require a device token. Private repositories can place a read-only token at:

```text
/userdata/ota/gh_token
```

When a token exists, `ota` adds a bearer token to GitHub Release metadata, manifest, and image download requests. The OTA signing key and GitHub token are two independent credentials.
