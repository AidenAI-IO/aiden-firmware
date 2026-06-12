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

if ! grep -q -- '--retry-count 10' "$WORKFLOW"; then
    echo "build workflow must use doubled release upload retry attempts" >&2
    exit 1
fi

if ! grep -q '^retry_count=10$' "$ROOT_DIR/scripts/create_github_release.sh"; then
    echo "release script default retry count must stay doubled" >&2
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
    echo "build workflow must pass the full release asset allowlist to the rootfs reuse resolver" >&2
    exit 1
fi

if ! grep -q -- '--upload-assets "$upload_assets"' "$WORKFLOW"; then
    echo "build workflow must upload the resolver-adjusted release asset allowlist" >&2
    exit 1
fi

if ! grep -q -- '--channel "${{ steps.release_info.outputs.channel }}"' "$WORKFLOW"; then
    echo "build workflow must pass the current release channel to the rootfs reuse resolver" >&2
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

if grep -Fq 'rm -rf pico-sdk/output/out' "$WORKFLOW"; then
    echo "build workflow must preserve pico-sdk/output/out so unchanged SDK code can reuse SDK-managed build outputs" >&2
    exit 1
fi

if ! grep -Eq 'pico-sdk/output/image/\*\.img([[:space:]\\]|$)' "$WORKFLOW"; then
    echo "build workflow must clean all generated images before a release build" >&2
    exit 1
fi

if ! grep -Fq 'chmod -R u+w "$GITHUB_WORKSPACE/build/.cache/go-mod"' "$WORKFLOW"; then
    echo "self-hosted workspace reclaim must unlock stale read-only Go module cache directories before checkout" >&2
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

if grep -q 'scripts/test_reproducible_rootfs_policy.sh' "$CI_WORKFLOW"; then
    echo "CI release script checks must not run submodule-dependent reproducible rootfs policy checks" >&2
    exit 1
fi

if ! grep -q 'scripts/test_release_ci_scripts.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_clean_rootfs_overlay_staging.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_github_release_upload.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_compress_release_images.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_ota_manifest_generation.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_reusable_rootfs_release_asset.sh' "$CI_WORKFLOW"; then
    echo "CI must run repo-only release workflow and upload script tests" >&2
    exit 1
fi

if ! grep -q 'Resolve reusable rootfs release asset' "$WORKFLOW"; then
    echo "build workflow must resolve reusable rootfs release assets before manifest generation" >&2
    exit 1
fi

if ! grep -q 'rootfs_asset.outputs.upload_assets' "$WORKFLOW"; then
    echo "build workflow must pass the resolved release upload asset list to the release script" >&2
    exit 1
fi

if ! grep -q 'rootfs_asset.outputs.rootfs_asset_metadata' "$WORKFLOW" || \
   ! grep -q -- '--asset-metadata' "$WORKFLOW"; then
    echo "build workflow must pass full reused rootfs asset metadata into manifest generation" >&2
    exit 1
fi

if ! grep -q 'Compress OTA manifest images' "$WORKFLOW" || \
   ! grep -q 'Compress release upload images' "$WORKFLOW"; then
    echo "build workflow must compress OTA image assets before publishing releases" >&2
    exit 1
fi

oem_bin_sync_line=$(grep -nF 'rsync -a --delete "$OVERLAY/oem/usr/bin/" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin/"' "$ROOT_DIR/_build_image.sh" | sed 's/:.*//' | head -n 1)
oem_full_sync_line=$(grep -nF 'rsync -a "$OVERLAY/oem/" "$RK_PROJECT_PACKAGE_OEM_DIR/"' "$ROOT_DIR/_build_image.sh" | sed 's/:.*//' | head -n 1)
if [ -z "$oem_bin_sync_line" ] || [ -z "$oem_full_sync_line" ] || [ "$oem_bin_sync_line" -ge "$oem_full_sync_line" ]; then
    echo "_build_image.sh must sync OEM usr/bin with delete semantics before full OEM overlay sync" >&2
    exit 1
fi

if ! grep -q 'release_upload_assets.outputs.upload_assets' "$WORKFLOW"; then
    echo "build workflow must upload compressed release image assets" >&2
    exit 1
fi

if ! grep -q 'scripts/compress_release_images.sh' "$WORKFLOW"; then
    echo "build workflow must use the shared release image compression script" >&2
    exit 1
fi

echo "release CI script tests passed"
