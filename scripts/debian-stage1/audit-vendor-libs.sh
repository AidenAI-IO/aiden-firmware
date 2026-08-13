#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <sdk-dir> <report.tsv>" >&2
    exit 2
fi

readonly SDK_DIR=$1
readonly REPORT=$2

classify() {
    local file=$1
    local dynamic
    dynamic=$(readelf -d "${file}" 2>/dev/null || true)
    if grep -qE 'libc\.so\.0|ld-uClibc' <<<"${dynamic}"; then
        printf 'uclibc'
    elif grep -qE 'libc\.so\.6|ld-linux-armhf\.so\.3' <<<"${dynamic}"; then
        printf 'glibc'
    elif [[ "${file}" == *.a ]]; then
        if strings "${file}" | grep -qE 'libc\.so\.0|ld-uClibc'; then
            printf 'uclibc-or-mixed-static'
        else
            printf 'static-unproven'
        fi
    else
        printf 'unknown'
    fi
}

mkdir -p "$(dirname "${REPORT}")"
printf 'classification\tsha256\tbytes\tpath\tneeded\n' >"${REPORT}"

roots=(
    "${SDK_DIR}/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-glibc-rockchip/usr/lib"
    "${SDK_DIR}/media/rockit/rockit/lib/lib32"
    "${SDK_DIR}/media/mpp/release_mpp_rv1106_arm-rockchip830-linux-uclibcgnueabihf/lib"
    "${SDK_DIR}/media/rga/release_rga_rv1106_arm-rockchip830-linux-uclibcgnueabihf/lib"
    "${SDK_DIR}/media/isp/release_camera_engine_rkaiq_rv1106_arm-rockchip830-linux-uclibcgnueabihf/lib"
)

for root in "${roots[@]}"; do
    [ -d "${root}" ] || continue
    while IFS= read -r -d '' file; do
        needed=$(readelf -d "${file}" 2>/dev/null \
            | sed -n 's/.*Shared library: \[\([^]]*\)\].*/\1/p' \
            | paste -sd, -)
        printf '%s\t%s\t%s\t%s\t%s\n' \
            "$(classify "${file}")" \
            "$(sha256sum "${file}" | awk '{print $1}')" \
            "$(stat -c %s "${file}")" \
            "${file#${SDK_DIR}/}" \
            "${needed}" >>"${REPORT}"
    done < <(find "${root}" -type f \( -name '*.so*' -o -name '*.a' \) -print0 | sort -z)
done

