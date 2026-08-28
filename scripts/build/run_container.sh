#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BUILD_CONTAINER_IMAGE="luckfoxtech/luckfox_pico:1.0"

usage() {
  echo "Usage: run_container.sh <binaries|image> -- <command> [args...]" >&2
}

if [ "$#" -lt 3 ]; then
  usage
  exit 2
fi

profile="$1"
shift
if { [ "$profile" != binaries ] && [ "$profile" != image ]; } || [ "$1" != -- ]; then
  usage
  exit 2
fi
shift
if [ "$#" -eq 0 ]; then
  usage
  exit 2
fi
container_command=("$@")

docker_pid=""

# shellcheck source=host/go_toolchain.sh
source "$REPO_ROOT/scripts/build/host/go_toolchain.sh"

forward_signal_to_docker() {
  local signal_name="$1"
  local exit_status="$2"

  trap - INT TERM HUP
  if [ -n "${AIDEN_GO_TOOLCHAIN_PROVISION_PID:-}" ] && \
     kill -0 "$AIDEN_GO_TOOLCHAIN_PROVISION_PID" 2>/dev/null; then
    kill -s "$signal_name" "$AIDEN_GO_TOOLCHAIN_PROVISION_PID" 2>/dev/null || true
    wait "$AIDEN_GO_TOOLCHAIN_PROVISION_PID" 2>/dev/null || true
  fi
  if declare -F cleanup_go_toolchain >/dev/null 2>&1; then
    cleanup_go_toolchain || true
  fi
  if [ -n "$docker_pid" ] && kill -0 "$docker_pid" 2>/dev/null; then
    kill -s "$signal_name" "$docker_pid" 2>/dev/null || true
    wait "$docker_pid" 2>/dev/null || true
  fi
  exit "$exit_status"
}
trap 'forward_signal_to_docker INT 130' INT
trap 'forward_signal_to_docker TERM 143' TERM
trap 'forward_signal_to_docker HUP 129' HUP

ensure_go_toolchain "$REPO_ROOT"

docker_env_args=()
for name in http_proxy https_proxy all_proxy no_proxy \
            HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY \
            GOPROXY GOSUMDB GOPRIVATE GONOSUMDB GONOPROXY; do
  if [ -n "${!name:-}" ]; then
    docker_env_args+=(-e "${name}=${!name}")
  fi
done

restore_image_output_ownership() {
  if [ "$profile" != image ] || [ "$(uname -s)" != Linux ]; then
    return 0
  fi

  local host_paths=()
  local container_paths=()
  local path
  local owner
  local ownership_restored=0
  for path in build .cache/rootfs-cli-tools overlay/oem overlay/userdata pico-sdk/output; do
    if [ -e "$REPO_ROOT/$path" ]; then
      host_paths+=("$REPO_ROOT/$path")
      container_paths+=("/home/$path")
    fi
  done
  owner="$(id -u):$(id -g)"
  if [ "${#host_paths[@]}" -gt 0 ]; then
    if [ "$(id -u)" -eq 0 ]; then
      if chown -hR "$owner" "${host_paths[@]}"; then
        ownership_restored=1
      else
        echo "warning: failed to restore image output ownership with chown" >&2
      fi
    elif command -v sudo >/dev/null 2>&1; then
      if sudo -n chown -hR "$owner" "${host_paths[@]}"; then
        ownership_restored=1
      else
        echo "warning: non-interactive sudo ownership cleanup failed; trying Docker ownership fallback" >&2
      fi
    else
      echo "warning: sudo is unavailable; trying Docker ownership fallback" >&2
    fi
    if [ "$ownership_restored" -eq 0 ] && ! docker run --platform linux/amd64 --rm -u 0:0 \
        -v "$REPO_ROOT:/home" \
        "$BUILD_CONTAINER_IMAGE" \
        chown -hR "$owner" "${container_paths[@]}"; then
      echo "warning: Docker ownership fallback failed; image outputs may remain root-owned" >&2
    fi
  fi
  if [ -d "$REPO_ROOT/.cache/rootfs-cli-tools/go-mod" ]; then
    if ! chmod -R u+w "$REPO_ROOT/.cache/rootfs-cli-tools/go-mod"; then
      echo "warning: failed to restore write permission for the Go module cache" >&2
    fi
  fi
  return 0
}

if [ "$profile" = image ]; then
  trap restore_image_output_ownership EXIT
fi

docker_run_args=(
  --platform linux/amd64
  --rm
  -v "$AIDEN_GO_ROOT_RESOLVED:/usr/local/go:ro"
  -v "$REPO_ROOT:/home"
  -w /home
  -e AIDEN_BUILD_CONTEXT=container
)

if [ "$profile" = binaries ]; then
  docker_run_args+=(-u "$(id -u):$(id -g)")
else
  if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
    SOURCE_DATE_EPOCH="${AIDEN_REPRODUCIBLE_IMAGE_EPOCH:-1}"
  fi
  if ! [[ "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]]; then
    echo "SOURCE_DATE_EPOCH must be an unsigned Unix timestamp: $SOURCE_DATE_EPOCH" >&2
    exit 1
  fi
  export SOURCE_DATE_EPOCH

  docker_run_args+=(
    --privileged
    -u 0:0
    -e SOURCE_DATE_EPOCH
    -e TAR_OPTIONS=--no-same-owner
  )

  if [ -n "${OTA_PUBLIC_KEY_PATH:-}" ]; then
    case "$OTA_PUBLIC_KEY_PATH" in
      /*)
        ota_key_host_path="$OTA_PUBLIC_KEY_PATH"
        if [ ! -f "$ota_key_host_path" ]; then
          case "$OTA_PUBLIC_KEY_PATH" in
            /home/*) ota_key_host_path="$REPO_ROOT${OTA_PUBLIC_KEY_PATH#/home}" ;;
          esac
        fi
        ;;
      *) ota_key_host_path="$REPO_ROOT/$OTA_PUBLIC_KEY_PATH" ;;
    esac
    if [ ! -f "$ota_key_host_path" ]; then
      echo "OTA_PUBLIC_KEY_PATH is set but key is missing on host: $ota_key_host_path" >&2
      exit 1
    fi
    ota_key_host_path="$(cd "$(dirname "$ota_key_host_path")" && pwd)/$(basename "$ota_key_host_path")"
    ota_key_container_path="/run/aiden/ota_pubkey.pem"
    docker_run_args+=(
      -e "OTA_PUBLIC_KEY_PATH=$ota_key_container_path"
      -v "${ota_key_host_path}:${ota_key_container_path}:ro"
    )
  fi
fi

if [ "${#docker_env_args[@]}" -gt 0 ]; then
  docker_run_args+=("${docker_env_args[@]}")
fi
docker_run_args+=(
  "$BUILD_CONTAINER_IMAGE"
  /bin/bash -c 'export PATH="/usr/local/go/bin:$PATH"; exec "$@"' _
  "${container_command[@]}"
)

docker_status=0
docker run "${docker_run_args[@]}" &
docker_pid=$!
if wait "$docker_pid"; then
  docker_status=0
else
  docker_status=$?
fi
docker_pid=""
exit "$docker_status"
