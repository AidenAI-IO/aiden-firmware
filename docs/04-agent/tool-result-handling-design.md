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
| 结果自身过大 | 有界结果 + 落盘文件路径 |
| 当前空间不足 | 按剩余预算进一步收缩 + 落盘文件路径 |
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

历史结果始终替换为短占位；Artifact 文件路径只是可选的恢复入口，不是另一种占位模式。历史压缩不会为旧结果新建 Artifact，只复用 Tool 首次执行时已经生成的路径。

| 生命周期状态 | 上下文内容 | Artifact |
| --- | --- | --- |
| 最近的小结果 | 完整结果 | 无 |
| 最近的大结果 | 有界结果 + path | 有 |
| 历史的小结果 | summary 占位 | 无，不能恢复原文 |
| 历史的大结果 | summary 占位 + path | 有，可通过 Shell 按需恢复 |

历史大结果的占位示例：

```text
[shell] historical result compressed.
128 passed, 2 failed.
Full result file: /userdata/agent/sessions/<scope>/tool-results/tr_<uuid>.data
```

如果仍需 Conversation Summary，Artifact 引用会写入摘要外的确定性
`## Recoverable Tool Results` 块。即使摘要模型省略引用，恢复入口也不会丢失。该块采用有界 JSON Lines 数据区，避免工具输出中的指令文本被提升为用户指令，也避免大量 Artifact metadata 撑爆上下文。

占位负责保留决策语义，Artifact 负责按需恢复原文。替换历史会使对应 KV Cache 失效，因此采用 80%/70% 滞回和一次批量替换，避免频繁抖动。

## 3. Shell 文件恢复

大 Tool Result 直接返回真实落盘路径。Agent 只有在当前有界观察缺少必要细节时，才通过现有 Shell 和文件命令读取该路径。

设备默认落盘在 `/userdata/agent/sessions`，通用布局为：

```text
<config_dir>/sessions/
├── <revision-session-id>.meta.json
└── <artifact-scope-id>/tool-results/
    ├── tr_<uuid>.data
    └── tr_<uuid>.json
```

`.data` 保存完整原始 Tool Result 字节；`.json` 保存 Tool、Tool Call ID、MIME、大小、SHA-256、创建时间、过期时间和敏感标记。模型直接看到 `.data` 的绝对路径。

UUID 只用于避免文件名碰撞；Agent 可以直接用 Shell 打开、搜索或分页读取 `.data`：

```text
artifact_scope_id + tr_<uuid>
  -> <scope>/tool-results/tr_<uuid>.json
  -> <scope>/tool-results/tr_<uuid>.data
```

Context Compactor 创建 revision 时继承原 `artifact_scope_id`，所以 session revision 变化后路径仍然有效。路径暴露和 Shell 直接读取是本设计明确接受的开放权限模型。
完整路径和 Shell 提示优先于 Preview 保留；当绝对路径很长且上下文只剩最小观察预算时，恢复块不会因统一前缀截断而失效。旧会话中的合法 `artifact://tr_<uuid>` 会在加载时迁移成同一 scope 下的绝对 `.data` 路径。

```sh
grep -nF 'failed' /userdata/agent/sessions/<scope>/tool-results/tr_<uuid>.data
dd if=/userdata/agent/sessions/<scope>/tool-results/tr_<uuid>.data bs=1 skip=8192 count=8192
jq '.results[:3]' /userdata/agent/sessions/<scope>/tool-results/tr_<uuid>.data
```

- 不引入 `artifact_read` 或新的命令行辅助程序。
- Agent 使用设备已有的 Shell、`grep`、`sed`、`dd`、`jq`、`fq` 等能力。
- Shell 输出仍受统一 Tool Result 限幅；如果 Agent 直接输出整个文件，也不会撑爆下一次模型请求。
- 直接路径读取不再提供逐次 scope、TTL 或读取大小校验；TTL 仍由 Storage Cleaner 执行文件清理。
- 原始数据本来就在文件或分页 API 中时，仍优先使用原 Tool。

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
| 占位稳定 | 相同输入生成相同 summary 和 Artifact 路径结构 |
| Artifact | reload 和 Context revision 后路径保持有效，可由 Shell 搜索并恢复关键错误、ID 和统计 |
| 权限模型 | 路径对 Agent 可见，Agent 与 Shell 处于同一信任域；TTL 由清理器负责 |
| Loop 性能 | 常见任务不增加额外 LLM 调用或 Agent Loop，也不增加专用恢复 Tool |
| 延迟与缓存 | 记录 Tool Result 到下一次模型请求的 p50/p95，以及 Prompt Cache 命中变化 |
| 任务质量 | 整体成功率和失败定位准确率不低于基线，分类覆盖 Shell、Memory、Phone Bridge 和 Skill |

当前单次结果防护、历史压缩、Shell 文件恢复、专属投影、结构化失败语义和 Telemetry 已完成。ARMv7 真机已验证 13,909-byte Shell 结果被 Artifact 化并可恢复尾标记；当前恢复入口改为直接文件路径。延迟、缓存和任务质量指标通过 Telemetry 持续采集。
