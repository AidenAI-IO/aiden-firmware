#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compress_script="$repo_root/scripts/compress_release_images.sh"
if [ ! -x "$compress_script" ]; then
  echo "compress_release_images.sh is missing or not executable: $compress_script" >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

image_dir="$tmp_dir/images"
mkdir -p "$image_dir"

printf 'boot a image\n' > "$image_dir/boot_a.img"
printf 'boot b image\n' > "$image_dir/boot_b.img"
printf 'rootfs image\n' > "$image_dir/rootfs.img"
printf 'update image\n' > "$image_dir/update.img"
printf '{"version":"test"}\n' > "$image_dir/manifest.json"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

assert_archive_mtime() {
  local archive="$1"
  local entry_name="$2"
  local expected_mtime="$3"

  python3 - "$archive" "$entry_name" "$expected_mtime" <<'PY'
import gzip
import sys
import tarfile

archive, entry_name, raw_expected_mtime = sys.argv[1:]
expected_mtime = int(raw_expected_mtime)

with open(archive, "rb") as raw_file:
    header = raw_file.read(10)
if len(header) != 10 or header[:2] != b"\x1f\x8b" or header[2] != 8:
    raise SystemExit(f"{archive} is not a valid gzip archive")
gzip_mtime = int.from_bytes(header[4:8], "little")
if gzip_mtime != expected_mtime:
    raise SystemExit(
        f"{archive} gzip mtime is {gzip_mtime}, want {expected_mtime}"
    )

with gzip.open(archive, "rb") as gzip_file:
    with tarfile.open(fileobj=gzip_file, mode="r:") as tar_file:
        members = tar_file.getmembers()
if len(members) != 1 or members[0].name != entry_name:
    raise SystemExit(f"{archive} must contain exactly {entry_name}")
tar_mtime = int(members[0].mtime)
if tar_mtime != expected_mtime:
    raise SystemExit(
        f"{archive} tar mtime is {tar_mtime}, want {expected_mtime}"
    )
PY
}

output_value() {
  local key="$1"
  local file="$2"
  awk -v key="$key" '
    index($0, key "=") == 1 {
      sub("^[^=]*=", "")
      print
    }
  ' "$file" | tail -n 1
}

outputs_file="$tmp_dir/compress.outputs"
SOURCE_DATE_EPOCH=0 "$compress_script" \
  --image-dir "$image_dir" \
  --assets 'boot_a.img boot_b.img rootfs.img update.img manifest.json' \
  --output "$outputs_file"

upload_assets="$(output_value upload_assets "$outputs_file")"
if [ "$upload_assets" != "boot_a.img.tar.gz boot_b.img.tar.gz rootfs.img.tar.gz update.img.tar.gz manifest.json" ]; then
  echo "compressed upload list is wrong: $upload_assets" >&2
  exit 1
fi

for image in boot_a.img boot_b.img rootfs.img update.img; do
  archive="$image_dir/$image.tar.gz"
  if [ ! -f "$archive" ]; then
    echo "missing compressed archive: $archive" >&2
    exit 1
  fi

  if [ "$(tar -tzf "$archive")" != "$image" ]; then
    echo "archive $archive must contain exactly $image" >&2
    tar -tzf "$archive" >&2
    exit 1
  fi

  extracted="$tmp_dir/$image.extracted"
  tar -xOzf "$archive" "$image" > "$extracted"
  if [ "$(sha256_of "$extracted")" != "$(sha256_of "$image_dir/$image")" ]; then
    echo "archive $archive must preserve the original image bytes" >&2
    exit 1
  fi
done

if [ -f "$image_dir/manifest.json.tar.gz" ]; then
  echo "manifest.json must not be compressed as an image" >&2
  exit 1
fi

printf 'extra file\n' > "$image_dir/extra.txt"
tar -czf "$image_dir/boot_a.img.tar.gz" -C "$image_dir" boot_a.img extra.txt
SOURCE_DATE_EPOCH=0 "$compress_script" \
  --image-dir "$image_dir" \
  --assets 'boot_a.img' \
  --output "$outputs_file"

if [ "$(tar -tzf "$image_dir/boot_a.img.tar.gz")" != "boot_a.img" ]; then
  echo "compression script must refresh archives with extra entries" >&2
  tar -tzf "$image_dir/boot_a.img.tar.gz" >&2
  exit 1
fi

printf 'updated rootfs image\n' > "$image_dir/rootfs.img"
SOURCE_DATE_EPOCH=0 "$compress_script" \
  --image-dir "$image_dir" \
  --assets 'rootfs.img' \
  --output "$outputs_file"

tar -xOzf "$image_dir/rootfs.img.tar.gz" rootfs.img > "$tmp_dir/rootfs.updated.extracted"
if [ "$(sha256_of "$tmp_dir/rootfs.updated.extracted")" != "$(sha256_of "$image_dir/rootfs.img")" ]; then
  echo "compression script must refresh stale image archives" >&2
  exit 1
fi

SOURCE_DATE_EPOCH=0 "$compress_script" \
  --image-dir "$image_dir" \
  --assets 'update.img' \
  --output "$outputs_file"
assert_archive_mtime "$image_dir/update.img.tar.gz" update.img 0

SOURCE_DATE_EPOCH=1 "$compress_script" \
  --image-dir "$image_dir" \
  --assets 'update.img' \
  --output "$outputs_file"
assert_archive_mtime "$image_dir/update.img.tar.gz" update.img 1

echo "release image compression test passed"
