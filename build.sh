#!/usr/bin/env bash
set -e

docker run \
  --platform linux/amd64 \
  -u "$(id -u):$(id -g)" \
  --rm \
  -v "$(pwd):/home" \
  -w /home \
  luckfoxtech/luckfox_pico:1.0 \
  /bin/bash -c "./_build.sh"
