#!/usr/bin/env bash
set -euo pipefail

docker_go_args=()
if command -v go >/dev/null 2>&1; then
  host_goroot=$(go env GOROOT)
  host_goos=$(go env GOHOSTOS)
  host_goarch=$(go env GOHOSTARCH)
  if [ -d "$host_goroot" ] && [ "$host_goos" = linux ] && [ "$host_goarch" = amd64 ]; then
    docker_go_args=(-v "${host_goroot}:/usr/local/go:ro")
  else
    echo "Host Go toolchain is not linux/amd64; Docker build will rely on go already being present in the image." >&2
  fi
else
  echo "Host Go toolchain not found; Docker build will rely on go already being present in the image." >&2
fi

docker run \
  --platform linux/amd64 \
  --privileged \
  --rm \
  -e OTA_PUBLIC_KEY_PATH \
  -e OTA_ALLOW_DEV_KEY \
  "${docker_go_args[@]}" \
  -v "$(pwd):/home" \
  -w /home \
  luckfoxtech/luckfox_pico:1.0 \
  /bin/bash -c 'export PATH="/usr/local/go/bin:$PATH"; ./_build_image.sh'
