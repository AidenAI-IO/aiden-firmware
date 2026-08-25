#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT=/work
readonly SCRIPT_DIR=${REPO_ROOT}/scripts/debian-stage1
readonly OUTPUT_DIR=${REPO_ROOT}/output/debian-stage1
readonly WORK_DIR=${OUTPUT_DIR}/rootfs-work
readonly ROOTFS_DIR=${WORK_DIR}/rootfs
readonly MOUNT_DIR=${WORK_DIR}/mnt
readonly ROOTFS_IMAGE=${OUTPUT_DIR}/rootfs.ext4
readonly MIRROR=${DEBIAN_MIRROR:-http://deb.debian.org/debian}
readonly SECURITY_MIRROR=${DEBIAN_SECURITY_MIRROR:-http://security.debian.org/debian-security}
readonly DEBIAN_KEYRING_VERSION=2025.1
readonly DEBIAN_KEYRING_SHA256=9ea7778e443144ca490668737a8ab22dd3e748bb99e805e22ec055abeb3c7fac
readonly DEBIAN_KEYRING_URL=https://deb.debian.org/debian/pool/main/d/debian-archive-keyring/debian-archive-keyring_${DEBIAN_KEYRING_VERSION}_all.deb
readonly ELF_SANITIZATION_REPORT=${OUTPUT_DIR}/elf-sanitization.txt
readonly BINFMT_DIR=/proc/sys/fs/binfmt_misc

mounts=()

cleanup() {
    local index
    for ((index = ${#mounts[@]} - 1; index >= 0; index--)); do
        mountpoint -q "${mounts[index]}" && umount "${mounts[index]}" || true
    done
}
trap cleanup EXIT

mount_chroot_fs() {
    mkdir -p "${ROOTFS_DIR}/proc" "${ROOTFS_DIR}/sys" "${ROOTFS_DIR}/dev/pts" "${ROOTFS_DIR}/run"
    mount -t proc proc "${ROOTFS_DIR}/proc"
    mounts+=("${ROOTFS_DIR}/proc")
    mount -t sysfs sysfs "${ROOTFS_DIR}/sys"
    mounts+=("${ROOTFS_DIR}/sys")
    mount --bind /dev "${ROOTFS_DIR}/dev"
    mounts+=("${ROOTFS_DIR}/dev")
    mount --bind /dev/pts "${ROOTFS_DIR}/dev/pts"
    mounts+=("${ROOTFS_DIR}/dev/pts")
    mount -t tmpfs tmpfs "${ROOTFS_DIR}/run"
    mounts+=("${ROOTFS_DIR}/run")
}

unmount_chroot_fs() {
    cleanup
    mounts=()
}

arm_binfmt_is_enabled() {
    [ -r "${BINFMT_DIR}/qemu-arm" ] \
        && grep -qx enabled "${BINFMT_DIR}/qemu-arm"
}

ensure_arm_binfmt() {
    if ! mountpoint -q "${BINFMT_DIR}"; then
        if ! mount -t binfmt_misc binfmt_misc "${BINFMT_DIR}"; then
            echo "Unable to mount binfmt_misc; the rootfs builder must run in a privileged container" >&2
            exit 1
        fi
    fi

    if ! arm_binfmt_is_enabled; then
        if [ -x /usr/lib/systemd/systemd-binfmt ] \
            && [ -f /usr/lib/binfmt.d/qemu-arm.conf ]; then
            if ! /usr/lib/systemd/systemd-binfmt \
                /usr/lib/binfmt.d/qemu-arm.conf; then
                echo "Failed to register qemu-arm with systemd-binfmt" >&2
                exit 1
            fi
        elif command -v update-binfmts >/dev/null 2>&1; then
            if ! update-binfmts --enable qemu-arm; then
                echo "Failed to register qemu-arm with update-binfmts" >&2
                exit 1
            fi
        else
            echo "No supported qemu-arm binfmt registration tool is installed" >&2
            exit 1
        fi
    fi

    if ! arm_binfmt_is_enabled; then
        echo "qemu-arm binfmt registration is not enabled" >&2
        exit 1
    fi
}

install_host_tools() {
    local keyring_deb=/tmp/debian-archive-keyring.deb
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y --no-install-recommends \
        debian-archive-keyring debootstrap qemu-user-static binfmt-support \
        rsync e2fsprogs xz-utils \
        file binutils binutils-arm-linux-gnueabihf \
        acl attr libcap2-bin ca-certificates curl

    curl -fsSL -o "${keyring_deb}" "${DEBIAN_KEYRING_URL}"
    echo "${DEBIAN_KEYRING_SHA256}  ${keyring_deb}" | sha256sum -c -
    dpkg -i "${keyring_deb}"
    rm -f "${keyring_deb}"
}

bootstrap_rootfs() {
    rm -rf "${WORK_DIR}"
    mkdir -p "${ROOTFS_DIR}" "${MOUNT_DIR}" "${OUTPUT_DIR}"

    ensure_arm_binfmt
    debootstrap --arch=armhf --variant=minbase \
        --keyring=/usr/share/keyrings/debian-archive-keyring.gpg \
        trixie "${ROOTFS_DIR}" "${MIRROR}"

    cat >"${ROOTFS_DIR}/etc/apt/sources.list" <<EOF
deb ${MIRROR} trixie main non-free-firmware
deb ${MIRROR} trixie-updates main non-free-firmware
deb ${SECURITY_MIRROR} trixie-security main non-free-firmware
EOF
    cat >"${ROOTFS_DIR}/usr/sbin/policy-rc.d" <<'EOF'
#!/bin/sh
exit 101
EOF
    chmod 0755 "${ROOTFS_DIR}/usr/sbin/policy-rc.d"

    mount_chroot_fs
    cp "${SCRIPT_DIR}/packages.list" "${ROOTFS_DIR}/tmp/debian-stage1-packages.list"
    chroot "${ROOTFS_DIR}" /usr/bin/env \
        DEBIAN_FRONTEND=noninteractive SYSTEMD_OFFLINE=1 \
        apt-get update
    chroot "${ROOTFS_DIR}" /bin/sh -ec \
        'DEBIAN_FRONTEND=noninteractive SYSTEMD_OFFLINE=1 apt-get install -y --no-install-recommends $(grep -v "^[[:space:]]*#" /tmp/debian-stage1-packages.list | xargs)'

    for group in sudo audio video dialout plugdev netdev; do
        chroot "${ROOTFS_DIR}" getent group "${group}" >/dev/null \
            || chroot "${ROOTFS_DIR}" groupadd -r "${group}"
    done
    chroot "${ROOTFS_DIR}" useradd -m -s /bin/bash -G sudo,audio,video,dialout,plugdev,netdev luckfox
    printf 'luckfox:luckfox\n' | chroot "${ROOTFS_DIR}" chpasswd
    chroot "${ROOTFS_DIR}" passwd -l root

    rm -f "${ROOTFS_DIR}/usr/sbin/policy-rc.d" "${ROOTFS_DIR}/tmp/debian-stage1-packages.list"
    unmount_chroot_fs
}

configure_rootfs() {
    rsync -aHAX --numeric-ids --chown=0:0 \
        "${SCRIPT_DIR}/overlay/" "${ROOTFS_DIR}/"

    # fstab mount targets must exist in the immutable rootfs. Do not rely on
    # systemd creating these directories implicitly during the first boot.
    install -d -m 0755 "${ROOTFS_DIR}/oem" "${ROOTFS_DIR}/userdata"

    # Git records executable bits but not the complete mode. Normalize copied
    # overlay paths so the rootfs does not inherit the checkout owner's umask.
    while IFS= read -r -d '' path; do
        chmod 0755 "${ROOTFS_DIR}/${path}"
    done < <(find "${SCRIPT_DIR}/overlay" -mindepth 1 -type d -printf '%P\0')
    while IFS= read -r -d '' path; do
        if [ -x "${SCRIPT_DIR}/overlay/${path}" ]; then
            chmod 0755 "${ROOTFS_DIR}/${path}"
        else
            chmod 0644 "${ROOTFS_DIR}/${path}"
        fi
    done < <(find "${SCRIPT_DIR}/overlay" -type f -printf '%P\0')

    if [ -n "${DEBIAN_WIFI_SSID:-}" ]; then
        if [ -z "${DEBIAN_WIFI_PSK:-}" ]; then
            echo "DEBIAN_WIFI_PSK is required when DEBIAN_WIFI_SSID is set" >&2
            exit 1
        fi
        {
            printf 'ctrl_interface=DIR=/run/wpa_supplicant GROUP=netdev\nupdate_config=1\ncountry=CN\n\n'
            chroot "${ROOTFS_DIR}" wpa_passphrase "${DEBIAN_WIFI_SSID}" "${DEBIAN_WIFI_PSK}"
        } >"${ROOTFS_DIR}/etc/wpa_supplicant/wpa_supplicant-wlan0.conf"
        chmod 0600 "${ROOTFS_DIR}/etc/wpa_supplicant/wpa_supplicant-wlan0.conf"
    fi

    ln -sfn /run/systemd/resolve/stub-resolv.conf "${ROOTFS_DIR}/etc/resolv.conf"
    : >"${ROOTFS_DIR}/etc/machine-id"
    ln -sfn /etc/machine-id "${ROOTFS_DIR}/var/lib/dbus/machine-id"
    rm -f "${ROOTFS_DIR}"/etc/ssh/ssh_host_*

    systemctl --root="${ROOTFS_DIR}" enable \
        systemd-networkd.service systemd-resolved.service systemd-timesyncd.service \
        ssh.service sshd-keygen.service serial-getty@ttyFIQ0.service \
        wpa_supplicant@wlan0.service \
        luckfox-rootfs-grow.service luckfox-oem-ldconfig.service luckfox-wifi.service \
        luckfox-bluetooth-attach.service bluetooth.service \
        luckfox-zram.service luckfox-stage1-report.service
    systemctl --root="${ROOTFS_DIR}" disable wpa_supplicant.service || true
    systemctl --root="${ROOTFS_DIR}" mask \
        apt-daily.service apt-daily.timer apt-daily-upgrade.service apt-daily-upgrade.timer \
        systemd-networkd-wait-online.service e2scrub_reap.service \
        wpa_supplicant.service || true

    chroot "${ROOTFS_DIR}" dpkg-query -W -f='${binary:Package}\t${Version}\n' \
        | sort >"${OUTPUT_DIR}/packages.txt"
    for package in \
        adb android-sdk-platform-tools-common bluez libdrm2 openssh-server \
        systemd-resolved wpasupplicant; do
        if ! awk -F '\t' -v package="${package}" \
            '$1 == package || $1 == package ":armhf" {found = 1} END {exit !found}' \
            "${OUTPUT_DIR}/packages.txt"; then
            echo "Required Debian package is missing from the rootfs: ${package}" >&2
            exit 1
        fi
    done
    chroot "${ROOTFS_DIR}" adb version >"${OUTPUT_DIR}/adb-version.txt"
    chroot "${ROOTFS_DIR}" dpkg --audit >"${OUTPUT_DIR}/dpkg-audit.txt"
    if [ -s "${OUTPUT_DIR}/dpkg-audit.txt" ]; then
        echo "dpkg reports an incomplete or inconsistent rootfs" >&2
        exit 1
    fi
    cp "${ROOTFS_DIR}/etc/apt/sources.list" "${OUTPUT_DIR}/sources.list"

    # Debian's sqv currently embeds a GDB auto-load script section even in the
    # stripped runtime binary. Keep this narrowly scoped and let the final ELF
    # audit reject any other embedded debug content introduced by package
    # changes.
    printf 'path\tsection\taction\toriginal_sha256\tinstalled_sha256\tdpkg_md5\n' \
        >"${ELF_SANITIZATION_REPORT}"
    if readelf -SW "${ROOTFS_DIR}/usr/bin/sqv" \
        | grep -qw '\.debug_gdb_scripts'; then
        sqv_original_sha256=$(sha256sum "${ROOTFS_DIR}/usr/bin/sqv" \
            | awk '{print $1}')
        arm-linux-gnueabihf-objcopy --remove-section=.debug_gdb_scripts \
            "${ROOTFS_DIR}/usr/bin/sqv"
        sqv_installed_sha256=$(sha256sum "${ROOTFS_DIR}/usr/bin/sqv" \
            | awk '{print $1}')
        sqv_md5=$(md5sum "${ROOTFS_DIR}/usr/bin/sqv" | awk '{print $1}')
        sed -i -E \
            "s|^[0-9a-f]{32}([[:space:]]+)usr/bin/sqv$|${sqv_md5}\\1usr/bin/sqv|" \
            "${ROOTFS_DIR}/var/lib/dpkg/info/sqv.md5sums"
        if ! grep -Eq "^${sqv_md5}[[:space:]]+usr/bin/sqv$" \
            "${ROOTFS_DIR}/var/lib/dpkg/info/sqv.md5sums"; then
            echo "Failed to update the sqv dpkg checksum after sanitization" >&2
            exit 1
        fi
        printf '/usr/bin/sqv\t.debug_gdb_scripts\tremoved\t%s\t%s\t%s\n' \
            "${sqv_original_sha256}" "${sqv_installed_sha256}" "${sqv_md5}" \
            >>"${ELF_SANITIZATION_REPORT}"
    else
        sqv_installed_sha256=$(sha256sum "${ROOTFS_DIR}/usr/bin/sqv" \
            | awk '{print $1}')
        sqv_md5=$(md5sum "${ROOTFS_DIR}/usr/bin/sqv" | awk '{print $1}')
        printf '/usr/bin/sqv\t.debug_gdb_scripts\tnot-present\t%s\t%s\t%s\n' \
            "${sqv_installed_sha256}" "${sqv_installed_sha256}" "${sqv_md5}" \
            >>"${ELF_SANITIZATION_REPORT}"
    fi
    if readelf -SW "${ROOTFS_DIR}/usr/bin/sqv" \
        | grep -qw '\.debug_gdb_scripts'; then
        echo "Failed to remove sqv .debug_gdb_scripts" >&2
        exit 1
    fi
    chroot "${ROOTFS_DIR}" dpkg --verify sqv \
        >"${OUTPUT_DIR}/dpkg-verify-sqv.txt"
    if [ -s "${OUTPUT_DIR}/dpkg-verify-sqv.txt" ]; then
        echo "dpkg verification failed after sqv sanitization" >&2
        cat "${OUTPUT_DIR}/dpkg-verify-sqv.txt" >&2
        exit 1
    fi

    rm -rf "${ROOTFS_DIR}/var/lib/apt/lists/"* \
        "${ROOTFS_DIR}/var/cache/apt/archives/"*.deb \
        "${ROOTFS_DIR}/var/log/"* \
        "${ROOTFS_DIR}/tmp/"* \
        "${ROOTFS_DIR}/var/tmp/"*
    rm -f "${ROOTFS_DIR}/usr/bin/qemu-arm-static"
    find "${ROOTFS_DIR}/dev" -xdev \( -type b -o -type c \) -delete

    (
        cd "${ROOTFS_DIR}"
        find . -xdev -printf '%U\t%G\t%m\t%p\t%l\n' | sort
    ) >"${OUTPUT_DIR}/filesystem-manifest.txt"
    getcap -r "${ROOTFS_DIR}" 2>/dev/null \
        | sed "s#^${ROOTFS_DIR}##" >"${OUTPUT_DIR}/capabilities.txt" || true
    find "${ROOTFS_DIR}" -xdev -type f \( -perm -4000 -o -perm -2000 \) \
        -printf '%m\t%U\t%G\t%p\n' \
        | sed "s#${ROOTFS_DIR}##" | sort >"${OUTPUT_DIR}/setid-files.txt"
}

create_ext4_image() {
    local feature_opts
    feature_opts='^64bit,^huge_file,^metadata_csum,^metadata_csum_seed,^dir_index,^quota'
    local probe
    probe=$(mktemp)
    truncate -s 8M "${probe}"
    if mkfs.ext4 -q -n -O '^orphan_file' "${probe}" >/dev/null 2>&1; then
        feature_opts="${feature_opts},^orphan_file"
    fi
    rm -f "${probe}"

    rm -f "${ROOTFS_IMAGE}"
    truncate -s 1536M "${ROOTFS_IMAGE}"
    mkfs.ext4 -F -L rootfs -m 1 \
        -E lazy_itable_init=0,lazy_journal_init=0 \
        -O "${feature_opts}" "${ROOTFS_IMAGE}"

    mount -o loop "${ROOTFS_IMAGE}" "${MOUNT_DIR}"
    mounts+=("${MOUNT_DIR}")
    rsync -aHAX --numeric-ids "${ROOTFS_DIR}/" "${MOUNT_DIR}/"
    sync
    umount "${MOUNT_DIR}"
    mounts=()

    e2fsck -fy "${ROOTFS_IMAGE}"
    resize2fs -M "${ROOTFS_IMAGE}"
    e2fsck -fy "${ROOTFS_IMAGE}"
    dumpe2fs -h "${ROOTFS_IMAGE}" >"${OUTPUT_DIR}/rootfs-dumpe2fs.txt" 2>&1
    chown "${HOST_UID:-0}:${HOST_GID:-0}" \
        "${ROOTFS_IMAGE}" "${OUTPUT_DIR}/packages.txt" "${OUTPUT_DIR}/sources.list" \
        "${OUTPUT_DIR}/filesystem-manifest.txt" "${OUTPUT_DIR}/capabilities.txt" \
        "${OUTPUT_DIR}/setid-files.txt" \
        "${ELF_SANITIZATION_REPORT}" \
        "${OUTPUT_DIR}/dpkg-verify-sqv.txt" \
        "${OUTPUT_DIR}/rootfs-dumpe2fs.txt" "${OUTPUT_DIR}/adb-version.txt" \
        "${OUTPUT_DIR}/dpkg-audit.txt" \
        "${OUTPUT_DIR}/e2fsprogs-1.43.9-import-audit.txt"
    rm -rf "${WORK_DIR}"
}

install_host_tools
"${SCRIPT_DIR}/audit-e2fsprogs-import.sh" \
    "${OUTPUT_DIR}/luckfox-pico-sdk" \
    "${OUTPUT_DIR}/e2fsprogs-1.43.9-import-audit.txt" \
    "${OUTPUT_DIR}/e2fsprogs-import-audit-work"
bootstrap_rootfs
configure_rootfs
create_ext4_image
