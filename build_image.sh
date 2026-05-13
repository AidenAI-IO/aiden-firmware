#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OVERLAY="$SCRIPT_DIR/overlay"
DEST="$SCRIPT_DIR/pico-sdk/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-buildroot-aiden"

echo "Running build.sh"
cd "$SCRIPT_DIR"
./build.sh

echo "Copying binaries to overlay/oem/usr/bin"
mkdir -p "$OVERLAY/oem/usr/bin"
cp -a "$SCRIPT_DIR/build/bin"/. "$OVERLAY/oem/usr/bin/"

echo "Copying overlay to $DEST"
if [ ! -d "$DEST" ]; then
    echo "Error: destination directory not found at $DEST" >&2
    exit 1
fi
cp -a "$OVERLAY"/. "$DEST/"

echo "Running pico-sdk/build_all.sh"
cd "$SCRIPT_DIR/pico-sdk"
./build_all.sh "$@"
