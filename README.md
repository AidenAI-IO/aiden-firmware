# Aiden Hardware Demo

Aiden Hardware Demo is the firmware and device-side agent runtime for the
current Aiden development board. The board observes a target device through
HDMI capture and controls it through USB HID, so the agent can operate normal
mobile or desktop apps without relying on app-specific automation APIs.

This repository is for the development-board implementation: firmware overlay,
C++ hardware services, the Go Agent, OTA tooling, tests, and benchmark support.
It is not a final integrated hardware product.

![Aiden Development Board](./aiden-dev-board.webp)

## What Aiden Does

The current hardware setup connects to a phone or computer through a USB-C hub:

- the target device's display output is captured through HDMI and the TC358743
  HDMI-to-CSI path;
- the Luckfox Pico Zero exposes a composite USB gadget with keyboard/pointer HID
  and USB ECM networking;
- the Go Agent sends screenshots to a configured multimodal model, decides the
  next action, and writes keyboard/mouse/touch reports to `/dev/hidg0` and
  `/dev/hidg1`;
- voice mode records audio on the board, applies VAD, then uses configured STT,
  LLM, and TTS providers.

The basic control path does not require jailbreak, ADB, developer mode, or a
custom app on the target device. It does require a target that can output video
to the capture path and accept USB HID input. iOS control also requires
AssistiveTouch to be enabled.

## Key Features

### 1. **True Portability**
Most mobile agent projects are lab prototypes that require a laptop or desktop to run. Aiden is:
- **Pocket-sized** — Powered entirely by your phone's USB-C port (future versions will be credit-card-sized and magnetically attach to phone backs)
- **Plug-and-play** — No setup, no pairing, no configuration wizards. Connect and start.
- **Production-ready** — Designed for daily use, not just research prototypes

### 2. **Fully Open**
- **Open-source firmware** — Complete C++ services and Go agent code
- **Development board reference** — Current prototyping board schematics and assembly guide available
- **Any LLM** — Configure OpenAI, Anthropic, local models, or your own deployment
- **Exportable data** — Your memory, skills, and learned behaviors are yours to keep and migrate
- **Community-driven** — Contributions welcome; flash custom firmware freely

### 3. **Privacy-First**
- **No Aiden backend** — All services (LLM, STT, TTS) are user-configured external endpoints
- **Zero data collection** — We never see your screen, conversations, or behavior
- **Self-hostable** — Run your own models; keep everything on your infrastructure
- **Auditable** — Open-source code under community scrutiny

### 4. **Adaptive Intelligence**
- **Persistent memory** — Aiden remembers device-specific context, user preferences, and past interactions
- **Self-improving skills** — Skills update based on usage patterns and new workflows
- **Personalized over time** — The more you use Aiden, the better it understands your habits and needs

### 5. **Real-Time Voice Interaction**
- **Hardware VAD** (Voice Activity Detection) — Wake-word-free voice control with sub-100ms latency
- **Streaming STT/TTS** — Natural conversation flow with live transcription and speech synthesis
- **Audio feedback** — Aiden speaks responses and status updates

## Repository Scope

- **Firmware integration**: Buildroot overlay, init scripts, USB gadget setup,
  Wi-Fi/config portal defaults, and full `update.img` generation.
- **C++ services**: `frame_service` owns HDMI capture and exposes screenshots over
  Unix domain sockets; `audio_service` owns recording/playback and volume state.
- **Go Agent**: the device-side LLM runtime, voice loop, skills, memory, and
  built-in screenshot/HID/audio/shell tools.
- **USB networking**: The board exposes `usb0` at `192.168.42.1` for the device
  config page and local board-to-phone communication.
- **OTA**: A/B partition updates, signed manifests, health confirmation, and
  `abctl` diagnostics.

The firmware has no required Aiden-hosted backend. Screenshots, audio, and text
are sent only to the model/STT/TTS/search endpoints you configure.

## Architecture

```text
Target display
    -> HDMI / TC358743 / CSI
    -> /dev/video0
    -> frame_service
    -> screenshot tool
    -> Go Agent
    -> HID tools
    -> /dev/hidg0 and /dev/hidg1
    -> target device input

Board audio
    -> audio_service
    -> VAD / STT or audio attachment
    -> Go Agent / LLM
    -> TTS
    -> audio_service playback
```

At runtime, the Agent is intentionally decoupled from C++ service internals:
Frame and Audio capabilities go through Unix domain sockets, while device
control goes through the Linux USB gadget HID device nodes.

## Hardware

The current development setup centers on:

- [Luckfox Pico Zero](https://wiki.luckfox.com/Luckfox-Pico-Zero), RV1106 /
  Rockchip platform running Buildroot Linux;
- [TC358743XBG](https://toshiba.semicon-storage.com/eu/semiconductor/product/interface-bridge-ics-for-mobile-peripheral-devices/hdmir-interface-bridge-ics/detail.TC358743XBG.html),
  HDMI-to-CSI bridge for external screen capture;
- a USB-C hub that provides HDMI output for capture and a USB data path back to
  the target device;
- the Pico Zero Linux USB gadget stack for keyboard, pointer/touch, and ECM
  networking.

Some prototype hardware revisions may include additional USB modules, but the
firmware-documented HID path is the Linux gadget path exposed as `/dev/hidg0`
and `/dev/hidg1`.

See [Hardware & Wiring](docs/01-getting-started/hardware.md) for wiring and
target-device prerequisites.

## Quick Start

First-time hardware users should start with
[Newcomer Quickstart](docs/01-getting-started/quickstart.md). It walks through
wiring, flashing, Wi-Fi setup, Agent configuration, voice verification, and
troubleshooting.

Clone the repository:

```bash
git clone --recursive git@github.com:AidenAI-IO/aiden-firmware.git
cd aiden-firmware
```

Run host-native unit tests:

```bash
make test
```

Build ARM binaries for the device:

```bash
./build.sh
```

Build the full firmware image:

```bash
./build_image.sh
```

Flash a prebuilt or locally built `update.img`:

```bash
./upgrade_tool/upgrade_tool uf ./update.img
```

For a locally built image, the usual output path is:

```bash
./upgrade_tool/upgrade_tool uf ./pico-sdk/output/image/update.img
```

## Documentation

The full documentation is organized under [docs/](docs/README.md):

- [Documentation Hub](docs/README.md)
- [Newcomer Quickstart](docs/01-getting-started/quickstart.md)
- [Hardware & Wiring](docs/01-getting-started/hardware.md)
- [Firmware Build & Flashing](docs/01-getting-started/firmware.md)
- [Build & Development Environment](docs/01-getting-started/build.md)
- [Deployment to Device](docs/01-getting-started/deployment.md)
- [Testing & Verification](docs/01-getting-started/testing.md)
- [Architecture Overview](docs/02-architecture/overview.md)
- [Frame Service](docs/03-services/frame-service.md)
- [Audio Service](docs/03-services/audio-service.md)
- [USB HID & Device Control](docs/03-services/usb-hid.md)
- [Go Agent](docs/04-agent/overview.md)
- [Agent Configuration Reference](docs/04-agent/configuration.md)
- [Tools HTTP API](docs/04-agent/tools-http-api.md)
- [OTA](docs/08-ota/README.md)
- [Benchmark](docs/09-benchmark/README.md)
- [SkillOpt](docs/10-skillopt/README.md)
- [Troubleshooting](docs/07-operations/troubleshooting.md)

## Contributing

Contributions are welcome for firmware, services, Agent runtime, skills, tests,
benchmarks, and documentation. Before contributing, read [LICENSE](LICENSE); it
defines the dual-license terms, contribution license grant, patent terms, and
hardware-design licensing notes for this repository.

Do not commit secrets, device-specific credentials, or local environment files.

## License

This repository is distributed under the dual-license terms described in
[LICENSE](LICENSE), with the AGPL-3.0 text included in
[LICENSE-AGPL-3.0](LICENSE-AGPL-3.0). Third-party components keep their own
licenses; see [NOTICE](NOTICE) for details.
