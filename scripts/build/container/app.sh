#!/usr/bin/env bash
set -euo pipefail

CONTAINER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$CONTAINER_DIR/../../.." && pwd)"
BUILD_DIR="$REPO_ROOT/build"
TOOLCHAIN_FILE="$REPO_ROOT/cmake/toolchain-arm-rockchip830.cmake"

if [ "${AIDEN_BUILD_CONTEXT:-}" != container ]; then
    echo "Run this task through ./build.sh app." >&2
    exit 2
fi

echo "Building Aiden SDK..."

rm -rf "$BUILD_DIR"
cmake -S "$REPO_ROOT" -B "$BUILD_DIR" -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE"
CMAKE_JOBS="${CMAKE_BUILD_PARALLEL_LEVEL:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)}"
cmake --build "$BUILD_DIR" --parallel "$CMAKE_JOBS"

echo "Build complete!"
echo "Library: $BUILD_DIR/lib/libaiden.a"
echo "Binaries in: $BUILD_DIR/bin/"
ls -lh "$BUILD_DIR/bin/"

# Build Go tools. The build environment must provide a verified Go in PATH.
# GOTOOLCHAIN=local disables automatic toolchain downloads; if the installed Go
# cannot satisfy go.mod, go build fails before producing binaries.
echo ""
echo "Building Go binaries..."

GO_VERSION="1.26.0"
if ! command -v go >/dev/null 2>&1; then
    echo "Go ${GO_VERSION} is required in PATH. Run this task through ./build.sh app." >&2
    exit 1
fi

export GOCACHE="/tmp/go-cache"
export GOMODCACHE="/tmp/go-mod"
export GOPATH="/tmp/gopath"
export GOTOOLCHAIN=local

AGENT_COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
AGENT_BUILD_VERSION="$(date -u +"%Y%m%d-%H%M%S")-${AGENT_COMMIT}"
AGENT_LDFLAGS="-X aiden-agent/internal/agent.buildCommit=${AGENT_COMMIT} -X aiden-agent/internal/agent.buildVersion=${AGENT_BUILD_VERSION}"

cd "$REPO_ROOT/src/agent"
GOOS=linux GOARCH=arm GOARM=7 go build -buildvcs=false -ldflags "${AGENT_LDFLAGS}" -o "$BUILD_DIR/bin/agent" ./cmd/daemon
GOOS=linux GOARCH=arm GOARM=7 go build -buildvcs=false -o "$BUILD_DIR/bin/ble_service" ./cmd/ble_service
GOOS=linux GOARCH=arm GOARM=7 go build -buildvcs=false -ldflags "${AGENT_LDFLAGS}" -o "$BUILD_DIR/bin/ota" ./cmd/ota
GOOS=linux GOARCH=arm GOARM=7 go build -buildvcs=false -ldflags "${AGENT_LDFLAGS}" -o "$BUILD_DIR/bin/abctl" ./cmd/abctl

echo "Go binaries built:"
ls -lh "${BUILD_DIR}/bin/agent" "${BUILD_DIR}/bin/ble_service" "${BUILD_DIR}/bin/ota" "${BUILD_DIR}/bin/abctl"
