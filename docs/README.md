# Aiden Hardware Demo 文档中心

本文档中心是项目的结构化入口。根目录 `README.md` 仅保留项目简介、快速开发信息和指向本目录的目录索引。

> **第一次接触 Aiden？** 从 [新手快速上手](01-getting-started/quickstart.md) 开始，它按「连线 → 刷机 → 联网 → 配置 → 跑起来 → 开发 → 升级 → 排障」的顺序把整条上手路径串成一页，并链接到下面的专题文档。

## 文档结构

### 01. 入门

- [新手快速上手](01-getting-started/quickstart.md)
- [硬件与连线](01-getting-started/hardware.md)
- [固件构建与刷机](01-getting-started/firmware.md)
- [构建与开发环境](01-getting-started/build.md)
- [部署到设备](01-getting-started/deployment.md)
- [测试与验证](01-getting-started/testing.md)

### 02. 架构

- [系统架构概览](02-architecture/overview.md)
- [源码与目录结构](02-architecture/source-tree.md)
- [启动服务与运行时布局](02-architecture/boot-services.md)

### 03. 硬件服务

- [Frame Service：HDMI 帧捕获服务](03-services/frame-service.md)
- [Audio Service：音频录放服务](03-services/audio-service.md)
- [USB HID 与设备控制](03-services/usb-hid.md)
- [Config Web：设备配置网页](03-services/config-web.md)

### 04. Go Agent

- [Agent 概览](04-agent/overview.md)
- [Agent 配置参考](04-agent/configuration.md)
- [Agent Context Lifecycle](04-agent/context-lifecycle.md)
- [Session Memory Compaction](04-agent/session-memory.md)
- [工具 HTTP API](04-agent/tools-http-api.md)
- [语音能力：VAD / STT / TTS](04-agent/audio-features.md)
- [Skills 机制](04-agent/skills.md)

### 05. SDK 与工具

- [C++ SDK 参考](05-sdk-and-tools/cpp-sdk.md)
- [图像处理工具](05-sdk-and-tools/image-processing.md)
- [示例程序](05-sdk-and-tools/examples.md)

### 06. 协议

- [Unix Domain Socket 通用协议](06-protocols/uds-protocol.md)
- [Frame / Audio 服务协议参考](06-protocols/service-protocols.md)

### 07. 运维

- [音量初始化与调节](07-operations/audio-volume.md)
- [故障排查](07-operations/troubleshooting.md)

### 08. 参考

- [路径、产物与配置速查](08-reference/paths-and-artifacts.md)

### 09. OTA

- [OTA 概览](09-ota/README.md)
- [OTA 架构与运行时](09-ota/architecture.md)
- [OTA 密钥管理](09-ota/key-management.md)
- [设备验收流程](09-ota/device-acceptance.md)
- [A/B 与 abctl 验证](09-ota/verification.md)

### 10. Benchmark

- [快速开始](10-benchmark/README.md)
- [架构设计](10-benchmark/architecture.md)
- [详细指南](10-benchmark/quickstart.md)
