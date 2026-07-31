#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG_PATH="$ROOT_DIR/pico-sdk/output/out/ota/config.json"
IMAGE_DIR="$ROOT_DIR/pico-sdk/output/image"
OTA_DIR="$ROOT_DIR/pico-sdk/output/out/ota"
MKFS_EXT4="$ROOT_DIR/pico-sdk/sysdrv/tools/pc/e2fsprogs/mkfs_ext4.sh"

source "$ROOT_DIR/scripts/ota_partition_layout.sh"

if [ ! -s "$CONFIG_PATH" ]; then
  echo "repack_ota_update_image.sh: missing OTA device config: $CONFIG_PATH" >&2
  exit 1
fi

if [ ! -d "$OTA_DIR" ]; then
  echo "repack_ota_update_image.sh: missing OTA staging directory: $OTA_DIR" >&2
  exit 1
fi

if [ ! -x "$MKFS_EXT4" ]; then
  echo "repack_ota_update_image.sh: missing mkfs_ext4 tool: $MKFS_EXT4" >&2
  exit 1
fi

ota_size="$(aiden_ota_partition_size_bytes)" || {
  echo "repack_ota_update_image.sh: cannot determine OTA partition size" >&2
  exit 1
}

chown -hR 0:0 "$OTA_DIR"
"$MKFS_EXT4" "$OTA_DIR" "$IMAGE_DIR/ota.img" "$ota_size"

cd "$ROOT_DIR/pico-sdk/project"
./build.sh updateimg

for img in ota.img update.img; do
  if [ ! -s "$IMAGE_DIR/$img" ]; then
    echo "repack_ota_update_image.sh: missing rebuilt image: $IMAGE_DIR/$img" >&2
    exit 1
  fi
done
