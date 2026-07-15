# Aiden 存储空间占用清单

## 概览

本文档记录 Aiden 固件当前所有会写入磁盘的组件，用于指导存储空间管理和清理策略设计。

**主要存储分区**：

- `/userdata` - 用户数据、配置、日志、归档
- `/var/log` - 系统和应用日志
- `/tmp` - 临时文件

---

## 一、日志文件

### 1.1 Agent 主日志

**路径**: `/var/log/agent/agent.log`

**写入位置**:

- `overlay/etc/init.d/S53agent` (init 脚本重定向)
- Go agent 的 stdout/stderr 被重定向到此文件

**增长特性**:

- 持续写入，无自动轮转
- 包含所有 [INFO] [WARN] [ERROR] [DEBUG] 级别日志
- 包含启动、运行时错误、工具调用等

**当前清理机制**: 无

**建议清理策略**:

- 实现日志轮转（如 logrotate）
- 或限制最大文件大小（如 10MB），循环覆盖
- 保留最近 7 天

---

### 1.2 LLM HTTP 日志

**路径**: `/userdata/agent/log/llm-http-*.log`

**文件命名**:

- 格式 1: `llm-http-YYYYMMDDHHmm.log` (按时间)
- 格式 2: `llm-http-<session-id>.log` (按会话)

**写入位置**: `src/agent/internal/agent/openai_compatible_model.go:204`

```go
file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
```

**内容**:

- 每次 LLM API 调用的完整 HTTP 请求和响应
- 包含 prompt、completion、token 统计等
- 用于调试和分析

**增长特性**:

- 每次对话会产生多个请求/响应对
- 单个文件可能达到数 MB（取决于对话长度）
- 长期运行会积累大量文件

**当前清理机制**: `src/agent/internal/agent/logger.go:41-79`

- Agent 启动时清理
- 默认保留天数：由配置决定（`llm_http_retention_days`）
- 清理逻辑：删除超过保留天数的 `llm-http-*.log` 文件

**配置项**:

```toml
[log]
llm_http_retention_days = 3  # 默认值
```

**预估占用**:

- 每次对话：0.5-2 MB（取决于 context 大小）
- 每天 20 次对话：10-40 MB
- 3 天保留：30-120 MB

---

## 二、音频归档

### 2.1 用户语音录音

**路径**: `/userdata/agent/audio_archive/msg_*.wav`

**文件命名**: `msg_{timestamp}_{uuid8}.wav`

**写入位置**: `src/agent/internal/agent/audio_archive.go:54`

```go
if err := writeWAVFile(filePath, samples, sampleRate); err != nil
```

**内容**:

- 用户每次语音输入的 WAV 文件
- 16-bit PCM, 单声道
- 采样率：16000 Hz（通常）

**增长特性**:

- 每次语音交互产生 1 个文件
- 单个文件大小：10-20 秒录音 ≈ 300-600 KB
- 持续使用会快速积累

**当前清理机制**: `src/agent/internal/agent/audio_archive.go:68-130`

- 写入新文件后自动触发清理
- 按修改时间排序，删除最旧的文件
- 两个限制条件（任一超过即清理）：
  - `max_files`: 最大文件数（默认 100）
  - `max_size_mb`: 最大总大小（默认 500 MB）

**配置项**:

```toml
[audio_archive]
enabled = true
storage_path = "/userdata/agent/audio_archive"
max_files = 100
max_size_mb = 500
```

**预估占用**:

- 平均每个文件：500 KB
- 100 个文件上限：50 MB
- 500 MB 上限：理论最大值

**可优化点**:

- 配置项允许调整，降级模式可禁用
- 清理策略已经比较完善

---

## 三、会话记忆存储

### 3.1 会话事件日志

**路径**: `/userdata/agent/memory/session/events.jsonl`

**写入位置**: `src/agent/internal/agent/session_memory.go` (多处)

**内容**:

- 每次用户消息、Agent 回复、工具调用等事件
- JSONL 格式（每行一个 JSON）
- 包含完整的消息内容（但截图 base64 会被剥离）

**增长特性**:

- 持续追加写入
- 每轮对话增加数行到数十行
- 包含所有交互历史

**当前清理机制**: 会话轮转和压缩

- 当事件数超过阈值时，压缩为 chunk
- 压缩后清空 `events.jsonl`
- 详见会话归档部分

---

### 3.2 会话 Chunk 归档

**路径**: `/userdata/agent/memory/session/chunks/*.json`

**写入位置**: `src/agent/internal/agent/session_memory.go:243`

**内容**:

- 压缩后的会话片段
- 包含事件摘要、时间范围、token 统计等

**增长特性**:

- 随着对话进行逐渐增加
- 单个 chunk：几 KB 到几十 KB

**当前清理机制**: 会话轮转

- 当启动新会话时，旧会话会被移动到 `session_archive/`
- 不会无限增长

---

### 3.3 会话摘要

**路径**: `/userdata/agent/memory/session/summary.md`

**写入位置**: `src/agent/internal/agent/session_memory.go:272`

**内容**:

- 当前会话的滚动摘要
- Markdown 格式
- 包含最近 N 个 chunk 的摘要

**增长特性**:

- 有界增长（最多保留配置的 chunk 数）
- 单文件：几 KB 到几十 KB

**当前清理机制**: 自动控制大小

- `summary_max_chunks` 配置项限制
- 超过限制的 chunk 移动到 `summary_archive.md`

---

### 3.4 会话归档

**路径**: `/userdata/agent/memory/session_archive/{session_id}/`

**写入位置**: `src/agent/internal/agent/memory.go` (会话轮转时)

**内容**:

- 旧会话的完整数据
- 包含 events.jsonl、chunks/、summary.md 等

**增长特性**:

- 每次会话轮转产生一个归档目录
- 单个归档：几 MB 到几十 MB（取决于会话长度）
- 长期使用会积累大量归档

**当前清理机制**: 无自动清理

**建议清理策略**:

- 保留最近 N 个会话（如 10 个）
- 或保留最近 X 天的会话（如 30 天）
- 压缩旧归档为 tar.gz

**预估占用**:

- 每个会话归档：5-20 MB
- 10 个会话：50-200 MB
- 30 天（假设每天 1 个会话）：150-600 MB

---

## 四、长期记忆存储

### 4.1 记忆条目文件

**路径**: `/userdata/agent/memory/long_term/{id}.md`

**写入位置**: `src/agent/internal/agent/long_term_memory.go` (多处)

**内容**:

- 用户保存的长期记忆（preferences、rules、facts 等）
- Markdown 格式，带 YAML frontmatter

**增长特性**:

- 缓慢增长
- 单个文件：1-5 KB
- 总量通常不超过几十个文件

**当前清理机制**: 无需清理（用户主动保存）

**预估占用**: < 1 MB（通常）

---

## 五、临时文件

### 5.1 用户文件报告

**路径**: `/tmp/user_files_regenerate.log`

**写入位置**: `src/agent/internal/agent/user_files.go:73`

**内容**:

- 用户文件扫描脚本的输出

**增长特性**:

- 每次调用覆盖
- 单文件：几 KB

**当前清理机制**: 自动覆盖

**预估占用**: < 1 MB

---

### 5.2 其他临时文件

**可能路径**: `/tmp/`, `/var/tmp/`

**来源**:

- 工具调用产生的临时文件
- 脚本执行过程中的中间文件
- 测试文件（如前面提到的 `dd` 命令产生的）

**建议**:

- 定期清理 `/tmp/` 下的旧文件
- 或依赖系统的 tmpfs 自动清理

---

## 六、Episode 导出（Telemetry）

### 6.1 Episode 事件

**路径**: `/userdata/agent/memory/episode/{task_id}/`

**写入位置**: `src/agent/internal/agent/episode_exporter.go`

**内容**:

- Task episode 的元数据和事件
- `episode.yaml` + `events.jsonl`
- 用于遥测和分析

**增长特性**:

- 每个任务产生一个目录
- 单个 episode：几 KB 到几十 KB

**当前清理机制**: 导出到 Langfuse 后可能保留

**建议清理策略**:

- 导出成功后删除本地副本
- 或保留最近 N 个

**预估占用**: 取决于任务量，通常 < 10 MB

---

## 七、配置文件

### 7.1 Agent 配置

**路径**: `/userdata/agent/agent.toml`

**写入位置**: 用户手动编辑或通过 API 更新

**内容**:

- Agent 的运行时配置
- LLM 凭证、工具配置、行为设置等

**增长特性**: 静态，除非用户修改

**预估占用**: < 100 KB

---

### 7.2 技能配置

**路径**: `/userdata/agent/skills/*.json` 或 `*.md`

**写入位置**: 技能同步机制写入

**内容**:

- 自定义技能的定义和配置

**增长特性**: 缓慢增长

**预估占用**: < 1 MB

---

### 7.3 其他配置

- Live Activity Board ID: `/userdata/agent/.live_activity_board_id`
- WiFi 配置: `/userdata/wpa_supplicant.conf`
- 各类缓存和状态文件

**预估占用**: < 1 MB

---

## 八、OTA 相关

### 8.1 OTA 升级文件

**路径**: `/userdata/ota/`

**内容**:

- OTA 升级包
- 健康检查标记文件
- 待升级的固件镜像

**增长特性**:

- OTA 升级时产生
- 单个升级包：几十 MB 到上百 MB

**当前清理机制**: OTA 完成后应清理

**建议**:

- 升级成功后立即删除升级包
- 保留健康标记用于回滚

**预估占用**: 0-100 MB（临时）

---

## 九、总占用预估

### 正常运行状态（7 天使用）

| 类别                    | 预估占用       |
| ----------------------- | -------------- |
| Agent 主日志            | 5-10 MB        |
| LLM HTTP 日志 (3天保留) | 30-120 MB      |
| 音频归档 (100文件上限)  | 50 MB          |
| 当前会话                | 5-20 MB        |
| 会话归档 (7个)          | 35-140 MB      |
| 长期记忆                | 1 MB           |
| Episode 导出            | 5-10 MB        |
| 配置文件                | 1 MB           |
| 临时文件                | 5 MB           |
| **总计**                | **137-362 MB** |

### 长期运行状态（30 天使用，无清理）

| 类别                    | 预估占用        |
| ----------------------- | --------------- |
| Agent 主日志            | 20-50 MB        |
| LLM HTTP 日志 (3天保留) | 30-120 MB       |
| 音频归档 (500MB上限)    | 500 MB          |
| 当前会话                | 5-20 MB         |
| 会话归档 (30个)         | 150-600 MB      |
| 长期记忆                | 2 MB            |
| Episode 导出            | 20-50 MB        |
| 配置文件                | 1 MB            |
| 临时文件                | 10 MB           |
| **总计**                | **738-1353 MB** |

**关键观察**:

1. **最大占用来源**：音频归档（500 MB 上限）和会话归档（无上限）
2. **快速增长项**：LLM HTTP 日志、音频归档
3. **无清理机制**：Agent 主日志、会话归档、Episode 导出

---

## 十、清理优先级建议

基于增长速度、重要性和恢复难度，建议的清理优先级：

### 优先级 1（最优先清理）

1. **LLM HTTP 日志** - 快速增长，可再生（通过日志级别控制）
   - 清理：7天前 → 3天前 → 1天前 → 当前会话外所有
2. **临时文件** - 非关键，易清理
   - 清理：`/tmp/` 下所有非当前使用的文件

### 优先级 2（次优先清理）

3. **音频归档** - 快速增长，但用户可能想保留
   - 清理：30天前 → 7天前 → 保留最近 10 个
4. **旧会话归档** - 较大，但可能包含重要记忆
   - 清理：30天前 → 10个会话前

### 优先级 3（保守清理）

5. **Agent 主日志** - 调试关键，但可循环覆盖
   - 清理：限制文件大小到 10MB，循环覆盖
6. **Episode 导出** - 可再生（如果已上传）
   - 清理：已导出成功的

### 不建议清理

- 长期记忆（用户主动保存）
- 当前会话数据
- 配置文件

---

## 十一、配置项总结

当前已有的存储相关配置：

```toml
[log]
llm_http_retention_days = 3  # LLM HTTP 日志保留天数

[audio_archive]
enabled = true
storage_path = "/userdata/agent/audio_archive"
max_files = 100
max_size_mb = 500

[memory.extraction]
summary_max_chunks = 10  # 会话摘要最大 chunk 数
```

**建议新增配置**（在存储监控方案中）：

```toml
[storage]
enabled = true
check_interval_seconds = 300
warning_threshold_mb = 50
critical_threshold_mb = 10
emergency_threshold_mb = 5

[storage.cleanup]
enabled = true
agent_log_max_size_mb = 10
session_archive_max_count = 10
session_archive_retention_days = 30
episode_cleanup_after_export = true
```

---

**文档版本**: v1.0  
**最后更新**: 2026-07-15  
**基于代码版本**: commit c6ec5f20
