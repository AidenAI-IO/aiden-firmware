# Aiden DEMO

## Hardware

- [Luckfox Pico Zero](https://wiki.luckfox.com/Luckfox-Pico-Zero)
- [TC358743XBG](https://toshiba.semicon-storage.com/eu/semiconductor/product/interface-bridge-ics-for-mobile-peripheral-devices/hdmir-interface-bridge-ics/detail.TC358743XBG.html)
- [CH375B](https://easyelecmodule.com/ch375b-u-disk-read-write-module-development-guide/)

## Init

```bash
git clone --recursive git@github.com:AidenAI-IO/aiden-hardware-demo.git
cd aiden-hardware-demo
brew install git-lfs
git lfs install
git lfs pull
```

## Flash Image

```bash
# Hold the boot button while plugging the board in, or run `reboot loader`
# on the device to enter maskrom mode.
./upgrade_tool/upgrade_tool uf ./image/update.img
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

## Frame Service

`frame_service` owns `/dev/video0` and exposes raw frames over a Unix domain socket. It samples at 1 fps by default to reduce CPU, memory bandwidth, and heat. Start it before using screenshot tools or `/api/capture`:

```bash
./build/bin/frame_service --socket /tmp/frame_service.sock
```

Use `--fps 0` to capture as fast as the HDMI source produces frames.

Consumers use `FRAME_SERVICE_SOCKET` or `[agent] frame_service_socket` to locate the service. `hid_server` keeps `/api/capture`, but it now reads frames through `FrameServiceClient` instead of opening `/dev/video0` directly.

LLM tools can use the service through `agent_main`:

- `capture_screenshot` — captures the latest frame and returns metadata plus a local image path. It writes PNG by default, limits the output image to `max_edge=960`, and forwards the PNG to the LLM as `image_url` input. Pass `format=bmp` for BMP output or `max_edge=0` for full resolution.
- `frame_service_health` — returns capture state, latest frame sequence, ring usage, recovery, and latency metrics.
- `frame_service_restart` — requests a capture-path restart if frames appear stale or unhealthy.

Screenshot interpretation requires a model/provider that supports image input. Text-only models can call `capture_screenshot`, but they cannot inspect the pixels sent as `image_url`.

Debug the service with:

```bash
./build/bin/frame_service_cli --socket /tmp/frame_service.sock health
./build/bin/frame_service_cli --socket /tmp/frame_service.sock screenshot --out /tmp/screenshot.bmp
./build/bin/frame_service_cli --socket /tmp/frame_service.sock latest-frame --out /tmp/frame.raw
./build/bin/frame_service_cli --socket /tmp/frame_service.sock list-frames
./build/bin/frame_service_cli --socket /tmp/frame_service.sock restart
```

## AI Agent

Pure C++ AI agent that runs directly on the Pico Zero and drives the device's
keyboard and touchscreen via voice.

### Features

- **GPIO wakeup trigger**: waits for a wakeup event on GPIO 33 before recording
  (no always-on listening)
- **Real-time audio capture**: captures 16 kHz / 16-bit / mono audio from the
  onboard microphone
- **VAD segmentation**: energy-based voice activity detection ends an utterance
  on silence
- **LLM processing**: calls an audio-capable model through OpenRouter
- **Streaming TTS**: MiniMax streaming synthesis — decode and play as bytes
  arrive, for low latency
- **Tool calls**: keyboard input, touchscreen control, and HDMI screenshot capture
- **Debug logging**: detailed HTTP request/response logs

### Setup

1. Make sure `curl` is installed on the device.
2. Copy the config template and fill in the API keys:
   ```bash
   cp agent.conf.example agent.conf
   vi agent.conf
   ```

### Usage

```bash
# GPIO wakeup mode (default, production)
sudo ./build/bin/agent_main
sudo ./build/bin/agent_main --mode=wakeup

# Manual trigger mode (press Enter to start/stop recording, for debugging)
sudo ./build/bin/agent_main --mode=manual
# First Enter: start recording
# Second Enter: stop immediately and send what has been recorded (bypasses VAD)

# Text input mode (reads a line from stdin as the instruction, no recording)
sudo ./build/bin/agent_main --mode=text
# Type text at the `> ` prompt and press Enter to submit.
# Empty line or Ctrl+C to exit.
# Note: text mode requires a non-audio model (e.g., gpt-4o or gpt-4o-mini);
#       gpt-4o-audio-preview is not compatible.

# Specify a config file
sudo ./build/bin/agent_main --mode=manual /etc/agent.conf
```

### Config file (`agent.conf`)

```ini
[model]
provider = openrouter
api_key = sk-or-v1-...
model = openai/gpt-4o-audio-preview

[tts]
provider = minimax
api_key = YOUR_MINIMAX_API_KEY
model = speech-2.8-hd
voice_id = male-qn-qingse
emotion = happy
speed = 1.0

[agent]
hid_binary = ./build/bin/example_usb_hid
frame_service_socket = /tmp/frame_service.sock
energy_threshold = 300   # speech energy threshold
silence_ms = 800         # silence duration that ends an utterance
min_speech_ms = 300      # minimum utterance length
```

### How it works

1. **Wait for wakeup**: the agent idles on a GPIO 33 wakeup event (no CPU
   polling).
2. **Start recording**: on wakeup, `AudioCapture` opens the input stream.
3. **VAD segmentation**: when silence exceeds the configured duration
   (default 800 ms), the utterance is considered complete.
4. **Send to LLM**: the audio is wrapped as WAV, base64-encoded, and posted to
   OpenRouter via curl.
5. **Tool calls**: the LLM can invoke tools to drive the device:
    - `keyboard_tap` — key combos (e.g., ENTER, CTRL+C)
    - `keyboard_text` — type text
    - `touch_click` — click at an absolute coordinate (0..32767)
    - `touch_swipe` — swipe gesture
    - `capture_screenshot` — capture the latest HDMI frame, write a PNG by default, and attach it to the next LLM turn as image input
    - `frame_service_health` — inspect frame capture health and latency metrics
    - `frame_service_restart` — request capture recovery when frames appear stale
6. **Streaming TTS playback**: the LLM reply is sent to MiniMax TTS:
   - MP3 chunks arrive as a hex-encoded stream
   - chunks are piped into ffmpeg and decoded to PCM
   - PCM is played back while the rest of the stream is still arriving
7. **Return to idle**: once playback finishes, the agent goes back to waiting
   for the next wakeup event.

### Debug output

The agent prints verbose logs tagged by subsystem:

- `[wakeup]` — GPIO trigger events
- `[listen]` — recording started
- `[utterance]` — captured utterance duration
- `[debug]` — WAV size and other diagnostics
- `[http]` — HTTP request/response details
- `[llm]` — LLM request status
- `[tools]` — tool invocations and their results
- `[tts]` — TTS synthesis status
- `[error]` — errors

USB HID setup is required (see `src/README.md`).

## Scripts

- `build.sh` — build the project with the cross-compile toolchain
- `scripts/setup_audio_volume.sh` — maximize audio output volume on device
  (see `docs/AUDIO_VOLUME_SETUP.md`)
- `scripts/setup_usb_gadget.sh` — configure the USB gadget for HID emulation
- `scripts/test_audio_roundtrip.sh` — generate audio with the OpenRouter audio
  model and feed it back for recognition
