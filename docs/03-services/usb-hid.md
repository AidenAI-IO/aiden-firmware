# USB HID and Device Control

The project configures the Luckfox Pico Zero's USB-C port through the Linux USB gadget framework. The firmware startup script creates a composite gadget with keyboard HID, pointer/touch HID, and CDC ECM networking; the HID nodes are used to simulate keyboard, mouse, and touch input.

## Startup Method

In the firmware, the gadget initialization is automatically completed by startup scripts; in the development environment, it can be executed manually:

```bash
sudo ./build/bin/example_usb_hid setup composite
```

You can also use the helper script:

```bash
scripts/setup_usb_gadget.sh
```

This script will:

1. Mount configfs: `/sys/kernel/config`;
2. Load the `dwc2` module;
3. Load the `libcomposite` module;
4. Call `example_usb_hid setup composite`.

On the firmware image, `overlay/etc/init.d/S49usbhid` is the authoritative startup path. It also brings up `usb0` at `192.168.42.1` for the config page and local USB networking.

## example_usb_hid Usage

```text
example_usb_hid [global options] setup <keyboard|touch|composite>
example_usb_hid [global options] cleanup
example_usb_hid [global options] keyboard press <KEY> [KEY ...]
example_usb_hid [global options] keyboard release [KEY ...]
example_usb_hid [global options] keyboard tap <KEY> [KEY ...]
example_usb_hid [global options] keyboard text <TEXT>
example_usb_hid [global options] touch move <X> <Y>
example_usb_hid [global options] touch click <X> <Y> [left|right|middle]
example_usb_hid [global options] touch click [left|right|middle]
example_usb_hid [global options] touch down [left|right|middle]
example_usb_hid [global options] touch up
example_usb_hid [global options] touch scroll <amount>
example_usb_hid [global options] server [PORT]
```

### Server Command

The `server` command starts a simple HTTP server that accepts HID commands via POST requests. This is useful for remote control or integration testing.

```bash
sudo ./build/bin/example_usb_hid server 8090
```

The server listens on the specified port (default 8080 if not specified) and accepts JSON payloads with command specifications.

Common examples:

```bash
sudo ./build/bin/example_usb_hid keyboard tap ENTER
sudo ./build/bin/example_usb_hid keyboard tap CTRL ALT DELETE
sudo ./build/bin/example_usb_hid keyboard text "hello from pico"
sudo ./build/bin/example_usb_hid touch click 16000 16000
sudo ./build/bin/example_usb_hid cleanup
```

## Global Parameters

| Parameter | Description |
| --- | --- |
| `--gadget-root PATH` | configfs gadget root path |
| `--gadget-name NAME` | gadget name |
| `--keyboard-dev PATH` | keyboard HID device |
| `--touch-dev PATH` | touch/mouse HID device |
| `--state-dir PATH` | state file directory |
| `--manufacturer TEXT` | USB manufacturer string |
| `--product-name TEXT` | USB product string |
| `--serial TEXT` | USB serial string |
| `--vendor INT` / `--product-id INT` | USB VID/PID |
| `--udc NAME` | specify UDC |
| `--width INT` / `--height INT` | coordinate space dimensions |
| `--duration-ms INT` | input action duration |
| `--force` | force certain operations |

## Agent HID Configuration

```toml
[hid]
keyboard_device = "/dev/hidg0"
mouse_device = "/dev/hidg1"
android_keyboard_device = "/dev/hidg2"
frame_socket = "/run/frame_service/frame_service.sock"
```

The firmware also binds `hid.usb2` as `/dev/hidg2`. This second keyboard-like
interface advertises Consumer Control usages. In `pointer_mode = "touchscreen"`
(Android mode), it is used for Android extension keys such as Back, Home, App
Switch, Search, Power, and Volume. In `pointer_mode = "absolute"` (iOS cursor
mode), it is exposed as a smaller bitmap media-key interface that advertises
only volume mute/up/down, media playback controls, screenshot, and brightness
up/down.

Built-in Agent tools:

- `keyboard_tap`
- `keyboard_text`
- `mouse_click`
- `mouse_move`
- `mouse_scroll`
- `touch_gesture`

It is recommended to use normalized coordinates (`0..1000`, with center at `500,500`) to avoid click position shifts due to display resolution changes.
For dense targets such as small buttons, list items, and input boxes, prioritize estimating the normalized coordinates of the target center; only explicitly pass `coord_space: "pixel"` when the screenshot pixel coordinates and HID touch coordinates are already calibrated. After successful input tool execution, a post-action screenshot is returned; screen changes should be confirmed before proceeding to avoid duplicate clicks.
`keyboard_text` simulates a US keyboard and can only input ASCII typeable characters; Chinese input should be completed through pinyin/English search terms and on-screen candidates, and Chinese character strings cannot be passed directly to the tool.

## iOS AssistiveTouch modifier isolation

On iOS, AssistiveTouch can make modifier routing unstable when an external
keyboard and pointer are advertised by the same USB composite. Plain key input
may work while shortcuts such as `Cmd+A` or `Cmd+V` are ignored.

When `hid.pointer_mode = "absolute"`, firmware builds that include
`/oem/usr/bin/aiden-dynamic-keyboard` automatically isolate keyboard actions
whose HID reports contain Ctrl, Shift, Option/Alt, or Cmd/Meta. This includes
`keyboard_text` values containing uppercase letters or symbols that require
Shift on a US keyboard. Plain key taps, unmodified text, pointer input, and
Consumer Control continue to use the normal keyboard + pointer + Consumer
Control + ECM composite.

Immediately before a modifier-bearing keyboard action, the Agent switches the
single USB gadget to a pointer-free keyboard + Consumer Control + ECM profile.
After the action, including error and cancellation paths, it restores the normal
composite. The isolated profile uses a distinct USB product ID and serial number
so iOS does not reuse the pointer-bearing descriptor for the input.

The Luckfox board has one UDC, so isolation and restore briefly disconnect the
complete composite, including ECM. The dynamic controller and ECM watchdog
share `/run/aiden_dynamic_keyboard.lock` to prevent overlapping UDC resets.
Inspect or exercise the profiles manually with:

```bash
/oem/usr/bin/aiden-dynamic-keyboard status
/oem/usr/bin/aiden-dynamic-keyboard isolate
/oem/usr/bin/aiden-dynamic-keyboard restore
```

After any test, `status` should report `mode=normal` and list `hid.usb0`,
`hid.usb1`, `hid.usb2`, and `ecm.usb0`. Use HDMI visual feedback to verify the
shortcut effect; a successful `/dev/hidg0` write only proves that the gadget
accepted the HID report.
