#!/bin/bash
# Build script for Aiden SDK
# This should be run inside the Docker development environment

set -e

echo "Building Aiden SDK..."

cd /Volumes/dev/aiden-hardware-demo/src

# Clean previous build
make clean

# Build the SDK library
echo "Building libaiden.a..."
make libaiden.a

# Build examples
echo "Building examples..."
make examples

echo "Build complete!"
echo "Output files in: /Volumes/dev/aiden-hardware-demo/output/"
ls -lh ../output/
