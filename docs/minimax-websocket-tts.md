# Minimax WebSocket TTS 配置指南

## 概述

Minimax WebSocket TTS 提供了一个持久化的 WebSocket 连接来替代传统的 HTTP 请求方式，主要优势：

- **连接复用**：避免每次 TTS 请求都建立新的 TCP/TLS 连接
- **降低延迟**：省去 TLS 握手时间（~100ms）
- **更稳定**：长连接避免频繁的连接建立/断开

## 配置方式

### 方式 1：使用 `use_websocket` 标志

在现有的 `minimax` provider 配置中添加 `use_websocket = true`：

```toml
[tts]
provider = "minimax"
api_key = "your-minimax-api-key"
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0
use_websocket = true  # 启用 WebSocket 模式
```

### 方式 2：使用 `minimax-ws` provider

直接指定 WebSocket provider：

```toml
[tts]
provider = "minimax-ws"
api_key = "your-minimax-api-key"
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0
```

## 工作原理

### HTTP 模式（原有方式）

```
每次 TTS 请求:
  建立 TCP 连接 → TLS 握手 → HTTP POST → 接收音频流 → 关闭连接
  ↑ 每次都需要 100-200ms 的建连开销
```

### WebSocket 模式（新方式）

```
首次请求:
  建立 WebSocket 连接 → 保持连接

后续请求:
  复用现有连接 → task_start → task_continue(text) → 接收音频 → 保持连接
  ↑ 省去建连开销，只有 TTS 推理时间
```

## 消息协议

WebSocket 通信流程：

1. **连接建立**

   ```json
   // 客户端连接 wss://api.minimaxi.com/v1/t2a_v2
   // 服务端返回
   { "event": "connected_success" }
   ```

2. **启动任务**

   ```json
   // 客户端发送
   {
     "event": "task_start",
     "model": "speech-2.8-hd",
     "voice_setting": {
       "voice_id": "male-qn-qingse",
       "speed": 1.0,
       "vol": 1.0,
       "pitch": 0,
       "emotion": "happy"
     },
     "audio_setting": {
       "sample_rate": 16000,
       "format": "pcm",
       "channel": 1
     }
   }
   // 服务端返回
   {"event": "task_started"}
   ```

3. **发送文本并接收音频**

   ```json
   // 客户端发送
   {"event": "task_continue", "text": "你好，这是测试文本"}

   // 服务端流式返回音频
   {"data": {"audio": "<hex编码的PCM数据>"}, "is_final": false}
   {"data": {"audio": "<hex编码的PCM数据>"}, "is_final": false}
   {"data": {"audio": "<hex编码的PCM数据>"}, "is_final": true}
   ```

4. **关闭连接**（可选，连接可以保持复用）
   ```json
   { "event": "task_finish" }
   ```

## 实现细节

### 连接管理

- WebSocket 连接在首次 TTS 请求时建立
- 连接会被复用于后续的 TTS 请求
- 每次请求都会发送 `task_start` 和 `task_continue` 消息
- 连接在程序退出时关闭

### 音频格式

- **输出格式**：PCM 16-bit mono
- **采样率**：16000 Hz
- **通道数**：1（单声道）
- **编码**：Hex 编码（需要解码为字节）

### 错误处理

- 连接失败时会自动重试（最多 3 次）
- WebSocket 断开后会在下次请求时重新建立
- 所有错误都会记录到日志

## 性能对比

| 指标         | HTTP 模式      | WebSocket 模式 |
| ------------ | -------------- | -------------- |
| 首次请求延迟 | ~300-400ms     | ~300-400ms     |
| 后续请求延迟 | ~300-400ms     | ~200-250ms     |
| 连接开销     | 每次 100-200ms | 仅首次         |
| 适用场景     | 偶尔使用 TTS   | 频繁使用 TTS   |

## 注意事项

1. **流式文本输入限制**：当前 Minimax WebSocket API 的 `task_continue` 需要一次性发送完整文本，不支持逐 token 追加。当前 adapter 会在内部按句子边界 buffer LLM token；如需真正逐 token 推送，建议使用 Fish Audio 或 Qwen/AliCloud。

2. **连接超时**：WebSocket 连接会在长时间空闲后可能被服务端关闭，客户端会自动重连。

3. **并发限制**：单个 WebSocket 连接同时只能处理一个 TTS 任务。

## 故障排查

### 连接失败

```
[tts] dial websocket: connection refused
```

**解决方法**：

- 检查网络连接
- 确认 API key 正确
- 检查防火墙设置

### 认证失败

```
[tts] unexpected connection response: {"error": "invalid api key"}
```

**解决方法**：

- 检查 `api_key` 配置是否正确
- 确认 API key 有效且未过期

### 音频播放异常

```
[tts] TTS returned no PCM audio
```

**解决方法**：

- 检查文本内容是否为空
- 查看服务端返回的错误信息
- 确认 `voice_id` 配置正确

## 代码示例

查看实现细节：

- WebSocket adapter：`src/agent/internal/agent/tts/adapters/minimax/websocket.go`
- HTTP adapter（对比）：`src/agent/internal/agent/tts/adapters/minimax/http.go`
- 配置加载：`src/agent/internal/agent/input_processing.go`
