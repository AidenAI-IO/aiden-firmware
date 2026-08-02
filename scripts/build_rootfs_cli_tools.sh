#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_CATALOG="$SCRIPT_DIR/rootfs_cli_tools.catalog"
CATALOG_LIB="$SCRIPT_DIR/rootfs_cli_tool_catalog.sh"

# shellcheck source=/dev/null
source "$CATALOG_LIB"

usage() {
  cat >&2 <<'USAGE'
Usage:
  build_rootfs_cli_tools.sh [--catalog FILE] --output-dir DIR [--cache-dir DIR]

Build or download the pinned Linux ARMv7 tools declared in the rootfs CLI tool
catalog. The output contains each catalog tool, a SHA256 manifest, and version
metadata consumed by stage_rootfs_cli_tools.sh.
USAGE
}

die() {
  printf 'build_rootfs_cli_tools.sh: %s\n' "$*" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "sha256sum or shasum is required"
  fi
}

download_file() {
  local url="$1"
  local output="$2"

  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --connect-timeout 20 -o "$output" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -O "$output" "$url"
  else
    die "curl or wget is required to download $url"
  fi
}

verify_arm32_elf() {
  local name="$1"
  local path="$2"
  local description

  description="$(file -b "$path")"
  case "$description" in
    *"ELF 32-bit LSB"*"ARM"*) ;;
    *) die "$name is not an ARM32 ELF executable: $description" ;;
  esac
}

catalog_path="$DEFAULT_CATALOG"
output_dir=""
cache_dir=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --catalog)
      catalog_path="${2:-}"
      shift 2
      ;;
    --output-dir)
      output_dir="${2:-}"
      shift 2
      ;;
    --cache-dir)
      cache_dir="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      die "unknown argument: $1"
      ;;
  esac
done

[ -n "$catalog_path" ] || die "missing --catalog value"
[ -n "$output_dir" ] || die "missing --output-dir"
catalog_records="$(rootfs_cli_catalog_records "$catalog_path")" || exit 1

if [ -z "$cache_dir" ]; then
  cache_dir="$(dirname "$output_dir")/.cache/rootfs-cli-tools"
fi

needs_go=0
while IFS='|' read -r name version kind source target source_sha256 artifact_path strip_policy; do
  if [ "$kind" = "go" ]; then
    needs_go=1
  fi
done <<< "$catalog_records"

if [ "$needs_go" -eq 1 ]; then
  command -v go >/dev/null 2>&1 || die "Go 1.26.0 is required in PATH"
  go_version="$(go version)"
  case "$go_version" in
    *" go1.26.0 "*) ;;
    *) die "Go 1.26.0 is required, got: $go_version" ;;
  esac
fi
command -v file >/dev/null 2>&1 || die "file is required to verify target architecture"

mkdir -p "$output_dir" "$cache_dir"
output_dir="$(cd "$output_dir" && pwd)"
cache_dir="$(cd "$cache_dir" && pwd)"
work_dir="$(mktemp -d "${output_dir}.tmp.XXXXXX")"
cleanup() {
  rm -rf "$work_dir"
}
trap cleanup EXIT
mkdir -p "$work_dir/bin" "$work_dir/archives" "$work_dir/go-path"

if [ "$needs_go" -eq 1 ]; then
  export GOCACHE="${GOCACHE:-$cache_dir/go-build}"
  export GOMODCACHE="${GOMODCACHE:-$cache_dir/go-mod}"
  export GOPATH="${GOPATH:-$cache_dir/go}"
  export GOTOOLCHAIN=local
  mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOPATH"
  printf 'Building rootfs CLI tools with %s\n' "$go_version"
fi

build_go_tool() {
  local name="$1"
  local module="$2"
  local target="$3"
  local built_path

  case "$target" in
    linux/arm/v7)
      CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 GOPATH="$work_dir/go-path" \
        go install -trimpath -buildvcs=false -ldflags="-s -w -buildid=" "$module"
      built_path="$work_dir/go-path/bin/linux_arm/$name"
      ;;
    *) die "unsupported Go target for $name: $target" ;;
  esac

  [ -f "$built_path" ] || die "Go build did not produce $built_path"
  cp "$built_path" "$work_dir/bin/$name"
}

build_archive_tool() {
  local name="$1"
  local url="$2"
  local expected_sha="$3"
  local artifact_path="$4"
  local archive_name archive_path archive_sha download_tmp extract_dir source_path

  archive_name="${url##*/}"
  [ -n "$archive_name" ] || die "archive URL has no filename for $name: $url"
  archive_path="$cache_dir/$archive_name"
  archive_sha=""
  if [ -f "$archive_path" ]; then
    archive_sha="$(sha256_file "$archive_path")"
  fi
  if [ "$archive_sha" != "$expected_sha" ]; then
    rm -f "$archive_path"
    download_tmp="$work_dir/$archive_name"
    printf 'Downloading %s\n' "$url"
    download_file "$url" "$download_tmp"
    archive_sha="$(sha256_file "$download_tmp")"
    if [ "$archive_sha" != "$expected_sha" ]; then
      die "$name archive checksum mismatch: expected $expected_sha, got $archive_sha"
    fi
    mv "$download_tmp" "$archive_path"
  fi

  extract_dir="$work_dir/archives/$name"
  mkdir -p "$extract_dir"
  tar -xzf "$archive_path" -C "$extract_dir"
  source_path="$extract_dir/$artifact_path"
  [ -f "$source_path" ] || die "$name archive is missing $artifact_path"
  cp "$source_path" "$work_dir/bin/$name"
}

while IFS='|' read -r name version kind source target source_sha256 artifact_path strip_policy; do
  case "$kind" in
    go) build_go_tool "$name" "$source" "$target" ;;
    archive) build_archive_tool "$name" "$source" "$source_sha256" "$artifact_path" ;;
  esac

  tool_path="$work_dir/bin/$name"
  [ -f "$tool_path" ] || die "build did not produce $tool_path"
  chmod 0755 "$tool_path"
  verify_arm32_elf "$name" "$tool_path"
done <<< "$catalog_records"

# Remove files managed by the previous and current bundle manifests without
# disturbing unrelated content if a caller reuses an existing directory.
if [ -f "$output_dir/manifest.sha256" ]; then
  while read -r old_sha old_name extra; do
    case "$old_name" in
      ""|[!A-Za-z0-9]*|*[!A-Za-z0-9._+-]*) continue ;;
    esac
    rm -f "$output_dir/$old_name"
  done < "$output_dir/manifest.sha256"
fi
while IFS='|' read -r name version kind source target source_sha256 artifact_path strip_policy; do
  rm -f "$output_dir/$name"
done <<< "$catalog_records"
rm -f "$output_dir/manifest.sha256" "$output_dir/versions.txt"

while IFS='|' read -r name version kind source target source_sha256 artifact_path strip_policy; do
  cp "$work_dir/bin/$name" "$output_dir/$name"
  chmod 0755 "$output_dir/$name"
  printf '%s  %s\n' "$(sha256_file "$output_dir/$name")" "$name" >> "$output_dir/manifest.sha256"
  printf '%s %s %s %s\n' "$name" "$version" "$target" "$strip_policy" >> "$output_dir/versions.txt"
done <<< "$catalog_records"

printf 'Built rootfs CLI bundle in %s\n' "$output_dir"
while IFS='|' read -r name version kind source target source_sha256 artifact_path strip_policy; do
  ls -lh "$output_dir/$name"
done <<< "$catalog_records"
