# 语音唤醒与网页对话统一设计

**日期：** 2026-06-10  
**状态：** 设计阶段  
**目标：** 实现语音对话（wake word + 语音输入）与网页文字对话共存，统一会话历史，实时在网页显示语音对话记录

## 背景

当前项目架构存在互斥问题：

```go
// main.go 当前逻辑
if inputMode == "audio" || inputMode == "stt" {
    runAudioMode()  // 无 HTTP server，网页不可用
    return
}
server.Start()      // 只有纯文字模式才启动 HTTP server
```

这导致：

- 语音模式下没有网页 UI
- 无法同时支持语音和文字输入
- 语音对话历史无法在网页查看

## 产品需求

### 理想用户体验

1. 用户可以选择**语音唤醒对话**或**网页文字对话**
2. 无论哪种模式，都能在网页内看到**完整历史会话记录**
3. 使用语音模式时，网页**实时显示**转写文字和 agent 回复
4. （可选）保留原始音频，支持回放

### 当前实现目标

- 在网页中实现上述功能（未来接入 app 时架构无需变动）
- 底层使用统一的会话历史存储
- HTTP server 在所有模式下常驻（端口 8080）
- 语音对话记录实时推送到网页

## 技术设计

### 1. 架构调整 — 解除互斥限制

**变更点：** `cmd/daemon/main.go`

```go
func main() {
    // ... 配置加载 ...

    runtime, err := agent.NewRuntime(cfg)
    // ...

    server := agent.NewServer(runtime, *addr)

    // HTTP server 常驻后台
    go func() {
        if err := server.Start(); err != nil {
            log.Fatalf("HTTP server error: %v", err)
        }
    }()

    inputMode := cfg.InputModeOrDefault()

    if inputMode == "audio" || inputMode == "stt" {
        runAudioMode(cfg, runtime, server)  // 传入 server 引用以便集成
    } else {
        // 纯 HTTP 模式，阻塞主线程
        select {}
    }
}
```

**效果：** 无论何种模式，8080 端口都开启，网页始终可访问。

---

### 2. 会话历史数据结构扩展

**变更点：** `internal/agent/chat_history.go` 的 `Message` 结构

```go
type Message struct {
    // ... 现有字段 ...

    // 新增字段
    Source           string `json:"source,omitempty"`            // "text" | "voice"
    AudioFile        string `json:"audio_file,omitempty"`        // 音频文件路径（如果启用归档）
    AudioDurationMs  int    `json:"audio_duration_ms,omitempty"` // 音频时长（毫秒）
}
```

**记录时机：**

- **文字输入：** `source = "text"`
- **语音输入：** `source = "voice"`，如果音频归档启用则填充 `audio_file` 和 `audio_duration_ms`

**向后兼容：** 旧消息没有 `source` 字段，前端默认视为 `"text"`

---

### 3. 音频归档功能（可选）

**新增配置项：** `config.toml`

```toml
[audio_archive]
enabled = false                    # 默认关闭，只存转写文字
max_files = 500                   # 滚动保留最近 N 段音频
max_size_mb = 100                 # 或按总大小上限（二者取先达到的）
storage_path = "/userdata/audio"  # 存储路径
```

**实现逻辑：**

1. **保存音频**
   - VAD 检测到完整语音段落后，保存为 WAV 文件
   - 文件命名：`msg_<timestamp>_<uuid>.wav`
   - 写入 `ChatHistoryStore` 时记录 `audio_file` 路径

2. **清理策略**
   - 每次保存新音频后检查限额
   - 超限时按时间戳从旧到新删除
   - **音频清理只删文件，不回写 `chat_history.jsonl`**（jsonl append-only，改老记录代价高）
   - chat history 里 `audio_file` 字段保留原值，由"过期感知"机制兜底显示

3. **存储估算**
   - 16kHz / 16-bit / 单声道 WAV ≈ 1.9 MB/分钟
   - 默认限额 500 段 × 平均 5 秒 ≈ 40 MB（可控范围）

4. **过期音频感知（避免渲染失效的播放按钮）**

   音频被清理后，chat history 里的 `audio_file` 还在，但文件已不存在。前端如果只看字段非空就渲染播放按钮，用户点了才会 404。需要双层兜底：

   **服务端兜底 — `GET /api/history` 返回前 stat 文件：**

   ```go
   func (s *Server) decorateAudioAvailability(messages []Message) []Message {
       audioDir := s.runtime.config.AudioArchive.StoragePath
       for i := range messages {
           if messages[i].AudioFile == "" {
               continue
           }
           full := filepath.Join(audioDir, filepath.Base(messages[i].AudioFile))
           if _, err := os.Stat(full); err != nil {
               messages[i].AudioFile = ""           // 清空指针
               messages[i].AudioExpired = true      // 标记过期
           }
       }
       return messages
   }
   ```

   - 在 `historySnapshot()` 之后、`/api/history` 写响应之前调用一次
   - 性能：N 次 syscall，历史 < 1000 条时无感知；超长历史可加内存 LRU 缓存或只对尾部 N 条做 stat
   - 不修改盘上数据，重启后状态自然恢复（删过的还是查不到）

   **前端容错 — 长时间停留页面后清理触发：**

   ```js
   function renderMessage(msg) {
     // ...
     if (msg.audio_file) {
       const playBtn = document.createElement("button");
       playBtn.textContent = "▶️ 播放原始音频";
       playBtn.onclick = async () => {
         try {
           const res = await fetch(
             "/api/audio/" + encodeURIComponent(msg.audio_file),
           );
           if (!res.ok) throw new Error("not found");
           const audio = new Audio(URL.createObjectURL(await res.blob()));
           audio.play();
         } catch {
           playBtn.disabled = true;
           playBtn.textContent = "🚫 音频已过期";
         }
       };
       div.appendChild(playBtn);
     } else if (msg.audio_expired) {
       const span = document.createElement("span");
       span.className = "audio-expired";
       span.textContent = "🚫 音频已过期";
       div.appendChild(span);
     }
   }
   ```

   - 服务端兜底处理"刷新页面"的主路径（按钮一开始就不渲染）
   - 前端兜底处理"页面长开、刚被清理"的边界情况（按钮点击降级）

   **新增 Message 字段：**

   ```go
   type Message struct {
       // ... 已有字段 ...
       AudioFile    string `json:"audio_file,omitempty"`
       AudioExpired bool   `json:"audio_expired,omitempty"` // 服务端 stat 失败时置 true
   }
   ```

   `AudioExpired` 不持久化（只在响应时计算），盘上仍只保留原始 `audio_file`。

5. **澄清：与 `RetentionPolicy.EventCompactAfter` 无关**

   现有 `internal/agent/lifecycle.go` 的 `RetentionPolicy` 是给 episode trace / memory chunk 用的（默认 7d / 30d / 90d），**与 chat_history 和音频归档是独立两套**。本设计的音频清理按"数量 / 总大小"滚动，不按时间。如果未来需要按时间清理音频，可以在 `[audio_archive]` 增加 `max_age = "30d"` 配置，与现有 `parseRetentionDuration` 复用解析逻辑。

---

### 4. 实时消息推送 — SSE 端点

**目标：** 语音对话时，网页实时收到新消息，体验与流式聊天一致。

**新增端点：** `GET /api/events` (Server-Sent Events)

```go
// internal/agent/server.go

type Server struct {
    // ... 现有字段 ...

    // 新增：消息广播通道
    eventBroadcaster *EventBroadcaster
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
    // 设置 SSE headers
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
        return
    }

    // 订阅新消息
    ch := s.eventBroadcaster.Subscribe()
    defer s.eventBroadcaster.Unsubscribe(ch)

    ctx := r.Context()
    for {
        select {
        case <-ctx.Done():
            return
        case msg := <-ch:
            data, _ := json.Marshal(msg)
            fmt.Fprintf(w, "data: %s\n\n", data)
            flusher.Flush()
        }
    }
}
```

**EventBroadcaster 实现：**

```go
type EventBroadcaster struct {
    mu          sync.RWMutex
    subscribers map[chan Message]struct{}
}

func (b *EventBroadcaster) Subscribe() chan Message {
    ch := make(chan Message, 16)
    b.mu.Lock()
    b.subscribers[ch] = struct{}{}
    b.mu.Unlock()
    return ch
}

func (b *EventBroadcaster) Unsubscribe(ch chan Message) {
    b.mu.Lock()
    delete(b.subscribers, ch)
    close(ch)
    b.mu.Unlock()
}

func (b *EventBroadcaster) Broadcast(msg Message) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for ch := range b.subscribers {
        select {
        case ch <- msg:
        default:
            // 订阅者消费慢，跳过（避免阻塞）
        }
    }
}
```

**集成点 1 — ChatHistoryStore：**

```go
// internal/agent/chat_history.go

type ChatHistoryStore struct {
    // ... 现有字段 ...
    onNewMessage func(Message)  // 新增回调
}

func (s *ChatHistoryStore) Append(ctx context.Context, message Message) error {
    // ... 现有写入逻辑 ...

    // 广播新消息
    if s.onNewMessage != nil {
        s.onNewMessage(message)
    }
    return nil
}
```

**集成点 2 — AudioDialog：**

```go
// internal/agent/audio_dialog.go

type AudioDialog struct {
    // ... 现有字段 ...
    onNewMessage func(Message)  // 新增回调
}

// 在 ProcessUtterance 和 RunAgentTurn 写入 history 后调用
func (d *AudioDialog) notifyNewMessage(msg Message) {
    if d.onNewMessage != nil {
        d.onNewMessage(msg)
    }
}
```

**连接回路：**

```go
// cmd/daemon/main.go

func runAudioMode(cfg agent.Config, runtime *agent.Runtime, server *agent.Server) {
    dialog, _ := agent.NewAudioDialog(cfg)

    // 连接 AudioDialog → EventBroadcaster
    dialog.SetOnNewMessage(func(msg agent.Message) {
        server.BroadcastMessage(msg)
    })

    // ... 启动语音循环 ...
}
```

---

### 5. 网页 UI 适配

**前端新增 SSE 订阅：** `server.go` 内嵌的 HTML/JS

```js
// 页面加载后订阅事件流
const eventSource = new EventSource("/api/events");

eventSource.onmessage = function (e) {
  const data = JSON.parse(e.data);
  if (data.type === "user" || data.type === "assistant") {
    appendMessageToHistory(data);
  }
};

eventSource.onerror = function () {
  console.error("SSE connection lost, retrying...");
  // 浏览器会自动重连
};
```

**消息渲染适配：**

```js
function renderMessage(msg) {
  const div = document.createElement("div");
  div.className = msg.type;

  // 语音消息显示图标
  if (msg.source === "voice") {
    const icon = document.createElement("span");
    icon.textContent = "🎤 ";
    div.appendChild(icon);
  }

  // 显示转写文字
  const text = document.createElement("span");
  text.textContent = msg.content;
  div.appendChild(text);

  // 如果有音频文件，显示播放按钮
  if (msg.audio_file) {
    const playBtn = document.createElement("button");
    playBtn.textContent = "▶️ 播放原始音频";
    playBtn.onclick = () => {
      const audio = new Audio(
        "/api/audio/" + encodeURIComponent(msg.audio_file),
      );
      audio.play();
    };
    div.appendChild(playBtn);
  }

  historyEl.appendChild(div);
}
```

**新增音频播放端点：** `GET /api/audio/{filename}`

```go
func (s *Server) handleAudioFile(w http.ResponseWriter, r *http.Request) {
    filename := strings.TrimPrefix(r.URL.Path, "/api/audio/")

    // 安全检查：只允许访问配置的音频目录
    audioDir := s.runtime.config.AudioArchive.StoragePath
    fullPath := filepath.Join(audioDir, filepath.Base(filename))

    if !strings.HasPrefix(fullPath, audioDir) {
        http.Error(w, "Forbidden", http.StatusForbidden)
        return
    }

    http.ServeFile(w, r, fullPath)
}
```

**路由注册：**

```go
// internal/agent/server.go NewServer()

mux.HandleFunc("/api/events", s.handleEvents)
mux.HandleFunc("/api/audio/", s.handleAudioFile)
```

---

## 兼容性与硬件支持

### 唤醒触发源兼容性

当前 GPIO 唤醒机制是通用的：

```go
// cmd/daemon/main.go
var wakeupGPIOPins = []int{33, 32}

watchers, _ := startWakeupWatchers(newWatcher, func() {
    // GPIO 中断回调，不区分信号来源
    events <- voiceEventWakeup
})
```

**支持的触发源：**

1. **ASRPro 2.0 唤醒词** → 检测到唤醒词后拉高 GPIO 33
2. **物理按键** → 按下时拉高 GPIO 32
3. **未来扩展** → 任何能产生 GPIO 中断的硬件

软件侧无需区分触发源，都进入同一个 `runVoiceSession` 流程。

### 现有功能不受影响

- **流式聊天：** `/api/chat?stream=1` 继续工作
- **工具调用：** `/api/tools` 端点和 HTTP API 不变
- **Phone Bridge：** WebSocket 端点 `/api/phone-bridge` 不受影响
- **历史记录：** `/api/history` 端点继续返回完整历史（包含新字段）

---

## 实现阶段划分

### Phase 1: 架构解耦（基础）

- [ ] 修改 `main.go`，HTTP server 常驻后台
- [ ] 验证语音模式下网页可访问

### Phase 2: 会话历史扩展

- [ ] 扩展 `Message` 结构，增加 `source`、`audio_file` 等字段
- [ ] `AudioDialog` 写入 history 时标记 `source = "voice"`
- [ ] 验证 `/api/history` 返回新字段

### Phase 3: 实时推送

- [ ] 实现 `EventBroadcaster`
- [ ] 新增 `/api/events` SSE 端点
- [ ] 连接 `ChatHistoryStore.Append()` → `Broadcaster.Broadcast()`
- [ ] 连接 `AudioDialog` → `Broadcaster`
- [ ] 前端订阅 SSE，实时更新 UI

### Phase 4: 音频归档（可选）

- [ ] 新增配置项 `[audio_archive]`
- [ ] VAD 完成后保存 WAV 文件
- [ ] 实现滚动清理逻辑
- [ ] 新增 `/api/audio/{filename}` 端点
- [ ] 前端显示播放按钮

### Phase 5: 硬件集成测试

- [ ] ASRPro 2.0 刷写唤醒词固件
- [ ] 接线到 Luckfox GPIO 33
- [ ] 端到端测试：唤醒词 → 语音对话 → 网页实时显示

---

## 风险与限制

### 存储空间

- Luckfox Pico Zero 用户分区有限（几十到几百 MB）
- 音频归档启用后需监控磁盘使用
- 缓解：默认关闭，滚动清理机制

### 并发负载

- SSE 长连接增加内存占用（每个连接 ~16KB 缓冲）
- 预期：设备端通常只有 1-2 个网页客户端，影响可控

### 网络依赖

- SSE 需要稳定网络连接
- 断线后浏览器会自动重连，历史记录从 `/api/history` 补全

---

## 未来扩展

### App 集成

当前设计基于 HTTP API + SSE，未来 app 集成路径：

1. App 通过 WebSocket 或 HTTP/2 连接同一套 API
2. 订阅 `/api/events` 或自定义协议
3. 会话历史存储层无需改动

### 多模态输入

架构支持未来扩展更多输入源：

- 图像输入（摄像头 + 视觉模型）
- 屏幕共享（HDMI 采集已集成）
- 都通过统一的 `Message` 结构和事件推送

---

## 参考

- 现有代码：`cmd/daemon/main.go`、`internal/agent/server.go`、`internal/agent/audio_dialog.go`
- 硬件文档：`docs/01-getting-started/hardware.md`
- GPIO 唤醒示例：`src/example_wakeup.cpp`
