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
command -v python3 >/dev/null 2>&1 || die "python3 is required"

source_date_epoch="${SOURCE_DATE_EPOCH:-1}"
case "$source_date_epoch" in
  ''|*[!0-9]*)
    die "SOURCE_DATE_EPOCH must be an unsigned Unix timestamp: $source_date_epoch"
    ;;
esac

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
  local mtime="$source_date_epoch"
  local tmp_archive="${archive}.tmp.$$"

  rm -f "$tmp_archive"
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

  mv "$tmp_archive" "$archive"
}

archive_matches_source() {
  local source="$1"
  local archive="$2"
  local entry_name="$3"
  local mtime="$source_date_epoch"

  python3 - "$source" "$archive" "$entry_name" "$mtime" <<'PY'
import gzip
import hashlib
import os
import sys
import tarfile

source, archive, entry_name, raw_mtime = sys.argv[1:]
try:
    expected_mtime = int(raw_mtime)
except ValueError:
    sys.exit(1)

def hash_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()

def hash_reader(reader):
    digest = hashlib.sha256()
    for chunk in iter(lambda: reader.read(1024 * 1024), b""):
        digest.update(chunk)
    return digest.hexdigest()

try:
    with open(archive, "rb") as raw_file:
        header = raw_file.read(10)
    if len(header) != 10 or header[:2] != b"\x1f\x8b" or header[2] != 8:
        sys.exit(1)
    if header[3] != 0:
        sys.exit(1)
    if int.from_bytes(header[4:8], "little") != expected_mtime:
        sys.exit(1)

    with tarfile.open(archive, mode="r:gz") as tar_file:
        info = tar_file.next()
        if info is None:
            sys.exit(1)
        if (
            info.name != entry_name
            or not info.isfile()
            or info.size != os.path.getsize(source)
            or int(info.mtime) != expected_mtime
            or info.mode != 0o644
            or info.uid != 0
            or info.gid != 0
            or info.uname != ""
            or info.gname != ""
        ):
            sys.exit(1)

        extracted = tar_file.extractfile(info)
        if extracted is None:
            sys.exit(1)
        archive_sha = hash_reader(extracted)
        if tar_file.next() is not None:
            sys.exit(1)

    if hash_file(source) != archive_sha:
        sys.exit(1)
except (OSError, tarfile.TarError, EOFError):
    sys.exit(1)
PY
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
