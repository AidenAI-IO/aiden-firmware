#!/usr/bin/env bash
set -euo pipefail

YQ_MODULE="github.com/mikefarah/yq/v4@v4.53.3"
FQ_MODULE="github.com/wader/fq@v0.17.0"
RIPGREP_VERSION="15.2.0"
RIPGREP_TARGET="armv7-unknown-linux-musleabihf"
RIPGREP_ARCHIVE="ripgrep-${RIPGREP_VERSION}-${RIPGREP_TARGET}.tar.gz"
RIPGREP_SHA256="0332b481aa007969a54d5c19e793208e73405c48d38f226bdee56b9ed085cdde"
RIPGREP_URL="https://github.com/BurntSushi/ripgrep/releases/download/${RIPGREP_VERSION}/${RIPGREP_ARCHIVE}"

usage() {
  cat >&2 <<'USAGE'
Usage:
  build_rootfs_cli_tools.sh --output-dir DIR [--cache-dir DIR]

Build pinned fq and yq releases for Linux ARMv7 and fetch the pinned official
ripgrep ARMv7 static release. The output contains fq, yq, rg, and a SHA256
manifest consumed by stage_rootfs_cli_tools.sh.
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

output_dir=""
cache_dir=""

while [ "$#" -gt 0 ]; do
  case "$1" in
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

[ -n "$output_dir" ] || die "missing --output-dir"
if [ -z "$cache_dir" ]; then
  cache_dir="$(dirname "$output_dir")/.cache/rootfs-cli-tools"
fi

command -v go >/dev/null 2>&1 || die "Go 1.26.0 is required in PATH"
command -v file >/dev/null 2>&1 || die "file is required to verify target architecture"
go_version="$(go version)"
case "$go_version" in
  *" go1.26.0 "*) ;;
  *) die "Go 1.26.0 is required, got: $go_version" ;;
esac

mkdir -p "$output_dir" "$cache_dir"
output_dir="$(cd "$output_dir" && pwd)"
cache_dir="$(cd "$cache_dir" && pwd)"
work_dir="$(mktemp -d "${output_dir}.tmp.XXXXXX")"
archive_tmp=""
cleanup() {
  rm -rf "$work_dir"
  if [ -n "$archive_tmp" ]; then
    rm -f "$archive_tmp"
  fi
}
trap cleanup EXIT
mkdir -p "$work_dir/bin" "$work_dir/rg" "$work_dir/go-path"

export GOCACHE="${GOCACHE:-$cache_dir/go-build}"
export GOMODCACHE="${GOMODCACHE:-$cache_dir/go-mod}"
export GOPATH="${GOPATH:-$cache_dir/go}"
export GOTOOLCHAIN=local
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOPATH"

build_go_tool() {
  local module="$1"

  CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 GOPATH="$work_dir/go-path" \
    go install -trimpath -buildvcs=false -ldflags="-s -w -buildid=" "$module"
}

printf 'Building rootfs CLI tools with %s\n' "$go_version"
build_go_tool "$YQ_MODULE"
build_go_tool "$FQ_MODULE"
cp "$work_dir/go-path/bin/linux_arm/yq" "$work_dir/bin/yq"
cp "$work_dir/go-path/bin/linux_arm/fq" "$work_dir/bin/fq"

archive_path="$cache_dir/$RIPGREP_ARCHIVE"
archive_sha=""
if [ -f "$archive_path" ]; then
  archive_sha="$(sha256_file "$archive_path")"
fi
if [ "$archive_sha" != "$RIPGREP_SHA256" ]; then
  rm -f "$archive_path"
  archive_tmp="${archive_path}.tmp.$$"
  rm -f "$archive_tmp"
  printf 'Downloading %s\n' "$RIPGREP_URL"
  download_file "$RIPGREP_URL" "$archive_tmp"
  archive_sha="$(sha256_file "$archive_tmp")"
  if [ "$archive_sha" != "$RIPGREP_SHA256" ]; then
    rm -f "$archive_tmp"
    die "ripgrep archive checksum mismatch: expected $RIPGREP_SHA256, got $archive_sha"
  fi
  mv "$archive_tmp" "$archive_path"
  archive_tmp=""
fi

tar -xzf "$archive_path" -C "$work_dir/rg"
rg_source="$work_dir/rg/ripgrep-${RIPGREP_VERSION}-${RIPGREP_TARGET}/rg"
[ -f "$rg_source" ] || die "ripgrep archive is missing $rg_source"
cp "$rg_source" "$work_dir/bin/rg"

for tool in fq yq rg; do
  tool_path="$work_dir/bin/$tool"
  [ -f "$tool_path" ] || die "build did not produce $tool_path"
  chmod 0755 "$tool_path"
  verify_arm32_elf "$tool" "$tool_path"
done

rm -f "$output_dir/fq" "$output_dir/yq" "$output_dir/rg" "$output_dir/manifest.sha256" "$output_dir/versions.txt"
for tool in fq yq rg; do
  cp "$work_dir/bin/$tool" "$output_dir/$tool"
  chmod 0755 "$output_dir/$tool"
  printf '%s  %s\n' "$(sha256_file "$output_dir/$tool")" "$tool" >> "$output_dir/manifest.sha256"
done
cat > "$output_dir/versions.txt" <<EOF
fq v0.17.0
yq v4.53.3
rg ${RIPGREP_VERSION}
target linux/arm/v7
EOF

printf 'Built rootfs CLI bundle in %s\n' "$output_dir"
ls -lh "$output_dir/fq" "$output_dir/yq" "$output_dir/rg"
