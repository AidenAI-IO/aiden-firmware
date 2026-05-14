#!/bin/bash
# Build script for Aiden SDK
# This should be run inside the Docker development environment

set -e

ROOT_DIR="./"
BUILD_DIR="$ROOT_DIR/build"
TOOLCHAIN_FILE="$ROOT_DIR/cmake/toolchain-arm-rockchip830.cmake"

echo "Building Aiden SDK..."

rm -rf "$BUILD_DIR"
cmake -S "$ROOT_DIR" -B "$BUILD_DIR" -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE"
cmake --build "$BUILD_DIR"

echo "Build complete!"
echo "Library: $ROOT_DIR/build/lib/libaiden.a"
echo "Binaries in: $ROOT_DIR/build/bin/"
ls -lh "$ROOT_DIR/build/bin/"

# Build Go agent
echo ""
echo "Building Go agent..."

GO_VERSION="1.26.0"
GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"
GO_INSTALL_DIR="/tmp/go-${GO_VERSION}"

if [ ! -x "${GO_INSTALL_DIR}/bin/go" ]; then
    echo "Installing Go ${GO_VERSION}..."
    mkdir -p "${GO_INSTALL_DIR}"
    wget -q "https://go.dev/dl/${GO_TARBALL}" -O "/tmp/${GO_TARBALL}"
    tar -C "${GO_INSTALL_DIR}" --strip-components=1 -xzf "/tmp/${GO_TARBALL}"
    rm -f "/tmp/${GO_TARBALL}"
fi

export PATH="${GO_INSTALL_DIR}/bin:$PATH"
export GOCACHE="/tmp/go-cache"
export GOMODCACHE="/tmp/go-mod"
export GOPATH="/tmp/gopath"
export GOTOOLCHAIN=local

cd src/agent
GOOS=linux GOARCH=arm GOARM=7 go build -o "../../${BUILD_DIR}/bin/agent" ./cmd/daemon
cd ../..

echo "Go agent built: ${BUILD_DIR}/bin/agent"
ls -lh "${BUILD_DIR}/bin/agent"
