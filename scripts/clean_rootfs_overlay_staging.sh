#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage:
  clean_rootfs_overlay_staging.sh --dest-overlay DIR

Remove stale generated files from the pico-sdk rootfs overlay staging tree.
USAGE
}

log() {
  printf '%s\n' "$*" >&2
}

die() {
  log "clean_rootfs_overlay_staging.sh: $*"
  exit 1
}

dest_overlay=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dest-overlay)
      dest_overlay="${2:-}"
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

[ -n "$dest_overlay" ] || die "missing --dest-overlay"
[ -d "$dest_overlay" ] || die "missing destination rootfs overlay: $dest_overlay"

remove_stale_path() {
  local relative_path="$1"
  local path="$dest_overlay/$relative_path"

  case "$relative_path" in
    ""|/*|*../*) die "invalid stale rootfs overlay path: $relative_path" ;;
  esac

  if [ -e "$path" ] || [ -L "$path" ]; then
    rm -rf -- "$path"
    log "Removed stale rootfs overlay path: $relative_path"
  fi
}

# Bundled skills moved from rootfs to OEM staging in 6abb8de. Old dev branches
# can still leave this directory in the shared self-hosted runner workspace.
remove_stale_path "usr/share/aiden/skills"
