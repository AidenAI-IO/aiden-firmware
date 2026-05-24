#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
RK_WIFI="$ROOT_DIR/pico-sdk/project/app/wifi_app/wifi/src/Rk_wifi.c"
WIFI_INIT="$ROOT_DIR/pico-sdk/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-glibc-ultra/usr/bin/wifi_bt_init.sh"

if ! grep -q 'access("/data/wpa_supplicant.conf", F_OK)' "$RK_WIFI"; then
    echo "rkwifi_server must preserve existing /data/wpa_supplicant.conf" >&2
    exit 1
fi

data_line=$(grep -n 'access("/data/wpa_supplicant.conf", F_OK)' "$RK_WIFI" | sed 's/:.*//' | head -n 1)
etc_line=$(grep -n 'access("/etc/wpa_supplicant.conf", F_OK)' "$RK_WIFI" | sed 's/:.*//' | head -n 1)
if [ -z "$data_line" ] || [ -z "$etc_line" ] || [ "$data_line" -ge "$etc_line" ]; then
    echo "rkwifi_server must check persisted Wi-Fi config before /etc default" >&2
    exit 1
fi

if grep -q 'cp /etc/wpa_supplicant.conf /data/wpa_supplicant.conf' "$RK_WIFI"; then
    echo "rkwifi_server must not overwrite persisted Wi-Fi config with /etc default" >&2
    exit 1
fi

if ! grep -q 'WPA_CONF=/etc/wpa_supplicant.conf' "$WIFI_INIT" || \
   ! grep -q 'WPA_CONF=/data/wpa_supplicant.conf' "$WIFI_INIT" || \
   ! grep -q 'wpa_supplicant -B -i wlan0 -c "$WPA_CONF"' "$WIFI_INIT"; then
    echo "wifi init must prefer persisted Wi-Fi config when present" >&2
    exit 1
fi

init_data_line=$(grep -n 'WPA_CONF=/data/wpa_supplicant.conf' "$WIFI_INIT" | sed 's/:.*//' | head -n 1)
init_cfg_line=$(grep -n 'WPA_CONF=/data/cfg/wpa_supplicant.conf' "$WIFI_INIT" | sed 's/:.*//' | head -n 1)
if [ -z "$init_data_line" ] || [ -z "$init_cfg_line" ] || [ "$init_data_line" -ge "$init_cfg_line" ]; then
    echo "wifi init must prefer /data/wpa_supplicant.conf before /data/cfg/wpa_supplicant.conf" >&2
    exit 1
fi

echo "wifi persistence tests passed"
