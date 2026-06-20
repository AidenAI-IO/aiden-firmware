# Hardware & Wiring

## Target Hardware

This project targets the Aiden hardware device. Current main components include:

- [Luckfox Pico Zero](https://wiki.luckfox.com/Luckfox-Pico-Zero): RV1106 / Rockchip platform development board running Buildroot Linux.
- [TC358743XBG](https://toshiba.semicon-storage.com/eu/semiconductor/product/interface-bridge-ics-for-mobile-peripheral-devices/hdmir-interface-bridge-ics/detail.TC358743XBG.html): HDMI to CSI bridge chip for capturing external HDMI video.
- [CH375B](https://easyelecmodule.com/ch375b-u-disk-read-write-module-development-guide/): USB storage-related module, part of the hardware solution.

## Core Connection Topology

The target device (iPhone / PC) connects to Aiden through a USB-C hub. The hub splits the connection into HDMI output (for screen capture) and USB-C output (for data/power to the Pico Zero):

```text
Target Device (iPhone / PC)
          │ USB-C
          ▼
      USB-C Hub
          ├─ HDMI out ──→ TC358743XBG (HDMI to CSI bridge)
          │                      │ CSI cable
          │                      ▼
          └─ USB-C out ──→ Luckfox Pico Zero (CSI input + USB)
                                   │
                                   ├─ Video capture: /dev/video0 (from TC358743 CSI)
                                   ├─ HID keyboard/mouse/touch: /dev/hidg0, /dev/hidg1
                                   ├─ Audio codec / ALSA
                                   └─ Network: Wi-Fi or USB gadget network
```

Physical wiring summary:

1. Target device USB-C → USB-C hub input
2. Hub HDMI output → TC358743XBG HDMI input
3. TC358743XBG CSI output → Pico Zero CSI input (CSI cable between the two boards)
4. Hub USB-C output → Pico Zero USB-C port (data + power)

## iOS HID Prerequisites

To control an iOS device via USB HID, enable on the target iOS device:

```text
Settings > Accessibility > Touch > AssistiveTouch
```

It is also recommended to enable **Show Onscreen Keyboard** on the AssistiveTouch page for a more stable keyboard input experience.

## Default Device Nodes

| Capability | Default node / path | Description |
| --- | --- | --- |
| Video capture | `/dev/video0` | TC358743 output to V4L2 capture device |
| HDMI subdev | `/dev/v4l-subdev2` | EDID / DV timings / HDMI sync |
| Keyboard HID | `/dev/hidg0` | Default keyboard device for Go Agent and example tools |
| Mouse/touch HID | `/dev/hidg1` | Go Agent uses the same HID device for mouse/touch input |
| Frame socket | `/run/frame_service/frame_service.sock` | Default path for system service deployment |
| Audio socket | `/run/audio_service/audio_service.sock` | Default path for system service deployment |

When running the binary directly during development, the default frame socket is `/tmp/frame_service.sock`; after deployment with the firmware startup script, it defaults to `/run/frame_service/frame_service.sock`.
