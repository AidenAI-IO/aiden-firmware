# HDMI 动态分辨率检测指南

## 问题背景

`example_camera_capture` 默认请求 1920x1080，但如果 HDMI 源输出其他分辨率（如 1280x720），会导致初始化失败并报错：

```
HDMI timing mismatch on /dev/v4l-subdev2: detected 1280x720, expected 1920x1080
Failed to synchronize HDMI DV timings on /dev/v4l-subdev2: Link has been severed
```

## 解决方案

### 方案 1：使用 v4l2-ctl 查询（推荐）

在目标设备上运行：

```bash
# 查询当前 HDMI 检测到的分辨率
v4l2-ctl -d /dev/v4l-subdev2 --query-dv-timings

# 输出示例：
# DV timings:
#         Active width: 1280
#         Active height: 720
#         Total width: 1650
#         Total height: 750
#         Frame format: progressive
#         Polarities: -vsync -hsync
#         Pixelclock: 74250000 Hz (60.00 frames per second)
```

然后使用检测到的分辨率：

```bash
example_camera_capture --width 1280 --height 720
```

### 方案 2：使用 query_hdmi_timings 工具

在目标设备上编译并运行：

```bash
# 编译（在交叉编译环境或目标设备上）
cd build
make query_hdmi_timings

# 运行
./bin/query_hdmi_timings

# 或指定自定义 subdev
./bin/query_hdmi_timings /dev/v4l-subdev2
```

输出示例：

```
Querying HDMI timings from /dev/v4l-subdev2...

Detected HDMI Resolution:
  Width:  1280
  Height: 720
  Frame rate: 60.00 Hz
  Interlaced: no

Use these parameters with example_camera_capture:
  example_camera_capture --width 1280 --height 720

Or use auto-detect mode:
  example_camera_capture --allow-resolution-mismatch
```

### 方案 3：允许自动适应（最简单）

```bash
# 让程序接受检测到的任何分辨率
example_camera_capture --allow-resolution-mismatch
```

这会：
1. 尝试请求默认的 1920x1080
2. 查询 HDMI 实际分辨率（通过 `VIDIOC_SUBDEV_QUERY_DV_TIMINGS`）
3. 接受检测到的分辨率并继续捕获

### 方案 4：跳过 HDMI 同步

如果你的硬件不需要 EDID/timing 同步：

```bash
example_camera_capture --width 1280 --height 720 --no-hdmi-sync
```

## 代码逻辑说明

### 默认配置

默认分辨率定义在 `src/camera_frame_utils.h:18-19`：

```cpp
config->width = 1920;
config->height = 1080;
```

### HDMI 同步流程

在 `src/aiden_sdk.cpp:373-458` 中，`sync_hdmi_input()` 函数执行以下步骤：

1. **打开 subdev**：默认 `/dev/v4l-subdev2`
2. **查询 DV timings**：通过 `VIDIOC_SUBDEV_QUERY_DV_TIMINGS` 获取实际分辨率
3. **检查匹配**：
   - 如果 `require_exact_resolution = true`（默认），检测值必须与请求值完全匹配
   - 如果 `require_exact_resolution = false`（`--allow-resolution-mismatch`），接受任何检测值
4. **EDID 重新触发**（如果不匹配）：
   - 推送 EDID（默认使用内置的 1080p30 EDID）
   - 重新查询 timings
   - 重试 `trigger_retries + 1` 次

### 相关参数

```bash
--width N                 # 请求的宽度（默认：1920）
--height N                # 请求的高度（默认：1080）
--subdev PATH             # HDMI subdev 设备（默认：/dev/v4l-subdev2）
--allow-resolution-mismatch  # 接受检测到的任何分辨率
--no-hdmi-sync           # 完全跳过 HDMI 同步
--trigger-retries N      # EDID 重新触发次数（默认：0）
--trigger-delay-ms N     # 触发后延迟（默认：1000ms）
```

## 常见错误

### "Link has been severed" (ENOLINK)

**原因**：
- HDMI 线缆松动或信号质量差
- HDMI 源未连接或未输出信号
- V4L2 subdev 驱动状态异常

**解决**：
1. 检查 HDMI 物理连接
2. 确认 HDMI 源正在输出信号
3. 尝试 `--allow-resolution-mismatch` 或 `--no-hdmi-sync`

### "HDMI timing mismatch"

**原因**：
- HDMI 源输出的分辨率与请求的不一致
- `require_exact_resolution = true`（默认）导致严格检查失败

**解决**：
1. 使用 `v4l2-ctl` 或 `query_hdmi_timings` 查询实际分辨率
2. 指定正确的 `--width` 和 `--height`
3. 或使用 `--allow-resolution-mismatch`

## 调试技巧

### 查看详细日志

`example_camera_capture` 会打印初始化信息：

```
Initializing camera capture...
Capture device: /dev/video0
Requested format: 1920x1080 uyvy
HDMI sync: enabled, subdev=/dev/v4l-subdev2, EDID=built-in 1080p30-only CTA, force_trigger=no
```

如果失败，会显示具体错误位置。

### 手动测试 V4L2

```bash
# 查看可用设备
v4l2-ctl --list-devices

# 查看 subdev 能力
v4l2-ctl -d /dev/v4l-subdev2 --all

# 查询当前 timings
v4l2-ctl -d /dev/v4l-subdev2 --query-dv-timings

# 手动设置 timings（如果需要）
v4l2-ctl -d /dev/v4l-subdev2 --set-dv-bt-timings query
```

## 源码参考

- **默认配置**：`src/camera_frame_utils.h:12-32`
- **HDMI 同步**：`src/aiden_sdk.cpp:373-458`
- **分辨率检查**：`src/aiden_sdk.cpp:133-141`
- **命令行解析**：`src/example_camera_capture.cpp:158-276`
