---
sidebar_position: 12
---

# 通知持久化与自动记忆

**状态：** 提案

**日期：** 2026-08-20

## 决策

`ble_service` 只生产规范化通知事件。Agent 侧用一个 `NotificationContext` 模块统一完成消费、脱敏、去重、落盘和增量读取。

通知 Memory 使用独立的 `MemoryWorker` 实例。Worker 内部接入 `NotificationMemoryProcessor`，把短期结论写入 Temporary Memory，把稳定结论写入 Long-Term Memory。两个 Store 由 `recall_memory` 统一检索。

## 组件职责

| 模块 | 职责 |
| --- | --- |
| `ble_service` | 接收 iOS/Android 通知、规范化、分配事件 ID、维护短期 event ring |
| `NotificationContext` | 消费 `events_since`、脱敏去重、JSONL 落盘、source cursor、memory cursor、查询和清理 |
| `MemoryWorker` | 通用的 timer、唤醒、批处理、取消、重试和前台任务避让 |
| `NotificationMemoryProcessor` | 判断通知是否值得记忆，提炼 Candidate，执行 policy 和 Memory resolve |
| `TemporaryMemoryStore` | 保存有明确过期时间的短期结论，供 `recall_memory` 检索 |

`NotificationMemoryProcessor` 是 Worker 的场景处理器，不是独立进程。Episode Memory 使用另一个 Worker 实例和另一个 Processor；两者在同一 Agent 进程内做逻辑隔离，状态、队列、timer、cursor、retry 和取消上下文彼此独立。

两个 Worker 只共享模型客户端、全局存储监控，以及本方案新增的 `MemoryRunGate`。`MemoryRunGate` 只包围后台模型调用，使 Episode 和 Notification 的后台 LLM 调用串行执行；它不合并两个 Worker 的 timer、cursor 或 retry。一个场景失败时只进入自己的退避重试，不会改变另一个场景的处理状态。进程级故障由各自的持久化状态恢复；需要故障域完全分离时，再拆成独立 daemon。

## 生命周期

通知本身不是 Memory。Processor 必须先判断价值和有效期，再互斥地写入 Temporary Memory 或 Long-Term Memory。

| 结果 | 保存位置 | 默认生命周期 |
| --- | --- | --- |
| `ignore`：营销、验证码、敏感通知 | 仅作短暂处理，随后删除 | 不进入 Memory |
| `temporary`：一次性提醒、物流状态、临时改期 | `/userdata/agent/memory/temporary/` | 按事件有效期，默认 7 天 |
| `long_term`：稳定偏好、规则、重复事实 | `/userdata/agent/memory/long_term/` | 自动推断默认 90 天 |
| 用户明确要求记住的偏好或规则 | `/userdata/agent/memory/long_term/` | 不设 TTL，直到更新或删除 |

`NotificationContext` 只保存原始通知证据。Temporary Memory 与 `long_term/` 同级，保存已经提炼的短期结论；Worker 只在当前批次中持有内存对象。

自动推断的 Long-Term Memory 在新证据 reinforce 时刷新 TTL。通知原文过期不会自动删除已经提炼的 Memory；Temporary 和 Long-Term Memory 都只保留事件 ID 等最小来源信息。

## 主流程

```text
手机通知源
  -> ble_service
  -> NotificationContext
       consume -> sanitize/dedupe -> append JSONL
  -> MemoryWorker + NotificationMemoryProcessor
       read pending -> debounce -> extract -> resolve
       -> Temporary Memory（短期结论）
       -> Long-Term Memory（稳定结论）
```

`ble_service` 不调用 LLM，也不感知通知 retention、查询方式或 Memory policy。

## NotificationContext

### 对外接口

NotificationContext 对 runtime 暴露四个动作：

```text
Consume(limit)              从 events_since 拉取并可靠落盘
ReadPending(limit)          读取尚未完成 Memory 处理的通知
CommitProcessed(batch)      Memory 写入成功后推进 memory cursor
Cleanup(policy)             按 cursor 和存储水位清理已结算数据
```

`source cursor` 和 `memory cursor` 都由 NotificationContext 持久化。调用方不需要理解 JSONL 分片、generation 或文件清理细节。

### 存储

```text
/userdata/agent/memory/notifications/
├── events/2026-08-20.jsonl
├── state.json
└── fingerprints.json
```

`events/` 是通知事实来源。`state.json` 保存上游 generation、source cursor、memory cursor、最早保留事件和 gap 状态。`fingerprints.json` 只用于重放去重，可重建。

### 默认值

| 配置 | 默认值 |
| --- | --- |
| 通知 Context retention | 14 天 |
| Context 总容量 | 16 MiB |
| 单文件滚动阈值 | 1 MiB |
| 单条正文上限 | 4 KiB |
| 单次上游消费 | 100 条 |
| Android 单批事件 | 8 条 |
| Worker 单批处理 | 20 条 |
| 首次 debounce | 30 秒 |

持久化采用 append + sync。写入成功后才推进 source cursor；重复 `source_event_id` 直接确认，不重复追加。掉电恢复时修复最后一条不完整 JSON 行。

### StorageMonitor

StorageMonitor 负责全局水位，NotificationContext 负责原始通知的本地配额和 cursor-aware 清理。清理必须调用 `Cleanup(policy)`，不能由监控器直接删除 JSONL。

| 水位 | 行为 |
| --- | --- |
| Normal | 清理超过 14 天且已完成处理的数据，允许落盘 |
| Warning | 清理超过 7 天且已完成处理的数据，允许落盘和提炼 |
| Critical | 清理超过 1 天且已完成处理的数据，暂停新通知落盘 |
| Emergency | 先记录 `storage_emergency` gap，再清理；不删除已生成的 Long-Term Memory |

未完成处理的数据不能静默删除。写入失败时保持 source cursor，StorageMonitor 恢复后继续重试。

这部分不能直接复用当前 Episode Storage 清理，因为 Episode 目前没有为
`memory/episodes/` 注册常规 retention cleaner：原始 `episode.yaml`、
`events.jsonl` 和 artifacts 会一直保留，直到显式清空整套 Memory。Episode
现有可复用的只有两种机制：处理账本只保留最近 64 条 `done/ignored` 终态，
以及提炼后的 Device Memory 在召回时按 `expires_at` 过滤；后者当前也不会
物理删除过期文件。

Notification 复用这些基础机制，但清理职责分开：

- Worker 状态复用 Episode 的 cursor 和终态账本裁剪模式；
- Temporary Memory 复用 Memory Store 的 `expires_at` 判断，并由
  `TemporaryMemoryCleaner` 物理删除过期记录、重建索引；
- 原始通知由 `NotificationContext.Cleanup(policy)` 清理，只有 memory cursor
  已结算的数据才能按保留期删除；StorageMonitor 只负责触发该接口。

原始通知的数量、隐私敏感度和写入频率都高于 Episode，因此不能沿用 Episode
当前“不做常规清理”的行为。

## 事件模型与消费

```json
{
  "id": "42",
  "source": "ios_ancs",
  "device_id": "phone-a",
  "source_event_id": "ancs:123456:added",
  "notification_uid": 123456,
  "event": "added",
  "app_identifier": "com.example.calendar",
  "title": "Weekly review",
  "message": "Moved to Wednesday 14:00",
  "received_at": "2026-08-19T06:00:00Z",
  "metadata_complete": true
}
```

`id` 是开发板本地游标 ID，`source_event_id` 是跨重试的幂等键。`removed` 事件也必须落盘，因为它可能使临时结论失效。

NotificationContext 直接调用：

```json
{
  "op": "events_since",
  "since": "42",
  "generation": "<generation>",
  "limit": 100
}
```

generation 变化或 ring 已截断时，Context 记录 gap 并从当前 oldest ID 继续。MVP 不承诺生产者长期离线时零丢失，但所有缺口都必须可观测。

## Temporary Memory 与统一 Recall

```text
/userdata/agent/memory/
├── notifications/     # 原始通知、消费状态和 gap
├── temporary/         # 已提炼的短期 Memory
│   ├── index.yaml
│   └── memories/
│       └── tmp_<id>.md
└── long_term/         # 已提炼的长期 Memory
    ├── index.yaml
    └── memories/
        └── mem_<id>.md
```

`temporary/` 复用 `long_term/` 的存储格式：根目录下一个 `index.yaml`，记忆正文保存为带 YAML frontmatter 的 Markdown 文件。它不复用 Episode 的 `episode.yaml + events.jsonl + artifacts/` 布局，因为 Temporary Memory 是可检索结论，不是任务轨迹。

Temporary 和 Long-Term Store 使用相同的 index schema，现有索引字段中的 `expires_at` 可直接用于过期过滤。Temporary Memory 必须包含 `time_scope=temporary`、`expires_at` 和通知 `SourceRefs`；没有明确失效时间时默认 TTL 为 7 天。Temporary ID 使用 `tmp_` 前缀，与 Long-Term Memory 的 `mem_` ID 保持全局不冲突。

Agent 不需要选择 Store，也不新增 recall tool。现有 `recall_memory` 同时搜索两个目录，过滤掉已过期的 Temporary Memory，再合并排序。结果增加 `memory_scope=temporary|long_term` 和 `expires_at`，让 Agent 能区分临时例外和长期基线。

同一主题同时命中时，仍有效的 Temporary Memory 排在 Long-Term Memory 前面，但两个结果都返回。例如“周会通常周二”和“本周改到周三”同时存在时，本周优先使用临时结论，临时记录过期后自动回到长期结论。

Temporary Memory 到期后不再被召回，并由 `TemporaryMemoryCleaner` 删除。若后续证据证明结论已经稳定，Processor 先成功写入或更新 Long-Term Memory，再删除或标记对应 Temporary Memory 为 promoted。

## MemoryWorker 与 Processor

### Episode Memory 的现有实现

Episode Memory consolidation 已合入当前 main，核心类型是 `episodeMemoryWorker`、`episodeMemoryBatchProcessor` interface 和 `episodeMemoryProcessor`。当前流程为：

```text
Episode 完成并落盘
  -> NotifyEpisode
  -> Agent 空闲 5 分钟
  -> 代码硬预筛
  -> 查询相关 Device Memory
  -> 一次后台 LLM 提炼 0～3 个 Candidate
  -> 代码硬校验
  -> create / update / disputed
  -> 写入 Device Memory
```

现有 Worker 已负责：

- 一个可重置 timer，单实例不并行处理多个 batch；
- Episode 完成后的非阻塞唤醒；
- 前台 Agent task 开始时停止 timer 并取消在途后台模型调用；
- batch pending、`NextRunAt`、退避重试、停止和等待退出；
- 默认空闲等待 5 分钟，单批最多处理 5 个 Episode。

现有 Episode Processor 负责：

- 只接收已结束、具有设备动作或非取消结构化错误的 Episode；
- 持久化 `processing -> proposed -> done/ignored/retry` 处理状态；
- 提炼 `procedure`、`navigation`、`calibration`、`failure` 或 `fact`；
- 校验真实 event evidence、Scope 和各类型证据门槛；
- 使用 `episode_id + lesson_key` 保证创建幂等，使用 Memory revision 保护更新；
- 只写 Device Memory，不写 Temporary 或 Long-Term Memory。

### 抽取通用 Worker

本方案不再并列实现第二套 timer 和取消逻辑，而是把现有 `episodeMemoryWorker` 的调度部分抽成通用 `MemoryWorker`。现有 interface 可以直接收敛为：

```go
type MemoryProcessor interface {
    Initialize() error
    NextRunAt(context.Context) (time.Time, error)
    ProcessBatch(context.Context, int, func() bool) (MemoryBatchResult, error)
    LogBatchError(error)
}
```

名称映射：

```text
episodeMemoryWorker          -> MemoryWorker
episodeMemoryBatchProcessor  -> MemoryProcessor
episodeMemoryBatchResult     -> MemoryBatchResult
NotifyEpisode                -> Notify
```

抽取后创建两个独立实例：

| 实例 | Processor | 触发时机 | Batch | 持久化状态 | 写入目标 |
| --- | --- | --- | --- | --- | --- |
| Episode Worker | `EpisodeMemoryProcessor` | Agent 空闲 5 分钟 | 5 个 Episode | Episode consolidation state | Device Memory |
| Notification Worker | `NotificationMemoryProcessor` | 通知落盘后 debounce 30 秒 | 20 条通知 | NotificationContext cursor 和处理状态 | Temporary 或 Long-Term Memory |

通用 Worker 不理解 Episode、Notification、Candidate 或 Store。它只通过 `MemoryProcessor` interface 调用场景实现，因此删除通用 Worker 后，timer、取消、pending 和重试复杂度会重新散落到两个调用方；这个 seam 具有实际复用价值。

### 不复用的业务逻辑

Notification 不调用 Episode Processor，也不复用以下规则：

- Episode Prompt、`goal_result` 和设备任务证据门槛；
- `procedure/navigation/calibration/failure` 类型体系；
- Device Memory 的 Scope、revision 合并和 disputed 规则；
- Episode 的 state file、cursor 和 5 分钟空闲策略。

Episode Processor 会主动过滤 OTP 和临时值，而 Notification 的必要能力之一正是把有短期价值的通知写入 Temporary Memory。因此 Prompt、Candidate、policy、状态文件和 Store 写入必须分别保留在各自 Processor 内。

### 后台模型调用协调

当前 Episode Processor 直接调用模型，并没有 `MemoryRunGate`。引入 Notification Worker 后新增一个进程内 `MemoryRunGate`，规则如下：

- 同一时间最多运行一个后台 Memory 模型调用；
- 前台 Agent task 开始时取消正在执行或等待中的后台调用；
- gate 只保护 LLM 调用，不保护本地预筛、索引查询、JSONL 读取或 Store 写入；
- Worker 获取 gate 失败或被取消时保持自己的 pending 状态，之后按自己的 timer 重试；
- Episode 和 Notification 不共享 cursor、proposal、attempt count 或错误状态。

这避免两个后台 Worker 同时占用模型，又不把两个场景合并成一个调度队列。

### Notification Processor

MemoryWorker 只负责通用调度：

- 启停、唤醒、单实例批次串行化和退避；
- 前台 Agent task 运行时取消在途模型调用；
- 通过新增的 `MemoryRunGate` 与其他后台 Worker 串行使用模型；
- 记录场景级错误，不修改 Processor 的业务状态。

NotificationMemoryProcessor 负责场景语义：

- 从 `ReadPending` 读取通知并合并同一通知的 added/modified/removed 变化；
- 先执行敏感类别、App policy 和确定性过滤，再调用 LLM；
- 根据 `ignore`、`temporary`、`long_term` 三种结果提交；
- `temporary` 写入 Temporary Memory；`long_term` 执行 add、reinforce、supersede、hold；
- 只有 resolve 成功后才调用 `CommitProcessed`。

运行时序：

```text
落盘成功 -> Notify Worker
         -> 等待 30 秒合并窗口
         -> 最多处理 20 条
         -> resolve 成功
         -> CommitProcessed
```

Agent 启动时若发现未结算数据，立即恢复处理。没有 pending 数据时只做轻量检查，不调用模型。

## Candidate 与晋升策略

LLM 只返回严格 JSON：

```json
{
  "action": "ignore|temporary|long_term",
  "type": "preference|rule|fact|profile",
  "title": "Weekly review day",
  "content": "Weekly review is on Wednesday.",
  "time_scope": "recurring",
  "confidence": 0.93,
  "sensitivity": "normal",
  "source_event_ids": ["42"]
}
```

自动进入 Long-Term Memory 必须同时满足：

- 类型为 preference、rule、fact 或 profile；
- 时间范围为长期或 recurring；
- 置信度至少 0.90；
- 不是敏感内容，也未被 App policy 禁止；
- 单条结论有明确语义，或七天内有至少三次独立证据。

以下内容不自动晋升：OTP、密码、支付授权、银行和安全告警、健康和法律信息、私人聊天、营销、物流状态、一次性提醒和一次性日程变更。

分流结果互斥：适合创建、reinforce 或 supersede Long-Term Memory 的 Candidate 不再写入 Temporary Memory；只有仍有短期价值、但不应改变长期结论的 Candidate 才写入 `temporary/`。

## 已有 Memory 的更新

```text
同一结论 + 新来源       -> reinforce，刷新证据和 TTL
同一主题 + 长期变化     -> supersede，保留旧版本
一次性变化              -> temporary，不修改长期事实
含义不明确的冲突        -> hold，等待更多证据
```

示例：

| 已有 Memory | 新通知 | 结果 |
| --- | --- | --- |
| 周会是周二 | 本周改到周三 | temporary |
| 周会是周二 | 以后统一改到周三 | supersede |
| 用户偏好中文回复 | 一条英文通知 | 不更新 |
| 用户偏好中文回复 | 多次明确表达英文偏好 | supersede |

Temporary 和 Long-Term Memory 都通过 `SourceRefs`、`EvidenceRefs` 和 `Traceability` 标记通知来源，不复制通知正文。

## 配置

```toml
[notification_memory]
enabled = true
auto_create = true
auto_update = true
retention_days = 14
temporary_default_ttl_days = 7
context_max_mb = 16
debounce_seconds = 30
max_batch_events = 20
min_confidence = 0.90
allowed_apps = []
blocked_apps = []

[storage.cleanup]
notification_context_retention_days = [14, 7, 1, 0]
```

空的 `allowed_apps` 使用内置安全默认值，不表示允许所有 App。敏感类别规则优先级高于 allowlist。

## 隐私、失败与重试

- 通知正文只保留在手机到设备的本地链路，不上传托管服务。
- 日志只记录 event ID、App ID、policy 结果和 gap，不记录正文。
- 用户可以暂停自动记忆，也可以分别删除通知 Context、Temporary Memory 和 Long-Term Memory。
- BLE 或传输失败由手机侧重试；Agent 不因 BLE 断开而退出。
- 落盘失败不推进 source cursor；提炼或 Memory 写入失败不推进 memory cursor。
- Worker 在写入后崩溃时，使用 source reference 和 content fingerprint 保证幂等。

## 实施顺序

1. 增加统一事件模型和 `NotificationContext`，完成消费、落盘、游标、去重和 retention。
2. 以当前 main 的 `episodeMemoryWorker` 和 `episodeMemoryBatchProcessor` 为基础抽出通用 `MemoryWorker`/`MemoryProcessor`，保持 Episode 行为不变。
3. 增加 Temporary Memory Store，并让 `recall_memory` 合并检索 Temporary 和 Long-Term Memory。
4. 增加 `MemoryRunGate`，串行化 Episode 与 Notification 的后台模型调用，并验证前台任务可取消两者。
5. 接入 `NotificationMemoryProcessor`，先记录 Candidate 和 policy 结果，再开启低风险 Memory 的自动创建与更新。
6. 根据运行数据调整 TTL、debounce、置信度阈值和模型预算。

## 验收标准

- iOS 和 Android 事件进入同一事件模型，并可通过 `events_since` 消费。
- NotificationContext 重启后能从 source cursor 继续落盘。
- MemoryWorker 重启后能从 memory cursor 继续处理，且不重复写入 Memory。
- Episode 与 Notification 使用独立 Worker 状态；一个场景 retry 不改变另一个场景的 timer、cursor 或 attempt count。
- 同一时间最多有一个后台 Memory LLM 调用，前台 Agent task 可以取消它。
- 临时通知写入 Temporary Memory，不覆盖长期事实；稳定结论只写入 Long-Term Memory。
- `recall_memory` 一次查询能返回未过期的 Temporary Memory 和 Long-Term Memory。
- 自动推断 Memory 默认有 TTL，reinforce 会刷新 TTL，用户明确记住的规则不设 TTL。
- 敏感通知不会自动晋升。
- ring 截断、存储不足和处理失败都会留下可查询状态。

## 相关文档

- [BLE Service](../03-services/ble-service.md)
- [Memory Plane](memory-plane.md)
- [Agent Context Lifecycle](context-lifecycle.md)
- [Storage Manager](storage-manager.md)
