# Aiden Hardware Demo

Aiden Hardware Demo is a hardware demo project for the Luckfox Pico Zero, integrating HDMI frame capture, audio record/playback, USB HID control, a device config web page, and a Go LLM Agent.

The project manages low-level hardware resources through C++ services and exposes Frame / Audio capabilities over Unix domain sockets; the Go Agent provides a Web UI, HTTP Tool API, Skills, voice pipeline, and HID automation control on the device side.

## Hardware

- [Luckfox Pico Zero](https://wiki.luckfox.com/Luckfox-Pico-Zero)
- [TC358743XBG](https://toshiba.semicon-storage.com/eu/semiconductor/product/interface-bridge-ics-for-mobile-peripheral-devices/hdmir-interface-bridge-ics/detail.TC358743XBG.html)
- [CH375B](https://easyelecmodule.com/ch375b-u-disk-read-write-module-development-guide/)

## Quick Start

> If this is your first time bringing up the hardware (wiring, flashing, configuring, getting voice working), go straight to [Newcomer Quickstart](docs/01-getting-started/quickstart.md). Below is a build cheat sheet for developers.

```bash
git clone --recursive git@github.com:AidenAI-IO/aiden-hardware-demo.git
cd aiden-hardware-demo
```

Standard CMake build:

```bash
cmake -S . -B build
cmake --build build
```

Luckfox cross-compilation:

```bash
./build.sh
```

Run host unit tests:

```bash
make test
```

Build the full firmware:

```bash
./build_image.sh
```

Flash a prebuilt or locally built `update.img`:

```bash
./upgrade_tool/upgrade_tool uf ./update.img
```

## Documentation Index

The full documentation is organized under [`docs/`](docs/README.md):

- [Documentation Hub](docs/README.md)
- **Newcomers first: [Newcomer Quickstart](docs/01-getting-started/quickstart.md)** — strings the entire onboarding path together in the order wire up, flash, configure, develop, upgrade, troubleshoot
- [Hardware & Wiring](docs/01-getting-started/hardware.md)
- [Firmware Build & Flashing](docs/01-getting-started/firmware.md)
- [Build & Development Environment](docs/01-getting-started/build.md)
- [Deployment to Device](docs/01-getting-started/deployment.md)
- [Architecture Overview](docs/02-architecture/overview.md)
- [Frame Service](docs/03-services/frame-service.md)
- [Audio Service](docs/03-services/audio-service.md)
- [USB HID & Device Control](docs/03-services/usb-hid.md)
- [Go Agent](docs/04-agent/overview.md)
- [Agent Configuration Reference (incl. Config Web config page)](docs/04-agent/configuration.md)
- [Tools HTTP API](docs/04-agent/tools-http-api.md)
- [C++ SDK Reference](docs/05-sdk-and-tools/cpp-sdk.md)
- [Unix Domain Socket Protocol](docs/06-protocols/uds-protocol.md)
- [Troubleshooting](docs/07-operations/troubleshooting.md)
- [OTA](docs/09-ota/README.md)
