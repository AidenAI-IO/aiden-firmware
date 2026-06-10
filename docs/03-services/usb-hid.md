# USB HID 与设备控制

项目通过 Linux USB gadget framework 将 Luckfox Pico Zero 的 USB-C 口配置为 HID 设备，可模拟键盘、鼠标/触控输入。

## 启动方式

固件中由启动脚本自动完成 gadget 初始化；开发环境可以手动执行：

```bash
sudo ./build/bin/example_usb_hid setup composite
```

也可以使用辅助脚本：

```bash
scripts/setup_usb_gadget.sh
```

该脚本会：

1. 挂载 configfs：`/sys/kernel/config`；
2. 加载 `dwc2` 模块；
3. 加载 `libcomposite` 模块；
4. 调用 `example_usb_hid setup composite`。

## example_usb_hid 用法

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

常用示例：

```bash
sudo ./build/bin/example_usb_hid keyboard tap ENTER
sudo ./build/bin/example_usb_hid keyboard tap CTRL ALT DELETE
sudo ./build/bin/example_usb_hid keyboard text "hello from pico"
sudo ./build/bin/example_usb_hid touch click 16000 16000
sudo ./build/bin/example_usb_hid cleanup
```

## 全局参数

| 参数 | 说明 |
| --- | --- |
| `--gadget-root PATH` | configfs gadget 根路径 |
| `--gadget-name NAME` | gadget 名称 |
| `--keyboard-dev PATH` | 键盘 HID device |
| `--touch-dev PATH` | 触控/鼠标 HID device |
| `--state-dir PATH` | 状态文件目录 |
| `--manufacturer TEXT` | USB manufacturer string |
| `--product-name TEXT` | USB product string |
| `--serial TEXT` | USB serial string |
| `--vendor INT` / `--product-id INT` | USB VID/PID |
| `--udc NAME` | 指定 UDC |
| `--width INT` / `--height INT` | 坐标空间尺寸 |
| `--duration-ms INT` | 输入动作持续时间 |
| `--force` | 强制执行某些操作 |

## Agent HID 配置

```toml
[hid]
keyboard_device = "/dev/hidg0"
mouse_device = "/dev/hidg1"
frame_socket = "/run/frame_service/frame_service.sock"
```

Agent 内置工具：

- `keyboard_tap`
- `keyboard_text`
- `mouse_click`
- `mouse_move`
- `mouse_scroll`
- `touch_gesture`

建议使用 normalized 坐标（`0..1000`，中心为 `500,500`），避免显示分辨率变化导致点击位置偏移。
对小按钮、列表项、输入框等密集目标，也优先估算目标中心的 normalized 坐标；只有截图像素坐标和 HID 触控坐标已经校准时，才显式传 `coord_space: "pixel"`。输入工具成功后会返回 post-action screenshot，应先确认画面变化再继续，避免重复点击。
`keyboard_text` 模拟美式键盘，只能输入 ASCII 可键入字符；中文应通过拼音/英文搜索词和屏幕候选完成，不能把中文字符串直接传给工具。
