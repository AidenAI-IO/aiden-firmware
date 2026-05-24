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
5. 将 `overlay/oem`、`overlay/userdata` 注入输出目录；
6. 重新打包 `oem.img`、`userdata.img` 和相关镜像。

构建结束后，镜像位于：

```text
pico-sdk/output/image/
```

## 刷入固件

> 需要使用 Luckfox Pico Zero 板载 USB-C 口连接电脑。

### 1. 进入 Maskrom / Loader 模式

可选方法：

- 按住板子的 boot 按钮，同时插入 USB-C；
- 或在设备 shell 中执行：

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

| Partition | Start Address | End Address | Size (Hex) | Size (Bytes) | Size |
| --- | --- | --- | --- | ---: | --- |
| `env` | `0x000000000000` | `0x000000040000` | `0x40000` | 262,144 | 256 KB |
| `idblock` | `0x000000040000` | `0x000000080000` | `0x40000` | 262,144 | 256 KB |
| `uboot` | `0x000000080000` | `0x000000100000` | `0x80000` | 524,288 | 512 KB |
| `boot` | `0x000000100000` | `0x000000500000` | `0x400000` | 4,194,304 | 4 MB |
| `oem` | `0x000000500000` | `0x000002300000` | `0x1E00000` | 31,457,280 | 30 MB |
| `userdata` | `0x000002300000` | `0x000002d00000` | `0xA00000` | 10,485,760 | 10 MB |
| `rootfs` | `0x000002d00000` | `0x00000ff00000` | `0xC200000` | 203,843,584 | ~194 MB |

`upgrade_tool` 支持单独更新指定分区；完整升级一般使用 `uf update.img`。

生产镜像使用 A/B 分区布局。在线 OTA 只写入非活动槽位的 `boot_*`、`oem_*`、`rootfs_*` 分区；`env`、`idblock`、`uboot` 仅用于工厂或 USB 恢复刷机，不通过 OTA 更新。`misc` 分区保存 Rockchip SPL A/B 元数据，元数据位于字节偏移 `2048`。

发布版 `update.img` 会内置 `/userdata/ota/config.json`，其中包含 `repo`、`channel`、`factory_version`、`factory_build_time` 和 slot-aware `factory_partition_hashes`，设备首次 USB 刷机后即可从 GitHub Release 执行后续 OTA。
