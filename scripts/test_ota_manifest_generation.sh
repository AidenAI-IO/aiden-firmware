#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

image_dir="$tmp_dir/images"
mkdir -p "$image_dir"

# Create test images
printf 'boot_a content\n' > "$image_dir/boot_a.img"
printf 'boot_b content\n' > "$image_dir/boot_b.img"
printf 'oem content\n' > "$image_dir/oem.img"
printf 'rootfs content\n' > "$image_dir/rootfs.img"

# Note: symlinks should NOT exist at this point
# (build.sh cleans them up after update.img packaging)

# Generate a test signing key
private_key="$tmp_dir/test_key.pem"
openssl genpkey -algorithm ED25519 -out "$private_key" 2>/dev/null

# Generate manifest
manifest_output="$tmp_dir/manifest.json"
"$repo_root/scripts/generate_ota_manifest.sh" \
  --version "test-version" \
  --channel "test" \
  --build-time "2026-01-01T00:00:00Z" \
  --sign-key "$private_key" \
  --image-dir "$image_dir" \
  --output "$manifest_output"

if [ ! -f "$manifest_output" ]; then
  echo "manifest generation failed: output file not created" >&2
  exit 1
fi

# Verify manifest uses neutral resources for oem and rootfs
if ! jq -e '.parts[] | select(.name=="oem") | .asset' "$manifest_output" >/dev/null; then
  echo "manifest must use neutral asset for oem (not asset_a/asset_b)" >&2
  echo "Generated manifest:" >&2
  jq . "$manifest_output" >&2
  exit 1
fi

if ! jq -e '.parts[] | select(.name=="rootfs") | .asset' "$manifest_output" >/dev/null; then
  echo "manifest must use neutral asset for rootfs (not asset_a/asset_b)" >&2
  echo "Generated manifest:" >&2
  jq . "$manifest_output" >&2
  exit 1
fi

# Verify oem neutral asset references the correct file
oem_asset_name="$(jq -r '.parts[] | select(.name=="oem") | .asset.name' "$manifest_output")"
if [ "$oem_asset_name" != "oem.img" ]; then
  echo "oem neutral asset must reference oem.img, got: $oem_asset_name" >&2
  exit 1
fi

# Verify rootfs neutral asset references the correct file
rootfs_asset_name="$(jq -r '.parts[] | select(.name=="rootfs") | .asset.name' "$manifest_output")"
if [ "$rootfs_asset_name" != "rootfs.img" ]; then
  echo "rootfs neutral asset must reference rootfs.img, got: $rootfs_asset_name" >&2
  exit 1
fi

# Verify boot still uses slot-specific assets
if ! jq -e '.parts[] | select(.name=="boot") | .asset_a' "$manifest_output" >/dev/null; then
  echo "manifest must use slot-specific assets for boot" >&2
  exit 1
fi

if ! jq -e '.parts[] | select(.name=="boot") | .asset_b' "$manifest_output" >/dev/null; then
  echo "manifest must use slot-specific assets for boot" >&2
  exit 1
fi

# Verify signature is present
if ! jq -e '.signature.value' "$manifest_output" >/dev/null; then
  echo "manifest must be signed" >&2
  exit 1
fi

echo "OTA manifest generation test passed (neutral resources correctly used)."
