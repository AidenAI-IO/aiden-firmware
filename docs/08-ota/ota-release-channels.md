---
sidebar_position: 9
---

# OTA Release Channels

GitHub Actions publication is outside the current Debian build scope. This document defines the convention to use when releases are published manually, so that development builds do not interfere with production OTA updates.

## Channel Strategy

The publisher assigns a release channel and GitHub release type based on the source branch:

| Branch | Channel | GitHub Release | Default Manual OTA Behavior |
|--------|---------|----------------|----------------------|
| `main` | `stable` | Normal release | Discoverable by `ota update` |
| any other branch | `dev-{branch-name}` | Prerelease | Ignored by default `ota update` |

For example:
- `main` → channel `stable`, normal release
- `feat/new-feature` → channel `dev-feat-new-feature`, prerelease

The `channel` value is recorded in the manifest as a human-readable label. The
OTA client does **not** match it against an expected channel (it only validates
the channel string format). Isolation comes entirely from the prerelease
mechanism described below.

## How It Prevents Interference

Non-main branch builds are protected from affecting production OTA by their
**prerelease** status on GitHub.

Non-main branch releases are marked as **prerelease**. A manual `ota update`
without `--manifest-url` uses the `releases/latest` API, which only returns the
newest non-prerelease release, so the default update path never discovers
development builds. The `dev-*` channel name is just a label that makes the
manifest easy to identify; it is not what keeps the build off production
devices.

This means an experimental build can be published manually as a prerelease
without being selected by devices using the default release lookup.

## Testing a Development Branch Build

When you want to install a development build on a device for testing, fetch its manifest directly by URL. This bypasses the `releases/latest` lookup, so point the device at the development release explicitly.

```bash
# 1. Find the manually published development tag on the Releases page
#    (it will be marked as "Pre-release")
TAG="20260604-120000-abc1234"
REPO="AidenAI-IO/aiden-firmware"

# 2. Update the device using the dev release manifest URL
ota update \
  --manifest-url "https://github.com/$REPO/releases/download/$TAG/manifest.json" \
  --public-key /oem/etc/ota_pubkey.pem
```

Notes:
- The official signing key is already trusted on the device at `/oem/etc/ota_pubkey.pem`, so no extra public key is needed for official-repo builds.
- Use `--dry-run` first to download and verify without switching slots or rebooting.

## Why Branch Builds Are Safe to Publish

Because development releases are published as prereleases, they:

- **Do not** appear as the latest stable release
- **Do not** get selected by the default `ota update` release lookup
- **Do** remain available for manual testing via `--manifest-url`

This lets individual developers build and debug firmware on their own branches without coordinating with, or disrupting, anyone else.

## Related

- For distributing firmware from a fork or your own server, see [ota-external-developers.md](ota-external-developers.md).
- For quick command examples, see [ota-quick-examples.md](ota-quick-examples.md).
