# 硬件与连线

## 目标硬件

本项目面向 Aiden 硬件演示设备，当前主要组件包括：

- [Luckfox Pico Zero](https://wiki.luckfox.com/Luckfox-Pico-Zero)：RV1106 / Rockchip 平台开发板，运行 Buildroot Linux。
- [TC358743XBG](https://toshiba.semicon-storage.com/eu/semiconductor/product/interface-bridge-ics-for-mobile-peripheral-devices/hdmir-interface-bridge-ics/detail.TC358743XBG.html)：HDMI 转 CSI 桥接芯片，用于采集外部 HDMI 画面。
- [CH375B](https://easyelecmodule.com/ch375b-u-disk-read-write-module-development-guide/)：USB 存储相关模块，作为硬件方案组成部分。

## 核心连接关系

```text
HDMI Source / iPhone / Host UI
          │ HDMI
          ▼
     TC358743XBG
          │ CSI / V4L2: /dev/video0 + /dev/v4l-subdev2
          ▼
   Luckfox Pico Zero ─── USB-C gadget ─── Target Host / iOS / PC
          │
          ├─ HID keyboard / mouse / touch: /dev/hidg0, /dev/hidg1
          ├─ Audio codec / ALSA
          └─ Network: Wi-Fi or USB gadget network
```

## iOS HID 使用前置设置

如果要通过 USB HID 控制 iOS 设备，需要在目标 iOS 设备开启：

```text
Settings > Accessibility > Touch > AssistiveTouch
```

建议同时在 AssistiveTouch 页面启用 **Show Onscreen Keyboard**，以便键盘输入体验更稳定。

## 默认设备节点

| 能力 | 默认节点 / 路径 | 说明 |
| --- | --- | --- |
| 视频采集 | `/dev/video0` | TC358743 输出到 V4L2 capture device |
| HDMI subdev | `/dev/v4l-subdev2` | EDID / DV timings / HDMI sync |
| 键盘 HID | `/dev/hidg0` | Go Agent 和示例工具默认键盘设备 |
| 鼠标/触控 HID | `/dev/hidg1` | Go Agent 使用同一 HID 设备完成鼠标/触控输入 |
| Frame socket | `/run/frame_service/frame_service.sock` | 系统服务部署默认路径 |
| Audio socket | `/run/audio_service/audio_service.sock` | 系统服务部署默认路径 |

开发时直接运行二进制的默认 frame socket 是 `/tmp/frame_service.sock`；随固件启动脚本部署后默认使用 `/run/frame_service/frame_service.sock`。
