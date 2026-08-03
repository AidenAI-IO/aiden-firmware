#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
layout_script="$repo_root/scripts/ota_partition_layout.sh"

if [ ! -f "$layout_script" ]; then
  echo "missing $layout_script" >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

board_path="$tmp_dir/BoardConfig.mk"

printf '%s\n' \
  'export RK_PARTITION_CMD_IN_ENV="32K(env),3G(userdata),300M(ota)"' \
  'export RK_PARTITION_FS_TYPE_CFG=rootfs_a@IGNORE@ext4,userdata@/userdata@ext4,ota@/userdata/ota@ext4' \
  > "$board_path"

AIDEN_OTA_BOARD_CONFIG_PATH="$board_path"
source "$layout_script"

if [ "$(aiden_ota_partition_size_mib)" != "300" ]; then
  echo "OTA partition size must be read from the SDK board config" >&2
  exit 1
fi
if [ "$(aiden_ota_partition_size_bytes)" != "314572800" ]; then
  echo "OTA partition size must be 300 MiB" >&2
  exit 1
fi
if [ "$(aiden_ota_download_safety_margin_bytes)" != "16777216" ]; then
  echo "OTA download safety margin must be 16 MiB" >&2
  exit 1
fi
if [ "$(aiden_ota_manifest_max_download_bytes)" != "266338304" ]; then
  echo "OTA manifest limit must derive to 254 MiB" >&2
  exit 1
fi

source "$board_path"

case ",$RK_PARTITION_FS_TYPE_CFG," in
  *',ota@/userdata/ota@ext4,'*) ;;
  *) echo "SDK board config is missing the OTA filesystem mapping: $RK_PARTITION_FS_TYPE_CFG" >&2; exit 1 ;;
esac

printf '%s\n' 'export RK_PARTITION_CMD_IN_ENV="32K(env),3G(userdata)"' > "$board_path"
if aiden_ota_partition_size_mib 2>/dev/null; then
  echo "partition lookup must fail when the SDK config omits OTA" >&2
  exit 1
fi

echo "OTA partition layout tests passed"
