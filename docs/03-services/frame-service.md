---
sidebar_position: 4
---

# Frame Service: HDMI Frame Capture Service

`frame_service` is a long-running C++ service that exclusively owns `/dev/video0` and provides fresh screenshots through a Unix domain socket. It keeps the device and V4L2 MMAP buffers open. By default it pauses streaming between requests instead of continuously dequeuing frames; an `agent.toml` setting can keep STREAMON active for latency and power measurements.

## Why Frame Service is Needed

Allowing multiple processes to directly open `/dev/video0` can lead to resource conflicts and unstable states. `frame_service` centralizes video device management:

- Completes HDMI sync / EDID / DV timings / VI initialization at startup;
- Calls `VIDIOC_STREAMOFF` while idle, then requeues buffers and calls `VIDIOC_STREAMON` for each frame request;
- Keeps raw frame bytes only for the lifetime of the active request rather than retaining a production ring buffer;
- Provides frames to consumers (Agent, CLI, HTTP examples, etc.) through a socket;
- Supports restarting the capture manager on errors.

## Default Parameters

| Parameter | Default Value | Description |
| --- | --- | --- |
| Socket (development direct run) | `/tmp/frame_service.sock` | Default value in `frame_service_main.cpp` |
| Socket (firmware service) | `/run/frame_service/frame_service.sock` | Default value in init configuration |
| EDID | Bridge-aware | RK628D keeps its driver-provided 1080p60 EDID; TC358743 loads `/oem/usr/share/aiden/edid/hdmi_1080p30_cta.hex` |
| Capture mode | `on_demand` | One fresh capture for each `latest_frame` / screenshot request |
| Warm-up frames | `12` | Dequeued without copying after each stream restart before the response frame |
| Production ring usage | `0` | Health reports `ring_buffer_size=0`, `ring_buffer_used=0` |
| Screenshot max edge | `960` | Related to Go screenshot tool default compression strategy |

## Startup

Development mode:

```bash
./build/bin/frame_service --socket /tmp/frame_service.sock
```

The service initializes HDMI/V4L2 once, pauses the stream, and waits for a
capture request.

Firmware service:

```bash
/etc/init.d/S52frame_service start
```

The firmware service uses the same on-demand capture lifecycle.

## Parameters

```text
frame_service [--socket PATH] [--device PATH] [--width N] [--height N]
              [--pixel-format FMT] [--subdev PATH] [--edid PATH]
              [--ring-size N] [--fps N] [--no-hdmi-sync]
              [--force-trigger|--no-force-trigger]
              [--warmup-frames N]
              [--keep-streamon|--pause-between-captures]
              [--allow-uniform-frames|--reject-uniform-frames]
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
| `--ring-size N` | Deprecated compatibility option; ignored by production on-demand capture |
| `--fps N` | Deprecated compatibility option; ignored by production on-demand capture |
| `--warmup-frames N` | Frames to dequeue/release after `STREAMON` before copying the response; defaults to `12` |
| `--keep-streamon` / `--pause-between-captures` | Keep capture STREAMON between requests, or restore the default per-request STREAMON/STREAMOFF lifecycle |
| `--allow-uniform-frames` | Accept black or single-colour frames after warm-up; this is the default because they are valid screenshots |
| `--reject-uniform-frames` | Reject all-same packed UYVY/YUYV frames after warm-up for diagnostics |
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

## On-Demand Capture Lifecycle

With the default `keep_streamon = false`, service startup and each request use
the following lifecycle.

At service startup:

1. HDMI timing/EDID synchronization runs as before.
2. `/dev/video0` is opened and V4L2 buffers are requested and mapped.
3. The service immediately calls `VIDIOC_STREAMOFF` and waits without dequeuing frames.

For each `latest_frame` request:

1. The mapped buffers are queued and capture restarts with `VIDIOC_STREAMON`.
2. The configured warm-up frames are dequeued and immediately released without copying. This lets RK628D/TC358743 output stabilize after every stream restart.
3. One frame is copied, optionally cropped/encoded, and sent to the requester. Uniform content is accepted by default, so a black screen or single-colour page is not reported as a capture failure.
4. The service calls `VIDIOC_STREAMOFF` again.

This avoids old completed buffers after a long idle interval and removes the
continuous VI/DDR traffic caused by the previous drain loop. Requests are
serialized, so multiple clients do not race the V4L2 queue.

The firmware init script reads the persistent setting from
`/userdata/agent/agent.toml`:

```toml
[frame_service]
keep_streamon = false
```

When set to `true`, the stream remains active after initialization and requests
skip the STREAMOFF/STREAMON pair. Warm-up frames are still dequeued before each
response so queued stale frames are not returned. Saving a changed value in
Config Web restarts Frame Service so the new policy takes effect immediately.

## Resource and Power Boundary

On-demand mode removes retained production frame copies and continuous frame
dequeue/copy work. It intentionally keeps the video fd and four driver MMAP
buffers allocated so HDMI does not need full EDID/timing reinitialization on
every screenshot. The HDMI bridge itself also remains configured. Therefore:

- userspace ring-buffer memory and continuous memory bandwidth should drop;
- screenshot latency increases by stream restart plus the configured warm-up period (12 frames by default) and any requested crop/encode work;
- `keep_streamon = true` removes the per-request stream restart latency but keeps capture hardware active while idle;
- total board power savings depend on whether the active RK628D/TC358743 and VI drivers gate clocks after `VIDIOC_STREAMOFF` and must be measured on hardware.

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

`list-frames` returns an empty list in on-demand mode. `get_frame` remains in
the wire protocol for compatibility but returns `FRAME_NOT_FOUND`, because raw
frames are released after their requesting connection finishes.

## Relationship with Agent

The Go Agent's `screenshot` tool accesses `frame_service` through `FrameServiceClient`. Configuration:

```toml
[hid]
frame_socket = "/run/frame_service/frame_service.sock"

[frame_service]
keep_streamon = false
```

When health reports `capture_mode=on_demand`, the Agent treats an old
`frame_age_ms` as expected idle state and issues `latest_frame` to trigger a
fresh capture. Buffered-mode stale-frame checks remain unchanged.

Screenshot interpretation requires the model/provider to support image input. Text-only models can call `screenshot` but cannot understand pixel content.

## Conflicts with Direct Camera Examples

`frame_service` exclusively owns `/dev/video0` while running. To run `example_camera_capture`:

```bash
/etc/init.d/S52frame_service stop
./build/bin/example_camera_capture
/etc/init.d/S52frame_service start
```
