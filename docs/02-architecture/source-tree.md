# Source Code and Directory Structure

```text
aiden-firmware/
├── CMakeLists.txt                 # Main C/C++ build configuration
├── Makefile                       # Local build/test shortcut entry point
├── build.sh / _build.sh           # Docker cross-compile the application
├── build_image.sh / _build_image.sh # Full firmware build entry point
├── cmake/                         # Toolchain files
├── docs/                          # Structured documentation
├── edid/                          # HDMI EDID hex files
├── overlay/                       # Firmware injection files: etc/oem/userdata
├── pico-sdk/                      # Luckfox SDK submodule
├── scripts/                       # Device helper scripts
├── src/                           # C/C++ SDK, services, tools
├── src/agent/                     # Go Agent
├── tests/                         # host-native unit tests
├── third_party/                   # doctest, stb, opencv-mobile, etc.
└── upgrade_tool/                  # Flashing tool
```

## Key files in `src/`

| File / Module | Description |
| --- | --- |
| `aiden_sdk.*` | C++ SDK entry point, providing Wakeup, AudioCapture, AudioPlayer, CameraCapture |
| `frame_*` | Frame Service, frame buffer, camera source, frame protocol and client |
| `audio_*` | Audio Service, recording/playback session, protocol and client |
| `uds_*` | Generic Unix domain socket transport layer |
| `service_status.*` | Common service status enums |
| `image_process.*` | Image cropping, black border removal, scaling, rotation |
| `hid_server.*`, `example_usb_hid.cpp` | USB HID gadget and HTTP example server |
| `config_web.*` | Device configuration web service, maintaining Agent TOML and Wi-Fi configuration |
| `agent_toml.*` | C++ side Agent TOML parsing/writing |

## Key directories in `src/agent/`

| Path | Description |
| --- | --- |
| `cmd/daemon` | Long-running Agent daemon providing the HTTP/Web UI in all input modes and an additional device voice loop in `stt` mode |
| `cmd/abctl` | A/B partition control utility for OTA system |
| `cmd/ota` | OTA update client |
| `cmd/benchmark-http` | Standalone LLM endpoint latency probe |
| `cmd/test-warmup` | Connection warmup measurement harness |
| `internal/agent/runtime.go` | Agent runtime, model invocation and tool loop |
| `internal/agent/server.go` | HTTP server / Web UI / tool API |
| `internal/agent/tools*.go` | Built-in tools such as HID, screenshot, audio, shell |
| `internal/agent/stt.go` / `internal/agent/tts/` / `vad.go` | STT, pluggable TTS providers, RKNN/CPU VAD orchestration |
| `internal/agent/skills*.go` | Skill loading, activation and tool restrictions |
| `config/agent.toml` | Agent configuration example |

## `overlay/` structure

```text
overlay/
├── etc/
│   ├── aiden_audio_service.conf
│   ├── aiden_frame_service.conf
│   ├── dnsmasq.d/usb0.conf
│   └── init.d/S*
├── oem/usr/bin/
├── oem/usr/model/
└── userdata/
    ├── agent/agent.toml
    └── wpa_supplicant.conf
```

`_build_image.sh` copies the application binaries to `overlay/oem/usr/bin/`, injects the VAD model into the OEM partition along with `overlay/oem/usr/model/`, and syncs/injects the overlay into the `pico-sdk` output image.
