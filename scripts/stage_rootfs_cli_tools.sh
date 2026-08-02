#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_CATALOG="$SCRIPT_DIR/rootfs_cli_tools.catalog"
CATALOG_LIB="$SCRIPT_DIR/rootfs_cli_tool_catalog.sh"

# shellcheck source=/dev/null
source "$CATALOG_LIB"

usage() {
  cat >&2 <<'USAGE'
Usage:
  stage_rootfs_cli_tools.sh [--catalog FILE] [--policy all|preserve|normal]
                            --source-dir DIR --dest-overlay DIR

Verify the catalog-selected ARM32 CLI bundle and install it into the rootfs
overlay under /usr/bin. The default policy installs every catalog tool.
USAGE
}

die() {
  printf 'stage_rootfs_cli_tools.sh: %s\n' "$*" >&2
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

catalog_path="$DEFAULT_CATALOG"
selected_policy="all"
source_dir=""
dest_overlay=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --catalog)
      catalog_path="${2:-}"
      shift 2
      ;;
    --policy)
      selected_policy="${2:-}"
      shift 2
      ;;
    --source-dir)
      source_dir="${2:-}"
      shift 2
      ;;
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

case "$selected_policy" in
  all|preserve|normal) ;;
  *) die "invalid --policy: $selected_policy" ;;
esac
[ -n "$catalog_path" ] || die "missing --catalog value"
[ -n "$source_dir" ] || die "missing --source-dir"
[ -n "$dest_overlay" ] || die "missing --dest-overlay"
[ -d "$source_dir" ] || die "missing source directory: $source_dir"
[ -d "$dest_overlay" ] || die "missing destination rootfs overlay: $dest_overlay"
[ -f "$source_dir/manifest.sha256" ] || die "missing checksum manifest: $source_dir/manifest.sha256"
command -v file >/dev/null 2>&1 || die "file is required to verify target architecture"

catalog_records="$(rootfs_cli_catalog_records "$catalog_path")" || exit 1
selected_names=""
selected_count=0
while IFS='|' read -r name version kind source target source_sha256 artifact_path strip_policy; do
  if [ "$selected_policy" != "all" ] && [ "$selected_policy" != "$strip_policy" ]; then
    continue
  fi

  source_path="$source_dir/$name"
  [ -f "$source_path" ] || die "missing built tool: $source_path"

  expected_sha="$(awk -v tool="$name" '$2 == tool { print $1; found = 1; exit } END { if (!found) exit 1 }' "$source_dir/manifest.sha256")" || \
    die "checksum manifest has no entry for $name"
  actual_sha="$(sha256_file "$source_path")"
  if [ "$actual_sha" != "$expected_sha" ]; then
    die "checksum verification failed for $name: expected $expected_sha, got $actual_sha"
  fi

  file_description="$(file -b "$source_path")"
  case "$file_description" in
    *"ELF 32-bit LSB"*"ARM"*) ;;
    *) die "$name is not an ARM32 ELF executable: $file_description" ;;
  esac

  selected_names="${selected_names}${selected_names:+ }$name"
  selected_count=$((selected_count + 1))
done <<< "$catalog_records"

mkdir -p "$dest_overlay/usr/bin"
while IFS='|' read -r name version kind source target source_sha256 artifact_path strip_policy; do
  if [ "$selected_policy" != "all" ] && [ "$selected_policy" != "$strip_policy" ]; then
    continue
  fi
  cp "$source_dir/$name" "$dest_overlay/usr/bin/$name"
  chmod 0755 "$dest_overlay/usr/bin/$name"
done <<< "$catalog_records"

printf 'Staged %s rootfs CLI tool(s) in %s/usr/bin: %s\n' "$selected_count" "$dest_overlay" "$selected_names"
