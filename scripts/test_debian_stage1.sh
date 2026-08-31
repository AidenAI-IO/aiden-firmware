#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stage1_dir="${repo_root}/scripts/debian-stage1"

bash -n \
    "${stage1_dir}/build.sh" \
    "${stage1_dir}/container-build-rootfs.sh" \
    "${stage1_dir}/container-assemble-images.sh" \
    "${stage1_dir}/container-audit-images.sh" \
    "${stage1_dir}/flash.sh" \
    "${stage1_dir}/audit-vendor-libs.sh" \
    "${stage1_dir}/audit-sdk-shared-libs.sh" \
    "${stage1_dir}/audit-e2fsprogs-import.sh"

grep -q 'LF_TARGET_ROOTFS=debian' \
    "${stage1_dir}/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk"
grep -q '6G(rootfs)' \
    "${stage1_dir}/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk"
bash -c 'source "$1"; test "${RK_KERNEL_CMDLINE_EXTRA}" = "net.ifnames=0"' _ \
    "${stage1_dir}/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk"
if grep -E '^export.*RK_.*=.*=' \
    "${stage1_dir}/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk"; then
    echo "Board config export contains a second literal equals sign" >&2
    exit 1
fi
grep -q 'RK_KERNEL_CMDLINE_EXTRA' \
    "${stage1_dir}/sdk-patches/0002-append-extra-kernel-cmdline.patch"
grep -q '/lib/ld-linux-armhf.so.3' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'ld-uClibc' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'debian-archive-keyring' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -q '9ea7778e443144ca490668737a8ab22dd3e748bb99e805e22ec055abeb3c7fac' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -Fq 'mount -t binfmt_misc binfmt_misc "${BINFMT_DIR}"' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -Fq '/usr/lib/systemd/systemd-binfmt' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -Fq '/usr/lib/binfmt.d/qemu-arm.conf' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -Fq 'grep -qx enabled "${BINFMT_DIR}/qemu-arm"' \
    "${stage1_dir}/container-build-rootfs.sh"
if grep -Fq 'update-binfmts --enable qemu-arm >/dev/null 2>&1 || true' \
    "${stage1_dir}/container-build-rootfs.sh"; then
    echo "Stage-1 rootfs builder silently ignores qemu-arm registration failures" >&2
    exit 1
fi
grep -Eq '^[[:space:]]*debootstrap --arch=armhf' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -q 'sshd-keygen.service' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -qx 'ConditionFirstBoot=' \
    "${stage1_dir}/overlay/etc/systemd/system/sshd-keygen.service.d/10-luckfox-stage1.conf"
grep -qx 'ConditionPathIsReadWrite=/etc/ssh' \
    "${stage1_dir}/overlay/etc/systemd/system/sshd-keygen.service.d/10-luckfox-stage1.conf"
grep -qx 'ConditionPathIsSymbolicLink=!/etc/ssh' \
    "${stage1_dir}/overlay/etc/systemd/system/sshd-keygen.service.d/10-luckfox-stage1.conf"
grep -q 'does not inherit the checkout owner' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -q 'group/other writable' \
    "${stage1_dir}/container-audit-images.sh"
grep -qx 'adb' "${stage1_dir}/packages.list"
grep -qx 'libdrm2' "${stage1_dir}/packages.list"
grep -qx 'bluez' "${stage1_dir}/packages.list"
grep -q 'Unresolved OEM DT_NEEDED' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'e2fsprogs-1.43.9-import-audit.txt' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -q 'install -d -m 0755.*ROOTFS_DIR}/oem.*ROOTFS_DIR}/userdata' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -q 'find.*ROOTFS_DIR}/dev.*-type b.*-type c.*-delete' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -q 'setid-files.txt' "${stage1_dir}/container-build-rootfs.sh"
grep -q 'arm-linux-gnueabihf-objcopy --remove-section=.debug_gdb_scripts' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -q 'elf-sanitization.txt' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -q 'dpkg --verify sqv' "${stage1_dir}/container-build-rootfs.sh"
grep -q 'arm-linux-gnueabihf-strip --strip-debug' \
    "${stage1_dir}/container-assemble-images.sh"
grep -q 'oem-elf-sanitization.tsv' \
    "${stage1_dir}/container-assemble-images.sh"
grep -q "stat -c '%u:%g:%a'.*ROOTFS_MOUNT}/oem" \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'sdk-shared-libs-inventory.tsv' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'arm32-android-bionic' \
    "${stage1_dir}/audit-sdk-shared-libs.sh"
grep -q 'Tag_ABI_VFP_args: VFP registers' \
    "${stage1_dir}/audit-sdk-shared-libs.sh"
grep -q '0xff000000.*0x05000000' \
    "${stage1_dir}/container-audit-images.sh"
grep -q '0x00000400' "${stage1_dir}/container-audit-images.sh"
grep -q 'elf-symbol-versions.tsv' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'ROOTFS_MOUNT}/usr/bin/sqv.*--version' \
    "${stage1_dir}/container-audit-images.sh"
grep -q -- '--verify sqv' "${stage1_dir}/container-audit-images.sh"
grep -q '\\.gnu_debugdata' "${stage1_dir}/container-audit-images.sh"
grep -q 'qemu-arm-static.*ROOTFS_MOUNT}/lib/ld-linux-armhf.so.3' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'oem-module-audit.tsv' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'Unresolved stage-1 autoload module dependency' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'boot-fit-audit.txt' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'cmp.*BOOT_AUDIT_DIR}/kernel.*BSP_KERNEL' \
    "${stage1_dir}/container-audit-images.sh"
grep -q '/aliases serial1.*serial@ff4b0000' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'systemd-unit-audit.txt' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'rootfs-grow-audit.txt' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'stage1-validator-help.txt' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'stage1-shellcheck.txt' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'overlay/usr/local/libexec/luckfox-ext4-size' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'partition-size-audit.tsv' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'Device node leaked into the Debian rootfs image' \
    "${stage1_dir}/container-audit-images.sh"
grep -qx 'RequiresMountsFor=/userdata' \
    "${stage1_dir}/overlay/etc/systemd/system/luckfox-stage1-report.service"
grep -qx 'Before=oem.mount userdata.mount local-fs.target' \
    "${stage1_dir}/overlay/etc/systemd/system/luckfox-rootfs-grow.service"
grep -qx 'After=systemd-fsck@dev-mmcblk0p5.service systemd-fsck@dev-mmcblk0p6.service' \
    "${stage1_dir}/overlay/etc/systemd/system/luckfox-rootfs-grow.service"
grep -qx 'ExecStart=/usr/bin/hciattach -n -s 1500000 /dev/ttyS1 any 1500000 flow nosleep' \
    "${stage1_dir}/overlay/etc/systemd/system/luckfox-bluetooth-attach.service"
grep -qx 'ExecStartPost=/usr/local/sbin/luckfox-bluetooth-hci-wait' \
    "${stage1_dir}/overlay/etc/systemd/system/luckfox-bluetooth-attach.service"
grep -q 'luckfox-bluetooth-attach.service bluetooth.service' \
    "${stage1_dir}/container-build-rootfs.sh"
grep -q 'usr/libexec/bluetooth/bluetoothd' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'Required kernel setting is not enabled' \
    "${stage1_dir}/build.sh"
grep -q 'build-container.txt' "${stage1_dir}/build.sh"
help_output=$(mktemp -d)
rmdir "${help_output}"
DEBIAN_STAGE1_OUTPUT_DIR="${help_output}" \
    "${stage1_dir}/build.sh" --help >/dev/null
test ! -e "${help_output}"
grep -q 'CONFIG_BT_HCIUART_H4 CONFIG_CRYPTO_ECDH CONFIG_CRYPTO_CMAC' \
    "${stage1_dir}/build.sh"
grep -q 'Required Bluetooth kernel setting is not enabled' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'OUTPUT_DIR}:/work/output/debian-stage1' \
    "${stage1_dir}/build.sh"
grep -q 'mkdir -p "${IMAGE_DIR}/unpacked"' \
    "${stage1_dir}/build.sh"
grep -q 'mkdir -p "${OUTPUT_DIR}/audit-unpacked"' \
    "${stage1_dir}/container-audit-images.sh"
if grep -q 'findmnt.*-T /opt' "${stage1_dir}/container-audit-images.sh"; then
    echo "The /opt image gate must not inspect the host mount namespace" >&2
    exit 1
fi
grep -q 'readlink.*ROOTFS_MOUNT}/opt.*= /userdata/opt' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'unpacked/Image/${item}' \
    "${stage1_dir}/build.sh"
grep -q 'cd "${OUTPUT_DIR}"' \
    "${stage1_dir}/container-audit-images.sh"
grep -q 'mask.*wpa_supplicant.service\|wpa_supplicant.service || true' \
    "${stage1_dir}/container-build-rootfs.sh"
cleanup_line=$(grep -n 'var/cache/apt/archives/' \
    "${stage1_dir}/container-build-rootfs.sh" | tail -1 | cut -d: -f1)
manifest_line=$(grep -n 'filesystem-manifest.txt' \
    "${stage1_dir}/container-build-rootfs.sh" | head -1 | cut -d: -f1)
test "${manifest_line}" -gt "${cleanup_line}"
test ! -e "${stage1_dir}/overlay/etc/systemd/system/ssh-keygen.service"
test -x "${stage1_dir}/build.sh"
test -x "${stage1_dir}/audit-vendor-libs.sh"
test -x "${stage1_dir}/audit-sdk-shared-libs.sh"
test -x "${stage1_dir}/audit-e2fsprogs-import.sh"
test -x "${stage1_dir}/flash.sh"
test -x "${stage1_dir}/overlay/usr/local/sbin/luckfox-wifi-init"
test -x "${stage1_dir}/overlay/usr/local/sbin/luckfox-bluetooth-hci-wait"
test -x "${stage1_dir}/overlay/usr/local/sbin/luckfox-stage1-validate"
test -x "${stage1_dir}/overlay/usr/local/libexec/luckfox-ext4-size"

ext4_size_test_dir=$(mktemp -d)
truncate -s 256M "${ext4_size_test_dir}/userdata.ext4"
/usr/sbin/mke2fs -q -t ext4 -F "${ext4_size_test_dir}/userdata.ext4"
test "$("${stage1_dir}/overlay/usr/local/libexec/luckfox-ext4-size" \
    "${ext4_size_test_dir}/userdata.ext4")" = 268435456
rm -rf "${ext4_size_test_dir}"
grep -q '/usr/local/libexec/luckfox-ext4-size' \
    "${stage1_dir}/overlay/usr/local/sbin/luckfox-stage1-validate"
if grep -q 'stat -f -c' \
    "${stage1_dir}/overlay/usr/local/sbin/luckfox-stage1-validate"; then
    echo "Stage-1 filesystem growth check must use ext4 superblock size" >&2
    exit 1
fi

grow_test_dir=$(mktemp -d)
mkdir -p "${grow_test_dir}/bin" "${grow_test_dir}/root"
cat >"${grow_test_dir}/bin/findmnt" <<'EOF'
#!/usr/bin/env sh
printf '179:7  \n'
EOF
cat >"${grow_test_dir}/bin/readlink" <<'EOF'
#!/usr/bin/env sh
printf '/sys/devices/platform/ffaa0000.mmc/mmc_host/mmc0/mmc0:0001/block/mmcblk0/mmcblk0p7\n'
EOF
cat >"${grow_test_dir}/bin/resize2fs" <<'EOF'
#!/usr/bin/env sh
printf '%s\n' "$1" >>"${GROW_TEST_CALLS}"
EOF
chmod +x "${grow_test_dir}/bin/findmnt" \
    "${grow_test_dir}/bin/readlink" "${grow_test_dir}/bin/resize2fs"
GROW_TEST_CALLS="${grow_test_dir}/resize-calls" \
PATH="${grow_test_dir}/bin:${PATH}" \
LUCKFOX_ROOTFS_GROW_MOUNT="${grow_test_dir}/root" \
LUCKFOX_ROOTFS_GROW_OEM_DEVICE="${grow_test_dir}/oem" \
LUCKFOX_ROOTFS_GROW_USERDATA_DEVICE="${grow_test_dir}/userdata" \
LUCKFOX_ROOTFS_GROW_MARKER="${grow_test_dir}/rootfs-grown" \
    "${stage1_dir}/overlay/usr/local/sbin/luckfox-rootfs-grow"
cat >"${grow_test_dir}/expected-resize-calls" <<EOF
/dev/mmcblk0p7
${grow_test_dir}/oem
${grow_test_dir}/userdata
EOF
cmp "${grow_test_dir}/expected-resize-calls" "${grow_test_dir}/resize-calls"
test "$(cat "${grow_test_dir}/rootfs-grown")" = /dev/mmcblk0p7
rm -rf "${grow_test_dir}"

"${stage1_dir}/flash.sh" --help >/dev/null
"${stage1_dir}/overlay/usr/local/sbin/luckfox-stage1-validate" --help >/dev/null
grep -q -- '--confirm-erase-all-data' "${stage1_dir}/flash.sh"
grep -q 'device_count.*-ne 1' "${stage1_dir}/flash.sh"
grep -q 'Mode=(Loader|Maskrom)' "${stage1_dir}/flash.sh"
grep -q -- '--record-identity-baseline' \
    "${stage1_dir}/overlay/usr/local/sbin/luckfox-stage1-validate"
grep -q -- '--verify-identity-baseline' \
    "${stage1_dir}/overlay/usr/local/sbin/luckfox-stage1-validate"
grep -q 'apt-get update || return' \
    "${stage1_dir}/overlay/usr/local/sbin/luckfox-stage1-validate"
grep -q 'bluetoothctl --timeout' \
    "${stage1_dir}/overlay/usr/local/sbin/luckfox-stage1-validate"

flash_test_dir=$(mktemp -d)
trap 'rm -rf "${flash_test_dir}"' EXIT
cat >"${flash_test_dir}/upgrade_tool" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
ld)
    printf '%s\n' \
        'DevNo=1 Vid=0x2207,Pid=0x110c,LocationID=1 Mode=Loader SerialNo=test'
    ;;
uf)
    printf '%s\n' called >"${FLASH_TEST_MARKER}"
    ;;
esac
EOF
chmod +x "${flash_test_dir}/upgrade_tool"
FLASH_TEST_MARKER="${flash_test_dir}/flash-called" \
    "${stage1_dir}/flash.sh" inspect \
    --tool "${flash_test_dir}/upgrade_tool" >/dev/null
test ! -e "${flash_test_dir}/flash-called"
if FLASH_TEST_MARKER="${flash_test_dir}/flash-called" \
    "${stage1_dir}/flash.sh" flash \
    --tool "${flash_test_dir}/upgrade_tool" >/dev/null 2>&1; then
    echo "Flash helper accepted a destructive action without confirmation" >&2
    exit 1
fi
test ! -e "${flash_test_dir}/flash-called"
rm -rf "${flash_test_dir}"
trap - EXIT

legacy_stage1_doc=docs/debian-stage1.md
if git -C "${repo_root}" ls-files --error-unmatch "${legacy_stage1_doc}" \
    >/dev/null 2>&1; then
    echo "Obsolete Stage 1 deployment guide must not be published as a production path" >&2
    exit 1
fi
if test -e "${repo_root}/${legacy_stage1_doc}" &&
    ! git -C "${repo_root}" check-ignore -q "${legacy_stage1_doc}"; then
    echo "Local Stage 1 history must be ignored or removed from the documentation tree" >&2
    exit 1
fi

echo "Debian stage-1 static checks passed"
