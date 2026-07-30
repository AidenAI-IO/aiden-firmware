# Tool Result 大结果防护技术设计

> 状态：功能已实现并完成包级、race、交叉编译和真机验证
>
> 实现分支：`ljunb/tool-result-protection`
>
> 更新日期：2026-07-30

## 1. 目标与边界

本设计解决两个问题：防止当前单个 Tool Result 撑爆下一次模型请求，以及在上下文压缩时回收历史 Tool Result 占用。

核心目标：

- 大结果不能无界进入 Context Manager。
- 超预算请求不能发送给模型 Provider。
- 模型仍能看到工具、动作、状态、关键字段和恢复方式。
- 不增加用户可见配置。
- 热路径不调用额外 LLM。
- 常见语音任务不增加额外 Agent Loop。

本设计不替代工具自身的输出限制。例如 Shell 仍应控制进程内存、stdout 和 poll 返回大小。

## 2. 核心链路

```text
Tool Call
  -> Tool Result
  -> ToolResultPolicy
       -> 完整内联
       -> 有界观察 + artifact ref
  -> Context Manager
  -> Context Compactor（达到阈值时）
  -> Hard Context Guard
  -> Model Provider
```

职责划分：

| 组件 | 职责 |
| --- | --- |
| ToolResultPolicy | 判断当前结果能否完整进入上下文，并生成有界观察 |
| Artifact Store | 保存可恢复的原始结果 |
| Context Manager | 持久化模型可见内容及机器可读 metadata |
| Context Compactor | 触发压缩时缩减久远 Tool Result |
| Hard Context Guard | 最终估算请求；超硬窗口时本地拒绝 |

## 3. 当前结果判定

### 3.1 内部预算

```text
usable_input_budget =
  context_window
  - max_output_tokens
  - safety_margin

safety_margin =
  max(1,024 tokens, context_window * 2%)

soft_limit = usable_input_budget * 80%

available_for_result =
  soft_limit
  - current_message_tokens
  - tool_schema_tokens
```

内部常量不暴露到 TOML 或 Web UI：

| 项目 | 当前值 |
| --- | ---: |
| 完整内联字节上限 | 8 KiB |
| 完整内联 token 上限 | 2,000 |
| Preview 目标 | 1,200 tokens |
| 最小观察预算 | 96 tokens |
| Artifact 默认读取 | 8 KiB |
| Artifact 单次读取硬上限 | 16 KiB |
| 单 Artifact 上限 | 8 MiB |
| 单会话作用域上限 | 32 MiB |
| 普通 Artifact TTL | 7 天 |
| 敏感 Artifact TTL | 1 小时 |

### 3.2 判定规则

```text
intrinsic_large =
  raw_bytes > 8 KiB
  OR estimated_tokens > 2,000

context_large =
  estimated_tokens > available_for_result
```

处理矩阵：

| 场景 | 处理 |
| --- | --- |
| 结果小且预算充足 | 完整内联 |
| 结果自身过大 | 有界观察 + artifact |
| 结果不大但当前空间不足 | 按剩余预算收缩 + artifact |
| Artifact 保存失败 | 有界观察，明确结果不可完整恢复 |
| 最终 Prompt 超硬窗口 | Hard Guard 本地拒绝 |

`artifact_read` 是恢复工具，不按普通 8 KiB 阈值递归生成新 Artifact。它只受当前上下文预算和自身 16 KiB 硬上限约束。

## 4. 有界观察与 Artifact

### 4.1 模型可见内容

大结果不使用模糊的 `tool result omitted`。模型至少应看到：

- 工具名称和动作。
- 动作已完成。
- 当前观察是否完整。
- 原始字符数和字节数。
- 状态、错误、ID、统计或 Top-K。
- Artifact 恢复引用。

示例：

```text
[shell] go test ./...
Tool action completed; model-visible result is partial
(84,320 chars, 84,320 bytes).
Full result: artifact://tr_...
Error: exit status 1
Stderr:
FAIL TestPhoneBridgeTimeout
```

### 4.2 确定性投影

投影由本地代码生成，不调用 LLM。

| 类型 | 当前策略 |
| --- | --- |
| 通用 JSON | 保留状态、错误、ID、路径、URL、cursor 和 continuation |
| JSON 集合 | 保留总数和前 3 条结果 |
| Shell/Test | 保留关键 PASS/FAIL、失败项、exit status、stderr 和 stdout head/tail |
| Memory/Episode | 保留查询条件、总数、Top-K ID、Episode 状态、结果和事件总数 |
| Phone Bridge | Calendar/Contacts/Notification 保留动作、目标、范围、资源 ID、总数和权限状态 |
| Clipboard | 保留状态、字符数和短 preview，使用 1 小时 TTL |
| Skill | 保留 skill/file、文件大小、标题列表和开头 preview |
| Web/Search | 保留 query/URL、来源、Top-K URL、标题和 answer/page preview |
| 普通文本 | 有界 head/tail，并标记省略规模 |

所有投影均由确定性本地代码生成，不增加 LLM 调用。

### 4.3 Artifact Store

Artifact ref 采用不可预测的 opaque ID：

```text
artifact://tr_<uuid>
```

安全与生命周期规则：

- Ref 不包含文件路径或 scope ID。
- 只能读取当前 Context Manager 所属 scope。
- Context Compactor 创建的新 revision 继承原 scope。
- 保存 MIME、大小、SHA-256、创建时间、过期时间和工具信息。
- scope 容量统计同时包含原文和 metadata 文件；目录/文件权限分别为 `0700`/`0600`。
- 单文件原子写入；中断遗留文件由 Storage Cleaner 延迟清理。
- 未引用 scope 经过 10 分钟 grace period 后才清理，并在删除前重新检查引用。
- 清空全部会话时同时删除 transcript、metadata 和 artifact scope。

### 4.4 `artifact_read`

```json
{
  "ref": "artifact://tr_...",
  "offset": 0,
  "limit": 8192,
  "query": "failed"
}
```

规则：

- 无 query 时按 byte offset 分页。
- 有 query 时执行字面量搜索，不支持正则。
- 返回 `content`、`offset`、`next_offset`、`complete` 和 `sha256`。
- 序列化后的整个 Tool Result 不超过 16 KiB。
- 已有源文件或原生分页 API 时，优先读取原始来源。
- `artifact_read` 不重新执行原工具；工具描述会提示模型优先使用原工具的分页/源文件能力，Artifact 作为没有原生恢复入口时的稳定快照。

## 5. Hard Context Guard

Hard Guard 位于最终消息转换和 Provider 调用之间：

```text
Context Manager clone
  -> outbound transforms
  -> ConvertMessageList
  -> Hard Context Guard
  -> Provider
```

它只做两件事：

1. 使用最终消息、Tool Schema 和输出 token 预留重新估算 Prompt。
2. 超出硬窗口时返回 `ContextBudgetExceededError`。

Hard Guard 不删除任何 Tool Result，也不临时改写历史消息。

该错误属于本地预算错误，不包装成 `LLMCallError`，也不计入 Provider 失败率。

## 6. 历史 Tool Result 压缩

历史结果压缩只在 Context Compactor 触发时执行，不在每轮请求前动态改写 Prompt。

```text
token usage < usable budget * 80%
  -> 不修改历史

token usage >= usable budget * 80%
  -> 从旧到新压缩历史 Tool Result
  -> 目标降到 usable budget * 70%
  -> 不足时继续原 Conversation Summary
```

第一版只处理最新用户消息之前、且能匹配 Tool Call ID 的 Tool Result。

保护范围：

- System Prompt。
- 最新用户消息。
- 当前 turn 的 Tool Call 和 Tool Result。
- 没有匹配 Tool Call 的异常结果。
- 当前 tail 中的截图和状态消息。

历史占位是确定性的，并优先使用 Tool Result metadata 中的 summary 与 artifact ref。

如果历史占位仍不足以降到目标，Conversation Summary 只负责生成对话摘要；
Artifact 引用会收集到摘要外的确定性 `## Recoverable Tool Results` 块。
即使摘要模型省略全部引用，完整和部分 Artifact 的恢复入口仍然保留。
该块使用明确标记的 JSON Lines 数据边界，限制为最多 32 项和 1,200 tokens；超限时优先保留较新的引用并记录省略数量，避免恢复 metadata 反向撑爆上下文。

压缩会使第一个变化 token 之后的 KV Cache 失效。通过 80%/70% 滞回、批量替换和稳定 revision，将影响控制为每次压缩一次。

## 7. 数据与失败语义

Context Manager 持久化的 Tool Result：

```go
type ToolResult struct {
    ToolCallID string
    Name       string
    Content    string
    Meta       *ToolResultMeta
}
```

核心 metadata：

```go
type ToolResultMeta struct {
    ArtifactRef      string
    OriginalBytes    int64
    OriginalChars    int
    EstimatedTokens  int
    Complete         bool
    ArtifactComplete bool
    Reason           string
    Summary          string
    ActionCompleted  bool
    ObservationComplete bool
    ProcessingErrorCode string
    ArtifactStoreError  string
}
```

`ConvertMessageList` 只向模型发送 `Content`，不会发送 metadata。

`Complete` 表示模型看到的内容是否完整；`ArtifactComplete` 表示 Artifact 是否保存了完整原文。两者不能混用。

如果工具动作已执行，但 Artifact 保存失败：

- 不把完整大结果写入 Context Manager。
- 不重新执行有副作用的工具动作。
- 返回有界观察并设置 `Complete=false`、`action_completed=true`、
  `observation_complete=false` 和 `code=tool_result_persistence_failed`。
- 没有完整 Artifact 时明确说明结果不可完整恢复。

## 8. 验收与剩余工作

### 8.1 当前完成状态

| 能力 | 状态 |
| --- | --- |
| 当前 Tool Result 限幅 | 已完成 |
| Artifact 保存与有界读取 | 已完成 |
| Scope、TTL、容量和清理 | 已完成 |
| Hard Context Guard | 已完成 |
| 通用和高风险工具专属投影 | 已完成 |
| 历史 Tool Result 压缩 | 已完成 |
| Summary 后恢复引用保留 | 已完成 |
| Artifact 失败结构化语义 | 已完成 |
| Telemetry 指标接线 | 已完成 |
| ARM 真机验证 | 已完成 |

### 8.2 已有测试

- 小结果完整内联。
- 8 KiB、2,000 tokens 和当前预算限幅。
- 大 JSON 状态字段与 Top-K 保留。
- Shell stderr tail 保留。
- Clipboard 敏感投影。
- Artifact 容量、TTL、scope、reload 和 revision 继承。
- `artifact_read` 分页、搜索和 16 KiB 硬上限。
- Hard Guard 在 Provider 调用前拒绝。
- 历史结果保护当前 turn，并在不足时继续 Conversation Summary。
- Conversation Summary 模型省略 ref 时，摘要外仍保留完整/部分 Artifact 引用。
- Artifact 写入失败后，已完成副作用动作不会重复执行。
- Artifact scope 直到最后一个 revision 引用消失后才清理。
- Tool Result、Hard Guard、Provider context-length 和历史压缩 Telemetry。

### 8.3 真机验收结果

2026-07-30 在 ARMv7 开发板隔离 Agent（18080 端口）验证：

- Shell 原始结果 13,909 bytes / 3,478 estimated tokens。
- 模型可见内容 3,818 bytes / 955 estimated tokens。
- `processing_reason=intrinsic_large`、`artifactized=true`、`artifact_complete=true`。
- `artifact_read` 字面量搜索找到尾标记，返回 `complete=true` 和一致 SHA-256。
- Context Budget Telemetry 正常写入，Hard Guard 未误拒绝。
- 隔离 Agent 测试后已停止，生产 8080 服务保持健康。

### 8.4 持续 Benchmark

以下指标由新增 Telemetry 支持持续采集，不阻塞本功能交付：

- 10 KiB、100 KiB、1 MiB Tool Result。
- Provider context-length error 为 0。
- Tool Result 到下一次模型请求的 p50、p95 延迟。
- Artifact 读取率与额外 Agent Loop 数。
- Prompt Cache 命中变化。
- Shell、Memory、Phone Bridge 和 Skill 任务成功率。
- 重复副作用率保持为 0。

## 9. 最终决策

1. 当前结果安全和历史空间回收是两个独立阶段。
2. 普通结果必须在写入 Context Manager 前处理。
3. Result 自身大小和当前可用空间都参与判断。
4. Hard Guard 只验证和拒绝，不负责删除内容。
5. 历史结果只在 Compactor 触发时批量替换。
6. Artifact 是按需恢复通道，不是默认追加步骤。
7. 第一版不增加用户配置、额外 LLM 或额外 Agent Loop。

相关调研见 [Claude Code 大 Tool Result 与上下文压缩调研](claude-code-tool-result-research.md)。
