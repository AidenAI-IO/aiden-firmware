#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PICO_SDK_DIR="${1:-$ROOT_DIR/pico-sdk}"
PATCH_FILE="$ROOT_DIR/scripts/patches/pico-sdk-rootfs-reproducible-build.patch"

if [ ! -d "$PICO_SDK_DIR" ]; then
  echo "pico-sdk directory not found: $PICO_SDK_DIR" >&2
  exit 1
fi

if [ ! -f "$PATCH_FILE" ]; then
  echo "rootfs reproducibility patch not found: $PATCH_FILE" >&2
  exit 1
fi

if git -C "$PICO_SDK_DIR" apply --unidiff-zero --reverse --check "$PATCH_FILE" >/dev/null 2>&1; then
  echo "pico-sdk rootfs reproducibility patch already applied"
  exit 0
fi

git -C "$PICO_SDK_DIR" apply --unidiff-zero --check "$PATCH_FILE"
git -C "$PICO_SDK_DIR" apply --unidiff-zero "$PATCH_FILE"
echo "pico-sdk rootfs reproducibility patch applied"
