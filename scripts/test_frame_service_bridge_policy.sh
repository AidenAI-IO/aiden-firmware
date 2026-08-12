#!/bin/sh
set -eu

repo_root="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
config="$repo_root/overlay/etc/aiden_frame_service.conf"
init_script="$repo_root/overlay/etc/init.d/S52frame_service"
tc_edid="$repo_root/overlay/oem/usr/share/aiden/edid/hdmi_1080p30_cta.hex"

grep -q '^FRAME_SERVICE_FORCE_TRIGGER=auto$' "$config"
grep -q '^FRAME_SERVICE_EDID=auto$' "$config"
grep -q '^FRAME_SERVICE_TC358743_EDID=/oem/usr/share/aiden/edid/hdmi_1080p30_cta.hex$' "$config"
grep -q '^FRAME_SERVICE_TC358743_HPD_LOW_SECONDS=2$' "$config"
grep -q '^FRAME_SERVICE_TC358743_SETTLE_SECONDS=5$' "$config"
grep -q '^resolve_force_trigger() {' "$init_script"
grep -q '^resolve_edid() {' "$init_script"
grep -q '^prepare_tc358743_edid() {' "$init_script"
grep -q '\*tc358743\*) echo 1' "$init_script"
grep -q '\*rk628-csi\*) echo 0' "$init_script"
grep -q '\*tc358743\*) echo "$FRAME_SERVICE_TC358743_EDID"' "$init_script"
grep -q 'force_trigger="$(resolve_force_trigger ' "$init_script"
grep -q 'edid="$(resolve_edid ' "$init_script"
grep -q -- '--clear-edid=pad=0' "$init_script"
grep -q -- '--set-edid="pad=0,file=$edid" --fix-edid-checksums' "$init_script"
grep -q '\[ "$bridge_prepared" != "1" \] && \[ "$force_trigger" = "1" \]' "$init_script"
grep -q 'prepare_tc358743_edid "$subdev" "$edid"' "$init_script"
cmp "$repo_root/edid/hdmi_1080p30_cta.hex" "$tc_edid"

echo "frame_service HDMI bridge policy: ok"
