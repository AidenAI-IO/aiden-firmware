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

docker_ota_key_args=()
if [ -n "${OTA_PUBLIC_KEY_PATH:-}" ]; then
  ota_key_host_path="$OTA_PUBLIC_KEY_PATH"
  if [ ! -f "$ota_key_host_path" ]; then
    case "$OTA_PUBLIC_KEY_PATH" in
      /home/*) ota_key_host_path="$(pwd)${OTA_PUBLIC_KEY_PATH#/home}" ;;
    esac
  fi
  if [ ! -f "$ota_key_host_path" ]; then
    echo "OTA_PUBLIC_KEY_PATH is set but key is missing on host: $ota_key_host_path" >&2
    exit 1
  fi
  docker_ota_key_args=(-v "${ota_key_host_path}:${OTA_PUBLIC_KEY_PATH}:ro")
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
  for path in build overlay/oem overlay/userdata pico-sdk/output; do
    if [ -e "$path" ]; then
      paths+=("$path")
    fi
  done

  if [ "${#paths[@]}" -gt 0 ]; then
    sudo chown -R "$(id -u):$(id -g)" "${paths[@]}"
  fi
}

docker_run_args=(
  --platform linux/amd64
  --privileged
  -u 0:0
  --rm
  -e OTA_PUBLIC_KEY_PATH
)
if [ "${#docker_go_args[@]}" -gt 0 ]; then
  docker_run_args+=("${docker_go_args[@]}")
fi
docker_run_args+=(-v "$(pwd):/home")
if [ "${#docker_ota_key_args[@]}" -gt 0 ]; then
  docker_run_args+=("${docker_ota_key_args[@]}")
fi
docker_run_args+=(
  -w /home
  luckfoxtech/luckfox_pico:1.0
  /bin/bash -c 'export PATH="/usr/local/go/bin:$PATH"; exec "$@"' _ "${docker_command[@]}"
)

docker_status=0
docker run "${docker_run_args[@]}" || docker_status=$?

restore_docker_output_ownership
exit "$docker_status"
