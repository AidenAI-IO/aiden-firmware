# OTA Openness Improvements

This improvement makes the OTA system more open, allowing external developers to distribute firmware from their own repositories or backends.

## Key Improvements

### 1. Manifest Supports Direct URLs

`ManifestAsset` adds an optional `url` field:

```json
{
  "name": "boot_a.img",
  "url": "https://firmware.example.com/v1.0.0/boot_a.img",
  "size": 12345678,
  "sha256": "abc..."
}
```

**Advantages**:
- No longer requires GitHub Release API format
- Can use any web server (Nginx, Apache, S3, CDN, etc.)
- More suitable for intranet deployment and private distribution

### 2. New --manifest-url Parameter

Directly specify manifest URL, skip Release API:

```bash
ota update --manifest-url https://example.com/firmware/manifest.json \
  --public-key /path/to/pubkey.pem
```

### 3. Manifest Generation Script Supports --base-url

```bash
scripts/generate_ota_manifest.sh \
  --version v1.0.0 \
  --channel stable \
  --build-time 2026-06-04T12:00:00Z \
  --sign-key private_key.pem \
  --image-dir output/image \
  --output manifest.json \
  --base-url https://firmware.example.com/v1.0.0  # New option
```

Automatically inject complete download URLs into each asset.

### 4. Official Repository Distinguishes main and Non-main Branches

The official repository's CI/CD now automatically distinguishes release channels based on branches:

| Branch | Channel | GitHub Release | Default Manual OTA Behavior |
|------|---------|---------------|--------------|
| `main` | `stable` | Normal Release | `ota update` discoverable by default |
| Other branches | `dev-{branch-name}` | Prerelease | `ota update` will not discover by default |

**Isolation Mechanism**:

Non-main branch releases are marked as **Prerelease**, while manual `ota update` without specifying `--manifest-url` uses the `releases/latest` API, which only returns official releases, not prereleases. Therefore, default manual updates will not discover non-main branch firmware.

Note: The `channel` field in the manifest serves only as a human-readable label (CI writes `dev-{branch-name}` for non-main branches), and the OTA client only validates its string format without matching it to an expected channel. The actual isolation mechanism is the prerelease mechanism described above.

This way, even if non-main branch firmware is published, it will not affect the default manual OTA update path for production devices.

**Testing Non-main Branch Firmware**:

```bash
# Manually specify dev branch release via manifest-url (marked as Pre-release)
ota update \
  --manifest-url "https://github.com/AidenAI-IO/aiden-hardware-demo/releases/download/TAG/manifest.json" \
  --public-key /oem/etc/ota_pubkey.pem
```

See [ota-release-channels.md](ota-release-channels.md) for details.

## Backward Compatibility

All changes are **optional and incremental**:
- Existing manifests without URLs continue to work normally
- Existing GitHub Release process is unaffected
- Default behavior remains unchanged

## Use Cases

### Use Case 1: Developer Forks Repository

```bash
# Developer builds and publishes firmware in their own GitHub repository
# Generate manifest with GitHub direct URLs
TAG="v1.0.0-custom"
REPO="developer/aiden-custom"
scripts/generate_ota_manifest.sh ... \
  --base-url "https://github.com/$REPO/releases/download/$TAG"

# Device updates from developer's release
ota update \
  --manifest-url "https://github.com/$REPO/releases/download/$TAG/manifest.json" \
  --public-key /path/to/developer_pubkey.pem
```

### Use Case 2: Self-hosted Server Distribution

```bash
# Generate manifest with URLs
scripts/generate_ota_manifest.sh ... \
  --base-url https://firmware.mycompany.com/aiden/v1.0.0

# Upload to your own server
rsync -avz output/image/*.img manifest.json user@server:/var/www/firmware/

# Device updates directly from server
ota update --manifest-url https://firmware.mycompany.com/aiden/v1.0.0/manifest.json \
  --public-key /userdata/ota/company_pubkey.pem
```

### Use Case 3: Local Development Testing

```bash
# Generate local test manifest
scripts/generate_ota_manifest.sh ... \
  --base-url http://192.168.1.100:8000

# Start local server
cd output/image && python3 -m http.server 8000

# Test (without actually flashing)
ota update --manifest-url http://192.168.1.100:8000/manifest.json \
  --public-key /path/to/dev_pubkey.pem --dry-run
```

## File Changes

### Code Changes
- `src/agent/internal/ota/manifest.go` - Add URL field and validation
- `src/agent/internal/ota/updater.go` - Support direct URL and manifest-url parameter
- `src/agent/cmd/ota/main.go` - Add --manifest-url CLI parameter
- `scripts/generate_ota_manifest.sh` - Add --base-url option
- `scripts/create_github_release.sh` - Add --prerelease option
- `.github/workflows/build.yml` - Distinguish channel and release strategy for main and non-main branches

### Tests
- `src/agent/internal/ota/manifest_test.go` - Add URL field tests
- All existing tests pass ✅

### Documentation
- `docs/08-ota/ota-external-developers.md` - Complete developer guide
- `docs/08-ota/ota-quick-examples.md` - Quick usage examples
- `docs/08-ota/ota-release-channels.md` - Release channel and branch distinction explanation

## Security

**Unchanged Security Guarantees**:
1. Signature verification is still mandatory
2. Users must explicitly trust external public keys
3. Version downgrade protection remains effective
4. All existing security checks remain unchanged

**New Flexibility**:
- Allows HTTP (only recommended for test environments, production must use HTTPS)
- URL validation ensures HTTP or HTTPS protocol usage

## Test Verification

```bash
# Compile test
cd src/agent && go build ./cmd/ota

# Run tests
go test ./internal/ota -v

# All tests pass ✅
```

## Next Steps

Suggested enhancements (not implemented, optional):
1. Update source configuration management (support configuring multiple sources and switching)
2. Graphical configuration interface
3. Update source discovery and subscription mechanism

## Reference Documentation

- Detailed guide: [ota-external-developers.md](ota-external-developers.md)
- Quick examples: [ota-quick-examples.md](ota-quick-examples.md)
