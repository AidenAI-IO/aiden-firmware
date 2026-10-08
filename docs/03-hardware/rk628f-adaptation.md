# RK628F hardware adaptation

Aiden SCH v1 uses J3 for both USB2 ECM and Type-C video. The video path is
CSA171 -> CS5366 -> RK628F HDMI RX -> four CSI lanes -> RV1106.
`RK628_Plugin_DET` is routed to the MCU, not to an RV1106 GPIO. The device
tree therefore deliberately has no `plugin-det-gpios` property.

The older RK628D driver cannot initialize this silicon. The board reports
`0x20230321` at register `0x0200`, identifying RK628F/H. The SDK now
backports the coherent RK628 receiver, clock/reset, MIPI PHY and
post-processing drivers from Rockchip commit
`81417fbf13edb25f203ea794a723b8bf1f457d5c`. See
`pico-sdk/sysdrv/source/kernel/drivers/media/i2c/rk628/README.aiden` for
the source and local compatibility changes.

The board retains its onboard 24 MHz oscillator, I2C4 M1 at 100 kHz,
GPIO1_A0 power enable, GPIO1_A1 active-low reset, GPIO1_B1 interrupt,
and four continuous-clock CSI lanes. HPD is controlled by the receiver
driver, not a GPIO-high startup workaround. The built-in EDID matches
`edid/hdmi_1080p60_cta.hex` and advertises 1080p60 only.

Userspace EDID updates stop streaming, hold HPD low, and schedule receiver
configuration again. An absent input returns `ENOLCK` rather than a
successful fallback VGA timing. Without a plugin GPIO, failed training
retries autonomously with a ten-second delay.

## Hardware verification

Check all of the following with the connected host awake:

1. Kernel log reports `RK628F/H (20230321)`.
2. The host enumerates `Linux FHD60` at 1920x1080, 60 Hz without a manual
   HPD override.
3. `v4l2-ctl -d /dev/v4l-subdev2 --query-dv-timings` returns locked,
   nonzero input timings. Discover the actual subdevice using
   `media-ctl -p` if numbering changes.
4. `/sys/kernel/debug/rk628/4-0050/hdmirx/status` shows clock and all three
   HDMI data channels locked.
5. `frame_service_cli --socket /run/frame_service/frame_service.sock
   screenshot --out /tmp/capture.jpg` returns an actual image, and
   successive captures change when the host image changes.
6. Repeat after software reboot and verify Wi-Fi recovery and
   `systemctl --failed`.

Host display enumeration alone verifies HPD/DDC, not pixel transport.
A sleeping host can keep its display enumerated while transmitting no
video clock. The driver must report no signal in that condition.

The SDK BSP build and audit compile the hardware driver. Also run
`bash scripts/test_rk628d_driver.sh` (the legacy filename is retained)
for the board/EDID integration contract, followed by the required Docker
`make check` and `make check-full` profiles.
