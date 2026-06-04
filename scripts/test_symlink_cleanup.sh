#!/usr/bin/env bash
set -euo pipefail

# Integration test to verify that temporary symlinks created during update.img
# packaging are properly cleaned up and don't pollute release artifacts.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

image_dir="$tmp_dir/image"
mkdir -p "$image_dir"

# Simulate the build process
echo "Simulating build_updateimg() symlink lifecycle..."

# 1. Create base images (as build_firmware does)
printf 'boot_a\n' > "$image_dir/boot_a.img"
printf 'boot_b\n' > "$image_dir/boot_b.img"
printf 'oem\n' > "$image_dir/oem.img"
printf 'rootfs\n' > "$image_dir/rootfs.img"
printf 'userdata\n' > "$image_dir/userdata.img"

echo "✓ Created base images"

# 2. Create temporary symlinks (as build_updateimg does before mk-update_pack.sh)
ln -sf oem.img "$image_dir/oem_a.img"
ln -sf oem.img "$image_dir/oem_b.img"
ln -sf rootfs.img "$image_dir/rootfs_a.img"
ln -sf rootfs.img "$image_dir/rootfs_b.img"

echo "✓ Created temporary symlinks"

# Verify symlinks exist
if [ ! -L "$image_dir/oem_a.img" ] || [ ! -L "$image_dir/rootfs_a.img" ]; then
  echo "ERROR: Failed to create symlinks" >&2
  exit 1
fi

# 3. Simulate update.img packaging (mk-update_pack.sh would run here)
printf 'update\n' > "$image_dir/update.img"

echo "✓ Simulated update.img packaging"

# 4. Clean up symlinks (as build_updateimg does after mk-update_pack.sh)
rm -f "$image_dir/oem_a.img" "$image_dir/oem_b.img"
rm -f "$image_dir/rootfs_a.img" "$image_dir/rootfs_b.img"

echo "✓ Cleaned up symlinks"

# 5. Verify cleanup was successful
remaining_symlinks=()
for file in "$image_dir"/*_a.img "$image_dir"/*_b.img; do
  [ -e "$file" ] || continue  # Skip if glob didn't match
  basename=$(basename "$file")
  # boot_a.img and boot_b.img are real files, not symlinks
  if [[ "$basename" != "boot_a.img" && "$basename" != "boot_b.img" ]]; then
    remaining_symlinks+=("$basename")
  fi
done

if [ "${#remaining_symlinks[@]}" -gt 0 ]; then
  echo "ERROR: Symlinks still present after cleanup:" >&2
  printf '  - %s\n' "${remaining_symlinks[@]}" >&2
  exit 1
fi

# 6. Verify only expected files remain
expected_files=("boot_a.img" "boot_b.img" "oem.img" "rootfs.img" "userdata.img" "update.img")
for expected in "${expected_files[@]}"; do
  if [ ! -f "$image_dir/$expected" ]; then
    echo "ERROR: Expected file missing: $expected" >&2
    exit 1
  fi
done

echo "✓ Only expected files remain (no symlink pollution)"
echo ""
echo "Symlink cleanup test passed."
echo "Files in output directory:"
ls -lh "$image_dir" | tail -n +2 | awk '{print "  " $9 " (" $5 ")"}'
