# Aiden DEMO

## Hardware

- [Luckfox Pico Zero](https://wiki.luckfox.com/Luckfox-Pico-Zero)
- [TC358743XBG](https://toshiba.semicon-storage.com/eu/semiconductor/product/interface-bridge-ics-for-mobile-peripheral-devices/hdmir-interface-bridge-ics/detail.TC358743XBG.html)
- [CH375B](https://easyelecmodule.com/ch375b-u-disk-read-write-module-development-guide/)

## Init

```bash
git clone --recursive git@github.com:AidenAI-IO/aiden-hardware-demo.git
cd aiden-hardware-demo
```

## Flash Image

```bash
# Hold the boot button while plugging the board in, or run `reboot loader`
# on the device to enter maskrom mode.
# update.img can be found on: https://github.com/AidenAI-IO/aiden-hardware-demo/releases
./upgrade_tool/upgrade_tool uf ./update.img
```

The bundled firmware is built from `pico-sdk` with the following adjustments:

- Wi-Fi defaults to the onboard antenna
- Kernel enables the TC358743 driver
- DTS adds TC358743 support
- A 1080p30 EDID is baked in
- The USB-C port is configured as a HID device at boot

See the relevant commits in `pico-sdk/` for details.

## Build

Standard out-of-source CMake build:

```bash
cmake -S . -B build
cmake --build build
```

For the Luckfox cross-compilation environment, use:

```bash
./build.sh
```

On macOS Apple Silicon with Colima and Docker CLI, keep the Colima VM on the native `aarch64` architecture and enable Rosetta for `linux/amd64` containers:

```bash
# Install Docker CLI, buildx, and Colima if needed.
brew install docker docker-buildx colima

# Start a native Apple Silicon VM with Rosetta-based amd64 emulation.
colima start --vm-type vz --vz-rosetta

# Verify that buildx is available and Docker can see the builder.
docker buildx version
docker buildx ls

# Cross-compile inside the pinned Luckfox amd64 image.
./build.sh
```

`build.sh` runs `luckfoxtech/luckfox_pico:1.0` with `--platform linux/amd64` and maps your host UID/GID so generated build files remain owned by your user. Avoid starting Colima with `--arch x86_64` for this workflow; use the native `aarch64` VM plus `--platform linux/amd64` for the container instead.

All build artifacts are placed in `build/`:

- `build/lib/` — static libraries
- `build/bin/` — executables
- `build/CMakeFiles/` — CMake metadata and intermediate files

Copy the `build/bin/` executables onto the board and [configure Wi-Fi](https://wiki.luckfox.com/Luckfox-Pico-Zero/WiFi-BT/) before running the HID server demo.

## iOS Configuration

To use HID control on iOS, enable AssistiveTouch on the target device first:

```text
Settings > Accessibility > Touch > AssistiveTouch
```

For a better experience, also enable **Show Onscreen Keyboard** on the
AssistiveTouch page.

## Frame Service

`frame_service` owns `/dev/video0` and exposes raw frames over a Unix domain socket. It samples at 1 fps by default to reduce CPU, memory bandwidth, and heat. Start it before using screenshot tools or `/api/capture`:

```bash
./build/bin/frame_service --socket /tmp/frame_service.sock
```

Use `--fps 0` to capture as fast as the HDMI source produces frames.

Consumers use `FRAME_SERVICE_SOCKET` or `[agent] frame_service_socket` to locate the service. `hid_server` keeps `/api/capture`, but it now reads frames through `FrameServiceClient` instead of opening `/dev/video0` directly.

While `frame_service` is running, it intentionally owns `/dev/video0`. Direct camera examples such as `example_camera_capture` will fail with `Device or resource busy`; use `frame_service_cli` for normal frame/screenshot testing. If you need to run `example_camera_capture` directly, stop the service first and restart it afterward:

```bash
/etc/init.d/S52frame_service stop
./build/bin/example_camera_capture
/etc/init.d/S52frame_service start
```

LLM tools can use the service through the Go agent (see [`src/agent/`](src/agent/README.md)):

- `screenshot` — captures the latest frame, encodes a PNG, and forwards it to the LLM as image input.

Screenshot interpretation requires a model/provider that supports image input. Text-only models can call `screenshot`, but they cannot inspect the pixels.

Debug the service with:

```bash
./build/bin/frame_service_cli --socket /tmp/frame_service.sock health
./build/bin/frame_service_cli --socket /tmp/frame_service.sock screenshot --out /tmp/screenshot.bmp
./build/bin/frame_service_cli --socket /tmp/frame_service.sock latest-frame --out /tmp/frame.raw
./build/bin/frame_service_cli --socket /tmp/frame_service.sock list-frames
./build/bin/frame_service_cli --socket /tmp/frame_service.sock restart
```

## AI Agent

The agent is a Go-based daemon and CLI under [`src/agent/`](src/agent/README.md).
It drives the device's keyboard/mouse/touchscreen, exposes an HTTP tool API,
serves a Web UI, and supports a device-side speech pipeline (VAD/STT/TTS).

See [`src/agent/README.md`](src/agent/README.md) for setup, configuration, and
usage. On the device the agent runs under `/etc/init.d/S53agent` and reads
its config from `/userdata/agent/`.

## Scripts

- `build.sh` — build the project with the cross-compile toolchain
- `scripts/setup_audio_volume.sh` — maximize audio output volume on device
  (see `docs/AUDIO_VOLUME_SETUP.md`)
- `scripts/setup_usb_gadget.sh` — configure the USB gadget for HID emulation
- `scripts/test_audio_roundtrip.sh` — generate audio with the OpenRouter audio
  model and feed it back for recognition
