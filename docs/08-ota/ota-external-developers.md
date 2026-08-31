---
sidebar_position: 7
---

# OTA for External Developers

External distributors use the same Debian build and image audit as official
releases. A device can install from any HTTP(S) source when given a signed
manifest and the matching trusted Ed25519 public key.

## Build a Custom Release

Generate a signing key pair and keep the private key outside the repository:

```bash
openssl genpkey -algorithm ed25519 -out ota_private_key.pem
openssl pkey -in ota_private_key.pem -pubout -out ota_public_key.pem
scripts/validate_ota_pubkey.sh ota_public_key.pem
```

Build the complete Debian firmware and signed OTA asset set:

```bash
VERSION="v1.0.0-custom"
CHANNEL="custom"
BASE_URL="https://firmware.example.com/aiden/$VERSION"

OTA_PRIVATE_KEY_PATH="$PWD/ota_private_key.pem" \
OTA_PUBLIC_KEY_PATH="$PWD/ota_public_key.pem" \
AGENT_CONFIG_PATH="$PWD/agent.toml" \
OTA_CHANNEL="$CHANNEL" \
OTA_BUILD_VERSION="$VERSION" \
OTA_BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
OTA_BASE_URL="$BASE_URL" \
./debian_build.sh
```

`OTA_BASE_URL` is optional. Set it for direct static hosting. Omit it for the
normal GitHub Release API flow and set `OTA_REPO=OWNER/REPO` instead.

The publishable directory contains:

```text
output/debian/image/
├── boot_a.img.tar.gz
├── boot_b.img.tar.gz
├── manifest.json
├── oem.img.tar.gz
├── rootfs.img.tar.gz
└── update.img.tar.gz
```

The directory also contains the directly flashable local `update.img` and its
checksum. The release allowlist intentionally publishes the compressed image,
not that extra local copy.

## Host the Assets

### GitHub Releases

Build without `OTA_BASE_URL`, set `OTA_REPO`, then upload the exact six release
assets:

```bash
TAG="$VERSION"
REPO="YOUR_USERNAME/aiden-firmware"

gh release create "$TAG" \
  --title "Custom Firmware $TAG" \
  output/debian/image/boot_a.img.tar.gz \
  output/debian/image/boot_b.img.tar.gz \
  output/debian/image/oem.img.tar.gz \
  output/debian/image/rootfs.img.tar.gz \
  output/debian/image/update.img.tar.gz \
  output/debian/image/manifest.json
```

### Static Web Server or CDN

Build with `OTA_BASE_URL` set to the final directory URL, then upload without
renaming or recompressing anything:

```bash
rsync -avz \
  output/debian/image/*.img.tar.gz \
  output/debian/image/manifest.json \
  user@firmware.example.com:/var/www/firmware/aiden/$VERSION/
```

For local testing, serve the same directory directly:

```bash
cd output/debian/image
python3 -m http.server 8000
```

## Trust and Update

Copy the public key to the device through a trusted channel:

```bash
DEVICE_IP="192.168.42.1"
scp ota_public_key.pem "root@$DEVICE_IP:/userdata/ota/custom_pubkey.pem"
```

Run an update from a direct manifest URL:

```bash
ota update \
  --manifest-url "https://firmware.example.com/aiden/v1.0.0-custom/manifest.json" \
  --public-key /userdata/ota/custom_pubkey.pem
```

Add `--dry-run` to download and verify every asset without writing partitions or
switching slots.

## Persistent Device Configuration

The Debian factory configuration lives at
`/userdata/debian/ota/config.json`. To pin a device to a custom source, add
`manifest_url` and `public_key_path` while preserving the generated factory
version, build time, safety margin, and partition hashes:

```json
{
  "manifest_url": "https://firmware.example.com/aiden/latest/manifest.json",
  "public_key_path": "/userdata/ota/custom_pubkey.pem"
}
```

Do not replace the complete generated file with only those two fields. The
factory baseline is required for downgrade and inactive-slot verification.

## Manifest Rules

The generated manifest is signed canonical JSON. Each `parts` entry names
`boot`, `oem`, or `rootfs` and contains either a slot-neutral `asset` or
slot-specific `asset_a` and `asset_b` values.

Published assets are `.img.tar.gz` files. For each asset:

- `size` and `sha256` describe the downloaded archive;
- `image_sha256` describes the extracted image written to the partition;
- `url` is present when `OTA_BASE_URL` was supplied;
- `boot` remains slot-specific because its DTB carries slot-specific bootargs.

The build rejects a target-slot download set that exceeds the capacity derived
from the dedicated OTA partition.

## Channel and Version Strategy

The channel must match `[A-Za-z0-9._-]`. It is a release label rather than an
authorization boundary. Use a specific manifest URL and signing key to control
which firmware a device trusts.

Use monotonically increasing build times and traceable versions, for example a
UTC timestamp plus Git commit. OTA rejects older build times and conflicting
versions with the same build time.

## Security Requirements

- Never commit the private signing key or production `agent.toml`.
- Use HTTPS for production distribution.
- Verify the public key fingerprint through a separate trusted channel.
- Never modify, rename, or recompress assets after `manifest.json` is signed.
- Treat different distribution authorities as separate signing keys.

## Troubleshooting

Inspect the Debian boot-time health unit and log:

```bash
systemctl status aiden-ota-health.service
journalctl -u aiden-ota-health.service
tail -f /var/log/ota/ota.log
```

Useful checks:

```bash
ota status
ota update --manifest-url URL --public-key KEY --dry-run
ota verify-manifest /path/to/manifest.json --public-key /path/to/pubkey.pem
```

Common failures:

- `manifest signature verification failed`: wrong key, modified manifest, or a
  private/public key mismatch.
- `invalid channel`: the channel contains unsupported characters.
- `missing required release asset`: the hosted filename differs from the signed
  manifest or the GitHub Release does not contain all six allowlisted assets.
- archive or image checksum mismatch: an asset was changed after signing.
