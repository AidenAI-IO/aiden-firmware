#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKFLOW="$ROOT_DIR/.github/workflows/build.yml"
SCHEDULED_WORKFLOW="$ROOT_DIR/.github/workflows/build-scheduled.yml"
BACKUP_WORKFLOW="$ROOT_DIR/.github/workflows/build-backup.yml"
FALLBACK_WORKFLOW="$ROOT_DIR/.github/workflows/build-fallback.yml"
CI_WORKFLOW="$ROOT_DIR/.github/workflows/ci.yml"

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

# The Debian firmware ships as compressed partition images plus the signed
# manifest. Define the allowlist once and reuse it for both the required-assets
# gate and the upload set, and add the update.img checksum only to the upload.
release_assets='boot_a.img.tar.gz boot_b.img.tar.gz oem.img.tar.gz rootfs.img.tar.gz update.img.tar.gz manifest.json'
if ! grep -Fq "release_assets='${release_assets}'" "$WORKFLOW"; then
    echo "build workflow must define the Debian release asset allowlist" >&2
    exit 1
fi

if ! grep -Fq -- '--required-assets "$release_assets"' "$WORKFLOW"; then
    echo "build workflow must require the Debian OTA release assets before publishing" >&2
    exit 1
fi

if ! grep -Fq -- '--upload-assets "$release_assets update.img.sha256"' "$WORKFLOW"; then
    echo "build workflow must upload the Debian release asset allowlist plus the checksum" >&2
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

# A rotated OTA secret must not ship images trusting the previous committed
# signer, so CI pins the trust anchor to the key it signs with.
if ! grep -Fq 'OTA_TRUST_PUBLIC_KEY_PATH=$public_key' "$WORKFLOW"; then
    echo "build workflow must pin the OTA trust anchor to the build signing key" >&2
    exit 1
fi

# The production image is built by the Debian entrypoint, not the retired
# Buildroot CLI.
if ! grep -Fq 'run: ./debian_build.sh' "$WORKFLOW" || \
   grep -Fq 'run: ./build.sh image' "$WORKFLOW" || \
   grep -Fq 'build_image.sh' "$WORKFLOW"; then
    echo "build workflow must build the Debian firmware through debian_build.sh" >&2
    exit 1
fi

# Stage 2 fills .cache with read-only Go module directories that block the next
# actions/checkout clean phase on the shared self-hosted runners.
if ! grep -Fq '"$GITHUB_WORKSPACE/.cache"' "$WORKFLOW" || \
   ! grep -Fq 'chmod -R u+w "$go_mod_cache"' "$WORKFLOW"; then
    echo "self-hosted workspace reclaim must unlock the Debian Go module cache before checkout" >&2
    exit 1
fi

if ! grep -q 'cancel-in-progress: false' "$SCHEDULED_WORKFLOW"; then
    echo "scheduled build workflow must not cancel an in-progress release build" >&2
    exit 1
fi

if ! grep -q 'runs-on: aiden-hosted-01' "$SCHEDULED_WORKFLOW" || \
   ! grep -q 'runner: aiden-hosted-01' "$SCHEDULED_WORKFLOW"; then
    echo "scheduled build workflow must use the primary dedicated Aiden hosted runner label" >&2
    exit 1
fi

if ! grep -q 'runner: aiden-hosted-02' "$BACKUP_WORKFLOW"; then
    echo "backup build workflow must use the backup dedicated Aiden hosted runner label" >&2
    exit 1
fi

if ! grep -q 'runner: ubuntu-latest' "$FALLBACK_WORKFLOW" || \
   ! grep -q 'free_disk_space: true' "$FALLBACK_WORKFLOW"; then
    echo "fallback build workflow must build on a hosted runner with disk space reclaimed" >&2
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

# The policy check reads pico-sdk, so it must not share the release-script job,
# which has no SDK checkout, and any job running it must sparse-fetch rather
# than check out the multi-GB worktree. Those are per-job structural facts, so
# they are checked against the parsed workflow.
python3 "$ROOT_DIR/scripts/check_ci_policy_job.py"

if ! grep -q 'scripts/test_release_ci_scripts.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_reproducible_rootfs_policy.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_build_cli.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_rootfs_cli_tool_catalog.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_clean_rootfs_overlay_staging.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_build_rootfs_cli_tools.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_stage_rootfs_cli_tools.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_github_release_upload.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_compress_release_images.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_ota_partition_layout.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_ota_device_config.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_ota_init.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_ota_manifest_generation.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_reusable_rootfs_release_asset.sh' "$CI_WORKFLOW"; then
    echo "CI must run repo-only release workflow and upload script tests" >&2
    exit 1
fi

echo "release CI script tests passed"
