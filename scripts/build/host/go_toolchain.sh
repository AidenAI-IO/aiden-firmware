#!/usr/bin/env bash

GO_VERSION="1.26.0"
GO_DIST="linux-amd64"
GO_TARBALL="go${GO_VERSION}.${GO_DIST}.tar.gz"
GO_TARBALL_SHA256="aac1b08a0fb0c4e0a7c1555beb7b59180b05dfc5a3d62e40e9de90cd42f88235"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "sha256sum or shasum is required to verify ${GO_TARBALL}" >&2
    return 1
  fi
}

verify_go_tarball() {
  local actual
  actual="$(sha256_file "$1")"
  if [ "$actual" != "$GO_TARBALL_SHA256" ]; then
    echo "Go toolchain checksum mismatch for $1" >&2
    echo "expected: $GO_TARBALL_SHA256" >&2
    echo "actual:   $actual" >&2
    return 1
  fi
}

go_binary_version() {
  local binary="$1"
  local version=""

  if command -v go >/dev/null 2>&1; then
    version="$(go version "$binary" 2>/dev/null || true)"
  fi
  if [ -z "$version" ]; then
    version="$("$binary" version 2>/dev/null || true)"
  fi
  printf '%s\n' "$version"
}

go_toolchain_valid() {
  local root="$1"
  local binary="$root/bin/go"
  local version_report
  local target_report=""

  [ -x "$binary" ] || return 1
  [ -f "$root/VERSION" ] || return 1
  grep -qx "go${GO_VERSION}" "$root/VERSION" || return 1

  version_report="$(go_binary_version "$binary")"
  if [ -n "$version_report" ]; then
    case "$version_report" in
      "go version go${GO_VERSION} linux/amd64"|*": go${GO_VERSION}") ;;
      *) return 1 ;;
    esac
  fi

  if command -v file >/dev/null 2>&1; then
    target_report="$(file -b "$binary" 2>/dev/null || true)"
    case "$target_report" in
      *"ELF 64-bit"*"x86-64"*|*"ELF 64-bit"*"x86_64"*) return 0 ;;
    esac
  fi

  [ "$version_report" = "go version go${GO_VERSION} linux/amd64" ]
}

download_go_tarball() {
  local tarball_path="$1"
  local tmp="${tarball_path}.tmp.$$"

  mkdir -p "$(dirname "$tarball_path")"
  rm -f "$tmp"
  echo "Downloading Go ${GO_VERSION} ${GO_DIST} toolchain..."
  if command -v curl >/dev/null 2>&1; then
    if ! curl -fL --retry 3 --connect-timeout 20 -o "$tmp" "https://go.dev/dl/${GO_TARBALL}"; then
      rm -f "$tmp"
      return 1
    fi
  elif command -v wget >/dev/null 2>&1; then
    if ! wget -O "$tmp" "https://go.dev/dl/${GO_TARBALL}"; then
      rm -f "$tmp"
      return 1
    fi
  else
    echo "curl or wget is required to download https://go.dev/dl/${GO_TARBALL}" >&2
    return 1
  fi
  if ! verify_go_tarball "$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  mv "$tmp" "$tarball_path"
}

acquire_go_toolchain_lock() {
  local lock_dir="$1"
  local go_root="$2"
  local timeout="${AIDEN_GO_TOOLCHAIN_LOCK_TIMEOUT_SECONDS:-300}"
  local started_at="$SECONDS"
  local current_host owner_host owner_pid

  case "$timeout" in
    ''|*[!0-9]*)
      echo "AIDEN_GO_TOOLCHAIN_LOCK_TIMEOUT_SECONDS must be an unsigned integer: $timeout" >&2
      return 1
      ;;
  esac
  current_host="$(hostname)"

  while ! mkdir "$lock_dir" 2>/dev/null; do
    if go_toolchain_valid "$go_root"; then
      return 2
    fi

    owner_host=""
    owner_pid=""
    if [ ! -e "$lock_dir/owner" ]; then
      sleep 1
      if [ ! -e "$lock_dir/owner" ] && rmdir "$lock_dir" 2>/dev/null; then
        continue
      fi
    fi
    if read -r owner_host owner_pid < "$lock_dir/owner" 2>/dev/null && \
       [ "$owner_host" = "$current_host" ] && \
       [ -n "$owner_pid" ] && ! kill -0 "$owner_pid" 2>/dev/null; then
      rm -f "$lock_dir/owner"
      rmdir "$lock_dir" 2>/dev/null || true
      continue
    fi

    if [ $((SECONDS - started_at)) -ge "$timeout" ]; then
      echo "Timed out waiting for Go toolchain cache lock: $lock_dir" >&2
      return 1
    fi
    sleep 1
  done

  if ! printf '%s %s\n' "$current_host" "$$" > "$lock_dir/owner"; then
    rmdir "$lock_dir" 2>/dev/null || true
    return 1
  fi
}

ensure_go_toolchain() {
  local repo_root="$1"
  local cache_root="${AIDEN_GO_TOOLCHAIN_CACHE:-$repo_root/.toolchains}"
  local go_root
  local host_go_root
  local tarball_path
  local lock_dir
  local extract_dir
  local lock_status=0
  local provision_status=0

  if [ -n "${AIDEN_GO_ROOT:-}" ]; then
    go_root="$AIDEN_GO_ROOT"
    case "$go_root" in
      /*) ;;
      *) go_root="$repo_root/$go_root" ;;
    esac
    if ! go_toolchain_valid "$go_root"; then
      echo "AIDEN_GO_ROOT is not a valid go${GO_VERSION} linux/amd64 toolchain: $go_root" >&2
      return 1
    fi
    AIDEN_GO_ROOT_RESOLVED="$(cd "$go_root" && pwd)"
    return 0
  fi

  # Reuse a verified host installation (for example, actions/setup-go) before
  # falling back to the managed cache. This keeps CI builds offline when the
  # runner already provides the pinned toolchain.
  if command -v go >/dev/null 2>&1; then
    host_go_root="$(go env GOROOT 2>/dev/null || true)"
    case "$host_go_root" in
      /*) ;;
      '') host_go_root='' ;;
      *) host_go_root="$repo_root/$host_go_root" ;;
    esac
    if [ -n "$host_go_root" ] && go_toolchain_valid "$host_go_root"; then
      AIDEN_GO_ROOT_RESOLVED="$(cd "$host_go_root" && pwd)"
      return 0
    fi
  fi

  case "$cache_root" in
    /*) ;;
    *) cache_root="$repo_root/$cache_root" ;;
  esac
  mkdir -p "$cache_root"
  cache_root="$(cd "$cache_root" && pwd)"
  tarball_path="$cache_root/$GO_TARBALL"

  go_root="$cache_root/go${GO_VERSION}.${GO_DIST}"
  if go_toolchain_valid "$go_root"; then
    AIDEN_GO_ROOT_RESOLVED="$go_root"
    return 0
  fi

  lock_dir="$cache_root/.go${GO_VERSION}.${GO_DIST}.lock"
  acquire_go_toolchain_lock "$lock_dir" "$go_root" || lock_status=$?
  if [ "$lock_status" -eq 2 ] && go_toolchain_valid "$go_root"; then
    AIDEN_GO_ROOT_RESOLVED="$go_root"
    return 0
  fi
  if [ "$lock_status" -ne 0 ]; then
    return "$lock_status"
  fi

  (
    extract_dir="$cache_root/.go${GO_VERSION}.${GO_DIST}.$$"
    cleanup_provisioning() {
      if [ -n "$extract_dir" ]; then
        rm -rf "$extract_dir"
      fi
      rm -f "$lock_dir/owner"
      rmdir "$lock_dir" 2>/dev/null || true
    }
    trap cleanup_provisioning EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    trap 'exit 129' HUP

    if go_toolchain_valid "$go_root"; then
      exit 0
    fi
    if [ -f "$tarball_path" ] && ! verify_go_tarball "$tarball_path"; then
      rm -f "$tarball_path"
    fi
    if [ ! -f "$tarball_path" ]; then
      download_go_tarball "$tarball_path"
    fi

    rm -rf "$extract_dir"
    mkdir -p "$extract_dir"
    tar -C "$extract_dir" -xzf "$tarball_path"
    rm -rf "$go_root"
    mv "$extract_dir/go" "$go_root"
    extract_dir=""
  ) || provision_status=$?
  if [ "$provision_status" -ne 0 ]; then
    return "$provision_status"
  fi

  if ! go_toolchain_valid "$go_root"; then
    echo "Provisioned Go toolchain is not go${GO_VERSION} linux/amd64: $go_root" >&2
    return 1
  fi
  AIDEN_GO_ROOT_RESOLVED="$go_root"
}
