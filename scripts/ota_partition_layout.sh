#!/usr/bin/env bash

AIDEN_OTA_PARTITION_NAME="${AIDEN_OTA_PARTITION_NAME:-ota}"
AIDEN_OTA_MOUNT_POINT="${AIDEN_OTA_MOUNT_POINT:-/userdata/ota}"
AIDEN_OTA_DEVICE_PATH="${AIDEN_OTA_DEVICE_PATH:-/dev/block/by-name/ota}"
AIDEN_OTA_FILESYSTEM="${AIDEN_OTA_FILESYSTEM:-ext4}"
AIDEN_OTA_DOWNLOAD_SAFETY_MARGIN_MIB="${AIDEN_OTA_DOWNLOAD_SAFETY_MARGIN_MIB:-16}"
AIDEN_OTA_FILESYSTEM_OVERHEAD_MIB="${AIDEN_OTA_FILESYSTEM_OVERHEAD_MIB:-24}"
AIDEN_OTA_LAYOUT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AIDEN_OTA_BOARD_CONFIG_PATH="${AIDEN_OTA_BOARD_CONFIG_PATH:-$AIDEN_OTA_LAYOUT_ROOT/pico-sdk/.BoardConfig.mk}"

aiden_ota_partition_size_mib() {
  local partition_env definition size_spec partition_name value

  if [ ! -f "$AIDEN_OTA_BOARD_CONFIG_PATH" ]; then
    echo "ota_partition_layout.sh: missing board config: $AIDEN_OTA_BOARD_CONFIG_PATH" >&2
    return 1
  fi
  partition_env="$({
    unset RK_PARTITION_CMD_IN_ENV
    source "$AIDEN_OTA_BOARD_CONFIG_PATH"
    printf '%s' "${RK_PARTITION_CMD_IN_ENV:-}"
  })" || return 1

  while IFS= read -r definition; do
    partition_name="${definition#*(}"
    partition_name="${partition_name%)}"
    [ "$partition_name" = "$AIDEN_OTA_PARTITION_NAME" ] || continue

    size_spec="${definition%%(*}"
    size_spec="${size_spec%%@*}"
    value="${size_spec%?}"
    case "$size_spec" in
      *K|*k)
        [[ "$value" =~ ^[0-9]+$ ]] && [ $((value % 1024)) -eq 0 ] || break
        echo $((value / 1024))
        return 0
        ;;
      *M|*m)
        [[ "$value" =~ ^[0-9]+$ ]] || break
        echo "$value"
        return 0
        ;;
      *G|*g)
        [[ "$value" =~ ^[0-9]+$ ]] || break
        echo $((value * 1024))
        return 0
        ;;
    esac
    break
  done < <(printf '%s\n' "$partition_env" | tr ',' '\n')

  echo "ota_partition_layout.sh: valid ${AIDEN_OTA_PARTITION_NAME} partition not found in $AIDEN_OTA_BOARD_CONFIG_PATH" >&2
  return 1
}

aiden_ota_partition_size_bytes() {
  local partition_mib
  partition_mib="$(aiden_ota_partition_size_mib)" || return 1
  echo $((partition_mib * 1024 * 1024))
}

aiden_ota_download_safety_margin_bytes() {
  echo $((AIDEN_OTA_DOWNLOAD_SAFETY_MARGIN_MIB * 1024 * 1024))
}

aiden_ota_manifest_max_download_bytes() {
  local partition_mib usable_mib
  partition_mib="$(aiden_ota_partition_size_mib)" || return 1
  usable_mib=$((partition_mib - AIDEN_OTA_DOWNLOAD_SAFETY_MARGIN_MIB - AIDEN_OTA_FILESYSTEM_OVERHEAD_MIB))
  if [ "$usable_mib" -le 0 ]; then
    echo "ota_partition_layout.sh: OTA partition has no usable download capacity" >&2
    return 1
  fi
  echo $((usable_mib * 1024 * 1024))
}
