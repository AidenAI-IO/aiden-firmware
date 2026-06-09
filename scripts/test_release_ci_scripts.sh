#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKFLOW="$ROOT_DIR/.github/workflows/build.yml"
SCHEDULED_WORKFLOW="$ROOT_DIR/.github/workflows/build-scheduled.yml"
CI_WORKFLOW="$ROOT_DIR/.github/workflows/ci.yml"
BUILD_IMAGE_SCRIPT="$ROOT_DIR/_build_image.sh"
DOCKER_BUILD_SCRIPT="$ROOT_DIR/build_image.sh"

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

release_assets='boot_a.img boot_b.img oem.img rootfs.img update.img manifest.json'
if ! grep -q -- "--required-assets '$release_assets'" "$WORKFLOW"; then
    echo "build workflow must require only allowlisted release assets" >&2
    exit 1
fi

if ! grep -q -- '--upload-assets' "$WORKFLOW"; then
    echo "build workflow must pass an explicit release upload asset allowlist" >&2
    exit 1
fi

if ! grep -q -- "--upload-assets '$release_assets'" "$WORKFLOW"; then
    echo "build workflow must upload only allowlisted release assets" >&2
    exit 1
fi

if grep -q 'userdata.img' "$WORKFLOW"; then
    echo "build workflow must not upload userdata.img to GitHub releases" >&2
    exit 1
fi

if ! grep -q 'GH_DEBUG' "$WORKFLOW"; then
    echo "build workflow must enable GitHub CLI debug output for release creation" >&2
    exit 1
fi

if ! grep -q 'SOURCE_DATE_EPOCH' "$DOCKER_BUILD_SCRIPT" || \
   ! grep -q 'SOURCE_DATE_EPOCH' "$BUILD_IMAGE_SCRIPT"; then
    echo "image build scripts must set and propagate SOURCE_DATE_EPOCH" >&2
    exit 1
fi

if grep -q 'apply_pico_sdk_rootfs_reproducibility_patch.sh' "$BUILD_IMAGE_SCRIPT" || \
   [ -e "$ROOT_DIR/scripts/apply_pico_sdk_rootfs_reproducibility_patch.sh" ] || \
   [ -e "$ROOT_DIR/scripts/patches/pico-sdk-rootfs-reproducible-build.patch" ]; then
    echo "rootfs reproducibility support must live in the pico-sdk submodule, not a build-time patch" >&2
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
