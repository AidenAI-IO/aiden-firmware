#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SDK_DIR="${PICO_SDK_DIR:-$ROOT_DIR/pico-sdk}"
KERNEL_DIR="$SDK_DIR/sysdrv/source/kernel"
DRIVER="$KERNEL_DIR/drivers/media/i2c/rk628/rk628_csi_v4l2.c"
DEFCONFIG="$KERNEL_DIR/arch/arm/configs/luckfox_rv1106_linux_defconfig"
DTS="$KERNEL_DIR/arch/arm/boot/dts/rv1106-luckfox-pico-zero-ipc.dtsi"

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

require_pattern '^CONFIG_VIDEO_RK628_CSI=y$' "$DEFCONFIG" \
    "Luckfox RV1106 defconfig must enable the RK628 CSI driver"
require_pattern '^CONFIG_MEDIA_CONTROLLER=y$' "$DEFCONFIG" \
    "RK628 CSI requires the media controller API"
require_pattern '^CONFIG_VIDEO_V4L2_SUBDEV_API=y$' "$DEFCONFIG" \
    "RK628 CSI requires the V4L2 subdevice API"
reject_pattern '^CONFIG_VIDEO_TC358743(_CEC)?=y$' "$DEFCONFIG" \
    "Luckfox RV1106 defconfig must not enable the replaced TC358743 driver"

require_pattern 'compatible = "rockchip,rk628-csi-v4l2";' "$DTS" \
    "Pico Zero DTS must bind the RK628 CSI driver"
require_pattern 'rk628-csi@50' "$DTS" \
    "Pico Zero DTS must use the strapped RK628 address 0x50"
require_pattern 'clock-frequency = <100000>;' "$DTS" \
    "RK628 I2C must run at 100 kHz for reliable 32-bit register transfers"
require_pattern 'reset-gpios = <&gpio3 RK_PB1 GPIO_ACTIVE_LOW>;' "$DTS" \
    "RK628 reset must use GPIO3_B1"
require_pattern 'rk628_reset_pin: rk628-reset-pin' "$DTS" \
    "RK628 reset must have an explicit pinctrl group"
require_pattern '<3 RK_PB1 RK_FUNC_GPIO &pcfg_pull_up>' "$DTS" \
    "RK628 reset pinctrl must switch GPIO3_B1 out of MIPI lane mode"
require_pattern 'rk628_mipi_pins: rk628-mipi-pins' "$DTS" \
    "RK628 capture must use a dedicated two-lane MIPI pinctrl group"
require_pattern 'pinctrl-0 = <&rk628_mipi_pins>;' "$DTS" \
    "CIF MIPI pinctrl must not reserve the unused RK628 lanes"
reject_pattern 'pinctrl-0 = <&mipi_pins>;' "$DTS" \
    "Four-lane MIPI pinctrl would conflict with the RK628 reset pin"
require_pattern 'clocks = <&cru MCLK_REF_MIPI0>;' "$DTS" \
    "RK628 must receive the RV1106 24 MHz MIPI reference clock"
require_pattern 'clock-names = "soc_24M";' "$DTS" \
    "RK628 reference clock must use the driver clock name"
require_pattern 'pinctrl-0 = <&rk628_reset_pin &mipi_refclk_out0>;' "$DTS" \
    "RK628 pinctrl must configure both reset and the reference clock"
require_pattern 'remote-endpoint = <&rk628_out>;' "$DTS" \
    "CSI D-PHY input must link to RK628"
require_pattern 'remote-endpoint = <&csi_dphy_input2>;' "$DTS" \
    "RK628 output must link to CSI D-PHY input 2"
dphy_endpoint="$(sed -n '/csi_dphy_input2: endpoint@2 {/,/^[[:space:]]*};/p' "$DTS")"
rk628_endpoint="$(sed -n '/rk628_out: endpoint {/,/^[[:space:]]*};/p' "$DTS")"
if ! grep -q 'data-lanes = <1 2>;' <<< "$dphy_endpoint"; then
    echo "FAIL: CSI D-PHY input 2 must use two data lanes" >&2
    exit 1
fi
if ! grep -q 'data-lanes = <1 2>;' <<< "$rk628_endpoint"; then
    echo "FAIL: RK628 output must use two data lanes" >&2
    exit 1
fi
require_pattern 'clock-lanes = <0>;' "$DTS" \
    "RK628 CSI endpoint must declare its clock lane"
require_pattern 'clock-noncontinuous;' "$DTS" \
    "RK628 CSI endpoint must match the transmitter non-continuous clock mode"
reject_pattern 'tc358743' "$DTS" \
    "Pico Zero DTS must not retain the replaced TC358743 node"

require_pattern 'case RKMODULE_GET_HDMI_MODE:' "$DRIVER" \
    "RK628 driver must identify itself as an HDMI input"
require_pattern 'RKMODULE_HDMIIN_MODE' "$DRIVER" \
    "RK628 driver must report Rockchip HDMI input mode"
require_pattern '\.query_dv_timings[[:space:]]*=' "$DRIVER" \
    "RK628 driver must support HDMI timing discovery"
require_pattern '\.set_edid[[:space:]]*=' "$DRIVER" \
    "RK628 driver must accept the existing EDID setup path"
require_pattern '\.get_mbus_config[[:space:]]*=' "$DRIVER" \
    "RK628 driver must report CSI lane and clock configuration"
require_pattern '#define RK628_CSI_LINK_FREQ_LOW[[:space:]]+375000000' "$DRIVER" \
    "750 Mbps/lane must be advertised as a 375 MHz link frequency"
require_pattern '#define RK628_CSI_LINK_FREQ_HIGH[[:space:]]+625000000' "$DRIVER" \
    "1250 Mbps/lane must be advertised as a 625 MHz link frequency"
require_pattern 'V4L2_MBUS_CSI2_NONCONTINUOUS_CLOCK' "$DRIVER" \
    "RK628 mbus configuration must report non-continuous clock mode"
require_pattern 'link_freq->flags \|= V4L2_CTRL_FLAG_READ_ONLY' "$DRIVER" \
    "RK628 link frequency must be derived read-only state"

for call_site in rk628_csi_s_dv_timings rk628_csi_set_fmt mipi_dphy_power_on rk628_csi_probe; do
    call_site_body="$(sed -n "/^static .*${call_site}(/,/^}/p" "$DRIVER")"
    if ! grep -q 'rk628_csi_update_link_freq(csi);' <<< "$call_site_body"; then
        echo "FAIL: $call_site must synchronize the advertised and hardware link rates" >&2
        exit 1
    fi
done

echo "PASS: RK628D driver integration contract"
