#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG_PATH="$ROOT_DIR/pico-sdk/output/out/userdata/ota/config.json"
IMAGE_DIR="$ROOT_DIR/pico-sdk/output/image"

if [ ! -s "$CONFIG_PATH" ]; then
  echo "repack_ota_update_image.sh: missing OTA device config: $CONFIG_PATH" >&2
  exit 1
fi

cd "$ROOT_DIR/pico-sdk/project"
./build.sh firmware

for img in userdata.img update.img; do
  if [ ! -s "$IMAGE_DIR/$img" ]; then
    echo "repack_ota_update_image.sh: missing rebuilt image: $IMAGE_DIR/$img" >&2
    exit 1
  fi
done
