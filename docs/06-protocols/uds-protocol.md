# Unix Domain Socket 通用协议

本项目使用 Unix domain sockets 连接 C++ 硬件服务和其他进程。协议设计目标是简单、跨语言、可携带大块二进制数据。

## 消息 Envelope

每条消息是一个完整 frame：

1. `uint32` little-endian：JSON header 长度；
2. `uint64` little-endian：binary payload 长度；
3. UTF-8 JSON header bytes；
4. 可选 binary payload bytes。

```text
+----------------------+-----------------------+----------------+------------------+
| header_len: uint32le | payload_len: uint64le | JSON header    | binary payload   |
+----------------------+-----------------------+----------------+------------------+
```

JSON header 描述请求或响应；大型二进制数据（如 raw HDMI frame、PCM chunk）放在 payload 中，避免 base64 编码开销。

## C++ 实现

| 文件 | 说明 |
| --- | --- |
| `src/uds_message.*` | envelope 读写 |
| `src/uds_client.*` | 一次请求/响应 client |
| `src/uds_server.*` | bind/listen/accept/thread 生命周期，并分发到服务 handler |
| `src/frame_ipc.*` | frame service 兼容 wrapper |

## 跨语言客户端

Go 或其他语言不需要 C++ ABI / cgo，只需：

1. 连接 service socket；
2. 写入 12-byte envelope prefix；
3. 写入 JSON header 和可选 payload；
4. 按同样格式读取响应。

## 大整数处理

某些字段可能超过 JavaScript safe integer 范围。跨语言客户端应与现有服务保持一致：必要时将大整数编码为 JSON string。

## 状态值

服务共享 `AidenServiceStatus`，常见值包括：

- `OK`
- `NO_NEW_FRAME`
- `FRAME_NOT_FOUND`
- `SESSION_NOT_FOUND`
- `SERVICE_RECOVERING`
- `TIMEOUT`
- `TRANSPORT_ERROR`
- `INTERNAL_ERROR`

具体业务语义见各服务协议。
