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

if grep -Eq 'AIDEN_REPRODUCIBLE_IMAGE_EPOCH:-0' "$BUILD_IMAGE_SH" "$INNER_BUILD_IMAGE_SH"; then
  echo "image builds must use a non-zero default SOURCE_DATE_EPOCH so falsey-zero package bugs cannot affect releases" >&2
  exit 1
fi

if ! grep -Eq 'AIDEN_REPRODUCIBLE_IMAGE_EPOCH:-[1-9][0-9]*' "$BUILD_IMAGE_SH" || \
   ! grep -Eq 'AIDEN_REPRODUCIBLE_IMAGE_EPOCH:-[1-9][0-9]*' "$INNER_BUILD_IMAGE_SH"; then
  echo "image builds must keep a deterministic non-zero default SOURCE_DATE_EPOCH" >&2
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

  if ! grep -q '^BR2_TAR_OPTIONS="--no-same-owner"$' "$defconfig"; then
    echo "$(basename "$defconfig") must set BR2_TAR_OPTIONS=--no-same-owner so Dockerized Buildroot extracts archives without restoring unmappable owners" >&2
    exit 1
  fi

  if ! grep -q '^BR2_PACKAGE_ANDROID_TOOLS_AIDEN=y$' "$defconfig"; then
    echo "$(basename "$defconfig") must include the android-tools-aiden adb client (1.0.41) so the board can act as an ADB host" >&2
    exit 1
  fi

  if grep -q '^BR2_PACKAGE_ANDROID_TOOLS_ADBD=y$' "$defconfig"; then
    echo "$(basename "$defconfig") must not enable adbd; Aiden expects the board to run the adb client instead" >&2
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

refresh_buildroot_config_state="$(sed -n '/^define refresh_buildroot_config_state$/,/^endef$/p' "$PICO_SDK/sysdrv/Makefile")"
if ! printf '%s\n' "$refresh_buildroot_config_state" | grep -q 'SOURCE_DATE_EPOCH'; then
  echo "pico-sdk Buildroot state must include SOURCE_DATE_EPOCH so stale package outputs are rebuilt when the reproducible epoch changes" >&2
  exit 1
fi

if ! printf '%s\n' "$refresh_buildroot_config_state" | grep -q 'AIDEN_BUILDROOT_REPRODUCIBLE_STATE_VERSION'; then
  echo "pico-sdk Buildroot state must include an Aiden reproducibility state version to invalidate older runner caches" >&2
  exit 1
fi

if ! printf '%s\n' "$refresh_buildroot_config_state" | grep -Fq 'sha256sum "$(SYSDRV_DIR)/Makefile"'; then
  echo "pico-sdk Buildroot state must include sysdrv/Makefile so reproducibility policy changes invalidate stale package outputs" >&2
  exit 1
fi

if ! grep -q 'define refresh_boardtools_config_state' "$PICO_SDK/sysdrv/Makefile" || \
   ! grep -q '.aiden_boardtools_config_state' "$PICO_SDK/sysdrv/Makefile" || \
   ! grep -q 'Board tools reproducibility inputs changed; rebuilding generated board tool state' "$PICO_SDK/sysdrv/Makefile"; then
  echo "pico-sdk sysdrv Makefile must invalidate generated board tool state when reproducible inputs change" >&2
  exit 1
fi

refresh_boardtools_config_state="$(sed -n '/^define refresh_boardtools_config_state$/,/^endef$/p' "$PICO_SDK/sysdrv/Makefile")"
if ! printf '%s\n' "$refresh_boardtools_config_state" | grep -q 'SOURCE_DATE_EPOCH'; then
  echo "pico-sdk board tool state must include SOURCE_DATE_EPOCH so stale board tool outputs are rebuilt when the reproducible epoch changes" >&2
  exit 1
fi

if ! printf '%s\n' "$refresh_boardtools_config_state" | grep -q 'AIDEN_BOARDTOOLS_REPRODUCIBLE_STATE_VERSION'; then
  echo "pico-sdk board tool state must include an Aiden reproducibility state version to invalidate older runner caches" >&2
  exit 1
fi

if ! printf '%s\n' "$refresh_boardtools_config_state" | grep -q 'tools_board-clean'; then
  echo "pico-sdk board tool state must clean generated board tool outputs when reproducibility inputs change" >&2
  exit 1
fi

if ! printf '%s\n' "$refresh_boardtools_config_state" | grep -q 'tools/board/toolkits/openssl'; then
  echo "pico-sdk board tool state must include board OpenSSL inputs because adb-related tooling links against that cached output" >&2
  exit 1
fi

if ! grep -q '^boardtools: refresh_boardtools_config_state$' "$PICO_SDK/sysdrv/Makefile" || \
   ! sed -n '/^boardtools:/,/^$/p' "$PICO_SDK/sysdrv/Makefile" | grep -q 'tools_board-builds'; then
  echo "pico-sdk boardtools target must refresh board tool state before reusing cached outputs" >&2
  exit 1
fi

echo "reproducible rootfs timestamp policy tests passed"
