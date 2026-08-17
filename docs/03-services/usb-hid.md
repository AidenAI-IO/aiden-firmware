---
sidebar_position: 7
---

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

On the firmware image, `overlay/etc/init.d/S49usbhid` is the authoritative startup path. It also brings up `usb0` at `192.168.42.1` for the config page and local USB networking. The completed keyboard + pointer + Consumer Control + ECM composite is bound to the UDC exactly once. Do not add a same-identity startup unbind/rebind: while the cable remains attached, iOS can retain the physical USB session but rebuild the HID interfaces with inconsistent external-keyboard and AssistiveTouch pointer state, leaving the cursor operational while the on-screen keyboard stays suppressed. A physical unplug/replug fully tears down that host session, which is why it can recover the symptom.

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

| Parameter                           | Description                 |
| ----------------------------------- | --------------------------- |
| `--gadget-root PATH`                | configfs gadget root path   |
| `--gadget-name NAME`                | gadget name                 |
| `--keyboard-dev PATH`               | keyboard HID device         |
| `--touch-dev PATH`                  | touch/mouse HID device      |
| `--state-dir PATH`                  | state file directory        |
| `--manufacturer TEXT`               | USB manufacturer string     |
| `--product-name TEXT`               | USB product string          |
| `--serial TEXT`                     | USB serial string           |
| `--vendor INT` / `--product-id INT` | USB VID/PID                 |
| `--udc NAME`                        | specify UDC                 |
| `--width INT` / `--height INT`      | coordinate space dimensions |
| `--duration-ms INT`                 | input action duration       |
| `--force`                           | force certain operations    |

## Agent HID Configuration

```toml
[device]
device_type = "iOS"

[hid]
keyboard_device = "/dev/hidg0"
keyboard_layout = "qwerty"
mouse_device = "/dev/hidg1"
android_keyboard_device = "/dev/hidg2"
touchscreen_device = "/dev/hidg3"
frame_socket = "/run/frame_service/frame_service.sock"
```

`mouse_device` is always an absolute mouse. On Android, the firmware also binds
`hid.usb3` as `touchscreen_device`, so the host sees both a real mouse with a
visible pointer and a touchscreen at the same time. `touch_gesture` keeps using
the touchscreen by default; pass `"input_device":"mouse"` to send the gesture
as mouse movement/button reports instead.

The firmware also binds `hid.usb2` as `/dev/hidg2`. This second keyboard-like
interface advertises Consumer Control usages. When `[device].device_type =
"Android"` derives `pointer_mode = "touchscreen"`, it is used for Android
extension keys such as Back, Home, App Switch, Search, Power, and Volume. Other
device types derive `pointer_mode = "absolute"` and expose a smaller bitmap
media-key interface that advertises only volume mute/up/down, media playback
controls, screenshot, and brightness up/down.

Built-in Agent tools:

- `keyboard_tap`
- `keyboard_text`
- `mouse_move`
- `mouse_scroll`
- `touch_gesture`

Android mouse click example:

```json
{"type":"tap","point":{"x":500,"y":500},"input_device":"mouse"}
```

It is recommended to use normalized coordinates (`0..1000`, with center at `500,500`) to avoid click position shifts due to display resolution changes.
For dense targets such as small buttons, list items, and input boxes, prioritize estimating the normalized coordinates of the target center. After successful input tool execution, a post-action screenshot is returned; screen changes should be confirmed before proceeding to avoid duplicate clicks.
`keyboard_layout` must match how the phone interprets the external USB HID keyboard. Supported values are `qwerty` (default), `azerty`, and `qwertz`. The visible soft-keyboard layout is not authoritative: a phone can display an AZERTY soft keyboard while still interpreting Aiden's USB HID reports as QWERTY. Both `keyboard_text` and standard text-like keys in `keyboard_tap` use this mapping. The mapping itself is loaded by the Agent and does not change USB descriptors, but Config Web requires a board restart after saving so the host starts a clean USB session.

Select the matching layout under `[hid]` in Config Web (`qwerty`, `azerty`, or `qwertz`). Most users keep the default `qwerty`; only change it if typed characters come out transposed (for example "shape" becomes "shqpe").

A phone running iOS locks its hardware-keyboard layout at the moment the USB keyboard is enumerated, based on the software keyboard that is active at that instant. Switching the on-screen keyboard afterwards does not change the locked interpretation. To align a non-QWERTY layout:

1. On the phone, switch the input language to the matching one (for example French for `azerty`, German for `qwertz`).
2. Set `keyboard_layout` in Config Web to the target layout and save.
3. Accept the Config Web reboot prompt and wait for the board and USB composite to reconnect. The phone then locks the hardware layout based on the language active in step 1.

The order matters: switch the language _before_ saving the configuration, because the layout is locked during enumeration. The Config Web save flow does not hot-reload or soft re-enumerate the same USB identity: iOS can retain an inconsistent keyboard and pointer session across that transition. If immediate reboot is cancelled, reboot the board manually before verifying the new layout.

`keyboard_text` can only input ASCII typeable characters. For Chinese or other non-ASCII text, use `enter_text` (which leverages the phone's on-screen keyboard and IME candidates) instead of transliterating to pinyin or romanized approximations. The configured layouts cover common ASCII keys, but country-specific punctuation variants may still require device verification.

## iOS AssistiveTouch modifier isolation

On iOS, AssistiveTouch can make modifier routing unstable when an external
keyboard and pointer are advertised by the same USB composite. Plain key input
may work while shortcuts such as `Cmd+A` or `Cmd+V` are ignored.

When `[device].device_type` derives `pointer_mode = "absolute"`, firmware builds that include
`/oem/usr/bin/aiden-dynamic-keyboard` automatically isolate keyboard actions
whose HID reports contain Ctrl, Shift, Option/Alt, or Cmd/Meta. This includes
`keyboard_text` values containing uppercase letters or symbols that require
Shift or AltGr on the configured keyboard layout. Plain key taps, unmodified text, pointer input, and
Consumer Control continue to use the normal keyboard + pointer + Consumer
Control + ECM composite.

Immediately before a modifier-bearing keyboard action, the Agent switches the
single USB gadget to a pointer-free keyboard + Consumer Control + ECM profile.
After the action, including error and cancellation paths, it restores the normal
composite. The isolated profile uses a distinct USB product ID and serial number
so iOS does not reuse the pointer-bearing descriptor for the input.

The normal composite must enumerate the pointer as the last HID interface:
`keyboard -> Consumer Control -> pointer -> ECM`. iOS builds its keyboard and
AssistiveTouch pointer subsystem state in interface order; a pointer that is
enumerated immediately after the keyboard leaves the on-screen keyboard policy
subject to an async race, so after an isolate/restore cycle the soft keyboard
only reappears about 80% of the time. With the Consumer Control interface between
them, the keyboard subsystem settles first and the pointer registers last, which
closes that race: field tests showed 10/10 successful soft-keyboard restores.
The pointer must stay before ECM: putting a HID interface after the ECM IAD
causes continuous USB reset loops on iOS.

One conversational Agent run owns a shared isolation scope. The first
modifier-bearing keyboard operation removes the pointer, and later keyboard or
Consumer Control operations reuse that profile without another enumeration. A
mouse, touch, scroll, or wheel operation restores the normal profile only when
the pointer is currently absent. If a later keyboard operation needs a modifier,
the run isolates again. Success, failure, cancellation, preemption, panic
cleanup, and normal termination all leave the scope through a context-independent
final restore.

Direct HTTP Tool API calls use the same rules within one invocation, but do not
share isolation state across separate requests. A modifier-bearing HTTP call
therefore isolates and restores once unless that invocation needs pointer input
before it completes.

When `search_launch_app` omits `platform`, an active iOS HID isolation
controller is also treated as an iOS platform signal. This keeps the documented
`{"app":"WeChat"}` input on the `Cmd+Space` Spotlight path even while Phone
Bridge is unavailable or backgrounded.

The Luckfox board has one UDC, so isolation and restore briefly disconnect the
complete composite, including ECM. The dynamic controller and ECM watchdog
share `/run/aiden_dynamic_keyboard.lock` to prevent overlapping UDC resets. The
controller also publishes a short post-switch grace deadline so the watchdog
does not count the planned ECM interruption toward its stall threshold. Normal
watchdog probes resume after the grace window and still recover persistent ECM
failures. HID node open recovery also honors this deadline: a transient
`/dev/hidg*` `ENXIO` waits and retries instead of forcing another composite
refresh during the planned switch.

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
