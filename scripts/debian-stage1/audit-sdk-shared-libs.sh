#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <sdk-dir> <report.tsv>" >&2
    exit 2
fi

readonly SDK_DIR=$1
readonly REPORT=$2

if [ ! -d "${SDK_DIR}/media" ] || [ ! -d "${SDK_DIR}/project" ]; then
    echo "Luckfox SDK media/project directories are missing: ${SDK_DIR}" >&2
    exit 1
fi

classify() {
    local machine=$1
    local needed=$2

    if tr ',' '\n' <<<"${needed}" | grep -qx 'libc.so'; then
        case "${machine}" in
        ARM)
            printf 'arm32-android-bionic'
            ;;
        AArch64)
            printf 'aarch64-android-bionic'
            ;;
        *)
            printf 'android-bionic-other-arch'
            ;;
        esac
    elif grep -qE '(^|,)(libc\.so\.0|ld-uClibc[^,]*)(,|$)' <<<"${needed}"; then
        case "${machine}" in
        ARM)
            printf 'arm32-uclibc'
            ;;
        AArch64)
            printf 'aarch64-uclibc'
            ;;
        *)
            printf 'uclibc-other-arch'
            ;;
        esac
    elif grep -qE '(^|,)(libc\.so\.6|ld-linux-armhf\.so\.3|ld-linux-aarch64\.so\.1)(,|$)' \
        <<<"${needed}"; then
        case "${machine}" in
        ARM)
            printf 'arm32-glibc'
            ;;
        AArch64)
            printf 'aarch64-glibc'
            ;;
        *)
            printf 'glibc-other-arch'
            ;;
        esac
    else
        case "${machine}" in
        ARM)
            printf 'arm32-unknown-libc'
            ;;
        AArch64)
            printf 'aarch64-unknown-libc'
            ;;
        *)
            printf 'unknown'
            ;;
        esac
    fi
}

mkdir -p "$(dirname "${REPORT}")"
printf 'classification\tmachine\thard_float\tsha256\tbytes\tsoname\tneeded\tpath\n' \
    >"${REPORT}"

while IFS= read -r -d '' file; do
    if ! head -c 4 "${file}" | grep -q $'\177ELF'; then
        echo "SDK .so candidate is not ELF: ${file#${SDK_DIR}/}" >&2
        exit 1
    fi

    dynamic=$(readelf -d "${file}" 2>/dev/null || true)
    machine=$(readelf -h "${file}" \
        | sed -n 's/^[[:space:]]*Machine:[[:space:]]*//p')
    soname=$(sed -n 's/.*(SONAME).*\[\([^]]*\)\].*/\1/p' <<<"${dynamic}")
    needed=$(sed -n 's/.*(NEEDED).*\[\([^]]*\)\].*/\1/p' <<<"${dynamic}" \
        | paste -sd, -)
    hard_float=not-applicable
    if [ "${machine}" = ARM ]; then
        if readelf -A "${file}" 2>/dev/null \
            | grep -q 'Tag_ABI_VFP_args: VFP registers'; then
            hard_float=yes
        else
            hard_float=not-declared
        fi
    fi

    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$(classify "${machine}" "${needed}")" \
        "${machine}" \
        "${hard_float}" \
        "$(sha256sum "${file}" | awk '{print $1}')" \
        "$(stat -c %s "${file}")" \
        "${soname}" \
        "${needed}" \
        "${file#${SDK_DIR}/}" >>"${REPORT}"
done < <(find "${SDK_DIR}/media" "${SDK_DIR}/project" \
    -type f -name '*.so*' -print0 | sort -z)

if [ "$(wc -l <"${REPORT}")" -le 1 ]; then
    echo "No SDK shared objects were inventoried" >&2
    exit 1
fi
