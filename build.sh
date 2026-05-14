#!/usr/bin/env bash
set -e

# Build C++ components using ARM toolchain
docker run \
  --platform linux/amd64 \
  -u "$(id -u):$(id -g)" \
  --rm \
  -v "$(pwd):/home" \
  -w /home \
  luckfoxtech/luckfox_pico:1.0 \
  /bin/bash -c "./_build.sh"

# Build Go agent using golang Docker
echo ""
echo "Building Go agent..."
docker run --rm \
  -u "$(id -u):$(id -g)" \
  -v "$(pwd)/src/agent:/go/src/app" \
  -v "$(pwd)/build/bin:/go/bin" \
  -v "$(pwd)/build/.cache:/go/.cache" \
  -e GOCACHE=/go/.cache/go-build \
  -e GOMODCACHE=/go/.cache/go-mod \
  -w /go/src/app \
  golang:1.26-alpine \
  sh -c "GOOS=linux GOARCH=arm GOARM=7 go build -o /go/bin/agent ./cmd/daemon"

if [ -f "$(pwd)/build/bin/agent" ]; then
  echo "Go agent built successfully!"
  ls -lh "$(pwd)/build/bin/agent"
else
  echo "Warning: Go agent build failed"
fi

