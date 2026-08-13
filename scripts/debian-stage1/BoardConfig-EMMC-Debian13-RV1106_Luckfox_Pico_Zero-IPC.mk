#!/usr/bin/env bash

#################################################
# Stage 1 Debian board configuration
#################################################
export LF_ORIGIN_BOARD_CONFIG=BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk
export RK_CHIP=rv1106
export RK_APP_TYPE=RKIPC_RV1106
export RK_BOOTARGS_CMA_SIZE="100M"
# Stage-1 networking and helper scripts intentionally use the vendor names
# wlan0 and usb0. Keep predictable legacy names until the hardware matrix is
# validated and the userspace is ready to consume arbitrary udev names. Keep
# the equals sign escaped because the vendor build's config-reset grep treats a
# second literal '=' on an export line as an unterminated shell assignment.
export RK_KERNEL_CMDLINE_EXTRA=net.ifnames$'\x3d'0
export RK_KERNEL_DTS=rv1106g-luckfox-pico-zero.dts

export RK_BOOT_MEDIUM=emmc
export RK_UBOOT_DEFCONFIG_FRAGMENT="rk-emmc.config"

# Keep the original Luckfox Pico Zero factory partition layout for stage 1.
export RK_PARTITION_CMD_IN_ENV="32K(env),512K@32K(idblock),256K(uboot),32M(boot),512M(oem),256M(userdata),6G(rootfs)"
export RK_PARTITION_FS_TYPE_CFG=rootfs@IGNORE@ext4,userdata@/userdata@ext4,oem@/oem@ext4

# The SDK still uses its bundled compiler for U-Boot, the kernel and modules.
# No binary produced by this toolchain is installed in the Debian rootfs.
export LF_TARGET_ROOTFS=debian
export RK_ARCH=arm
export RK_TOOLCHAIN_CROSS=arm-rockchip830-linux-uclibcgnueabihf

export RK_MISC=wipe_all-misc.img
export RK_UBOOT_DEFCONFIG=luckfox_rv1106_uboot_defconfig
export RK_KERNEL_DEFCONFIG=luckfox_rv1106_linux_defconfig
export RK_KERNEL_DEFCONFIG_FRAGMENT="rv1106-bt.config debian-stage1.config"

export RK_CAMERA_SENSOR_IQFILES="mia1321_MIA1321_30IRC-F16.json imx415_CMK-OT2022-PX1_IR0147-36IRC-8M-F20.json"
export RK_BUILD_APP_TO_OEM_PARTITION=y
export RK_ENABLE_WIFI=y
export RK_ENABLE_WIFI_CHIP=AIC8800DC

# Stage 1 assembles OEM and userdata images itself. Do not apply Buildroot
# overlays or package Buildroot IPC applications into the Debian image.
export RK_POST_OVERLAY=""
