# Firmware Build and Flashing

## Getting Prebuilt Firmware

If you don't want to compile the firmware locally from scratch, you can download a prebuilt image from Releases:

- [Aiden Firmware Releases](https://github.com/AidenAI-IO/aiden-firmware/releases)

When flashing the full firmware, you typically use `update.img`.

## Firmware Features

This project's firmware is built on `pico-sdk` and includes the following customizations:

- Wi-Fi uses the external antenna by default;
- Kernel builds both the Rockchip RK628 and Toshiba TC358743 HDMI-to-CSI V4L2 drivers;
- DTS declares the Firefly RK628D board on 100 kHz I2C4 at `0x50`, with GPIO3_C5 push-pull/no-pull reset and four continuous-clock CSI lanes, plus the legacy TC358743 board at `0x0f` with two non-continuous-clock lanes. Only the bridge that responds on I2C registers a V4L2 subdevice;
- Bridge-aware HDMI timing: RK628D keeps its 1080p60 EDID, while TC358743 automatically advertises 1080p30 to fit its two-lane CSI link;
- USB-C port is configured as a composite gadget on boot: keyboard HID,
  pointer/touch HID, and CDC ECM networking (`usb0`, default `192.168.42.1`);
- Injects startup scripts, configuration, and application binaries from `/overlay`.

The related low-level changes can be found in the `pico-sdk/` submodule.

## Building the Full Firmware Locally

This requires an x86_64 Linux + Docker environment, or a compatible environment capable of running amd64 containers:

```bash
./build_image.sh
```

`build_image.sh` launches the Luckfox Docker image in privileged mode and runs `_build_image.sh`. Process overview:

1. Compile the application: `./_build.sh`;
2. Copy `build/bin/` to `overlay/oem/usr/bin/`;
3. Sync `overlay/etc/` to the `pico-sdk` Buildroot overlay;
4. Run `pico-sdk/build.sh all`;
5. Inject `overlay/oem` and `overlay/userdata` into the output directory; the VAD model is located in `overlay/oem/usr/model/` and is included in OTA along with the OEM partition;
6. Generate the A/B partition images and the full USB first-flash package.

After the build completes, the images are located in:

```text
pico-sdk/output/image/
```

## Firmware pip Integration

The Python package environment enables the SDK-provided pip in both
active Luckfox Buildroot defconfigs:

```text
BR2_PACKAGE_PYTHON_PIP=y
```

The defconfigs live in the `pico-sdk` submodule:

```text
pico-sdk/sysdrv/tools/board/buildroot/luckfox_pico_defconfig
pico-sdk/sysdrv/tools/board/buildroot/luckfox_pico_w_defconfig
```

The current SDK uses Buildroot 2023.02.6, Python 3.11, and pip 22.3.1. The
repository build-policy test requires the option in both defconfigs. The SDK
also overrides Buildroot's `charset-normalizer` 3.0.1 recipe with 2.1.1 because
the bundled `aiohttp` 3.8.3 requires `charset-normalizer>=2.0,<3.0`; this keeps
the firmware package set valid under `python3 -m pip check`.

After a full image build, verify the rootfs package on the board with:

```bash
python3 -m pip --version
```

Runtime-installed packages do not go into the rootfs. See
[Persistent Python Package Environment](../04-agent/python-packages.md)
for the persistent `/userdata` layout and installation policy.

## Flashing the Firmware

> You need to connect the Luckfox Pico Zero's onboard USB-C port to a computer. There are several flashing methods; for the complete instructions, refer to the [Luckfox Pico Zero official flashing guide](https://wiki.luckfox.com/zh/Luckfox-Pico-Zero/Flash-image/).

### 1. Enter Maskrom / Loader Mode

Available methods:

- Hold down the board's BOOT button while plugging in USB-C;
- If triggering flash mode with the BOOT button doesn't work well, you can first log in to the board via SSH on the USB network or the TTL serial port, then run:

```bash
reboot loader
```

The firmware image includes the `adb` client on the board so it can act as an
ADB host for external Android devices.  It does not expose `adbd` for
host-side `adb shell` login into the board itself. The client is built from the
nmeum/android-tools 30.0.5p1 release (AOSP platform-tools-30) and reports
version 1.0.41, so it speaks the current adb auth and pairing protocol.

### 2. Flash with upgrade_tool

The project ships with an `upgrade_tool` that works on macOS. The Linux / Windows versions can be obtained from `pico-sdk/tools/`.

```bash
cd aiden-firmware
./upgrade_tool/upgrade_tool uf ./update.img
```

If the image comes from a local build, the path is usually similar to:

```bash
./upgrade_tool/upgrade_tool uf ./pico-sdk/output/image/update.img
```

## Partition Reference

The production image uses an A/B partition layout:

| Partition | Size | Purpose |
| --- | ---: | --- |
| `env` | 32 KB | Bootloader environment, factory/USB recovery only |
| `idblock` | 512 KB @ 32 KB | Rockchip idblock, factory/USB recovery only |
| `uboot` | 256 KB | Bootloader, factory/USB recovery only |
| `misc` | 4 MB | SPL A/B metadata, AVB A/B record at byte offset `2048` |
| `boot_a` | 32 MB | Slot A FIT boot image, points to `rootfs_a` |
| `boot_b` | 32 MB | Slot B FIT boot image, points to `rootfs_b` |
| `oem_a` | 256 MB | Slot A `/oem` contents |
| `oem_b` | 256 MB | Slot B `/oem` contents |
| `rootfs_a` | 1536 MB | Slot A root filesystem |
| `rootfs_b` | 1536 MB | Slot B root filesystem |
| `userdata` | 3 GB | Shared non-OTA persistent data |
| `ota` | 300 MiB | Dedicated OTA configuration, state, health markers, and download cache |

`upgrade_tool` supports updating individual partitions; a full upgrade generally uses `uf update.img`.

The production image uses an A/B partition layout. Online OTA only writes to the inactive slot's `boot_*`, `oem_*`, and `rootfs_*` partitions; `env`, `idblock`, and `uboot` are used only for factory or USB recovery flashing and are not updated via OTA. The `misc` partition holds the Rockchip SPL A/B metadata, which is located at byte offset `2048`.

The released `update.img` includes `ota.img`. When mounted at `/userdata/ota`, it provides `config.json` with `repo`, `channel`, `factory_version`, `factory_build_time`, and slot-aware `factory_partition_hashes`, so that after the device's first USB flash it can perform subsequent OTAs from GitHub Releases.

For more OTA details, see [OTA Overview](../08-ota/README.md).
