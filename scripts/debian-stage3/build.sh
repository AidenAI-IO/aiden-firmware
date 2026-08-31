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
readonly IMAGE_DIR=${OUTPUT_DIR}/image
readonly SOURCE_SDK=${DEBIAN_STAGE3_SOURCE_SDK:-${REPO_ROOT}/pico-sdk}
readonly SOURCE_SDK_COMMIT=${DEBIAN_STAGE3_SOURCE_SDK_COMMIT:-a290a4345685e3c711d86ed78a39579e1e735328}
readonly STAGE2_OUTPUT=${DEBIAN_STAGE2_OUTPUT_DIR:-${REPO_ROOT}/output/debian-stage2}
readonly ROOTFS_BUILD_IMAGE=${DEBIAN_STAGE3_BUILD_IMAGE:-aiden-debian13-armhf-builder:stage3}
readonly BSP_BUILD_IMAGE=${DEBIAN_STAGE3_BSP_BUILD_IMAGE:-luckfoxtech/luckfox_pico:1.0}
readonly JOBS=${RK_JOBS:-$(getconf _NPROCESSORS_ONLN)}
readonly BUILD_EPOCH=${SOURCE_DATE_EPOCH:-1767360516}

usage() {
    cat <<'EOF'
Usage: scripts/debian-stage3/build.sh [all|builder|rootfs|bsp|images|config|audit]

Environment:
  DEBIAN_STAGE3_OUTPUT_DIR       Output directory (default: output/debian-stage3).
  DEBIAN_STAGE3_SOURCE_SDK       Clean source SDK (default: repository pico-sdk).
  DEBIAN_STAGE3_SOURCE_SDK_COMMIT
                                 Required source SDK commit.
  DEBIAN_STAGE2_OUTPUT_DIR       Audited Stage 2 application output.
  DEBIAN_STAGE3_BUILD_IMAGE      Rootfs/image builder image name.
  DEBIAN_STAGE3_BSP_BUILD_IMAGE  Luckfox BSP builder image name.
  OTA_PUBLIC_KEY_PATH            Production Ed25519 public key (required by images).
  AGENT_CONFIG_PATH              External agent.toml installed into userdata.img
                                 (required by images; never copied into the repository).
  OTA_DEVICE_CONFIG_PATH         Config generated from the signed release manifest
                                 (required by config and all).
  SOURCE_DATE_EPOCH              Rootfs archive/build metadata timestamp.
  RK_JOBS                        BSP build parallelism.

The images action creates generic rootfs.img and oem.img artifacts. The SDK
packer maps each generic image to both A/B partitions, so the two slots start
with identical bytes.

The config action validates factory hashes against those images, installs the
Debian-only config at /userdata/debian/ota/config.json inside userdata.img,
and repacks update.img. Run it after generating the signed release manifest.
EOF
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "Required command not found: $1" >&2
        exit 1
    }
}

validate_epoch() {
    case "${BUILD_EPOCH}" in
        '' | *[!0-9]*)
            echo "SOURCE_DATE_EPOCH must be an unsigned Unix timestamp: ${BUILD_EPOCH}" >&2
            exit 1
            ;;
    esac
}

ensure_sdk() {
    if [ ! -d "${SOURCE_SDK}/.git" ] && [ ! -f "${SOURCE_SDK}/.git" ]; then
        echo "Source Luckfox SDK is missing: ${SOURCE_SDK}" >&2
        exit 1
    fi
    if [ -n "$(git -C "${SOURCE_SDK}" status --porcelain)" ]; then
        echo "Source SDK must remain clean; refusing to clone dirty state: ${SOURCE_SDK}" >&2
        exit 1
    fi
    if [ "$(git -C "${SOURCE_SDK}" rev-parse HEAD)" != "${SOURCE_SDK_COMMIT}" ]; then
        echo "Source SDK commit mismatch" >&2
        echo "expected: ${SOURCE_SDK_COMMIT}" >&2
        echo "actual:   $(git -C "${SOURCE_SDK}" rev-parse HEAD)" >&2
        exit 1
    fi

    mkdir -p "${OUTPUT_DIR}"
    if [ ! -d "${SDK_DIR}/.git" ]; then
        git clone --shared --no-checkout "${SOURCE_SDK}" "${SDK_DIR}"
    fi
    git -C "${SDK_DIR}" checkout --detach --force "${SOURCE_SDK_COMMIT}"
    git -C "${SDK_DIR}" clean -ffd -e output/
    git -C "${SDK_DIR}" apply "${SCRIPT_DIR}/sdk-patches/0001-use-all-host-cpus.patch"
    git -C "${SDK_DIR}" apply "${SCRIPT_DIR}/sdk-patches/0002-append-slot-kernel-cmdline.patch"
    git -C "${SDK_DIR}" apply "${SCRIPT_DIR}/sdk-patches/0003-add-ab-images-action.patch"
    git -C "${SDK_DIR}" apply "${SCRIPT_DIR}/sdk-patches/0004-make-bsp-images-reproducible.patch"
    git -C "${SDK_DIR}" apply "${SCRIPT_DIR}/sdk-patches/0005-improve-rv1106-usb2-hs-margin.patch"
    git -C "${SDK_DIR}" apply "${SCRIPT_DIR}/sdk-patches/0006-fix-configfs-uevent-rebind-uaf.patch"

    install -m 0755 \
        "${SCRIPT_DIR}/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk" \
        "${SDK_DIR}/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk"
    install -m 0644 "${SCRIPT_DIR}/debian-stage3.config" \
        "${SDK_DIR}/sysdrv/source/kernel/arch/arm/configs/debian-stage3.config"
    ln -sfn \
        project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk \
        "${SDK_DIR}/.BoardConfig.mk"
    printf '%s\n' "${SOURCE_SDK_COMMIT}" >"${OUTPUT_DIR}/source-sdk-commit.txt"
}

docker_proxy_args() {
    local name
    for name in http_proxy https_proxy all_proxy no_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY; do
        if [ -n "${!name:-}" ]; then
            printf '%s\0%s\0' -e "${name}=${!name}"
        fi
    done
}

run_builder() {
    mkdir -p "${OUTPUT_DIR}"
    docker build -t "${ROOTFS_BUILD_IMAGE}" -f "${SCRIPT_DIR}/Dockerfile" "${REPO_ROOT}"
    docker image inspect "${ROOTFS_BUILD_IMAGE}" --format '{{.Id}}' \
        >"${OUTPUT_DIR}/rootfs-builder-image-id.txt"
}

run_rootfs_container() {
    local script=$1
    shift
    local image_id
    local -a proxy_args=()
    while IFS= read -r -d '' item; do proxy_args+=("${item}"); done < <(docker_proxy_args)
    image_id=$(docker image inspect "${ROOTFS_BUILD_IMAGE}" --format '{{.Id}}')
    docker run --rm --privileged \
        "${proxy_args[@]}" \
        -e "HOST_UID=$(id -u)" \
        -e "HOST_GID=$(id -g)" \
        -e "SOURCE_DATE_EPOCH=${BUILD_EPOCH}" \
        -e "DEBIAN_STAGE3_BUILD_IMAGE_ID=${image_id}" \
        -v "${REPO_ROOT}:/work:ro" \
        -v "${OUTPUT_DIR}:/out" \
        -w /work \
        "${ROOTFS_BUILD_IMAGE}" \
        bash "${script}" "$@"
}

run_rootfs() {
    docker image inspect "${ROOTFS_BUILD_IMAGE}" >/dev/null
    run_rootfs_container scripts/debian-stage3/container-build-rootfs.sh
}

run_bsp() {
    local source_git_common_dir build_timestamp
    ensure_sdk
    source_git_common_dir=$(git -C "${SOURCE_SDK}" rev-parse \
        --path-format=absolute --git-common-dir)
    build_timestamp=$(date -u -d "@${BUILD_EPOCH}" '+%Y-%m-%d %H:%M:%S UTC')
    docker image inspect "${BSP_BUILD_IMAGE}" --format '{{.Id}}' \
        >"${OUTPUT_DIR}/bsp-builder-image-id.txt"
    docker run --rm \
        -u "$(id -u):$(id -g)" \
        -e "RK_JOBS=${JOBS}" \
        -e "SOURCE_DATE_EPOCH=${BUILD_EPOCH}" \
        -e "KBUILD_BUILD_TIMESTAMP=${build_timestamp}" \
        -e KBUILD_BUILD_USER=aiden \
        -e KBUILD_BUILD_HOST=stage3 \
        -v "${SDK_DIR}:/sdk" \
        -v "${source_git_common_dir}:${source_git_common_dir}:ro" \
        -w /sdk \
        "${BSP_BUILD_IMAGE}" \
        bash -lc './build.sh uboot && ./build.sh driver && ./build.sh env && ./build.sh abimages'

    "${SCRIPT_DIR}/canonicalize-bsp.py" \
        --source-date-epoch "${BUILD_EPOCH}" \
        --fit "${SDK_DIR}/output/image/uboot.img" \
        --fit "${SDK_DIR}/output/image/boot_a.img" \
        --fit "${SDK_DIR}/output/image/boot_b.img" \
        --crc-table-source "${SDK_DIR}/sysdrv/source/uboot/u-boot/tools/rockchip/boot_merger.c" \
        --loader "${SDK_DIR}/output/image/download.bin"

    local kernel_config=${SDK_DIR}/sysdrv/source/objs_kernel/.config
    local uboot_config=${SDK_DIR}/sysdrv/source/uboot/u-boot/.config
    local env_text=${SDK_DIR}/output/image/.env.txt
    local symbol
    for symbol in \
        CONFIG_DEVTMPFS CONFIG_DEVTMPFS_MOUNT CONFIG_TMPFS \
        CONFIG_TMPFS_XATTR CONFIG_TMPFS_POSIX_ACL CONFIG_EXT4_FS \
        CONFIG_CGROUPS CONFIG_MEMCG CONFIG_BLK_CGROUP CONFIG_CGROUP_SCHED \
        CONFIG_CGROUP_PIDS CONFIG_NAMESPACES CONFIG_UTS_NS CONFIG_IPC_NS \
        CONFIG_PID_NS CONFIG_NET_NS CONFIG_SECCOMP CONFIG_SECCOMP_FILTER \
        CONFIG_AUTOFS_FS CONFIG_INOTIFY_USER CONFIG_EPOLL CONFIG_SIGNALFD \
        CONFIG_TIMERFD CONFIG_FHANDLE CONFIG_ZSMALLOC CONFIG_ZRAM \
        CONFIG_RFKILL CONFIG_BT CONFIG_BT_BREDR CONFIG_BT_RFCOMM \
        CONFIG_BT_RFCOMM_TTY CONFIG_BT_LE CONFIG_BT_HCIUART \
        CONFIG_BT_HCIUART_H4 CONFIG_CRYPTO_ECDH CONFIG_CRYPTO_CMAC; do
        grep -qx "${symbol}=y" "${kernel_config}" || {
            echo "Required production kernel setting is not enabled: ${symbol}" >&2
            exit 1
        }
    done
    for symbol in \
        CONFIG_CMD_ROCKUSB CONFIG_USB CONFIG_USB_GADGET \
        CONFIG_USB_GADGET_DOWNLOAD CONFIG_USB_DWC3 \
        CONFIG_USB_DWC3_GADGET; do
        grep -qx "${symbol}=y" "${uboot_config}" || {
            echo "Required production U-Boot setting is not enabled: ${symbol}" >&2
            exit 1
        }
    done
    grep -qx '# CONFIG_CMD_DFU is not set' "${uboot_config}" || {
        echo "DFU must remain disabled in the production eMMC A/B U-Boot" >&2
        exit 1
    }
    grep -qx '# CONFIG_FASTBOOT is not set' "${uboot_config}" || {
        echo "Fastboot must remain disabled in the production RockUSB U-Boot" >&2
        exit 1
    }
    grep -qx \
        'blkdevparts=mmcblk0:32K(env),512K@32K(idblock),256K(uboot),4M(misc),32M(boot_a),32M(boot_b),256M(oem_a),256M(oem_b),1536M(rootfs_a),1536M(rootfs_b),3G(userdata),300M(ota)' \
        "${env_text}"
    for item in env.img idblock.img uboot.img misc.img boot_a.img boot_b.img download.bin; do
        test -s "${SDK_DIR}/output/image/${item}"
    done
    test -s "${SDK_DIR}/output/out/sysdrv_out/kernel_drv_ko/aic8800_bsp.ko"
    test -s "${SDK_DIR}/output/out/sysdrv_out/kernel_drv_ko/aic8800_fdrv.ko"
    cp "${kernel_config}" "${OUTPUT_DIR}/kernel.config"
    cp "${env_text}" "${OUTPUT_DIR}/bsp-env.txt"
    DEBIAN_STAGE3_OUTPUT_DIR="${OUTPUT_DIR}" "${SCRIPT_DIR}/audit-bsp.sh"
}

run_images() {
    ensure_sdk
    docker image inspect "${ROOTFS_BUILD_IMAGE}" >/dev/null
    test -s "${OUTPUT_DIR}/rootfs.ext4" || {
        echo "Missing Stage 3 rootfs; run the rootfs action first" >&2
        exit 1
    }
    test -d "${STAGE2_OUTPUT}/apps" || {
        echo "Missing Stage 2 applications: ${STAGE2_OUTPUT}/apps" >&2
        exit 1
    }
    grep -qx 'status=pass' "${STAGE2_OUTPUT}/apps-audit/summary.txt" || {
        echo "Stage 2 application audit has not passed" >&2
        exit 1
    }
    if [ -z "${OTA_PUBLIC_KEY_PATH:-}" ] || [ ! -f "${OTA_PUBLIC_KEY_PATH}" ]; then
        echo "OTA_PUBLIC_KEY_PATH must name a production Ed25519 public key" >&2
        exit 1
    fi
    if [ -z "${AGENT_CONFIG_PATH:-}" ] || [ ! -f "${AGENT_CONFIG_PATH}" ]; then
        echo "AGENT_CONFIG_PATH must name an external agent.toml" >&2
        exit 1
    fi
    "${STAGE2_OUTPUT}/apps/bin/agent" config-check --format=json \
        --config="${AGENT_CONFIG_PATH}" >"${OUTPUT_DIR}/agent-config-validation.json"

    local image_id
    image_id=$(docker image inspect "${ROOTFS_BUILD_IMAGE}" --format '{{.Id}}')
    docker run --rm --privileged \
        -e "HOST_UID=$(id -u)" \
        -e "HOST_GID=$(id -g)" \
        -e "SOURCE_DATE_EPOCH=${BUILD_EPOCH}" \
        -e "DEBIAN_STAGE3_BUILD_IMAGE_ID=${image_id}" \
        -v "${REPO_ROOT}:/work:ro" \
        -v "${OUTPUT_DIR}:/out" \
        -v "${STAGE2_OUTPUT}/apps:/apps:ro" \
        -v "${STAGE2_OUTPUT}/apps-audit:/apps-audit:ro" \
        -v "${OTA_PUBLIC_KEY_PATH}:/run/secrets/ota_pubkey.pem:ro" \
        -v "${AGENT_CONFIG_PATH}:/run/secrets/agent.toml:ro" \
        -w /work \
        "${ROOTFS_BUILD_IMAGE}" \
        bash scripts/debian-stage3/container-assemble-images.sh

    run_sdk_packer
    test -s "${IMAGE_DIR}/update.img"
}

run_sdk_packer() {
    # The image assembly container may use a rootless UID mapping for image/.
    # Run the SDK packer in that same container so it can create package-file
    # beside the inputs without relying on host ownership of the mount.
    docker run --rm \
        -v "${OUTPUT_DIR}:/out" \
        -w /out/luckfox-pico-sdk \
        "${ROOTFS_BUILD_IMAGE}" \
        bash -lc 'tools/linux/Linux_Pack_Firmware/mk-update_pack.sh -id rv1106 -i /out/image'
}

run_config() {
    ensure_sdk
    docker image inspect "${ROOTFS_BUILD_IMAGE}" >/dev/null
    if [ -z "${OTA_DEVICE_CONFIG_PATH:-}" ] || [ ! -f "${OTA_DEVICE_CONFIG_PATH}" ]; then
        echo "OTA_DEVICE_CONFIG_PATH must name a config generated from the signed release manifest" >&2
        exit 1
    fi
    for image in boot_a.img boot_b.img oem.img rootfs.img userdata.img ota.img; do
        test -s "${IMAGE_DIR}/${image}" || {
            echo "Missing Stage 3 image ${IMAGE_DIR}/${image}; run the images action first" >&2
            exit 1
        }
    done

    local image_id
    image_id=$(docker image inspect "${ROOTFS_BUILD_IMAGE}" --format '{{.Id}}')
    docker run --rm --privileged \
        -e "HOST_UID=$(id -u)" \
        -e "HOST_GID=$(id -g)" \
        -e "DEBIAN_STAGE3_BUILD_IMAGE_ID=${image_id}" \
        -v "${REPO_ROOT}:/work:ro" \
        -v "${OUTPUT_DIR}:/out" \
        -v "${OTA_DEVICE_CONFIG_PATH}:/run/secrets/debian-ota-config.json:ro" \
        -w /work \
        "${ROOTFS_BUILD_IMAGE}" \
        bash scripts/debian-stage3/container-install-ota-config.sh

    run_sdk_packer
    test -s "${IMAGE_DIR}/update.img"
}

run_audit() {
    ensure_sdk
    docker image inspect "${ROOTFS_BUILD_IMAGE}" >/dev/null
    run_rootfs_container scripts/debian-stage3/container-audit-images.sh
}

main() {
    local action=${1:-all}
    case "${action}" in
        -h | --help | help)
            usage
            return
            ;;
        all | builder | rootfs | bsp | images | config | audit) ;;
        *)
            usage >&2
            exit 2
            ;;
    esac

    require_command docker
    require_command git
    require_command python3
    require_command sha256sum
    validate_epoch
    if [ "${action}" != help ]; then mkdir -p "${OUTPUT_DIR}"; fi

    case "${action}" in
        all)
            run_builder
            run_rootfs
            run_bsp
            run_images
            run_config
            run_audit
            ;;
        builder) run_builder ;;
        rootfs) run_rootfs ;;
        bsp) run_bsp ;;
        images) run_images ;;
        config) run_config ;;
        audit) run_audit ;;
    esac
}

main "$@"
