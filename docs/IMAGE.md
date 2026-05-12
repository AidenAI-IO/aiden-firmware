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
> `./build_image.sh` 将会使用 Docker 启动一个标准的 Ubuntu 22.04 编译环境，自动编译所有组件以及打包相关镜像

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
./upgrade_tool/upgrade_tool uf ./image/update.img
```

upgrade_tool 是支持单独更新指定的分区的，分区划分可参考下表：

| Partition | Start Address | End Address | Size (Hex) | Size (Bytes) | Size (Readable) |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **env** | `0x000000000000` | `0x000000040000` | `0x40000` | 262,144 | 256 KB |
| **idblock** | `0x000000040000` | `0x000000080000` | `0x40000` | 262,144 | 256 KB |
| **uboot** | `0x000000080000` | `0x000000100000` | `0x80000` | 524,288 | 512 KB |
| **boot** | `0x000000100000` | `0x000000500000` | `0x400000` | 4,194,304 | 4 MB |
| **oem** | `0x000000500000` | `0x000002300000` | `0x1E00000` | 31,457,280 | 30 MB |
| **userdata** | `0x000002300000` | `0x000002d00000` | `0xA00000` | 10,485,760 | 10 MB |
| **rootfs** | `0x000002d00000` | `0x00000ff00000` | `0xC200000` | 203,843,584 | ~194.375 MB |
