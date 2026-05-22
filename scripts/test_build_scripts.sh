#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUILD_SH="$ROOT_DIR/_build.sh"
BUILD_IMAGE_SH="$ROOT_DIR/build_image.sh"
WORKFLOW="$ROOT_DIR/.github/workflows/build.yml"

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

echo "build script tests passed"
