---
sidebar_position: 4
---

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
- Builds a Debian 13 rootfs from `overlay-debian/` and a separate OEM image from
  `overlay-debian-oem/` plus the audited application bundle.

The related low-level changes can be found in the `pico-sdk/` submodule.

## Building the Full Firmware Locally

This requires an x86_64 Linux + Docker environment, or a compatible environment capable of running amd64 containers:

```bash
./debian_build.sh
```

The command requires an external Agent configuration and matching Ed25519 OTA
key pair (see `./debian_build.sh --help`). Process overview:

1. Build and audit the Debian armhf C/C++ and Go application bundle;
2. Build the pinned Debian 13 rootfs and apply `overlay-debian/`;
3. Build the RV1106 BSP, bootloader, kernel modules, and A/B boot images from the pinned SDK;
4. Assemble the OEM image from `overlay-debian-oem/`, audited applications, vendor libraries, models, and web assets;
5. Create rootfs, OEM, userdata, and OTA images and validate their contents;
6. Generate the signed local OTA manifest and full USB first-flash package.

After the build completes, the images are located in:

```text
output/debian/image/
```

## Firmware pip

The Debian production package set includes `python3` and `python3-pip`. After
building an image, verify the runtime with:

```bash
/usr/bin/python3 -m pip --version
```

Runtime-installed packages stay under `/userdata`; see
[Persistent Python Packages](../04-agent/python-packages.md).

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
./upgrade_tool/upgrade_tool uf ./output/debian/image/update.img
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
