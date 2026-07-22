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

## Experimental on-demand keyboard profile

The on-demand profile is intended for the iOS AssistiveTouch experiment where
the software keyboard must remain available while Aiden is idle. Enable it with:

```toml
[hid]
pointer_mode = "absolute"
input_backend = "hid"
dynamic_keyboard = true
dynamic_keyboard_control = "/oem/usr/bin/aiden-dynamic-keyboard"
```

At boot, this profile exposes only the absolute mouse and ECM functions. It
does not link the standard keyboard or Consumer Control function into the
active USB configuration. A keyboard operation switches to an isolated
keyboard + ECM profile; the AssistiveTouch pointer is deliberately absent so
iOS cannot route modifier chords through the pointer session. The mouse profile
is restored immediately when that keyboard tool or high-level input transaction
finishes, fails, or is canceled; it is not held down for the whole conversational
Agent run. A high-level text-entry transaction may switch back to mouse + ECM
for a focus click or paste-menu fallback, then return to keyboard + ECM for the
next keystroke. Both experimental profiles omit
`hid.usb2`, so media and Android extension keys are not host-visible.

The Luckfox board has one UDC, so this is a full gadget profile switch rather
than an independent pointer-interface hotplug. The USB cable stays connected,
but the whole composite gadget briefly disconnects and re-enumerates on each
transition; therefore iOS may still log a hardware-keyboard detach/attach even
though removing the pointer is the behavioral goal. The two profiles use
different product IDs and serial numbers to prevent iOS from reusing a cached
descriptor from the other profile.
Use `pointer_mode = "absolute"` for iOS AssistiveTouch; touchscreen Digitizer
reports are intended for Android and are not accepted as taps by the tested iOS
device. Dynamic keyboard mode requires `input_backend = "hid"`.

Inspect or switch the profile manually on the board:

```bash
/oem/usr/bin/aiden-dynamic-keyboard status
/oem/usr/bin/aiden-dynamic-keyboard on
/oem/usr/bin/aiden-dynamic-keyboard off
```

Because ECM disappears during each switch, run the first end-to-end Agent test
through the board-local HTTP endpoint instead of from the Mac:

```bash
curl -X POST http://127.0.0.1:8080/api/tools/keyboard_text \
  -H 'Content-Type: application/json' \
  -d '{"input":{"text":"Aiden123"}}'
```

After the call, `status` should report `mode=off`, the linked functions should
be `hid.usb1` and `ecm.usb0`, and refocusing an iOS text field should show the
software keyboard again. Validate mouse clicks and ECM recovery separately. A
successful HTTP tool result confirms the board transaction, but HDMI or direct
iOS observation is still required to confirm that all characters arrived.

When deploying the experiment manually, copy the startup-script backup outside
`/etc/init.d` (for example `/root/S49usbhid.backup`). Buildroot executes files
matching `/etc/init.d/S??*`; a backup left there can silently run at boot and
restore the old keyboard-present profile.

## iOS shortcut paste prerequisite

On iOS, HID text input and HID keyboard shortcuts are separate failure domains. `keyboard_text` can successfully type ASCII while shortcuts such as paste (`keyboard_tap` with `["meta","v"]` / `Cmd+V`) still have no UI effect.

The keyboard HID function is advertised as a boot keyboard (`protocol=1`, `subclass=1`) for broad host compatibility. If iOS retains a stale external-keyboard session, it may accept plain keycodes while ignoring modifier chords; symptoms include `Shift+1` producing `1` instead of `!`, and `Cmd+A` / `Cmd+V` doing nothing. Force a clean composite gadget re-enumeration before chasing HID timing or key mappings, but do it as an explicit recovery step rather than after every healthy host configuration event.

For stale iOS HID sessions, capture the current watchdog snapshot first and then run the manual refresh command:

```bash
/etc/init.d/S60usb_ecm_watchdog snapshot
/etc/init.d/S60usb_ecm_watchdog refresh
```

The watchdog intentionally preserves normal `configured` transitions. ECM ARP probe failures are also diagnostic-only because a transient or unsupported ARP response is not proof that the USB HID session is stale. Automatically refreshing the composite gadget can leave `/dev/hidg0` or `/dev/hidg1` present but unopenable until the phone fully re-enumerates the interfaces. Use the explicit `refresh` command only as incident recovery; if HID nodes remain unopenable afterward, physically reconnect or power-cycle the device before continuing HID input.

Before validating shortcut-based paste on iPhone or iPad, turn on:

```text
Settings > Accessibility > Keyboards & Typing > Full Keyboard Access
```

On some iOS versions this path may be shown as `Settings > Accessibility > Keyboards > Full Keyboard Access`.

Use HDMI visual feedback after the shortcut. A successful `/dev/hidg0` write only means the HID report was accepted by the device node; it does not prove that iOS executed the paste action. If clipboard content is known to be present and plain typing works but `meta+v` / `rmeta+v` does nothing, check Full Keyboard Access first before changing HID timing or retrying the same shortcut path.
