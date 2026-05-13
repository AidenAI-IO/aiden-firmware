#!/usr/bin/env bash
set -e

docker run \
  --platform linux/amd64 \
  --privileged \
  --rm \
  -v "$(pwd):/home" \
  -w /home \
  luckfoxtech/luckfox_pico:1.0 \
  /bin/bash -c "./_build_image.sh"
