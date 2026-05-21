# Aiden 文件系统记忆设计

> 版本：v1.0  
> 状态：实施基准版  
> 适用场景：Aiden 单 agent、基本单会话、板端文件系统持久化。  
> 核心目标：短期记忆可压缩可召回，长期记忆可召回可追溯，生命周期可管理，不引入 SQLite。

---

## 1. 设计结论

Aiden 的记忆系统不做复杂图谱、不做完整数据库化 L0-L3 管线，也不在 MVP 引入 SQLite、向量库或独立 Memory Service。

采用一个更轻的文件系统模型：

```text
短期记忆：events.jsonl + summary.md + chunks
长期记忆：memories/*.md + profile.md
召回索引：index.yaml
追溯链路：source_refs + evidence_excerpt
生命周期：status + tombstones.jsonl + GC 扫描
```

关键链路只保留一条：

```text
session event / chunk
  ↓ source_refs
long_term memory
  ↓ source_memories
profile.md
```

也就是：

```text
原始事件 → 长期记忆 → 用户画像
```

这条链路已经满足：

- 短期会话不断片。
- 长会话可压缩卸载。
- 历史 chunk 可按需召回。
- 长期偏好、规则、流程、失败教训可召回。
- 用户画像可在启动时快速恢复。
- 原始证据可追溯，原始事件被 GC 后长期记忆仍自包含。
- 记忆可删除、可 tombstone、可物理 GC。

---

## 2. 设计原则

### P1：短期和长期职责分离

短期记忆服务当前任务连续性，长期记忆服务跨时间沉淀。

```text
Session Memory:    当前任务怎么继续
Long-term Memory:  以后还应该记住什么
User Profile:      用户稳定偏好和规则是什么
```

### P2：文件系统是真源，索引是缓存

```text
events.jsonl        是 session 事件真源
chunks/*.jsonl      是卸载片段真源
memories/*.md       是长期记忆真源
profile.md          是用户画像视图
index.yaml          是可重建缓存
```

如果 `index.yaml` 丢失或损坏，直接扫描 Markdown frontmatter 重建。

### P3：长期记忆必须自包含证据

每条长期记忆都必须包含：

- `source_refs`：弱引用，指向 event/chunk。
- `evidence_excerpt`：强引用，内嵌脱敏证据摘录。

这样即使 event/chunk 被生命周期管理清理，长期记忆仍能解释“为什么这么记”。

### P4：关系只保留关键链路

MVP 不维护复杂 `links.jsonl` 拓扑。

需要反向引用时，通过扫描 `long_term/memories/*.md` 和 `profile.md` 即可完成。单 agent、基本单会话场景下，这比维护图谱更可靠。

### P5：生命周期显式管理

删除不是直接物理删除，而是：

```text
active
  ↓
inactive / replaced / deleted
  ↓
tombstoned
  ↓
physical_gc
```

进入 prompt 和检索前必须过滤 `status != active` 的内容。

---

## 3. 存储布局

```text
<configDir>/memory/
│
├── MEMORY.md                         # 启动入口：用户画像摘要 + 当前状态索引
│
├── default.json                      # 兼容现有 ConversationWindowBuffer snapshot，过渡期保留
│
├── session/                          # 短期记忆
│   ├── state.yaml                     # 当前 session 状态
│   ├── events.jsonl                   # 当前事件流，append-only
│   ├── summary.md                     # 当前会话滚动摘要
│   └── chunks/
│       ├── index.yaml                 # chunk 召回索引
│       └── chunk_*.jsonl              # 被压缩卸载的原始事件片段
│
├── long_term/                         # 长期记忆
│   ├── profile.md                     # 用户画像，启动必读
│   ├── index.yaml                     # 长期记忆检索索引，可重建
│   └── memories/
│       └── mem_*.md                   # 长期记忆单元
│
└── lifecycle/                         # 生命周期管理
    ├── retention.yaml                 # 保留策略
    ├── tombstones.jsonl               # tombstone 记录
    ├── gc_log.jsonl                   # GC 执行日志
    └── txn.jsonl                      # 简单写入事务日志，用于崩溃恢复
```

MVP 必需文件：

```text
session/events.jsonl
session/summary.md
session/chunks/index.yaml
long_term/profile.md
long_term/index.yaml
long_term/memories/*.md
lifecycle/retention.yaml
lifecycle/tombstones.jsonl
```

---

## 4. 短期记忆

短期记忆负责当前 session 内的上下文连续性。

### 4.1 写入事件

每轮交互先追加到 `session/events.jsonl`：

```jsonl
{"event_id":"evt_01HXYZ","ts":"2026-05-19T10:00:00+08:00","type":"user_input","role":"user","source":"voice","content":"帮我看看这个登录失败是什么意思","app_name":"某政务App","risk_level":"normal"}
{"event_id":"evt_01HXYA","ts":"2026-05-19T10:00:02+08:00","type":"screen_context","role":"screen","source":"vlm","content":"页面提示：验证码已过期，请重新获取。","app_name":"某政务App","risk_level":"normal"}
{"event_id":"evt_01HXYB","ts":"2026-05-19T10:00:05+08:00","type":"assistant_output","role":"assistant","content":"这是验证码过期，需要重新获取验证码。"}
```

事件字段：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `event_id` | 是 | 全局唯一，建议 ULID |
| `ts` | 是 | ISO-8601 时间 |
| `type` | 是 | `user_input` / `screen_context` / `assistant_output` / `tool_call` / `tool_result` / `action_result` / `system_event` |
| `role` | 是 | `user` / `assistant` / `screen` / `tool` / `system` |
| `source` | 否 | `voice` / `button` / `vlm` / `hid` / `system` |
| `content` | 是 | 脱敏后的文本内容 |
| `app_name` | 否 | 当前 App |
| `risk_level` | 否 | `normal` / `high` |
| `tool_call_id` | 否 | 工具调用关联 ID |

写入规则：

- 用户输入、屏幕摘要、assistant 回复、工具调用和结果都进入事件流。
- 用户输入和 assistant 回复必须同步落盘。
- 高频屏幕摘要可以批量缓冲，但写入前必须脱敏。

### 4.2 Hot Window

现有 `ConversationWindowBuffer` 继续作为 hot window。启动恢复时，优先从 `session/events.jsonl` 末尾读取最近 N 轮用户输入、屏幕摘要、assistant 回复和工具结果，重建 hot window。

默认 prompt 中包含：

```text
session/summary.md
+ ConversationWindowBuffer 最近 N 轮
+ 当前屏幕摘要
```

`default.json` 过渡期保留，用于兼容现有实现。稳定后，`events.jsonl` 末尾 N 轮是 hot window 恢复真源，`default.json` 可废弃。

### 4.3 会话压缩

触发条件：

```text
Hot Window 轮数 > 20
Hot Window token 估算 > 4000
events.jsonl > 50MB
session idle > 10 分钟
用户切换任务
高风险任务完成
```

压缩流程：

```text
1. 从 events.jsonl 选择待卸载事件范围。
2. 保留最近 N 轮在 Hot Window。
3. 把较旧事件写入 session/chunks/chunk_<id>.jsonl。
4. 生成 chunk summary、tags、entities、risk_types。
5. 更新 session/chunks/index.yaml。
6. 更新 session/summary.md。
7. 从 ConversationWindowBuffer 移除已卸载内容。
```

`summary.md` 示例：

```markdown
# Current Session Summary

updated_at: 2026-05-19T10:10:00+08:00

## 当前任务

用户正在处理某政务 App 登录失败问题。

## 已知上下文

- 页面提示验证码已过期。
- 用户希望继续验证码登录，不切换到密码登录。

## 未完成事项

- 等待用户重新获取验证码。
- 重新提交登录。

## 可沉淀长期候选

- 某政务 App 登录时验证码容易过期，下次应先重新获取验证码。
```

### 4.4 Chunk 索引

`session/chunks/index.yaml`：

```yaml
version: 1
updated_at: "2026-05-19T10:10:00+08:00"
chunks:
  - id: chunk_01HXYZ
    file: chunk_01HXYZ.jsonl
    status: active
    summary: 用户在某政务 App 登录页遇到验证码过期。
    app_name: 某政务App
    tags: [登录, 验证码]
    entities: [某政务App]
    risk_types: []
    time_range: ["2026-05-19T10:00:00+08:00", "2026-05-19T10:10:00+08:00"]
    event_count: 42
    checksum: "sha256:..."
```

短期召回流程：

```text
1. 根据当前 app、用户输入、tags、entities 查询 chunks/index.yaml。
2. 取 top-N chunk。
3. 读取 chunk_*.jsonl。
4. 校验 checksum。
5. 返回 summary + 相关事件片段。
```

---

## 5. 长期记忆

长期记忆负责跨时间沉淀：偏好、规则、流程、事实、失败教训。

MVP 只使用一个实体：`MemoryItem`。

### 5.1 MemoryItem 文件格式

`long_term/memories/mem_*.md`：

```markdown
---
id: mem_01HXYZ
type: preference
status: active
priority: 80
confidence: 0.9
tags: [登录, 验证码, 政务App]
entities: [某政务App]
time_scope: long_term
created_at: "2026-05-19T10:15:00+08:00"
updated_at: "2026-05-19T10:15:00+08:00"
source_refs:
  - type: chunk
    id: chunk_01HXYZ
    event_ids: [evt_01HXYA, evt_01HXYC]
supersedes: null
superseded_by: null
traceability: full
---

# 某政务 App 验证码登录偏好

## 内容

用户下次登录某政务 App 时，应先重新获取验证码，不主动切换到密码登录。

## 证据摘录

- 用户说：“记一下，这个 App 下次登录要先重新获取验证码。”
- 屏幕显示：“验证码已过期，请重新获取。”

## 适用条件

- App: 某政务App
- 场景: 登录页出现验证码过期或验证码相关提示
```

`type` 枚举：

```text
preference      用户偏好
rule            必须遵守的规则，尤其是高风险操作规则
procedure       可复用操作流程
fact            稳定事实
failure_lesson  失败教训
```

`status` 枚举：

```text
active      正常使用
inactive    用户否认或人工停用
replaced    被新记忆取代
conflicted  与另一条记忆冲突，等待用户确认
deleted     用户要求遗忘，不再使用
```

### 5.2 长期记忆写入

抽取触发条件：

```text
用户说“记一下”
用户说“以后 / 下次 / 不要再 / 你要记住”
用户主动纠正 Aiden
用户给出高风险操作规则
任务完成或失败后 summary 出现长期候选
```

写入动作只保留五类：

| 动作 | 含义 |
| --- | --- |
| `add` | 新增长期记忆 |
| `ignore` | 重复或无长期价值，不写入 |
| `supersede` | 新记忆取代旧记忆，旧记忆 `status=replaced` |
| `conflict` | 新旧记忆冲突，标记 `conflicted` 等待用户确认 |
| `delete` | 用户主动遗忘，标记 `deleted` |

MVP 不做复杂 `merge`、`refine`、`depends_on` 级联。

### 5.3 长期索引

`long_term/index.yaml` 是可重建缓存：

```yaml
version: 1
updated_at: "2026-05-19T10:15:00+08:00"
memories:
  - id: mem_01HXYZ
    file: memories/mem_01HXYZ.md
    type: preference
    status: active
    priority: 80
    confidence: 0.9
    tags: [登录, 验证码, 政务App]
    entities: [某政务App]
    summary: 某政务 App 登录时优先重新获取验证码。
```

长期召回流程：

```text
1. 从当前用户输入、屏幕摘要、app_name 提取 tags/entities/risk_level。
2. 查询 long_term/index.yaml。
3. 过滤 status=active。
4. 按 priority、confidence、tag/entity overlap、recency 排序。
5. 读取 top-N mem_*.md。
6. 注入 prompt。
```

---

## 6. 用户画像

`long_term/profile.md` 是用户画像视图，启动必读。

它不是唯一真源。长期记忆真源仍是 `memories/*.md`。

`profile.md` 面向人和 prompt，不在正文里暴露 `status`、`priority`、`confidence`、`source_memories` 这类机器字段。机器字段放在 `long_term/index.yaml` 的 `profile_items` 中，或由 `memories/*.md` 反向扫描得到。

`profile.md` 示例：

```markdown
# User Profile

updated_at: 2026-05-19T10:20:00+08:00

## 沟通偏好 {#pref_comm_direct}

用户偏好直接、简洁的语音回复。解释手机操作时，不需要过多铺垫。

## App 操作偏好 {#pref_gov_app_login}

用户在某政务 App 登录时偏好验证码登录。验证码过期时应先重新获取验证码，不主动切换到密码登录。

## 高风险操作规则 {#rule_confirm_before_payment}

涉及支付、转账、授权、提交表单前，必须先读出关键信息并等待用户确认。
```

对应机器索引放在 `long_term/index.yaml`：

```yaml
profile_items:
  - id: pref_comm_direct
    anchor: profile.md#pref_comm_direct
    status: active
    priority: 80
    confidence: 0.9
    source_memories: [mem_01HAAA]
    summary: 用户偏好直接、简洁的语音回复。
  - id: pref_gov_app_login
    anchor: profile.md#pref_gov_app_login
    status: active
    priority: 70
    confidence: 0.86
    source_memories: [mem_01HXYZ]
    summary: 某政务 App 登录时优先重新获取验证码。
  - id: rule_confirm_before_payment
    anchor: profile.md#rule_confirm_before_payment
    status: active
    priority: 100
    confidence: 0.95
    source_memories: [mem_01HBBB]
    summary: 高风险操作前必须读出关键信息并等待确认。
```

画像更新流程：

```text
1. 新增或更新 MemoryItem。
2. 判断是否影响 profile。
3. 若影响，重写 profile.md。
4. profile item 保留 source_memories。
```

启动时默认注入 prompt：

```text
MEMORY.md
long_term/profile.md
session/summary.md
Hot Window（从 session/events.jsonl 末尾恢复最近 N 轮）
```

`long_term/index.yaml` 和 `session/chunks/index.yaml` 不进 prompt。它们只供检索层在需要 recall 时读取；实现上可以懒加载，也可以启动后读入内存作为轻量缓存。prompt 中不默认加载所有 `memories/*.md`，只按当前 query 检索。

---

## 7. MEMORY.md 启动入口

`MEMORY.md` 是人工和 Agent 都能快速阅读的恢复入口。

示例：

```markdown
# Aiden Memory

## 用户画像摘要

- 用户偏好直接、简洁的回复。
- 高风险操作前必须读出关键信息并等待确认。
- 某政务 App 登录时优先重新获取验证码。

## 当前会话

- 当前任务：处理某政务 App 登录失败。
- 当前摘要：见 session/summary.md。

## 重要索引

- 用户画像：long_term/profile.md
- 长期记忆索引：long_term/index.yaml
- 会话 chunk 索引：session/chunks/index.yaml
- 生命周期策略：lifecycle/retention.yaml
```

维护规则：

- 每次 profile 发生重要变化后，更新 `MEMORY.md` 的摘要。
- `MEMORY.md` 保持短小，作为入口，不承载完整长期记忆。
- 详细内容仍在 `profile.md` 和 `memories/*.md`。

---

## 8. 生命周期管理

生命周期管理负责 event、chunk、memory、profile 的保留、删除和 GC。

### 8.1 retention.yaml

```yaml
event_compact_after: 7d
chunk_no_ref_ttl: 30d
chunk_referenced_keep: forever
memory_deleted_ttl: 90d
tombstone_ttl: 30d
gc_interval: 24h
```

### 8.2 Tombstone

`lifecycle/tombstones.jsonl`：

```jsonl
{"id":"tomb_01HXYZ","ts":"2026-05-19T11:00:00+08:00","ref":{"type":"memory","id":"mem_01HXYZ"},"reason":"user_forget","physical_gc_after":"2026-08-17T11:00:00+08:00"}
```

### 8.3 Memory 删除

用户要求忘记某条长期记忆时：

```text
1. 将 mem_*.md frontmatter status 改为 deleted。
2. 追加 tombstones.jsonl。
3. 从 long_term/index.yaml 移除或标记 deleted。
4. 如果 profile.md 引用了该 memory，更新 profile.md。
5. 到 TTL 后物理删除 mem_*.md。
```

### 8.4 Chunk / Event GC

chunk 是否可删由扫描长期记忆决定：

```text
1. 扫描 long_term/memories/*.md 的 source_refs。
2. 被 active memory 引用的 chunk 默认不自动删除。
3. 未被引用的 chunk 超过 TTL 后可 tombstone + 物理删除。
4. 即使 chunk 删除，MemoryItem 仍保留 evidence_excerpt。
```

`events.jsonl` 可周期 compact：

```text
1. 已压缩到 chunk 的旧事件可从 events.jsonl 移除。
2. 移除前必须确保 chunk checksum 正常。
3. 如果长期记忆 source_refs 指向裸 event_id，应先迁移为 chunk + event_ids。
```

### 8.5 启动校验

启动时运行轻量 verify：

```text
1. index.yaml 指向的 mem_*.md 必须存在，否则重建 index。
2. memories/*.md 缺少 evidence_excerpt 时告警，并禁止其 source chunk GC。
3. source_refs 指向已删除 chunk 时，把 traceability 标记为 excerpt_only。
4. profile.md 中 source_memories 不存在时告警并移除对应引用。
```

---

## 9. 写入一致性

不用 SQLite 的代价是必须有简单的文件写入恢复机制。

长期记忆写入顺序：

```text
1. append lifecycle/txn.jsonl: begin(txn_id)
2. 写 mem_*.md.tmp
3. rename mem_*.md.tmp → mem_*.md
4. 重建 long_term/index.yaml.tmp
5. rename index.yaml.tmp → index.yaml
6. 如影响 profile，写 profile.md.tmp 并 rename
7. 如影响 MEMORY.md，写 MEMORY.md.tmp 并 rename
8. append lifecycle/txn.jsonl: commit(txn_id)
```

启动恢复：

```text
1. 扫描 txn.jsonl 中未 commit 的 txn。
2. 如果 mem 文件已存在但 index 缺失，重建 index。
3. 如果 index 指向不存在文件，删除 index 项。
4. 如果 profile 引用不存在 memory，告警并移除引用。
```

原则：

- Markdown 文件和 JSONL 事件流是真源。
- `index.yaml` 可以随时重建。
- 不做跨文件强事务，只做可恢复写入。

---

## 10. 内部接口

### 10.1 SessionMemory

```go
type SessionMemory interface {
    AppendEvent(ctx context.Context, event SessionEvent) (string, error)
    LoadSummary(ctx context.Context) (string, error)
    UpdateSummary(ctx context.Context, summary string) error
    Compress(ctx context.Context, opt CompressOption) (ChunkSummary, error)
    RecallChunks(ctx context.Context, query ChunkRecallQuery) ([]ChunkRecallResult, error)
}
```

### 10.2 LongTermMemory

```go
type LongTermMemory interface {
    LoadProfile(ctx context.Context) (string, error)
    Search(ctx context.Context, query MemoryQuery) ([]MemoryResult, error)
    AddMemory(ctx context.Context, item MemoryItem) (string, error)
    SupersedeMemory(ctx context.Context, oldID string, next MemoryItem) (string, error)
    MarkConflict(ctx context.Context, aID string, bID string, reason string) error
    Forget(ctx context.Context, id string, reason string) error
    RebuildIndex(ctx context.Context) error
}
```

### 10.3 LifecycleManager

```go
type LifecycleManager interface {
    Verify(ctx context.Context) (VerifyReport, error)
    MarkForGC(ctx context.Context) ([]GCCandidate, error)
    RunGC(ctx context.Context, candidates []GCCandidate) (GCReport, error)
    CompactEvents(ctx context.Context) error
    RebuildIndexes(ctx context.Context) error
}
```

---

## 11. 和现有 MemoryManager 的整合

现有实现位于：

```text
src/agent/internal/agent/memory.go
```

当前能力：

```text
MemoryManager
└── ConversationWindowBuffer / ConversationBuffer
    └── default.json snapshot
```

改造后：

```text
MemoryManager
├── ConversationWindowBuffer       # Hot Window，保留
├── SessionMemory                  # events.jsonl / summary / chunks / recall
├── LongTermMemory                 # profile / memories / index
└── LifecycleManager               # tombstone / GC / verify
```

每轮处理流程：

```text
1. 用户输入 / 屏幕摘要到达。
2. SessionMemory.AppendEvent 写 events.jsonl。
3. 写入 ConversationWindowBuffer。
4. LongTermMemory.Search 检索相关长期记忆。
5. PromptBuilder 组装：profile + summary + hot window + retrieved memory + screen + user input。
6. Agent 推理并执行工具。
7. 记录 assistant_output / tool_call / tool_result。
8. 如果命中长期抽取条件，写 MemoryItem 并更新 profile/index。
9. 如果 hot window 超限，SessionMemory.Compress。
```

---

## 12. 实施阶段

### Phase 1：Session 事件流

- 新增 `SessionMemory.AppendEvent`。
- 写 `session/events.jsonl`。
- 保留 `default.json` 兼容现有 hot window。
- 启动时可从 `events.jsonl` 末尾恢复 hot window。

交付：重启后当前会话上下文可恢复。

### Phase 2：摘要与 chunk 召回

- 写 `session/summary.md`。
- 实现 `Compress`，生成 `chunks/chunk_*.jsonl`。
- 实现 `chunks/index.yaml`。
- 实现 `RecallChunks`。

交付：短期记忆可压缩、可召回。

### Phase 3：长期记忆

- 实现 `long_term/memories/*.md`。
- 实现 `long_term/index.yaml`。
- 支持 `add / ignore / supersede / conflict / delete`。
- 强制每条长期记忆包含 evidence_excerpt。

交付：长期记忆可保存、可召回、可追溯。

### Phase 4：用户画像

- 实现 `long_term/profile.md`。
- 启动加载 profile。
- MemoryItem 更新后按需更新 profile。
- 维护 `MEMORY.md` 作为启动入口。

交付：用户偏好和高风险规则可稳定注入启动上下文。

### Phase 5：生命周期

- 实现 `retention.yaml`。
- 实现 `tombstones.jsonl`。
- 实现 verify / GC / index rebuild。
- 实现 chunk 引用扫描。

交付：记忆可遗忘，文件不会无限膨胀。

---

## 13. 后续可扩展性

这个设计刻意保持小核心，但保留清晰扩展点。

### 13.1 扩展 L2 Scene

如果后续需要“完整场景记忆”，可以新增：

```text
long_term/scenes/scene_*.md
long_term/scenes/index.yaml
```

Scene 只作为 `memories/*.md` 的聚合视图，不改变现有 memory 真源。

扩展链路：

```text
chunk/event → memory → scene → profile
```

现有 `source_refs` 和 `source_memories` 可以继续使用。

### 13.2 扩展 rules.md / procedures.md

如果高风险规则和操作流程变多，可以从 `memories/*.md` 派生物化视图：

```text
long_term/rules.md
long_term/procedures.md
```

它们仍不是唯一真源，只是为了更快注入 prompt 和 PolicyGate 检索。

### 13.3 扩展 links.jsonl

如果未来出现大量 memory 间关系，再引入：

```text
long_term/links.jsonl
```

但它只用于加速关系查询，不取代 frontmatter 中的关键链路。

可选关系：

```text
source
supersedes
contradicts
related_to
depends_on
```

单 agent 单会话阶段不需要提前实现。

### 13.4 扩展向量索引

如果关键词/tag 检索不够，可以新增向量缓存：

```text
long_term/vector.index
session/chunks/vector.index
```

向量索引必须是可重建缓存，不是真源。删除或损坏后可从 Markdown/JSONL 重建。

### 13.5 扩展多 session

当前设计是基本单会话。后续如果需要多 session，只需增加归档目录：

```text
session/active/
session/archive/session_<id>/
```

并在 `source_refs` 中带 `session_id`。

### 13.6 扩展多 agent

如果以后从单 agent 演进到多 agent，可以把当前目录作为 agent namespace：

```text
memory/agents/<agent_id>/...
```

当前 schema 不需要重写。

### 13.7 扩展云同步

如果未来需要跨设备同步，可以基于文件 manifest：

```text
manifest.json
checksums.json
```

同步层只同步文件和冲突版本，不改变本地记忆模型。

### 13.8 扩展 PolicyGate

高风险操作规则可以先放在 `profile.md` 和 `type=rule` 的 MemoryItem 中。

后续 PolicyGate 直接检索：

```text
profile.md 高优先级规则
+ long_term/index.yaml 中 type=rule 的记忆
```

无需先实现完整 Skills Memory。

---

## 14. 总结

Aiden 当前阶段采用：

```text
短期：events + summary + chunks
长期：memories + profile
召回：index.yaml
追溯：source_refs + evidence_excerpt
生命周期：status + tombstone + GC 扫描
```

这套设计满足当前关键需求，同时避免把文件系统提前做成弱版数据库。

后续扩展遵守同一原则：

```text
新增能力可以加视图、索引、聚合层；
不要破坏 memories/*.md 和 events/chunks 这两个真源。
```
