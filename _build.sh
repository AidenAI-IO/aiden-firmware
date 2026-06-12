#!/bin/bash
# Build script for Aiden SDK
# This should be run inside the Docker development environment

set -e

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR="$ROOT_DIR/build"
TOOLCHAIN_FILE="$ROOT_DIR/cmake/toolchain-arm-rockchip830.cmake"

echo "Building Aiden SDK..."

cmake -S "$ROOT_DIR" -B "$BUILD_DIR" -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE"
CMAKE_JOBS="${CMAKE_BUILD_PARALLEL_LEVEL:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)}"
cmake --build "$BUILD_DIR" --parallel "$CMAKE_JOBS"

echo "Build complete!"
echo "Library: $ROOT_DIR/build/lib/libaiden.a"
echo "Binaries in: $ROOT_DIR/build/bin/"
ls -lh "$BUILD_DIR/bin/"

# Build Go tools. The build environment must provide a verified Go in PATH.
# GOTOOLCHAIN=local disables automatic toolchain downloads; if the installed Go
# cannot satisfy go.mod, go build fails before producing binaries.
echo ""
echo "Building Go binaries..."

GO_VERSION="1.26.0"
if ! command -v go >/dev/null 2>&1; then
    echo "Go ${GO_VERSION} is required in PATH. Install a verified Go toolchain in the build container/CI before running _build.sh." >&2
    exit 1
fi

BUILD_CACHE_DIR="${AIDEN_BUILD_CACHE_DIR:-$ROOT_DIR/build/.cache}"
export GOCACHE="${AIDEN_GOCACHE:-$BUILD_CACHE_DIR/go-build}"
export GOMODCACHE="${AIDEN_GOMODCACHE:-$BUILD_CACHE_DIR/go-mod}"
export GOPATH="${AIDEN_GOPATH:-$BUILD_CACHE_DIR/gopath}"
export GOTOOLCHAIN=local
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOPATH"

AGENT_COMMIT="$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
AGENT_BUILD_VERSION="$(date -u +"%Y%m%d-%H%M%S")-${AGENT_COMMIT}"
AGENT_LDFLAGS="-X aiden-agent/internal/agent.buildCommit=${AGENT_COMMIT} -X aiden-agent/internal/agent.buildVersion=${AGENT_BUILD_VERSION}"

cd "$ROOT_DIR/src/agent"
GOOS=linux GOARCH=arm GOARM=7 go build -buildvcs=false -ldflags "${AGENT_LDFLAGS}" -o "$BUILD_DIR/bin/agent" ./cmd/daemon
GOOS=linux GOARCH=arm GOARM=7 go build -buildvcs=false -ldflags "${AGENT_LDFLAGS}" -o "$BUILD_DIR/bin/ota" ./cmd/ota
GOOS=linux GOARCH=arm GOARM=7 go build -buildvcs=false -ldflags "${AGENT_LDFLAGS}" -o "$BUILD_DIR/bin/abctl" ./cmd/abctl
cd "$ROOT_DIR"

echo "Go binaries built:"
ls -lh "${BUILD_DIR}/bin/agent" "${BUILD_DIR}/bin/ota" "${BUILD_DIR}/bin/abctl"
