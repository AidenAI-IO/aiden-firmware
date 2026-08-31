#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT=/work
readonly OUTPUT_DIR=/out
readonly SDK_DIR=${OUTPUT_DIR}/luckfox-pico-sdk
readonly IMAGE_DIR=${OUTPUT_DIR}/image
readonly WORK_DIR=${OUTPUT_DIR}/image-work
readonly OEM_ROOT=${WORK_DIR}/oem-root
readonly USERDATA_ROOT=${WORK_DIR}/userdata-root
readonly OTA_ROOT=${WORK_DIR}/ota-root
readonly ROOTFS_IMAGE=${OUTPUT_DIR}/rootfs.ext4
readonly OTA_PUBLIC_KEY=/run/secrets/ota_pubkey.pem
readonly AGENT_CONFIG=/run/secrets/agent.toml
readonly OEM_UUID=80a2f3fd-c8e2-439d-b718-5059b74dcc91
readonly USERDATA_UUID=ee2962d6-bd9c-4096-b22b-71934584d36a
readonly OTA_UUID=950e39a6-5445-47df-a542-e80ed45b08ac

readonly -a PRODUCTION_BINARIES=(
    abctl
    agent
    aiden-environment
    audio_service
    ble_service
    config_web
    cpu_vad
    frame_service
    ota
    rknn_vad
)

mounts=()
cleanup() {
    local index
    for ((index = ${#mounts[@]} - 1; index >= 0; index--)); do
        mountpoint -q "${mounts[index]}" && umount "${mounts[index]}" || true
    done
}
trap cleanup EXIT

normalize_tree_modes() {
    local root=$1
    find "${root}" -type d -exec chmod 0755 {} +
    while IFS= read -r -d '' path; do
        if [ -x "${path}" ]; then chmod 0755 "${path}"; else chmod 0644 "${path}"; fi
    done < <(find "${root}" -type f -print0)
}

stage_oem() {
    grep -qx 'status=pass' /apps-audit/summary.txt || {
        echo "Stage 2 application audit has not passed" >&2
        exit 1
    }
    "${REPO_ROOT}/scripts/validate_ota_pubkey.sh" "${OTA_PUBLIC_KEY}"
    test -d "${SDK_DIR}/output/out/sysdrv_out/kernel_drv_ko" || {
        echo "Missing isolated SDK kernel modules" >&2
        exit 1
    }

    rm -rf "${WORK_DIR}" "${IMAGE_DIR}"
    install -d -m 0755 \
        "${OEM_ROOT}/etc" \
        "${OEM_ROOT}/usr/bin" \
        "${OEM_ROOT}/usr/lib" \
        "${OEM_ROOT}/usr/ko" \
        "${OEM_ROOT}/usr/model" \
        "${OEM_ROOT}/usr/share/aiden"
    rsync -aH --chown=0:0 "${REPO_ROOT}/overlay-debian-oem/" "${OEM_ROOT}/"
    local binary
    for binary in "${PRODUCTION_BINARIES[@]}"; do
        install -m 0755 "/apps/bin/${binary}" "${OEM_ROOT}/usr/bin/${binary}"
    done
    rsync -aH --chown=0:0 /apps/lib/ "${OEM_ROOT}/usr/lib/"
    rsync -aH --chown=0:0 \
        "${SDK_DIR}/output/out/sysdrv_out/kernel_drv_ko/" "${OEM_ROOT}/usr/ko/"
    rsync -aH --chown=0:0 "${REPO_ROOT}/src/config_web/web/" \
        "${OEM_ROOT}/usr/share/aiden/config-web/"
    install -m 0644 "${REPO_ROOT}/src/agent/internal/agent/quick_actions.json" \
        "${OEM_ROOT}/usr/share/aiden/quick_actions.json"
    rsync -aH --chown=0:0 "${REPO_ROOT}/src/agent/config/skills/" \
        "${OEM_ROOT}/usr/share/aiden/skills/"
    install -m 0644 "${OTA_PUBLIC_KEY}" "${OEM_ROOT}/etc/ota_pubkey.pem"

    normalize_tree_modes "${OEM_ROOT}"
    chmod 0755 "${OEM_ROOT}/usr/bin/"* "${OEM_ROOT}/usr/ko/insmod_wifi.sh" \
        2>/dev/null || true
    chmod 0644 "${OEM_ROOT}/etc/ota_pubkey.pem"

    if find "${OEM_ROOT}" \( -name '*.a' -o -name '*.la' -o -name '*.o' \
        -o -name '*.map' -o -name '*.pc' -o -name CMakeFiles \
        -o -name pkgconfig -o -name include \) -print -quit | grep -q .; then
        echo "Development artifact leaked into production OEM staging" >&2
        exit 1
    fi
    (
        cd "${OEM_ROOT}"
        find . -xdev -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum
    ) >"${OUTPUT_DIR}/oem-files.sha256"
}

make_ext4_image() {
    local source_dir=$1
    local image=$2
    local size=$3
    local label=$4
    local uuid=$5
    local mount_dir=${WORK_DIR}/mnt-${label}
    local feature_opts='^64bit,^huge_file,^metadata_csum,^metadata_csum_seed,^dir_index,^orphan_file,^quota'
    mkdir -p "${mount_dir}"
    rm -f "${image}"
    truncate -s "${size}" "${image}"
    mkfs.ext4 -F -L "${label}" -U "${uuid}" -m 1 \
        -E lazy_itable_init=0,lazy_journal_init=0 \
        -O "${feature_opts}" "${image}"
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

stage_images() {
    test -s "${AGENT_CONFIG}" || {
        echo "Missing external Agent configuration: ${AGENT_CONFIG}" >&2
        exit 1
    }
    install -d -m 0755 \
        "${USERDATA_ROOT}/agent" "${USERDATA_ROOT}/ota" "${OTA_ROOT}" "${IMAGE_DIR}"
    install -m 0600 "${AGENT_CONFIG}" "${USERDATA_ROOT}/agent/agent.toml"
    sha256sum "${AGENT_CONFIG}" | awk '{print $1}' \
        >"${OUTPUT_DIR}/agent-config.sha256"
    make_ext4_image "${OEM_ROOT}" "${IMAGE_DIR}/oem.img" 256M oem "${OEM_UUID}"
    make_ext4_image "${USERDATA_ROOT}" "${IMAGE_DIR}/userdata.img" 3G userdata "${USERDATA_UUID}"
    make_ext4_image "${OTA_ROOT}" "${IMAGE_DIR}/ota.img" 300M ota "${OTA_UUID}"
    cp --reflink=auto "${ROOTFS_IMAGE}" "${IMAGE_DIR}/rootfs.img"
    for item in env.img idblock.img uboot.img misc.img boot_a.img boot_b.img download.bin; do
        cp "${SDK_DIR}/output/image/${item}" "${IMAGE_DIR}/${item}"
    done
    cp "${SDK_DIR}/output/image/.env.txt" "${OUTPUT_DIR}/bsp-env.txt"
    (
        cd "${IMAGE_DIR}"
        sha256sum boot_a.img boot_b.img oem.img rootfs.img userdata.img ota.img \
            >prepack-images.sha256
    )
}

finalize() {
    local path
    for path in "${IMAGE_DIR}"/* "${OUTPUT_DIR}/oem-files.sha256" \
        "${OUTPUT_DIR}/agent-config.sha256"; do
        [ -e "${path}" ] || continue
        chown "${HOST_UID:-0}:${HOST_GID:-0}" "${path}"
    done
    # Keep the output directory ownership consistent for host-side inspection.
    chown "${HOST_UID:-0}:${HOST_GID:-0}" "${IMAGE_DIR}"
    rm -rf "${WORK_DIR}"
}

stage_oem
stage_images
finalize
