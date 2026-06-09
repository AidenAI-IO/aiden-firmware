#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage:
  compress_release_images.sh \
    --image-dir DIR \
    --assets 'FILE ...' \
    [--output FILE]

Compress .img release assets into .img.tar.gz archives.

Outputs:
  upload_assets='FILE ...'
USAGE
}

log() {
  printf '%s\n' "$*" >&2
}

die() {
  log "compress_release_images.sh: $*"
  exit 1
}

image_dir=""
assets=""
output="${GITHUB_OUTPUT:-}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --image-dir)
      image_dir="${2:-}"
      shift 2
      ;;
    --assets)
      assets="${2:-}"
      shift 2
      ;;
    --output)
      output="${2:-}"
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

[ -n "$image_dir" ] || die "missing --image-dir"
[ -n "$assets" ] || die "missing --assets"
[ -d "$image_dir" ] || die "missing image directory: $image_dir"
command -v tar >/dev/null 2>&1 || die "tar is required"
if ! command -v python3 >/dev/null 2>&1 && ! command -v gzip >/dev/null 2>&1; then
  die "python3 or gzip is required"
fi

emit_output() {
  local key="$1"
  local value="$2"

  if [ -n "$output" ]; then
    printf '%s=%s\n' "$key" "$value" >> "$output"
  else
    printf '%s=%s\n' "$key" "$value"
  fi
}

create_tar_gz() {
  local source="$1"
  local archive="$2"
  local entry_name="$3"
  local mtime="${SOURCE_DATE_EPOCH:-0}"
  local tmp_archive="${archive}.tmp.$$"

  rm -f "$tmp_archive"
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$source" "$tmp_archive" "$entry_name" "$mtime" <<'PY'
import gzip
import os
import sys
import tarfile

source, archive, entry_name, raw_mtime = sys.argv[1:]
try:
    mtime = int(raw_mtime)
except ValueError:
    mtime = 0

size = os.path.getsize(source)
info = tarfile.TarInfo(entry_name)
info.size = size
info.mtime = mtime
info.mode = 0o644
info.uid = 0
info.gid = 0
info.uname = ""
info.gname = ""

with open(source, "rb") as source_file, open(archive, "wb") as raw_file:
    with gzip.GzipFile(filename="", mode="wb", fileobj=raw_file, compresslevel=9, mtime=mtime) as gzip_file:
        with tarfile.TarFile(fileobj=gzip_file, mode="w", format=tarfile.USTAR_FORMAT) as tar_file:
            tar_file.addfile(info, source_file)
PY
  else
    tar -cf - -C "$(dirname "$source")" "$entry_name" | gzip -n -9 > "$tmp_archive"
  fi

  mv "$tmp_archive" "$archive"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

sha256_stream() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  else
    shasum -a 256 | awk '{print $1}'
  fi
}

archive_matches_source() {
  local source="$1"
  local archive="$2"
  local entry_name="$3"
  local source_sha archive_sha

  source_sha="$(sha256_file "$source")" || return 1
  if ! archive_sha="$(tar -xOzf "$archive" "$entry_name" 2>/dev/null | sha256_stream)"; then
    return 1
  fi

  [ "$source_sha" = "$archive_sha" ]
}

compressed_assets=()
for asset in $assets; do
  case "$asset" in
    ''|*/*)
      die "asset names must be release asset basenames: $asset"
      ;;
  esac

  case "$asset" in
    *.img)
      source_path="$image_dir/$asset"
      archive_name="$asset.tar.gz"
      archive_path="$image_dir/$archive_name"
      [ -f "$source_path" ] || die "missing image asset: $source_path"

      if [ -f "$archive_path" ] && archive_matches_source "$source_path" "$archive_path" "$asset"; then
        log "Compressed image is up to date: $archive_name"
      else
        log "Compressing release image: $asset -> $archive_name"
        create_tar_gz "$source_path" "$archive_path" "$asset"
      fi

      compressed_assets+=("$archive_name")
      ;;
    *.img.tar.gz)
      [ -f "$image_dir/$asset" ] || die "missing compressed image asset: $image_dir/$asset"
      compressed_assets+=("$asset")
      ;;
    *)
      [ -f "$image_dir/$asset" ] || die "missing release asset: $image_dir/$asset"
      compressed_assets+=("$asset")
      ;;
  esac
done

IFS=' '
emit_output "upload_assets" "${compressed_assets[*]}"
