---
sidebar_position: 12
---

# 通知持久化与自动记忆

**状态：** 当前实现说明

## 设计结论

`ble_service` 只生产规范化通知事件。Agent 侧的 `NotificationContext` 负责消费、去重、落盘、游标和清理。

Memory 维护只有一个共享 `MemoryWorker`：

```text
MemoryWorker（Agent 闲时调度，串行执行）
├── EpisodeProcessor
└── NotificationProcessor
```

Worker 只负责前台取消、默认 5 分钟闲时窗口、按注册顺序串行调用 Processor、pending 和停止生命周期。Worker 不理解 Episode、Notification、Memory 类型或 Store 语义，也不建立跨场景优先级队列。每个 Processor 自己决定读取方式、批大小、提炼规则、proposal 校验和落盘方式。

启动时会先一次性注册 EpisodeProcessor 和 NotificationProcessor，再启动
Worker，确保首个闲时批次也是 Episode -> Notification 的固定顺序。

`MemoryMergeEngine` 只抽取两类场景都需要的机械步骤：

```text
场景原始输入 + 相关 Memory -> 构造场景消息 -> 调用 LLM -> 返回原始 proposal
```

Episode 和 Notification 的 proposal schema、证据门槛、冲突处理、Memory 新增/更新/晋升逻辑分别留在各自 Processor 中，不强行统一。

## 组件职责

| 模块 | 职责 |
| --- | --- |
| `ble_service` | 接收 iOS/Android 通知、规范化、分配事件 ID、维护短期 event ring |
| `NotificationContext` | 消费事件、去重、JSONL 落盘、source/memory cursor、查询和清理 |
| `MemoryWorker` | 统一闲时窗口、前台取消、串行执行 Processor、重试和停止 |
| `episodeMemoryProcessor` | 读取 Episode、按 Episode 规则提炼并写入 Device Memory |
| `NotificationMemoryProcessor` | 读取通知、过滤、调用提炼、校验 proposal 并写入 Temporary/Long-Term Memory |
| `MemoryMergeEngine` | 召回相关 Memory、构造消息、调用 LLM、返回 raw proposal |

## 生命周期

通知先进入 `ble_service` 的 event ring；Agent 进入闲时后，NotificationProcessor
才消费 ring、把原始记录持久化到 NotificationContext，并继续完成提炼：

```text
通知源 -> ble_service event ring
Agent 前台任务结束 -> 等待 5 分钟闲时
                    -> EpisodeProcessor（最多 5 个 Episode）
                    -> NotificationProcessor
                         -> Consume / NotificationContext.append
                         -> 提炼最多 10 条通知
                    -> 仍有 pending 时在当前闲时窗口继续下一批
```

通知没有独立的 30 秒 timer，也没有单独的 Notification Worker。通知发布成功后只唤醒共享 Worker 的闲时计时器，不会立即触发 LLM；Processor 在共享 Worker 被调度时先 `Consume` BLE ring，再读取自己的 pending 数据。没有 pending 时返回空结果，Worker 不会持续轮询。

## NotificationContext

`NotificationContext` 对 runtime 提供：

```text
Consume(limit)               从 events_since 拉取并可靠落盘
ReadPending(limit)           读取 memory cursor 之后的记录
CommitProcessed(batch)       Memory 写入成功后推进 memory cursor
CleanupProcessedBefore(...)  只清理已处理的日期分片
Query(query)                 只读查询原始通知，不访问 BLE、不改游标
```

落盘记录保留 BLE 原始字段，并增加 Agent 本地单调递增的 `context_id`。Memory cursor 使用 `context_id`，不依赖 BLE generation。落盘采用按 UTC 日期分片的 JSONL；写入成功后才推进 source cursor。断电恢复时保留已完整写入的记录，并修复最后一条不完整 JSON 行。

`CommitProcessed` 要求传入从当前 cursor 开始的连续批次。清理器必须调用 `NotificationContext` 的清理方法，不能绕过 Context 直接删除 JSONL，以避免和 append/commit 并发。

## NotificationProcessor

Processor 自己拥有通知场景的完整业务流程：

1. `Consume` 当前 BLE ring，随后 `ReadPending` 取本批数据；
2. 合并同一通知的 added/modified/removed 变化；
3. 确定性过滤 OTP、验证码、营销、秘密等明显噪声；
4. 为本批每条通知检索相关 Memory，并合并成一次 `MemoryMergeEngine.Extract` 调用；
5. 按 `context_id` 拆分、解析并校验通知专用 proposal；每条通知最多产生一个 action；
6. 按 action 在 Temporary 或 Long-Term Store 中新增、更新、删除、reinforce 或 promote；
7. 所有相关 Memory 写入成功后才 `CommitProcessed`；仍有积压时，在同一次闲时窗口继续下一批，不重新等待 5 分钟。

没有模型时，测试和离线场景可以使用确定性的 Temporary Memory fallback。Processor 不把通知业务规则放入 Worker，也不要求 Episode 和 Notification 使用同一套 action 或状态机。

通知 proposal 的 action 包括 `ignore`、`add`、`update`、`reinforce`、`remove` 和 `promote`。更新已有记录必须带准确的 `memory_id` 和 `memory_revision`；无效 proposal 不推进 memory cursor。通知变化会作为新的 Context 记录进入后续批次，因此不保留会阻塞 cursor 的 `hold` 状态。

## EpisodeProcessor

EpisodeProcessor 保留已有 Episode 语义：

- 只接收已结束且有设备证据的 Episode；
- 每批最多处理 5 个 Episode，并将整批 Episode 和相关 Device Memory 合并为一次 LLM 调用；
- LLM 返回带 `episode_id` 的结果，Processor 再按 Episode 逐项校验和落盘；
- 自己校验 `goal_result`、candidate 类型、证据引用和 revision；
- 自己执行 Device Memory 的新增、更新和冲突处理；
- 用 Episode processing ledger 保存 `processing -> proposed -> done/retry/ignored`，支持崩溃恢复。

## Memory 与 Recall

```text
memory/
├── notifications/   # 原始通知和游标
├── temporary/       # 有 expires_at 的短期结论
├── long_term/       # 稳定偏好、规则和事实
├── device/          # Episode 产生的设备知识
└── episodes/        # 不可变 Episode 轨迹
```

Temporary 和 Long-Term 使用相同的可检索记录格式。`recall_memory` 同时搜索两个 Store，过滤过期 Temporary；Device Memory 由 EpisodeProcessor 单独维护。

默认值：单条通知正文最多 4 KiB；单次 BLE consume 最多 100 条；Notification 单批最多 10 条；通知重放去重窗口最多保留 4096 个 fingerprint；Temporary 默认 7 天；自动生成的 Long-Term 默认 90 天。Episode 闲时延迟由 `episode_memory_idle_delay_seconds` 配置，默认 300 秒；该值也是共享 Worker 的闲时窗口。

## 存储水位与隐私

`StorageMonitor` 统一管理水位和 Cleaner。Normal/Warning/Critical 分别启用 14/7/1 天的 Notification Context 清理，Emergency 清理全部已处理分片；任何水位都不删除未处理记录。Critical 和 Emergency 会关闭 `notification_context` 写能力，因此 Processor 不再从 BLE consume 新通知，待水位恢复后再从原 source cursor 继续。Temporary Memory Cleaner 在 Normal 起即可清理已过期或 deleted 记录。

通知正文会作为提炼输入发送给当前配置的 LLM provider；只有配置本地模型时，
该内容才会完全留在设备内。日志只记录 event ID、App ID、处理结果和 gap，
不记录正文。

## 失败与恢复

- BLE consume 失败：不推进 source cursor，保留 pending，下一次闲时重试；
- LLM、proposal 校验或 Memory 写入失败：不推进 memory cursor；
- 单个 Processor 失败：记录该场景错误并保留其 pending，按闲时延迟退避，避免 BLE/LLM/磁盘持续错误形成热循环；其他已注册场景仍可在同一轮串行执行，不共享业务事务；
- 前台任务开始：共享 Worker 取消在途模型调用；Processor 保留自己的持久化状态，下一次闲时继续；
- Worker 重启：Episode 从 processing ledger 恢复，Notification 从 source/memory cursor 恢复；
- Notification 当前不单独持久化 proposal ledger；若进程在 Memory 写入成功、cursor 提交前崩溃，下一次会重新提炼该记录，因此 Notification 的 add/update 逻辑必须保持幂等；
- 一个 Notification 批次内的 Store action 依次执行，不提供跨 Temporary/Long-Term Store 事务；发生中途 I/O 失败时不推进 cursor，并依靠固定 ID、revision 校验和重试收敛；
- 任何 gap 都写入 `NotificationContextState.Gaps`，不得静默吞掉。

## 验收重点

- Episode 和 Notification 由同一个 `MemoryWorker` 按注册顺序串行调度；每个 Processor 每批只调用一次 LLM，返回结果在本地逐项校验和落盘；
- Agent 前台任务不会被后台 Memory 提炼阻塞，且能取消在途模型调用；
- 两个 Processor 可独立决定批大小、proposal 结构和 Apply 逻辑；
- `MemoryMergeEngine` 不拥有跨场景 Apply 或统一 `MemoryIntent` 语义；
- 通知无 pending 时不启动持续 polling，也不存在固定 30 秒处理 timer；
- cursor 只在对应 Memory 写入成功后推进。

## 相关文档

- [Memory Plane](memory-plane.md)
- [Agent Context Lifecycle](context-lifecycle.md)
- [Storage Manager](storage-manager.md)
- [BLE Service](../03-services/ble-service.md)
