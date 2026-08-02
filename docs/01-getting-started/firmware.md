# Firmware Build and Flashing

## Getting Prebuilt Firmware

If you don't want to compile the firmware locally from scratch, you can download a prebuilt image from Releases:

- [Aiden Firmware Releases](https://github.com/AidenAI-IO/aiden-firmware/releases)

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
3. Read `scripts/rootfs_cli_tools.catalog`, build or download every pinned ARMv7 CLI tool, verify the resulting bundle, then stage it with `overlay/etc/` in the `pico-sdk` Buildroot overlay;
4. Run `pico-sdk/build.sh all`;
5. Inject `overlay/oem` and `overlay/userdata` into the output directory; the VAD model is located in `overlay/oem/usr/model/` and is included in OTA along with the OEM partition;
6. Generate the A/B partition images and the full USB first-flash package.

The CLI tools are installed in rootfs as `/usr/bin/fq`, `/usr/bin/yq`, and `/usr/bin/rg`. Because `/usr/bin` is part of the default service and login-shell `PATH`, Agent shell calls do not need an OEM-specific PATH override.

Add future tools through `scripts/rootfs_cli_tools.catalog`. Go modules use `kind=go`; checksum-pinned `.tar.gz` releases use `kind=tar_gz`. Entries with `strip_policy=preserve` are restored after the SDK release-strip pass and verified byte-for-byte in the final rootfs image. Entries with `strip_policy=normal` keep the SDK-stripped bytes and are verified against the final package staging tree. A sidecar managed-tool list next to the Buildroot overlay ensures removed or renamed catalog entries are also deleted from reused staging without placing that state file in rootfs.

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
