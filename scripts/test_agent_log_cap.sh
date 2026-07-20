#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

log_path="$tmp_dir/agent.log"
printf '0123456789abcdef' > "$log_path"

LOG_PATH="$log_path" AGENT_LOG_MAX_BYTES=10 \
    sh "$repo_root/overlay/etc/init.d/S53agent" trim-log

size=$(wc -c < "$log_path" | tr -d ' ')
[ "$size" = 10 ]
[ "$(cat "$log_path")" = "6789abcdef" ]

printf 'agent log cap test passed\n'
