#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly REPO_ROOT
if [ -n "${DEBIAN_STAGE1_OUTPUT_DIR:-}" ]; then
    if [[ "${DEBIAN_STAGE1_OUTPUT_DIR}" = /* ]]; then
        OUTPUT_DIR=${DEBIAN_STAGE1_OUTPUT_DIR}
    else
        OUTPUT_DIR=${REPO_ROOT}/${DEBIAN_STAGE1_OUTPUT_DIR}
    fi
else
    OUTPUT_DIR=${REPO_ROOT}/output/debian-stage1
fi
readonly OUTPUT_DIR
readonly DEFAULT_TOOL="${OUTPUT_DIR}/luckfox-pico-sdk/tools/linux/Linux_Upgrade_Tool/upgrade_tool"
readonly DEFAULT_IMAGE="${OUTPUT_DIR}/image/update.img"
readonly DEFAULT_SUMS="${OUTPUT_DIR}/SHA256SUMS"

usage() {
    cat <<'EOF'
Usage:
  scripts/debian-stage1/flash.sh inspect [--tool PATH]
  scripts/debian-stage1/flash.sh flash --confirm-erase-all-data [options]

Options:
  --tool PATH              Rockchip upgrade_tool to use.
  --image PATH             Factory update.img to flash.
  --sha256 HEX             Required for a non-default image.
  --confirm-erase-all-data Required confirmation for the destructive flash.
  -h, --help               Show this help without probing USB devices.

The flash action requires root, exactly one Rockchip device in Loader or
Maskrom mode, and a verified image hash. It overwrites the factory partition
layout and userdata. This script never attempts a flash without the explicit
confirmation option.
EOF
}

action=${1:-help}
case "${action}" in
inspect | flash)
    shift
    ;;
-h | --help | help)
    usage
    exit 0
    ;;
*)
    echo "Unknown action: ${action}" >&2
    usage >&2
    exit 2
    ;;
esac

tool=${DEFAULT_TOOL}
image=${DEFAULT_IMAGE}
expected_sha256=
confirmed=no
while [ "$#" -gt 0 ]; do
    case "$1" in
    --tool)
        [ "$#" -ge 2 ] || { echo "--tool requires a path" >&2; exit 2; }
        tool=$2
        shift 2
        ;;
    --image)
        [ "$#" -ge 2 ] || { echo "--image requires a path" >&2; exit 2; }
        image=$2
        shift 2
        ;;
    --sha256)
        [ "$#" -ge 2 ] || { echo "--sha256 requires a hash" >&2; exit 2; }
        expected_sha256=$2
        shift 2
        ;;
    --confirm-erase-all-data)
        confirmed=yes
        shift
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *)
        echo "Unknown option: $1" >&2
        exit 2
        ;;
    esac
done

[ -x "${tool}" ] || { echo "upgrade_tool is not executable: ${tool}" >&2; exit 1; }
if [ "${action}" = flash ]; then
    [ "${confirmed}" = yes ] || {
        echo "Refusing to flash without --confirm-erase-all-data" >&2
        exit 2
    }
    [ "$(id -u)" -eq 0 ] || {
        echo "Run the flash action as root, for example with sudo" >&2
        exit 1
    }
    [ -f "${image}" ] || { echo "Firmware image is missing: ${image}" >&2; exit 1; }

    if [ -z "${expected_sha256}" ]; then
        if [ "${image}" != "${DEFAULT_IMAGE}" ]; then
            echo "A non-default image requires --sha256" >&2
            exit 2
        fi
        [ -f "${DEFAULT_SUMS}" ] || {
            echo "Checksum manifest is missing: ${DEFAULT_SUMS}" >&2
            exit 1
        }
        expected_sha256=$(awk '$2 == "image/update.img" {print $1}' "${DEFAULT_SUMS}")
    fi
    printf '%s\n' "${expected_sha256}" | grep -Eq '^[[:xdigit:]]{64}$' || {
        echo "Invalid expected SHA-256: ${expected_sha256}" >&2
        exit 2
    }
    actual_sha256=$(sha256sum "${image}" | awk '{print $1}')
    if [ "${actual_sha256,,}" != "${expected_sha256,,}" ]; then
        echo "Firmware SHA-256 mismatch" >&2
        echo "expected=${expected_sha256}" >&2
        echo "actual=${actual_sha256}" >&2
        exit 1
    fi
fi

device_output=$("${tool}" ld 2>&1 | tr -d '\r') || {
    printf '%s\n' "${device_output}" >&2
    echo "upgrade_tool failed to list devices" >&2
    exit 1
}
printf '%s\n' "${device_output}"
device_count=$(grep -Ec '(^|[[:space:]])DevNo=' <<<"${device_output}" || true)
if [ "${device_count}" -ne 1 ]; then
    echo "Expected exactly one Rockchip target, found ${device_count}" >&2
    exit 1
fi
device_line=$(grep -E '(^|[[:space:]])DevNo=' <<<"${device_output}")
grep -q 'Vid=0x2207' <<<"${device_line}" || {
    echo "The single target is not a Rockchip USB device: ${device_line}" >&2
    exit 1
}
grep -Eq 'Mode=(Loader|Maskrom)' <<<"${device_line}" || {
    echo "Target is not in Loader or Maskrom mode: ${device_line}" >&2
    exit 1
}

if [ "${action}" = inspect ]; then
    echo "Exactly one flashable Rockchip target is present. No data was written."
    exit 0
fi

log_dir=${OUTPUT_DIR}/hardware-validation
mkdir -p "${log_dir}"
log=${log_dir}/flash-$(date -u '+%Y%m%dT%H%M%SZ')-$$.log
{
    printf 'tool=%s\n' "${tool}"
    printf 'tool_sha256=%s\n' "$(sha256sum "${tool}" | awk '{print $1}')"
    printf 'image=%s\n' "${image}"
    printf 'sha256=%s\n' "${actual_sha256}"
    printf 'device=%s\n' "${device_line}"
    printf 'started_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
} >"${log}"

echo "Flashing ${image}; this overwrites the factory layout and userdata."
set +e
"${tool}" uf "${image}" 2>&1 | tee -a "${log}"
flash_status=${PIPESTATUS[0]}
set -e
printf 'finished_utc=%s\nexit_status=%s\n' \
    "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "${flash_status}" >>"${log}"
if [ "${flash_status}" -ne 0 ]; then
    echo "Flash failed; preserve the log: ${log}" >&2
    exit "${flash_status}"
fi
echo "Flash command completed successfully. Log: ${log}"
echo "Capture the complete first-boot UART log before running board validation."
