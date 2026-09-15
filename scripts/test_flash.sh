#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly FLASH_HELPER="${REPO_ROOT}/scripts/flash.sh"

fail() {
    echo "Flash helper test failure: $*" >&2
    exit 1
}

bash -n "${FLASH_HELPER}"
[ -x "${FLASH_HELPER}" ] || fail "scripts/flash.sh is not executable"

"${FLASH_HELPER}" --help >/dev/null
grep -q -- '--confirm-erase-all-data' "${FLASH_HELPER}" \
    || fail "flash helper does not require explicit confirmation"
grep -q 'device_count.*-ne 1' "${FLASH_HELPER}" \
    || fail "flash helper does not require exactly one target"
grep -q 'Mode=(Loader|Maskrom)' "${FLASH_HELPER}" \
    || fail "flash helper does not require Loader or Maskrom mode"
grep -q 'pico-sdk/tools/linux/Linux_Upgrade_Tool/upgrade_tool' "${FLASH_HELPER}" \
    || fail "flash helper does not default to the repository pico-sdk upgrade_tool"

test_dir=$(mktemp -d)
trap 'rm -rf "${test_dir}"' EXIT

cat >"${test_dir}/upgrade_tool" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
ld)
    printf '%s\n' \
        'DevNo=1 Vid=0x2207,Pid=0x110c,LocationID=1 Mode=Loader SerialNo=test'
    ;;
uf)
    printf '%s\n' called >"${FLASH_TEST_MARKER}"
    ;;
esac
EOF
chmod +x "${test_dir}/upgrade_tool"

FLASH_TEST_MARKER="${test_dir}/flash-called" \
    "${FLASH_HELPER}" inspect --tool "${test_dir}/upgrade_tool" >/dev/null
[ ! -e "${test_dir}/flash-called" ] \
    || fail "inspect action wrote to the device"

if FLASH_TEST_MARKER="${test_dir}/flash-called" \
    "${FLASH_HELPER}" flash --tool "${test_dir}/upgrade_tool" >/dev/null 2>&1; then
    fail "flash helper accepted a destructive action without confirmation"
fi
[ ! -e "${test_dir}/flash-called" ] \
    || fail "flash helper wrote to the device without confirmation"

# A mock tool that reports two targets must be rejected in inspect mode.
cat >"${test_dir}/upgrade_tool_multi" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' \
    'DevNo=1 Vid=0x2207,Pid=0x110c,LocationID=1 Mode=Loader SerialNo=a' \
    'DevNo=2 Vid=0x2207,Pid=0x110c,LocationID=2 Mode=Maskrom SerialNo=b'
EOF
chmod +x "${test_dir}/upgrade_tool_multi"
if "${FLASH_HELPER}" inspect --tool "${test_dir}/upgrade_tool_multi" >/dev/null 2>&1; then
    fail "flash helper accepted multiple Rockchip targets"
fi

echo "Flash helper checks passed"
