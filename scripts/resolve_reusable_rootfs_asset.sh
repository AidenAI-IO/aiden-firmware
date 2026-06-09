#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage:
  resolve_reusable_rootfs_asset.sh \
    --image-dir DIR \
    [--channel CHANNEL] \
    --upload-assets 'FILE ...' \
    [--output FILE]

Resolve whether rootfs.img can reuse a previous same-channel GitHub release asset.

Outputs:
  rootfs_reused=true|false
  rootfs_asset_url=URL
  rootfs_asset_metadata=JSON
  upload_assets='FILE ...'
USAGE
}

log() {
  printf '%s\n' "$*" >&2
}

die() {
  log "resolve_reusable_rootfs_asset.sh: $*"
  exit 1
}

image_dir=""
channel=""
upload_assets=""
output="${GITHUB_OUTPUT:-}"
rootfs_asset_name="rootfs.img"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --image-dir)
      image_dir="${2:-}"
      shift 2
      ;;
    --channel)
      channel="${2:-}"
      shift 2
      ;;
    --upload-assets)
      upload_assets="${2:-}"
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
[ -n "$upload_assets" ] || die "missing --upload-assets"
[ -d "$image_dir" ] || die "missing image directory: $image_dir"
[ -f "$image_dir/$rootfs_asset_name" ] || die "missing rootfs image: $image_dir/$rootfs_asset_name"
case "$channel" in
  *[!A-Za-z0-9._-]*) die "invalid --channel: $channel" ;;
esac

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    return 1
  fi
}

emit_output() {
  local key="$1"
  local value="$2"

  if [ -n "$output" ]; then
    printf '%s=%s\n' "$key" "$value" >> "$output"
  else
    printf '%s=%s\n' "$key" "$value"
  fi
}

remove_upload_asset() {
  local remove_name="$1"
  local resolved=()
  local asset

  for asset in $upload_assets; do
    if [ "$asset" = "$remove_name" ]; then
      continue
    fi
    resolved+=("$asset")
  done

  local IFS=' '
  printf '%s' "${resolved[*]}"
}

finish() {
  emit_output "rootfs_reused" "$rootfs_reused"
  emit_output "rootfs_asset_url" "$rootfs_asset_url"
  emit_output "rootfs_asset_metadata" "$rootfs_asset_metadata"
  emit_output "upload_assets" "$resolved_upload_assets"
}

disable_reuse() {
  log "Rootfs reuse disabled: $*"
  finish
  exit 0
}

rootfs_reused="false"
rootfs_asset_url=""
rootfs_asset_metadata=""
resolved_upload_assets="$upload_assets"

command -v jq >/dev/null 2>&1 || disable_reuse "jq is unavailable"
command -v gh >/dev/null 2>&1 || disable_reuse "gh CLI is unavailable"
current_rootfs_sha="$(sha256_of "$image_dir/$rootfs_asset_name")" || disable_reuse "sha256 tool is unavailable"
compressed_rootfs_asset_name="${rootfs_asset_name}.tar.gz"

repo="${GITHUB_REPOSITORY:-}"
case "$repo" in
  */*) ;;
  *) disable_reuse "GITHUB_REPOSITORY is not set to OWNER/REPO" ;;
esac

if [ -z "${GH_TOKEN:-}" ] && [ -z "${GITHUB_TOKEN:-}" ]; then
  disable_reuse "GH_TOKEN or GITHUB_TOKEN is not set"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
err_file="$tmp_dir/gh.err"

candidate_tag=""
candidate_manifest_api_url=""
candidate_manifest=""

load_manifest_for_release() {
  local release="$1"

  candidate_tag="$(printf '%s' "$release" | jq -r '.tag_name // .tagName // "unknown"')"
  candidate_manifest_api_url="$(printf '%s' "$release" | jq -r '.assets[]? | select(.name == "manifest.json") | .url // empty' | head -n 1)"
  candidate_manifest=""

  if [ -z "$candidate_manifest_api_url" ]; then
    log "Rootfs reuse skipped: release $candidate_tag has no manifest.json asset"
    return 1
  fi

  if ! candidate_manifest="$(gh api -H "Accept: application/octet-stream" "$candidate_manifest_api_url" 2>"$err_file")"; then
    log "Rootfs reuse skipped: failed to download manifest.json from release $candidate_tag"
    sed 's/^/  /' "$err_file" >&2 || true
    return 1
  fi

  return 0
}

release_json=""
if ! release_json="$(gh api "repos/$repo/releases?per_page=20" 2>"$err_file")"; then
  log "Rootfs reuse disabled: failed to query previous GitHub releases"
  sed 's/^/  /' "$err_file" >&2 || true
  finish
  exit 0
fi

published_releases_file="$tmp_dir/published-releases.ndjson"
if ! printf '%s' "$release_json" | jq -c 'if type == "array" then .[] | select(.draft != true) else error("expected releases array") end' > "$published_releases_file"; then
  disable_reuse "previous release JSON is invalid"
fi

if [ ! -s "$published_releases_file" ]; then
  disable_reuse "no previous published release found"
fi

previous_release=""
previous_tag=""
previous_manifest_api_url=""
previous_manifest=""

if [ -z "$channel" ]; then
  previous_release="$(head -n 1 "$published_releases_file")"
  if ! load_manifest_for_release "$previous_release"; then
    log "Rootfs reuse disabled: no usable manifest.json found on latest published release"
    finish
    exit 0
  fi
  previous_tag="$candidate_tag"
  previous_manifest_api_url="$candidate_manifest_api_url"
  previous_manifest="$candidate_manifest"
else
  selected_previous_release="false"
  while IFS= read -r candidate_release; do
    if ! load_manifest_for_release "$candidate_release"; then
      continue
    fi

    if ! candidate_channel="$(printf '%s' "$candidate_manifest" | jq -r '.channel // empty')"; then
      log "Rootfs reuse skipped: release $candidate_tag manifest.json is invalid"
      continue
    fi

    if [ "$candidate_channel" != "$channel" ]; then
      log "Rootfs reuse skipped: release $candidate_tag channel ${candidate_channel:-<empty>} does not match current channel $channel"
      continue
    fi

    previous_release="$candidate_release"
    previous_tag="$candidate_tag"
    previous_manifest_api_url="$candidate_manifest_api_url"
    previous_manifest="$candidate_manifest"
    selected_previous_release="true"
    break
  done < "$published_releases_file"

  if [ "$selected_previous_release" != "true" ]; then
    disable_reuse "no previous published release found for channel $channel"
  fi
fi

if [ -z "$previous_manifest_api_url" ] || [ -z "$previous_manifest" ]; then
  log "Rootfs reuse disabled: no usable previous manifest.json found"
  finish
  exit 0
fi

rootfs_query='
  .parts[]?
  | select(.name == "rootfs")
  | .asset // empty
  | select(.name == $name or .name == $compressed_name)
  | {
      name:(.name // ""),
      url:(.url // ""),
      size:(.size // 0),
      sha256:(.sha256 // ""),
      image_sha256:(.image_sha256 // "")
    }
'
previous_rootfs_json=""
if ! previous_rootfs_json="$(printf '%s' "$previous_manifest" | jq -c --arg name "$rootfs_asset_name" --arg compressed_name "$compressed_rootfs_asset_name" "$rootfs_query" | head -n 1)"; then
  disable_reuse "previous manifest.json is invalid"
fi

if [ -z "$previous_rootfs_json" ]; then
  disable_reuse "previous manifest.json has no reusable neutral rootfs asset"
fi

previous_rootfs_name="$(printf '%s' "$previous_rootfs_json" | jq -r '.name // empty')"
previous_rootfs_size="$(printf '%s' "$previous_rootfs_json" | jq -r '.size // empty')"
previous_rootfs_sha="$(printf '%s' "$previous_rootfs_json" | jq -r '.sha256 // empty' | tr '[:upper:]' '[:lower:]')"
previous_rootfs_image_sha="$(printf '%s' "$previous_rootfs_json" | jq -r '.image_sha256 // empty' | tr '[:upper:]' '[:lower:]')"
previous_manifest_rootfs_url="$(printf '%s' "$previous_rootfs_json" | jq -r '.url // empty')"
previous_release_rootfs_url="$(printf '%s' "$previous_release" | jq -r --arg name "$previous_rootfs_name" '.assets[]? | select(.name == $name) | .browser_download_url // .browserDownloadUrl // empty' | head -n 1)"
reuse_url="$previous_manifest_rootfs_url"
if [ -z "$reuse_url" ]; then
  reuse_url="$previous_release_rootfs_url"
fi

case "$previous_rootfs_size" in
  ''|*[!0-9]*|0)
    disable_reuse "previous manifest.json rootfs asset has invalid size: $previous_rootfs_size"
    ;;
esac

if [ -z "$previous_rootfs_sha" ]; then
  disable_reuse "previous manifest.json rootfs asset has no sha256"
fi

comparison_sha="$previous_rootfs_sha"
comparison_field="sha256"
if [ "$previous_rootfs_name" = "$compressed_rootfs_asset_name" ]; then
  if [ -z "$previous_rootfs_image_sha" ]; then
    disable_reuse "previous compressed rootfs asset has no image_sha256"
  fi
  comparison_sha="$previous_rootfs_image_sha"
  comparison_field="image_sha256"
elif [ "$previous_rootfs_name" != "$rootfs_asset_name" ]; then
  disable_reuse "previous rootfs asset name is not reusable: $previous_rootfs_name"
fi

if [ "$current_rootfs_sha" != "$comparison_sha" ]; then
  log "Rootfs reuse skipped: sha256 differs from previous release $previous_tag"
  log "  current:  $current_rootfs_sha"
  log "  previous $comparison_field: $comparison_sha"
  finish
  exit 0
fi

case "$reuse_url" in
  http://*|https://*) ;;
  "") disable_reuse "previous release $previous_tag has no reusable rootfs download URL" ;;
  *) disable_reuse "previous rootfs download URL is not http(s): $reuse_url" ;;
esac

rootfs_reused="true"
rootfs_asset_url="$reuse_url"
rootfs_asset_metadata="$(printf '%s' "$previous_rootfs_json" | jq -c --arg url "$rootfs_asset_url" '.url = $url | if .image_sha256 == "" then del(.image_sha256) else . end')"
resolved_upload_assets="$(remove_upload_asset "$rootfs_asset_name")"

log "Rootfs reuse enabled: $previous_rootfs_name $comparison_field matches previous release $previous_tag"
log "  url: $rootfs_asset_url"
log "  upload_assets: $resolved_upload_assets"
finish
