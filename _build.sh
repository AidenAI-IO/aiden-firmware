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
