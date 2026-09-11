#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT=/work
readonly OUTPUT_DIR=/out
readonly IMAGE_DIR=${OUTPUT_DIR}/image
readonly USERDATA_IMAGE=${IMAGE_DIR}/userdata.img
readonly OTA_CONFIG=/run/secrets/debian-ota-config.json
readonly MOUNT_DIR=${OUTPUT_DIR}/ota-config-work/userdata
readonly CONFIG_TARGET=${MOUNT_DIR}/debian/ota/config.json
readonly AUDIT_REPORT=${OUTPUT_DIR}/ota-config-audit.txt
readonly VALIDATOR=${REPO_ROOT}/scripts/debian-stage3/validate-ota-config.py

mounted=false
cleanup() {
    if ${mounted} && mountpoint -q "${MOUNT_DIR}"; then
        umount "${MOUNT_DIR}"
    fi
    rm -rf "${OUTPUT_DIR}/ota-config-work"
}
trap cleanup EXIT

for image in boot_a.img boot_b.img oem.img rootfs.img userdata.img; do
    test -s "${IMAGE_DIR}/${image}" || {
        echo "Missing Stage 3 factory image: ${IMAGE_DIR}/${image}" >&2
        exit 1
    }
done
test -s "${OTA_CONFIG}" || {
    echo "Missing Debian OTA device configuration: ${OTA_CONFIG}" >&2
    exit 1
}

"${VALIDATOR}" \
    --config "${OTA_CONFIG}" \
    --boot-a "${IMAGE_DIR}/boot_a.img" \
    --boot-b "${IMAGE_DIR}/boot_b.img" \
    --oem "${IMAGE_DIR}/oem.img" \
    --rootfs "${IMAGE_DIR}/rootfs.img" \
    >"${AUDIT_REPORT}"

mkdir -p "${MOUNT_DIR}"
mount -o loop "${USERDATA_IMAGE}" "${MOUNT_DIR}"
mounted=true
install -d -m 0700 "$(dirname "${CONFIG_TARGET}")"
install -m 0600 "${OTA_CONFIG}" "${CONFIG_TARGET}"
sync -f "${CONFIG_TARGET}"
sync -f "$(dirname "${CONFIG_TARGET}")"
umount "${MOUNT_DIR}"
mounted=false
e2fsck -fy "${USERDATA_IMAGE}"

(
    cd "${IMAGE_DIR}"
    sha256sum boot_a.img boot_b.img oem.img rootfs.img userdata.img ota.img \
        >prepack-images.sha256
)
chown "${HOST_UID:-0}:${HOST_GID:-0}" \
    "${USERDATA_IMAGE}" "${IMAGE_DIR}/prepack-images.sha256" "${AUDIT_REPORT}"
