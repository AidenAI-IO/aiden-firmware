#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly HELPER="${REPO_ROOT}/overlay-debian/usr/lib/aiden/aiden-agent-control"
readonly TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() {
    echo "Debian agent control test failure: $*" >&2
    exit 1
}

[ -x "${HELPER}" ] || fail "helper is not executable"
sh -n "${HELPER}"

cat >"${TEST_ROOT}/systemctl" <<'EOF'
#!/bin/sh
if [ -n "${AIDEN_SYSTEMCTL_LOG:-}" ]; then
    printf '%s\n' "$*" >>"${AIDEN_SYSTEMCTL_LOG}"
fi
case "${1:-}" in
    is-active)
        [ "${AIDEN_SYSTEMCTL_INACTIVE:-0}" != 1 ] || exit 3
        [ "${2:-}" = --quiet ]
        [ "${3:-}" = aiden-agent.service ]
        ;;
    show)
        [ "${2:-}" = aiden-agent.service ] || exit 1
        [ "${3:-}" = -p ] || exit 1
        [ "${4:-}" = MainPID ] || exit 1
        [ "${5:-}" = --value ] || exit 1
        printf '%s\n' 745
        ;;
    restart)
        [ "${2:-}" = aiden-environment.service ] || [ "${2:-}" = aiden-agent.service ]
        ;;
    start)
        [ "${2:-}" = aiden.target ]
        ;;
    *)
        exit 2
        ;;
esac
EOF
chmod 0755 "${TEST_ROOT}/systemctl"

output="$(PATH="${TEST_ROOT}:${PATH}" "${HELPER}" status)"
grep -qx 'watchdog=running pid=745 supervisor=systemd' <<<"${output}" \
    || fail "systemd supervisor status is missing"
grep -qx 'agent=running pid=745 addr=0.0.0.0:8080' <<<"${output}" \
    || fail "agent status is missing"

if AIDEN_SYSTEMCTL_INACTIVE=1 PATH="${TEST_ROOT}:${PATH}" "${HELPER}" status >/dev/null 2>&1; then
    fail "inactive systemd service unexpectedly reported success"
fi

: >"${TEST_ROOT}/systemctl.log"
AIDEN_SYSTEMCTL_LOG="${TEST_ROOT}/systemctl.log" \
    PATH="${TEST_ROOT}:${PATH}" "${HELPER}" restart
cat >"${TEST_ROOT}/expected-restart.log" <<'EOF'
restart aiden-environment.service
restart aiden-agent.service
start aiden.target
EOF
cmp "${TEST_ROOT}/expected-restart.log" "${TEST_ROOT}/systemctl.log" \
    || fail "agent restart did not restore the Aiden service target"

echo "Debian agent control tests passed"
