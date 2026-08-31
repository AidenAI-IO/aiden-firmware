---
sidebar_position: 8
---

# OTA Distribution Quick Examples

All examples use the Debian production entrypoint. It builds and audits the
complete firmware, signs `manifest.json`, and writes publishable artifacts to
`output/debian/image/`.

## Example 1: GitHub Releases (Recommended)

```bash
# 1. Generate signing keys once.
openssl genpkey -algorithm ed25519 -out ota_private_key.pem
openssl pkey -in ota_private_key.pem -pubout -out ota_public_key.pem

# 2. Build a signed Debian release.
TAG="v1.0.0-custom"
REPO="YOUR_USERNAME/aiden-firmware"
OTA_PRIVATE_KEY_PATH="$PWD/ota_private_key.pem" \
OTA_PUBLIC_KEY_PATH="$PWD/ota_public_key.pem" \
AGENT_CONFIG_PATH="$PWD/agent.toml" \
OTA_REPO="$REPO" \
OTA_CHANNEL=custom \
OTA_BUILD_VERSION="$TAG" \
OTA_BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
./debian_build.sh

# 3. Publish only the signed manifest and compressed image assets.
gh release create "$TAG" \
  --title "Custom Firmware $TAG" \
  output/debian/image/boot_a.img.tar.gz \
  output/debian/image/boot_b.img.tar.gz \
  output/debian/image/oem.img.tar.gz \
  output/debian/image/rootfs.img.tar.gz \
  output/debian/image/update.img.tar.gz \
  output/debian/image/manifest.json

# 4. Update a device that trusts this signing key.
ota update \
  --manifest-url "https://github.com/$REPO/releases/download/$TAG/manifest.json" \
  --public-key /userdata/ota/custom_pubkey.pem
```

## Example 2: Self-Hosted Server

```bash
VERSION="v1.0.0"
BASE_URL="https://firmware.mycompany.com/aiden/$VERSION"

OTA_PRIVATE_KEY_PATH="$PWD/ota_private_key.pem" \
OTA_PUBLIC_KEY_PATH="$PWD/ota_public_key.pem" \
AGENT_CONFIG_PATH="$PWD/agent.toml" \
OTA_CHANNEL=internal \
OTA_BUILD_VERSION="$VERSION" \
OTA_BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
OTA_BASE_URL="$BASE_URL" \
./debian_build.sh

rsync -avz \
  output/debian/image/*.img.tar.gz \
  output/debian/image/manifest.json \
  user@server:/var/www/firmware/aiden/$VERSION/

ota update \
  --manifest-url "$BASE_URL/manifest.json" \
  --public-key /userdata/ota/company_pubkey.pem
```

## Example 3: Local Development

```bash
VERSION="dev-$(date +%Y%m%d-%H%M%S)"
BASE_URL="http://192.168.1.100:8000"

OTA_PRIVATE_KEY_PATH="$PWD/ota_private_key.pem" \
OTA_PUBLIC_KEY_PATH="$PWD/ota_public_key.pem" \
AGENT_CONFIG_PATH="$PWD/agent.toml" \
OTA_CHANNEL=dev \
OTA_BUILD_VERSION="$VERSION" \
OTA_BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
OTA_BASE_URL="$BASE_URL" \
./debian_build.sh

cd output/debian/image
python3 -m http.server 8000
```

On the device, verify downloads and signatures without switching slots:

```bash
ota update \
  --manifest-url "http://192.168.1.100:8000/manifest.json" \
  --public-key /userdata/ota/dev_pubkey.pem \
  --dry-run
```

## Security Notes

1. Keep the private key and `agent.toml` outside the repository.
2. Use HTTPS outside an isolated development network.
3. Distribute and verify the public key through a trusted channel.
4. Publish the exact manifest and archives produced by the same build; changing
   any asset after signing makes verification fail.
