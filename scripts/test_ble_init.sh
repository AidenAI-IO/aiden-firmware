#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BOARD_CONFIG="$ROOT_DIR/pico-sdk/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Buildroot-RV1106_Luckfox_Pico_Zero-IPC.mk"
BOOT_CONF="$ROOT_DIR/overlay/etc/aiden_boot.conf"
HCI_INIT="$ROOT_DIR/overlay/etc/init.d/S39hciinit"
BLUEZ_INIT="$ROOT_DIR/overlay/etc/init.d/S40bluetoothd"
BLE_INIT="$ROOT_DIR/overlay/etc/init.d/S41ble_service"
BLE_CONFIG="$ROOT_DIR/overlay/etc/aiden_ble_service.conf"
BLUEZ_CONFIG="$ROOT_DIR/overlay/etc/bluetooth/main.conf"
BLE_CONSTANTS="$ROOT_DIR/src/agent/internal/ble/constants.go"
BLE_MAIN="$ROOT_DIR/src/agent/cmd/ble_service/main.go"

for path in "$BOARD_CONFIG" "$BOOT_CONF" "$HCI_INIT" "$BLUEZ_INIT" "$BLE_INIT" "$BLE_CONFIG" "$BLUEZ_CONFIG" "$BLE_CONSTANTS" "$BLE_MAIN"; do
    if [ ! -f "$path" ]; then
        echo "missing BLE integration file: $path" >&2
        exit 1
    fi
done

for script in "$HCI_INIT" "$BLUEZ_INIT" "$BLE_INIT"; do
    if [ ! -x "$script" ]; then
        echo "BLE init script must be executable: $script" >&2
        exit 1
    fi
    sh -n "$script"
done

if [ -e "$ROOT_DIR/overlay/etc/init.d/S99hciinit" ]; then
    echo "legacy late HCI init script must be removed" >&2
    exit 1
fi

fragment_line=$(grep 'RK_KERNEL_DEFCONFIG_FRAGMENT=' "$BOARD_CONFIG")
case "$fragment_line" in
    *aiden-zram.config*rv1106-bt.config*) ;;
    *) echo "Pico Zero kernel fragments must include zram and Bluetooth: $fragment_line" >&2; exit 1 ;;
esac

for setting in ENABLE_BLUETOOTH_HCI ENABLE_BLUETOOTHD ENABLE_BLE_SERVICE; do
    if ! grep -Fxq "$setting=1" "$BOOT_CONF"; then
        echo "$setting must be enabled by default" >&2
        exit 1
    fi
done

grep -Fq '/dev/ttyS1' "$HCI_INIT"
grep -Fq '1500000' "$HCI_INIT"
grep -Fq 'ensure_smp_crypto || return 1' "$HCI_INIT"
grep -Fq 'ensure_crypto_module aes_generic' "$HCI_INIT"
grep -Fq 'ensure_crypto_module cmac' "$HCI_INIT"
grep -Fq 'Bluetooth HCI reinitialized after loading SMP crypto' "$HCI_INIT"
grep -Fq '/userdata/ble_service/bluetooth' "$BLUEZ_INIT"
grep -Fq 'bluetoothd-watchdog.pid' "$BLUEZ_INIT"
grep -Fq 'hci0 disappeared; restarting Bluetooth stack' "$BLUEZ_INIT"
grep -Fq 'retaining $PID_FILE' "$BLUEZ_INIT"
grep -Fq 'restart|reload) stop && start' "$BLUEZ_INIT"
grep -Fq '/run/ble_service/ble_service.sock' "$BLE_CONFIG"
grep -Fxq 'PAIRING_WINDOW_SECONDS=300' "$BLE_CONFIG"
grep -Fxq 'PairableTimeout=300' "$BLUEZ_CONFIG"
grep -Fxq 'JustWorksRepairing=confirm' "$BLUEZ_CONFIG"
grep -Fq "trap 'shutdown' INT TERM" "$BLE_INIT"
grep -Fq 'retaining $PID_FILE and $SOCKET_PATH' "$BLE_INIT"
grep -Fq 'restart|reload) stop && start' "$BLE_INIT"
grep -Fq 'pairing-window must be positive' "$BLE_MAIN"
grep -Fq '00001812-0000-1000-8000-00805f9b34fb' "$BLE_CONSTANTS"
grep -Fq './cmd/ble_service' "$ROOT_DIR/_build.sh"

echo "BLE init tests passed"
