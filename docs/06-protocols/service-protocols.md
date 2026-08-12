# Frame / Audio Service Protocol Reference

This page outlines the application protocols based on UDS envelope. For the common envelope, see [Unix Domain Socket Protocol](uds-protocol.md).

## Frame Service

Default socket:

- Direct development run: `/tmp/frame_service.sock`
- Firmware service: `/run/frame_service/frame_service.sock`

### Operations

| op / command | Description |
| --- | --- |
| `health` | Returns service health status, latest frame sequence number, ring usage, error messages, average latency, etc. |
| `latest_frame` | Returns latest frame metadata + raw payload |
| `get_frame` | Gets specific frame by sequence number |
| `list_frames` | Lists frame metadata in ring buffer |
| `restart` | Requests service to restart capture manager |

`latest_frame` accepts `format: "jpeg"` or `format: "raw"`, an optional
`crop_black` boolean, and an optional `minimal_width`. When `crop_black` is
true, both JPEG and raw responses crop only uniformly dark columns at the left
and right edges. When `minimal_width` is present, the cropped width will not be
smaller than that value (clamped to the source width). Raw crops preserve the
source pixel format and align horizontal bounds to complete chroma pairs.
For compatibility, omitted `crop_black` defaults to true for JPEG and false
for raw. The response includes
`source_width`, `source_height`, and the `crop_*` rectangle for coordinate
mapping.

### FrameMetadata

Core fields:

| Field | Description |
| --- | --- |
| `seq` | Frame sequence number |
| `capture_ts_ns` | Capture timestamp in nanoseconds |
| `width` / `height` | Resolution |
| `pixel_format` | Pixel format, e.g., `uyvy` |
| `stride` | Row stride |
| `bytes` | Payload byte count |
| `planes` | Multi-plane metadata: offset / stride / bytes |
| `stale` | Whether frame is stale |

CLI will write the `latest-frame` payload as-is; `screenshot` will encode the frame as BMP before writing.

## Audio Service

Default socket: `/run/audio_service/audio_service.sock`

### AudioFormat

```json
{
  "sample_rate": 16000,
  "channels": 1,
  "bit_width": 16
}
```

### Operations

| op / CLI command | Description |
| --- | --- |
| `health` | Returns recording/playback session status |
| `start_recording` | Creates recording session, returns `session_id` |
| `read_record_chunk` | Long-polls to read PCM chunk |
| `stop_recording` | Stops recording session |
| `start_playback` | Creates playback session, returns `session_id` |
| `write_play_chunk` | Writes PCM payload; can mark end-of-stream |
| `stop_playback` | Stops playback session |
| `get_playback_volume` | Gets logical volume `0..100` |
| `set_playback_volume` | Sets logical volume `0..100` |

### Response Status

Audio Service uses shared `AidenServiceStatus`. Recording long-poll may return `TIMEOUT` on timeout; returns `SESSION_NOT_FOUND` when session does not exist.

## Version Compatibility Recommendations

- Maintain JSON backward compatibility when adding new fields;
- Keep payload as raw binary data, do not convert to base64;
- Clients should ignore unknown fields;
- Server errors should also return valid envelope and `status`; only socket connection/read-write failures are considered transport failures.
