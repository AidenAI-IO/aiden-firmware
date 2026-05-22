# 固件

[Luckfox Pico Zero Documentation](https://wiki.luckfox.com/Luckfox-Pico-Zero/)
[Aiden SDK](https://github.com/AidenAI-IO/aiden-sdk)

## 固件获取

如果不想自己编译生成固件（耗时较长），可以下载使用提前准备好的固件：

[Aiden Release](https://github.com/AidenAI-IO/aiden-hardware-demo/releases)


## 固件生成

你需要准备好 x86_64 Linux 环境，并安装好 Docker

```shell
./build_image.sh
```

> [!NOTE]
> `./build_image.sh` 将会使用 Docker 启动 Luckfox Pico SDK 编译环境，自动编译所有组件以及打包相关镜像。完整 USB 刷机包输出到 `pico-sdk/output/image/update.img`，A/B OTA 分区镜像输出在同一目录。

## 固件刷入

> [!NOTE]
> 需要板载 USB-C 口连接电脑

### 1. 让开发版进入 maskrom 模式

方法1: 按住 boot 按钮后，原 USB-C 口接入电脑

方法2: 在板子 shell 中执行 `reboot loader`

### 2. 刷入固件

项目目录下提供了 macOS 下可用的 `upgrade_tool`，linux 和 windows 版本的工具可以在 `pico-sdk/tools/` 下获取

```shell
cd aiden-hardware-demo
# 刷入完整固件
./upgrade_tool/upgrade_tool uf pico-sdk/output/image/update.img
```

upgrade_tool 支持单独更新指定分区。生产镜像使用 A/B 分区布局，在线 OTA 只写入非活动槽位的 `boot_*`、`oem_*`、`rootfs_*` 分区；`env`、`idblock`、`uboot` 仅用于工厂或 USB 恢复刷机，不通过 OTA 更新。`misc` 分区保存 Rockchip SPL A/B 元数据，元数据位于字节偏移 `2048`，详见 `docs/OTA_AB_VERIFICATION.md`。

| Partition | Size | Purpose |
| :--- | :--- | :--- |
| **env** | 32 KB | Bootloader environment, factory/USB recovery only |
| **idblock** | 512 KB @ 32 KB | Rockchip idblock, factory/USB recovery only |
| **uboot** | 256 KB | Bootloader, factory/USB recovery only |
| **misc** | 4 MB | SPL A/B metadata; AVB A/B record at byte offset `2048` |
| **boot_a** | 32 MB | Slot A FIT boot image with `rootfs_a` bootargs |
| **boot_b** | 32 MB | Slot B FIT boot image with `rootfs_b` bootargs |
| **oem_a** | 256 MB | Slot A `/oem` contents, mounted when `aiden.slot_suffix=_a` |
| **oem_b** | 256 MB | Slot B `/oem` contents, mounted when `aiden.slot_suffix=_b` |
| **rootfs_a** | 1 GB | Slot A root filesystem |
| **rootfs_b** | 1 GB | Slot B root filesystem |
| **userdata** | 4 GB | Shared persistent data, including `/userdata/ota` state |

构建完成后，`pico-sdk/output/image/` 应包含 `misc.img`、`boot_a.img`、`boot_b.img`、`oem_a.img`、`oem_b.img`、`rootfs_a.img`、`rootfs_b.img`、`userdata.img` 和 `update.img`。发布流程还会在同一目录生成签名的 `manifest.json`。
