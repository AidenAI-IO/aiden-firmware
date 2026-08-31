#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKFLOW="$ROOT_DIR/.github/workflows/build.yml"
SCHEDULED_WORKFLOW="$ROOT_DIR/.github/workflows/build-scheduled.yml"
CI_WORKFLOW="$ROOT_DIR/.github/workflows/ci.yml"
IMAGE_TASK="$ROOT_DIR/scripts/build/container/image.sh"
BINARIES_TASK="$ROOT_DIR/scripts/build/container/binaries.sh"
CONTAINER_RUNNER="$ROOT_DIR/scripts/build/run_container.sh"
GENERATED_BINARIES_LIB="$ROOT_DIR/scripts/build/container/lib/generated_binaries.sh"
EXT4_IMAGES_LIB="$ROOT_DIR/scripts/build/container/lib/ext4_images.sh"
CONFIG_WEB_INIT_SCRIPT="$ROOT_DIR/overlay/etc/init.d/S56config_web"

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

if ! grep -q 'SOURCE_DATE_EPOCH' "$CONTAINER_RUNNER" || \
   ! grep -q 'SOURCE_DATE_EPOCH' "$IMAGE_TASK"; then
    echo "image build scripts must set and propagate SOURCE_DATE_EPOCH" >&2
    exit 1
fi

# Each helper library must stand on its own: sourceable in isolation and the
# sole definer of the entry point the orchestrator calls. Asserting on the
# resulting function ownership rather than on source lines keeps this checking
# the split itself instead of the spelling of any one statement.
for library_contract in \
    "$GENERATED_BINARIES_LIB:repair_generated_binaries_from_manifest" \
    "$EXT4_IMAGES_LIB:rebuild_ext4_image"; do
    library_path="${library_contract%:*}"
    library_function="${library_contract##*:}"
    if ! bash -c 'source "$1" >/dev/null 2>&1 || exit 1; declare -F "$2" >/dev/null' \
        _ "$library_path" "$library_function"; then
        echo "$(basename "$library_path") must be sourceable on its own and define $library_function()" >&2
        exit 1
    fi
    if grep -Eq "^[[:space:]]*(function[[:space:]]+)?${library_function}[[:space:]]*\(\)" "$IMAGE_TASK"; then
        echo "image orchestrator must delegate $library_function() to $(basename "$library_path") instead of redefining it" >&2
        exit 1
    fi
done

# The orchestrator must keep delegating to both libraries and to the binaries
# task. Matching filenames only leaves the wiring style free to change.
for required_dependency in \
    lib/generated_binaries.sh \
    lib/ext4_images.sh \
    binaries.sh; do
    if ! grep -Fq "$required_dependency" "$IMAGE_TASK"; then
        echo "image orchestrator must delegate to $required_dependency" >&2
        exit 1
    fi
done

# Generated binaries are anchored to the repository's build/bin. Evaluate the
# real assignment with a hostile environment so an override knob reintroduced
# under any name fails here rather than silently relocating release binaries.
build_bin_dir_assignment=$(grep -E '^[[:space:]]*BUILD_BIN_DIR=' "$IMAGE_TASK") || {
    echo "image orchestrator must define BUILD_BIN_DIR for generated binaries" >&2
    exit 1
}
resolved_build_bin_dir=$(
    REPO_ROOT=/fixture-repo \
    BUILD_BIN_DIR=/hijacked-stale \
    AIDEN_BUILD_BIN_DIR=/hijacked-knob \
    sh -c 'eval "$1"; printf %s "$BUILD_BIN_DIR"' _ "$build_bin_dir_assignment"
)
if [ "$resolved_build_bin_dir" != /fixture-repo/build/bin ]; then
    echo "generated binaries must resolve to the fixed repository build/bin directory, got $resolved_build_bin_dir" >&2
    exit 1
fi

# Release binaries carry the build commit in their ldflags. Evaluate the real
# assignment against a non-repository directory to prove it degrades to
# "unknown" instead of aborting the build or embedding an empty commit.
agent_commit_assignment=$(grep -E '^[[:space:]]*AGENT_COMMIT=' "$BINARIES_TASK") || {
    echo "binaries task must define AGENT_COMMIT for build metadata" >&2
    exit 1
}
non_repo_dir=$(mktemp -d "${TMPDIR:-/tmp}/aiden-release-ci-test.XXXXXX")
resolved_agent_commit=$(
    REPO_ROOT="$non_repo_dir" \
    GIT_CEILING_DIRECTORIES="$non_repo_dir" \
    sh -c 'eval "$1"; printf %s "$AGENT_COMMIT"' _ "$agent_commit_assignment"
) || resolved_agent_commit='<failed>'
rm -rf "$non_repo_dir"
if [ "$resolved_agent_commit" != unknown ]; then
    echo "binary builds must preserve unknown commit metadata when .git is unavailable, got $resolved_agent_commit" >&2
    exit 1
fi

# The retained Buildroot binaries entrypoint still uses the vendor uClibc
# toolchain. It must not inherit the repository's Debian/glibc platform default.
if ! grep -Fq -- '-DAIDEN_TARGET_PLATFORM=rv1106-buildroot-uclibc' "$BINARIES_TASK"; then
    echo "legacy uClibc binary builds must select the Buildroot platform explicitly" >&2
    exit 1
fi

if ! grep -Fq 'run: ./build.sh image' "$WORKFLOW" || \
   ! grep -Fq 'run: ./build.sh exec image -- bash ./scripts/repack_ota_update_image.sh' "$WORKFLOW" || \
   grep -Fq 'build_image.sh' "$WORKFLOW"; then
    echo "build workflow must use the public build CLI for image and container exec tasks" >&2
    exit 1
fi

if ! grep -q "go-version: '1.26.0'" "$WORKFLOW"; then
    echo "image release builds must pin Go 1.26.0 for reproducible rootfs CLI binaries" >&2
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

if ! grep -Fq '"$GITHUB_WORKSPACE/.cache/rootfs-cli-tools/go-mod"' "$WORKFLOW" || \
   ! grep -Fq 'chmod -R u+w "$go_mod_cache"' "$WORKFLOW"; then
    echo "self-hosted workspace reclaim must unlock the rootfs CLI Go module cache before checkout" >&2
    exit 1
fi

if ! grep -Fq 'for path in build .cache/rootfs-cli-tools overlay/oem overlay/userdata pico-sdk/output' "$CONTAINER_RUNNER" || \
   ! grep -Fq 'chmod -R u+w "$REPO_ROOT/.cache/rootfs-cli-tools/go-mod"' "$CONTAINER_RUNNER"; then
    echo "Docker image builds must restore ownership and write permission for the rootfs CLI cache" >&2
    exit 1
fi

if grep -q 'apply_pico_sdk_rootfs_reproducibility_patch.sh' "$IMAGE_TASK" || \
   [ -e "$ROOT_DIR/scripts/apply_pico_sdk_rootfs_reproducibility_patch.sh" ] || \
   [ -e "$ROOT_DIR/scripts/patches/pico-sdk-rootfs-reproducible-build.patch" ]; then
    echo "rootfs reproducibility support must live in the pico-sdk submodule, not a build-time patch" >&2
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

if ! grep -q 'runner: aiden-hosted-02' "$ROOT_DIR/.github/workflows/build-backup.yml"; then
    echo "backup build workflow must use the backup dedicated Aiden hosted runner label" >&2
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
# they are checked against the parsed workflow: grepping the whole file accepts
# a marker that appears only in a comment, and cannot attribute a line to a job.
python3 "$ROOT_DIR/scripts/check_ci_policy_job.py"

if ! grep -q 'scripts/test_release_ci_scripts.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_debian_stage2.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_debian_stage3.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_debian_init_script_map.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_debian_systemd_overlay.sh' "$CI_WORKFLOW" || \
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

oem_full_sync_line=$(grep -nF 'rsync -a "$OVERLAY/oem/" "$RK_PROJECT_PACKAGE_OEM_DIR/"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
oem_repair_line=$(grep -nF 'repair_generated_binaries_from_manifest "sdk-oem-usr-bin" "$BUILD_BIN_DIR" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin" "$GENERATED_BINARY_MANIFEST"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
if [ -z "$oem_full_sync_line" ] || [ -z "$oem_repair_line" ] || [ "$oem_full_sync_line" -ge "$oem_repair_line" ]; then
    echo "image task must sync OEM overlay first, then restore generated usr/bin files from the build manifest source" >&2
    exit 1
fi
if grep -Fq 'rsync -a --delete "$OVERLAY/oem/usr/bin/" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin/"' "$IMAGE_TASK"; then
    echo "image task must not trust overlay/oem/usr/bin as the final generated binary source" >&2
    exit 1
fi
if ! grep -Fq 'WEB_ROOT=${WEB_ROOT:-/oem/usr/share/aiden/config-web}' "$CONFIG_WEB_INIT_SCRIPT" || \
   ! grep -Fq -- '--web-root="$WEB_ROOT"' "$CONFIG_WEB_INIT_SCRIPT"; then
    echo "config_web init must pass the overridable OEM web root" >&2
    exit 1
fi
if ! grep -Fq 'CONFIG_WEB_SRC="$REPO_ROOT/src/config_web/web"' "$IMAGE_TASK" || \
   ! grep -Fq 'CONFIG_WEB_DEST="$RK_PROJECT_PACKAGE_OEM_DIR/usr/share/aiden/config-web"' "$IMAGE_TASK" || \
   ! grep -Fq 'rsync -a --delete "$CONFIG_WEB_SRC/" "$CONFIG_WEB_DEST/"' "$IMAGE_TASK"; then
    echo "image task must replace OEM config web assets from src/config_web/web" >&2
    exit 1
fi
if grep -Fq 'RK_PROJECT_PACKAGE_USERDATA_DIR/usr/share/aiden/config-web' "$IMAGE_TASK"; then
    echo "config web assets must not be staged in userdata" >&2
    exit 1
fi
if ! grep -Fq 'verify_oem_config_web_in_image "$RK_PROJECT_OUTPUT_IMAGE/oem.img" "$RK_PROJECT_PACKAGE_OEM_DIR"' "$IMAGE_TASK" || \
   ! grep -Fq '"usr/share/aiden/config-web/index.html"' "$EXT4_IMAGES_LIB" || \
   ! grep -Fq '"usr/share/aiden/config-web/llm-logs.html"' "$EXT4_IMAGES_LIB"; then
    echo "image task must verify both config web entry pages in the final OEM image" >&2
    exit 1
fi
firmware_count=$(grep -cF 'run_pico_sdk_project_build firmware "$@"' "$IMAGE_TASK")
firmware_line=$(grep -nF 'run_pico_sdk_project_build firmware "$@"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
if [ "$firmware_count" -ne 1 ] || [ -z "$firmware_line" ] || [ "$firmware_line" -ge "$oem_full_sync_line" ]; then
    echo "image task must sync final OEM overlay after pico-sdk firmware packaging regenerates SDK-managed OEM files" >&2
    exit 1
fi
if ! grep -Fq '"usr/ko/insmod_wifi.sh"' "$IMAGE_TASK" || \
   grep -Fq '"usr/ko"' "$IMAGE_TASK"; then
    echo "image task must clean only Aiden-managed usr/ko overrides, not the SDK module directory" >&2
    exit 1
fi

rootfs_cleanup_line=$(grep -nF 'scripts/clean_rootfs_overlay_staging.sh"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
rootfs_cli_build_line=$(grep -nF 'scripts/build_rootfs_cli_tools.sh"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
rootfs_cli_stage_line=$(grep -nF 'scripts/stage_rootfs_cli_tools.sh"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
sysdrv_line=$(grep -nF 'run_pico_sdk_build sysdrv "$@"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
if [ -z "$rootfs_cleanup_line" ] || [ -z "$rootfs_cli_build_line" ] || \
   [ -z "$rootfs_cli_stage_line" ] || [ -z "$sysdrv_line" ] || \
   [ "$rootfs_cleanup_line" -ge "$rootfs_cli_build_line" ] || \
   [ "$rootfs_cli_build_line" -ge "$rootfs_cli_stage_line" ] || \
   [ "$rootfs_cli_stage_line" -ge "$sysdrv_line" ]; then
    echo "image task must clean, build, and stage rootfs CLI tools before the Buildroot sysdrv build" >&2
    exit 1
fi
if ! grep -Fq 'verify_rootfs_cli_tools_in_image "$RK_PROJECT_OUTPUT_IMAGE/rootfs.img" "$DEST_OVERLAY" "$RK_PROJECT_PACKAGE_ROOTFS_DIR"' "$IMAGE_TASK"; then
    echo "image task must verify every catalog tool inside the final rootfs image" >&2
    exit 1
fi
rootfs_cli_restage_line=$(grep -nF -- '--dest-overlay "$RK_PROJECT_PACKAGE_ROOTFS_DIR"' "$IMAGE_TASK" | sed 's/:.*//' | tail -n 1)
firmware_package_line=$(grep -nF 'run_pico_sdk_project_build firmware "$@"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
rootfs_rebuild_line=$(grep -nF 'rebuild_ext4_image rootfs "$RK_PROJECT_PACKAGE_ROOTFS_DIR" "$RK_PROJECT_OUTPUT_IMAGE"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
rootfs_cli_verify_line=$(grep -nF 'verify_rootfs_cli_tools_in_image "$RK_PROJECT_OUTPUT_IMAGE/rootfs.img" "$DEST_OVERLAY" "$RK_PROJECT_PACKAGE_ROOTFS_DIR"' "$IMAGE_TASK" | sed 's/:.*//' | head -n 1)
if ! grep -Fq 'RK_PROJECT_PACKAGE_ROOTFS_DIR="${RK_PROJECT_OUTPUT}/rootfs_${RK_LIBC_TPYE}_${RK_CHIP}"' "$IMAGE_TASK"; then
    echo "image task must define the SDK rootfs staging directory before restaging CLI tools" >&2
    exit 1
fi
if [ -z "$firmware_package_line" ] || [ -z "$rootfs_cli_restage_line" ] || \
   [ -z "$rootfs_rebuild_line" ] || \
   [ -z "$rootfs_cli_verify_line" ] || \
   [ "$firmware_package_line" -ge "$rootfs_cli_restage_line" ] || \
   [ "$rootfs_cli_restage_line" -ge "$rootfs_rebuild_line" ] || \
   [ "$rootfs_rebuild_line" -ge "$rootfs_cli_verify_line" ]; then
    echo "image task must package firmware, restage CLI tools, rebuild rootfs.img, then verify it" >&2
    exit 1
fi
if ! grep -Fq 'ROOTFS_CLI_TOOL_CATALOG="$REPO_ROOT/scripts/rootfs_cli_tools.catalog"' "$IMAGE_TASK" || \
   ! grep -Fq 'rootfs_cli_catalog_name_policy_records "$ROOTFS_CLI_TOOL_CATALOG"' "$IMAGE_TASK" || \
   ! grep -Fq 'for tool in "${ROOTFS_CLI_PRESERVE_TOOLS[@]}"' "$EXT4_IMAGES_LIB" || \
   ! grep -Fq -- '-path "$target_dir/usr/bin/$tool"' "$EXT4_IMAGES_LIB"; then
    echo "release builds must derive rootfs CLI tool and preserve lists from the shared catalog" >&2
    exit 1
fi
if grep -Fq 'ROOTFS_CLI_TOOLS=(fq yq rg)' "$IMAGE_TASK" "$EXT4_IMAGES_LIB" || \
   grep -Fq -- '-path "$target_dir/usr/bin/fq"' "$EXT4_IMAGES_LIB"; then
    echo "release builds must not hardcode rootfs CLI tool names" >&2
    exit 1
fi
if ! grep -Fq -- '--catalog "$ROOTFS_CLI_TOOL_CATALOG"' "$IMAGE_TASK" || \
   ! grep -Fq -- '--policy preserve' "$IMAGE_TASK" || \
   ! grep -Fq 'ROOTFS_CLI_MANAGED_STATE="${DEST_OVERLAY}.aiden-rootfs-cli-tools.list"' "$IMAGE_TASK" || \
   ! grep -Fq -- '--managed-state "$ROOTFS_CLI_MANAGED_STATE"' "$IMAGE_TASK"; then
    echo "rootfs CLI build, staging, and post-strip restore must use the shared catalog policy" >&2
    exit 1
fi
if ! grep -Fq 'ROOTFS_CLI_CACHE_DIR="$REPO_ROOT/.cache/rootfs-cli-tools"' "$IMAGE_TASK"; then
    echo "rootfs CLI cache must live outside build/, which the binaries task recreates on every image build" >&2
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

if grep -Fq 'echo "max_download_bytes=$(aiden_ota_manifest_max_download_bytes)"' "$WORKFLOW"; then
    echo "build workflow must not mask OTA download-limit resolution failures inside echo" >&2
    exit 1
fi

if ! grep -Fq 'max_download_bytes="$(aiden_ota_manifest_max_download_bytes)" || {' "$WORKFLOW" || \
   ! grep -Fq "printf 'max_download_bytes=%s\\n' \"\$max_download_bytes\" >> \"\$GITHUB_OUTPUT\"" "$WORKFLOW"; then
    echo "build workflow must resolve and validate the OTA download limit before publishing it" >&2
    exit 1
fi

echo "release CI script tests passed"
