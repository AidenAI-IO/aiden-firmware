#!/bin/bash
set -eu

agent_dir="${AIDEN_AGENT_DIR:-/userdata/agent}"
system_env="${AIDEN_SYSTEM_ENV:-/userdata/system/env}"
agent_service="${AIDEN_AGENT_INIT_SCRIPT:-/usr/local/bin/aiden-agent-service}"
env_run_bin="${AIDEN_ENV_RUN_BIN:-/usr/local/bin/aiden-env-run}"
config_web_pid=""
wetty_pid=""

mkdir -p \
    "$agent_dir/log" \
    "$agent_dir/memory" \
    "$agent_dir/skill-state" \
    "$agent_dir/cache" \
    /userdata/audio \
    /userdata/ota \
    /userdata/system \
    /userdata/tmp \
    /run/agent
chmod 1777 /userdata/tmp

if [ ! -f "$agent_dir/agent.toml" ]; then
    cp /opt/aiden/defaults/agent.toml "$agent_dir/agent.toml"
fi
if [ ! -f "$system_env" ]; then
    : > "$system_env"
fi
if [ ! -f /userdata/wpa_supplicant.conf ]; then
    : > /userdata/wpa_supplicant.conf
fi

shutdown() {
    trap - INT TERM EXIT
    if [ -n "$wetty_pid" ]; then
        kill "$wetty_pid" 2>/dev/null || true
        wait "$wetty_pid" 2>/dev/null || true
    fi
    if [ -n "$config_web_pid" ]; then
        kill "$config_web_pid" 2>/dev/null || true
        wait "$config_web_pid" 2>/dev/null || true
    fi
    "$agent_service" stop || true
}

trap shutdown INT TERM EXIT

"$agent_service" start

wetty \
    --host=0.0.0.0 \
    --port=3000 \
    --base=/wetty/ \
    --title="Aiden Shell" \
    --command=/bin/bash \
    >>/userdata/agent/log/wetty.log 2>&1 &
wetty_pid="$!"

"$env_run_bin" "${AIDEN_AGENT_BIN:-/oem/usr/bin/agent}" config-web \
    --bind=0.0.0.0 \
    --port=80 \
    --config="$agent_dir/agent.toml" \
    --wifi-config=/userdata/wpa_supplicant.conf \
    --ota-state=/userdata/ota/state.json \
    --cmdline=/proc/cmdline \
    --system-env="$system_env" \
    --storage-state=/run/agent/storage_state &
config_web_pid="$!"

set +e
# Keep both web frontends tied to the container lifecycle so Compose can restart them together.
wait -n "$config_web_pid" "$wetty_pid"
status="$?"
set -e
exit "$status"
