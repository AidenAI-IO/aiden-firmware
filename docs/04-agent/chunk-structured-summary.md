# Chunk 结构化摘要设计

## 背景

Agent 会话历史采用两层 memory：

- **session memory**：保存当前会话事件、压缩后的 chunk、chunk summary，用于上下文压缩和 `recall_session_chunks` 回查。
- **long-term memory**：保存长期可复用的用户偏好、规则、事实、流程，由 `save_memory` 或显式 `AddMemory` 写入。

原先 session chunk 只有一段自然语言 `summary`。它适合人读，但会混合事实、决策、提案和待办，后续模型容易把“讨论过/建议过”误读为“已经实现/已经决定”。

因此新增 **结构化 chunk summary**：保留纯文本 summary，同时在 chunk index 中记录机器可读字段。

## 目标

- 保持旧数据兼容：旧 chunk 没有 structured 字段也能正常读取。
- 保持 prompt 简洁：`summary.md` 仍只展示纯文本摘要。
- 明确区分：事实、决策、提案、待办、风险和长期记忆候选。
- 提升 recall 质量：`recall_session_chunks` 返回 structured 字段和原始 evidence。
- 不自动污染 long-term memory：`memory_candidates` 只是候选，不会自动写入长期记忆。

## 存储位置

```text
memory/
  session/
    events.jsonl                    # 当前 hot window 事件
    summary.md                      # 给 prompt/人看的短摘要窗口
    summary_archive.md              # 更早的摘要归档
    chunks/
      chunk_xxx.jsonl               # 被压缩出去的原始事件
      index.yaml                    # chunk 元数据，包含 structured summary

  long_term/
    memories/
      mem_xxx.md                    # 长期记忆条目
    index.yaml
    profile.md
```

结构化摘要只存于：

```text
memory/session/chunks/index.yaml
```

不会直接写入：

```text
memory/long_term/memories/
```

## 数据结构

```go
type ChunkStructuredSummary struct {
    Summary          string   `json:"summary,omitempty" yaml:"summary,omitempty"`
    UserGoals        []string `json:"user_goals,omitempty" yaml:"user_goals,omitempty"`
    ConfirmedFacts   []string `json:"confirmed_facts,omitempty" yaml:"confirmed_facts,omitempty"`
    Decisions        []string `json:"decisions,omitempty" yaml:"decisions,omitempty"`
    Proposals        []string `json:"proposals,omitempty" yaml:"proposals,omitempty"`
    OpenTasks        []string `json:"open_tasks,omitempty" yaml:"open_tasks,omitempty"`
    RisksOrPitfalls  []string `json:"risks_or_pitfalls,omitempty" yaml:"risks_or_pitfalls,omitempty"`
    MemoryCandidates []string `json:"memory_candidates,omitempty" yaml:"memory_candidates,omitempty"`
}
```

字段含义：

- `summary`：一句简短自然语言摘要。**当结构化摘要生成成功时，此字段会覆盖 chunk 的 plain summary**。
- `user_goals`：用户在该片段中表达的目标。
- `confirmed_facts`：用户明确说明、工具验证或代码真实存在的事实。
- `decisions`：已确认或已采用的决策。
- `proposals`：提出但未确认/未实现的方案。
- `open_tasks`：尚未完成的任务。
- `risks_or_pitfalls`：风险、坑、约束。
- `memory_candidates`：可能值得沉淀到 long-term memory 的候选内容；当前只记录，不自动保存。

## index.yaml 示例

```yaml
version: 1
updated_at: "2026-05-29T18:30:00Z"
chunks:
  - id: chunk_1780048951468076000_b78f6967
    file: chunk_1780048951468076000_b78f6967.jsonl
    status: active
    summary: 讨论 MiniCPM 局域网 VLM 接入方案。
    structured:
      summary: 讨论 MiniCPM 局域网 VLM 接入方案。
      user_goals:
        - 测试 MiniCPM 作为板子的局域网 VLM
      confirmed_facts:
        - 主模型仍承担语音链路
      decisions:
        - VLM 使用独立 model_vision 配置
      proposals:
        - screen_memory_summarizer 优先读取 model_vision
      open_tasks:
        - 实现 model_vision 配置解析
      risks_or_pitfalls:
        - 不要把主 model 直接替换成 VLM
      memory_candidates:
        - Aiden 的主语音模型和屏幕 VLM 应分离配置
    tags:
      - MiniCPM
      - VLM
    entities:
      - Aiden
    event_count: 8
    checksum: sha256:...
```

## 压缩流程

```text
events.jsonl 超过 hot window
        |
        v
取 compactEvents
        |
        v
生成 fallback plain summary (heuristic)
        |
        v
尝试生成 structured summary (优先)
        |
        +--> StructuredSummarizeFn 生成 ChunkStructuredSummary (含 summary 字段)
        |         |
        |         +-- JSON parse 成功：使用 structured.Summary 作为 chunk summary
        |         |
        |         +-- JSON parse 失败：fallback 到 plain SummarizeFn
        |                   |
        |                   +-- 成功：使用 plain LLM summary
        |                   |
        |                   +-- 失败：使用 heuristic fallback
        v
session.compressEvents(Summary, Structured, Tags, Entities)
        |
        v
写 chunk_xxx.jsonl / chunks/index.yaml / summary.md
        |
        v
replaceEvents(hotEvents)
```

**优化说明**：当 `StructuredSummarizeFn` 成功时，不再调用 `SummarizeFn`，直接使用结构化摘要中的 `summary` 字段。这样将压缩时的 LLM 调用从两次减少到一次，节约成本。

## LLM 输出要求

结构化摘要由 `buildLLMStructuredSummarizeFn` 生成。LLM 必须输出 strict JSON：

```json
{
  "summary": "...",
  "user_goals": [],
  "confirmed_facts": [],
  "decisions": [],
  "proposals": [],
  "open_tasks": [],
  "risks_or_pitfalls": [],
  "memory_candidates": []
}
```

关键约束：

- 已实现/已验证事实放 `confirmed_facts`。
- 助手建议或未实现设计放 `proposals`，不能放 `confirmed_facts`。
- 只有用户明确认可、上下文明确采用或已完成的内容放 `decisions`。
- 未完成工作放 `open_tasks`。
- `memory_candidates` 只放耐久的用户/项目事实，不放临时 TODO 或助手猜测。

解析失败时返回空结构化摘要，不影响正常压缩。

## Recall 行为

`recall_session_chunks` 返回：

```json
{
  "results": [
    {
      "chunk_id": "chunk_xxx",
      "summary": "讨论 MiniCPM 局域网 VLM 接入方案。",
      "structured": {
        "decisions": ["VLM 使用独立 model_vision 配置"],
        "open_tasks": ["实现 model_vision 配置解析"]
      },
      "evidence": [
        {
          "event_id": "evt_xxx",
          "role": "user",
          "type": "user_input",
          "content": "..."
        }
      ]
    }
  ]
}
```

`structured` 用于帮助模型快速理解 chunk；`evidence` 仍是最终追溯依据。

## 与 long-term memory 的关系

结构化 chunk summary **不是 long-term memory**。

当前不会自动执行：

```go
longTerm.AddMemory(...)
```

关系如下：

```text
session chunk structured summary
        |
        | recall_session_chunks 返回给 LLM
        v
LLM 可基于 evidence 判断是否需要调用 save_memory
        |
        v
long_term/memories/mem_xxx.md
```

也就是说：

- chunk summary 用于会话历史压缩和召回。
- long-term memory 用于长期偏好/规则/事实/流程。
- `memory_candidates` 只是候选，不直接沉淀。

## 当前不做

- 不修改 `summary.md` 渲染结构化字段。
- 不自动写 long-term memory。
- 不按 `decisions/open_tasks/proposals` 做结构化检索过滤。
- 不为每条结构化字段记录 event-level provenance。

## 后续扩展

建议后续按优先级推进：

1. **Schema version**：为 `ChunkStructuredSummary` 增加 `schema_version`。
2. **字段裁剪/去重**：限制每个列表长度和单条长度，避免 LLM 输出污染 index。
3. **结构化 recall 过滤**：支持按 `decisions/open_tasks/proposals` 搜索。
4. **discarded_options**：记录被明确否掉的方案。
5. **memory candidate review**：对候选长期记忆做半自动确认，而不是直接保存。

## 验证

当前实现已通过：

```bash
cd src/agent
go test ./internal/agent
```

测试覆盖：

- structured summary 写入 chunk index。
- recall 返回 structured 字段。
- MemoryManager 调用 structured summarizer。
- LLM strict JSON 解析成功。
- LLM 非 JSON 输出 fallback，不阻断压缩。
