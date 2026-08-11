#!/bin/sh
set -eu

repo_root="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
config="$repo_root/overlay/etc/aiden_frame_service.conf"
init_script="$repo_root/overlay/etc/init.d/S52frame_service"

grep -q '^FRAME_SERVICE_FORCE_TRIGGER=auto$' "$config"
grep -q '^resolve_force_trigger() {' "$init_script"
grep -q '\*tc358743\*) echo 1' "$init_script"
grep -q '\*rk628-csi\*) echo 0' "$init_script"
grep -q 'force_trigger="$(resolve_force_trigger ' "$init_script"

echo "frame_service HDMI bridge policy: ok"
