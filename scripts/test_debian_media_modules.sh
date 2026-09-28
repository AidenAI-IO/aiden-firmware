#!/usr/bin/env bash
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture"/{bin,ko,sys/module,proc,dev}
export FIXTURE="$fixture"
# No bridge/sensor is present. The vendor cleanup knobs must remain untouched.
for module in video_rkcif video_rkisp; do
    mkdir -p "$fixture/sys/module/$module/parameters"
    printf 'not-ready\n' >"$fixture/sys/module/$module/parameters/clr_unready_dev"
done
for module in rk_dvbm video_rkcif video_rkisp phy-rockchip-csi2-dphy-hw \
    phy-rockchip-csi2-dphy rga3 mpp_vcodec rknpu snd-soc-rv1106 rockit; do
    touch "$fixture/ko/$module.ko"
done
cat >"$fixture/bin/insmod" <<'EOF'
#!/bin/sh
set -eu
name=$(basename "$1" .ko | tr '-' '_')
mkdir -p "$FIXTURE/sys/module/$name"
printf '%s\n' "$*" >>"$FIXTURE/loaded"
EOF
printf '#!/bin/sh\nexit 0\n' >"$fixture/bin/lsmod"
printf '#!/bin/sh\nexit 0\n' >"$fixture/bin/udevadm"
chmod +x "$fixture/bin/"*
# Redirect only hardware paths; execute the actual loader logic and order.
sed -e "s|/oem/usr/ko|$fixture/ko|g" \
    -e "s|/sys/|$fixture/sys/|g" \
    -e "s|/proc/|$fixture/proc/|g" \
    -e "s|/dev/|$fixture/dev/|g" \
    "$repo/overlay-debian/usr/lib/aiden/aiden-media-modules" >"$fixture/loader"
PATH="$fixture/bin:$PATH" sh "$fixture/loader"
for module in video_rkcif video_rkisp; do
    grep -qx 'not-ready' "$fixture/sys/module/$module/parameters/clr_unready_dev"
done
for module in rga3 mpp_vcodec rknpu snd_soc_rv1106 rockit; do
    test -d "$fixture/sys/module/$module"
done
grep -q 'isp_max_h=0' "$fixture/loaded"
before=$(wc -l <"$fixture/loaded")
PATH="$fixture/bin:$PATH" sh "$fixture/loader"
test "$(wc -l <"$fixture/loaded")" -eq "$before"
echo 'PASS: missing HDMI bridge leaves cleanup untouched and loads independent media modules; rerun is idempotent'
