#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUILD_SH="$ROOT_DIR/_build.sh"
LOCAL_BUILD_SH="$ROOT_DIR/build.sh"
BUILD_IMAGE_SH="$ROOT_DIR/build_image.sh"
WORKFLOW="$ROOT_DIR/.github/workflows/build.yml"
CI_WORKFLOW="$ROOT_DIR/.github/workflows/ci.yml"
REPACK_SCRIPT="$ROOT_DIR/scripts/repack_ota_update_image.sh"
GITIGNORE="$ROOT_DIR/.gitignore"

if grep -Eq 'go\.dev/dl|wget .*go|curl .*go|tar .*go\$|GO_TARBALL|GO_TARBALL_SHA256' "$BUILD_SH" "$BUILD_IMAGE_SH"; then
    echo "build scripts must not download or extract Go toolchains" >&2
    exit 1
fi

if grep -Eq 'GOTOOLCHAIN=(auto|path|go[0-9])' "$BUILD_SH"; then
    echo "_build.sh must not allow automatic or pinned Go toolchain downloads" >&2
	 exit 1
fi

if ! grep -q 'GOTOOLCHAIN=local' "$BUILD_SH"; then
    echo "_build.sh must force local/no-download Go toolchain mode" >&2
	 exit 1
fi

if ! grep -q 'command -v go' "$BUILD_SH"; then
    echo "_build.sh must clearly require go in PATH" >&2
    exit 1
fi

if ! grep -q 'GO_TARBALL_SHA256' "$LOCAL_BUILD_SH" || \
   ! grep -q 'go.dev/dl' "$LOCAL_BUILD_SH" || \
   ! grep -Eq 'sha256sum|shasum -a 256' "$LOCAL_BUILD_SH"; then
    echo "build.sh must install a verified linux/amd64 Go toolchain for Docker builds" >&2
    exit 1
fi

if ! grep -Eq -- '-v .*:/usr/local/go:ro' "$LOCAL_BUILD_SH"; then
    echo "build.sh must mount the verified Go toolchain read-only into Docker" >&2
    exit 1
fi

if ! grep -q '/usr/local/go/bin:$PATH' "$LOCAL_BUILD_SH"; then
    echo "build.sh must prepend the mounted Go toolchain to Docker PATH" >&2
    exit 1
fi

if grep -q 'build.sh firmware .*|| true' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must not mask firmware rebuild failures" >&2
    exit 1
fi

if ! grep -Fq 'rm -rf "$BUILD_DIR"' "$BUILD_SH"; then
    echo "_build.sh must clean the CMake build directory; this PR only keeps parallel compile speedups, not reused build outputs" >&2
    exit 1
fi

if ! grep -q 'cmake --build "$BUILD_DIR" --parallel' "$BUILD_SH"; then
    echo "_build.sh must use parallel CMake builds for changed native targets" >&2
    exit 1
fi

if grep -Fq 'AIDEN_BUILD_CACHE_DIR' "$BUILD_SH" || \
   grep -Fq 'build/.cache' "$BUILD_SH"; then
    echo "_build.sh must not add workspace build-output reuse caches in this PR" >&2
    exit 1
fi

if ! grep -Fq 'GOCACHE="/tmp/go-cache"' "$BUILD_SH" || \
   ! grep -Fq 'GOMODCACHE="/tmp/go-mod"' "$BUILD_SH" || \
   ! grep -Fq 'GOPATH="/tmp/gopath"' "$BUILD_SH"; then
    echo "_build.sh must keep Go caches ephemeral; persistent cache reuse is out of scope for this PR" >&2
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

if grep -q './build.sh all' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must not run build.sh all before overlay injection; it creates large A/B images twice" >&2
    exit 1
fi

if grep -q 'SDK_STAGE_STATE_DIR=' "$ROOT_DIR/_build_image.sh" || \
   grep -q 'run_cached_sdk_stage' "$ROOT_DIR/_build_image.sh" || \
   grep -q 'Skipping pico-sdk $stage (cached outputs match inputs)' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must not cache or reuse pico-sdk stage outputs in this PR" >&2
    exit 1
fi

if grep -q 'RK_LIBC_TPYE:=glibc' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must not guess the SDK libc type; pico-sdk derives it from RK_TOOLCHAIN_CROSS" >&2
    exit 1
fi

if ! grep -q './build.sh sysdrv' "$ROOT_DIR/_build_image.sh" || \
   ! grep -q './build.sh media' "$ROOT_DIR/_build_image.sh" || \
   ! grep -q './build.sh app' "$ROOT_DIR/_build_image.sh" || \
   ! grep -q './build.sh firmware' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must build components directly, inject overlay, then package firmware once" >&2
    exit 1
fi

if [ ! -x "$ROOT_DIR/scripts/clean_rootfs_overlay_staging.sh" ]; then
    echo "rootfs overlay cleanup script must exist and be executable" >&2
    exit 1
fi

if ! grep -Fq 'scripts/clean_rootfs_overlay_staging.sh" --dest-overlay "$DEST_OVERLAY"' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must clean stale rootfs overlay staging before syncing current rootfs assets" >&2
    exit 1
fi

if grep -Fq 'cp -a "$SCRIPT_DIR/build/bin"/. "$OVERLAY/oem/usr/bin/"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq 'clean_generated_binaries "$OVERLAY/oem/usr/bin"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq 'clean_generated_binaries "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin"' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must remove stale generated binaries from overlay and SDK OEM staging" >&2
    exit 1
fi

oem_bin_sync_line=$(grep -nF 'rsync -a --delete "$OVERLAY/oem/usr/bin/" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin/"' "$ROOT_DIR/_build_image.sh" | sed 's/:.*//' | head -n 1)
oem_full_sync_line=$(grep -nF 'rsync -a "$OVERLAY/oem/" "$RK_PROJECT_PACKAGE_OEM_DIR/"' "$ROOT_DIR/_build_image.sh" | sed 's/:.*//' | head -n 1)
if [ -z "$oem_bin_sync_line" ] || [ -z "$oem_full_sync_line" ] || [ "$oem_bin_sync_line" -ge "$oem_full_sync_line" ]; then
    echo "_build_image.sh must sync OEM usr/bin with delete semantics before full OEM overlay sync" >&2
    exit 1
fi

if ! grep -Fq 'clean_managed_staging_paths "$RK_PROJECT_PACKAGE_OEM_DIR"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq '"usr/model"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq '"etc/ota_pubkey.pem"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq 'clean_managed_staging_paths "$RK_PROJECT_PACKAGE_USERDATA_DIR"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq '"agent/benchmark"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq '"agent_tools"' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must clean Aiden-managed SDK staging paths before preserving pico-sdk/output/out" >&2
    exit 1
fi

if ! grep -Fq 'BENCHMARK_SRC="$SCRIPT_DIR/benchmark"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq 'BENCHMARK_DEST="$OVERLAY/userdata/agent/benchmark"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq -- "--exclude '__pycache__/'" "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq -- "--exclude '*.pyc'" "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq -- "--exclude '.DS_Store'" "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq -- "--exclude '._*'" "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq 'rsync -a --delete "${BENCHMARK_RSYNC_EXCLUDES[@]}" "$BENCHMARK_SRC/runner/" "$BENCHMARK_DEST/runner/"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq 'rsync -a --delete "${BENCHMARK_RSYNC_EXCLUDES[@]}" "$BENCHMARK_SRC/suites/" "$BENCHMARK_DEST/suites/"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq 'rm -f "$BENCHMARK_DEST/pyproject.toml"' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must stage benchmark runner and suites into userdata" >&2
    exit 1
fi

if grep -Fq 'SKILLS_DEST="$OVERLAY/oem/usr/share/aiden/skills"' "$ROOT_DIR/_build_image.sh" || \
   grep -Fq 'SKILLS_DEST="$DEST_OVERLAY/usr/share/aiden/skills"' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must not stage bundled skills through repo overlay directories" >&2
    exit 1
fi

if ! grep -Fq 'SKILLS_DEST="$RK_PROJECT_PACKAGE_OEM_DIR/usr/share/aiden/skills"' "$ROOT_DIR/_build_image.sh" || \
   ! grep -Fq 'rsync -a --delete "$SKILLS_SRC/" "$SKILLS_DEST/"' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must sync bundled skills directly into final OEM staging with delete semantics" >&2
    exit 1
fi

oem_dir_line=$(grep -n 'RK_PROJECT_PACKAGE_OEM_DIR="${RK_PROJECT_OUTPUT}/oem"' "$ROOT_DIR/_build_image.sh" | sed 's/:.*//' | head -n 1)
skills_dest_line=$(grep -n 'SKILLS_DEST="$RK_PROJECT_PACKAGE_OEM_DIR/usr/share/aiden/skills"' "$ROOT_DIR/_build_image.sh" | sed 's/:.*//' | head -n 1)
if [ -z "$oem_dir_line" ] || [ -z "$skills_dest_line" ] || [ "$skills_dest_line" -le "$oem_dir_line" ]; then
    echo "_build_image.sh must resolve final OEM staging before setting bundled skills destination" >&2
    exit 1
fi

if ! grep -q 'overlay/userdata' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must restore ownership of Docker-staged overlay userdata" >&2
    exit 1
fi

if ! grep -q '^/overlay/userdata/agent/benchmark/$' "$GITIGNORE"; then
    echo "generated benchmark userdata staging directory must be gitignored" >&2
    exit 1
fi

if ! grep -q 'copy_ab_image' "$ROOT_DIR/pico-sdk/project/build.sh"; then
    echo "pico-sdk build.sh must avoid duplicating identical A/B image bytes when possible" >&2
    exit 1
fi

if ! grep -q 'normalize_image_tree_ownership' "$ROOT_DIR/pico-sdk/project/build.sh" || \
   ! grep -q 'chown -hR 0:0' "$ROOT_DIR/pico-sdk/project/build.sh" || \
   ! grep -q 'id -u' "$ROOT_DIR/pico-sdk/project/build.sh" || \
   ! grep -q 'Skip ownership normalization' "$ROOT_DIR/pico-sdk/project/build.sh"; then
    echo "pico-sdk build.sh must normalize image staging ownership before mkfs" >&2
    exit 1
fi

for required in \
    'SOURCE_DATE_EPOCH ?=' \
    'local epoch="${SOURCE_DATE_EPOCH:-' \
    'source_date_epoch="${SOURCE_DATE_EPOCH:-' \
    '--sort=name' \
    '--mtime="@$(SOURCE_DATE_EPOCH)"' \
    'Build Time:  $(reproducible_build_utc)' \
    'find "$dir" -xdev -exec touch -h -d "@$epoch"' \
    'lazy_itable_init=0,lazy_journal_init=0' \
    '^metadata_csum' \
    '^orphan_file' \
    '^quota' \
    '-U "${AIDEN_EXT4_UUID:-00000000-0000-4000-8000-000000000000}"' \
    'write_ext4_le32_at "$image" "$((sb_offset + 44))" "$source_date_epoch"' \
    'write_ext4_le32_at "$image" "$((sb_offset + 264))" "$source_date_epoch"' \
    'write_at($fh, $inode_offset + 100, "\0" x 4)' \
    'normalize ext4 inode metadata'; do
    if ! grep -Fq -- "$required" "$ROOT_DIR/pico-sdk/project/build.sh" \
       && ! grep -Fq -- "$required" "$ROOT_DIR/pico-sdk/sysdrv/Makefile" \
       && ! grep -Fq -- "$required" "$ROOT_DIR/pico-sdk/sysdrv/tools/pc/e2fsprogs/mkfs_ext4.sh"; then
        echo "pico-sdk rootfs reproducibility support missing required content: $required" >&2
        exit 1
    fi
done

if ! grep -q 'chown -hR 0:0 "\$USERDATA_DIR"' "$REPACK_SCRIPT"; then
    echo "OTA update repack must normalize userdata ownership before rebuilding userdata.img" >&2
    exit 1
fi

dev_key_env='OTA_ALLOW_DEV_''KEY'
dev_key_file='ota_pubkey.''dev''.pem'
if grep -R -Eq "${dev_key_env}|${dev_key_file}" "$ROOT_DIR/_build_image.sh" "$ROOT_DIR/build_image.sh" "$ROOT_DIR/scripts/validate_ota_pubkey.sh"; then
    echo "production build path must not support development OTA key fallback" >&2
    exit 1
fi

if ! grep -q 'actions/setup-go@' "$WORKFLOW"; then
    echo "build workflow must install a verified Go toolchain with actions/setup-go" >&2
    exit 1
fi

if ! grep -Eq 'uses: actions/setup-go@v[0-9]+' "$WORKFLOW"; then
    echo "actions/setup-go must use an explicit version tag" >&2
    exit 1
fi

setup_go_line=$(grep -n 'actions/setup-go@' "$WORKFLOW" | sed 's/:.*//' | head -n 1)
run_build_line=$(grep -n 'Run build script' "$WORKFLOW" | sed 's/:.*//' | head -n 1)
if [ -z "$setup_go_line" ] || [ -z "$run_build_line" ] || [ "$setup_go_line" -ge "$run_build_line" ]; then
    echo "actions/setup-go must run before the image build" >&2
    exit 1
fi

if grep -Fq 'sudo chown -R "$(id -u):$(id -g)" "$GITHUB_WORKSPACE"' "$WORKFLOW"; then
    echo "self-hosted workspace reclaim must not require sudo unconditionally" >&2
    exit 1
fi

if ! grep -Fq 'owner="$(id -u):$(id -g)"' "$WORKFLOW" || \
   ! grep -Fq 'command -v sudo >/dev/null 2>&1' "$WORKFLOW" || \
   ! grep -Fq 'sudo chown -R "$owner" "$GITHUB_WORKSPACE"' "$WORKFLOW" || \
   ! grep -Fq '[ "$(id -u)" -eq 0 ]' "$WORKFLOW" || \
   ! grep -Eq '^[[:space:]]+chown -R "\$owner" "\$GITHUB_WORKSPACE"' "$WORKFLOW" || \
   ! grep -Fq '::warning::Skipping workspace ownership reclaim' "$WORKFLOW"; then
    echo "self-hosted workspace reclaim must handle sudo, root, and non-root runners without failing" >&2
    exit 1
fi

if ! grep -q 'go env GOROOT' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must discover the host Go root with go env GOROOT" >&2
    exit 1
fi

if ! grep -q 'GOHOSTOS' "$BUILD_IMAGE_SH" || ! grep -q 'GOHOSTARCH' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must verify host Go OS and architecture" >&2
    exit 1
fi

if ! grep -Eq -- '-v .*:/usr/local/go:ro' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must mount suitable host Go read-only into Docker" >&2
    exit 1
fi

if ! grep -q 'docker_ota_key_args' "$BUILD_IMAGE_SH" || ! grep -q 'OTA_PUBLIC_KEY_PATH}:ro' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must mount OTA_PUBLIC_KEY_PATH read-only into Docker" >&2
    exit 1
fi

if ! grep -Eq -- '-u 0:0|--user 0:0' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must run Docker packaging as root so image ownership normalization works" >&2
    exit 1
fi

if ! grep -q 'TAR_OPTIONS=--no-same-owner' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must prevent tar from restoring archived owners in Dockerized Buildroot" >&2
    exit 1
fi

if ! grep -q '/usr/local/go/bin:$PATH' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must prepend mounted Go to Docker PATH" >&2
    exit 1
fi

if ! grep -q 'chown -R' "$BUILD_IMAGE_SH" || ! grep -q 'pico-sdk/output' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must restore ownership of Docker-generated output for later CI steps" >&2
    exit 1
fi

if ! grep -Eq '^trap[[:space:]]+restore_docker_output_ownership[[:space:]]+EXIT' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must restore Docker output ownership in an EXIT trap so signal-killed builds do not leave root-owned files for the next CI checkout" >&2
    exit 1
fi

if ! grep -q 'exec "\$@"' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must allow CI to run a Dockerized repack command" >&2
    exit 1
fi

if [ ! -f "$REPACK_SCRIPT" ]; then
    echo "missing OTA update repack script" >&2
    exit 1
fi

if grep -q 'build.sh firmware' "$REPACK_SCRIPT"; then
    echo "OTA update repack must not rebuild boot/oem/rootfs after manifest generation" >&2
    exit 1
fi

if ! grep -q 'build.sh updateimg' "$REPACK_SCRIPT"; then
    echo "OTA update repack must rebuild update.img without rerunning full firmware packaging" >&2
    exit 1
fi

manifest_line=$(grep -n 'Generate OTA manifest' "$WORKFLOW" | sed 's/:.*//' | head -n 1)
config_line=$(grep -n 'Generate OTA device config' "$WORKFLOW" | sed 's/:.*//' | head -n 1)
repack_line=$(grep -n 'Repack update image with OTA config' "$WORKFLOW" | sed 's/:.*//' | head -n 1)
release_line=$(grep -n 'Create Release' "$WORKFLOW" | sed 's/:.*//' | head -n 1)
if [ -z "$manifest_line" ] || [ -z "$config_line" ] || [ -z "$repack_line" ] || [ -z "$release_line" ] || \
    [ "$manifest_line" -ge "$config_line" ] || [ "$config_line" -ge "$repack_line" ] || [ "$repack_line" -ge "$release_line" ]; then
    echo "workflow must generate manifest, write device config, repack update.img, then create release" >&2
    exit 1
fi

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

if ! grep -q 'GH_DEBUG' "$WORKFLOW"; then
    echo "build workflow must enable GitHub CLI debug output for release creation" >&2
    exit 1
fi

if grep -q 'git submodule update.*pico-sdk' "$CI_WORKFLOW"; then
    echo "CI release script checks must not fetch the large pico-sdk submodule" >&2
    exit 1
fi

if grep -q 'scripts/test_reproducible_rootfs_policy.sh' "$CI_WORKFLOW"; then
    echo "CI release script checks must not run submodule-dependent reproducible rootfs policy checks" >&2
    exit 1
fi

if ! grep -q 'scripts/test_release_ci_scripts.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_github_release_upload.sh' "$CI_WORKFLOW"; then
    echo "CI must run repo-only release workflow and upload script tests" >&2
    exit 1
fi

fetch_sdk_line=$(grep -n 'Fetch pico-sdk submodule' "$WORKFLOW" | sed 's/:.*//' | head -n 1)
policy_line=$(grep -n 'scripts/test_reproducible_rootfs_policy.sh' "$WORKFLOW" | sed 's/:.*//' | head -n 1)
run_build_line=$(grep -n 'Run build script' "$WORKFLOW" | sed 's/:.*//' | head -n 1)
if [ -z "$fetch_sdk_line" ] || [ -z "$policy_line" ] || [ -z "$run_build_line" ] || \
   [ "$fetch_sdk_line" -ge "$policy_line" ] || [ "$policy_line" -ge "$run_build_line" ]; then
    echo "build workflow must verify reproducible rootfs policy after fetching pico-sdk and before building images" >&2
    exit 1
fi

echo "build script tests passed"
