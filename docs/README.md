# Aiden Hardware Demo Documentation Hub

This documentation hub is the structured entry point for the project. The root `README.md` keeps only the project introduction, quick development info, and an index pointing to this directory.

> **First time with Aiden?** Start from [Newcomer Quickstart](01-getting-started/quickstart.md). It strings the entire onboarding path into one page in the order wire up → flash → connect Wi-Fi → configure → run → develop → upgrade → troubleshoot, linking to the topic docs below.

## Documentation Structure

### 01. Getting Started

- [Newcomer Quickstart](01-getting-started/quickstart.md)
- [Hardware & Wiring](01-getting-started/hardware.md)
- [Firmware Build & Flashing](01-getting-started/firmware.md)
- [Build & Development Environment](01-getting-started/build.md)
- [Deployment to Device](01-getting-started/deployment.md)
- [Testing & Verification](01-getting-started/testing.md)

### 02. Architecture

- [Architecture Overview](02-architecture/overview.md)
- [Source Tree & Directory Layout](02-architecture/source-tree.md)
- [Boot Services & Runtime Layout](02-architecture/boot-services.md)

### 03. Hardware Services

- [Frame Service: HDMI Frame Capture Service](03-services/frame-service.md)
- [Audio Service: Audio Record/Playback Service](03-services/audio-service.md)
- [USB HID & Device Control](03-services/usb-hid.md)

### 04. Go Agent

- [Agent Overview](04-agent/overview.md)
- [Agent Configuration Reference](04-agent/configuration.md)
- [Agent Context Lifecycle](04-agent/context-lifecycle.md)
- [Session Memory Compaction](04-agent/session-memory.md)
- [Tools HTTP API](04-agent/tools-http-api.md)
- [Voice Capabilities: VAD / STT / TTS](04-agent/audio-features.md)
- [Skills Mechanism](04-agent/skills.md)

### 05. SDK & Tools

- [C++ SDK Reference](05-sdk-and-tools/cpp-sdk.md)
- [Image Processing Tools](05-sdk-and-tools/image-processing.md)
- [Example Programs](05-sdk-and-tools/examples.md)

### 06. Protocols

- [Unix Domain Socket Common Protocol](06-protocols/uds-protocol.md)
- [Frame / Audio Service Protocol Reference](06-protocols/service-protocols.md)

### 07. Operations

- [Volume Initialization & Adjustment](07-operations/audio-volume.md)
- [Troubleshooting](07-operations/troubleshooting.md)

### 08. Reference

- [Paths, Artifacts & Config Cheat Sheet](08-reference/paths-and-artifacts.md)

### 09. OTA

- [OTA Overview](09-ota/README.md)
- [OTA Architecture & Runtime](09-ota/architecture.md)
- [OTA Key Management](09-ota/key-management.md)
- [Device Acceptance Process](09-ota/device-acceptance.md)
- [A/B & abctl Verification](09-ota/verification.md)

### 10. Benchmark

- [Quick Start](10-benchmark/README.md)
- [Architecture Design](10-benchmark/architecture.md)
- [Detailed Guide](10-benchmark/quickstart.md)
