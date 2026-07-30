# Tool Result 处理设计（精简版）

> 状态：功能已实现，并完成 Telemetry、包级/race、交叉编译和真机验证
>
> 日期：2026-07-30

目标：避免单次大 Tool Result 撑爆上下文，同时保留最近结果、关键决策信息和按需恢复能力。默认使用通用规则，不增加用户配置、额外 LLM 调用或常规 Agent Loop。

## 1. 单次 Tool Result 处理

Tool 执行完成后、写入 Context Manager 前，统一经过 `ToolResultPolicy`。

满足任一条件时，不完整内联：

```text
raw bytes > 8 KiB
OR estimated tokens > 2,000
OR result tokens > 当前可用空间
```

处理结果：

| 场景 | 写入上下文 |
| --- | --- |
| 小结果且空间充足 | 完整结果 |
| 结果自身过大 | 有界结果 + `artifact://` 引用 |
| 当前空间不足 | 按剩余预算进一步收缩 + `artifact://` 引用 |
| Artifact 保存失败 | 有界结果 + 无法恢复说明 |
| 最终 Prompt 仍超窗口 | Hard Guard 本地拒绝，不调用 Provider |

有界结果必须保留 Tool、动作、状态、错误、精确 ID、总数、原始规模和恢复方式。

默认规则覆盖所有 Tool：JSON 提取关键字段、总数、cursor 和前 3 条；文本保留 head/tail；未知结构按文本处理。只有通用规则会丢失关键决策信息时，才增加专属规则。

当前专属处理覆盖 Shell/Test、Memory/Episode、Phone Bridge 数据、Clipboard、Skill 和 Web/Search；未知工具仍使用通用 JSON 或文本规则。

已完成但结果过大的副作用 Tool 不得因裁剪或 Artifact 保存失败而自动重试。

## 2. 历史 Tool Result 处理

历史结果与单次结果分开处理。它只在 Context Compactor 达到阈值时触发，不在每轮请求前改写历史。

```text
上下文使用率 <= 80% -> 不处理
上下文使用率 > 80%  -> 从旧到新替换历史 Tool Result
替换目标             -> 尝试降到 70%
全部替换后仍超目标   -> 继续现有 Conversation Summary
```

只替换最新用户消息之前、能匹配 Tool Call ID 的结果。System Prompt、最新用户消息、当前 turn 的 Tool Call/Result、未匹配结果和当前 tail 状态受保护。

历史结果始终替换为短占位；Artifact 只是可选的恢复引用，不是另一种占位模式。历史压缩不会为旧结果新建 Artifact，只复用 Tool 首次执行时已经生成的引用。

| 生命周期状态 | 上下文内容 | Artifact |
| --- | --- | --- |
| 最近的小结果 | 完整结果 | 无 |
| 最近的大结果 | 有界结果 + ref | 有 |
| 历史的小结果 | summary 占位 | 无，不能通过 `artifact_read` 恢复原文 |
| 历史的大结果 | summary 占位 + ref | 有，可按需恢复 |

历史大结果的占位示例：

```text
[shell] historical result compressed.
128 passed, 2 failed.
Full result: artifact://tr_...
```

如果仍需 Conversation Summary，Artifact 引用会写入摘要外的确定性
`## Recoverable Tool Results` 块。即使摘要模型省略引用，恢复入口也不会丢失。该块采用有界 JSON Lines 数据区，避免工具输出中的指令文本被提升为用户指令，也避免大量 Artifact metadata 撑爆上下文。

占位负责保留决策语义，Artifact 负责按需恢复原文。替换历史会使对应 KV Cache 失效，因此采用 80%/70% 滞回和一次批量替换，避免频繁抖动。

## 3. `artifact_read`

`artifact_read` 是大 Tool Result 的受控恢复通道。只有当前上下文缺少完成任务所需细节时才调用，不作为默认步骤。

设备默认落盘在 `/userdata/agent/sessions`，通用布局为：

```text
<config_dir>/sessions/
├── <revision-session-id>.meta.json
└── <artifact-scope-id>/tool-results/
    ├── tr_<uuid>.data
    └── tr_<uuid>.json
```

`.data` 保存完整原始 Tool Result 字节；`.json` 保存 Tool、Tool Call ID、MIME、大小、SHA-256、创建时间、过期时间和敏感标记。对外只返回 `artifact://tr_<uuid>`，不暴露物理路径和 scope。

UUID 不用于扫描或模糊搜索。读取时去掉 `artifact://` 得到 `tr_<uuid>`，校验格式后，在当前 Context Manager 的 `artifact_scope_id` 下直接打开同名 `.json` 和 `.data`：

```text
artifact_scope_id + tr_<uuid>
  -> <scope>/tool-results/tr_<uuid>.json
  -> <scope>/tool-results/tr_<uuid>.data
```

Context Compactor 创建 revision 时继承原 `artifact_scope_id`，所以 session revision 变化后仍能定位原结果。物理路径不暴露给模型，避免 Shell 绕过分页、TTL 和 scope 检查。

```json
{
  "ref": "artifact://tr_...",
  "offset": 0,
  "limit": 8192,
  "query": "failed"
}
```

返回格式：

```json
{
  "content": "bounded result fragment",
  "offset": 0,
  "next_offset": 8192,
  "complete": false,
  "found": true,
  "sha256": "...",
  "mime_type": "application/json"
}
```

- 无 `query`：按 byte offset 分页，`next_offset` 是下一页起点。
- 有 `query`：从 `offset` 开始做字面量搜索，不支持正则；`content` 返回匹配位置附近的窗口。
- 单次默认最多读取 8 KiB，序列化后的完整返回不超过 16 KiB。
- 拒绝路径、路径穿越、跨 scope、过期引用和超限读取。
- 读取结果不会再次生成 Artifact。

原始数据本来就在文件或分页 API 中时，仍优先使用原 Tool；只有 Tool Result Artifact 使用 `artifact_read`。

存储边界：单 Artifact 8 MiB，单会话 scope 32 MiB（原文和 metadata 合计）；普通 TTL 7 天，敏感 TTL 1 小时。Artifact 目录/文件使用 owner-only 权限。Context revision 继承原 scope，孤儿 scope 经过 grace period 且删除前复核引用。

## 4. 验收标准

| 维度 | 验收要求 |
| --- | --- |
| 单次结果 | 10 KiB、100 KiB、1 MiB 结果均不会无界进入 active context |
| 窗口安全 | Provider context-length error 为 0；Hard Guard 拒绝时 Provider 调用数为 0 |
| 语义保留 | 状态、错误、精确 ID、总数、cursor 和关键统计不被改写 |
| Tool 协议 | Tool Call ID 与 Tool Result 配对合法；视觉结果继续走 attachment 链路 |
| 副作用 | 已完成 Tool 因结果裁剪或 Artifact 保存失败而重复执行的次数为 0 |
| 历史处理 | 低于 80% 不改写；触发后从旧到新压到 70%；当前 turn 保持完整 |
| 占位稳定 | 相同输入生成相同 summary 和 Artifact 引用结构 |
| Artifact | reload 和 Context revision 后可分页、搜索并恢复关键错误、ID 和统计 |
| 访问控制 | 跨 scope、过期引用、路径输入和超限读取均被拒绝 |
| Loop 性能 | 常见任务不增加额外 LLM 调用或 Agent Loop，且不默认调用 `artifact_read` |
| 延迟与缓存 | 记录 Tool Result 到下一次模型请求的 p50/p95，以及 Prompt Cache 命中变化 |
| 任务质量 | 整体成功率和失败定位准确率不低于基线，分类覆盖 Shell、Memory、Phone Bridge 和 Skill |

当前单次结果防护、历史压缩、`artifact_read`、专属投影、结构化失败语义和 Telemetry 已完成。ARMv7 真机已验证 13,909-byte Shell 结果被 Artifact 化，`artifact_read` 成功搜索尾标记；隔离测试后生产 8080 服务保持健康。延迟、缓存和任务质量指标通过 Telemetry 持续采集。
