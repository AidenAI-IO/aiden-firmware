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
case "${1:-}" in
    is-active)
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

cat >"${TEST_ROOT}/systemctl" <<'EOF'
#!/bin/sh
case "${1:-}" in
    is-active) exit 3 ;;
    *) exit 2 ;;
esac
EOF
chmod 0755 "${TEST_ROOT}/systemctl"
if PATH="${TEST_ROOT}:${PATH}" "${HELPER}" status >/dev/null 2>&1; then
    fail "inactive systemd service unexpectedly reported success"
fi

echo "Debian agent control tests passed"
