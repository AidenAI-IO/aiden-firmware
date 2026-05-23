#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUILD_SH="$ROOT_DIR/_build.sh"
BUILD_IMAGE_SH="$ROOT_DIR/build_image.sh"
WORKFLOW="$ROOT_DIR/.github/workflows/build.yml"
REPACK_SCRIPT="$ROOT_DIR/scripts/repack_ota_update_image.sh"

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

if grep -q 'build.sh firmware .*|| true' "$ROOT_DIR/_build_image.sh"; then
    echo "_build_image.sh must not mask firmware rebuild failures" >&2
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

if ! grep -q '/usr/local/go/bin:$PATH' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must prepend mounted Go to Docker PATH" >&2
    exit 1
fi

if ! grep -q 'chown -R' "$BUILD_IMAGE_SH" || ! grep -q 'pico-sdk/output' "$BUILD_IMAGE_SH"; then
    echo "build_image.sh must restore ownership of Docker-generated output for later CI steps" >&2
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

echo "build script tests passed"
