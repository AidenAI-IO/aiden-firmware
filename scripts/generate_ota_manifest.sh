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
  --asset-url     Exact asset download URL in FILE=URL form; may be repeated
                  Takes precedence over --base-url for the named file
  --asset-metadata
                  Exact manifest asset JSON in FILE=JSON form; may be repeated
                  Takes precedence over --asset-url and --base-url for the named file
  --max-download-bytes
                  Maximum compressed download bytes for either target slot
                  (default 205520896 = 196 MiB; use 0 to disable)

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
max_download_bytes=205520896
asset_url_files=()
asset_url_values=()
asset_metadata_files=()
asset_metadata_values=()

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
      [[ "$base_url" =~ ^https?:// ]] || die "--base-url must start with http:// or https://"
      [[ "$base_url" =~ [[:space:]] ]] && die "--base-url must not contain whitespace"
      shift 2
      ;;
    --asset-url)
      [ "$#" -ge 2 ] || die "--asset-url requires a value"
      asset_url_pair="$2"
      asset_url_file="${asset_url_pair%%=*}"
      asset_url_value="${asset_url_pair#*=}"
      [ "$asset_url_file" != "$asset_url_pair" ] || die "--asset-url must use FILE=URL"
      [ -n "$asset_url_file" ] || die "--asset-url file must not be empty"
      [ -n "$asset_url_value" ] || die "--asset-url URL must not be empty"
      case "$asset_url_file" in
        */*|*[[:space:]]*) die "--asset-url file must be a release asset name: $asset_url_file" ;;
      esac
      [[ "$asset_url_value" =~ ^https?:// ]] || die "--asset-url URL must start with http:// or https://"
      [[ "$asset_url_value" =~ [[:space:]] ]] && die "--asset-url URL must not contain whitespace"
      asset_url_files+=("$asset_url_file")
      asset_url_values+=("$asset_url_value")
      shift 2
      ;;
    --asset-metadata)
      [ "$#" -ge 2 ] || die "--asset-metadata requires a value"
      asset_metadata_pair="$2"
      asset_metadata_file="${asset_metadata_pair%%=*}"
      asset_metadata_value="${asset_metadata_pair#*=}"
      [ "$asset_metadata_file" != "$asset_metadata_pair" ] || die "--asset-metadata must use FILE=JSON"
      [ -n "$asset_metadata_file" ] || die "--asset-metadata file must not be empty"
      [ -n "$asset_metadata_value" ] || die "--asset-metadata JSON must not be empty"
      case "$asset_metadata_file" in
        */*|*[[:space:]]*) die "--asset-metadata file must be a release asset name: $asset_metadata_file" ;;
      esac
      asset_metadata_files+=("$asset_metadata_file")
      asset_metadata_values+=("$asset_metadata_value")
      shift 2
      ;;
    --max-download-bytes)
      [ "$#" -ge 2 ] || die "--max-download-bytes requires a value"
      max_download_bytes="$2"
      case "$max_download_bytes" in
        ''|*[!0-9]*) die "--max-download-bytes must be a non-negative integer" ;;
      esac
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

asset_url_for_file() {
  local file="$1"
  local index

  for index in "${!asset_url_files[@]}"; do
    if [ "${asset_url_files[$index]}" = "$file" ]; then
      printf '%s' "${asset_url_values[$index]}"
      return 0
    fi
  done

  return 1
}

asset_metadata_for_file() {
  local file="$1"
  local index

  for index in "${!asset_metadata_files[@]}"; do
    if [ "${asset_metadata_files[$index]}" = "$file" ]; then
      printf '%s' "${asset_metadata_values[$index]}"
      return 0
    fi
  done

  return 1
}

asset_metadata_json() {
  local file="$1"
  local path="$image_dir/$file"
  local metadata="$2"
  local image_sha256 compressed_name

  image_sha256="$(file_sha256 "$path")"
  compressed_name="${file}.tar.gz"

  if ! printf '%s' "$metadata" | jq -e \
    --arg file "$file" \
    --arg compressed_name "$compressed_name" \
    --arg image_sha256 "$image_sha256" \
    '
      type == "object" and
      (.name == $file or .name == $compressed_name) and
      (.url | type == "string" and test("^https?://") and (test("[[:space:]]") | not)) and
      (.size | type == "number" and . > 0) and
      (.sha256 | type == "string" and test("^[A-Fa-f0-9]{64}$")) and
      (
        if .name == $compressed_name then
          (.image_sha256 | type == "string" and test("^[A-Fa-f0-9]{64}$") and (ascii_downcase == ($image_sha256 | ascii_downcase)))
        else
          ((.image_sha256 // "") == "" and ((.sha256 | ascii_downcase) == ($image_sha256 | ascii_downcase)))
        end
      )
    ' >/dev/null; then
    die "invalid --asset-metadata for $file"
  fi

  printf '%s' "$metadata" | jq -c \
    --arg compressed_name "$compressed_name" \
    '
      {
        name: .name,
        url: .url,
        size: .size,
        sha256: (.sha256 | ascii_downcase)
      } +
      if .name == $compressed_name then
        {image_sha256: (.image_sha256 | ascii_downcase)}
      else
        {}
      end
    '
}

asset_json() {
  local file="$1"
  local path="$image_dir/$file"
  [ -f "$path" ] || die "missing required image: $path"
  local size sha256 image_sha256 url metadata compressed_file compressed_path
  if metadata="$(asset_metadata_for_file "$file")"; then
    asset_metadata_json "$file" "$metadata"
    return
  fi

  compressed_file="${file}.tar.gz"
  compressed_path="$image_dir/$compressed_file"
  if [ -f "$compressed_path" ]; then
    size="$(file_size "$compressed_path")"
    sha256="$(file_sha256 "$compressed_path")"
    image_sha256="$(file_sha256 "$path")"
    if ! url="$(asset_url_for_file "$compressed_file")"; then
      url=""
    fi
    if [ -z "$url" ] && [ -n "$base_url" ]; then
      url="${base_url%/}/$compressed_file"
    fi
    if [ -n "$url" ]; then
      jq -n \
        --arg name "$compressed_file" \
        --arg url "$url" \
        --argjson size "$size" \
        --arg sha256 "$sha256" \
        --arg image_sha256 "$image_sha256" \
        '{name:$name,url:$url,size:$size,sha256:$sha256,image_sha256:$image_sha256}'
    else
      jq -n \
        --arg name "$compressed_file" \
        --argjson size "$size" \
        --arg sha256 "$sha256" \
        --arg image_sha256 "$image_sha256" \
        '{name:$name,size:$size,sha256:$sha256,image_sha256:$image_sha256}'
    fi
    return
  fi

  size="$(file_size "$path")"
  sha256="$(file_sha256 "$path")"
  if ! url="$(asset_url_for_file "$file")"; then
    url=""
  fi
  if [ -z "$url" ] && [ -n "$base_url" ]; then
    url="${base_url%/}/$file"
  fi
  if [ -n "$url" ]; then
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

if [ "$max_download_bytes" -gt 0 ]; then
  for target_slot in a b; do
    target_download_bytes="$(jq -r --arg slot "$target_slot" '
      [
        .parts[] |
        if .asset != null then
          .asset.size
        elif $slot == "a" then
          .asset_a.size
        else
          .asset_b.size
        end
      ] | add
    ' "$unsigned")"
    if [ "$target_download_bytes" -gt "$max_download_bytes" ]; then
      die "target slot $target_slot download size $target_download_bytes bytes exceeds limit $max_download_bytes bytes"
    fi
  done
fi

jq -cS 'del(.signature.value)' "$unsigned" | tr -d '\n' > "$canonical"
openssl pkeyutl -sign -rawin -inkey "$sign_key" -in "$canonical" -out "$signature"
signature_value="$(openssl base64 -A -in "$signature")"

mkdir -p "$(dirname "$output")"
jq -S --arg signature_value "$signature_value" '.signature.value=$signature_value' "$unsigned" > "$output"
