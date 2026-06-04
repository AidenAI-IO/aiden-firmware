# OTA Open Sources - Quick Examples

## Example 1: Fork Repository and Build Custom Firmware

```bash
# 1. Fork the repository on GitHub
# 2. Generate your signing keys
openssl genpkey -algorithm ed25519 -out ota_private_key.pem
openssl pkey -in ota_private_key.pem -pubout -out ota_public_key.pem

# 3. Add private key to GitHub Secrets (Settings -> Secrets -> Actions)
#    Name: OTA_ED25519_PRIVATE_KEY
#    Value: <contents of ota_private_key.pem>

# 4. Push to your fork - GitHub Actions builds and creates release automatically

# 5. On device, update from your fork:
ota check-now \
  --repo YOUR_USERNAME/aiden-hardware-demo \
  --public-key /userdata/ota/your_pubkey.pem
```

## Example 2: GitHub Release with Direct URLs (Faster, Bypasses API)

```bash
# 1. Build firmware locally
./build_image.sh

# 2. Generate manifest with GitHub direct download URLs
TAG="v1.0.0-custom"
REPO="YOUR_USERNAME/aiden-hardware-demo"
BASE_URL="https://github.com/$REPO/releases/download/$TAG"

scripts/generate_ota_manifest.sh \
  --version "v1.0.0-custom" \
  --channel "custom" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "$BASE_URL"

# 3. Create GitHub Release
gh release create "$TAG" \
  --title "Custom Firmware v1.0.0" \
  --notes "Custom build" \
  pico-sdk/output/image/*.img \
  pico-sdk/output/image/manifest.json

# 4. Device updates directly (no Release API calls)
MANIFEST_URL="https://github.com/$REPO/releases/download/$TAG/manifest.json"
ota check-now \
  --manifest-url "$MANIFEST_URL" \
  --public-key /userdata/ota/your_pubkey.pem
```

## Example 3: Self-Hosted Firmware Server

```bash
# 1. Build firmware locally
./build_image.sh

# 2. Generate manifest with direct URLs
scripts/generate_ota_manifest.sh \
  --version "v1.0.0" \
  --channel "stable" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "https://firmware.mycompany.com/aiden/v1.0.0"

# 3. Upload to your server
# Directory structure:
# /var/www/firmware/aiden/v1.0.0/
#   ├── manifest.json
#   ├── boot_a.img
#   ├── boot_b.img
#   ├── oem.img
#   └── rootfs.img

# 4. Device updates directly from your server:
ota check-now \
  --manifest-url "https://firmware.mycompany.com/aiden/v1.0.0/manifest.json" \
  --public-key /userdata/ota/company_pubkey.pem
```

## Example 4: Development Testing with Local Server

```bash
# 1. Build firmware
./build_image.sh

# 2. Generate manifest for local testing
scripts/generate_ota_manifest.sh \
  --version "dev-$(date +%Y%m%d-%H%M%S)" \
  --channel "dev" \
  --build-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --sign-key ota_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json \
  --base-url "http://192.168.1.100:8000"

# 3. Start local HTTP server
cd pico-sdk/output/image
python3 -m http.server 8000

# 4. Test on device (use --dry-run to avoid flashing)
ota check-now \
  --manifest-url "http://192.168.1.100:8000/manifest.json" \
  --public-key /userdata/ota/dev_pubkey.pem \
  --dry-run
```

## Key Differences from Official OTA

| Aspect | Official | Custom (Your Repo/Server) |
|--------|----------|---------------------------|
| Signing Key | Official key in `/oem/etc/ota_pubkey.pem` | Your key, specify with `--public-key` |
| Channel | `stable` (default) | Any name, specify with `--channel` |
| Source | `GITHUB_REPOSITORY` in config | Your repo/URL via `--repo` or `--manifest-url` |
| Updates | Automatic via daemon | Manual or configure `/userdata/ota/config.json` |

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
