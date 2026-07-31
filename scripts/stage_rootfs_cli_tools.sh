#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage:
  stage_rootfs_cli_tools.sh --source-dir DIR --dest-overlay DIR

Verify the built ARM32 CLI bundle and install fq, yq, and rg into the rootfs
overlay under /usr/bin.
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

source_dir=""
dest_overlay=""

while [ "$#" -gt 0 ]; do
  case "$1" in
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

[ -n "$source_dir" ] || die "missing --source-dir"
[ -n "$dest_overlay" ] || die "missing --dest-overlay"
[ -d "$source_dir" ] || die "missing source directory: $source_dir"
[ -d "$dest_overlay" ] || die "missing destination rootfs overlay: $dest_overlay"
[ -f "$source_dir/manifest.sha256" ] || die "missing checksum manifest: $source_dir/manifest.sha256"
command -v file >/dev/null 2>&1 || die "file is required to verify target architecture"

tools=(fq yq rg)
for tool in "${tools[@]}"; do
  source_path="$source_dir/$tool"
  [ -f "$source_path" ] || die "missing built tool: $source_path"

  expected_sha="$(awk -v tool="$tool" '$2 == tool { print $1; found = 1; exit } END { if (!found) exit 1 }' "$source_dir/manifest.sha256")" || \
    die "checksum manifest has no entry for $tool"
  actual_sha="$(sha256_file "$source_path")"
  if [ "$actual_sha" != "$expected_sha" ]; then
    die "checksum verification failed for $tool: expected $expected_sha, got $actual_sha"
  fi

  file_description="$(file -b "$source_path")"
  case "$file_description" in
    *"ELF 32-bit LSB"*"ARM"*) ;;
    *) die "$tool is not an ARM32 ELF executable: $file_description" ;;
  esac
done

mkdir -p "$dest_overlay/usr/bin"
for tool in "${tools[@]}"; do
  cp "$source_dir/$tool" "$dest_overlay/usr/bin/$tool"
  chmod 0755 "$dest_overlay/usr/bin/$tool"
done

printf 'Staged rootfs CLI tools in %s/usr/bin: %s\n' "$dest_overlay" "${tools[*]}"
