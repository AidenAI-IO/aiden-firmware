#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SDK_DIR="${PICO_SDK_DIR:-$ROOT_DIR/pico-sdk}"
KERNEL_DIR="$SDK_DIR/sysdrv/source/kernel"
DRIVER="$KERNEL_DIR/drivers/media/i2c/rk628/rk628_csi_v4l2.c"
BT1120="$KERNEL_DIR/drivers/media/i2c/rk628/rk628_bt1120_v4l2.c"
HDMIRX="$KERNEL_DIR/drivers/media/i2c/rk628/rk628_hdmirx.c"
HDMIRX_HEADER="$KERNEL_DIR/drivers/media/i2c/rk628/rk628_hdmirx.h"
KERNEL_FRAGMENT="$KERNEL_DIR/arch/arm/configs/aiden-rk628.config"
DTS="$KERNEL_DIR/arch/arm/boot/dts/rv1106-luckfox-pico-zero-ipc.dtsi"
BOARD_CONFIG="$SDK_DIR/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Buildroot-RV1106_Luckfox_Pico_Zero-IPC.mk"

require_pattern() {
    local pattern="$1"
    local file="$2"
    local message="$3"

    if ! grep -Eq "$pattern" "$file"; then
        echo "FAIL: $message" >&2
        exit 1
    fi
}

reject_pattern() {
    local pattern="$1"
    local file="$2"
    local message="$3"

    if grep -Eq "$pattern" "$file"; then
        echo "FAIL: $message" >&2
        exit 1
    fi
}

require_pattern '^CONFIG_MEDIA_CONTROLLER=y$' "$KERNEL_FRAGMENT" \
    "RK628 CSI requires the media controller API"
require_pattern '^CONFIG_VIDEO_V4L2_SUBDEV_API=y$' "$KERNEL_FRAGMENT" \
    "RK628 CSI requires the V4L2 subdevice API"
require_pattern '^CONFIG_VIDEO_RK628_CSI=y$' "$KERNEL_FRAGMENT" \
    "the kernel fragment must enable the RK628 CSI driver"
require_pattern '^CONFIG_VIDEO_TC358743=y$' "$KERNEL_FRAGMENT" \
    "the dual-bridge image must retain the TC358743 driver"
require_pattern '^# CONFIG_VIDEO_TC358743_CEC is not set$' "$KERNEL_FRAGMENT" \
    "the unused TC358743 CEC path must stay disabled"
require_pattern 'RK_KERNEL_DEFCONFIG_FRAGMENT=.*aiden-rk628\.config' "$BOARD_CONFIG" \
    "the board build must apply the HDMI bridge kernel fragment"

require_pattern 'compatible = "rockchip,rk628-csi-v4l2";' "$DTS" \
    "Pico Zero DTS must bind the RK628 CSI driver"
require_pattern 'rk628-csi@50' "$DTS" \
    "Pico Zero DTS must use the strapped RK628 address 0x50"
require_pattern 'compatible = "toshiba,tc358743";' "$DTS" \
    "the dual-bridge DTS must retain the TC358743 node"
require_pattern 'tc358743@f' "$DTS" \
    "the dual-bridge DTS must retain TC358743 address 0x0f"
require_pattern 'clock-frequency = <100000>;' "$DTS" \
    "shared HDMI bridge I2C must run at the validated 100 kHz rate"
require_pattern 'reset-gpios = <&gpio3 RK_PC5 GPIO_ACTIVE_LOW>;' "$DTS" \
    "RK628 reset must use Pico Zero CSI connector pin 17"
require_pattern 'rk628_reset_pin: rk628-reset-pin' "$DTS" \
    "RK628 reset must have a dedicated pinctrl group"
require_pattern '<3 RK_PC5 RK_FUNC_GPIO &pcfg_pull_none>' "$DTS" \
    "RK628 reset must be push-pull without an internal pull-up"
reject_pattern 'reset-gpios = <&gpio1 RK_PC2' "$DTS" \
    "RK628 reset must not use the unrelated GPIO1_C2 pin"

rk628_node="$(sed -n '/rk628_csi: rk628-csi@50 {/,/^[[:space:]]*};/p' "$DTS")"
tc358743_node="$(sed -n '/tc358743_csi: tc358743@f {/,/^[[:space:]]*};/p' "$DTS")"
rk628_input="$(sed -n '/rk628_csi_in: endpoint@0 {/,/^[[:space:]]*};/p' "$DTS")"
tc358743_input="$(sed -n '/tc358743_csi_in: endpoint@1 {/,/^[[:space:]]*};/p' "$DTS")"

if ! grep -q 'continues-clk;' <<< "$rk628_node" || \
        ! grep -q 'data-lanes = <1 2 3 4>;' <<< "$rk628_node" || \
        ! grep -q 'data-lanes = <1 2 3 4>;' <<< "$rk628_input"; then
    echo "FAIL: RK628 must retain its validated four-lane continuous-clock CSI contract" >&2
    exit 1
fi
if ! grep -q 'clock-noncontinuous;' <<< "$tc358743_node" || \
        ! grep -q 'data-lanes = <1 2>;' <<< "$tc358743_node" || \
        ! grep -q 'data-lanes = <1 2>;' <<< "$tc358743_input"; then
    echo "FAIL: TC358743 must retain its two-lane non-continuous-clock CSI contract" >&2
    exit 1
fi
if grep -qE 'clocks = <&cru MCLK_REF_MIPI0>|clock-names = "soc_24M"|GPIO_OPEN_DRAIN' <<< "$rk628_node"; then
    echo "FAIL: Firefly RK628D must use its onboard clock and push-pull reset" >&2
    exit 1
fi

require_pattern 'remote-endpoint = <&rk628_csi_out>;' "$DTS" \
    "CSI D-PHY must expose the RK628 endpoint"
require_pattern 'remote-endpoint = <&tc358743_csi_out>;' "$DTS" \
    "CSI D-PHY must expose the TC358743 endpoint"
require_pattern 'remote-endpoint = <&rk628_csi_in>;' "$DTS" \
    "RK628 output must link back to the CSI D-PHY"
require_pattern 'remote-endpoint = <&tc358743_csi_in>;' "$DTS" \
    "TC358743 output must link back to the CSI D-PHY"

require_pattern 'case RKMODULE_GET_HDMI_MODE:' "$DRIVER" \
    "RK628 driver must identify itself as an HDMI input"
require_pattern 'RKMODULE_HDMIIN_MODE' "$DRIVER" \
    "RK628 driver must report Rockchip HDMI input mode"
require_pattern '\.query_dv_timings[[:space:]]*=' "$DRIVER" \
    "RK628 driver must support HDMI timing discovery"
require_pattern '\.set_edid[[:space:]]*=' "$DRIVER" \
    "RK628 driver must accept the existing EDID setup path"
require_pattern 'def_edid\.blocks = ARRAY_SIZE\(edid_init_data\) / EDID_BLOCK_SIZE;' "$DRIVER" \
    "RK628 default EDID block count must be derived from its data"
require_pattern 'msleep\(200\);' "$DRIVER" \
    "RK628 EDID updates must hold HPD low long enough for HDMI sources"
require_pattern '\.get_mbus_config[[:space:]]*=' "$DRIVER" \
    "RK628 driver must report CSI lane and clock configuration"
require_pattern 'of_property_read_bool\(dev->of_node,' "$DRIVER" \
    "RK628 clock mode must remain device-tree controlled"
require_pattern '"continues-clk"' "$DRIVER" \
    "RK628 driver must consume the continuous-clock property"
require_pattern 'V4L2_MBUS_CSI2_CONTINUOUS_CLOCK' "$DRIVER" \
    "RK628 mbus configuration must report continuous clock when selected"
require_pattern 'V4L2_MBUS_CSI2_NONCONTINUOUS_CLOCK' "$DRIVER" \
    "RK628 mbus configuration must retain non-continuous-clock support"
require_pattern '#define RK628_CSI_LINK_FREQ_LOW[[:space:]]+375000000' "$DRIVER" \
    "750 Mbps/lane must be advertised as a 375 MHz link frequency"
require_pattern '#define RK628_CSI_LINK_FREQ_HIGH[[:space:]]+625000000' "$DRIVER" \
    "1250 Mbps/lane must be advertised as a 625 MHz link frequency"
require_pattern 'link_freq->flags \|= V4L2_CTRL_FLAG_READ_ONLY' "$DRIVER" \
    "RK628 link frequency must be derived read-only state"
require_pattern 'v4l2_ctrl_s_ctrl\(csi->link_freq, index\);' "$DRIVER" \
    "RK628 link frequency updates must take the V4L2 control lock"
require_pattern 'v4l2_ctrl_s_ctrl_int64\(csi->pixel_rate, pixel_rate\);' "$DRIVER" \
    "RK628 pixel-rate updates must take the V4L2 control lock"

require_pattern '#define SIGNAL_RECOVERY_INTERVAL_MS[[:space:]]+10000' "$DRIVER" \
    "RK628 direct mode must throttle automatic HDMI PHY recovery"
require_pattern 'rk628_csi_schedule_recovery\(sd\);' "$DRIVER" \
    "RK628 polling must recover when HDMI starts after probe"
require_pattern 'time_before\(jiffies, csi->next_recovery\)' "$DRIVER" \
    "RK628 recovery must use a per-device retry deadline"
require_pattern 'rk628_csi_arm_recovery_cooldown\(csi\);' "$DRIVER" \
    "RK628 recovery cooldown must begin after PHY training"
require_pattern '#define HDMI_RX_SCDC_LOCK_MASK[[:space:]]+GENMASK\(11, 8\)' "$DRIVER" \
    "RK628 PHY lock detection must name the four HDMI lock flags"
require_pattern 'status & HDMI_RX_SCDC_LOCK_MASK' "$DRIVER" \
    "RK628 PHY lock detection must ignore unrelated SCDC flags"
reject_pattern 'status & 0xfff' "$DRIVER" \
    "RK628 PHY lock detection must not reject valid lock on status bit 0"
require_pattern 'rk628_is_avi_ready\(csi->rk628, &csi->avi_rcv_rdy\)' "$DRIVER" \
    "RK628 CSI setup must observe live AVI readiness changes"
require_pattern 'rk628_is_avi_ready\(bt1120->rk628, &bt1120->avi_rcv_rdy\)' "$BT1120" \
    "the shared AVI API change must cover the BT1120 caller"
require_pattern 'const bool \*avi_rcv_rdy' "$HDMIRX_HEADER" \
    "RK628 AVI readiness API must accept live state"
require_pattern 'READ_ONCE\(\*avi_rcv_rdy\)' "$HDMIRX" \
    "RK628 AVI polling must reload state updated by the interrupt path"

require_pattern 'i2c_set_clientdata\(client, sd\);' "$DRIVER" \
    "RK628 remove must retain its V4L2 subdevice"
remove_body="$(sed -n '/^static int rk628_csi_remove(/,/^}/p' "$DRIVER")"
for cleanup in \
        'v4l2_async_unregister_subdev(sd);' \
        'cancel_work_sync(&csi->work_i2c_poll);' \
        'rk628_hdmirx_audio_destroy(csi->audio_info);' \
        'media_entity_cleanup(&sd->entity);' \
        'v4l2_ctrl_handler_free(&csi->hdl);'; do
    if ! grep -Fq "$cleanup" <<< "$remove_body"; then
        echo "FAIL: RK628 remove is missing cleanup: $cleanup" >&2
        exit 1
    fi
done

poll_body="$(sed -n '/^static void rk628_csi_work_i2c_poll(/,/^}/p' "$DRIVER")"
poll_isr_line="$(grep -n 'rk628_csi_isr(sd, 0, &handled);' <<< "$poll_body" | cut -d: -f1 || true)"
poll_mutex_line="$(grep -n 'mutex_lock(&csi->confctl_mutex);' <<< "$poll_body" | cut -d: -f1 || true)"
if [[ -z "$poll_isr_line" || -z "$poll_mutex_line" || "$poll_isr_line" -ge "$poll_mutex_line" ]]; then
    echo "FAIL: no-IRQ polling must service HDMI interrupts before taking the config mutex" >&2
    exit 1
fi

for call_site in rk628_csi_s_dv_timings rk628_csi_set_fmt mipi_dphy_power_on rk628_csi_probe; do
    call_site_body="$(sed -n "/^static .*${call_site}(/,/^}/p" "$DRIVER")"
    if ! grep -q 'rk628_csi_update_mode_controls(csi);' <<< "$call_site_body"; then
        echo "FAIL: $call_site must synchronize mode, link frequency and pixel rate" >&2
        exit 1
    fi
done

python3 - "$DRIVER" "$ROOT_DIR/edid/hdmi_1080p60_cta.hex" "$ROOT_DIR/src/aiden_sdk.cpp" <<'PY'
import pathlib
import re
import sys

driver = pathlib.Path(sys.argv[1]).read_text()
fixture = bytes.fromhex(pathlib.Path(sys.argv[2]).read_text())
sdk = pathlib.Path(sys.argv[3]).read_text()

if len(fixture) != 256:
    raise SystemExit("FAIL: HDMI 1080p60 EDID must contain two blocks")
if any(sum(fixture[offset:offset + 128]) % 256 for offset in range(0, len(fixture), 128)):
    raise SystemExit("FAIL: every HDMI 1080p60 EDID block must have a valid checksum")
if fixture[20] & 0x80 == 0 or fixture[126] != 1 or fixture[128] != 0x02:
    raise SystemExit("FAIL: RK628 EDID must describe a digital HDMI sink with one CTA extension")

cta = fixture[128:]
dtd_offset = cta[2]
video_vics = []
has_hdmi_vsdb = False
offset = 4
while offset < dtd_offset:
    header = cta[offset]
    tag = header >> 5
    length = header & 0x1f
    payload = cta[offset + 1:offset + 1 + length]
    if len(payload) != length:
        raise SystemExit("FAIL: HDMI CTA extension contains a truncated data block")
    if tag == 2:
        video_vics.extend(value & 0x7f for value in payload)
    elif tag == 3 and payload[:3] == bytes((0x03, 0x0c, 0x00)):
        has_hdmi_vsdb = True
    offset += 1 + length

if video_vics != [16] or not has_hdmi_vsdb:
    raise SystemExit("FAIL: RK628 default EDID must advertise HDMI CTA VIC 16 (1080p60) only")


def extract_edid(source, declaration, missing_message):
    match = re.search(declaration + r"\s*=\s*\{(?P<body>.*?)\};", source, re.DOTALL)
    if not match:
        raise SystemExit(missing_message)
    return bytes(int(value, 16) for value in re.findall(r"0x([0-9A-Fa-f]{2})", match.group("body")))


driver_edid = extract_edid(driver, r"static u8 edid_init_data\[\]", "FAIL: RK628 default EDID is missing")
sdk_edid = extract_edid(sdk, r"static const uint8_t kDefaultHdmiEdid1080p60\[\]", "FAIL: libaiden default EDID is missing")
if driver_edid != fixture or sdk_edid != fixture:
    raise SystemExit("FAIL: RK628 and libaiden must use the same 1080p60 EDID")
PY

echo "PASS: dual-bridge RK628D driver integration contract"
