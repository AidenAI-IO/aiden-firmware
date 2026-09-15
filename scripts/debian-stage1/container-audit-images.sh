#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT=/work
readonly OUTPUT_DIR=${REPO_ROOT}/output/debian-stage1
readonly IMAGE_DIR=${OUTPUT_DIR}/image
readonly SDK_DIR=${OUTPUT_DIR}/luckfox-pico-sdk
readonly REPORT=${OUTPUT_DIR}/audit-report.txt
readonly ROOTFS_MOUNT=${OUTPUT_DIR}/audit-rootfs-mount
readonly OEM_MOUNT=${OUTPUT_DIR}/audit-oem-mount
readonly BOOT_AUDIT_DIR=${OUTPUT_DIR}/audit-boot
readonly SYSTEMD_AUDIT_DIR=${OUTPUT_DIR}/audit-systemd
readonly GROW_AUDIT_DIR=${OUTPUT_DIR}/audit-grow
readonly ELF_ABI_REPORT=${OUTPUT_DIR}/elf-abi-audit.tsv
readonly ELF_SYMBOL_REPORT=${OUTPUT_DIR}/elf-symbol-versions.tsv
readonly ELF_DEBUG_REPORT=${OUTPUT_DIR}/elf-debug-sections.tsv

mounts=()
cleanup() {
    local index
    for ((index = ${#mounts[@]} - 1; index >= 0; index--)); do
        if mountpoint -q "${mounts[index]}"; then
            umount "${mounts[index]}" || umount -l "${mounts[index]}" || true
        fi
    done
    for path in \
        "${ROOTFS_MOUNT}" "${OEM_MOUNT}" "${BOOT_AUDIT_DIR}" \
        "${SYSTEMD_AUDIT_DIR}" "${GROW_AUDIT_DIR}"; do
        if ! mountpoint -q "${path}"; then
            rm -rf "${path}"
        fi
    done
}
trap cleanup EXIT

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends \
    e2fsprogs file binutils libcap2-bin qemu-user-static device-tree-compiler \
    shellcheck

shellcheck -x \
    "${REPO_ROOT}/scripts/debian-stage1/flash.sh" \
    "${REPO_ROOT}/scripts/debian-stage1/overlay/usr/local/libexec/luckfox-ext4-size" \
    "${REPO_ROOT}/scripts/debian-stage1/overlay/usr/local/sbin/luckfox-stage1-validate" \
    "${REPO_ROOT}/scripts/debian-stage1/overlay/usr/local/sbin/luckfox-stage1-report" \
    >"${OUTPUT_DIR}/stage1-shellcheck.txt"

printf 'scope\tpath\ttype\te_flags\teabi5\thard_float_required\thard_float\tvfp_args\n' \
    >"${ELF_ABI_REPORT}"
printf 'scope\tpath\tglibc_versions\tglibcxx_versions\n' \
    >"${ELF_SYMBOL_REPORT}"
printf 'scope\tpath\tsection\n' >"${ELF_DEBUG_REPORT}"

audit_arm_elf() {
    local scope=$1
    local root=$2
    local file=$3
    local hard_float_required=$4
    local relative=${file#${root}}
    local header
    local elf_type
    local machine
    local flags_hex
    local flags_value
    local hard_float=no
    local attributes
    local vfp_args=not-declared
    local sections
    local debug_sections
    local versions
    local glibc_versions
    local glibcxx_versions

    header=$(LC_ALL=C readelf -h "${file}")
    elf_type=$(sed -n \
        's/^[[:space:]]*Type:[[:space:]]*\([^[:space:]]*\).*/\1/p' \
        <<<"${header}")
    machine=$(sed -n 's/^[[:space:]]*Machine:[[:space:]]*//p' <<<"${header}")
    flags_hex=$(sed -n \
        's/^[[:space:]]*Flags:[[:space:]]*\(0x[0-9a-fA-F]*\).*/\1/p' \
        <<<"${header}")

    if [ "${machine}" != ARM ]; then
        echo "Non-ARM ELF leaked into ${scope}: ${relative} (${machine})" >&2
        exit 1
    fi
    if [ -z "${flags_hex}" ]; then
        echo "Unable to read ARM ELF flags in ${scope}: ${relative}" >&2
        exit 1
    fi
    flags_value=$((flags_hex))
    if (( (flags_value & 0xff000000) != 0x05000000 )); then
        echo "Non-EABI5 ARM ELF leaked into ${scope}: ${relative} (${flags_hex})" >&2
        exit 1
    fi

    if [ "${hard_float_required}" = auto ]; then
        if [ "${elf_type}" = REL ]; then
            hard_float_required=no
        else
            hard_float_required=yes
        fi
    fi
    if (( (flags_value & 0x00000400) != 0 )); then
        hard_float=yes
    fi
    if [ "${hard_float_required}" = yes ] \
        && { [ "${hard_float}" != yes ] \
            || (( (flags_value & 0x00000200) != 0 )); }; then
        echo "ARM ELF is not unambiguously hard-float in ${scope}: ${relative} (${flags_hex})" >&2
        exit 1
    fi

    attributes=$(LC_ALL=C readelf -A "${file}" 2>/dev/null || true)
    if grep -q 'Tag_ABI_VFP_args:' <<<"${attributes}"; then
        vfp_args=$(sed -n \
            's/^[[:space:]]*Tag_ABI_VFP_args:[[:space:]]*//p' \
            <<<"${attributes}" | head -n 1)
        if [ "${hard_float_required}" = yes ] \
            && [ "${vfp_args}" != 'VFP registers' ]; then
            echo "ARM attributes contradict hard-float ABI in ${scope}: ${relative} (${vfp_args})" >&2
            exit 1
        fi
    fi

    sections=$(LC_ALL=C readelf -SW "${file}" 2>/dev/null \
        | sed -n \
            's/^[[:space:]]*\[[[:space:]]*[0-9][0-9]*\][[:space:]]\+\([^[:space:]]\+\).*/\1/p')
    debug_sections=$(grep -E \
        '^(\.debug.*|\.zdebug.*|\.gdb_index|\.gnu_debugdata)$' \
        <<<"${sections}" || true)
    if [ -n "${debug_sections}" ]; then
        while IFS= read -r section; do
            printf '%s\t%s\t%s\n' "${scope}" "${relative}" "${section}" \
                >>"${ELF_DEBUG_REPORT}"
        done <<<"${debug_sections}"
        echo "Unapproved debug section in ${scope}: ${relative} ($(paste -sd, <<<"${debug_sections}"))" >&2
        exit 1
    fi

    versions=$(LC_ALL=C readelf --version-info "${file}" 2>/dev/null || true)
    glibc_versions=$(grep -oE 'GLIBC_([0-9]+\.)*[0-9]+|GLIBC_PRIVATE' \
        <<<"${versions}" | sort -uV | paste -sd, - || true)
    glibcxx_versions=$(grep -oE 'GLIBCXX_([0-9]+\.)*[0-9]+' \
        <<<"${versions}" | sort -uV | paste -sd, - || true)

    printf '%s\t%s\t%s\t%s\tyes\t%s\t%s\t%s\n' \
        "${scope}" "${relative}" "${elf_type}" "${flags_hex}" \
        "${hard_float_required}" "${hard_float}" "${vfp_args}" \
        >>"${ELF_ABI_REPORT}"
    printf '%s\t%s\t%s\t%s\n' \
        "${scope}" "${relative}" "${glibc_versions}" "${glibcxx_versions}" \
        >>"${ELF_SYMBOL_REPORT}"
}

"${REPO_ROOT}/scripts/debian-stage1/audit-vendor-libs.sh" \
    "${SDK_DIR}" "${OUTPUT_DIR}/vendor-libs-audit.tsv"
"${REPO_ROOT}/scripts/debian-stage1/audit-sdk-shared-libs.sh" \
    "${SDK_DIR}" "${OUTPUT_DIR}/sdk-shared-libs-inventory.tsv"

: >"${REPORT}"
for image in rootfs.img oem.img userdata.img; do
    echo "## ${image}" >>"${REPORT}"
    e2fsck -fn "${IMAGE_DIR}/${image}" >>"${REPORT}" 2>&1
    dumpe2fs -h "${IMAGE_DIR}/${image}" 2>/dev/null \
        | grep -E 'Filesystem volume name|Filesystem UUID|Filesystem features|Inode count|Block count|Block size' \
        >>"${REPORT}"
done

rootfs_features=$(dumpe2fs -h "${IMAGE_DIR}/rootfs.img" 2>/dev/null \
    | sed -n 's/^Filesystem features:[[:space:]]*//p')
for forbidden in 64bit huge_file metadata_csum metadata_csum_seed orphan_file quota dir_index; do
    if grep -qw "${forbidden}" <<<"${rootfs_features}"; then
        echo "Forbidden rootfs ext4 feature enabled: ${forbidden}" >&2
        exit 1
    fi
done

declare -A partition_limits=(
    [env.img]=$((32 * 1024))
    [idblock.img]=$((512 * 1024))
    [uboot.img]=$((256 * 1024))
    [boot.img]=$((32 * 1024 * 1024))
    [oem.img]=$((512 * 1024 * 1024))
    [userdata.img]=$((256 * 1024 * 1024))
    [rootfs.img]=$((6 * 1024 * 1024 * 1024))
)
printf 'image\tsize_bytes\tpartition_limit_bytes\theadroom_bytes\n' \
    >"${OUTPUT_DIR}/partition-size-audit.tsv"
for image in env.img idblock.img uboot.img boot.img oem.img userdata.img rootfs.img; do
    size=$(stat -c %s "${IMAGE_DIR}/${image}")
    limit=${partition_limits[${image}]}
    if [ "${size}" -gt "${limit}" ]; then
        echo "Image exceeds its factory partition: ${image} (${size} > ${limit})" >&2
        exit 1
    fi
    printf '%s\t%s\t%s\t%s\n' \
        "${image}" "${size}" "${limit}" "$((limit - size))" \
        >>"${OUTPUT_DIR}/partition-size-audit.tsv"
done

mkdir -p "${ROOTFS_MOUNT}" "${OEM_MOUNT}"
mount -o loop,ro "${IMAGE_DIR}/rootfs.img" "${ROOTFS_MOUNT}"
mounts+=("${ROOTFS_MOUNT}")

test -x "${ROOTFS_MOUNT}/lib/systemd/systemd"
test -x "${ROOTFS_MOUNT}/usr/sbin/sshd"
test -x "${ROOTFS_MOUNT}/usr/bin/adb"
test -x "${ROOTFS_MOUNT}/usr/bin/hciattach"
test -x "${ROOTFS_MOUNT}/usr/libexec/bluetooth/bluetoothd"
test -e "${ROOTFS_MOUNT}/usr/lib/arm-linux-gnueabihf/libdrm.so.2"
test "$(stat -c '%u:%g:%a' "${ROOTFS_MOUNT}/tmp")" = 0:0:1777
test "$(stat -c '%u:%g:%a' "${ROOTFS_MOUNT}/var/tmp")" = 0:0:1777
test "$(stat -c '%u:%g:%a' "${ROOTFS_MOUNT}/oem")" = 0:0:755
test "$(stat -c '%u:%g:%a' "${ROOTFS_MOUNT}/userdata")" = 0:0:755
test -L "${ROOTFS_MOUNT}/etc/systemd/system/getty.target.wants/serial-getty@ttyFIQ0.service"
test "$(readlink "${ROOTFS_MOUNT}/etc/systemd/system/getty.target.wants/serial-getty@ttyFIQ0.service")" \
    = /lib/systemd/system/serial-getty@.service
test -L "${ROOTFS_MOUNT}/etc/systemd/system/ssh.service.wants/sshd-keygen.service"
test "$(readlink "${ROOTFS_MOUNT}/etc/systemd/system/ssh.service.wants/sshd-keygen.service")" \
    = /usr/lib/systemd/system/sshd-keygen.service
test -f "${ROOTFS_MOUNT}/etc/systemd/system/sshd-keygen.service.d/10-luckfox-stage1.conf"
grep -qx 'ConditionFirstBoot=' \
    "${ROOTFS_MOUNT}/etc/systemd/system/sshd-keygen.service.d/10-luckfox-stage1.conf"
test -L "${ROOTFS_MOUNT}/etc/systemd/system/multi-user.target.wants/wpa_supplicant@wlan0.service"
test "$(readlink "${ROOTFS_MOUNT}/etc/systemd/system/multi-user.target.wants/wpa_supplicant@wlan0.service")" \
    = /lib/systemd/system/wpa_supplicant@.service
test -L "${ROOTFS_MOUNT}/etc/systemd/system/multi-user.target.wants/luckfox-oem-ldconfig.service"
test -L "${ROOTFS_MOUNT}/etc/systemd/system/multi-user.target.wants/luckfox-bluetooth-attach.service"
test "$(readlink "${ROOTFS_MOUNT}/etc/systemd/system/multi-user.target.wants/luckfox-bluetooth-attach.service")" \
    = /etc/systemd/system/luckfox-bluetooth-attach.service
test -L "${ROOTFS_MOUNT}/etc/systemd/system/bluetooth.target.wants/bluetooth.service"
test "$(readlink "${ROOTFS_MOUNT}/etc/systemd/system/bluetooth.target.wants/bluetooth.service")" \
    = /usr/lib/systemd/system/bluetooth.service
test -L "${ROOTFS_MOUNT}/etc/systemd/system/dbus-org.bluez.service"
test "$(readlink "${ROOTFS_MOUNT}/etc/systemd/system/dbus-org.bluez.service")" \
    = /usr/lib/systemd/system/bluetooth.service
test "$(readlink "${ROOTFS_MOUNT}/etc/systemd/system/wpa_supplicant.service")" = /dev/null
if find "${ROOTFS_MOUNT}/etc/ssh" -maxdepth 1 -name 'ssh_host_*' -print -quit \
    | grep -q .; then
    echo "Build-time SSH host key leaked into the Debian rootfs" >&2
    exit 1
fi
test ! -s "${ROOTFS_MOUNT}/etc/machine-id"
test ! -e "${ROOTFS_MOUNT}/var/lib/systemd/random-seed"
test ! -e "${ROOTFS_MOUNT}/usr/bin/qemu-arm-static"
if awk '
    /^[[:space:]]*(#|$)/ {next}
    $1 == "/userdata/opt" || $2 == "/userdata/opt" ||
        ($2 == "/opt" && $1 ~ /(^|\/)userdata\/opt$/) {found = 1}
    END {exit !found}
' "${ROOTFS_MOUNT}/etc/fstab" \
    || grep -R -l -F '/userdata/opt' \
        --include='*.mount' --include='*.service' \
        "${ROOTFS_MOUNT}/etc/systemd/system" \
        "${ROOTFS_MOUNT}/usr/lib/systemd/system" 2>/dev/null | grep -q . \
    || { [ -L "${ROOTFS_MOUNT}/opt" ] \
        && { [ "$(readlink "${ROOTFS_MOUNT}/opt")" = /userdata/opt ] \
            || [ "$(readlink -m "${ROOTFS_MOUNT}/opt")" \
                = "${ROOTFS_MOUNT}/userdata/opt" ]; }; }; then
    echo "Debian rootfs must not bind or mount Buildroot /userdata/opt" >&2
    exit 1
fi
if find "${ROOTFS_MOUNT}" -xdev \( -type b -o -type c \) -print -quit \
    | grep -q .; then
    echo "Device node leaked into the Debian rootfs image" >&2
    exit 1
fi
if find "${ROOTFS_MOUNT}/var/lib/apt/lists" -type f -print -quit | grep -q .; then
    echo "APT package lists were not cleaned from the Debian rootfs" >&2
    exit 1
fi
if find "${ROOTFS_MOUNT}/var/cache/apt/archives" -type f -name '*.deb' -print -quit \
    | grep -q .; then
    echo "APT package cache was not cleaned from the Debian rootfs" >&2
    exit 1
fi
if find \
    "${ROOTFS_MOUNT}/etc/fstab" \
    "${ROOTFS_MOUNT}/etc/hostname" \
    "${ROOTFS_MOUNT}/etc/hosts" \
    "${ROOTFS_MOUNT}/etc/ssh/sshd_config.d/10-luckfox-stage1.conf" \
    "${ROOTFS_MOUNT}/etc/systemd/journald.conf.d/10-luckfox-stage1.conf" \
    "${ROOTFS_MOUNT}/etc/systemd/network/20-wlan0.network" \
    "${ROOTFS_MOUNT}/etc/systemd/resolved.conf.d/10-luckfox-stage1.conf" \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-oem-ldconfig.service" \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-bluetooth-attach.service" \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-rootfs-grow.service" \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-stage1-report.service" \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-wifi.service" \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-zram.service" \
    "${ROOTFS_MOUNT}/etc/tmpfiles.d/luckfox-stage1.conf" \
    "${ROOTFS_MOUNT}/etc/wpa_supplicant/wpa_supplicant-wlan0.conf" \
    "${ROOTFS_MOUNT}/usr/local/libexec/luckfox-ext4-size" \
    "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-rootfs-grow" \
    "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-bluetooth-hci-wait" \
    "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-stage1-report" \
    "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-stage1-validate" \
    "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-wifi-init" \
    "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-zram-start" \
    "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-zram-stop" \
    "${ROOTFS_MOUNT}/usr/local/share/luckfox-debian-stage1/README" \
    -type f -perm /022 -print -quit | grep -q .; then
    echo "Stage-1 configuration or helper is group/other writable" >&2
    exit 1
fi
grep -q '^root:!' "${ROOTFS_MOUNT}/etc/shadow"
grep -q '^luckfox:x:1000:1000:' "${ROOTFS_MOUNT}/etc/passwd"
grep -q '^luckfox:.*:1000:' "${ROOTFS_MOUNT}/etc/group"
grep -qx 'RequiresMountsFor=/userdata' \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-stage1-report.service"
grep -qx 'Before=oem.mount userdata.mount local-fs.target' \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-rootfs-grow.service"
grep -qx 'After=systemd-fsck@dev-mmcblk0p5.service systemd-fsck@dev-mmcblk0p6.service' \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-rootfs-grow.service"
grep -qx 'ExecStart=/usr/bin/hciattach -n -s 1500000 /dev/ttyS1 any 1500000 flow nosleep' \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-bluetooth-attach.service"
grep -qx 'Before=bluetooth.service' \
    "${ROOTFS_MOUNT}/etc/systemd/system/luckfox-bluetooth-attach.service"
test -x "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-stage1-validate"
test -x "${ROOTFS_MOUNT}/usr/local/libexec/luckfox-ext4-size"
grep -q '/usr/local/libexec/luckfox-ext4-size' \
    "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-stage1-validate"
qemu-arm-static -L "${ROOTFS_MOUNT}" \
    "${ROOTFS_MOUNT}/bin/sh" \
    "${ROOTFS_MOUNT}/usr/local/sbin/luckfox-stage1-validate" --help \
    >"${OUTPUT_DIR}/stage1-validator-help.txt"
grep -q '^Usage: luckfox-stage1-validate' \
    "${OUTPUT_DIR}/stage1-validator-help.txt"
for package in bluez; do
    if ! awk -F '\t' -v package="${package}" \
        '$1 == package || $1 == package ":armhf" {found = 1} END {exit !found}' \
        "${OUTPUT_DIR}/packages.txt"; then
        echo "Required Debian stage-1 package is missing: ${package}" >&2
        exit 1
    fi
done
for package in \
    dhcpcd dhcpcd-base isc-dhcp-client net-tools opkg \
    flash-kernel unattended-upgrades; do
    if awk -F '\t' -v package="${package}" \
        '$1 == package || $1 == package ":armhf" {found = 1} END {exit !found}' \
        "${OUTPUT_DIR}/packages.txt"; then
        echo "Forbidden Debian stage-1 package is installed: ${package}" >&2
        exit 1
    fi
done
if awk -F '\t' \
    '$1 ~ /^(linux-image-|u-boot|initramfs-tools)/ {print; found = 1} END {exit !found}' \
    "${OUTPUT_DIR}/packages.txt"; then
    echo "Forbidden boot-stack package is installed in the Debian rootfs" >&2
    exit 1
fi
for account in _apt messagebus sshd; do
    grep -q "^${account}:" "${ROOTFS_MOUNT}/etc/passwd"
done
for group in messagebus systemd-journal; do
    grep -q "^${group}:" "${ROOTFS_MOUNT}/etc/group"
done

readonly KERNEL_CONFIG=${SDK_DIR}/sysdrv/source/objs_kernel/.config
for symbol in \
    CONFIG_RFKILL CONFIG_BT CONFIG_BT_BREDR CONFIG_BT_RFCOMM \
    CONFIG_BT_RFCOMM_TTY CONFIG_BT_LE CONFIG_BT_HCIUART \
    CONFIG_BT_HCIUART_H4 CONFIG_CRYPTO_ECDH CONFIG_CRYPTO_CMAC; do
    if ! grep -qx "${symbol}=y" "${KERNEL_CONFIG}"; then
        echo "Required Bluetooth kernel setting is not enabled: ${symbol}" >&2
        exit 1
    fi
done

while IFS= read -r -d '' file; do
    if ! head -c 4 "${file}" | grep -q $'\177ELF'; then
        continue
    fi
    audit_arm_elf rootfs "${ROOTFS_MOUNT}" "${file}" yes
    dynamic=$(readelf -d "${file}" 2>/dev/null || true)
    interpreter=$(readelf -l "${file}" 2>/dev/null \
        | sed -n 's/.*Requesting program interpreter: \([^]]*\).*/\1/p')
    if grep -qE 'libc\.so\.0|ld-uClibc' <<<"${dynamic} ${interpreter}"; then
        echo "uClibc ELF leaked into Debian rootfs: ${file#${ROOTFS_MOUNT}}" >&2
        exit 1
    fi
    if [ -n "${interpreter}" ] && [ "${interpreter}" != /lib/ld-linux-armhf.so.3 ]; then
        echo "Unexpected interpreter in ${file#${ROOTFS_MOUNT}}: ${interpreter}" >&2
        exit 1
    fi
    if grep -qE '\((RPATH|RUNPATH)\).*(/work|/home|/tmp)' <<<"${dynamic}"; then
        echo "Build/writable path leaked into ELF search path: ${file#${ROOTFS_MOUNT}}" >&2
        exit 1
    fi
done < <(find "${ROOTFS_MOUNT}" -xdev -type f -print0)

qemu-arm-static -L "${ROOTFS_MOUNT}" \
    "${ROOTFS_MOUNT}/usr/bin/sqv" --version \
    >"${OUTPUT_DIR}/sqv-version.txt"
test -s "${OUTPUT_DIR}/sqv-version.txt"
qemu-arm-static -L "${ROOTFS_MOUNT}" \
    "${ROOTFS_MOUNT}/usr/bin/dpkg" --root="${ROOTFS_MOUNT}" --verify sqv \
    >"${OUTPUT_DIR}/dpkg-verify-sqv.txt"
if [ -s "${OUTPUT_DIR}/dpkg-verify-sqv.txt" ]; then
    echo "sqv differs from its post-sanitization dpkg checksum" >&2
    cat "${OUTPUT_DIR}/dpkg-verify-sqv.txt" >&2
    exit 1
fi

mkdir -p "${SYSTEMD_AUDIT_DIR}"/{generator,generator.early,generator.late,tmp}
: >"${OUTPUT_DIR}/systemd-unit-audit.txt"
cp "${ROOTFS_MOUNT}/etc/fstab" "${SYSTEMD_AUDIT_DIR}/fstab"
cp /usr/bin/qemu-arm-static "${SYSTEMD_AUDIT_DIR}/qemu-arm-static"
mount --bind "${SYSTEMD_AUDIT_DIR}" "${ROOTFS_MOUNT}/run"
mounts+=("${ROOTFS_MOUNT}/run")
chroot "${ROOTFS_MOUNT}" /run/qemu-arm-static \
    -E SYSTEMD_SYSFS_CHECK=false -E SYSTEMD_FSTAB=/run/fstab \
    /usr/lib/systemd/system-generators/systemd-fstab-generator \
    /run/generator /run/generator.early /run/generator.late \
    >>"${OUTPUT_DIR}/systemd-unit-audit.txt" 2>&1
for unit in -.mount oem.mount userdata.mount tmp.mount; do
    test -f "${SYSTEMD_AUDIT_DIR}/generator/${unit}"
done
grep -qx 'What=/dev/mmcblk0p5' "${SYSTEMD_AUDIT_DIR}/generator/oem.mount"
grep -qx 'What=/dev/mmcblk0p6' "${SYSTEMD_AUDIT_DIR}/generator/userdata.mount"
grep -qx 'What=tmpfs' "${SYSTEMD_AUDIT_DIR}/generator/tmp.mount"
grep -qw 'mode=1777' "${SYSTEMD_AUDIT_DIR}/generator/tmp.mount"
qemu-arm-static -L "${ROOTFS_MOUNT}" \
    "${ROOTFS_MOUNT}/usr/bin/systemd-analyze" --version \
    >>"${OUTPUT_DIR}/systemd-unit-audit.txt"
SYSTEMD_UNIT_PATH=/run/generator:/run/generator.early:/run/generator.late:/etc/systemd/system:/run/systemd/system:/usr/local/lib/systemd/system:/usr/lib/systemd/system \
TMPDIR=/run/tmp \
qemu-arm-static -L "${ROOTFS_MOUNT}" \
    "${ROOTFS_MOUNT}/usr/bin/systemd-analyze" \
    --root="${ROOTFS_MOUNT}" --generators=no verify \
    /etc/systemd/system/luckfox-rootfs-grow.service \
    /etc/systemd/system/luckfox-oem-ldconfig.service \
    /etc/systemd/system/luckfox-wifi.service \
    /etc/systemd/system/luckfox-bluetooth-attach.service \
    /etc/systemd/system/luckfox-zram.service \
    /etc/systemd/system/luckfox-stage1-report.service \
    /usr/lib/systemd/system/sshd-keygen.service \
    >>"${OUTPUT_DIR}/systemd-unit-audit.txt" 2>&1
echo "systemd unit and generated fstab mount verification passed" \
    >>"${OUTPUT_DIR}/systemd-unit-audit.txt"
umount "${ROOTFS_MOUNT}/run"
mounts=("${ROOTFS_MOUNT}")

mount -o loop,ro "${IMAGE_DIR}/oem.img" "${OEM_MOUNT}"
mounts+=("${OEM_MOUNT}")

while IFS= read -r -d '' file; do
    if head -c 4 "${file}" | grep -q $'\177ELF'; then
        audit_arm_elf oem "${OEM_MOUNT}" "${file}" auto
    fi
done < <(find "${OEM_MOUNT}" -xdev -type f -print0)

KERNEL_RELEASE=$(cat "${SDK_DIR}/sysdrv/source/objs_kernel/include/config/kernel.release")
readonly KERNEL_RELEASE
test "${KERNEL_RELEASE}" = 5.10.160
declare -A oem_modules=()
while IFS= read -r -d '' module; do
    oem_modules["$(basename "${module}" .ko)"]=${module}
done < <(find "${OEM_MOUNT}/usr/ko" -maxdepth 1 -type f -name '*.ko' -print0)

printf 'module\tvermagic\tdepends\tstage1_autoload\tdependency_status\n' \
    >"${OUTPUT_DIR}/oem-module-audit.tsv"
while IFS= read -r -d '' module; do
    module_name=$(basename "${module}" .ko)
    machine=$(readelf -h "${module}" 2>/dev/null \
        | sed -n 's/^[[:space:]]*Machine:[[:space:]]*//p')
    elf_type=$(readelf -h "${module}" 2>/dev/null \
        | sed -n 's/^[[:space:]]*Type:[[:space:]]*\([^[:space:]]*\).*/\1/p')
    vermagic=$(strings -a "${module}" | sed -n 's/^vermagic=//p' | head -n 1)
    dependencies=$(strings -a "${module}" | sed -n 's/^depends=//p' | head -n 1)
    autoload=no
    case "${module_name}" in
    libarc4|ctr|ccm|aes_generic|cfg80211|aic8800_bsp|aic8800_fdrv|aic8800_btlpm)
        autoload=yes
        ;;
    esac

    test "${machine}" = ARM
    test "${elf_type}" = REL
    if [[ "${vermagic}" != "${KERNEL_RELEASE} "* ]]; then
        echo "OEM module vermagic does not match ${KERNEL_RELEASE}: ${module_name}" >&2
        exit 1
    fi

    dependency_status=resolved
    IFS=',' read -r -a dependency_names <<<"${dependencies}"
    for dependency in "${dependency_names[@]}"; do
        [ -n "${dependency}" ] || continue
        if [ -z "${oem_modules[${dependency}]:-}" ]; then
            dependency_status="missing:${dependency}"
            if [ "${autoload}" = yes ]; then
                echo "Unresolved stage-1 autoload module dependency ${dependency}: ${module_name}" >&2
                exit 1
            fi
        fi
    done
    printf '%s\t%s\t%s\t%s\t%s\n' \
        "${module_name}" "${vermagic}" "${dependencies}" "${autoload}" \
        "${dependency_status}" >>"${OUTPUT_DIR}/oem-module-audit.tsv"
done < <(find "${OEM_MOUNT}/usr/ko" -maxdepth 1 -type f -name '*.ko' -print0 | sort -z)

for module in \
    libarc4.ko ctr.ko ccm.ko aes_generic.ko cfg80211.ko \
    aic8800_bsp.ko aic8800_fdrv.ko aic8800_btlpm.ko; do
    test -f "${OEM_MOUNT}/usr/ko/${module}"
done
test -s "${OEM_MOUNT}/usr/ko/aic8800dc_fw/fmacfw_patch_8800dc_u02.bin"

declare -A runtime_libraries=()
while IFS= read -r -d '' library; do
    runtime_libraries["$(basename "${library}")"]=${library}
done < <(find \
    "${ROOTFS_MOUNT}/lib" "${ROOTFS_MOUNT}/usr/lib" "${OEM_MOUNT}/usr/lib" \
    \( -type f -o -type l \) -name '*.so*' -print0)

: >"${OUTPUT_DIR}/oem-elf-audit.tsv"
printf 'path\tsoname\tneeded\n' >"${OUTPUT_DIR}/oem-elf-audit.tsv"
: >"${OUTPUT_DIR}/oem-loader-audit.txt"
while IFS= read -r -d '' file; do
    if ! head -c 4 "${file}" | grep -q $'\177ELF'; then
        continue
    fi
    elf_type=$(readelf -h "${file}" 2>/dev/null \
        | sed -n 's/^[[:space:]]*Type:[[:space:]]*\([^[:space:]]*\).*/\1/p')
    if [ "${elf_type}" != DYN ] && [ "${elf_type}" != EXEC ]; then
        continue
    fi
    dynamic=$(readelf -d "${file}" 2>/dev/null || true)
    machine=$(readelf -h "${file}" 2>/dev/null \
        | sed -n 's/^[[:space:]]*Machine:[[:space:]]*//p')
    if [ "${machine}" != ARM ]; then
        echo "Non-ARM ELF leaked into OEM: ${file#${OEM_MOUNT}} (${machine})" >&2
        exit 1
    fi
    if grep -qE 'libc\.so\.0|ld-uClibc' <<<"${dynamic}"; then
        echo "uClibc ELF leaked into OEM: ${file#${OEM_MOUNT}}" >&2
        exit 1
    fi
    if grep -qE '\((RPATH|RUNPATH)\)' <<<"${dynamic}"; then
        echo "OEM ELF contains an unapproved RPATH/RUNPATH: ${file#${OEM_MOUNT}}" >&2
        exit 1
    fi
    soname=$(sed -n 's/.*(SONAME).*\[\([^]]*\)\].*/\1/p' <<<"${dynamic}")
    needed=$(sed -n 's/.*(NEEDED).*\[\([^]]*\)\].*/\1/p' <<<"${dynamic}" | paste -sd, -)
    printf '%s\t%s\t%s\n' "${file#${OEM_MOUNT}}" "${soname}" "${needed}" \
        >>"${OUTPUT_DIR}/oem-elf-audit.tsv"
    while IFS= read -r dependency; do
        [ -n "${dependency}" ] || continue
        if [ -z "${runtime_libraries[${dependency}]:-}" ]; then
            echo "Unresolved OEM DT_NEEDED ${dependency}: ${file#${OEM_MOUNT}}" >&2
            exit 1
        fi
    done < <(sed -n 's/.*(NEEDED).*\[\([^]]*\)\].*/\1/p' <<<"${dynamic}")

    {
        printf '## %s\n' "${file#${OEM_MOUNT}}"
        qemu-arm-static "${ROOTFS_MOUNT}/lib/ld-linux-armhf.so.3" \
            --library-path \
            "${OEM_MOUNT}/usr/lib:${ROOTFS_MOUNT}/lib/arm-linux-gnueabihf:${ROOTFS_MOUNT}/usr/lib/arm-linux-gnueabihf:${ROOTFS_MOUNT}/lib:${ROOTFS_MOUNT}/usr/lib" \
            --list "${file}"
    } >>"${OUTPUT_DIR}/oem-loader-audit.txt"
done < <(find "${OEM_MOUNT}" -xdev -type f -print0)

umount "${OEM_MOUNT}"
umount "${ROOTFS_MOUNT}"
mounts=()

mkdir -p "${GROW_AUDIT_DIR}/root/var/lib/luckfox"
cp --reflink=auto "${IMAGE_DIR}/rootfs.img" "${GROW_AUDIT_DIR}/rootfs.img"
cp --reflink=auto "${IMAGE_DIR}/oem.img" "${GROW_AUDIT_DIR}/oem.img"
cp --reflink=auto "${IMAGE_DIR}/userdata.img" "${GROW_AUDIT_DIR}/userdata.img"
truncate -s +64M "${GROW_AUDIT_DIR}/rootfs.img"
truncate -s +16M "${GROW_AUDIT_DIR}/oem.img"
truncate -s +16M "${GROW_AUDIT_DIR}/userdata.img"
root_loop=
oem_loop=
userdata_loop=
cleanup_grow_loops() {
    local loop
    for loop in "${userdata_loop}" "${oem_loop}" "${root_loop}"; do
        [ -n "${loop}" ] && losetup -d "${loop}" 2>/dev/null || true
    done
}
trap 'cleanup_grow_loops; cleanup' EXIT
losetup --find --show "${GROW_AUDIT_DIR}/rootfs.img" \
    >"${GROW_AUDIT_DIR}/rootfs.loop"
read -r root_loop <"${GROW_AUDIT_DIR}/rootfs.loop"
losetup --find --show "${GROW_AUDIT_DIR}/oem.img" \
    >"${GROW_AUDIT_DIR}/oem.loop"
read -r oem_loop <"${GROW_AUDIT_DIR}/oem.loop"
losetup --find --show "${GROW_AUDIT_DIR}/userdata.img" \
    >"${GROW_AUDIT_DIR}/userdata.loop"
read -r userdata_loop <"${GROW_AUDIT_DIR}/userdata.loop"
LUCKFOX_ROOTFS_GROW_MOUNT="${GROW_AUDIT_DIR}/root" \
LUCKFOX_ROOTFS_GROW_ROOT_DEVICE="${root_loop}" \
LUCKFOX_ROOTFS_GROW_OEM_DEVICE="${oem_loop}" \
LUCKFOX_ROOTFS_GROW_USERDATA_DEVICE="${userdata_loop}" \
LUCKFOX_ROOTFS_GROW_MARKER="${GROW_AUDIT_DIR}/root/var/lib/luckfox/rootfs-grown" \
    "${REPO_ROOT}/scripts/debian-stage1/overlay/usr/local/sbin/luckfox-rootfs-grow" \
    >"${OUTPUT_DIR}/rootfs-grow-audit.txt" 2>&1
test "$(cat "${GROW_AUDIT_DIR}/root/var/lib/luckfox/rootfs-grown")" = "${root_loop}"
for image in rootfs oem userdata; do
    e2fsck -fn "${GROW_AUDIT_DIR}/${image}.img" \
        >>"${OUTPUT_DIR}/rootfs-grow-audit.txt" 2>&1
done
cleanup_grow_loops
trap cleanup EXIT

mkdir -p "${BOOT_AUDIT_DIR}"
readonly DUMPIMAGE=${SDK_DIR}/sysdrv/source/uboot/u-boot/tools/dumpimage
readonly BSP_DTB=${SDK_DIR}/output/out/sysdrv_out/board_uclibc_rv1106/rv1106g-luckfox-pico-zero.dtb
readonly BSP_KERNEL=${SDK_DIR}/sysdrv/source/objs_kernel/arch/arm/boot/zImage
"${DUMPIMAGE}" -l "${IMAGE_DIR}/boot.img" >"${OUTPUT_DIR}/boot-fit-audit.txt"
"${DUMPIMAGE}" -i "${IMAGE_DIR}/boot.img" -T flat_dt -p 0 \
    -o "${BOOT_AUDIT_DIR}/fdt" unused >>"${OUTPUT_DIR}/boot-fit-audit.txt"
"${DUMPIMAGE}" -i "${IMAGE_DIR}/boot.img" -T flat_dt -p 1 \
    -o "${BOOT_AUDIT_DIR}/kernel" unused >>"${OUTPUT_DIR}/boot-fit-audit.txt"
"${DUMPIMAGE}" -i "${IMAGE_DIR}/boot.img" -T flat_dt -p 2 \
    -o "${BOOT_AUDIT_DIR}/resource" unused >>"${OUTPUT_DIR}/boot-fit-audit.txt"
cmp "${BOOT_AUDIT_DIR}/fdt" "${BSP_DTB}"
cmp "${BOOT_AUDIT_DIR}/kernel" "${BSP_KERNEL}"
test "$(fdtget -t s "${BOOT_AUDIT_DIR}/fdt" / model)" = 'Luckfox Pico Zero'
test "$(fdtget -t s "${BOOT_AUDIT_DIR}/fdt" /aliases serial1)" = /serial@ff4b0000
test "$(fdtget -t s "${BOOT_AUDIT_DIR}/fdt" /serial@ff4b0000 status)" = okay
dt_bootargs=$(fdtget -t s "${BOOT_AUDIT_DIR}/fdt" /chosen bootargs)
grep -qw 'root=/dev/mmcblk0p7' <<<"${dt_bootargs}"
grep -qw 'rootwait' <<<"${dt_bootargs}"
grep -qx \
    'sys_bootargs= root=/dev/mmcblk0p7 rootfstype=ext4 rk_dma_heap_cma=100M net.ifnames=0' \
    "${OUTPUT_DIR}/bsp-env.txt"
{
    printf '\nembedded_dtb_sha256='
    sha256sum "${BOOT_AUDIT_DIR}/fdt" | awk '{print $1}'
    printf 'bsp_dtb_sha256='
    sha256sum "${BSP_DTB}" | awk '{print $1}'
    printf 'embedded_kernel_sha256='
    sha256sum "${BOOT_AUDIT_DIR}/kernel" | awk '{print $1}'
    printf 'bsp_kernel_sha256='
    sha256sum "${BSP_KERNEL}" | awk '{print $1}'
    printf 'dt_model=%s\n' "$(fdtget -t s "${BOOT_AUDIT_DIR}/fdt" / model)"
    printf 'dt_serial1=%s\n' "$(fdtget -t s "${BOOT_AUDIT_DIR}/fdt" /aliases serial1)"
    printf 'dt_serial1_status=%s\n' \
        "$(fdtget -t s "${BOOT_AUDIT_DIR}/fdt" /serial@ff4b0000 status)"
    printf 'dt_bootargs=%s\n' "${dt_bootargs}"
    printf 'uboot_sys_bootargs=%s\n' "$(sed -n 's/^sys_bootargs=//p' "${OUTPUT_DIR}/bsp-env.txt")"
    printf 'kernel_release=%s\n' "${KERNEL_RELEASE}"
} >>"${OUTPUT_DIR}/boot-fit-audit.txt"

rm -rf "${OUTPUT_DIR}/audit-unpacked"
mkdir -p "${OUTPUT_DIR}/audit-unpacked"
"${SDK_DIR}/tools/linux/Linux_Pack_Firmware/mk-update_unpack.sh" \
    -i "${IMAGE_DIR}/update.img" -o "${OUTPUT_DIR}/audit-unpacked" >>"${REPORT}" 2>&1
for item in env.img idblock.img uboot.img boot.img oem.img userdata.img rootfs.img; do
    cmp "${IMAGE_DIR}/${item}" "${OUTPUT_DIR}/audit-unpacked/Image/${item}"
done
cmp "${IMAGE_DIR}/download.bin" "${OUTPUT_DIR}/audit-unpacked/download.bin"

(
    cd "${OUTPUT_DIR}"
    sha256sum image/*.{img,bin} packages.txt >SHA256SUMS
)
echo "Audit passed" >>"${REPORT}"

chown "${HOST_UID:-0}:${HOST_GID:-0}" \
    "${REPORT}" "${OUTPUT_DIR}/vendor-libs-audit.tsv" \
    "${OUTPUT_DIR}/sdk-shared-libs-inventory.tsv" \
    "${OUTPUT_DIR}/oem-elf-audit.tsv" "${OUTPUT_DIR}/oem-loader-audit.txt" \
    "${OUTPUT_DIR}/oem-module-audit.tsv" "${OUTPUT_DIR}/boot-fit-audit.txt" \
    "${OUTPUT_DIR}/systemd-unit-audit.txt" \
    "${OUTPUT_DIR}/rootfs-grow-audit.txt" \
    "${ELF_ABI_REPORT}" "${ELF_SYMBOL_REPORT}" "${ELF_DEBUG_REPORT}" \
    "${OUTPUT_DIR}/sqv-version.txt" "${OUTPUT_DIR}/dpkg-verify-sqv.txt" \
    "${OUTPUT_DIR}/stage1-validator-help.txt" \
    "${OUTPUT_DIR}/stage1-shellcheck.txt" \
    "${OUTPUT_DIR}/partition-size-audit.tsv" \
    "${OUTPUT_DIR}/SHA256SUMS" 2>/dev/null || true
