#!/bin/sh
set -eu

source_config_dir="${AIDEN_SOURCE_CONFIG_DIR:-/config}"
runtime_config_dir="${AIDEN_RUNTIME_CONFIG_DIR:-/tmp/aiden-config}"
config_file="$runtime_config_dir/agent.toml"

rm -rf "$runtime_config_dir"
mkdir -p "$runtime_config_dir"
cp -a "$source_config_dir"/. "$runtime_config_dir"/
mkdir -p "$runtime_config_dir/log" "$runtime_config_dir/memory" "$runtime_config_dir/skill-state"

if [ ! -f "$config_file" ]; then
    echo "missing agent.toml in $source_config_dir" >&2
    exit 1
fi

if grep -q '^[[:space:]]*bridge_token_file[[:space:]]*=' "$config_file"; then
    sed -i 's#^[[:space:]]*bridge_token_file[[:space:]]*=.*#bridge_token_file = "/config/bridge_token"#' "$config_file"
fi

if grep -q '^[[:space:]]*control_token_file[[:space:]]*=' "$config_file"; then
    sed -i 's#^[[:space:]]*control_token_file[[:space:]]*=.*#control_token_file = "/config/control_token"#' "$config_file"
fi

default_forward_tools="screenshot,touch_gesture,keyboard_text,keyboard_tap,mouse_click,mouse_move,mouse_scroll,quick_action"
set -- daemon -config "$runtime_config_dir" -addr "${AIDEN_DAEMON_ADDR:-0.0.0.0:8080}"

if [ "${AIDEN_TOOL_PROXY_MODE:-}" = "1" ] || [ "${AIDEN_TOOL_PROXY_MODE:-}" = "true" ]; then
    if [ -z "${TOOL_PROXY_ENDPOINT:-}" ]; then
        echo "TOOL_PROXY_ENDPOINT is required when AIDEN_TOOL_PROXY_MODE is enabled" >&2
        exit 1
    fi
    set -- "$@" --tool-proxy-mode --tool-proxy-endpoint "$TOOL_PROXY_ENDPOINT" --forward-tools "${AIDEN_FORWARD_TOOLS:-$default_forward_tools}"
fi

exec "$@"
