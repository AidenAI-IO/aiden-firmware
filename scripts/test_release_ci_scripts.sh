#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKFLOW="$ROOT_DIR/.github/workflows/build.yml"
SCHEDULED_WORKFLOW="$ROOT_DIR/.github/workflows/build-scheduled.yml"
CI_WORKFLOW="$ROOT_DIR/.github/workflows/ci.yml"
BUILD_IMAGE_SCRIPT="$ROOT_DIR/_build_image.sh"
DOCKER_BUILD_SCRIPT="$ROOT_DIR/build_image.sh"
ROOTFS_REPRO_SCRIPT="$ROOT_DIR/scripts/apply_pico_sdk_rootfs_reproducibility_patch.sh"
ROOTFS_REPRO_PATCH="$ROOT_DIR/scripts/patches/pico-sdk-rootfs-reproducible-build.patch"

if ! grep -q 'scripts/create_github_release.sh' "$WORKFLOW"; then
    echo "build workflow must create releases through the retry-capable local script" >&2
    exit 1
fi

if grep -q 'softprops/action-gh-release' "$WORKFLOW"; then
    echo "build workflow must not rely on action-gh-release for release uploads" >&2
    exit 1
fi

if [ ! -x "$ROOT_DIR/scripts/create_github_release.sh" ]; then
    echo "release creation script must exist and be executable" >&2
    exit 1
fi

if ! grep -q -- '--retry-count' "$WORKFLOW" || ! grep -q -- '--retry-delay-seconds' "$WORKFLOW"; then
    echo "build workflow must configure release upload retry count and delay" >&2
    exit 1
fi

if ! grep -q -- '--retry-delay-seconds 30' "$WORKFLOW"; then
    echo "build workflow must use a longer release upload retry base delay" >&2
    exit 1
fi

if ! grep -q -- '--required-assets' "$WORKFLOW"; then
    echo "build workflow must require OTA release assets before publishing" >&2
    exit 1
fi

for asset in boot_a.img boot_b.img oem.img rootfs.img userdata.img update.img manifest.json; do
    if ! grep -q "$asset" "$WORKFLOW"; then
        echo "build workflow must require release asset: $asset" >&2
        exit 1
    fi
done

if ! grep -q 'GH_DEBUG' "$WORKFLOW"; then
    echo "build workflow must enable GitHub CLI debug output for release creation" >&2
    exit 1
fi

if [ ! -x "$ROOTFS_REPRO_SCRIPT" ]; then
    echo "rootfs reproducibility patch script must exist and be executable" >&2
    exit 1
fi

if [ ! -f "$ROOTFS_REPRO_PATCH" ]; then
    echo "rootfs reproducibility patch file must exist" >&2
    exit 1
fi

if ! grep -q 'SOURCE_DATE_EPOCH' "$DOCKER_BUILD_SCRIPT" || \
   ! grep -q 'SOURCE_DATE_EPOCH' "$BUILD_IMAGE_SCRIPT"; then
    echo "image build scripts must set and propagate SOURCE_DATE_EPOCH" >&2
    exit 1
fi

patch_line=$(grep -n 'apply_pico_sdk_rootfs_reproducibility_patch.sh' "$BUILD_IMAGE_SCRIPT" | sed 's/:.*//' | head -n 1)
sysdrv_line=$(grep -n './build.sh sysdrv' "$BUILD_IMAGE_SCRIPT" | sed 's/:.*//' | head -n 1)
if [ -z "$patch_line" ] || [ -z "$sysdrv_line" ] || [ "$patch_line" -ge "$sysdrv_line" ]; then
    echo "_build_image.sh must apply the pico-sdk rootfs reproducibility patch before building sysdrv" >&2
    exit 1
fi

for required in \
    'SOURCE_DATE_EPOCH ?= 0' \
    '--sort=name' \
    '--mtime="@$(SOURCE_DATE_EPOCH)"' \
    'Build Time:  $(reproducible_build_utc)' \
    'find "$dir" -xdev -exec touch -h -d "@$epoch"' \
    'lazy_itable_init=0,lazy_journal_init=0' \
    '^metadata_csum' \
    '-U "${AIDEN_EXT4_UUID:-00000000-0000-4000-8000-000000000000}"' \
    'write_ext4_le32 "$dst" 44 "$source_date_epoch"' \
    'write_ext4_le32 "$dst" 264 "$source_date_epoch"'; do
    if ! grep -Fq -- "$required" "$ROOTFS_REPRO_PATCH"; then
        echo "rootfs reproducibility patch missing required content: $required" >&2
        exit 1
    fi
done

if ! grep -q 'git -C "$PICO_SDK_DIR" apply --unidiff-zero --check "$PATCH_FILE"' "$ROOTFS_REPRO_SCRIPT" || \
   ! grep -q 'apply --unidiff-zero --reverse --check' "$ROOTFS_REPRO_SCRIPT"; then
    echo "rootfs reproducibility patch script must apply the patch idempotently" >&2
    exit 1
fi

if ! grep -q 'cancel-in-progress: false' "$SCHEDULED_WORKFLOW"; then
    echo "scheduled build workflow must not cancel an in-progress release build" >&2
    exit 1
fi

if grep -q 'git submodule update.*pico-sdk' "$CI_WORKFLOW"; then
    echo "CI release script checks must not fetch the large pico-sdk submodule" >&2
    exit 1
fi

if grep -q 'scripts/test_build_scripts.sh' "$CI_WORKFLOW"; then
    echo "CI must not run submodule-dependent build script checks for release script coverage" >&2
    exit 1
fi

if ! grep -q 'scripts/test_release_ci_scripts.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_github_release_upload.sh' "$CI_WORKFLOW"; then
    echo "CI must run repo-only release workflow and upload script tests" >&2
    exit 1
fi

echo "release CI script tests passed"
