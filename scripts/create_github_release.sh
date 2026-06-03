#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage:
  create_github_release.sh \
    --tag-name TAG \
    --release-name NAME \
    --target-commitish SHA \
    --asset-glob 'path/to/assets/*' \
    [--retry-count N] \
    [--retry-delay-seconds N]
EOF
}

log() {
  printf '%s\n' "$*" >&2
}

die() {
  log "$*"
  exit 1
}

tag_name=""
release_name=""
target_commitish=""
asset_glob=""
retry_count=5
retry_delay_seconds=20

while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag-name)
      tag_name="${2:-}"
      shift 2
      ;;
    --release-name)
      release_name="${2:-}"
      shift 2
      ;;
    --target-commitish)
      target_commitish="${2:-}"
      shift 2
      ;;
    --asset-glob)
      asset_glob="${2:-}"
      shift 2
      ;;
    --retry-count)
      retry_count="${2:-}"
      shift 2
      ;;
    --retry-delay-seconds)
      retry_delay_seconds="${2:-}"
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

[ -n "$tag_name" ] || die "missing --tag-name"
[ -n "$release_name" ] || die "missing --release-name"
[ -n "$target_commitish" ] || die "missing --target-commitish"
[ -n "$asset_glob" ] || die "missing --asset-glob"

case "$retry_count" in
  ''|*[!0-9]*)
    die "--retry-count must be a positive integer"
    ;;
esac

case "$retry_delay_seconds" in
  ''|*[!0-9]*)
    die "--retry-delay-seconds must be a non-negative integer"
    ;;
esac

if [ "$retry_count" -lt 1 ]; then
  die "--retry-count must be at least 1"
fi

if ! command -v gh >/dev/null 2>&1; then
  die "gh CLI is required to create GitHub releases"
fi

if [ -z "${GH_TOKEN:-}" ] && [ -z "${GITHUB_TOKEN:-}" ]; then
  die "GH_TOKEN or GITHUB_TOKEN is required for GitHub release creation"
fi

asset_files=()
while IFS= read -r asset; do
  if [ -f "$asset" ]; then
    asset_files+=("$asset")
  fi
done < <(compgen -G "$asset_glob" | sort || true)

if [ "${#asset_files[@]}" -eq 0 ]; then
  die "no release assets matched: $asset_glob"
fi

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    printf 'unavailable'
  fi
}

log "Release upload context"
log "  repository: ${GITHUB_REPOSITORY:-unknown}"
log "  tag_name: $tag_name"
log "  release_name: $release_name"
log "  target_commitish: $target_commitish"
log "  asset_glob: $asset_glob"
log "  retry_count: $retry_count"
log "  retry_delay_seconds: $retry_delay_seconds"
log "  gh_debug: ${GH_DEBUG:-unset}"
gh --version >&2 || true

if command -v df >/dev/null 2>&1; then
  log "Release upload disk context"
  df -h . >&2 || true
fi

log "Release assets"
for asset in "${asset_files[@]}"; do
  size_bytes="$(wc -c < "$asset" | tr -d '[:space:]')"
  checksum="$(sha256_of "$asset")"
  log "  $(basename "$asset") size=${size_bytes} sha256=${checksum}"
done

run_with_retry() {
  local label="$1"
  shift

  local attempt=1
  local status=0
  local tmp_base="${RUNNER_TEMP:-/tmp}"
  local err_file

  while true; do
    err_file="$(mktemp "$tmp_base/github-release.XXXXXX")"
    log "$label attempt $attempt/$retry_count"
    if "$@" 2> >(tee "$err_file" >&2); then
      rm -f "$err_file"
      return 0
    fi

    status=$?
    if [ "$attempt" -ge "$retry_count" ]; then
      log "$label failed after $attempt attempt(s)"
      rm -f "$err_file"
      return "$status"
    fi

    log "Retrying $label after failure; waiting ${retry_delay_seconds}s before attempt $((attempt + 1))/$retry_count"
    rm -f "$err_file"
    if [ "$retry_delay_seconds" -gt 0 ]; then
      sleep "$retry_delay_seconds"
    fi
    attempt=$((attempt + 1))
  done
}

ensure_release() {
  if gh release view "$tag_name" >/dev/null 2>&1; then
    log "Release already exists for tag $tag_name; assets will be uploaded with --clobber"
    return 0
  fi

  local attempt=1
  local status=0
  local tmp_base="${RUNNER_TEMP:-/tmp}"
  local err_file

  while true; do
    err_file="$(mktemp "$tmp_base/github-release.XXXXXX")"
    log "release draft creation $tag_name attempt $attempt/$retry_count"
    if gh release create "$tag_name" \
      --draft \
      --title "$release_name" \
      --target "$target_commitish" \
      --notes "Automated build for $target_commitish" \
      2> >(tee "$err_file" >&2); then
      rm -f "$err_file"
      return 0
    fi

    status=$?
    if gh release view "$tag_name" >/dev/null 2>&1; then
      log "Release exists after failed creation attempt for tag $tag_name; continuing with asset uploads"
      rm -f "$err_file"
      return 0
    fi

    if [ "$attempt" -ge "$retry_count" ]; then
      log "release draft creation $tag_name failed after $attempt attempt(s)"
      rm -f "$err_file"
      return "$status"
    fi

    log "Retrying release draft creation $tag_name after failure; waiting ${retry_delay_seconds}s before attempt $((attempt + 1))/$retry_count"
    rm -f "$err_file"
    if [ "$retry_delay_seconds" -gt 0 ]; then
      sleep "$retry_delay_seconds"
    fi
    attempt=$((attempt + 1))
  done
}

ensure_release

for asset in "${asset_files[@]}"; do
  run_with_retry \
    "release asset upload $(basename "$asset")" \
    gh release upload "$tag_name" "$asset" --clobber
done

run_with_retry \
  "release publish $tag_name" \
  gh release edit "$tag_name" --draft=false --latest

log "Release upload completed for $tag_name"
