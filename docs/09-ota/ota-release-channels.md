# OTA Release Channels

This document explains how the official repository distinguishes releases by branch, so that development builds never interfere with production OTA updates.

## Channel Strategy

The CI/CD pipeline assigns a release channel based on the branch being built:

| Branch | Channel | GitHub Release | Default OTA Behavior |
|--------|---------|----------------|----------------------|
| `main` | `stable` | Normal release | Auto-updates devices |
| any other branch | `dev-{branch-name}` | Prerelease | Ignored by default OTA |

For example:
- `main` → channel `stable`, normal release
- `feat/new-feature` → channel `dev-feat-new-feature`, prerelease

## How It Prevents Interference

Non-main branch builds are protected from affecting production OTA by two independent mechanisms:

### 1. Channel Validation

Devices are configured with `channel: stable` by default. The OTA updater validates that a manifest's channel matches the expected channel. A `dev-*` manifest is rejected by a `stable` device, even if it is somehow fetched.

### 2. Prerelease Marking

Non-main branch releases are marked as **prerelease** on GitHub. The `releases/latest` API only returns the newest non-prerelease release, so `stable` devices never even discover development builds during their normal update checks.

This means you can safely push experimental branches and let CI build them, without any risk to devices running production firmware.

## Testing a Development Branch Build

When you want to flash a development branch build onto a device for testing, fetch its manifest directly by URL. This bypasses the `releases/latest` lookup and the channel must still match, so point the device at the dev release explicitly.

```bash
# 1. Find the release tag for your branch build on the Releases page
#    (it will be marked as "Pre-release")
TAG="20260604-120000-abc1234"
REPO="AidenAI-IO/aiden-hardware-demo"

# 2. Update the device using the dev release manifest URL
ota check-now \
  --manifest-url "https://github.com/$REPO/releases/download/$TAG/manifest.json" \
  --public-key /oem/etc/ota_pubkey.pem
```

Notes:
- The official signing key is already trusted on the device at `/oem/etc/ota_pubkey.pem`, so no extra public key is needed for official-repo builds.
- Use `--dry-run` first to download and verify without switching slots or rebooting.

## Why Branch Builds Are Safe to Publish

Because development releases are isolated by both channel and prerelease status, they:

- **Do not** appear as the latest stable release
- **Do not** auto-update production devices
- **Do** remain available for manual testing via `--manifest-url`

This lets individual developers build and debug firmware on their own branches without coordinating with, or disrupting, anyone else.

## Related

- For distributing firmware from a fork or your own server, see [ota-external-developers.md](ota-external-developers.md).
- For quick command examples, see [ota-quick-examples.md](ota-quick-examples.md).
