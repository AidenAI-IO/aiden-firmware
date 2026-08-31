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
| Socket (Debian service) | `/run/frame_service/frame_service.sock` | Default value in systemd configuration |
| EDID | Bridge-aware | RK628D keeps its driver-provided 1080p60 EDID; TC358743 loads `/oem/usr/share/aiden/edid/hdmi_1080p30_cta.hex` |
| Capture mode | `on_demand` | One fresh capture for each `latest_frame` / screenshot request |
| Warm-up frames | Mode-aware | `6` with persistent STREAMON; `0` when streaming restarts per request |
| Production ring usage | `0` | Health reports `ring_buffer_size=0`, `ring_buffer_used=0` |
| Pixel format | `nv12` | Debian and direct frame_service defaults; set `FRAME_SERVICE_PIXEL_FORMAT=uyvy` for compatibility fallback |
| Screenshot max edge | `960` | Related to Go screenshot tool default compression strategy |

## Startup

Development mode:

```bash
./build/bin/frame_service --socket /tmp/frame_service.sock
```

The service initializes HDMI/V4L2 once, pauses the stream, and waits for a
capture request.

Debian service:

```bash
systemctl start aiden-frame.service
```

The Debian service uses the same on-demand capture lifecycle.
`FRAME_SERVICE_PIXEL_FORMAT` in `/etc/aiden_frame_service.conf` accepts `nv12`,
`nv16`, `uyvy`, or `yuyv` and defaults to `nv12`. `FRAME_SERVICE_FPS` remains
accepted for configuration compatibility but is ignored in on-demand mode.

## Parameters

```text
frame_service [--socket PATH] [--device PATH] [--width N] [--height N]
              [--pixel-format FMT] [--subdev PATH|--auto-subdev] [--edid PATH]
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
| `--pixel-format FMT` | `nv12`, `nv16`, `uyvy`, `yuyv`, defaults to `nv12` for frame_service |
| `--subdev PATH` | Explicit HDMI bridge subdev. The Debian service normally uses `--auto-subdev` so the bridge can appear after boot and its `/dev/v4l-subdevX` index can change. |
| `--auto-subdev` | Rediscover `rk628-csi` or `tc358743` on every capture recovery. This is the Debian service mode and keeps the IPC endpoint alive while HDMI is absent. |
| `--edid PATH` | Custom EDID hex. The Debian start helper automatically selects the 1080p30 CTA EDID for TC358743 and leaves RK628D on its 1080p60 driver EDID; an explicit path overrides this policy |
| `--force-trigger` / `--no-force-trigger` | Enable or disable one-shot startup EDID/HPD renegotiation. The Debian start helper defaults to bridge-aware `auto`: disabled for RK628D and enabled for TC358743. Before starting capture on TC358743, the helper also holds HPD low for 2 seconds and allows 5 seconds for the HDMI source to settle on 1080p30 |
| `--ring-size N` | Deprecated compatibility option; ignored by production on-demand capture |
| `--fps N` | Deprecated compatibility option; ignored by production on-demand capture |
| `--warmup-frames N` | Override frames dequeued/released before copying the response. Without an override, defaults to `6` with `--keep-streamon` and `0` with `--pause-between-captures` |
| `--keep-streamon` / `--pause-between-captures` | Keep capture STREAMON between requests, or restore the default per-request STREAMON/STREAMOFF lifecycle |
| `--allow-uniform-frames` | Accept black or single-colour frames after warm-up; this is the default because they are valid screenshots |
| `--reject-uniform-frames` | Reject all-same packed UYVY/YUYV frames after warm-up for diagnostics |
| `--no-hdmi-sync` | Skip HDMI sync helper flow |
| `--require-exact-resolution` / `--allow-resolution-mismatch` | Require or relax matching `--width/--height`; exact matching is enabled by default |

Environment variables can also be used:

```bash
export FRAME_SERVICE_SOCKET=/tmp/frame_service.sock
```

The Debian start helper leaves `FRAME_SERVICE_SUBDEV` empty by default and
discovers a subdevice whose sysfs name contains `rk628-csi` or `tc358743`.
The service creates its Unix socket before capture initialization. If the
bridge, video node, HDMI signal, EDID, or DV timings are unavailable, the
capture manager remains in `RECOVERING` and retries with a bounded backoff;
the socket and health endpoint remain available. Once a source is connected,
the manager transitions to `RUNNING` without a systemd restart. Set an
explicit path in `/etc/aiden_frame_service.conf` only when automatic discovery
is not suitable.

For capture, frame_service defaults to V4L2 single-plane NV12. The capture
boundary removes any driver row padding (Y and UV rows are copied into a tight
`width * height * 3 / 2` payload), so the existing IPC protocol and raw-frame
consumers continue to see compact NV12. Configured `uyvy`, `yuyv`, and `nv16`
formats remain available as compatibility fallbacks.

JPEG requests use software JPEG by default on Debian. Hardware mode is an
optional RV1106 Rockit MPI VENC path for compact NV12: the service copies the
frame into an aligned MMZ buffer using the VENC horizontal and virtual
strides, synchronizes caches, and retries once after transient channel errors.
If VENC is unavailable or cooling down, software JPEG encoding is used. On the
current RV1106 image, hardware VENC has returned `RK_ERR_VENC_BUF_EMPTY` and
has triggered faults in the vendor `mpp_vcodec` module, so it is experimental
and must be explicitly enabled with `FRAME_SERVICE_JPEG_ENCODER=hardware` only
after board-specific validation. When hardware mode is enabled, a background
warm-up runs after the first valid NV12 frame; a request racing that warm-up
immediately uses software fallback instead of waiting on the cold VENC path. A
resolution change schedules a new warm-up without blocking capture. Optionally
set `FRAME_SERVICE_VENC_CHANNEL` to a preferred channel from 0 through 63; on
a channel collision the service automatically tries another channel.


## On-Demand Capture Lifecycle

With the default `keep_streamon = false`, service startup and each request use
the following lifecycle.

At service startup:

1. HDMI timing/EDID synchronization runs as before.
2. `/dev/video0` is opened and V4L2 buffers are requested and mapped.
3. The service immediately calls `VIDIOC_STREAMOFF` and waits without dequeuing frames.

For each `latest_frame` request:

1. The mapped buffers are queued and capture restarts with `VIDIOC_STREAMON`.
2. The camera layer discards its initial transitional frame. No additional frames are discarded in the default pause-between-captures mode.
3. One frame is copied, optionally cropped/encoded, and sent to the requester. Uniform content is accepted by default, so a black screen or single-colour page is not reported as a capture failure.
4. The service calls `VIDIOC_STREAMOFF` again.

This avoids old completed buffers after a long idle interval and removes the
continuous VI/DDR traffic caused by the previous drain loop. Requests are
serialized, so multiple clients do not race the V4L2 queue.

The Debian start helper reads the persistent setting from
`/userdata/agent/agent.toml`:

```toml
[frame_service]
keep_streamon = false
```

When set to `true`, the stream remains active after initialization and requests
skip the STREAMOFF/STREAMON pair. Each request dequeues six frames before the
response: four clear the completed V4L2 MMAP queue observed on RK628D, with two
additional frames of margin. Saving a changed value in Config Web restarts
Frame Service so the new policy takes effect immediately. An explicit
`--warmup-frames` value overrides this mode-aware default for diagnostics.

## Resource and Power Boundary

On-demand mode removes retained production frame copies and continuous frame
dequeue/copy work. It intentionally keeps the video fd and four driver MMAP
buffers allocated so HDMI does not need full EDID/timing reinitialization on
every screenshot. The HDMI bridge itself also remains configured. Therefore:

- userspace ring-buffer memory and continuous memory bandwidth should drop;
- screenshot latency increases by stream restart, the mode-aware warm-up policy, and requested crop/encode work;
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
systemctl stop aiden-frame.service
./build/bin/example_camera_capture
systemctl start aiden-frame.service
```
