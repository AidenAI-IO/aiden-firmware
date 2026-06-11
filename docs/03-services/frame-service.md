# Frame Service：HDMI 帧捕获服务

`frame_service` 是 C++ 长运行服务，负责独占 `/dev/video0`，持续从 TC358743 HDMI capture path 读取帧，并通过 Unix domain socket 对外提供最近帧、截图和健康状态。

## 为什么需要 Frame Service

直接让多个进程打开 `/dev/video0` 会导致资源冲突和状态不稳定。`frame_service` 将视频设备集中管理：

- 启动时完成 HDMI sync / EDID / DV timings / VI 初始化；
- 按固定 FPS 将帧复制到 ring buffer；
- 通过 socket 为 Agent、CLI、HTTP demo 等消费者提供帧；
- 出错时支持重启 capture manager。

## 默认参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| Socket（开发直接运行） | `/tmp/frame_service.sock` | `frame_service_main.cpp` 默认值 |
| Socket（固件服务） | `/run/frame_service/frame_service.sock` | init 配置默认值 |
| EDID | 内置 1080p30 CTA EDID | 未传 `--edid` 时使用 |
| Ring size | `3` | `kDefaultFrameServiceRingSize` |
| FPS | `3.0` | 默认 1080p 输入，服务采样 FPS 保持较低以控制 CPU、内存带宽和发热 |
| Screenshot max edge | `960` | Go screenshot 工具默认压缩策略相关 |

## 启动

开发模式：

```bash
./build/bin/frame_service --socket /tmp/frame_service.sock
```

固件服务：

```bash
/etc/init.d/S52frame_service start
```

## 参数

```text
frame_service [--socket PATH] [--device PATH] [--width N] [--height N]
              [--pixel-format FMT] [--subdev PATH] [--edid PATH]
              [--ring-size N] [--fps N] [--no-hdmi-sync]
              [--require-exact-resolution]
```

| 参数 | 说明 |
| --- | --- |
| `--socket PATH` | UDS socket 路径 |
| `--device PATH` | V4L2 capture device，默认 `/dev/video0` |
| `--width N` / `--height N` | HDMI sync 前的目标分辨率提示值，默认 1920x1080；默认接受实际协商到的输入分辨率 |
| `--pixel-format FMT` | `nv12`、`nv16`、`uyvy`、`yuyv`，默认 `uyvy` |
| `--subdev PATH` | HDMI bridge subdev，默认 `/dev/v4l-subdev2` |
| `--edid PATH` | 自定义 EDID hex；为空时使用内置 1080p30-only CTA EDID |
| `--ring-size N` | ring buffer 容量 |
| `--fps N` | 采样 FPS；`0` 表示尽可能快 |
| `--no-hdmi-sync` | 跳过 HDMI sync 辅助流程 |
| `--require-exact-resolution` | 严格要求实际输入分辨率与 `--width/--height` 一致；默认关闭，便于 720p/竖屏等模式直接截图 |

也可使用环境变量：

```bash
export FRAME_SERVICE_SOCKET=/tmp/frame_service.sock
```

## CLI

```bash
frame_service_cli [--socket PATH] <health|latest-frame|screenshot|list-frames|restart> [--out PATH]
```

示例：

```bash
./build/bin/frame_service_cli --socket /tmp/frame_service.sock health
./build/bin/frame_service_cli --socket /tmp/frame_service.sock screenshot --out /tmp/screenshot.bmp
./build/bin/frame_service_cli --socket /tmp/frame_service.sock latest-frame --out /tmp/frame.raw
./build/bin/frame_service_cli --socket /tmp/frame_service.sock list-frames
./build/bin/frame_service_cli --socket /tmp/frame_service.sock restart
```

## 与 Agent 的关系

Go Agent 的 `screenshot` 工具通过 `FrameServiceClient` 访问 `frame_service`。配置项：

```toml
[hid]
frame_socket = "/run/frame_service/frame_service.sock"
```

截图解释要求模型/provider 支持图像输入。文本模型可以调用 `screenshot`，但无法理解像素内容。

## 与直接相机示例的冲突

`frame_service` 运行期间会独占 `/dev/video0`。如果要运行 `example_camera_capture`：

```bash
/etc/init.d/S52frame_service stop
./build/bin/example_camera_capture
/etc/init.d/S52frame_service start
```
