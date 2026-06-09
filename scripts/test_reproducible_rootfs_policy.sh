#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_IMAGE_SH="$ROOT_DIR/build_image.sh"
INNER_BUILD_IMAGE_SH="$ROOT_DIR/_build_image.sh"

for script in "$BUILD_IMAGE_SH" "$INNER_BUILD_IMAGE_SH"; do
  if ! grep -q 'AIDEN_REPRODUCIBLE_IMAGE_EPOCH' "$script"; then
    echo "$(basename "$script") must use AIDEN_REPRODUCIBLE_IMAGE_EPOCH for the default reproducible image timestamp" >&2
    exit 1
  fi

  if ! grep -q 'export SOURCE_DATE_EPOCH' "$script"; then
    echo "$(basename "$script") must export SOURCE_DATE_EPOCH for image packaging tools" >&2
    exit 1
  fi
done

if grep -Eq 'git .*log -1 --format=%ct|git .*log -1 .*%ct' "$BUILD_IMAGE_SH" "$INNER_BUILD_IMAGE_SH"; then
  echo "image builds must not derive the default SOURCE_DATE_EPOCH from the current commit time" >&2
  exit 1
fi

echo "reproducible rootfs timestamp policy tests passed"
