# Aiden 存储空间耗尽处理方案设计文档

## 一、背景

Aiden 固件运行在嵌入式 Linux 设备（Luckfox Pico Zero）上，存储空间有限。主要存储消耗来源：

- `/var/log/agent/agent.log` - Agent 主日志
- `/userdata/agent/log/llm-http-*.log` - LLM HTTP 交互日志
- `/userdata/agent/memory/` - 会话记忆存储
- `/userdata/agent/audio_archive/` - 音频归档文件

当存储空间耗尽时，可能导致：

- 日志写入失败
- 会话无法保存
- 系统崩溃

## 二、设计目标

1. **分层检测**：在多个关键点检测存储空间
2. **分级响应**：根据严重程度采取不同措施
3. **用户友好**：通过语音、Web UI 等方式友好提示
4. **自动恢复**：主动清理过期数据，释放空间
5. **优雅降级**：而非崩溃，保证核心功能可用

## 三、检测层次

### 3.1 启动时检查

Agent 启动时检查关键分区的存储空间：

- `/userdata` 分区
- `/var/log` 分区

如果空间低于阈值（默认 10MB），记录警告并考虑进入降级模式。

### 3.2 写操作前检查

在关键写入操作前检查空间：

- 日志写入前（logger.go）
- 音频归档前（audio_archive.go）
- Memory/Session 存储前（memory.go）

如果空间不足，跳过该次写入并触发清理。

### 3.3 后台监控

启动后台 goroutine，定期检查磁盘使用情况：

- 检查间隔：默认 5 分钟（可配置）
- 当空间低于警戒线时，主动触发清理
- 记录每次检查结果到日志

## 四、处理策略（分级响应）

### 4.1 绿色区域（剩余空间 > 50MB）

**状态**：正常运行

**行为**：

- 所有功能正常
- 定期清理过期日志（按配置的保留天数）

### 4.2 黄色区域（10MB < 剩余空间 ≤ 50MB）

**状态**：警告

**触发清理**：

1. 删除超过 7 天的 llm-http 日志
2. 删除超过 3 天的 llm-http 日志
3. 清理音频归档（保留最近 10 个文件）
4. 压缩或删除旧的 session 归档

**用户提示**：

- 语音：在下次交互时前置提醒
  - "存储空间不足，已自动清理部分历史数据"
- Web UI：显示黄色警告标志
- 日志：记录警告级别日志

### 4.3 红色区域（5MB < 剩余空间 ≤ 10MB）

**状态**：严重

**进入降级模式**：

- 禁用 llm-http 日志记录（保留 agent.log）
- 禁用音频归档
- 停止 Memory 归档（仅保留当前会话在内存）
- agent.log 限制最大 1MB，循环覆盖

**激进清理**：

1. 删除所有 1 天前的 llm-http 日志
2. 删除所有音频归档（保留最近 3 个）
3. 删除 30 天前的 session 归档

**用户提示**：

- 语音：在当前播放完成后立即提醒
  - "存储空间严重不足，部分功能已暂停。请清理设备存储空间"
- Web UI：显示红色警告，提供一键清理按钮
- LED：快速闪烁红灯（如果硬件支持）

### 4.4 临界区域（剩余空间 ≤ 5MB）

**状态**：紧急

**行为**：

- 立即打断当前播放（如果正在播放）
- 拒绝所有新的语音交互
- 停止录音（如果正在录音）
- 仅保留最基本的系统日志

**最后清理**：

1. 删除所有当前会话外的 llm-http 日志
2. 删除所有音频归档
3. 删除所有可选的 session 归档

**用户提示**：

- 语音：立即播放紧急警告
  - [紧急提示音] + "存储空间已满，无法继续使用。请立即清理设备存储空间或联系技术支持"
- Web UI：全屏红色警告
- LED：持续快速闪烁红灯
- 唤醒按钮：按下时仅播放警告，不进入录音

## 五、语音提醒时机设计

### 5.1 交互状态定义

```
Idle        - 空闲状态
Listening   - 用户正在说话（录音中）
Processing  - LLM 处理中（静默但忙碌）
Playing     - Agent 正在播放回复
```

### 5.2 提醒时机策略

#### 警告级（黄色区域）

**原则**：不打断当前交互，在合适时机插入

**时机**：

- 下一次用户唤醒后、Agent 开始回复**之前**插入
- 或者空闲超过 5 分钟后主动提醒

**示例**：

```
User: "今天天气怎么样"
Agent: "存储空间不足，已自动清理部分数据。今天天气晴朗，气温..."
        ↑ 警告前置              ↑ 正常回复
```

#### 严重级（红色区域）

**原则**：优先提醒，但尽量不打断当前播放

**时机**：

- 如果正在**播放中**：等播放完成后立即提醒
- 如果正在**录音中**：等用户说完，在处理请求前先提醒
- 如果**空闲**：立即语音提醒

**示例 1**（播放中检测到）：

```
Agent: "根据你的需求，我建议..." [播放完成]
Agent: "存储空间严重不足，部分功能已暂停。请清理设备存储空间。"
```

**示例 2**（录音中检测到）：

```
User: "帮我查一下明天的日程"
Agent: [先播放] "存储空间严重不足，部分功能已暂停。"
       [再回复] "明天的日程如下..."
```

#### 紧急级（临界区域）

**原则**：立即打断，因为系统已无法正常工作

**时机**：

- 正在**播放中**：立即打断，播放紧急警告
- 正在**录音中**：立即停止录音，播放警告
- 用户**按唤醒键**：仅播放警告，拒绝录音

**示例 1**（播放中触发）：

```
Agent: "根据你的需求，我建议..." [被打断]
Agent: [紧急音效] "存储空间已满，无法继续使用。请立即清理设备存储空间。"
```

**示例 2**（用户唤醒）：

```
User: [按下唤醒按钮]
Agent: [提示音] "存储空间已满，无法使用。请清理存储空间。"
[不进入录音状态]
```

### 5.3 提示音设计

为了让提醒更自然，加入音效提示：

- **警告级**：轻柔的"叮"
- **严重级**：两声短促的"哔哔"
- **紧急级**：急促的"哔哔哔"（类似设备告警）

播放顺序：`[提示音] + [停顿 200ms] + [语音文本]`

### 5.4 防止重复提醒

- 同级别警告：5 分钟内不重复
- 级别升级：立即提醒
- 紧急级：总是提醒

## 六、自动清理机制

### 6.1 清理优先级（从低到高）

1. 7 天前的 llm-http 日志
2. 3 天前的 llm-http 日志
3. 1 天前的 llm-http 日志
4. 30 天前的音频归档
5. 7 天前的音频归档
6. 30 天前的 session 归档
7. 当前会话外的所有 llm-http 日志
8. 所有音频归档

### 6.2 清理流程

```
1. 检测到存储不足
2. 根据当前区域，选择清理策略
3. 按优先级执行清理
4. 每次清理后重新检查空间
5. 如果仍不足，继续下一级清理
6. 记录清理结果到日志
7. 如果清理成功，通知用户
```

### 6.3 清理后验证

- 清理后立即重新检查剩余空间
- 记录清理释放的字节数
- 如果清理后仍处于警告区域，继续清理
- 清理失败（无可清理内容）时，进入降级模式

## 七、用户引导

### 7.1 语音提示文案

| 级别 | 文案                                                               | 使用场景               |
| ---- | ------------------------------------------------------------------ | ---------------------- |
| 警告 | "存储空间不足，已自动清理部分数据"                                 | 黄色区域，清理成功     |
| 警告 | "存储空间不足，建议清理部分数据"                                   | 黄色区域，无可清理内容 |
| 严重 | "存储空间严重不足，部分功能已暂停。请清理设备存储空间"             | 红色区域               |
| 紧急 | "存储空间已满，无法继续使用。请立即清理设备存储空间或联系技术支持" | 临界区域               |

### 7.2 Web UI 提示

#### 存储状态显示

在配置页面显著位置显示：

```
┌─────────────────────────────────────┐
│ 📊 存储空间                          │
│                                     │
│ /userdata:  [████████░░] 82% 已使用 │
│ 剩余: 45 MB / 256 MB                │
│                                     │
│ ⚠️  存储空间不足                     │
│ 建议清理部分历史数据以释放空间        │
│                                     │
│ [一键清理] [查看详情]                │
└─────────────────────────────────────┘
```

#### 一键清理功能

提供清理选项：

- ☑️ 清理 LLM HTTP 日志（预计释放: 30 MB）
- ☑️ 清理音频归档（预计释放: 15 MB）
- ☑️ 清理会话归档（预计释放: 8 MB）
- 总计可释放: 53 MB

点击后显示清理进度和结果。

### 7.3 SSH/串口提示

agent.log 中记录详细信息：

```
[WARN] storage check: /userdata 82% used (45MB available, 211MB used)
[INFO] storage cleanup: removing llm-http logs older than 7 days
[INFO] storage cleanup: freed 32MB, /userdata now 70% used (77MB available)
```

提供诊断命令：

```bash
# 查看存储状态
agent config storage-status

# 手动触发清理
agent config storage-cleanup

# 查看清理历史
agent config storage-cleanup-history
```

## 八、实现架构

### 8.1 核心模块

```go
// 存储监控器
type StorageMonitor struct {
    checkInterval      time.Duration
    warningThreshold   uint64  // 50MB
    criticalThreshold  uint64  // 10MB
    emergencyThreshold uint64  // 5MB

    audioDialog        *AudioDialog
    logger             *Logger
    cleaners           []StorageCleaner

    lastAlertTime      time.Time
    lastAlertLevel     string
    minAlertInterval   time.Duration  // 5分钟
}

// 清理器接口
type StorageCleaner interface {
    Name() string
    Priority() int  // 数字越小优先级越高
    EstimateReclaimable() (uint64, error)
    Clean() (freed uint64, err error)
}

// 具体清理器实现
type LLMHTTPLogCleaner struct {
    logDir       string
    retentionAge time.Duration
}

type AudioArchiveCleaner struct {
    archiveDir  string
    maxFiles    int
}

type SessionArchiveCleaner struct {
    archiveDir   string
    retentionAge time.Duration
}
```

### 8.2 交互状态管理

```go
type InteractionState int

const (
    StateIdle       InteractionState = iota
    StateListening
    StateProcessing
    StatePlaying
)

type AudioDialog struct {
    // 现有字段...

    interactionState InteractionState
    stateMu          sync.Mutex
    pendingAlert     *StorageAlert
}

func (d *AudioDialog) setState(state InteractionState) {
    d.stateMu.Lock()
    defer d.stateMu.Unlock()

    oldState := d.interactionState
    d.interactionState = state

    // 状态切换时检查是否有待播放的提醒
    if d.pendingAlert != nil && d.canPlayAlert(oldState, state) {
        d.playAlertNow(d.pendingAlert)
        d.pendingAlert = nil
    }
}

func (d *AudioDialog) scheduleAlert(alert StorageAlert) {
    d.stateMu.Lock()
    defer d.stateMu.Unlock()

    if alert.Level == "emergency" {
        // 紧急：立即打断
        d.interruptAndPlayAlert(alert)
    } else {
        // 非紧急：等待合适时机
        d.pendingAlert = &alert
    }
}
```

### 8.3 集成点

1. **main.go 启动时**
   - 初始化 StorageMonitor
   - 执行启动检查
   - 启动后台监控 goroutine

2. **logger.go**
   - 写日志前检查空间
   - 如果空间不足，跳过或降级

3. **audio_archive.go**
   - 归档前检查空间
   - 如果空间不足，跳过归档

4. **memory.go**
   - 存储前检查空间
   - 降级模式下仅保留内存中的数据

5. **AudioDialog**
   - 集成状态管理
   - 实现提醒调度逻辑

## 九、配置项

在 `config.toml` 中添加：

```toml
[storage]
# 存储监控开关
enabled = true

# 检查间隔（秒）
check_interval_seconds = 300  # 5分钟

# 空间阈值（MB）
warning_threshold_mb = 50
critical_threshold_mb = 10
emergency_threshold_mb = 5

# 降级模式配置
[storage.degraded_mode]
disable_llm_http_log = true
disable_audio_archive = true
max_agent_log_mb = 1

# 自动清理配置
[storage.cleanup]
enabled = true

# 清理策略：LLM HTTP 日志保留天数（按优先级）
llm_http_log_retention_days = [7, 3, 1, 0]

# 音频归档保留天数
audio_archive_retention_days = [30, 7, 0]

# Session 归档保留天数
session_archive_retention_days = [30]

# 清理后的最小重试间隔（秒）
cleanup_retry_interval_seconds = 60
```

## 十、监控和 API

### 10.1 HTTP API

**GET /api/storage/status**

返回存储状态：

```json
{
  "partitions": {
    "userdata": {
      "total_mb": 256,
      "used_mb": 211,
      "available_mb": 45,
      "percent_used": 82.4
    },
    "var_log": {
      "total_mb": 128,
      "used_mb": 100,
      "available_mb": 28,
      "percent_used": 78.1
    }
  },
  "alert_level": "warning",
  "degraded_mode": false,
  "last_cleanup": "2026-07-15T10:30:00Z",
  "last_cleanup_freed_mb": 32,
  "cleanup_history": [
    {
      "timestamp": "2026-07-15T10:30:00Z",
      "cleaner": "llm_http_log",
      "freed_mb": 32,
      "status": "success"
    }
  ]
}
```

**POST /api/storage/cleanup**

手动触发清理：

```json
{
  "force": false, // 是否强制清理（忽略保留策略）
  "targets": ["llm_http_log", "audio_archive"] // 可选，指定清理目标
}
```

返回：

```json
{
  "success": true,
  "freed_mb": 45,
  "cleaners_run": [
    {
      "name": "llm_http_log",
      "freed_mb": 32,
      "status": "success"
    },
    {
      "name": "audio_archive",
      "freed_mb": 13,
      "status": "success"
    }
  ]
}
```

### 10.2 日志记录

每次检查和清理都记录到 agent.log：

```
[INFO] storage_monitor: check started
[WARN] storage_monitor: /userdata 82% used (45MB available), threshold: 50MB
[INFO] storage_cleanup: running cleaner llm_http_log (priority 1)
[INFO] storage_cleanup: llm_http_log freed 32MB
[INFO] storage_monitor: cleanup successful, /userdata now 70% used (77MB available)
[INFO] storage_alert: scheduled warning alert for next interaction
```

## 十一、测试策略

### 11.1 单元测试

- 各个 Cleaner 的清理逻辑
- 空间检测和阈值判断
- 状态切换逻辑
- 提醒调度逻辑

### 11.2 集成测试

- 模拟存储空间不足的场景
- 验证清理流程的完整性
- 验证降级模式的触发和恢复

### 11.3 手动测试

在设备上测试：

```bash
# 1. 填充存储空间至接近临界
dd if=/dev/zero of=/userdata/test.bin bs=1M count=200

# 2. 触发语音交互，观察提醒时机

# 3. 验证自动清理

# 4. 验证 Web UI 显示

# 5. 清理测试文件
rm /userdata/test.bin
```

## 十二、上线计划

### 12.1 Phase 1：核心功能（1-2 天）

- [ ] 实现 StorageMonitor
- [ ] 实现基础 Cleaners（LLMHTTPLog, AudioArchive）
- [ ] 集成到启动流程和后台监控
- [ ] 添加配置项
- [ ] 单元测试

### 12.2 Phase 2：语音提醒（1 天）

- [ ] 实现交互状态管理
- [ ] 实现提醒调度逻辑
- [ ] 集成到 AudioDialog
- [ ] 添加提示音资源
- [ ] 测试不同场景的提醒时机

### 12.3 Phase 3：Web UI（1 天）

- [ ] 实现存储状态显示
- [ ] 实现一键清理功能
- [ ] 实现清理历史查看
- [ ] UI/UX 优化

### 12.4 Phase 4：测试和优化（1 天）

- [ ] 集成测试
- [ ] 设备上实测
- [ ] 性能优化
- [ ] 文档完善

## 十三、未来优化方向

1. **智能清理**：基于使用频率智能决定清理优先级
2. **压缩存储**：对归档数据进行压缩而非删除
3. **远程备份**：提供云端备份选项，本地仅保留近期数据
4. **预测性告警**：基于历史使用趋势，提前预警存储空间问题
5. **用户自定义**：允许用户配置清理策略和保留时长

---

**文档版本**: v1.0  
**最后更新**: 2026-07-15  
**作者**: Claude & Jacob
