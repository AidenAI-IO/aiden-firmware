---
sidebar_position: 1
---

# System Architecture Overview

Aiden Hardware combines HDMI video capture, audio recording/playback, USB HID control, and LLM Agent into an automated system that runs on-device.

## Overall Architecture

```text
                 ┌──────────────────────────┐
                 │        Go Agent          │
                 │ Web UI / HTTP Tool API   │
                 │ LLM / Skills / Memory    │
                 └───────┬─────────┬────────┘
                         │         │
          screenshot UDS │         │ audio UDS / volume
                         │         │
             ┌───────────▼───┐ ┌───▼────────────┐
             │ frame_service │ │ audio_service   │
             │ C++ daemon    │ │ C++ daemon      │
             └───────┬───────┘ └───────┬────────┘
                     │                 │
           /dev/video0 + subdev        │ ALSA / RK MPI
                     │                 │
             ┌───────▼──────┐          │
             │ TC358743 HDMI│          │
             │ capture path │          │
             └──────────────┘          │

                 ┌──────────────────────────┐
                 │ USB HID Gadget           │
                 │ hidg0 / hidg1 / hidg2     │
                 └───────────▲──────────────┘
                             │
                         Agent tools
```

## Module Layers

| Layer | Module | Description |
| --- | --- | --- |
| Hardware Abstraction | `src/aiden_sdk.*` | Encapsulates GPIO, audio, video, HID-related low-level capabilities |
| Common Transport | `src/uds_*` | Unix domain socket one-shot request/response transport layer |
| Hardware Services | `frame_service`, `audio_service`, `ble_service` | Centrally manage hardware resources and expose them to other processes |
| Utility Programs | `*_cli`, `example_*`, `image_process` | Debugging, validation, and single-capability examples |
| Go Agent | `src/agent` | LLM runtime, tool invocation, Web UI, HTTP Tool API, voice pipeline |
| Firmware Integration | `overlay/`, `pico-sdk/` | Startup scripts, configuration files, userdata/oem injection |

## Key Design Principles

1. **Single Hardware Resource Owner**: For example, `/dev/video0` is exclusively owned by `frame_service`, and other consumers read frames via UDS, avoiding conflicts from multiple processes opening the video device simultaneously.
2. **Cross-Language Protocol**: UDS envelope uses JSON header + binary payload, allowing Go / C++ / other languages to implement clients.
3. **Agent and Hardware Decoupling**: Agent does not directly depend on C++ ABI, completing observation/operation through sockets and HID devices.
4. **Service Daemon**: Init scripts in overlay automatically restart services after they exit through a simple watchdog.

## Main Data Flows

### Screenshot / Visual Observation

```text
TC358743 → /dev/video0 → frame_service ring buffer → Go screenshot tool → LLM image input
```

### Device Control

```text
LLM tool call → Go HID tool → /dev/hidg0, /dev/hidg1, or /dev/hidg2 → target device
```

### Voice Interaction

```text
audio_service recording → RKNN Silero VAD → STT or audio attachment → LLM → TTS → audio_service playback
```

### Bluetooth Pairing and System Notifications

```text
iOS CoreBluetooth encrypted read → BlueZ/hci0 → ble_service pairing service
iOS ANCS → BlueZ/hci0 → ble_service event ring → UDS events_since
```

The encrypted GATT read establishes the system bond. ANCS events do not enter
Agent memory in this layer.
