# OTA for External Developers

This guide explains how external developers can distribute custom firmware using the Aiden OTA system.

## Overview

The OTA system supports distributing firmware from any source using `--manifest-url`:

```bash
ota check-now \
  --manifest-url "https://example.com/path/to/manifest.json" \
  --public-key /path/to/your_pubkey.pem
```

The manifest must be signed with your Ed25519 private key, and devices must explicitly trust your public key.

## Quick Start

### 1. Generate Signing Keys

```bash
# Generate private key (keep this secret!)
openssl genpkey -algorithm ed25519 -out ota_private_key.pem

# Extract public key (distribute to users)
openssl pkey -in ota_private_key.pem -pubout -out ota_public_key.pem
```

### 2. Build and Sign Firmware

```bash
# Build firmware
./build_image.sh

# Generate signed manifest with direct download URLs
scripts/generate_ota_manifest.sh \
  --version "v1.0.0-custom" \
  --channel "custom" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "https://your-server.com/firmware/v1.0.0"
```

### 3. Host Firmware

Upload the manifest and images to any web server that can serve static files.

### 4. Update Devices

```bash
# Copy your public key to device
scp ota_public_key.pem root@192.168.50.188:/userdata/ota/custom_pubkey.pem

# On device, update from your manifest
ota check-now \
  --manifest-url "https://your-server.com/firmware/v1.0.0/manifest.json" \
  --public-key /userdata/ota/custom_pubkey.pem
```

## Hosting Options

### Option 1: GitHub Releases (Recommended)

Most developers will use GitHub to build and host firmware.

**Steps:**

1. **Generate manifest with GitHub direct URLs:**
```bash
TAG="v1.0.0-custom"
REPO="YOUR_USERNAME/aiden-hardware-demo"
BASE_URL="https://github.com/$REPO/releases/download/$TAG"

scripts/generate_ota_manifest.sh \
  --version "$TAG" \
  --channel "custom" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "$BASE_URL"
```

2. **Create GitHub Release:**
```bash
gh release create "$TAG" \
  --title "Custom Firmware v1.0.0" \
  --notes "Custom build" \
  pico-sdk/output/image/*.img \
  pico-sdk/output/image/manifest.json
```

3. **Update devices:**
```bash
MANIFEST_URL="https://github.com/$REPO/releases/download/$TAG/manifest.json"

ota check-now \
  --manifest-url "$MANIFEST_URL" \
  --public-key /userdata/ota/custom_pubkey.pem
```

**Benefits:**
- Free hosting
- Automatic CI/CD with GitHub Actions  
- Works with private repositories
- No extra infrastructure needed

### Option 2: Self-Hosted Server

For corporate/internal deployments or air-gapped environments.

**Steps:**

1. **Generate manifest with your server URLs:**
```bash
BASE_URL="https://firmware.mycompany.com/aiden/v1.0.0"

scripts/generate_ota_manifest.sh \
  --version "v1.0.0-internal" \
  --channel "internal" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "$BASE_URL"
```

2. **Upload to your server:**
```bash
# Example directory structure on server:
# /var/www/firmware/aiden/v1.0.0/
#   ├── manifest.json
#   ├── boot_a.img
#   ├── boot_b.img
#   ├── oem.img
#   └── rootfs.img

rsync -avz pico-sdk/output/image/*.img \
  pico-sdk/output/image/manifest.json \
  user@firmware.mycompany.com:/var/www/firmware/aiden/v1.0.0/
```

3. **Update devices:**
```bash
ota check-now \
  --manifest-url "https://firmware.mycompany.com/aiden/v1.0.0/manifest.json" \
  --public-key /userdata/ota/company_pubkey.pem
```

### Option 3: Local Development

For testing without external hosting.

```bash
# Generate manifest with localhost URLs
scripts/generate_ota_manifest.sh \
  --version "dev-$(date +%Y%m%d-%H%M%S)" \
  --channel "dev" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "http://192.168.1.100:8000"

# Start local HTTP server
cd pico-sdk/output/image
python3 -m http.server 8000

# Test on device (use --dry-run to avoid flashing)
ota check-now \
  --manifest-url "http://192.168.1.100:8000/manifest.json" \
  --public-key /userdata/ota/dev_pubkey.pem \
  --dry-run
```

## Manifest Structure

When you use `--base-url`, the manifest includes direct download URLs:

```json
{
  "schema_version": 1,
  "channel": "custom",
  "version": "v1.0.0-custom",
  "build_time": "2026-06-04T12:00:00Z",
  "parts": [
    {
      "name": "boot",
      "asset_a": {
        "name": "boot_a.img",
        "url": "https://github.com/USER/REPO/releases/download/TAG/boot_a.img",
        "size": 12345678,
        "sha256": "abc..."
      },
      "asset_b": {
        "name": "boot_b.img",
        "url": "https://github.com/USER/REPO/releases/download/TAG/boot_b.img",
        "size": 12345678,
        "sha256": "def..."
      }
    }
  ],
  "signature": {
    "algorithm": "ed25519",
    "value": "..."
  }
}
```

## Persistent Configuration

Instead of passing parameters every time, configure the device permanently:

```bash
# On device, edit /userdata/ota/config.json
cat > /userdata/ota/config.json << 'EOF'
{
  "manifest_url": "https://your-server.com/firmware/latest/manifest.json",
  "public_key_path": "/userdata/ota/custom_pubkey.pem",
  "interval_seconds": 3600
}
EOF

# Restart OTA daemon
/etc/init.d/S54ota restart
```

Now the device will automatically check your custom source every hour.

## Security Considerations

### Signature Verification
- **Always required** - All manifests must be signed
- Users must explicitly trust your public key
- Keep your private key secure and never commit it to git

### HTTPS vs HTTP
- **HTTPS recommended** for production
- HTTP allowed for local development/testing
- Devices will accept both but log warnings for HTTP

### Version Management
- Use monotonic version strings (timestamp-based recommended)
- OTA prevents downgrades by default
- Include git commit hash for traceability

### Public Key Distribution
- Distribute public key through secure channel
- Users should verify key fingerprint
- Consider multiple signing keys for different channels

## Channel Strategy

Recommended channel naming:

- `stable` - Official releases from main branch
- `beta` - Pre-release testing
- `dev` - Development builds (your custom builds)
- `internal` - Enterprise/private builds

Configure devices to only accept specific channels:
```json
{
  "channel": "dev",
  "manifest_url": "https://your-server.com/firmware/dev/manifest.json"
}
```

## Troubleshooting

### Check OTA logs
```bash
tail -f /var/log/ota/ota.log
```

### Verify manifest signature manually
```bash
ota verify-manifest /path/to/manifest.json --public-key /path/to/pubkey.pem
```

### Test download without flashing
```bash
ota check-now --manifest-url URL --public-key KEY --dry-run
```

### Common Issues

**"manifest signature verification failed"**
- Wrong public key
- Manifest was modified after signing
- Private key doesn't match public key

**"manifest channel mismatch"**
- Device expects different channel
- Solution: Match channel in device config or use `--channel` parameter

**"missing required release asset"**
- Using GitHub Release without `--base-url`
- Asset URLs in manifest but Release API mode used
- Solution: Use `--manifest-url` for direct URL manifests

## Example: Complete Custom Distribution

Here's a complete example for distributing custom firmware:

```bash
#!/bin/bash
set -euo pipefail

# Configuration
VERSION="20260604-$(git rev-parse --short HEAD)"
CHANNEL="community"
BASE_URL="https://cdn.example.com/aiden-firmware/$VERSION"
SIGN_KEY="./keys/ota_private_key.pem"

# Build
echo "Building firmware..."
./build_image.sh

# Generate manifest with URLs
echo "Generating signed manifest..."
scripts/generate_ota_manifest.sh \
  --version "$VERSION" \
  --channel "$CHANNEL" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key "$SIGN_KEY" \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "$BASE_URL"

# Upload to CDN
echo "Uploading to CDN..."
aws s3 sync pico-sdk/output/image/ \
  s3://my-firmware-bucket/aiden-firmware/$VERSION/ \
  --exclude "*" \
  --include "*.img" \
  --include "manifest.json"

# Update "latest" symlink
echo "$VERSION" > latest.txt
aws s3 cp latest.txt s3://my-firmware-bucket/aiden-firmware/latest.txt

echo "Firmware published!"
echo "Users can update with:"
echo "  ota check-now --manifest-url $BASE_URL/manifest.json --public-key /path/to/pubkey.pem"
```

## Support

For issues or questions:
- Check logs in `/var/log/ota/ota.log`
- Use `--dry-run` for testing
- Join community discussions
