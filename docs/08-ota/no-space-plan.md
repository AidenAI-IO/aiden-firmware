# OTA Reserved Space Design

## Background

`/userdata` is a shared 3 GiB ext4 partition. OTA downloads compete with swap, recordings, logs, and other runtime data. This previously caused downloads to fail with:

```text
write /userdata/ota/downloads/oem.img.tar.gz.part: no space left on device
```

The earlier design cleaned the cache and used `statfs` before each update. That could detect low space at check time, but logs or recordings could still consume the available capacity before the download started.

The current design maintains reserved space continuously. During normal operation, a fully allocated file holds the OTA budget. The space is released only after OTA acquires the update lock and is ready to download.

## Default Budget

- Reserved budget: 200 MiB.
- Download safety margin: 4 MiB for ext4 metadata, directory entries, and `fsync` overhead.
- Reserve file: `<download_dir>/.ota-reserve`.
- Default eMMC path: `/userdata/ota/downloads/.ota-reserve`.

Across the latest 20 production manifests, the largest target-slot download set was in `20260730-032204-49de84e`:

| Asset | Size |
| --- | ---: |
| `boot_b.img.tar.gz` (one A/B asset) | 3.45 MiB |
| `oem.img.tar.gz` | 25.26 MiB |
| `rootfs.img.tar.gz` (referenced from an earlier Release) | 97.90 MiB |
| Total | 126.61 MiB |

Including the 4 MiB margin, this set requires 130.61 MiB. A 200 MiB budget leaves about 69.39 MiB for growth.

The device downloads only the manifest assets selected for the target slot whose partition hashes do not already match. It does not download the other boot slot or the roughly 254 MiB `update.img.tar.gz` asset.

## Space Model

The reserve is not always a single 200 MiB file. It represents a fixed OTA budget:

```text
effective download cache + .ota-reserve = reserve_size_bytes
```

For example, when 126 MiB of reusable cache already exists, the reserve file is reduced to 74 MiB. The cache remains reusable while ordinary runtime data cannot consume the unused 74 MiB.

The reserve file is created by writing zero-filled blocks and calling `fsync`. It is not extended with sparse-file truncation, so its logical size corresponds to allocated filesystem blocks.

### Technical Principle

The filesystem has no native capacity reservation dedicated to OTA. The reserve file simulates one by allocating the budget's blocks before they are needed.

Other processes therefore see less available space and cannot consume the OTA budget. After acquiring `update.lock`, OTA removes the reserve file and the filesystem immediately returns those blocks.

After the download succeeds or fails, OTA restores the invariant that cache plus reserve equals 200 MiB.

Compressed `.tar.gz` images are not extracted into temporary files under `/userdata`. They are decompressed as a stream and written directly to the inactive block device.

Persistent-storage peak usage therefore includes only compressed packages, partial downloads, and the reserve file.

## Lifecycle

### 1. Restore the Reserve at Boot

`S54ota` runs `ota health` on every boot. Pending-health handling and reserve restoration use the same OTA update lock:

1. Process a pending update and either commit it or handle rollback.
2. If the SD/eMMC route changed, clear the inactive OTA cache directory so only one budget remains allocated.
3. Count existing cache and partial-download files in the selected directory.
4. Resize `.ota-reserve` to the configured budget minus the cache size.

If another `ota update` process holds the lock, `ota health` does not recreate the reserve. This prevents it from reclaiming blocks that an active download has just released.

### 2. Before Downloading

After signature verification, version-policy checks, and partition-size validation succeed, OTA:

1. Removes cache entries that do not belong to the current target slot.
2. Removes complete packages whose names, sizes, or SHA-256 values do not match the manifest.
3. Keeps valid complete packages and size-compatible `.part` files for the target assets.
4. Restores the configured reserve budget.
5. Calculates remaining bytes from the verified manifest's `asset.size` values.
6. Confirms that remaining bytes plus the 4 MiB margin fit in the current reserve file.
7. Deletes `.ota-reserve` and starts downloading.

OTA no longer uses `statfs` to decide whether an update may start. The update consumes only the budget that was allocated in advance.

### 3. After Success or Failure

- After successful download and verification, OTA restores the reserve budget before writing partitions.
- After a network failure, OTA keeps `.part` and restores only the remaining reserve balance.
- After `ENOSPC`, OTA deletes the current `.part` as a fallback, preventing retries from repeatedly filling the filesystem at the same offset.
- Verified complete cache files may remain. Together with the smaller reserve file, they still consume only the configured budget and can be reused by a later update.

## Configuration

`/userdata/ota/config.json` supports:

```json
{
  "reserve_size_bytes": 209715200,
  "reserve_safety_margin_bytes": 4194304
}
```

| Field | Default | Description |
| --- | ---: | --- |
| `reserve_size_bytes` | 209715200 | Total budget for the download cache and reserve file; defaults to 200 MiB |
| `reserve_safety_margin_bytes` | 4194304 | Additional capacity that must remain after releasing the reserve; defaults to 4 MiB |

If a future update needs more capacity than the current reserve file, OTA fails before requesting any asset:

```text
OTA reserve too small: need 204.0 MiB for remaining downloads and safety margin,
reserved 200.0 MiB; increase reserve_size_bytes
```

This indicates that the release has exceeded the device's reserved-space policy. The release or configuration must be adjusted instead of temporarily deleting user data.

## SD Card Behavior

The reserve file follows `download_dir`. OTA selects `<sd-mount>/aiden/ota-cache` only when the SD card is mounted and its available space plus existing OTA cache or reserve can hold the full 200 MiB budget.

Otherwise, OTA uses eMMC. OTA state files always remain under `/userdata/ota`.

When mounting an SD card, StorageManager credits existing OTA cache and reserve files toward recoverable capacity. This prevents a restart from rejecting a card merely because its reserve already occupies space.

Audio migration from eMMC continues only when the SD card's actual remaining space stays above the general write floor.

OTA and eMMC-to-SD migration coordinate through `/userdata/ota/update.lock`. StorageManager does not start migration while OTA holds the lock.

An active migration stops after finishing its current file, preventing it from consuming SD blocks released for an OTA download.

## StorageMonitor, Cleanup, and Notifications

Reserved space replaces the temporary pre-download `statfs` decision. It does not replace storage governance:

- StorageMonitor continues to monitor `/userdata`. Its default Warning, Critical, and Emergency thresholds remain 50, 10, and 5 MiB.
- An eMMC reserve appears as used space in StorageMonitor samples by design. A reserve on SD does not affect `/userdata` monitoring.
- StorageMonitor continues cleaning logs, audio archives, and session archives. Its cleaners do not include the OTA directory and cannot remove `.ota-reserve`, verified cache, or `.part` files.
- OTA still removes invalid cache before downloading and clears the inactive SD/eMMC cache when routing changes. These operations maintain a single budget and prevent invalid resume state.
- The `ENOSPC` fallback still removes the current `.part`. Ordinary network failures keep `.part` for resumption.
- Reserve-allocation, insufficient-budget, and download failures continue to update `state.json.LastError` and print errors to stderr.
- This design does not add a separate voice notification or phone popup. Existing StorageMonitor alerts and degraded-mode notifications remain active.

## Release Constraint

`generate_ota_manifest.sh` checks the worst download combination for both A/B target slots.

After subtracting the 4 MiB margin from the 200 MiB budget, manifest assets may total at most 196 MiB for either target slot. CI fails before signing and publishing when this limit is exceeded.

## Operational Checks

```bash
# Inspect the reserve and cache.
ls -lh /userdata/ota/downloads
du -h /userdata/ota/downloads/* /userdata/ota/downloads/.ota-reserve 2>/dev/null

# Restore the reserve manually while also processing pending health.
/oem/usr/bin/ota health

# Inspect reserve-related logs.
grep 'ota reserve' /var/log/ota/ota.log
```

## Known Boundaries

- Reserved space protects updates only after the reserve has been created successfully. The old OTA client still controls the first update from firmware that predates this mechanism.
- Non-StorageManager system logs may continue writing between reserve release and restoration. The 4 MiB margin, current package-growth headroom, and `ENOSPC` cleanup provide fallback protection.
- If storage is already full during boot, OTA may be unable to create the reserve for the first time. `ota health` reports and records the error; an operator must free space once.

## Verification Scope

- `ota health` creates a physically allocated reserve file of the requested size.
- Existing `.part` files reduce the reserve amount by their current size.
- Reserve allocation does not occur without the OTA update lock.
- The reserve file is absent while asset requests are running.
- Cache plus reserve returns to the configured budget after a download.
- Stale cache is removed before downloading.
- A download larger than the reserve fails before any asset request and records the `reserve` phase in `state.json`.
- `ENOSPC` removes `.part`; ordinary network failures retain it.
- OTA falls back to eMMC when the SD card cannot hold the budget, while existing OTA cache and reserve count toward SD capacity.
- Switching between SD and eMMC clears the inactive OTA cache so a second budget is not left allocated.
- eMMC-to-SD audio migration does not start while the OTA update lock is held.
- Manifest generation fails when either target slot exceeds 196 MiB.
