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
require_pattern 'reset-gpios = <&gpio3 RK_PC5 GPIO_ACTIVE_LOW>;' "$DTS" \
    "RK628 reset must use Pico Zero CSI connector pin 17 as an active-low 1.8V push-pull output"
require_pattern 'rk628_reset_pin: rk628-reset-pin' "$DTS" \
    "RK628 reset must have an explicit pinctrl group"
require_pattern '<3 RK_PC5 RK_FUNC_GPIO &pcfg_pull_none>' "$DTS" \
    "RK628 reset pinctrl must use the 1.8V CSI I/O domain without an internal pull-up"
reject_pattern 'reset-gpios = <&gpio1 RK_PC2' "$DTS" \
    "RK628 reset must not drive the unrelated GPIO1_C2 pin"
reject_pattern 'GPIO_OPEN_DRAIN' "$DTS" \
    "RK628 reset must drive high because the module reset input has no usable pull-up"
require_pattern 'pinctrl-0 = <&mipi_pins>;' "$DTS" \
    "RK628 capture must configure the Pico Zero four-lane MIPI connector"
reject_pattern 'rk628_mipi_pins' "$DTS" \
    "RK628 capture must not retain the obsolete two-lane pinctrl group"
reject_pattern 'clocks = <&cru MCLK_REF_MIPI0>;' "$DTS" \
    "Firefly RK628D module must use its onboard reference clock"
reject_pattern 'clock-names = "soc_24M";' "$DTS" \
    "Firefly RK628D module must not request an external reference clock"
reject_pattern 'assigned-clocks = <&cru CLK_REF_MIPI0>;' "$DTS" \
    "Firefly RK628D module must not configure an unused reference clock"
reject_pattern 'assigned-clock-rates = <24000000>;' "$DTS" \
    "Firefly RK628D module must not configure an unused clock rate"
require_pattern 'pinctrl-0 = <&rk628_reset_pin>;' "$DTS" \
    "Firefly RK628D pinctrl must configure only the reset signal"
reject_pattern 'mipi_refclk_out0' "$DTS" \
    "Firefly RK628D module has no external reference-clock pin"
require_pattern 'remote-endpoint = <&rk628_out>;' "$DTS" \
    "CSI D-PHY input must link to RK628"
require_pattern 'remote-endpoint = <&csi_dphy_input2>;' "$DTS" \
    "RK628 output must link to CSI D-PHY input 2"
reject_pattern 'remote-endpoint = <&mia1321_out>;' "$DTS" \
    "RK628 firmware must not retain the mutually exclusive MIA1321 CSI endpoint"
reject_pattern 'remote-endpoint = <&imx415_out>;' "$DTS" \
    "RK628 firmware must not retain the mutually exclusive IMX415 CSI endpoint"
mia1321_node="$(sed -n '/mia1321: mia1321@60 {/,/^[[:space:]]};/p' "$DTS")"
imx415_node="$(sed -n '/imx415: imx415@37 {/,/^[[:space:]]};/p' "$DTS")"
if ! grep -q 'status = "disabled";' <<< "$mia1321_node"; then
    echo "FAIL: RK628 firmware must disable the mutually exclusive MIA1321 node" >&2
    exit 1
fi
if ! grep -q 'status = "disabled";' <<< "$imx415_node"; then
    echo "FAIL: RK628 firmware must disable the mutually exclusive IMX415 node" >&2
    exit 1
fi
dphy_endpoint="$(sed -n '/csi_dphy_input2: endpoint@2 {/,/^[[:space:]]*};/p' "$DTS")"
rk628_endpoint="$(sed -n '/rk628_out: endpoint {/,/^[[:space:]]*};/p' "$DTS")"
if ! grep -q 'data-lanes = <1 2 3 4>;' <<< "$dphy_endpoint"; then
    echo "FAIL: CSI D-PHY input 2 must use four data lanes" >&2
    exit 1
fi
if ! grep -q 'data-lanes = <1 2 3 4>;' <<< "$rk628_endpoint"; then
    echo "FAIL: RK628 output must use four data lanes" >&2
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
require_pattern 'def_edid\.blocks = ARRAY_SIZE\(edid_init_data\) / EDID_BLOCK_SIZE;' "$DRIVER" \
    "RK628 probe must derive the default EDID block count from its data"
require_pattern 'msleep\(200\);' "$DRIVER" \
    "RK628 EDID updates must hold HPD low long enough for HDMI sources to detect"
reject_pattern 'udelay\(100\);' "$DRIVER" \
    "RK628 EDID updates must not use an undetectably short HPD pulse"
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

python3 - "$DRIVER" "$ROOT_DIR/edid/hdmi_1080p30_cta.hex" "$ROOT_DIR/src/aiden_sdk.cpp" <<'PY'
import pathlib
import re
import sys

driver = pathlib.Path(sys.argv[1]).read_text()
fixture = bytes.fromhex(pathlib.Path(sys.argv[2]).read_text())
sdk = pathlib.Path(sys.argv[3]).read_text()

if len(fixture) != 256:
    raise SystemExit("FAIL: validated HDMI 1080p30 EDID must contain two blocks")
if any(sum(fixture[offset:offset + 128]) % 256 for offset in range(0, len(fixture), 128)):
    raise SystemExit("FAIL: every HDMI 1080p30 EDID block must have a valid checksum")
if fixture[20] & 0x80 == 0:
    raise SystemExit("FAIL: HDMI EDID must declare a digital video input")
if fixture[126] != 1 or fixture[128] != 0x02:
    raise SystemExit("FAIL: HDMI EDID must declare one CTA-861 extension")

cta = fixture[128:]
dtd_offset = cta[2]
if dtd_offset < 4 or dtd_offset > 127:
    raise SystemExit("FAIL: HDMI CTA extension has an invalid data-block boundary")

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

if video_vics != [34]:
    raise SystemExit("FAIL: RK628D EDID must advertise only CTA VIC 34 (1080p30)")
if not has_hdmi_vsdb:
    raise SystemExit("FAIL: RK628D EDID must identify itself as an HDMI sink")


def extract_edid(source, declaration, missing_message):
    match = re.search(
        declaration + r"\s*=\s*\{(?P<body>.*?)\};",
        source,
        re.DOTALL,
    )
    if not match:
        raise SystemExit(missing_message)
    return bytes(
        int(value, 16)
        for value in re.findall(r"0x([0-9A-Fa-f]{2})", match.group("body"))
    )


driver_edid = extract_edid(
    driver,
    r"static u8 edid_init_data\[\]",
    "FAIL: RK628 default EDID array is missing",
)
sdk_edid = extract_edid(
    sdk,
    r"static const uint8_t kDefaultHdmiEdid1080p30\[\]",
    "FAIL: libaiden default EDID array is missing",
)
if driver_edid != fixture or sdk_edid != fixture:
    raise SystemExit("FAIL: RK628 and libaiden must use the validated 1080p30 EDID")
PY

echo "PASS: RK628D driver integration contract"
