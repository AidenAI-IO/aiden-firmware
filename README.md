# Aiden Hardware Demo

Aiden Hardware Demo 是面向 Luckfox Pico Zero 的硬件演示项目，集成 HDMI 画面采集、音频录放、USB HID 控制、设备配置网页和 Go LLM Agent。

项目通过 C++ 服务管理底层硬件资源，通过 Unix domain socket 暴露 Frame / Audio 能力；Go Agent 在设备侧提供 Web UI、HTTP Tool API、Skills、语音链路和 HID 自动化控制。

## 硬件

- [Luckfox Pico Zero](https://wiki.luckfox.com/Luckfox-Pico-Zero)
- [TC358743XBG](https://toshiba.semicon-storage.com/eu/semiconductor/product/interface-bridge-ics-for-mobile-peripheral-devices/hdmir-interface-bridge-ics/detail.TC358743XBG.html)
- [CH375B](https://easyelecmodule.com/ch375b-u-disk-read-write-module-development-guide/)

## 快速开始

> 第一次上手硬件（连线、刷机、配置、跑通语音）请直接看 [新手快速上手](docs/01-getting-started/quickstart.md)。下面是面向开发者的构建速查。

```bash
git clone --recursive git@github.com:AidenAI-IO/aiden-hardware-demo.git
cd aiden-hardware-demo
```

标准 CMake 构建：

```bash
cmake -S . -B build
cmake --build build
```

Luckfox 交叉编译：

```bash
./build.sh
```

运行 host 单元测试：

```bash
make test
```

构建完整固件：

```bash
./build_image.sh
```

刷入预构建或本地构建的 `update.img`：

```bash
./upgrade_tool/upgrade_tool uf ./update.img
```

## 文档目录

完整文档已整理到 [`docs/`](docs/README.md)：

- [文档中心](docs/README.md)
- **新手优先：[新手快速上手](docs/01-getting-started/quickstart.md)** — 按连线、刷机、配置、开发、升级、排障的顺序串起整条上手路径
- [硬件与连线](docs/01-getting-started/hardware.md)
- [固件构建与刷机](docs/01-getting-started/firmware.md)
- [构建与开发环境](docs/01-getting-started/build.md)
- [部署到设备](docs/01-getting-started/deployment.md)
- [系统架构概览](docs/02-architecture/overview.md)
- [Frame Service](docs/03-services/frame-service.md)
- [Audio Service](docs/03-services/audio-service.md)
- [USB HID 与设备控制](docs/03-services/usb-hid.md)
- [Go Agent](docs/04-agent/overview.md)
- [Agent 配置参考（含 Config Web 配置页）](docs/04-agent/configuration.md)
- [工具 HTTP API](docs/04-agent/tools-http-api.md)
- [C++ SDK 参考](docs/05-sdk-and-tools/cpp-sdk.md)
- [Unix Domain Socket 协议](docs/06-protocols/uds-protocol.md)
- [故障排查](docs/07-operations/troubleshooting.md)
- [OTA](docs/09-ota/README.md)
