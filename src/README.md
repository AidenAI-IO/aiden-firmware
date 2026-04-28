# Aiden SDK

C++ development SDK for Luckfox Pico Zero providing high-level APIs for:
1. Wakeup event listening (GPIO-based)
2. Audio capture (recording)
3. Audio playback
4. Camera frame capture (VI)

## Architecture

The SDK wraps the Rockchip Media Process Interface (MPI) APIs with a clean C++ interface:

- **WakeupListener**: GPIO polling for hardware wakeup events
- **AudioCapture**: Audio input using RK_MPI_AI (Audio Input)
- **AudioPlayer**: Audio output using RK_MPI_AO (Audio Output)
- **CameraCapture**: Video input using RK_MPI_VI (Video Input)

## Building

Standard out-of-source CMake build:

```bash
cmake -S . -B build
cmake --build build
```

For the Luckfox cross-compilation environment, use:

```bash
./build.sh
```

This builds:
- `build/lib/libaiden.a` - Static library
- `build/bin/example_wakeup` - Wakeup event demo
- `build/bin/example_audio_capture` - Audio recording demo
- `build/bin/example_audio_play` - Audio playback demo
- `build/bin/example_camera_capture` - Camera frame capture demo

All build artifacts are placed in the `build/` directory to keep the source tree clean.

## API Reference

### WakeupListener

Listen for GPIO falling edge events (e.g., button press):

```cpp
#include "aiden_sdk.h"

void on_wakeup() {
    printf("Wakeup detected!\n");
}

aiden::WakeupListener listener;
listener.start(33, on_wakeup);  // GPIO 33
// ... do work ...
listener.stop();
```

### AudioCapture

Capture audio from microphone:

```cpp
#include "aiden_sdk.h"

void on_audio(const aiden::AudioFrame& frame) {
    // Process audio data
    printf("Got %u bytes\n", frame.length);
}

aiden::AudioConfig config;
config.sample_rate = 16000;
config.channels = 2;
config.bit_width = 16;

aiden::AudioCapture capture;
capture.init(config);
capture.start(on_audio);
// ... audio streams to callback ...
capture.stop();
```

### AudioPlayer

Play audio to speaker:

```cpp
#include "aiden_sdk.h"

aiden::AudioConfig config;
config.sample_rate = 16000;
config.channels = 1;
config.bit_width = 16;

aiden::AudioPlayer player;
player.init(config);

// Play raw PCM data
short samples[1024];
// ... fill samples ...
player.play(samples, sizeof(samples));

player.set_volume(-10);  // -60 to 0 dB
player.stop();
```

### CameraCapture

Capture video frames from camera:

```cpp
#include "aiden_sdk.h"

void on_video(const aiden::VideoFrame& frame) {
    // Process video frame
    printf("Frame: %ux%u, %u bytes\n", 
           frame.width, frame.height, frame.length);
}

aiden::CameraConfig config;
config.width = 1920;
config.height = 1080;
config.camera_id = 0;
config.pixel_format = "nv12";

aiden::CameraCapture camera;
camera.init(config);
camera.start(on_video);
// ... video streams to callback ...
camera.stop();
```

## Configuration

### AudioConfig

```cpp
struct AudioConfig {
    const char* device_name;  // ALSA device, e.g., "default:CARD=rockchipes8388"
    int sample_rate;          // 8000, 16000, 44100, 48000, etc.
    int channels;             // 1 (mono) or 2 (stereo)
    int bit_width;            // 8, 16, or 24
};
```

### CameraConfig

```cpp
struct CameraConfig {
    const char* device_name;  // V4L2 device, e.g., "/dev/video0"
    int width;                // Frame width (default: 1920)
    int height;               // Frame height (default: 1080)
    int camera_id;            // Camera ID (default: 0)
    const char* pixel_format; // "nv12", "nv16", "uyvy", "yuyv"
};
```

## Examples

### Wakeup Event

```bash
./build/bin/example_wakeup
```

Listens for falling edge on GPIO 33 (Pin 3).

### Audio Capture

```bash
./build/bin/example_audio_capture [device_name]
```

Captures audio and prints frame info. Optional device name parameter.

### Audio Playback

```bash
./build/bin/example_audio_play [device_name]
```

Plays a 440Hz tone for 2 seconds.

### Camera Capture

```bash
./build/bin/example_camera_capture [device_name]
```

Captures video frames and prints frame info. Optional device name parameter.

## Hardware Setup

### GPIO Wakeup
- Connect button between GPIO 33 (Pin 3) and GND
- Internal pull-up enabled
- Triggers on falling edge

### Audio
- Luckfox Pico Zero uses I2S audio codec
- Default ALSA device: `default:CARD=rockchipes8388`
- Supports 8kHz - 48kHz sample rates
- 8/16/24-bit audio

### Camera
- Luckfox Pico Zero supports MIPI CSI camera input
- Default resolution: 1920x1080
- Supported formats: NV12, NV16, UYVY, YUYV
- Uses Rockchip ISP for image processing

## Dependencies

- Rockchip MPI library (`librockchip_mpi`)
- Located in `aiden-sdk/media/out/lib/`
- Headers in `aiden-sdk/media/out/include/`

## Notes

- The SDK automatically initializes `RK_MPI_SYS_Init()` on first use
- Reference counting ensures proper cleanup
- All blocking operations support graceful shutdown
- Thread-safe initialization and cleanup
