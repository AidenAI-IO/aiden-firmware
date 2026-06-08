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

if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
  SOURCE_DATE_EPOCH="$(git -C "$(pwd)" log -1 --format=%ct 2>/dev/null || printf '0')"
fi
export SOURCE_DATE_EPOCH

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
    # Errors here must not mask the original docker exit status, hence `|| true`.
    sudo chown -R "$(id -u):$(id -g)" "${paths[@]}" || true
  fi
}

# Always restore ownership on exit, even when the script is interrupted by a
# signal (CI cancellation, runner restart, ^C). Without this, root-owned build
# artifacts get left behind and the next actions/checkout clean step fails with
# EACCES. Forwarding SIGINT/SIGTERM/SIGHUP through `exit` ensures the EXIT trap
# fires for signal-induced terminations as well as normal returns.
trap restore_docker_output_ownership EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

docker_run_args=(
  --platform linux/amd64
  --privileged
  -u 0:0
  --rm
  -e OTA_PUBLIC_KEY_PATH
  -e SOURCE_DATE_EPOCH
  -e TAR_OPTIONS=--no-same-owner
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

# Ownership restore happens in the EXIT trap above.
exit "$docker_status"
