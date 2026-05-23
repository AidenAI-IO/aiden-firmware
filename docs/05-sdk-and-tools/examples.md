# 示例程序

CMake 会构建多个 `example_*` 可执行文件，用于验证 SDK 和设备能力。

## Wakeup

```bash
./build/bin/example_wakeup
```

监听 GPIO 33 falling edge。典型硬件连接：按钮连接 GPIO 33 与 GND，内部上拉。

## Audio Capture

```bash
./build/bin/example_audio_capture [device_name]
```

采集音频并打印 frame 信息。可选传入 ALSA device name。

## Audio Playback

```bash
./build/bin/example_audio_play [device_name]
```

播放 440Hz 测试音约 2 秒。

## Camera Capture

```bash
./build/bin/example_camera_capture
./build/bin/example_camera_capture --output /mnt/tmp/frame.raw
./build/bin/example_camera_capture --edid /mnt/tmp/hdmi_1080p30_cta.hex
```

常用参数：

```text
example_camera_capture [device_path] [width] [height] [pixfmt] [skip_frames] [options]
```

支持 `--device`、`--subdev`、`--edid`、`--force-trigger`、`--capture-retries`、`--output` 等选项，详见 `--help`。

默认 one-shot 流程会：

- 先查询当前 DV timings；
- 必要时推送内置 1080p30-only CTA EDID；
- 等待 `/dev/v4l-subdev2` HDMI sync；
- stream-on 后丢弃过渡帧；
- 通过 V4L2 MMAP 从 `/dev/video0` 捕获；
- 默认保存可查看的 `/mnt/tmp/frame.ppm`。

> 若 `frame_service` 正在运行，需要先停止它，否则 `/dev/video0` 会被占用。

## USB HID

```bash
sudo ./build/bin/example_usb_hid setup composite
sudo ./build/bin/example_usb_hid keyboard tap ENTER
sudo ./build/bin/example_usb_hid keyboard text "hello from pico"
sudo ./build/bin/example_usb_hid touch click 16000 16000
sudo ./build/bin/example_usb_hid cleanup
```

更多信息见 [USB HID 与设备控制](../03-services/usb-hid.md)。

## 其他工具

| 程序 | 说明 |
| --- | --- |
| `hello` | 基础 C 程序验证 |
| `trigger` | GPIO / 触发相关小工具 |
| `audio_stream` | 音频流示例工具 |
| `config_web` | 配置网页服务 |
| `frame_service_cli` | Frame Service 调试 CLI |
| `audio_service_cli` | Audio Service 调试 CLI |
