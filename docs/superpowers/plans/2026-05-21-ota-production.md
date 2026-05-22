# Production OTA Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the full production OTA system described in `docs/superpowers/specs/2026-05-21-ota-production-design.md`.

**Architecture:** The implementation creates a real A/B image layout in `pico-sdk`, then adds a Go OTA runtime in `src/agent` that verifies signed GitHub Release manifests, writes only inactive partitions, switches `misc`, and commits slots only after nonce-bound health confirmation. Build, release, key deployment, and field diagnostics are first-class parts of the implementation.

**Tech Stack:** Go 1.26, C++17, POSIX shell, Buildroot init scripts, GitHub Actions, Ed25519, SHA256, Rockchip SPL A/B AVB metadata.

---

## File Structure

### Current Repository

- Create/modify: `src/agent/internal/ota/slot.go` and `slot_test.go` for exact Rockchip SPL AVB AB metadata and slot operations.
- Create/modify: `src/agent/cmd/abctl/main.go` and `main_test.go` for production diagnostic CLI.
- Create: `src/agent/internal/ota/manifest.go`, `manifest_test.go` for schema, canonical JSON, signature verification, slot asset resolution, downgrade rejection.
- Create: `src/agent/internal/ota/github.go`, `github_test.go` for GitHub Releases API and asset map resolution.
- Create: `src/agent/internal/ota/download.go`, `download_test.go` for resumable HTTP downloads.
- Create: `src/agent/internal/ota/verify.go`, `verify_test.go` for size and SHA256 checks.
- Create: `src/agent/internal/ota/state.go`, `state_test.go` for atomic state and pending boot records.
- Create: `src/agent/internal/ota/writer.go`, `writer_test.go` for inactive partition writes and active-slot rejection.
- Create: `src/agent/internal/ota/health.go`, `health_test.go` for nonce-bound health marker validation and timeout reboot behavior.
- Create: `src/agent/internal/ota/updater.go`, `updater_test.go` for one-shot update orchestration.
- Create: `src/agent/cmd/ota/main.go` for daemon and diagnostic subcommands.
- Modify: `_build.sh` to build `agent`, `ota`, and `abctl`.
- Modify: `_build_image.sh` to copy binaries/public key, run image build, and verify A/B artifacts.
- Create: `overlay/etc/init.d/S54ota` for boot startup.
- Create: `overlay/etc/init.d/S20oemslot` for slot-aware `/oem` mount.
- Modify: `src/agent_main.cpp` to write nonce-bound `/userdata/ota/health.ok` after ready.
- Production OTA public key is supplied via `OTA_PUBLIC_KEY_PATH` or derived by CI from the signing key.
- Create/copy: `overlay/oem/etc/ota_pubkey.pem` during image build from the production-safe key source above.
- Create: `scripts/generate_ota_manifest.sh` and optional helper `scripts/verify_ota_manifest.sh`.
- Modify: `.github/workflows/build.yml` for monotonic release naming and signed manifest generation.
- Create: `docs/OTA_AB_VERIFICATION.md`, `docs/OTA_KEY_MANAGEMENT.md`, `docs/OTA_DEVICE_ACCEPTANCE.md`.

### `pico-sdk` Submodule

- Modify: `pico-sdk/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Buildroot-RV1106_Luckfox_Pico_Zero-IPC.mk` for A/B partition table and filesystem config.
- Modify: `pico-sdk/project/build.sh` for `misc.img`, A/B image generation, slot-specific bootargs, and firmware packaging.
- Create if needed: `pico-sdk/project/scripts/mk-ab-misc.py` or shell equivalent for deterministic AVB AB metadata generation.

---

## Task 1: Harden AB Metadata And `abctl`

**Files:**
- Modify: `src/agent/internal/ota/slot.go`
- Modify: `src/agent/internal/ota/slot_test.go`
- Modify: `src/agent/cmd/abctl/main.go`
- Modify: `src/agent/cmd/abctl/main_test.go`

- [ ] **Step 1: Write failing layout tests**

Add tests that assert exact 32-byte Rockchip SPL AVB layout at byte offset 2048, reserved bytes remain zero, SDK `last_boot` is parsed and preserved, version must be 1.0, `successful_boot` must be 0 or 1, `successful_boot=1` cannot have nonzero tries, CRC is big-endian IEEE over bytes 0-27 of the metadata object, and `MarkSuccessful` clears tries and marks only the requested slot successful without making the previous rollback slot unbootable.

Run: `go test ./internal/ota -run 'TestAB|TestParse' -count=1`

Expected: FAIL until implementation rejects invalid bytes, uses byte offset 2048, and preserves SDK `last_boot` at metadata byte 16.

- [ ] **Step 2: Implement exact AVB AB metadata**

Remove any application use of reserved bytes while preserving SDK `last_boot`. Implement `ActiveSlot`, `Bootable`, `SetActive`, `MarkSuccessful`, `Marshal`, `ParseABData`, `ReadABData`, `WriteABDataAt`, and `CreateMiscImage` helpers. `MarkSuccessful` must not clear `SuccessfulBoot` on the other slot. Reject or normalize any state where `successful_boot=true` and `tries_remaining>0`; prefer rejection for CLI `write` and `set-active`, and force target tries to 0 when `successful=true` is explicitly requested.

- [ ] **Step 3: Write failing `abctl` CLI tests**

Cover `init --size 4M`, `read`, `set-active --tries --successful`, `mark-successful`, `write`, invalid tries, invalid successful values, and read/write round trip.

Run: `go test ./cmd/abctl -count=1`

Expected: FAIL until CLI supports the full command set.

- [ ] **Step 4: Implement production `abctl`**

Support image files and block devices without truncating block devices by default. `init` truncates regular files to requested size and writes metadata at byte offset 2048. `write` writes explicit A/B state and recalculates CRC.

- [ ] **Step 5: Verify task**

Run: `go test ./internal/ota ./cmd/abctl -count=1`

Expected: PASS.

Checkpoint: inspect `git diff -- src/agent/internal/ota src/agent/cmd/abctl`.

---

## Task 2: Implement Manifest, Signing, And CI Generator Core

**Files:**
- Create: `src/agent/internal/ota/manifest.go`
- Create: `src/agent/internal/ota/manifest_test.go`
- Create: `scripts/generate_ota_manifest.sh`

- [ ] **Step 1: Write manifest tests**

Cover canonical JSON signing, tamper failure, Ed25519 public key parsing, allowed part names, slot-specific asset selection, and per-slot size/hash. Defer downgrade and selective coherence policy tests to Task 3 after state types exist.

Run: `go test ./internal/ota -run 'TestManifest|TestCanonical|TestDowngrade|TestAsset' -count=1`

Expected: FAIL because manifest code does not exist.

- [ ] **Step 2: Implement manifest model**

Define structs for manifest, signature, part, asset, `asset`, `asset_a`, `asset_b`, and `requires_partitions`. Implement deterministic canonicalization by unmarshalling then re-marshalling with sorted map keys through Go's `encoding/json` over structs/maps after deleting `signature.value`.

- [ ] **Step 3: Implement signature and policy validation**

Implement Ed25519 verification, `ResolveAsset(part, targetSlot)`, and static manifest validation. Do not wire downgrade or selective coherence checks here; those depend on the state model added in Task 3.

- [ ] **Step 4: Write generator tests or dry-run fixtures**

Use small fake image files in a temp dir. Generate manifest with test Ed25519 key and verify with Go code.

Run: `scripts/generate_ota_manifest.sh --help`

Expected: PASS and usage output.

- [ ] **Step 5: Implement `generate_ota_manifest.sh`**

Support `--version`, `--channel`, `--build-time`, `--sign-key`, `--image-dir`, and `--output`. Emit `boot` with `asset_a/asset_b`; emit `oem` and `rootfs` with slot-specific assets when present. Fail if key or required images are missing.

- [ ] **Step 6: Verify task**

Run: `go test ./internal/ota -run 'TestManifest|TestCanonical|TestDowngrade|TestAsset' -count=1`

Run a local manifest dry run with generated test keys and tiny fake images.

Expected: PASS.

Checkpoint: inspect `git diff -- src/agent/internal/ota/manifest* scripts/generate_ota_manifest.sh`.

---

## Task 3: Implement Download, GitHub, Verify, State, Writer, Health

**Files:**
- Create: `src/agent/internal/ota/github.go`, `github_test.go`
- Create: `src/agent/internal/ota/download.go`, `download_test.go`
- Create: `src/agent/internal/ota/verify.go`, `verify_test.go`
- Create: `src/agent/internal/ota/state.go`, `state_test.go`
- Create: `src/agent/internal/ota/writer.go`, `writer_test.go`
- Create: `src/agent/internal/ota/health.go`, `health_test.go`

- [ ] **Step 1: Write failing component tests**

Use `httptest` for GitHub and range downloads. Use temp files for fake block devices. Use temp dirs for state and health markers. Include downgrade rejection by older signed build time or same build time with different version, exact same version/build time as no-update, and selective update coherence failures now that state types exist.

Run: `go test ./internal/ota -run 'TestGitHub|TestDownload|TestVerify|TestState|TestWriter|TestHealth' -count=1`

Expected: FAIL because components do not exist.

- [ ] **Step 2: Implement GitHub release client**

Fetch latest release JSON, support optional bearer token, map asset names to URLs, and return clear errors for missing assets.

- [ ] **Step 3: Implement resumable downloader**

Resume `.part` files with `Range`; handle servers without range support by restarting; fsync before rename; verify final size.

- [ ] **Step 4: Implement verify helpers**

Implement SHA256 stream verification and size checks.

- [ ] **Step 5: Implement atomic state**

Write temp file in same dir, fsync file, rename, fsync dir where supported. Include per-slot partition version/hash and pending boot fields. Add helpers to initialize factory state for both slots, reject downgrades, validate `requires_partitions`, and update per-slot partition versions/hashes plus `last_committed_version` and `last_committed_build_time` after a successful health commit.

- [ ] **Step 6: Implement writer**

Resolve inactive target block paths from canonical part names, reject active slot, reject unknown part, reject oversized images if partition size can be determined, stream-copy, fsync, and close.

- [ ] **Step 7: Implement health handling**

Delete stale markers before slot switch. Validate JSON health marker slot/version/build_time/nonce/boot_id. On timeout, call injected reboot function so tests can mock it.

- [ ] **Step 8: Verify task**

Run: `go test ./internal/ota -run 'TestGitHub|TestDownload|TestVerify|TestState|TestWriter|TestHealth' -count=1`

Expected: PASS.

Checkpoint: inspect `git diff -- src/agent/internal/ota`.

---

## Task 4: Implement OTA Orchestrator And CLI

**Files:**
- Create: `src/agent/internal/ota/updater.go`
- Create: `src/agent/internal/ota/updater_test.go`
- Create: `src/agent/cmd/ota/main.go`

- [ ] **Step 1: Write failing orchestrator tests**

Create fake release server, fake images, fake block devices, fake misc device, and fake reboot. Cover happy path, no update, bad signature, hash mismatch, stale target slot for selective update, active-slot write rejection, and health timeout reboot.

Run: `go test ./internal/ota -run TestUpdater -count=1`

Expected: FAIL.

- [ ] **Step 2: Implement updater config and one-shot flow**

Load config from `/userdata/ota/config.json` with defaults. Run check, manifest verification, asset resolution, download, hash verify, inactive writes, stale health deletion, pending boot write, misc switch, and reboot.

- [ ] **Step 3: Implement daemon loop**

Poll at configurable interval with jitter. On startup, first process pending health window if `pending_boot.json` exists. Log phase transitions. Do not block boot.

- [ ] **Step 4: Implement `ota` CLI**

Support daemon default, `check-now`, `status`, and `verify-manifest`. Add flags for config path, state dir, misc path, block dir, repo, channel, and dry-run/test mode.

- [ ] **Step 5: Verify task**

Run: `go test ./internal/ota ./cmd/ota -count=1`

Expected: PASS.

Checkpoint: inspect `git diff -- src/agent/internal/ota src/agent/cmd/ota`.

---

## Task 5: Integrate Device Runtime And Build Scripts

**Files:**
- Modify: `_build.sh`
- Modify: `_build_image.sh`
- Create: `overlay/etc/init.d/S20oemslot`
- Create: `overlay/etc/init.d/S54ota`
- Modify: `src/agent_main.cpp`
- Use production public key from `OTA_PUBLIC_KEY_PATH` or CI-derived signing key.
- Create/copy: `overlay/oem/etc/ota_pubkey.pem`
- Create: `scripts/test_oemslot.sh`

**Prerequisite:** Task 6 must be complete before `_build_image.sh` can be run end-to-end, because this task verifies A/B artifacts produced by the SDK. Steps 1, 2, 4, 5, and 6 can be implemented before Task 6; Step 3 and final image-build verification are runnable only after Task 6.

- [ ] **Step 1: Write shell smoke checks**

Add commands or documented checks that assert `_build.sh` emits `build/bin/agent`, `build/bin/ota`, and `build/bin/abctl`.

- [ ] **Step 2: Modify `_build.sh`**

Build Go binaries with `GOOS=linux GOARCH=arm GOARM=7`: `./cmd/daemon` -> `agent`, `./cmd/ota` -> `ota`, `./cmd/abctl` -> `abctl`.

- [ ] **Step 3: Modify `_build_image.sh`**

Copy all build binaries to `overlay/oem/usr/bin`, install `overlay/oem/etc/ota_pubkey.pem` from `OTA_PUBLIC_KEY_PATH` or the CI-derived public key, run SDK build, and fail if expected A/B images are missing after build.

- [ ] **Step 4: Add `/oem` slot mount init script**

Implement `S20oemslot` that parses `aiden.slot_suffix` from `/proc/cmdline`, validates `_a|_b`, creates `/oem`, and mounts `/dev/block/by-name/oem${suffix}`. Add test hooks such as `CMDLINE_PATH`, `MOUNT_BIN`, and `OEM_MOUNTPOINT` so the script can be tested without a device.

- [ ] **Step 5: Add OTA init script**

Implement `S54ota` watchdog-style startup similar to `S53agent`, writing logs to `/var/log/ota/ota.log` and not blocking boot.

- [ ] **Step 6: Add agent health marker**

In `agent_main.cpp`, after provider/audio setup and immediately before ready loop, write JSON health marker only if `/userdata/ota/pending_boot.json` exists. Include slot suffix from `/proc/cmdline`, version/build info, nonce copied from pending boot, and `/proc/sys/kernel/random/boot_id`.

- [ ] **Step 7: Verify task**

Run: `go test ./... -count=1` in `src/agent`.

Run: shell syntax checks with `sh -n overlay/etc/init.d/S20oemslot overlay/etc/init.d/S54ota scripts/test_oemslot.sh`.

Run: `scripts/test_oemslot.sh`.

Expected: synthetic cmdlines for `_a` and `_b` select matching OEM partitions; missing and invalid suffixes fail closed.

Expected: PASS.

Checkpoint: inspect `git diff -- _build.sh _build_image.sh overlay src/agent_main.cpp keys`.

---

## Task 6: Implement `pico-sdk` A/B Image Generation

**Files:**
- Modify: `pico-sdk/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Buildroot-RV1106_Luckfox_Pico_Zero-IPC.mk`
- Modify: `pico-sdk/project/build.sh`
- Create if cleaner: `pico-sdk/project/scripts/mk-ab-misc.py`

- [ ] **Step 1: Update BoardConfig**

Set partition table to `32K(env),512K@32K(idblock),256K(uboot),4M(misc),32M(boot_a),32M(boot_b),256M(oem_a),256M(oem_b),1536M(rootfs_a),1536M(rootfs_b),3G(userdata)`. Configure filesystem entries for `rootfs_a/rootfs_b`, `userdata`, and avoid static single `/oem` mounting.

- [ ] **Step 2: Add deterministic `misc.img` generation**

Generate 4 MiB image with valid AVB AB metadata at byte offset 2048. Prefer a small script with tests or a build.sh helper that uses Python available in SDK environment.

- [ ] **Step 3: Add duplicated `oem` and `rootfs` image generation**

Package identical initial content to `oem_a.img/oem_b.img` and `rootfs_a.img/rootfs_b.img` while preserving existing single-source package directories.

- [ ] **Step 4: Add slot-specific boot image generation**

Build `boot_a.img` and `boot_b.img` by creating temporary DTBs with `root=PARTLABEL=rootfs_a aiden.slot_suffix=_a` and `root=PARTLABEL=rootfs_b aiden.slot_suffix=_b`.

- [ ] **Step 5: Update firmware package file list**

Ensure `update.img` includes `misc.img`, `boot_a.img`, `boot_b.img`, `oem_a.img`, `oem_b.img`, `rootfs_a.img`, `rootfs_b.img`, and `userdata.img`.

- [ ] **Step 6: Verify task**

Run lightweight shell syntax check: `bash -n pico-sdk/project/build.sh`.

If Linux/case-sensitive build environment is available, run: `./build_image.sh`.

Expected on full build: `pico-sdk/output/image/` contains all A/B artifacts and `update.img`.

Checkpoint: inspect `git -C pico-sdk diff -- project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Buildroot-RV1106_Luckfox_Pico_Zero-IPC.mk project/build.sh project/scripts`.

---

## Task 7: CI, Keys, And Documentation

**Files:**
- Modify: `.github/workflows/build.yml`
- Create: `docs/OTA_AB_VERIFICATION.md`
- Create: `docs/OTA_KEY_MANAGEMENT.md`
- Create: `docs/OTA_DEVICE_ACCEPTANCE.md`

- [ ] **Step 1: Update workflow release naming**

Use `TIMESTAMP=$(date -u +"%Y%m%d-%H%M%S")` and `RELEASE_NAME="${TIMESTAMP}-${COMMIT_HASH}"` so versions are monotonic and comparable.

- [ ] **Step 2: Add manifest generation step**

After image build and release name generation, run `scripts/generate_ota_manifest.sh` with `OTA_ED25519_PRIVATE_KEY`, stable channel, image dir, output path, and build time.

- [ ] **Step 3: Keep Release upload**

Upload `pico-sdk/output/image/*`, including `manifest.json` and USB `update.img`.

- [ ] **Step 4: Add docs**

Document abctl verification, manual hardware acceptance, key generation, GitHub secret setup, key rotation, private repo token behavior, and emergency private-key compromise response.

- [ ] **Step 5: Verify task**

Run: `scripts/generate_ota_manifest.sh --help`.

Run local dry-run manifest generation with test key and fake images.

Expected: PASS.

Checkpoint: inspect `git diff -- .github/workflows/build.yml docs scripts keys overlay/oem/etc`.

---

## Task 8: Full Verification Pass

**Files:**
- All changed files.

- [ ] **Step 1: Run Go tests**

Run: `go test ./... -count=1` from `src/agent`.

Expected: PASS.

- [ ] **Step 2: Run shell syntax checks**

Run: `bash -n _build.sh _build_image.sh scripts/generate_ota_manifest.sh`

Run: `sh -n overlay/etc/init.d/S20oemslot overlay/etc/init.d/S54ota scripts/test_oemslot.sh`

Run: `scripts/test_oemslot.sh`

Run: `bash -n pico-sdk/project/build.sh`

Expected: PASS.

- [ ] **Step 3: Run local manifest dry run**

Create fake `boot_a.img`, `boot_b.img`, `oem_a.img`, `oem_b.img`, `rootfs_a.img`, `rootfs_b.img` in a temp dir, sign manifest with a test key, and verify with `ota verify-manifest`.

Expected: PASS.

- [ ] **Step 4: Inspect worktree**

Run: `git status --short --untracked-files=all`.

Expected: only intended files changed. Note any `pico-sdk` case-insensitive filesystem artifacts separately and do not include them in final changes.

- [ ] **Step 5: Report hardware-gated items**

If no device is available, explicitly report that USB flashing, UART SPL logs, slot switching, rollback, and end-to-end OTA remain hardware-gated.

Checkpoint: do not commit unless the user explicitly asks for a commit.
