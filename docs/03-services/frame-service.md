# Frame Service: HDMI Frame Capture Service

`frame_service` is a long-running C++ service that exclusively owns `/dev/video0`, continuously reads frames from the detected RK628D or TC358743 HDMI capture path, and provides the latest frames, screenshots, and health status externally through a Unix domain socket.

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
| EDID | Bridge-aware | RK628D keeps its driver-provided 1080p60 EDID; TC358743 loads `/oem/usr/share/aiden/edid/hdmi_1080p30_cta.hex` |
| Ring size | `3` | `kDefaultFrameServiceRingSize` |
| FPS (binary default) | `3.0` | Used when `frame_service` is run directly without `--fps` |
| FPS (firmware service default) | `30` | `S52frame_service` passes `--fps 30` from `/etc/aiden_frame_service.conf` |
| Screenshot max edge | `960` | Related to Go screenshot tool default compression strategy |

## Startup

Development mode:

```bash
./build/bin/frame_service --socket /tmp/frame_service.sock
```

This direct invocation uses the binary default of 3 FPS unless `--fps` is
provided.

Firmware service:

```bash
/etc/init.d/S52frame_service start
```

The firmware service overrides the binary default and samples at 30 FPS by
default. Change `FRAME_SERVICE_FPS` in `/etc/aiden_frame_service.conf` to tune
the deployed service.

## Parameters

```text
frame_service [--socket PATH] [--device PATH] [--width N] [--height N]
              [--pixel-format FMT] [--subdev PATH] [--edid PATH]
              [--ring-size N] [--fps N] [--no-hdmi-sync]
              [--force-trigger|--no-force-trigger]
              [--require-exact-resolution|--allow-resolution-mismatch]
```

| Parameter | Description |
| --- | --- |
| `--socket PATH` | UDS socket path |
| `--device PATH` | V4L2 capture device, defaults to `/dev/video0` |
| `--width N` / `--height N` | Required HDMI resolution, defaults to 1920x1080 |
| `--pixel-format FMT` | `nv12`, `nv16`, `uyvy`, `yuyv`, defaults to `uyvy` |
| `--subdev PATH` | HDMI bridge subdev; the binary defaults to `/dev/v4l-subdev2` |
| `--edid PATH` | Custom EDID hex. The firmware init script automatically selects the 1080p30 CTA EDID for TC358743 and leaves RK628D on its 1080p60 driver EDID; an explicit path overrides this policy |
| `--force-trigger` / `--no-force-trigger` | Enable or disable one-shot startup EDID/HPD renegotiation. The init script defaults to bridge-aware `auto`: disabled for RK628D and enabled for TC358743. Before starting capture on TC358743, the init script also holds HPD low for 2 seconds and allows 5 seconds for the HDMI source to settle on 1080p30 |
| `--ring-size N` | Ring buffer capacity |
| `--fps N` | Sampling FPS; `0` means as fast as possible |
| `--no-hdmi-sync` | Skip HDMI sync helper flow |
| `--require-exact-resolution` / `--allow-resolution-mismatch` | Require or relax matching `--width/--height`; exact matching is enabled by default |

Environment variables can also be used:

```bash
export FRAME_SERVICE_SOCKET=/tmp/frame_service.sock
```

The firmware init script leaves `FRAME_SERVICE_SUBDEV` empty by default and
discovers a subdevice whose sysfs name contains `rk628-csi` or `tc358743`.
Set an explicit path in `/etc/aiden_frame_service.conf` only when automatic
discovery is not suitable.

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
