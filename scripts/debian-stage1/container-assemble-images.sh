#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT=/work
readonly OUTPUT_DIR=${REPO_ROOT}/output/debian-stage1
readonly SDK_DIR=${OUTPUT_DIR}/luckfox-pico-sdk
readonly IMAGE_DIR=${OUTPUT_DIR}/image
readonly WORK_DIR=${OUTPUT_DIR}/image-work
readonly OEM_ROOT=${WORK_DIR}/oem-root
readonly USERDATA_ROOT=${WORK_DIR}/userdata-root
readonly OEM_ELF_SANITIZATION_REPORT=${OUTPUT_DIR}/oem-elf-sanitization.tsv

mounts=()
cleanup() {
    local index
    for ((index = ${#mounts[@]} - 1; index >= 0; index--)); do
        mountpoint -q "${mounts[index]}" && umount "${mounts[index]}" || true
    done
}
trap cleanup EXIT

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends \
    e2fsprogs rsync file binutils binutils-arm-linux-gnueabihf

rm -rf "${WORK_DIR}" "${IMAGE_DIR}"
mkdir -p "${OEM_ROOT}/usr/ko" "${OEM_ROOT}/usr/lib" \
    "${OEM_ROOT}/usr/share/luckfox-debian-stage1" "${USERDATA_ROOT}" "${IMAGE_DIR}"

rsync -aH --chown=0:0 "${SDK_DIR}/output/out/sysdrv_out/kernel_drv_ko/" "${OEM_ROOT}/usr/ko/"

# Only copy shared objects positively identified as glibc. Static archives are
# build inputs and are recorded by the audit, not installed at runtime.
while IFS= read -r -d '' library; do
    if readelf -d "${library}" 2>/dev/null | grep -qE 'libc\.so\.6|ld-linux-armhf\.so\.3'; then
        cp -aL "${library}" "${OEM_ROOT}/usr/lib/"
    fi
done < <(find "${SDK_DIR}/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-glibc-rockchip/usr/lib" \
    -type f -name '*.so*' -print0)
while IFS= read -r -d '' link; do
    resolved=$(readlink -f "${link}")
    if [ -e "${OEM_ROOT}/usr/lib/$(basename "${resolved}")" ]; then
        cp -a "${link}" "${OEM_ROOT}/usr/lib/"
    fi
done < <(find "${SDK_DIR}/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-glibc-rockchip/usr/lib" \
    -type l -name '*.so*' -print0)

printf 'path\tsections\taction\toriginal_sha256\tinstalled_sha256\n' \
    >"${OEM_ELF_SANITIZATION_REPORT}"
while IFS= read -r -d '' library; do
    if ! head -c 4 "${library}" | grep -q $'\177ELF'; then
        continue
    fi
    debug_sections=$(readelf -SW "${library}" 2>/dev/null \
        | sed -n \
            's/^[[:space:]]*\[[[:space:]]*[0-9][0-9]*\][[:space:]]\+\([^[:space:]]\+\).*/\1/p' \
        | grep -E \
            '^(\.debug.*|\.zdebug.*|\.gdb_index|\.gnu_debugdata)$' \
        || true)
    [ -n "${debug_sections}" ] || continue

    original_sha256=$(sha256sum "${library}" | awk '{print $1}')
    arm-linux-gnueabihf-strip --strip-debug "${library}"
    if readelf -SW "${library}" 2>/dev/null \
        | sed -n \
            's/^[[:space:]]*\[[[:space:]]*[0-9][0-9]*\][[:space:]]\+\([^[:space:]]\+\).*/\1/p' \
        | grep -Eq \
            '^(\.debug.*|\.zdebug.*|\.gdb_index|\.gnu_debugdata)$'; then
        echo "Failed to remove OEM debug sections: ${library#${OEM_ROOT}}" >&2
        exit 1
    fi
    installed_sha256=$(sha256sum "${library}" | awk '{print $1}')
    printf '%s\t%s\tstripped\t%s\t%s\n' \
        "${library#${OEM_ROOT}}" "$(paste -sd, <<<"${debug_sections}")" \
        "${original_sha256}" "${installed_sha256}" \
        >>"${OEM_ELF_SANITIZATION_REPORT}"
done < <(find "${OEM_ROOT}/usr/lib" -type f -print0)

cp "${OUTPUT_DIR}/vendor-libs-audit.tsv" \
    "${OEM_ROOT}/usr/share/luckfox-debian-stage1/vendor-libs-audit.tsv"
cat >"${OEM_ROOT}/usr/share/luckfox-debian-stage1/README" <<'EOF'
This is the stage-1 Debian bring-up OEM partition.

Kernel modules and firmware come from the original Luckfox SDK. Only vendor
shared libraries identified as glibc are installed. Original rkipc and sample
programs are intentionally absent because their current binaries use uClibc.
EOF

make_image() {
    local source_dir=$1
    local image=$2
    local size=$3
    local label=$4
    local mount_dir=${WORK_DIR}/mnt-${label}
    mkdir -p "${mount_dir}"
    truncate -s "${size}" "${image}"
    mkfs.ext4 -F -L "${label}" -m 1 \
        -E lazy_itable_init=0,lazy_journal_init=0 \
        -O '^64bit,^huge_file,^metadata_csum,^metadata_csum_seed,^dir_index,^quota' "${image}"
    mount -o loop "${image}" "${mount_dir}"
    mounts+=("${mount_dir}")
    rsync -aHAX --numeric-ids "${source_dir}/" "${mount_dir}/"
    sync
    umount "${mount_dir}"
    mounts=()
    e2fsck -fy "${image}"
    resize2fs -M "${image}"
    e2fsck -fy "${image}"
}

make_image "${OEM_ROOT}" "${IMAGE_DIR}/oem.img" 512M oem
make_image "${USERDATA_ROOT}" "${IMAGE_DIR}/userdata.img" 256M userdata

cp "${OUTPUT_DIR}/rootfs.ext4" "${IMAGE_DIR}/rootfs.img"
for item in env.img idblock.img uboot.img boot.img download.bin; do
    cp "${SDK_DIR}/output/image/${item}" "${IMAGE_DIR}/${item}"
done

chown -R "${HOST_UID:-0}:${HOST_GID:-0}" "${IMAGE_DIR}"
chown "${HOST_UID:-0}:${HOST_GID:-0}" "${OEM_ELF_SANITIZATION_REPORT}"
rm -rf "${WORK_DIR}"
