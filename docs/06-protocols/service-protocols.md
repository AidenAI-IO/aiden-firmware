# Frame / Audio 服务协议参考

本页概述基于 UDS envelope 的业务协议。公共 envelope 见 [Unix Domain Socket 通用协议](uds-protocol.md)。

## Frame Service

默认 socket：

- 开发直接运行：`/tmp/frame_service.sock`
- 固件服务：`/run/frame_service/frame_service.sock`

### 操作

| op / command | 说明 |
| --- | --- |
| `health` | 返回服务健康状态、最新帧序号、ring 使用情况、错误信息、平均延迟等 |
| `latest_frame` | 返回最新帧 metadata + raw payload |
| `get_frame` | 按序号获取指定帧 |
| `list_frames` | 列出 ring buffer 中的帧 metadata |
| `restart` | 请求服务重启 capture manager |

### FrameMetadata

核心字段：

| 字段 | 说明 |
| --- | --- |
| `seq` | 帧序号 |
| `capture_ts_ns` | 捕获时间戳，纳秒 |
| `width` / `height` | 分辨率 |
| `pixel_format` | 像素格式，如 `uyvy` |
| `stride` | 行 stride |
| `bytes` | payload 字节数 |
| `planes` | 多 plane metadata：offset / stride / bytes |
| `stale` | 是否为陈旧帧 |

CLI 会将 `latest-frame` 的 payload 原样写出；`screenshot` 会将帧编码为 BMP 后写出。

## Audio Service

默认 socket：`/run/audio_service/audio_service.sock`

### AudioFormat

```json
{
  "sample_rate": 16000,
  "channels": 1,
  "bit_width": 16
}
```

### 操作

| op / CLI command | 说明 |
| --- | --- |
| `health` | 返回录音/播放 session 状态 |
| `start_recording` | 创建录音 session，返回 `session_id` |
| `read_record_chunk` | 长轮询读取 PCM chunk |
| `stop_recording` | 停止录音 session |
| `start_playback` | 创建播放 session，返回 `session_id` |
| `write_play_chunk` | 写入 PCM payload；可标记 end-of-stream |
| `stop_playback` | 停止播放 session |
| `get_playback_volume` | 获取逻辑音量 `0..100` |
| `set_playback_volume` | 设置逻辑音量 `0..100` |

### 响应状态

Audio Service 使用共享 `AidenServiceStatus`。录音长轮询超时时可能返回 `TIMEOUT`；session 不存在时返回 `SESSION_NOT_FOUND`。

## 版本兼容建议

- 新增字段时保持 JSON 向后兼容；
- payload 保持二进制原始数据，不要改为 base64；
- 客户端应忽略未知字段；
- 服务端错误也应返回合法 envelope 和 `status`，只有 socket 连接/读写失败才视为 transport failure。
