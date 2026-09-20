#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly RETENTION_HELPER=${REPO_ROOT}/overlay-debian/usr/lib/aiden/aiden-agent-log-retention
readonly UNIT_DIR=${REPO_ROOT}/overlay-debian/etc/systemd/system
readonly TEST_ROOT=$(mktemp -d)
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() {
    echo "Agent log retention test failure: $*" >&2
    exit 1
}

[ -x "${RETENTION_HELPER}" ] || fail "retention helper is missing or not executable"
grep -Fqx 'StandardOutput=append:/userdata/agent/log/agent.log' \
    "${UNIT_DIR}/aiden-agent.service" \
    || fail "agent service no longer writes the expected persistent log"
grep -Fqx 'Environment=AIDEN_AGENT_LOG_PATH=/userdata/agent/log/agent.log' \
    "${UNIT_DIR}/aiden-agent-log-retention.service" \
    || fail "retention service does not target the agent service log"
grep -Fqx 'ExecStart=/usr/lib/aiden/aiden-agent-log-retention' \
    "${UNIT_DIR}/aiden-agent-log-retention.service" \
    || fail "retention service does not execute the retention helper"
grep -Fqx 'RuntimeDirectory=agent-log-retention' \
    "${UNIT_DIR}/aiden-agent-log-retention.service" \
    || fail "retention service does not isolate its temporary files"
grep -Fqx 'OnUnitActiveSec=1min' \
    "${UNIT_DIR}/aiden-agent-log-retention.timer" \
    || fail "retention timer does not run once per minute"
grep -Fqx 'Wants=aiden-agent-log-retention.timer' \
    "${UNIT_DIR}/aiden.target" \
    || fail "aiden.target does not start the retention timer"

log_path=${TEST_ROOT}/agent.log
level_path=${TEST_ROOT}/storage_level
config_path=${TEST_ROOT}/agent.toml

run_retention() {
    AIDEN_AGENT_LOG_PATH="${log_path}" \
    AIDEN_AGENT_STORAGE_LEVEL_PATH="${level_path}" \
    AIDEN_AGENT_CONFIG_PATH="${config_path}" \
    AIDEN_AGENT_LOG_MAX_BYTES=10 \
    AIDEN_AGENT_LOG_RETAIN_BYTES=6 \
        "${RETENTION_HELPER}"
}

printf '0123456789abcdef' >"${log_path}"
run_retention
[ "$(wc -c <"${log_path}" | tr -d '[:space:]')" = 6 ] \
    || fail "normal retention did not reduce the log to the retained size"
[ "$(cat "${log_path}")" = 'abcdef' ] \
    || fail "normal retention did not preserve the newest log bytes"

printf '123456' >"${log_path}"
run_retention
[ "$(cat "${log_path}")" = '123456' ] \
    || fail "a log at the retained size was modified"

cat >"${config_path}" <<'EOF'
[storage_settings.storage.degraded_mode]
max_agent_log_mb = 1
EOF
printf 'critical\n' >"${level_path}"
dd if=/dev/zero of="${log_path}" bs=1048576 count=1 2>/dev/null
printf 'critical-tail' >>"${log_path}"
AIDEN_AGENT_LOG_PATH="${log_path}" \
AIDEN_AGENT_STORAGE_LEVEL_PATH="${level_path}" \
AIDEN_AGENT_CONFIG_PATH="${config_path}" \
AIDEN_AGENT_LOG_MAX_BYTES=2097152 \
AIDEN_AGENT_LOG_RETAIN_BYTES=1048576 \
    "${RETENTION_HELPER}"
[ "$(wc -c <"${log_path}" | tr -d '[:space:]')" = 1048576 ] \
    || fail "critical retention did not apply max_agent_log_mb"
[ "$(tail -c 13 "${log_path}")" = 'critical-tail' ] \
    || fail "critical retention did not preserve the newest log bytes"

target=${TEST_ROOT}/target.log
printf 'do-not-touch' >"${target}"
ln -s "${target}" "${log_path}.symlink"
AIDEN_AGENT_LOG_PATH="${log_path}.symlink" \
AIDEN_AGENT_LOG_MAX_BYTES=1 \
AIDEN_AGENT_LOG_RETAIN_BYTES=1 \
    "${RETENTION_HELPER}" 2>/dev/null \
    && fail "retention helper accepted a symlink log path"
[ "$(cat "${target}")" = 'do-not-touch' ] \
    || fail "retention helper modified a symlink target"

echo "Agent log retention tests passed"
