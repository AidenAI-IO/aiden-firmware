# C++ SDK 参考

`libaiden.a` 是项目的 C++ 硬件 SDK，入口头文件为：

```cpp
#include "aiden_sdk.h"
```

主要能力：

1. GPIO wakeup event；
2. 音频采集；
3. 音频播放；
4. HDMI / camera frame capture；
5. USB HID 辅助能力。

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

GPIO 33/GPIO 32 通常用于按钮或唤醒信号，falling edge 触发。需要同时监听两路时，可创建两个 `WakeupListener`，并绑定同一个回调。

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

流式捕获：

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

一次性捕获并复制到 caller-owned buffer：

```cpp
aiden::CameraConfig config;
config.edid_path = nullptr; // 使用内置 1080p30-only CTA EDID

aiden::CameraCapture camera;
aiden::VideoFrame frame;
std::vector<uint8_t> bytes;

if (camera.capture_once(config, frame, bytes)) {
    printf("Frame: %ux%u, %u bytes\n", frame.width, frame.height, frame.length);
}
```

## 依赖

- Rockchip MPI / Rockit / MPP / RGA / IVE 库；
- opencv-mobile；
- pthread、m 等系统库；
- 对应头文件和库路径在 `CMakeLists.txt` 中配置。

## 注意事项

- SDK 会在首次使用时初始化 `RK_MPI_SYS_Init()`；
- 阻塞操作支持按服务逻辑优雅停止；
- 使用 `frame_service` 后，业务通常不应直接打开 `/dev/video0`。
