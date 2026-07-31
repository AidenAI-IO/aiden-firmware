#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/generate_ota_device_config.sh --manifest manifest.json --repo OWNER/REPO --channel CHANNEL --output config.json

Generate the device-side OTA config seeded into userdata.img during factory image builds.
USAGE
}

die() {
  printf 'generate_ota_device_config.sh: %s\n' "$*" >&2
  exit 1
}

manifest=""
repo=""
channel=""
output=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --help)
      usage
      exit 0
      ;;
    --manifest)
      [ "$#" -ge 2 ] || die "--manifest requires a value"
      manifest="$2"
      shift 2
      ;;
    --repo)
      [ "$#" -ge 2 ] || die "--repo requires a value"
      repo="$2"
      shift 2
      ;;
    --channel)
      [ "$#" -ge 2 ] || die "--channel requires a value"
      channel="$2"
      shift 2
      ;;
    --output)
      [ "$#" -ge 2 ] || die "--output requires a value"
      output="$2"
      shift 2
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

[ -n "$manifest" ] || die "missing --manifest"
[ -n "$repo" ] || die "missing --repo"
[ -n "$channel" ] || die "missing --channel"
[ -n "$output" ] || die "missing --output"
command -v jq >/dev/null 2>&1 || die "jq is required"
[ -f "$manifest" ] || die "missing manifest: $manifest"

if [[ ! "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  die "invalid --repo: $repo"
fi

case "$channel" in
  *[!A-Za-z0-9._-]*|'') die "invalid --channel: $channel" ;;
esac

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

jq -e -S \
  --arg repo "$repo" \
  --arg channel "$channel" \
  '
  def part($name): .parts[] | select(.name == $name);
  def partition_hash($asset):
    if ($asset.image_sha256 // "") != "" then
      $asset.image_sha256
    else
      $asset.sha256
    end;
  def hash_for($name; $slot):
    (part($name)) as $part |
    if $part.asset != null then
      partition_hash($part.asset)
    elif $slot == "a" and $part.asset_a != null then
      partition_hash($part.asset_a)
    elif $slot == "b" and $part.asset_b != null then
      partition_hash($part.asset_b)
    else
      error("missing asset hash for " + $name + " slot " + $slot)
    end;
  if (.version | type) != "string" or (.build_time | type) != "string" then
    error("manifest version and build_time are required")
  else
  {
    repo: $repo,
    channel: $channel,
    reserve_size_bytes: 209715200,
    reserve_safety_margin_bytes: 4194304,
    factory_version: .version,
    factory_build_time: .build_time,
    factory_partition_hashes: {
      a: {
        boot: hash_for("boot"; "a"),
        oem: hash_for("oem"; "a"),
        rootfs: hash_for("rootfs"; "a")
      },
      b: {
        boot: hash_for("boot"; "b"),
        oem: hash_for("oem"; "b"),
        rootfs: hash_for("rootfs"; "b")
      }
    }
  }
  end
  ' "$manifest" > "$tmp"

mkdir -p "$(dirname "$output")"
mv "$tmp" "$output"
trap - EXIT
