# OTA Open Sources - Quick Examples

## Example 1: GitHub Releases (Recommended)

```bash
# 1. Generate signing keys
openssl genpkey -algorithm ed25519 -out ota_private_key.pem
openssl pkey -in ota_private_key.pem -pubout -out ota_public_key.pem

# 2. Build firmware
./build_image.sh

# 3. Generate manifest with GitHub direct URLs
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

# 4. Create GitHub Release
gh release create "$TAG" \
  --title "Custom Firmware v1.0.0" \
  pico-sdk/output/image/*.img \
  pico-sdk/output/image/manifest.json

# 5. Update device
MANIFEST_URL="https://github.com/$REPO/releases/download/$TAG/manifest.json"
ota update \
  --manifest-url "$MANIFEST_URL" \
  --public-key /userdata/ota/your_pubkey.pem
```

## Example 2: Self-Hosted Server

```bash
# 1. Build firmware
./build_image.sh

# 2. Generate manifest with your server URLs
scripts/generate_ota_manifest.sh \
  --version "v1.0.0" \
  --channel "internal" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "https://firmware.mycompany.com/aiden/v1.0.0"

# 3. Upload to server
rsync -avz pico-sdk/output/image/*.img \
  pico-sdk/output/image/manifest.json \
  user@server:/var/www/firmware/aiden/v1.0.0/

# 4. Update device
ota update \
  --manifest-url "https://firmware.mycompany.com/aiden/v1.0.0/manifest.json" \
  --public-key /userdata/ota/company_pubkey.pem
```

## Example 3: Local Development

```bash
# 1. Generate manifest with localhost URLs
scripts/generate_ota_manifest.sh \
  --version "dev-$(date +%Y%m%d-%H%M%S)" \
  --channel "dev" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "http://192.168.1.100:8000"

# 2. Start local server
cd pico-sdk/output/image && python3 -m http.server 8000

# 3. Test on device (without flashing)
ota update \
  --manifest-url "http://192.168.1.100:8000/manifest.json" \
  --public-key /userdata/ota/dev_pubkey.pem \
  --dry-run
```

## Key Points

**Single Parameter**: Just use `--manifest-url` to specify where to fetch the manifest.

**Signing Required**: All manifests must be signed with your Ed25519 private key.

**Trust Required**: Users must explicitly trust your public key with `--public-key`.

**Any Hosting Works**: GitHub, self-hosted server, S3, CDN, localhost - anything that serves static files.

## Persistent Configuration

To avoid typing parameters every time:

```bash
# On device, create custom config
cat > /userdata/ota/config.json << 'EOF'
{
  "manifest_url": "https://your-server.com/firmware/latest/manifest.json",
  "public_key_path": "/userdata/ota/your_pubkey.pem",
  "interval_seconds": 3600
}
EOF

# Restart OTA service
/etc/init.d/S54ota restart
```

Now the device will automatically check your custom source every hour!

## Security Notes

1. **Keep private key secure** - Never commit to git or share publicly
2. **Use HTTPS in production** - HTTP is OK for local testing only
3. **Users must trust your key** - They must explicitly specify `--public-key`
4. **Signature is mandatory** - All manifests must be signed, no exceptions
