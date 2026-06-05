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

if grep -q './build.sh all' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must not run build.sh all before overlay injection; it creates large A/B images twice" >&2
    exit 1
fi

if ! grep -q './build.sh sysdrv' "$ROOT_DIR/_build_image.sh" || \
   ! grep -q './build.sh media' "$ROOT_DIR/_build_image.sh" || \
   ! grep -q './build.sh app' "$ROOT_DIR/_build_image.sh" || \
   ! grep -q './build.sh firmware' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must build components first, inject overlay, then package firmware once" >&2
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

if ! grep -q 'scripts/test_release_ci_scripts.sh' "$CI_WORKFLOW" || \
   ! grep -q 'scripts/test_github_release_upload.sh' "$CI_WORKFLOW"; then
    echo "CI must run repo-only release workflow and upload script tests" >&2
    exit 1
fi

echo "build script tests passed"
