#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly DEFAULT_OUTPUT_DIR=${REPO_ROOT}/output/debian-stage3
if [ -n "${DEBIAN_STAGE3_OUTPUT_DIR:-}" ]; then
    if [[ "${DEBIAN_STAGE3_OUTPUT_DIR}" = /* ]]; then
        OUTPUT_DIR=${DEBIAN_STAGE3_OUTPUT_DIR}
    else
        OUTPUT_DIR=${REPO_ROOT}/${DEBIAN_STAGE3_OUTPUT_DIR}
    fi
else
    OUTPUT_DIR=${DEFAULT_OUTPUT_DIR}
fi
readonly OUTPUT_DIR
readonly SDK_DIR=${OUTPUT_DIR}/luckfox-pico-sdk
readonly IMAGE_DIR=${SDK_DIR}/output/image
readonly MODULE_DIR=${SDK_DIR}/output/out/sysdrv_out/kernel_drv_ko
readonly DUMPIMAGE=${SDK_DIR}/sysdrv/source/uboot/u-boot/tools/dumpimage
readonly KERNEL_IMAGE=${SDK_DIR}/sysdrv/source/objs_kernel/arch/arm/boot/zImage
readonly KERNEL_CONFIG=${SDK_DIR}/sysdrv/source/objs_kernel/.config
readonly BSP_DTB=${SDK_DIR}/output/out/sysdrv_out/board_uclibc_rv1106/rv1106g-luckfox-pico-zero.dtb
readonly PARTITION_LAYOUT='32K(env),512K@32K(idblock),256K(uboot),4M(misc),32M(boot_a),32M(boot_b),256M(oem_a),256M(oem_b),1536M(rootfs_a),1536M(rootfs_b),3G(userdata),300M(ota)'
readonly MISC_METADATA_HEX=00414230010000000f00010000000000000000000000000000000000671e21a4
readonly BUILD_EPOCH=${SOURCE_DATE_EPOCH:-1767360516}

WORK_DIR=
cleanup() {
    [ -z "${WORK_DIR}" ] || rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

fail() {
    echo "Debian Stage 3 BSP audit failure: $*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command is missing: $1"
}

require_file() {
    test -s "$1" || fail "required BSP artifact is missing: $1"
}

audit_partition_sizes() {
    declare -A limits=(
        [env.img]=$((32 * 1024))
        [idblock.img]=$((512 * 1024))
        [uboot.img]=$((256 * 1024))
        [misc.img]=$((4 * 1024 * 1024))
        [boot_a.img]=$((32 * 1024 * 1024))
        [boot_b.img]=$((32 * 1024 * 1024))
    )
    local image size limit
    printf 'image\tsize_bytes\tlimit_bytes\theadroom_bytes\n' \
        >"${OUTPUT_DIR}/bsp-partition-size-audit.tsv"
    for image in env.img idblock.img uboot.img misc.img boot_a.img boot_b.img; do
        require_file "${IMAGE_DIR}/${image}"
        size=$(stat -c %s "${IMAGE_DIR}/${image}")
        limit=${limits[${image}]}
        [ "${size}" -le "${limit}" ] \
            || fail "${image} exceeds its partition size"
        printf '%s\t%s\t%s\t%s\n' "${image}" "${size}" "${limit}" \
            "$((limit - size))" >>"${OUTPUT_DIR}/bsp-partition-size-audit.tsv"
    done
    require_file "${IMAGE_DIR}/download.bin"
}

audit_misc() {
    local metadata_hex nonzero_count
    metadata_hex=$(od -An -v -tx1 -j 2048 -N 32 "${IMAGE_DIR}/misc.img" \
        | tr -d '[:space:]')
    [ "${metadata_hex}" = "${MISC_METADATA_HEX}" ] \
        || fail "factory A/B metadata is invalid"
    nonzero_count=$(od -An -v -tu1 "${IMAGE_DIR}/misc.img" \
        | awk '{ for (i = 1; i <= NF; i++) if ($i != 0) count++ } END { print count + 0 }')
    [ "${nonzero_count}" -eq 10 ] \
        || fail "misc.img contains data outside the factory A/B metadata"
    {
        printf 'offset=2048\n'
        printf 'size=32\n'
        printf 'metadata_hex=%s\n' "${metadata_hex}"
        printf 'slot_a=priority:15,tries_remaining:0,successful_boot:1\n'
        printf 'slot_b=priority:0,tries_remaining:0,successful_boot:0\n'
    } >"${OUTPUT_DIR}/bsp-misc-audit.txt"
}

audit_boot() {
    local slot suffix root_label boot fdt kernel resource bootargs model serial status
    : >"${OUTPUT_DIR}/bsp-boot-fit-audit.txt"
    for slot in a b; do
        suffix=_${slot}
        root_label=rootfs_${slot}
        boot=${IMAGE_DIR}/boot_${slot}.img
        fdt=${WORK_DIR}/boot_${slot}.dtb
        kernel=${WORK_DIR}/boot_${slot}.kernel
        resource=${WORK_DIR}/boot_${slot}.resource
        "${DUMPIMAGE}" -l "${boot}" >>"${OUTPUT_DIR}/bsp-boot-fit-audit.txt"
        "${DUMPIMAGE}" -i "${boot}" -T flat_dt -p 0 -o "${fdt}" unused \
            >>"${OUTPUT_DIR}/bsp-boot-fit-audit.txt"
        "${DUMPIMAGE}" -i "${boot}" -T flat_dt -p 1 -o "${kernel}" unused \
            >>"${OUTPUT_DIR}/bsp-boot-fit-audit.txt"
        "${DUMPIMAGE}" -i "${boot}" -T flat_dt -p 2 -o "${resource}" unused \
            >>"${OUTPUT_DIR}/bsp-boot-fit-audit.txt"

        cmp "${kernel}" "${KERNEL_IMAGE}"
        bootargs=$(fdtget -t s "${fdt}" /chosen bootargs)
        model=$(fdtget -t s "${fdt}" / model)
        serial=$(fdtget -t s "${fdt}" /aliases serial1)
        status=$(fdtget -t s "${fdt}" /serial@ff4b0000 status)
        [ "${model}" = 'Luckfox Pico Zero' ] || fail "boot_${slot}.img has the wrong model"
        [ "${serial}" = /serial@ff4b0000 ] || fail "boot_${slot}.img has the wrong serial alias"
        [ "${status}" = okay ] || fail "boot_${slot}.img disables the recovery serial port"
        grep -qw "blkdevparts=mmcblk0:${PARTITION_LAYOUT}" <<<"${bootargs}" \
            || fail "boot_${slot}.img has the wrong partition command line"
        grep -qw "root=PARTLABEL=${root_label}" <<<"${bootargs}" \
            || fail "boot_${slot}.img has the wrong root PARTLABEL"
        grep -qw "aiden.slot_suffix=${suffix}" <<<"${bootargs}" \
            || fail "boot_${slot}.img has the wrong Aiden slot suffix"
        grep -qw 'rootfstype=ext4' <<<"${bootargs}" \
            || fail "boot_${slot}.img is missing rootfstype=ext4"
        grep -qw 'rootwait' <<<"${bootargs}" \
            || fail "boot_${slot}.img is missing rootwait"
        grep -qw 'net.ifnames=0' <<<"${bootargs}" \
            || fail "boot_${slot}.img is missing net.ifnames=0"
        grep -qw 'rk_dma_heap_cma=100M' <<<"${bootargs}" \
            || fail "boot_${slot}.img is missing the production CMA setting"
        [ "$(tr ' ' '\n' <<<"${bootargs}" | grep -c '^root=')" -eq 1 ] \
            || fail "boot_${slot}.img contains multiple root arguments"
        [ "$(tr ' ' '\n' <<<"${bootargs}" | grep -c '^aiden\.slot_suffix=')" -eq 1 ] \
            || fail "boot_${slot}.img contains multiple slot suffixes"
        {
            printf 'slot=%s\n' "${slot}"
            printf 'bootargs=%s\n' "${bootargs}"
            printf 'model=%s\n' "${model}"
            printf 'serial1=%s\n' "${serial}"
            printf 'serial1_status=%s\n' "${status}"
            printf 'fdt_sha256=%s\n' "$(sha256sum "${fdt}" | awk '{print $1}')"
            printf 'kernel_sha256=%s\n' "$(sha256sum "${kernel}" | awk '{print $1}')"
            printf 'resource_sha256=%s\n' "$(sha256sum "${resource}" | awk '{print $1}')"
        } >>"${OUTPUT_DIR}/bsp-boot-fit-audit.txt"
    done
    cmp "${WORK_DIR}/boot_a.kernel" "${WORK_DIR}/boot_b.kernel"
}

audit_modules() {
    local module firmware_count module_count
    for module in \
        aic8800_bsp.ko aic8800_btlpm.ko aic8800_fdrv.ko \
        cfg80211.ko mac80211.ko mpp_vcodec.ko rga3.ko rknpu.ko rockit.ko \
        video_rkcif.ko video_rkisp.ko; do
        require_file "${MODULE_DIR}/${module}"
    done
    require_file "${MODULE_DIR}/aic8800dc_fw/lmacfw_rf_8800dc.bin"
    module_count=$(find "${MODULE_DIR}" -maxdepth 1 -type f -name '*.ko' | wc -l)
    firmware_count=$(find "${MODULE_DIR}/aic8800dc_fw" -type f | wc -l)
    [ "${module_count}" -ge 11 ] || fail "too few kernel modules were produced"
    [ "${firmware_count}" -ge 1 ] || fail "AIC8800 firmware is missing"
    printf '%s\n' "${module_count}" >"${WORK_DIR}/module-count"
    printf '%s\n' "${firmware_count}" >"${WORK_DIR}/firmware-count"
}

write_hashes() {
    (
        cd "${SDK_DIR}"
        sha256sum \
            output/image/env.img \
            output/image/idblock.img \
            output/image/uboot.img \
            output/image/misc.img \
            output/image/boot_a.img \
            output/image/boot_b.img \
            output/image/download.bin \
            output/out/sysdrv_out/board_uclibc_rv1106/rv1106g-luckfox-pico-zero.dtb \
            sysdrv/source/objs_kernel/arch/arm/boot/zImage \
            sysdrv/source/objs_kernel/.config
        find output/out/sysdrv_out/kernel_drv_ko -type f -print0 \
            | LC_ALL=C sort -z | xargs -0 sha256sum
    ) >"${OUTPUT_DIR}/bsp-artifacts.sha256"
    (
        cd "${REPO_ROOT}"
        sha256sum \
            scripts/debian-stage3/build.sh \
            scripts/debian-stage3/audit-bsp.sh \
            scripts/debian-stage3/canonicalize-bsp.py \
            scripts/debian-stage3/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk \
            scripts/debian-stage3/debian-stage3.config \
            scripts/debian-stage3/sdk-patches/0001-use-all-host-cpus.patch \
            scripts/debian-stage3/sdk-patches/0002-append-slot-kernel-cmdline.patch \
            scripts/debian-stage3/sdk-patches/0003-add-ab-images-action.patch \
            scripts/debian-stage3/sdk-patches/0004-make-bsp-images-reproducible.patch \
            scripts/debian-stage3/sdk-patches/0005-set-rv1106-usb2-hs-odt.patch \
            scripts/debian-stage3/sdk-patches/0006-fix-configfs-uevent-rebind-uaf.patch \
            scripts/debian-stage3/sdk-patches/0007-enable-rv1106-uboot-rockusb.patch
    ) >"${OUTPUT_DIR}/bsp-inputs.sha256"
}

write_summary() {
    local artifact_manifest_sha input_manifest_sha
    artifact_manifest_sha=$(sha256sum "${OUTPUT_DIR}/bsp-artifacts.sha256" | awk '{print $1}')
    input_manifest_sha=$(sha256sum "${OUTPUT_DIR}/bsp-inputs.sha256" | awk '{print $1}')
    {
        printf 'status=pass\n'
        printf 'source_sdk_commit=%s\n' "$(cat "${OUTPUT_DIR}/source-sdk-commit.txt")"
        printf 'bsp_builder_image=%s\n' "$(cat "${OUTPUT_DIR}/bsp-builder-image-id.txt")"
        printf 'partition_layout=%s\n' "${PARTITION_LAYOUT}"
        printf 'kernel_config_sha256=%s\n' "$(sha256sum "${KERNEL_CONFIG}" | awk '{print $1}')"
        printf 'boot_a_sha256=%s\n' "$(sha256sum "${IMAGE_DIR}/boot_a.img" | awk '{print $1}')"
        printf 'boot_b_sha256=%s\n' "$(sha256sum "${IMAGE_DIR}/boot_b.img" | awk '{print $1}')"
        printf 'artifact_manifest_sha256=%s\n' "${artifact_manifest_sha}"
        printf 'input_manifest_sha256=%s\n' "${input_manifest_sha}"
        printf 'kernel_module_count=%s\n' "$(cat "${WORK_DIR}/module-count")"
        printf 'aic8800_firmware_file_count=%s\n' "$(cat "${WORK_DIR}/firmware-count")"
    } >"${OUTPUT_DIR}/bsp-audit-summary.txt"
}

main() {
    for command in awk cmp fdtget find od sha256sum sort stat tr xargs; do
        require_command "${command}"
    done
    require_file "${DUMPIMAGE}"
    test -x "${DUMPIMAGE}" || fail "SDK dumpimage tool is not executable"
    require_file "${KERNEL_IMAGE}"
    require_file "${KERNEL_CONFIG}"
    require_file "${BSP_DTB}"
    require_file "${OUTPUT_DIR}/source-sdk-commit.txt"
    require_file "${OUTPUT_DIR}/bsp-builder-image-id.txt"
    require_file "${IMAGE_DIR}/.env.txt"
    "${SCRIPT_DIR}/canonicalize-bsp.py" \
        --check \
        --source-date-epoch "${BUILD_EPOCH}" \
        --fit "${IMAGE_DIR}/uboot.img" \
        --fit "${IMAGE_DIR}/boot_a.img" \
        --fit "${IMAGE_DIR}/boot_b.img" \
        --crc-table-source "${SDK_DIR}/sysdrv/source/uboot/u-boot/tools/rockchip/boot_merger.c" \
        --loader "${IMAGE_DIR}/download.bin"
    grep -qx "blkdevparts=mmcblk0:${PARTITION_LAYOUT}" "${IMAGE_DIR}/.env.txt" \
        || fail "BSP partition environment changed"
    cmp "${KERNEL_CONFIG}" "${OUTPUT_DIR}/kernel.config"
    cmp "${IMAGE_DIR}/.env.txt" "${OUTPUT_DIR}/bsp-env.txt"

    WORK_DIR=$(mktemp -d "${OUTPUT_DIR}/.bsp-audit.XXXXXX")
    audit_partition_sizes
    audit_misc
    audit_boot
    audit_modules
    write_hashes
    write_summary
}

main "$@"
