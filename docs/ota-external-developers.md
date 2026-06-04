# OTA for External Developers

This guide explains how external developers can distribute custom firmware using the Aiden OTA system.

## Overview

The OTA system now supports multiple distribution methods:

1. **GitHub Release (Standard)** - Host firmware on GitHub
2. **Direct URLs** - Host firmware on any web server
3. **Custom Backend** - Implement GitHub-compatible API

## Method 1: GitHub Release (Recommended for Open Source)

### Requirements
- GitHub repository (can be a fork)
- Ed25519 key pair for signing manifests
- GitHub Actions or local build environment

### Steps

1. **Generate your signing key pair:**
```bash
# Generate private key
openssl genpkey -algorithm ed25519 -out ota_private_key.pem

# Extract public key
openssl pkey -in ota_private_key.pem -pubout -out ota_public_key.pem
```

2. **Build and sign your firmware:**
```bash
# Build firmware (use Docker for consistency)
./build_image.sh

# Generate signed manifest
scripts/generate_ota_manifest.sh \
  --version "20260604-custom-$(git rev-parse --short HEAD)" \
  --channel "custom" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json
```

3. **Create GitHub Release:**
```bash
gh release create v1.0.0-custom \
  --title "Custom Firmware v1.0.0" \
  --notes "Custom build with XYZ features" \
  pico-sdk/output/image/boot_a.img \
  pico-sdk/output/image/boot_b.img \
  pico-sdk/output/image/oem.img \
  pico-sdk/output/image/rootfs.img \
  pico-sdk/output/image/manifest.json
```

4. **Update device to use your firmware:**
```bash
# Copy your public key to the device
scp ota_public_key.pem root@192.168.50.188:/userdata/ota/custom_pubkey.pem

# On the device, check for updates
ota check-now \
  --repo YOUR_USERNAME/aiden-hardware-demo \
  --channel stable \
  --public-key /userdata/ota/custom_pubkey.pem
```

## Method 2: Direct URLs (Recommended for Private/Internal)

This method allows you to host firmware on any web server without implementing GitHub API.

### Requirements
- Web server (Nginx, Apache, S3, CDN, etc.)
- Ed25519 key pair for signing

### Steps

1. **Build firmware and generate manifest with URLs:**
```bash
# Build firmware
./build_image.sh

# Generate manifest with direct URLs
BASE_URL="https://firmware.example.com/aiden/v1.0.0"

scripts/generate_ota_manifest.sh \
  --version "20260604-internal-001" \
  --channel "internal" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "$BASE_URL"
```

This generates a manifest like:
```json
{
  "schema_version": 1,
  "channel": "internal",
  "version": "20260604-internal-001",
  "build_time": "2026-06-04T12:00:00Z",
  "parts": [
    {
      "name": "boot",
      "asset_a": {
        "name": "boot_a.img",
        "url": "https://firmware.example.com/aiden/v1.0.0/boot_a.img",
        "size": 12345678,
        "sha256": "abc..."
      },
      ...
    }
  ],
  "signature": {...}
}
```

2. **Upload firmware to your server:**
```bash
# Example using rsync
rsync -avz pico-sdk/output/image/*.img \
  pico-sdk/output/image/manifest.json \
  user@firmware.example.com:/var/www/firmware/aiden/v1.0.0/
```

3. **Server configuration (Nginx example):**
```nginx
server {
    listen 443 ssl;
    server_name firmware.example.com;
    
    ssl_certificate /etc/ssl/certs/firmware.example.com.crt;
    ssl_certificate_key /etc/ssl/private/firmware.example.com.key;
    
    location /aiden/ {
        root /var/www/firmware;
        autoindex off;
        add_header Access-Control-Allow-Origin *;
    }
}
```

4. **Update device:**
```bash
# Copy public key to device
scp ota_public_key.pem root@192.168.50.188:/userdata/ota/custom_pubkey.pem

# On device, update using direct manifest URL
ota check-now \
  --manifest-url "https://firmware.example.com/aiden/v1.0.0/manifest.json" \
  --public-key /userdata/ota/custom_pubkey.pem
```

## Method 3: Local Development

For development and testing, you can use a local HTTP server.

```bash
# Generate manifest with localhost URLs
scripts/generate_ota_manifest.sh \
  --version "20260604-dev-$(git rev-parse --short HEAD)" \
  --channel "dev" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "http://192.168.1.100:8000"

# Start simple HTTP server
cd pico-sdk/output/image
python3 -m http.server 8000

# On device (ensure it can reach your dev machine)
ota check-now \
  --manifest-url "http://192.168.1.100:8000/manifest.json" \
  --public-key /userdata/ota/dev_pubkey.pem \
  --dry-run  # Test without actually flashing
```

## Persistent Configuration

Instead of passing parameters every time, you can configure the device permanently:

```bash
# On device, edit /userdata/ota/config.json
cat > /userdata/ota/config.json << 'EOF'
{
  "manifest_url": "https://firmware.example.com/aiden/latest/manifest.json",
  "public_key_path": "/userdata/ota/custom_pubkey.pem",
  "interval_seconds": 3600,
  "channel": "custom"
}
EOF

# Restart OTA daemon
/etc/init.d/S54ota restart

# Updates will now automatically check your custom source
```

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
