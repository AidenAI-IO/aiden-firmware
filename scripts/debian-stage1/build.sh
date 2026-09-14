#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly DEFAULT_OUTPUT_DIR="${REPO_ROOT}/output/debian-stage1"
if [ -n "${DEBIAN_STAGE1_OUTPUT_DIR:-}" ]; then
    if [[ "${DEBIAN_STAGE1_OUTPUT_DIR}" = /* ]]; then
        OUTPUT_DIR="${DEBIAN_STAGE1_OUTPUT_DIR}"
    else
        OUTPUT_DIR="${REPO_ROOT}/${DEBIAN_STAGE1_OUTPUT_DIR}"
    fi
else
    OUTPUT_DIR="${DEFAULT_OUTPUT_DIR}"
fi
readonly OUTPUT_DIR
readonly SDK_DIR="${OUTPUT_DIR}/luckfox-pico-sdk"
readonly IMAGE_DIR="${OUTPUT_DIR}/image"
readonly ROOTFS_IMAGE="${OUTPUT_DIR}/rootfs.ext4"
readonly ORIGINAL_SDK="${LUCKFOX_ORIGINAL_SDK:-/home/miaomiao/dev/luckfox/luckfox-pico}"
readonly ORIGINAL_SDK_COMMIT="${LUCKFOX_ORIGINAL_SDK_COMMIT:-824b817f889c2cbff1d48fcdb18ab494a68f69d1}"
readonly BUILD_IMAGE="${DEBIAN_STAGE1_BUILD_IMAGE:-luckfoxtech/luckfox_pico:1.0}"
readonly JOBS="${RK_JOBS:-$(getconf _NPROCESSORS_ONLN)}"

usage() {
    cat <<'EOF'
Usage: scripts/debian-stage1/build.sh [all|rootfs|bsp|image|audit]

Environment:
  LUCKFOX_ORIGINAL_SDK       Original Luckfox SDK checkout.
  LUCKFOX_ORIGINAL_SDK_COMMIT
                             Commit used for the isolated stage-1 SDK clone.
  DEBIAN_STAGE1_OUTPUT_DIR   Output directory (relative paths use the repository root).
  DEBIAN_WIFI_SSID/DEBIAN_WIFI_PSK
                             Optional development Wi-Fi credentials.
  RK_JOBS                    BSP build parallelism (defaults to all CPUs).
EOF
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "Required command not found: $1" >&2
        exit 1
    }
}

record_build_image() {
    local image_id
    local repo_digests
    image_id=$(docker image inspect "${BUILD_IMAGE}" --format '{{.Id}}')
    repo_digests=$(docker image inspect "${BUILD_IMAGE}" --format '{{join .RepoDigests ","}}')
    {
        printf 'reference=%s\n' "${BUILD_IMAGE}"
        printf 'image_id=%s\n' "${image_id}"
        printf 'repo_digests=%s\n' "${repo_digests}"
    } >"${OUTPUT_DIR}/build-container.txt"
}

ensure_sdk() {
    if [ ! -d "${ORIGINAL_SDK}/.git" ]; then
        echo "Original Luckfox SDK is missing: ${ORIGINAL_SDK}" >&2
        exit 1
    fi
    if ! git -C "${ORIGINAL_SDK}" cat-file -e "${ORIGINAL_SDK_COMMIT}^{commit}"; then
        echo "Original SDK does not contain commit ${ORIGINAL_SDK_COMMIT}" >&2
        exit 1
    fi

    mkdir -p "${OUTPUT_DIR}"
    if [ ! -d "${SDK_DIR}/.git" ]; then
        git clone --shared --no-checkout "${ORIGINAL_SDK}" "${SDK_DIR}"
    fi
    git -C "${SDK_DIR}" checkout --detach --force "${ORIGINAL_SDK_COMMIT}"
    git -C "${SDK_DIR}" apply \
        "${SCRIPT_DIR}/sdk-patches/0001-use-all-host-cpus.patch"
    git -C "${SDK_DIR}" apply \
        "${SCRIPT_DIR}/sdk-patches/0002-append-extra-kernel-cmdline.patch"

    install -m 0755 "${SCRIPT_DIR}/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk" \
        "${SDK_DIR}/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk"
    install -m 0644 "${SCRIPT_DIR}/debian-stage1.config" \
        "${SDK_DIR}/sysdrv/source/kernel/arch/arm/configs/debian-stage1.config"
    ln -sfn "project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk" \
        "${SDK_DIR}/.BoardConfig.mk"

    printf '%s\n' "${ORIGINAL_SDK_COMMIT}" >"${OUTPUT_DIR}/original-sdk-commit.txt"
}

docker_env_args() {
    local name
    for name in http_proxy https_proxy all_proxy no_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY; do
        if [ -n "${!name:-}" ]; then
            printf '%s\0%s\0' -e "${name}=${!name}"
        fi
    done
}

docker_output_args() {
    if [ "${OUTPUT_DIR}" != "${DEFAULT_OUTPUT_DIR}" ]; then
        printf '%s\0%s\0' -v "${OUTPUT_DIR}:/work/output/debian-stage1"
    fi
}

run_rootfs() {
    ensure_sdk
    "${SCRIPT_DIR}/audit-vendor-libs.sh" \
        "${SDK_DIR}" "${OUTPUT_DIR}/vendor-libs-audit.tsv"
    "${SCRIPT_DIR}/audit-sdk-shared-libs.sh" \
        "${SDK_DIR}" "${OUTPUT_DIR}/sdk-shared-libs-inventory.tsv"
    local -a proxy_args=()
    while IFS= read -r -d '' item; do
        proxy_args+=("${item}")
    done < <(docker_env_args)
    local -a output_args=()
    while IFS= read -r -d '' item; do
        output_args+=("${item}")
    done < <(docker_output_args)

    docker run --rm --privileged \
        "${proxy_args[@]}" \
        -e "HOST_UID=$(id -u)" \
        -e "HOST_GID=$(id -g)" \
        -e "DEBIAN_WIFI_SSID=${DEBIAN_WIFI_SSID:-}" \
        -e "DEBIAN_WIFI_PSK=${DEBIAN_WIFI_PSK:-}" \
        -v "${REPO_ROOT}:/work" \
        "${output_args[@]}" \
        -w /work \
        "${BUILD_IMAGE}" \
        bash scripts/debian-stage1/container-build-rootfs.sh
}

run_bsp() {
    ensure_sdk
    echo "Building original Luckfox BSP with RK_JOBS=${JOBS}"
    docker run --rm \
        -u "$(id -u):$(id -g)" \
        -e "RK_JOBS=${JOBS}" \
        -v "${SDK_DIR}:/sdk" \
        -v "${ORIGINAL_SDK}:${ORIGINAL_SDK}:ro" \
        -w /sdk \
        "${BUILD_IMAGE}" \
        bash -lc './build.sh uboot && ./build.sh driver && ./build.sh env'

    local kernel_config=${SDK_DIR}/sysdrv/source/objs_kernel/.config
    local env_text=${SDK_DIR}/output/image/.env.txt
    local symbol
    for symbol in \
        CONFIG_DEVTMPFS CONFIG_DEVTMPFS_MOUNT CONFIG_TMPFS \
        CONFIG_TMPFS_XATTR CONFIG_TMPFS_POSIX_ACL CONFIG_EXT4_FS \
        CONFIG_CGROUPS CONFIG_MEMCG \
        CONFIG_BLK_CGROUP CONFIG_CGROUP_SCHED CONFIG_CGROUP_PIDS \
        CONFIG_CGROUP_FREEZER CONFIG_CGROUP_DEVICE CONFIG_CGROUP_CPUACCT \
        CONFIG_NAMESPACES CONFIG_UTS_NS CONFIG_IPC_NS CONFIG_PID_NS \
        CONFIG_NET_NS CONFIG_SECCOMP CONFIG_SECCOMP_FILTER CONFIG_AUTOFS_FS \
        CONFIG_INOTIFY_USER CONFIG_EPOLL CONFIG_SIGNALFD CONFIG_TIMERFD \
        CONFIG_FHANDLE CONFIG_ZSMALLOC CONFIG_ZRAM \
        CONFIG_RFKILL CONFIG_BT CONFIG_BT_BREDR CONFIG_BT_RFCOMM \
        CONFIG_BT_RFCOMM_TTY CONFIG_BT_LE CONFIG_BT_HCIUART \
        CONFIG_BT_HCIUART_H4 CONFIG_CRYPTO_ECDH CONFIG_CRYPTO_CMAC; do
        if ! grep -qx "${symbol}=y" "${kernel_config}"; then
            echo "Required kernel setting is not enabled: ${symbol}" >&2
            exit 1
        fi
    done
    grep -qx 'sys_bootargs= root=/dev/mmcblk0p7 rootfstype=ext4 rk_dma_heap_cma=100M net.ifnames=0' \
        "${env_text}"
    grep -qx 'blkdevparts=mmcblk0:32K(env),512K@32K(idblock),256K(uboot),32M(boot),512M(oem),256M(userdata),6G(rootfs)' \
        "${env_text}"
    test -f "${SDK_DIR}/output/image/boot.img"
    test -f "${SDK_DIR}/output/out/sysdrv_out/kernel_drv_ko/aic8800_bsp.ko"
    test -f "${SDK_DIR}/output/out/sysdrv_out/kernel_drv_ko/aic8800_fdrv.ko"
    cp "${kernel_config}" "${OUTPUT_DIR}/kernel.config"
    cp "${env_text}" "${OUTPUT_DIR}/bsp-env.txt"
}

run_image() {
    ensure_sdk
    "${SCRIPT_DIR}/audit-vendor-libs.sh" \
        "${SDK_DIR}" "${OUTPUT_DIR}/vendor-libs-audit.tsv"
    "${SCRIPT_DIR}/audit-sdk-shared-libs.sh" \
        "${SDK_DIR}" "${OUTPUT_DIR}/sdk-shared-libs-inventory.tsv"
    if [ ! -f "${ROOTFS_IMAGE}" ]; then
        echo "Missing Debian rootfs image: ${ROOTFS_IMAGE}" >&2
        exit 1
    fi
    if [ ! -f "${SDK_DIR}/output/image/boot.img" ]; then
        echo "Missing BSP boot image; run the bsp stage first" >&2
        exit 1
    fi

    local -a output_args=()
    while IFS= read -r -d '' item; do
        output_args+=("${item}")
    done < <(docker_output_args)

    docker run --rm --privileged \
        -e "HOST_UID=$(id -u)" \
        -e "HOST_GID=$(id -g)" \
        -v "${REPO_ROOT}:/work" \
        "${output_args[@]}" \
        -w /work \
        "${BUILD_IMAGE}" \
        bash scripts/debian-stage1/container-assemble-images.sh

    rm -rf "${IMAGE_DIR}/unpacked"
    mkdir -p "${IMAGE_DIR}/unpacked"
    "${SDK_DIR}/tools/linux/Linux_Pack_Firmware/mk-update_pack.sh" \
        -id rv1106 -i "${IMAGE_DIR}"
    "${SDK_DIR}/tools/linux/Linux_Pack_Firmware/mk-update_unpack.sh" \
        -i "${IMAGE_DIR}/update.img" -o "${IMAGE_DIR}/unpacked"
    for item in env.img idblock.img uboot.img boot.img oem.img userdata.img rootfs.img; do
        cmp "${IMAGE_DIR}/${item}" "${IMAGE_DIR}/unpacked/Image/${item}"
    done
    cmp "${IMAGE_DIR}/download.bin" "${IMAGE_DIR}/unpacked/download.bin"
}

run_audit() {
    mkdir -p "${OUTPUT_DIR}"
    local -a output_args=()
    while IFS= read -r -d '' item; do
        output_args+=("${item}")
    done < <(docker_output_args)

    docker run --rm --privileged \
        -e "HOST_UID=$(id -u)" \
        -e "HOST_GID=$(id -g)" \
        -v "${REPO_ROOT}:/work" \
        "${output_args[@]}" \
        -w /work \
        "${BUILD_IMAGE}" \
        bash scripts/debian-stage1/container-audit-images.sh
}

main() {
    local action=${1:-all}

    case "${action}" in
    -h | --help | help)
        usage
        return
        ;;
    all | rootfs | bsp | image | audit)
        ;;
    *)
        usage >&2
        exit 2
        ;;
    esac

    require_command docker
    require_command git
    mkdir -p "${OUTPUT_DIR}"

    case "${action}" in
    all)
        run_rootfs
        run_bsp
        run_image
        run_audit
        ;;
    rootfs)
        run_rootfs
        ;;
    bsp)
        run_bsp
        ;;
    image)
        run_image
        ;;
    audit)
        run_audit
        ;;
    esac

    # docker run pulls a missing tag automatically. Resolve and record the
    # exact image only after the selected stage has used it successfully.
    record_build_image
}

main "$@"
