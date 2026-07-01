#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/overlay/etc/init.d/S21zram"
BOOT_CONF="$ROOT_DIR/overlay/etc/aiden_boot.conf"
BOARD_CONFIG="$ROOT_DIR/pico-sdk/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Buildroot-RV1106_Luckfox_Pico_Zero-IPC.mk"
KERNEL_FRAGMENT="$ROOT_DIR/pico-sdk/sysdrv/source/kernel/arch/arm/configs/aiden-zram.config"

for path in "$SCRIPT" "$BOOT_CONF" "$BOARD_CONFIG" "$KERNEL_FRAGMENT"; do
    if [ ! -f "$path" ]; then
        echo "missing zram file: $path" >&2
        exit 1
    fi
done

if [ ! -x "$SCRIPT" ]; then
    echo "S21zram must be executable" >&2
    exit 1
fi

for setting in ENABLE_ZRAM ZRAM_SIZE_MB ZRAM_SWAPPINESS ZRAM_COMP_ALGORITHM; do
    if ! grep -Eq "^${setting}=" "$BOOT_CONF"; then
        echo "aiden_boot.conf must define $setting" >&2
        exit 1
    fi
done

for setting in 'CONFIG_SWAP=y' 'CONFIG_CRYPTO=y' 'CONFIG_ZSMALLOC=y' 'CONFIG_ZRAM=y'; do
    if ! grep -Fxq "$setting" "$KERNEL_FRAGMENT"; then
        echo "kernel fragment must define $setting" >&2
        exit 1
    fi
done

if ! grep -Fq 'RK_KERNEL_DEFCONFIG_FRAGMENT=aiden-zram.config' "$BOARD_CONFIG"; then
    echo "Pico Zero BoardConfig must include aiden-zram.config" >&2
    exit 1
fi

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

SYSFS="$TMP_DIR/sys/block/zram0"
DEV="$TMP_DIR/dev/zram0"
PROC_VM="$TMP_DIR/proc/sys/vm"
PROC_SWAPS="$TMP_DIR/proc/swaps"
COMMAND_LOG="$TMP_DIR/commands.log"
TEST_CONF="$TMP_DIR/aiden_boot.conf"
mkdir -p "$SYSFS" "$(dirname "$DEV")" "$PROC_VM" "$TMP_DIR/bin"
touch "$DEV"
printf '253:0\n' > "$SYSFS/dev"
printf '0\n' > "$SYSFS/disksize"
printf '0\n' > "$SYSFS/reset"
printf '[lzo] lzo-rle\n' > "$SYSFS/comp_algorithm"
printf '0 0 0 0 0 0 0 0\n' > "$SYSFS/mm_stat"
printf '60\n' > "$PROC_VM/swappiness"
printf '3\n' > "$PROC_VM/page-cluster"
printf 'Filename Type Size Used Priority\n' > "$PROC_SWAPS"
cat > "$TEST_CONF" <<'EOF'
ENABLE_ZRAM=1
ZRAM_SIZE_MB=64
ZRAM_SWAPPINESS=100
ZRAM_COMP_ALGORITHM=lzo
EOF

for command in mkswap swapon swapoff mknod; do
    cat > "$TMP_DIR/bin/$command" <<EOF
#!/bin/sh
printf '%s %s\n' '$command' "\$*" >> "$COMMAND_LOG"
EOF
    chmod +x "$TMP_DIR/bin/$command"
done

env \
    BOOT_CONF="$TEST_CONF" \
    ZRAM_SYSFS="$SYSFS" \
    ZRAM_DEVICE="$DEV" \
    PROC_SWAPS="$PROC_SWAPS" \
    PROC_VM="$PROC_VM" \
    MKSWAP_BIN="$TMP_DIR/bin/mkswap" \
    SWAPON_BIN="$TMP_DIR/bin/swapon" \
    SWAPOFF_BIN="$TMP_DIR/bin/swapoff" \
    MKNOD_BIN="$TMP_DIR/bin/mknod" \
    "$SCRIPT" start >/dev/null

if [ "$(cat "$SYSFS/disksize")" != "67108864" ]; then
    echo "S21zram did not configure a 64 MiB logical device" >&2
    exit 1
fi
if [ "$(cat "$PROC_VM/swappiness")" != "100" ] || [ "$(cat "$PROC_VM/page-cluster")" != "0" ]; then
    echo "S21zram did not apply VM tuning" >&2
    exit 1
fi
if ! grep -Fq "mkswap $DEV" "$COMMAND_LOG" || ! grep -Fq "swapon $DEV" "$COMMAND_LOG"; then
    echo "S21zram did not initialize and activate swap" >&2
    exit 1
fi

printf 'Filename Type Size Used Priority\n%s partition 65532 0 -2\n' "$DEV" > "$PROC_SWAPS"
env \
    BOOT_CONF="$TEST_CONF" \
    ZRAM_SYSFS="$SYSFS" \
    ZRAM_DEVICE="$DEV" \
    PROC_SWAPS="$PROC_SWAPS" \
    PROC_VM="$PROC_VM" \
    MKSWAP_BIN="$TMP_DIR/bin/mkswap" \
    SWAPON_BIN="$TMP_DIR/bin/swapon" \
    SWAPOFF_BIN="$TMP_DIR/bin/swapoff" \
    MKNOD_BIN="$TMP_DIR/bin/mknod" \
    "$SCRIPT" stop >/dev/null

if ! grep -Fq "swapoff $DEV" "$COMMAND_LOG"; then
    echo "S21zram did not deactivate swap" >&2
    exit 1
fi

echo "zram init tests passed"
