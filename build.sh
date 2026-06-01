#!/usr/bin/env bash
set -euo pipefail

GO_VERSION="1.26.0"
GO_DIST="linux-amd64"
GO_TARBALL="go${GO_VERSION}.${GO_DIST}.tar.gz"
GO_TARBALL_SHA256="aac1b08a0fb0c4e0a7c1555beb7b59180b05dfc5a3d62e40e9de90cd42f88235"
GO_TOOLCHAIN_CACHE="${AIDEN_GO_TOOLCHAIN_CACHE:-$(pwd)/.toolchains}"
GO_ROOT="${GO_TOOLCHAIN_CACHE}/go${GO_VERSION}.${GO_DIST}"
GO_TARBALL_PATH="${GO_TOOLCHAIN_CACHE}/${GO_TARBALL}"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "sha256sum or shasum is required to verify ${GO_TARBALL}" >&2
    exit 1
  fi
}

verify_go_tarball() {
  local actual
  actual="$(sha256_file "$1")"
  if [ "$actual" != "$GO_TARBALL_SHA256" ]; then
    echo "Go toolchain checksum mismatch for $1" >&2
    echo "expected: $GO_TARBALL_SHA256" >&2
    echo "actual:   $actual" >&2
    exit 1
  fi
}

download_go_tarball() {
  local tmp="${GO_TARBALL_PATH}.tmp.$$"
  mkdir -p "$GO_TOOLCHAIN_CACHE"
  rm -f "$tmp"
  echo "Downloading Go ${GO_VERSION} ${GO_DIST} toolchain..."
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --connect-timeout 20 -o "$tmp" "https://go.dev/dl/${GO_TARBALL}"
  elif command -v wget >/dev/null 2>&1; then
    wget -O "$tmp" "https://go.dev/dl/${GO_TARBALL}"
  else
    echo "curl or wget is required to download https://go.dev/dl/${GO_TARBALL}" >&2
    exit 1
  fi
  verify_go_tarball "$tmp"
  mv "$tmp" "$GO_TARBALL_PATH"
}

ensure_go_toolchain() {
  if [ -x "$GO_ROOT/bin/go" ] && [ -f "$GO_ROOT/VERSION" ] && grep -qx "go${GO_VERSION}" "$GO_ROOT/VERSION"; then
    return 0
  fi

  if [ ! -f "$GO_TARBALL_PATH" ]; then
    download_go_tarball
  else
    verify_go_tarball "$GO_TARBALL_PATH"
  fi

  local extract_dir="${GO_TOOLCHAIN_CACHE}/.go${GO_VERSION}.${GO_DIST}.$$"
  rm -rf "$extract_dir" "$GO_ROOT"
  mkdir -p "$extract_dir"
  tar -C "$extract_dir" -xzf "$GO_TARBALL_PATH"
  mv "$extract_dir/go" "$GO_ROOT"
  rmdir "$extract_dir"
}

ensure_go_toolchain

docker_env_args=()
for name in http_proxy https_proxy all_proxy no_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY GOPROXY GOSUMDB GOPRIVATE GONOSUMDB GONOPROXY; do
  if [ -n "${!name:-}" ]; then
    docker_env_args+=(-e "${name}=${!name}")
  fi
done

docker_run_args=(
  --platform linux/amd64 \
  -u "$(id -u):$(id -g)" \
  --rm \
  -v "${GO_ROOT}:/usr/local/go:ro" \
  -v "$(pwd):/home" \
  -w /home \
  luckfoxtech/luckfox_pico:1.0 \
  /bin/bash -c 'export PATH="/usr/local/go/bin:$PATH"; exec "$@"' _ ./_build.sh
)
if [ "${#docker_env_args[@]}" -gt 0 ]; then
  docker_run_args=( "${docker_run_args[@]:0:4}" "${docker_env_args[@]}" "${docker_run_args[@]:4}" )
fi

docker run "${docker_run_args[@]}"
