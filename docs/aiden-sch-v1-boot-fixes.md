# Aiden SCH v1 boot repair

Changes are based on aiden_sch_v1_finalnew1.pdf (2026-09-07), sheets 1,
4, 6–8, 13 and 15, checked against Rockchip RV1106 Datasheet V1.3
table 2-1 and the SDK pinctrl definitions.

## Wiring corrections

| Function | RV1106 pin / mux | Previous conflict |
| --- | --- | --- |
| WL_EN | pin 3, GPIO3_C5, external pull-up to V_WIFI_1V8 | Linux must not drive the MCU-owned rail |
| BT wake | pin 4, GPIO3_C6 | Activity LED drove the same net |
| CPU AVS | pin 74, GPIO1_A2, PWM0 M0 | Fixed regulator and BT host IRQ |
| RK628 I2C | pins 93/92, GPIO1_C2/C3, I2C4 M1 | I2C3 M1 goes to MCU UART |
| RK628 3.3V enable | pin 72, GPIO1_A0 | Misassigned TF supply; legacy wakeup GPIO32 |
| RK628 reset | pin 73, GPIO1_A1 | Incorrect GPIO1_B0; legacy wakeup GPIO33 |
| RK628 interrupt | pin 78, GPIO1_B1 | Retained |
| TF supply enable | pin 59, GPIO0_A1 | Incorrect GPIO1_A0 |
| Speaker enable | pin 22, GPIO4_C1, codec pa-ctl | GPIO3_C0 is CSI clock; simple-card did not consume spk-con |
| RV_HOLD | pin 87, GPIO1_D1 | Incorrect GPIO0_A3; UART3 M1 conflict |
| MCU UART | pins 86/85, UART5 M1 | Incorrect UART3 M1 |
| ASRPro UART | pins 91/90, UART4 M1 | UART2 M1 belongs to debug console |

The charger and BQ27220 are on the companion MCU's I2C bus. Remove
the fictitious RV1106 I2C1 fuel-gauge node; I2C1 M1 steals Bluetooth UART0
RX/TX. BT_WAKE_MCU is routed to the MCU and is not a Linux host IRQ.
The MCU must assert WIFI_VCC_PWREN; Linux cannot switch that rail directly. The current image therefore disables the Wi-Fi, Bluetooth and BLE feature gates until MCU firmware that asserts GD32 PA4 is available. This avoids a failed boot unit while preserving the AIC8800 driver for a hardware-enabled board.

## Media initialization

Do not write CIF/ISP clr_unready_dev. It forces completion while the RK628
endpoint is missing, and the vendor CIF format check dereferences a null input
format. Preserve the async graph for normal binding and allow RGA, MPP, NPU,
codec and Rockit to load without a bridge. Also reject null CIF input formats
in the kernel. The missing-bridge loader test exercises this failure mode and
idempotent repeated initialization.

The agent disables Pico Zero GPIO32/33 wakeup watchers on model
Aiden SCH v1. Those pins now belong exclusively to RK628 power/reset.
This does not implement MCU button/voice wakeup protocol support.

## CPU regulator

SY8892 feedback is 0.6V. R82=56k, R84=100k, R85=24k, R86=150k,
R87=10k give:

- Vx = (Vpwm/24 + 0.6/150) / (1/24 + 1/150 + 1/10)
- Varm = 0.6 + 56 * (0.6/100 + (0.6 - Vx)/150)
- Nominal Varm is 1.149933V at PWM low and 0.803865V at PWM high.

Use an inverted 200kHz PWM, nominal 0.804–1.150V limits and 250us
settling time. These are schematic-derived values, not board measurements.
Validate VDD_ARM with a meter/scope at several OPPs after flashing.
The inherited OPP requests (0.85–1.0V) now fall within the regulator range.

## Validation after flashing

Reboot into the matched boot/OEM/rootfs build: the previous Oops leaves the
running kernel tainted, so restarting its service is not a clean kernel test.

- Check systemctl --failed, the current boot kernel journal, /sys/kernel/debug/gpio
  and /sys/kernel/debug/pwm.
- Confirm SDIO function enumeration, wlan0, AIC firmware load and hci0.
  If SDIO still does not enumerate, measure MCU-controlled WiFi 3.3V and 1.8V.
- Confirm 4-0050 binds rk628-csi-v4l2, then verify HDMI with a source connected.
  Test a boot without an HDMI source as well; no CIF Oops is acceptable.
- Check audio playback/recording, NPU/RGA/MPP device nodes and CPU frequency
  transitions with no voltage-setting errors.
- Connect WiFi to a network that supplies DHCP routing/DNS, then verify
  ip route, timedatectl timesync-status and timedatectl show -p NTPSynchronized.
  USB-only 192.168.42.0/24 has no Internet route, so NTP cannot synchronize.
  Do not hardcode the development host as the default gateway or fake NTP sync.
