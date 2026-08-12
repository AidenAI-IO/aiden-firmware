# C++ SDK Reference

`libaiden.a` is the project's C++ hardware SDK, with the main header file:

```cpp
#include "aiden_sdk.h"
```

Key capabilities:

1. GPIO wakeup event;
2. Audio capture;
3. Audio playback;
4. HDMI / camera frame capture;
5. USB HID auxiliary capabilities.

## AudioConfig

```cpp
struct AudioConfig {
    const char* device_name = "hw:0,0";
    int sample_rate = 16000;
    int channels = 1;
    int bit_width = 16;
};
```

## CameraConfig

```cpp
struct CameraConfig {
    const char* device_name = "/dev/video0";
    int width = 1920;
    int height = 1080;
    int camera_id = 0;
    const char* pixel_format = "uyvy";
    const char* subdev_device = "/dev/v4l-subdev2";
    const char* edid_path = nullptr;
    int skip_frames = 1;
    int trigger_retries = 0;
    int trigger_delay_ms = 1000;
    int capture_retries = 2;
    bool enable_hdmi_sync = true;
    bool force_trigger = false;
    bool require_exact_resolution = false;
    bool reject_uniform_frames = true;
};
```

`frame_service` overrides the two timing flags above and requires the negotiated
1920x1080 resolution. The RK628D path avoids forced EDID/HPD retriggering and
retains the driver-provided EDID. The TC358743 path performs one startup
EDID/HPD renegotiation, then queries the settled timings.

## WakeupListener

```cpp
#include "aiden_sdk.h"

void on_wakeup() {
    printf("Wakeup detected!\n");
}

aiden::WakeupListener listener;
listener.start(33, on_wakeup);
// ...
listener.stop();
```

GPIO 33/GPIO 32 are typically used for button or wakeup signals, triggered on falling edge. When you need to listen on both pins simultaneously, create two `WakeupListener` instances and bind them to the same callback.

## AudioCapture

```cpp
aiden::AudioConfig config;
config.sample_rate = 16000;
config.channels = 1;
config.bit_width = 16;

aiden::AudioCapture capture;
capture.init(config);
capture.start([](const aiden::AudioFrame& frame) {
    printf("Got %u bytes\n", frame.length);
});
// ...
capture.stop();
```

## AudioPlayer

```cpp
aiden::AudioConfig config;
config.sample_rate = 16000;
config.channels = 1;
config.bit_width = 16;

aiden::AudioPlayer player;
player.init(config);
player.set_volume(80); // 0..100
player.play(samples, sizeof(samples));
player.stop();
```

## CameraCapture

Streaming capture:

```cpp
aiden::CameraConfig config;
config.width = 1920;
config.height = 1080;
config.pixel_format = "uyvy";

aiden::CameraCapture camera;
camera.init(config);
camera.start([](const aiden::VideoFrame& frame) {
    printf("Frame: %ux%u, %u bytes\n", frame.width, frame.height, frame.length);
});
// ...
camera.stop();
```

One-shot capture and copy to caller-owned buffer:

```cpp
aiden::CameraConfig config;
config.edid_path = nullptr; // Use built-in 1080p60-only CTA EDID

aiden::CameraCapture camera;
aiden::VideoFrame frame;
std::vector<uint8_t> bytes;

if (camera.capture_once(config, frame, bytes)) {
    printf("Frame: %ux%u, %u bytes\n", frame.width, frame.height, frame.length);
}
```

## Dependencies

- Rockchip MPI / Rockit / MPP / RGA / IVE libraries;
- opencv-mobile;
- System libraries such as pthread, m;
- Corresponding header and library paths are configured in `CMakeLists.txt`.

## Notes

- The SDK initializes `RK_MPI_SYS_Init()` on first use;
- Blocking operations support graceful shutdown according to service logic;
- When using `frame_service`, applications should typically not open `/dev/video0` directly.
