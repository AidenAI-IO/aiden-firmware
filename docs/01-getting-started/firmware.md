# 固件构建与刷机

## 获取预构建固件

如果不想本地完整编译固件，可以从 Release 下载预构建镜像：

- [Aiden Hardware Demo Releases](https://github.com/AidenAI-IO/aiden-hardware-demo/releases)

通常刷入完整固件时使用 `update.img`。

## 固件特性

本项目的固件基于 `pico-sdk` 构建，并包含以下定制：

- Wi-Fi 默认使用板载天线；
- Kernel 启用 TC358743 驱动；
- DTS 添加 TC358743 支持；
- 内置 1080p30-only EDID；
- USB-C 口开机配置为 HID gadget；
- 注入 `/overlay` 中的启动脚本、配置和应用二进制。

相关底层改动可在 `pico-sdk/` 子模块中查看。

## 本地构建完整固件

需要 x86_64 Linux + Docker 环境，或可运行 amd64 容器的兼容环境：

```bash
./build_image.sh
```

`build_image.sh` 会以 privileged 模式启动 Luckfox Docker 镜像，并执行 `_build_image.sh`。流程概要：

1. 编译应用程序：`./_build.sh`；
2. 将 `build/bin/` 复制到 `overlay/oem/usr/bin/`；
3. 同步 `overlay/etc/` 到 `pico-sdk` 的 Buildroot overlay；
4. 执行 `pico-sdk/build.sh all`；
5. 将 `overlay/oem`、`overlay/userdata` 注入输出目录；VAD 模型位于 `overlay/oem/usr/model/`，会随 OEM 分区进入 OTA；
6. 生成 A/B 分区镜像和完整 USB 首刷包。

构建结束后，镜像位于：

```text
pico-sdk/output/image/
```

## 刷入固件

> 需要使用 Luckfox Pico Zero 板载 USB-C 口连接电脑。刷机方式有多种，完整说明参考 [Luckfox Pico Zero 官方刷机指南](https://wiki.luckfox.com/zh/Luckfox-Pico-Zero/Flash-image/)。

### 1. 进入 Maskrom / Loader 模式

可选方法：

- 按住板子的 BOOT 按钮，同时插入 USB-C；
- 如果 BOOT 按键触发烧录模式不好用，可以先用 adb 或 TTL 串口登录到板子上，执行：

```bash
reboot loader
```

### 2. 使用 upgrade_tool 刷写

项目自带 macOS 可用的 `upgrade_tool`。Linux / Windows 版本可从 `pico-sdk/tools/` 获取。

```bash
cd aiden-hardware-demo
./upgrade_tool/upgrade_tool uf ./update.img
```

如果镜像来自本地构建，路径通常类似：

```bash
./upgrade_tool/upgrade_tool uf ./pico-sdk/output/image/update.img
```

## 分区参考

生产镜像使用 A/B 分区布局：

| Partition | Size | 用途 |
| --- | ---: | --- |
| `env` | 32 KB | Bootloader environment，工厂/USB recovery only |
| `idblock` | 512 KB @ 32 KB | Rockchip idblock，工厂/USB recovery only |
| `uboot` | 256 KB | Bootloader，工厂/USB recovery only |
| `misc` | 4 MB | SPL A/B metadata，AVB A/B record 位于 byte offset `2048` |
| `boot_a` | 32 MB | Slot A FIT boot image，指向 `rootfs_a` |
| `boot_b` | 32 MB | Slot B FIT boot image，指向 `rootfs_b` |
| `oem_a` | 256 MB | Slot A `/oem` 内容 |
| `oem_b` | 256 MB | Slot B `/oem` 内容 |
| `rootfs_a` | 1536 MB | Slot A root filesystem |
| `rootfs_b` | 1536 MB | Slot B root filesystem |
| `userdata` | 3 GB | 共享持久数据、OTA 配置和运行时状态 |

`upgrade_tool` 支持单独更新指定分区；完整升级一般使用 `uf update.img`。

生产镜像使用 A/B 分区布局。在线 OTA 只写入非活动槽位的 `boot_*`、`oem_*`、`rootfs_*` 分区；`env`、`idblock`、`uboot` 仅用于工厂或 USB 恢复刷机，不通过 OTA 更新。`misc` 分区保存 Rockchip SPL A/B 元数据，元数据位于字节偏移 `2048`。

发布版 `update.img` 会内置 `/userdata/ota/config.json`，其中包含 `repo`、`channel`、`factory_version`、`factory_build_time` 和 slot-aware `factory_partition_hashes`，设备首次 USB 刷机后即可从 GitHub Release 执行后续 OTA。

更多 OTA 细节见 [OTA 概览](../09-ota/README.md)。
