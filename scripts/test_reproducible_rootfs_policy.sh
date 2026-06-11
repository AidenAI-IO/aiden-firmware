#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_IMAGE_SH="$ROOT_DIR/build_image.sh"
INNER_BUILD_IMAGE_SH="$ROOT_DIR/_build_image.sh"
PICO_SDK="$ROOT_DIR/pico-sdk"

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

for defconfig in \
  "$PICO_SDK/sysdrv/tools/board/buildroot/luckfox_pico_defconfig" \
  "$PICO_SDK/sysdrv/tools/board/buildroot/luckfox_pico_w_defconfig"; do
  if [ ! -f "$defconfig" ]; then
    echo "missing Buildroot defconfig: $defconfig" >&2
    exit 1
  fi

  if ! grep -q '^BR2_REPRODUCIBLE=y$' "$defconfig"; then
    echo "$(basename "$defconfig") must enable BR2_REPRODUCIBLE so package builds use Buildroot's reproducible timestamp policy" >&2
    exit 1
  fi
done

if ! grep -q 'define sync_buildroot_board_config' "$PICO_SDK/sysdrv/Makefile" || \
   ! grep -q '$(call sync_buildroot_board_config)' "$PICO_SDK/sysdrv/Makefile"; then
  echo "pico-sdk sysdrv Makefile must sync Buildroot board config before each Buildroot build" >&2
  exit 1
fi

if ! grep -q 'define refresh_buildroot_config_state' "$PICO_SDK/sysdrv/Makefile" || \
   ! grep -q '.aiden_buildroot_config_state' "$PICO_SDK/sysdrv/Makefile" || \
   ! grep -q 'Buildroot configuration changed; rebuilding generated Buildroot state' "$PICO_SDK/sysdrv/Makefile"; then
  echo "pico-sdk sysdrv Makefile must invalidate generated Buildroot state when reproducible config inputs change" >&2
  exit 1
fi

echo "reproducible rootfs timestamp policy tests passed"
