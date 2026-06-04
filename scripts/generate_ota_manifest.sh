#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/generate_ota_manifest.sh --version VERSION --channel CHANNEL --build-time RFC3339 --sign-key KEY.pem --image-dir DIR --output manifest.json

Generate a signed OTA manifest for slot-aware boot, oem, and rootfs images.

Required options:
  --version       Monotonic release version, for example 20260521-120000-abcdef0
  --channel       Release channel, for example stable
  --build-time    RFC3339 UTC build time, for example 2026-05-21T12:00:00Z
  --sign-key      Ed25519 private key PEM file
  --image-dir     Directory containing OTA partition images
  --output        Output manifest path
  --help          Show this help

Optional:
  --base-url      Base URL for direct asset downloads (e.g. https://example.com/firmware/v1.0.0)
                  If provided, full asset URLs will be embedded in the manifest

Required images:
  boot_a.img and boot_b.img are always required.
  For oem/rootfs, either NAME.img or both NAME_a.img and NAME_b.img are required.
USAGE
}

die() {
  printf 'generate_ota_manifest.sh: %s\n' "$*" >&2
  exit 1
}

version=""
channel=""
build_time=""
sign_key=""
image_dir=""
output=""
base_url=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --help)
      usage
      exit 0
      ;;
    --version)
      [ "$#" -ge 2 ] || die "--version requires a value"
      version="$2"
      shift 2
      ;;
    --channel)
      [ "$#" -ge 2 ] || die "--channel requires a value"
      channel="$2"
      shift 2
      ;;
    --build-time)
      [ "$#" -ge 2 ] || die "--build-time requires a value"
      build_time="$2"
      shift 2
      ;;
    --sign-key)
      [ "$#" -ge 2 ] || die "--sign-key requires a value"
      sign_key="$2"
      shift 2
      ;;
    --image-dir)
      [ "$#" -ge 2 ] || die "--image-dir requires a value"
      image_dir="$2"
      shift 2
      ;;
    --output)
      [ "$#" -ge 2 ] || die "--output requires a value"
      output="$2"
      shift 2
      ;;
    --base-url)
      [ "$#" -ge 2 ] || die "--base-url requires a value"
      base_url="$2"
      shift 2
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

[ -n "$version" ] || die "missing --version"
[ -n "$channel" ] || die "missing --channel"
[ -n "$build_time" ] || die "missing --build-time"
[ -n "$sign_key" ] || die "missing --sign-key"
[ -n "$image_dir" ] || die "missing --image-dir"
[ -n "$output" ] || die "missing --output"

command -v jq >/dev/null 2>&1 || die "jq is required"
command -v openssl >/dev/null 2>&1 || die "openssl is required"
[ -f "$sign_key" ] || die "missing signing key: $sign_key"
[ -d "$image_dir" ] || die "missing image directory: $image_dir"

case "$channel" in
  *[!A-Za-z0-9._-]*|'') die "invalid --channel: $channel" ;;
esac

case "$version" in
  *[!A-Za-z0-9._:-]*|'') die "invalid --version: $version" ;;
esac

if ! jq -n -e --arg build_time "$build_time" '$build_time | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$")' >/dev/null; then
  die "invalid --build-time: $build_time"
fi

file_size() {
  if stat -c%s "$1" >/dev/null 2>&1; then
    stat -c%s "$1"
  else
    stat -f%z "$1"
  fi
}

file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

asset_json() {
  local file="$1"
  local path="$image_dir/$file"
  [ -f "$path" ] || die "missing required image: $path"
  local size sha256
  size="$(file_size "$path")"
  sha256="$(file_sha256 "$path")"
  if [ -n "$base_url" ]; then
    local url="${base_url%/}/$file"
    jq -n \
      --arg name "$file" \
      --arg url "$url" \
      --argjson size "$size" \
      --arg sha256 "$sha256" \
      '{name:$name,url:$url,size:$size,sha256:$sha256}'
  else
    jq -n \
      --arg name "$file" \
      --argjson size "$size" \
      --arg sha256 "$sha256" \
      '{name:$name,size:$size,sha256:$sha256}'
  fi
}

part_json() {
  local part="$1"
  if [ "$part" = "boot" ]; then
    jq -n \
      --arg name "$part" \
      --argjson asset_a "$(asset_json boot_a.img)" \
      --argjson asset_b "$(asset_json boot_b.img)" \
      '{name:$name,asset_a:$asset_a,asset_b:$asset_b}'
    return
  fi

  if [ -f "$image_dir/${part}_a.img" ] || [ -f "$image_dir/${part}_b.img" ]; then
    [ -f "$image_dir/${part}_a.img" ] || die "missing required image: $image_dir/${part}_a.img"
    [ -f "$image_dir/${part}_b.img" ] || die "missing required image: $image_dir/${part}_b.img"
    jq -n \
      --arg name "$part" \
      --argjson asset_a "$(asset_json "${part}_a.img")" \
      --argjson asset_b "$(asset_json "${part}_b.img")" \
      '{name:$name,asset_a:$asset_a,asset_b:$asset_b}'
    return
  fi

  [ -f "$image_dir/${part}.img" ] || die "missing required image: $image_dir/${part}.img or ${part}_a.img/${part}_b.img"
  jq -n \
    --arg name "$part" \
    --argjson asset "$(asset_json "${part}.img")" \
    '{name:$name,asset:$asset}'
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

boot_part="$(part_json boot)"
oem_part="$(part_json oem)"
rootfs_part="$(part_json rootfs)"

unsigned="$tmpdir/unsigned.json"
canonical="$tmpdir/canonical.json"
signature="$tmpdir/signature.bin"

jq -n \
  --arg channel "$channel" \
  --arg version "$version" \
  --arg build_time "$build_time" \
  --argjson boot "$boot_part" \
  --argjson oem "$oem_part" \
  --argjson rootfs "$rootfs_part" \
  '{schema_version:1,channel:$channel,version:$version,build_time:$build_time,parts:[$boot,$oem,$rootfs],signature:{algorithm:"ed25519"}}' \
  > "$unsigned"

jq -cS 'del(.signature.value)' "$unsigned" | tr -d '\n' > "$canonical"
openssl pkeyutl -sign -rawin -inkey "$sign_key" -in "$canonical" -out "$signature"
signature_value="$(openssl base64 -A -in "$signature")"

mkdir -p "$(dirname "$output")"
jq -S --arg signature_value "$signature_value" '.signature.value=$signature_value' "$unsigned" > "$output"
