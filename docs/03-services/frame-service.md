# Frame Service: HDMI Frame Capture Service

`frame_service` is a long-running C++ service that exclusively owns `/dev/video0`, continuously reads frames from the TC358743 HDMI capture path, and provides the latest frames, screenshots, and health status externally through a Unix domain socket.

## Why Frame Service is Needed

Allowing multiple processes to directly open `/dev/video0` can lead to resource conflicts and unstable states. `frame_service` centralizes video device management:

- Completes HDMI sync / EDID / DV timings / VI initialization at startup;
- Copies frames to a ring buffer at a fixed FPS;
- Provides frames to consumers (Agent, CLI, HTTP examples, etc.) through a socket;
- Supports restarting the capture manager on errors.

## Default Parameters

| Parameter | Default Value | Description |
| --- | --- | --- |
| Socket (development direct run) | `/tmp/frame_service.sock` | Default value in `frame_service_main.cpp` |
| Socket (firmware service) | `/run/frame_service/frame_service.sock` | Default value in init configuration |
| EDID | Built-in 1080p30 CTA EDID | Used when `--edid` is not provided |
| Ring size | `3` | `kDefaultFrameServiceRingSize` |
| FPS | `3.0` | For default 1080p input, the service sampling FPS is kept low to control CPU, memory bandwidth, and heat |
| Screenshot max edge | `960` | Related to Go screenshot tool default compression strategy |

## Startup

Development mode:

```bash
./build/bin/frame_service --socket /tmp/frame_service.sock
```

Firmware service:

```bash
/etc/init.d/S52frame_service start
```

## Parameters

```text
frame_service [--socket PATH] [--device PATH] [--width N] [--height N]
              [--pixel-format FMT] [--subdev PATH] [--edid PATH]
              [--ring-size N] [--fps N] [--no-hdmi-sync]
              [--require-exact-resolution]
```

| Parameter | Description |
| --- | --- |
| `--socket PATH` | UDS socket path |
| `--device PATH` | V4L2 capture device, defaults to `/dev/video0` |
| `--width N` / `--height N` | Target resolution hint before HDMI sync, defaults to 1920x1080; accepts the actual negotiated input resolution by default |
| `--pixel-format FMT` | `nv12`, `nv16`, `uyvy`, `yuyv`, defaults to `uyvy` |
| `--subdev PATH` | HDMI bridge subdev, defaults to `/dev/v4l-subdev2` |
| `--edid PATH` | Custom EDID hex; uses built-in 1080p30-only CTA EDID when empty |
| `--ring-size N` | Ring buffer capacity |
| `--fps N` | Sampling FPS; `0` means as fast as possible |
| `--no-hdmi-sync` | Skip HDMI sync helper flow |
| `--require-exact-resolution` | Strictly require actual input resolution to match `--width/--height`; disabled by default for convenience with 720p/portrait modes and direct screenshots |

Environment variables can also be used:

```bash
export FRAME_SERVICE_SOCKET=/tmp/frame_service.sock
```

## CLI

```bash
frame_service_cli [--socket PATH] <health|latest-frame|screenshot|list-frames|restart> [--out PATH]
```

Examples:

```bash
./build/bin/frame_service_cli --socket /tmp/frame_service.sock health
./build/bin/frame_service_cli --socket /tmp/frame_service.sock screenshot --out /tmp/screenshot.bmp
./build/bin/frame_service_cli --socket /tmp/frame_service.sock latest-frame --out /tmp/frame.raw
./build/bin/frame_service_cli --socket /tmp/frame_service.sock list-frames
./build/bin/frame_service_cli --socket /tmp/frame_service.sock restart
```

## Relationship with Agent

The Go Agent's `screenshot` tool accesses `frame_service` through `FrameServiceClient`. Configuration:

```toml
[hid]
frame_socket = "/run/frame_service/frame_service.sock"
```

Screenshot interpretation requires the model/provider to support image input. Text-only models can call `screenshot` but cannot understand pixel content.

## Conflicts with Direct Camera Examples

`frame_service` exclusively owns `/dev/video0` while running. To run `example_camera_capture`:

```bash
/etc/init.d/S52frame_service stop
./build/bin/example_camera_capture
/etc/init.d/S52frame_service start
```
