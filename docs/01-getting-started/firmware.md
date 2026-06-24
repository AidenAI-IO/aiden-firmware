# Firmware Build and Flashing

## Getting Prebuilt Firmware

If you don't want to compile the firmware locally from scratch, you can download a prebuilt image from Releases:

- [Aiden Hardware Demo Releases](https://github.com/AidenAI-IO/aiden-hardware-demo/releases)

When flashing the full firmware, you typically use `update.img`.

## Firmware Features

This project's firmware is built on `pico-sdk` and includes the following customizations:

- Wi-Fi uses the external antenna by default;
- Kernel enables the TC358743 driver;
- DTS adds TC358743 support;
- Built-in 1080p30-only EDID;
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

## Flashing the Firmware

> You need to connect the Luckfox Pico Zero's onboard USB-C port to a computer. There are several flashing methods; for the complete instructions, refer to the [Luckfox Pico Zero official flashing guide](https://wiki.luckfox.com/zh/Luckfox-Pico-Zero/Flash-image/).

### 1. Enter Maskrom / Loader Mode

Available methods:

- Hold down the board's BOOT button while plugging in USB-C;
- If triggering flash mode with the BOOT button doesn't work well, you can first log in to the board via SSH on the USB network or the TTL serial port, then run:

```bash
reboot loader
```

The firmware image includes the `adb` client on the board so it can connect to
external Android devices like a host computer. It does not expose `adbd` for
host-side `adb shell` login into the board itself.

### 2. Flash with upgrade_tool

The project ships with an `upgrade_tool` that works on macOS. The Linux / Windows versions can be obtained from `pico-sdk/tools/`.

```bash
cd aiden-hardware-demo
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
| `userdata` | 3 GB | Shared persistent data, OTA configuration, and runtime state |

`upgrade_tool` supports updating individual partitions; a full upgrade generally uses `uf update.img`.

The production image uses an A/B partition layout. Online OTA only writes to the inactive slot's `boot_*`, `oem_*`, and `rootfs_*` partitions; `env`, `idblock`, and `uboot` are used only for factory or USB recovery flashing and are not updated via OTA. The `misc` partition holds the Rockchip SPL A/B metadata, which is located at byte offset `2048`.

The released `update.img` embeds `/userdata/ota/config.json`, which contains `repo`, `channel`, `factory_version`, `factory_build_time`, and slot-aware `factory_partition_hashes`, so that after the device's first USB flash it can perform subsequent OTAs from GitHub Releases.

For more OTA details, see [OTA Overview](../08-ota/README.md).
