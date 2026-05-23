# Production OTA Implementation Design

## Goal

Implement a production-grade OTA system for Luckfox Pico Zero devices that supports online firmware updates, A/B rollback safety, signed release manifests, selective partition updates, preserved userdata, and operational diagnostics.

This is a full implementation, not a patch-layer over the current single-slot image flow. The implementation changes the image layout, device runtime, build system, release pipeline, and verification tooling so each layer has a clear contract.

## Scope

The implementation spans two repositories in this workspace:

- `pico-sdk`: partition table, image generation, A/B boot image packaging, and initial `misc.img` generation.
- `aiden-hardware-demo`: OTA daemon, `abctl`, build/deployment integration, health marker integration, CI manifest signing, public key deployment, and docs.

The implementation does not OTA-update `env`, `idblock`, or `uboot`; those remain factory/USB-repair only.

## Architecture

The system uses Rockchip SPL A/B support with Android AVB AB metadata stored at LBA offset 4 in the `misc` partition, which is byte offset 2048 on 512-byte block devices.

Boot flow:

1. SPL reads `misc` and selects the highest-priority bootable slot.
2. SPL loads `boot_a` or `boot_b`.
3. Each boot image contains a slot-specific DTB bootargs value pointing root to `PARTLABEL=rootfs_a` or `PARTLABEL=rootfs_b` and also includes `aiden.slot_suffix=_a` or `_b`.
4. Linux mounts matching `rootfs_*`, then an early init script mounts `/oem` from `oem_<slot_suffix>`, and shared `/userdata` remains unslotted.
5. Before switching slots, `ota` deletes stale health markers and writes a pending boot record containing target slot, target version, target build time, and a boot nonce.
6. `agent_main` writes `/userdata/ota/health.ok` only after the business stack is ready. The marker includes slot, version, boot nonce, and current boot ID.
7. `ota` marks the slot successful only after seeing a health marker that matches the pending boot record during the health window.

Update flow:

1. `ota` loads `/userdata/ota/config.json` and public keys from `/oem/etc/ota_pubkey.pem` or keyring.
2. It queries GitHub Releases for the configured channel.
3. It downloads `manifest.json`, verifies Ed25519 signature over canonical JSON without `signature.value`, and rejects downgrades.
4. It downloads only listed assets with resume support and verifies size and SHA256.
5. It writes assets to inactive partitions only.
6. It fsyncs writes, updates `state.json`, sets inactive slot active with tries, and reboots.
7. On successful health confirmation, it commits the new slot and records `last_committed_version`.

## Partition And Image Design

The target eMMC partition layout is:

```text
32K(env),512K@32K(idblock),256K(uboot),4M(misc),32M(boot_a),32M(boot_b),256M(oem_a),256M(oem_b),1536M(rootfs_a),1536M(rootfs_b),3G(userdata)
```

`pico-sdk/project/build.sh` must generate these image artifacts:

- `env.img`
- `idblock.img`
- `uboot.img`
- `misc.img`
- `boot_a.img`
- `boot_b.img`
- `oem_a.img`
- `oem_b.img`
- `rootfs_a.img`
- `rootfs_b.img`
- `userdata.img`
- `update.img`

`misc.img` is 4 MiB. Bytes 0-2047 are zero. Bytes 2048-2079 contain valid AVB AB metadata with A priority 15, A successful, B disabled, `last_boot=0`, and big-endian CRC32 over bytes 0-27 of the 32-byte metadata structure.

`boot_a.img` and `boot_b.img` are full FIT boot images. Their DTBs are generated from the same base DTB but contain slot-specific `chosen/bootargs` values. This avoids relying on missing RV1106 `fdt_bootargs_append_ab` support.

Because boot images are slot-specific, OTA packaging must never treat `boot_a.img` and `boot_b.img` as interchangeable. The manifest uses canonical partition names but target-slot-specific asset names:

- Active A updating inactive B downloads `boot_b.img`, `oem.img` or `oem_b.img`, and `rootfs.img` or `rootfs_b.img` depending on asset availability.
- Active B updating inactive A downloads `boot_a.img`, `oem.img` or `oem_a.img`, and `rootfs.img` or `rootfs_a.img` depending on asset availability.
- For `boot`, suffixed assets are required because the DTB differs by slot.
- For `oem` and `rootfs`, suffixed assets are preferred; unsuffixed assets are accepted only when the same byte-identical image is valid for both slots.

`/oem` mounting is handled in rootfs, not by a static single-partition fstab entry. Buildroot receives an init script that reads `aiden.slot_suffix` from `/proc/cmdline`, validates it is `_a` or `_b`, and mounts `/dev/block/by-name/oem${suffix}` at `/oem`. If the suffix is absent or invalid, the script fails closed and logs the error rather than mounting the wrong OEM partition.

The top-level `_build_image.sh` copies binaries and public keys into the OEM source tree before image packaging. It must ensure both `oem_a` and `oem_b` receive identical initial content.

## Device Tools

### `abctl`

`abctl` is a diagnostic and factory/test tool. It can run on host image files and on-device block devices.

Required commands:

- `abctl read <misc>` prints raw and decoded slot state.
- `abctl init <misc> [--size 4M]` creates a factory `misc.img` or initializes a block device.
- `abctl set-active <misc> <A|B> [--tries N] [--successful 0|1]` makes a slot preferred.
- `abctl mark-successful <misc> <A|B>` commits a slot.
- `abctl write <misc> --a-priority N --a-tries N --a-successful 0|1 --b-priority N --b-tries N --b-successful 0|1` supports explicit test states.

The AB metadata implementation must match the AVB layout exactly. Reserved bytes remain reserved and are not used for application state.

### `ota`

`ota` is a small daemon and diagnostics CLI.

Required modes:

- Daemon mode: periodic check, download, write, reboot, health commit.
- `ota check-now`: perform one check/update cycle without waiting for interval.
- `ota verify-manifest <manifest>`: validate local or remote manifest against configured public keys.
- `ota status`: print state, active slot, pending update, and last committed version.

The daemon runs from `/oem/usr/bin/ota` and stores mutable data only under `/userdata/ota`.

## OTA Package And Signing

Manifest schema follows `OTA_PROPOSAL.md` with `schema_version`, `channel`, `version`, `build_time`, optional compatibility fields, `parts`, and `signature`.

Allowed `parts[].name` values are exactly `boot`, `oem`, and `rootfs`. The OTA daemon maps these canonical names to inactive partitions only:

- `boot` maps to `/dev/block/by-name/boot_<target-slot>` and a matching required asset `boot_<target-slot>.img`.
- `oem` maps to `/dev/block/by-name/oem_<target-slot>` and asset `oem_<target-slot>.img` if present, otherwise `oem.img`.
- `rootfs` maps to `/dev/block/by-name/rootfs_<target-slot>` and asset `rootfs_<target-slot>.img` if present, otherwise `rootfs.img`.

Each part defines one of these asset forms:

- `asset`: slot-neutral object `{name,size,sha256}` used only when bytes are valid for both slots.
- `asset_a` and `asset_b`: slot-specific objects `{name,size,sha256}`.

`boot` must use `asset_a` and `asset_b` because slot-specific DTBs produce different bytes. `oem` and `rootfs` may use either form. The daemon verifies the exact selected asset object's size and SHA256.

Selective update slot coherence rules:

- `state.json` tracks per-slot partition versions and per-slot partition hashes for `boot`, `oem`, and `rootfs` after every successful commit.
- Factory initialization records both slots as the factory version and uses slot-aware factory partition hashes from `/userdata/ota/config.json` because `boot_a.img` and `boot_b.img` differ.
- A manifest may omit unchanged partitions only if it declares `requires_partitions` for the omitted target-slot partitions and local state proves those partitions already match the required version/hash.
- If the inactive target slot has unknown or incompatible omitted partition versions, `ota` rejects the selective update before downloading assets.
- A full update that includes all `boot`, `oem`, and `rootfs` parts is always coherent.

Signing rules:

- Canonicalize JSON by removing `signature.value` and serializing deterministically.
- Sign with Ed25519 private key from `OTA_ED25519_PRIVATE_KEY` in GitHub Secrets.
- Verify with `/oem/etc/ota_pubkey.pem` initially.
- Allow future keyring support without requiring it for V1.

Release asset resolution:

- Manifest stores asset object names, not absolute URLs.
- Device queries GitHub Releases API and maps asset names to `browser_download_url`.
- Public repos require no token.
- Private repos may use `/userdata/ota/gh_token`; token support must be optional and absent by default.

Anti-rollback:

- `state.json` records `last_committed_version`.
- CI release versions must be monotonic and comparable. The production format is `YYYYMMDD-HHMMSS-<shortcommit>`.
- `manifest.build_time` is signed and must be later than the committed build time.
- `state.json` records both `last_committed_version` and `last_committed_build_time`.
- Treat a manifest whose version and build time exactly match the committed version/build time as no update.
- Reject a manifest whose build time is earlier than the committed build time.
- Reject a manifest whose build time equals the committed build time but version differs from the committed version.

## Runtime State

`/userdata/ota/state.json` is written atomically using temp file, fsync, and rename.

State records:

- current phase
- current version
- target version
- active slot at start of update
- target slot
- downloaded assets and hashes
- last committed version
- per-slot partition versions and hashes
- last error
- retry metadata
- pending boot nonce
- pending boot ID if known

No state required for safe boot may live outside `misc`; `state.json` is for observability and resumability only.

`ota` also maintains `/userdata/ota/pending_boot.json` between the slot switch and health commit. It contains target slot, target version, target build time, and nonce. This file is deleted after success or after the daemon observes that the device has rolled back to the previous slot.

## Safety Rules

- Never write the active slot.
- Never write `env`, `idblock`, or `uboot` from OTA.
- Reject unknown partition names in manifest.
- Reject selective manifests whose omitted target-slot partitions are not proven compatible by `requires_partitions` and local slot state.
- Reject images larger than target partition.
- Verify SHA256 before writing.
- Fsync files and block devices before changing `misc`.
- Change `misc` only after all selected inactive partitions are fully written and synced.
- Delete stale `health.ok` before changing `misc`.
- Bind `health.ok` to pending slot, version, nonce, and boot ID; never treat a bare file as sufficient proof of health.
- If the daemon dies before `misc` switch, the device keeps booting the old slot.
- If the new slot fails health, `ota` reboots the device at the end of each health window so SPL can decrement tries and eventually roll back.
- If `ota` itself is not running on the new slot, a hardware or software watchdog must reboot the system; the init script enables this watchdog policy where supported.

## Error Handling And Observability

All operations log clear phase-prefixed messages. Errors include enough context to diagnose asset, partition, HTTP status, hash mismatch, signature failure, or misc write failure.

Recoverable errors leave state in a retryable phase. Non-recoverable errors mark the update failed and keep the active slot untouched.

`ota status` and `abctl read` are the primary field debugging tools.

## Build And Deployment

`_build.sh` builds:

- C/C++ binaries as today.
- Go daemon `agent`.
- Go daemon/CLI `ota`.
- Go CLI `abctl`.

`_build_image.sh` copies produced binaries into `overlay/oem/usr/bin/`, copies OTA public key into `overlay/oem/etc/`, runs the SDK image build, and verifies expected A/B images exist.

The runtime startup script starts `ota` after persistent storage and network are available. It must not block boot if OTA fails.

## CI Release Flow

GitHub Actions keeps existing daily/manual triggers. After image build and release name generation, CI runs `scripts/generate_ota_manifest.sh` to create signed `manifest.json` from slot-aware images. CI then derives `/userdata/ota/config.json` from the signed manifest, writes it to the SDK userdata staging directory, and repacks `userdata.img` plus `update.img` before publishing. The script includes canonical manifest names and slot-specific asset objects where needed:

- `boot` includes both `asset_a={name:"boot_a.img",size,sha256}` and `asset_b={name:"boot_b.img",size,sha256}`.
- `oem` includes `asset_a={name:"oem_a.img",size,sha256}` and `asset_b={name:"oem_b.img",size,sha256}` unless a byte-identical `asset={name:"oem.img",size,sha256}` is produced.
- `rootfs` includes `asset_a={name:"rootfs_a.img",size,sha256}` and `asset_b={name:"rootfs_b.img",size,sha256}` unless a byte-identical `asset={name:"rootfs.img",size,sha256}` is produced.

The Release contains:

- signed `manifest.json`
- OTA partition images
- `update.img` for USB recovery/factory flashing

The published `update.img` must include `/userdata/ota/config.json` with `repo`, `channel`, `factory_version`, `factory_build_time`, and `factory_partition_hashes.{a,b}.{boot,oem,rootfs}`.

CI must fail if manifest signing key is missing for release builds. Release names and manifest versions use the monotonic format `YYYYMMDD-HHMMSS-<shortcommit>`.

## Testing Strategy

Unit tests:

- AB metadata byte layout, CRC, parse validation, mutation commands.
- Manifest canonicalization and Ed25519 verification, including tamper failures.
- GitHub asset resolution from sample API responses.
- Resume downloader using `httptest` range responses.
- SHA256 and size checks.
- State atomic write/read.
- Writer rejects active slot, unknown partitions, oversized images.
- Manifest slot mapping resolves correct assets for active A to target B and active B to target A.
- Slot-specific assets verify against their own size and SHA256.
- Selective updates are rejected when omitted target-slot partitions are stale, unknown, or incompatible.
- Downgrade rejection covers older signed build times and equal versions.
- Health markers are rejected when slot, version, nonce, or boot ID do not match pending boot.

Integration tests without hardware:

- Fake block-device files for inactive slot writes.
- Local HTTP server serving manifest and images.
- End-to-end `check -> download -> verify -> write -> misc switch` with reboot command mocked.
- Health-window timeout triggers a mocked reboot instead of waiting indefinitely.
- `/oem` mount script selects `oem_a` and `oem_b` from synthetic cmdlines and rejects missing or invalid suffixes.

Manual hardware tests:

- Fresh USB flash boots slot A.
- `abctl set-active B` switches to slot B.
- Try-count rollback returns to A.
- `mark-successful` keeps selected slot stable.
- OTA happy path moves v0 to v1 and commits after health.
- OTA bad v2 rolls back to v1.
- Network interruption resumes download.

## Implementation Order

1. Clean and harden AB metadata package and `abctl`.
2. Implement `pico-sdk` A/B partition/image generation.
3. Integrate top-level build/deploy for `ota`, `abctl`, public key, and init script.
4. Implement manifest/signing/verification and CI generator.
5. Implement downloader, writer, state, GitHub client, and OTA daemon.
6. Add agent health marker.
7. Add docs and run tests/dry-runs.
8. Leave hardware-only validation as an explicit checklist if no physical device is available in this session.

## Open Constraints

- Full `./build_image.sh` may need Linux/case-sensitive filesystem; on macOS this workspace can inspect code and run Go tests, but image build may need Docker/CI.
- GitHub Secret creation cannot be performed from this workspace; CI will reference `OTA_ED25519_PRIVATE_KEY` and docs will instruct setup.
- Hardware validation requires a physical device and UART/SSH access.
