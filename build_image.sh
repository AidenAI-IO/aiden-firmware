#!/usr/bin/env bash
set -euo pipefail

docker_go_args=()
cached_linux_go_root="${AIDEN_GO_ROOT:-$(pwd)/.toolchains/go1.26.0.linux-amd64}"
if command -v go >/dev/null 2>&1; then
  host_goroot=$(go env GOROOT)
  host_goos=$(go env GOHOSTOS)
  host_goarch=$(go env GOHOSTARCH)
  if [ -d "$host_goroot" ] && [ "$host_goos" = linux ] && [ "$host_goarch" = amd64 ]; then
    docker_go_args=(-v "${host_goroot}:/usr/local/go:ro")
  elif [ -x "$cached_linux_go_root/bin/go" ] && \
       [ -f "$cached_linux_go_root/VERSION" ] && \
       grep -qx 'go1.26.0' "$cached_linux_go_root/VERSION"; then
    docker_go_args=(-v "${cached_linux_go_root}:/usr/local/go:ro")
  else
    echo "Host Go toolchain is not linux/amd64 and cached Go 1.26.0 is unavailable: $cached_linux_go_root" >&2
    echo "Run ./build.sh once to provision the pinned Linux toolchain before building an image." >&2
    exit 1
  fi
elif [ -x "$cached_linux_go_root/bin/go" ] && \
     [ -f "$cached_linux_go_root/VERSION" ] && \
     grep -qx 'go1.26.0' "$cached_linux_go_root/VERSION"; then
  docker_go_args=(-v "${cached_linux_go_root}:/usr/local/go:ro")
else
  echo "Go is unavailable and cached Go 1.26.0 is missing: $cached_linux_go_root" >&2
  echo "Run ./build.sh once to provision the pinned Linux toolchain before building an image." >&2
  exit 1
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

# Forward Go module proxy and HTTP proxy configuration into the build
# container. The Go build runs inside this container, so a GOPROXY exported
# only on the host (e.g. the workflow's "Configure Go proxy" step) never
# reaches it; without this the build falls back to the default proxy.golang.org
# and fails on restricted networks. Only set variables are forwarded so unset
# ones do not clobber the container defaults with empty values.
docker_proxy_args=()
for name in http_proxy https_proxy all_proxy no_proxy \
            HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY \
            GOPROXY GOSUMDB GOPRIVATE GONOSUMDB GONOPROXY; do
  if [ -n "${!name:-}" ]; then
    docker_proxy_args+=(-e "${name}=${!name}")
  fi
done

docker_command=("./_build_image.sh")
if [ "$#" -gt 0 ]; then
  docker_command=("$@")
fi

if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
  # Keep image metadata stable without relying on every package treating epoch 0
  # as truthy. Callers can still set SOURCE_DATE_EPOCH explicitly, including 0.
  SOURCE_DATE_EPOCH="${AIDEN_REPRODUCIBLE_IMAGE_EPOCH:-1}"
fi
if ! [[ "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]]; then
  echo "SOURCE_DATE_EPOCH must be an unsigned Unix timestamp: $SOURCE_DATE_EPOCH" >&2
  exit 1
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
if [ "${#docker_proxy_args[@]}" -gt 0 ]; then
  docker_run_args+=("${docker_proxy_args[@]}")
fi
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
