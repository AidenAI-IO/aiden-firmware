#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG_PATH="$ROOT_DIR/pico-sdk/output/out/userdata/ota/config.json"
IMAGE_DIR="$ROOT_DIR/pico-sdk/output/image"
USERDATA_DIR="$ROOT_DIR/pico-sdk/output/out/userdata"
BOARD_CONFIG="$ROOT_DIR/pico-sdk/.BoardConfig.mk"
MKFS_EXT4="$ROOT_DIR/pico-sdk/sysdrv/tools/pc/e2fsprogs/mkfs_ext4.sh"

if [ ! -s "$CONFIG_PATH" ]; then
  echo "repack_ota_update_image.sh: missing OTA device config: $CONFIG_PATH" >&2
  exit 1
fi

if [ ! -d "$USERDATA_DIR" ]; then
  echo "repack_ota_update_image.sh: missing userdata staging directory: $USERDATA_DIR" >&2
  exit 1
fi

if [ ! -f "$BOARD_CONFIG" ]; then
  echo "repack_ota_update_image.sh: missing board config: $BOARD_CONFIG" >&2
  exit 1
fi

if [ ! -x "$MKFS_EXT4" ]; then
  echo "repack_ota_update_image.sh: missing mkfs_ext4 tool: $MKFS_EXT4" >&2
  exit 1
fi

partition_size_bytes() {
  local name="$1"
  local entry size suffix number
  IFS=',' read -ra entries <<< "$RK_PARTITION_CMD_IN_ENV"
  for entry in "${entries[@]}"; do
    case "$entry" in
      *"($name)")
        size="${entry%%(*}"
        size="${size%%@*}"
        suffix="${size: -1}"
        number="${size%?}"
        case "$suffix" in
          K|k) echo $((number * 1024)); return 0 ;;
          M|m) echo $((number * 1024 * 1024)); return 0 ;;
          G|g) echo $((number * 1024 * 1024 * 1024)); return 0 ;;
          *) echo "$size"; return 0 ;;
        esac
        ;;
    esac
  done
  return 1
}

source "$BOARD_CONFIG"
userdata_size="$(partition_size_bytes userdata)" || {
  echo "repack_ota_update_image.sh: userdata partition not found in RK_PARTITION_CMD_IN_ENV" >&2
  exit 1
}

"$MKFS_EXT4" "$USERDATA_DIR" "$IMAGE_DIR/userdata.img" "$userdata_size"

cd "$ROOT_DIR/pico-sdk/project"
./build.sh updateimg

for img in userdata.img update.img; do
  if [ ! -s "$IMAGE_DIR/$img" ]; then
    echo "repack_ota_update_image.sh: missing rebuilt image: $IMAGE_DIR/$img" >&2
    exit 1
  fi
done
