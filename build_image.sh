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

docker_command=("./_build_image.sh")
if [ "$#" -gt 0 ]; then
  docker_command=("$@")
fi

restore_docker_output_ownership() {
  if [ "$(uname -s)" != Linux ] || ! command -v sudo >/dev/null 2>&1; then
    return 0
  fi

  paths=()
  for path in build overlay/oem pico-sdk/output; do
    if [ -e "$path" ]; then
      paths+=("$path")
    fi
  done

  if [ "${#paths[@]}" -gt 0 ]; then
    sudo chown -R "$(id -u):$(id -g)" "${paths[@]}"
  fi
}

docker run \
  --platform linux/amd64 \
  --privileged \
  -u 0:0 \
  --rm \
  -e OTA_PUBLIC_KEY_PATH \
  "${docker_go_args[@]}" \
  -v "$(pwd):/home" \
  -w /home \
  luckfoxtech/luckfox_pico:1.0 \
  /bin/bash -c 'export PATH="/usr/local/go/bin:$PATH"; exec "$@"' _ "${docker_command[@]}"

restore_docker_output_ownership
