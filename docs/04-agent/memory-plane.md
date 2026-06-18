# Memory Plane 设计

本文设计 Aiden 新的 memory 部分，目标是在现有对话记忆之外增加设备环境记忆和任务 episode 记忆，并让 runtime 在每次任务开始前自动检索相关经验，在任务结束后自动沉淀成功路径和失败原因。

## 当前架构约束

现有 Go Agent 的 memory 已经有几类能力：

- `MemoryManager` 维护 langchaingo 的 conversation window，并把会话事件写入 `/userdata/agent/memory/session/events.jsonl`。
- `SessionMemoryStore` compresses older events from the active session into chunks, writes `session/summary.md`, and provides `recall_session_chunks` for active-session chunks only.
- `LongTermMemoryStore` 将 profile、rule、preference、procedure、fact 存为 markdown frontmatter，生成 `long_term/index.yaml` 和 `long_term/profile.md`，并提供 `recall_memory`、`save_memory`、`forget_memory`。
- `Runtime.Run` reads only the current active `session/summary.md` and `long_term/profile.md` for prompt context. Long-term memory retrieval mainly depends on the model calling tools; it is not stable input for every planning pass. Closed sessions under `session_archive/` are logs and are not prompt or recall context.
- Agent loop 已拆成 `planner`、`executor`、`verifier`，并采用 `default` / `plan` / `execution` 三阶段状态机。简单任务在 `default` 由 planner 直接调工具并结束；复杂任务经 `enter_plan_mode` -> `commit_plan` 进入 `execution`，再由 `executor` / `verifier` 协作。`planner` 和 `verifier` 能看到历史与 world state；`executor` 被刻意限制为只执行 planner 已提交的 `next_step`。

新设计应保留这个分层：自动检索进入 `planner` 和 `verifier`，不要把所有历史经验直接暴露给 `executor`。

## 设计目标

1. 运行前自动检索设备经验，降低每次任务的预热成本。
2. 运行后自动写入任务 episode，保存目标、环境、状态序列、工具序列、结果和经验。
3. 长期记忆支持 TTL、证据、适用条件和冲突处理，避免过期经验污染规划。
4. 相似成功任务进入 planner，失败经验进入 verifier；如果未来增加独立 policy 角色，同一份失败经验可直接转入 policy。
5. 继续使用现有文件系统持久化、原子写入、index 重建和 lifecycle 验证思路，先不引入数据库。

## 总体结构

新增 `MemoryPlane` 作为 runtime 内部编排层。它不是模型工具，而是 `Runtime.Run` 的固定前置和后置步骤。

```text
RunRequest
  │
  ▼
MemoryPlane.Retrieve
  │
  ├─ active session summary/profile
  ├─ device/app/procedure/calibration/failure memory
  └─ task episode retrieval
  │
  ▼
phased role loop (default / plan / execution)
  │
  ▼
TaskEpisodeWriter
  │
  ├─ write task episode
  ├─ extract reusable procedures
  ├─ extract failure memories
  └─ mark conflicts / refresh validation
```

建议新增代码边界：

| 文件 | 职责 |
| --- | --- |
| `memory_plane.go` | `MemoryPlane.Retrieve`、role memory context 分流、ranking |
| `device_memory.go` | device/app/procedure/calibration/failure 的 schema、store、search |
| `task_episode.go` | `TaskEpisode`、`TaskEpisodeStore`、`TaskEpisodeWriter` |
| `memory_lifecycle.go` 或扩展 `lifecycle.go` | TTL、过期清理、traceability 验证 |
| 扩展 `runtime.go` | 在 role profiles 构建前 Retrieve，run 结束后 CommitEpisode |
| 扩展 `role_profile.go` | planner/verifier 使用不同 memory context |
| 扩展 `role_executor.go` | 输出结构化 planner/verifier/tool trace 给 writer |

## 存储布局

沿用 `/userdata/agent/memory`，新增 `device` 和 `episodes`。现有目录保持兼容。

```text
/userdata/agent/memory/
├── default.json                 # 现有 conversation window snapshot
├── session/
│   ├── events.jsonl             # 现有热会话事件
│   ├── summary.md               # 现有压缩摘要
│   └── chunks/
├── session_archive/
│   └── <closed_session_id>/      # closed session logs, excluded from active context
├── long_term/
│   ├── profile.md               # 现有用户 profile
│   ├── index.yaml
│   └── memories/
├── device/
│   ├── profile.yaml             # Device Profile
│   ├── apps/
│   │   ├── index.yaml
│   │   └── <app_id>.yaml        # App Profile
│   ├── procedures/
│   │   ├── index.yaml
│   │   └── <procedure_id>.yaml
│   ├── calibration/
│   │   ├── index.yaml
│   │   └── <calibration_id>.yaml
│   └── failures/
│       ├── index.yaml
│       └── <failure_id>.yaml
├── episodes/
│   ├── index.yaml
│   └── <yyyy>/<episode_id>/
│       ├── episode.yaml
│       ├── events.jsonl
│       └── artifacts/
└── lifecycle/
    ├── retention.yaml
    ├── tombstones.jsonl
    └── conflicts.jsonl
```

MVP 可以先把可复用经验继续写入 `long_term/memories`，通过新增 `type` 区分 `device_profile`、`app_profile`、`procedure`、`calibration`、`failure`、`task_episode_summary`。但 episode 本体应独立放在 `episodes/`，避免把完整 trace 塞进长期 markdown。

## MemoryPlane API

建议 runtime 只依赖一个窄接口：

```go
type MemoryPlane interface {
    Retrieve(ctx context.Context, req MemoryRetrieveRequest) (MemoryContext, error)
    NewEpisodeRecorder(req MemoryRetrieveRequest, retrieved MemoryContext) *EpisodeRecorder
    CommitEpisode(ctx context.Context, episode TaskEpisode) error
}
```

`MemoryRetrieveRequest`：

```go
type MemoryRetrieveRequest struct {
    Input        string
    Attachments  []InputAttachment
    Skills       []string
    ToolNames    []string
    DeviceID     string
    CurrentHints CurrentEnvironmentHints
}
```

`CurrentHints` 不应主动操作设备。它只承载 runtime 已知信息，例如配置中的 frame socket、最近一次截图尺寸、上次识别到的 app/page。是否截图仍应由 planner 决定。

`MemoryContext` 按角色拆分：

```go
type MemoryContext struct {
    Planner  RoleMemoryContext
    Verifier RoleMemoryContext
    Common   RoleMemoryContext
}

type RoleMemoryContext struct {
    DeviceProfile      []MemoryHit
    AppProfiles        []MemoryHit
    Procedures         []MemoryHit
    SimilarEpisodes    []MemoryHit
    CalibrationNotes   []MemoryHit
    FailureModes       []MemoryHit
    Conflicts          []MemoryHit
}
```

分流规则：

- `planner` 接收 Device Profile、App Profile、Interaction Procedures、Calibration Memory、相似成功 Task Episodes。
- `verifier` 接收 Failure Memory、冲突记忆、与任务相关的校验经验，以及 planner 使用过的关键证据引用。
- `executor` 默认不接收全局 memory；它仍只看到 `next_step`、最新 world state 和上一步局部结果。需要使用经验时，由 planner 把经验压缩成具体下一步。

当前代码没有独立 `policy` role，因此失败经验先进入 `verifier`。未来增加 policy 时，可直接复用 `MemoryContext.Verifier.FailureModes` 作为 `PolicyHints`。

## 数据模型

### Device Profile

设备级稳定信息，低频更新。

```yaml
id: device_default
type: device_profile
status: active
device_id: default
model: ""
os_name: android
os_version: ""
language: zh-CN
screen:
  screenshot_width: 640
  screenshot_height: 1200
  density: unknown
confidence: 0.8
evidence_refs:
  - type: episode
    id: ep_xxx
ttl: 90d
updated_at: "..."
```

### App Profile

描述 app 名称、别名、入口、登录状态、常见页面结构和弹窗。

```yaml
id: app_wechat
type: app_profile
status: active
device_id: default
app_id: wechat
display_names: ["微信", "WeChat", "weixin"]
aliases: ["wx", "we chat"]
open_methods:
  - method: system_search
    query: weixin
    success_count: 3
    failure_count: 0
known_entries:
  search_box: "top search field on main page"
common_dialogs:
  - permission_contacts
login_state: unknown
applicability:
  language: zh-CN
  screen: "640x1200"
confidence: 0.75
ttl: 30d
```

### Interaction Procedure

验证过的操作流程。它不是完整 episode，而是从 episode 提炼出来的可复用策略。

```yaml
id: proc_open_app_system_search
type: procedure
status: active
scope: device
title: "Open apps through system search"
goal_pattern: "open_app"
steps:
  - "Use touch_gesture home."
  - "Open system search."
  - "Input pinyin or English alias with keyboard_text."
  - "Select the visible app result."
applicability:
  device_id: default
  language: zh-CN
  input_method: pinyin
evidence_refs:
  - type: episode
    id: ep_xxx
success_count: 4
failure_count: 1
confidence: 0.82
ttl: 45d
```

### Calibration Memory

设备控制链路经验，必须带适用条件和证据。

```yaml
id: cal_normalized_coord_reliable
type: calibration
status: active
title: "Prefer normalized coordinates"
content: "normalized coordinates align better with current screenshot than pixel coordinates."
applicability:
  screen: "640x1200"
  hid_profile: default
evidence_refs:
  - type: episode
    id: ep_xxx
confidence: 0.9
ttl: 30d
```

### Failure Memory

失败模式进入 verifier/policy，避免重复走坏路径。

```yaml
id: fail_direct_chinese_input
type: failure
status: active
failure_mode: "keyboard_text_cannot_input_chinese"
trigger:
  tool: keyboard_text
  input_contains_non_ascii: true
lesson: "Use pinyin or English keywords, then select candidate/search result."
applicability:
  device_id: default
  language: zh-CN
evidence_refs:
  - type: episode
    id: ep_xxx
    event_ids: ["evt_1", "evt_2"]
confidence: 0.95
ttl: 60d
```

### Task Episode

完整任务经历，用于相似任务检索和经验提炼。

```yaml
id: ep_20260601_xxx
status: active
started_at: "..."
ended_at: "..."
user_goal: "打开微信，找张三，准备发送消息"
normalized_goal:
  intent: open_contact_chat
  app: 微信
  target: 张三
device_scope:
  device_id: default
  language: zh-CN
  screen: "640x1200"
initial_state:
  summary: "home screen"
  screenshot_ref: artifacts/initial.jpg
outcome:
  success: true
  final_state: "张三聊天页"
  verifier_reason: "visible chat title matches target"
retrieved_memory_refs:
  - proc_open_app_system_search
reusable_lessons:
  - "打开微信用系统搜索比桌面翻页稳定。"
failure_causes: []
conflicts: []
```

`events.jsonl` 记录完整序列：

```json
{"type":"planner_decision","plan":["home","system search","open WeChat"],"next_step":"go home"}
{"type":"tool_call","tool_name":"touch_gesture","tool_input":"{\"type\":\"home\"}"}
{"type":"tool_result","tool_name":"touch_gesture","is_error":false,"screenshot_ref":"artifacts/step_001.jpg"}
{"type":"verifier_decision","can_finish":false,"reason":"contact not open yet"}
```

episode 不应长期保存大段 base64。截图落到 `artifacts/`，事件里只放相对路径、尺寸、hash、必要摘要和短 evidence excerpt。

## 检索策略

`MemoryPlane.Retrieve` 分三步：

1. 解析查询：从用户目标提取 intent、app、实体、人名、操作类型、工具需求。MVP 可复用 `MemoryExtractionConfig` 的 tag/entity 规则，并补充 app alias 表；后续可加轻量 LLM query normalizer。
2. 候选召回：从 `device/`、`episodes/index.yaml`、`long_term/index.yaml` 召回 active 且未过期的候选。
3. 排序裁剪：每类保留少量高价值结果，避免 prompt 膨胀。

建议 scoring：

```text
score =
  0.35 * goal_similarity
+ 0.20 * entity_or_app_match
+ 0.15 * applicability_match
+ 0.10 * confidence
+ 0.10 * recency
+ 0.10 * validation_score
- 0.30 * conflict_or_stale_penalty
```

硬过滤：

- `status != active` 不进入普通检索。
- `expires_at < now` 不进入 prompt，但可进入后台刷新候选。
- `applicability` 与当前设备明显不匹配时不进入 prompt。
- evidence 缺失或 traceability 断裂的记忆降权；关键校准类记忆 evidence 缺失则不使用。

## Prompt 注入格式

替换当前单一 `memoryContextForPrompt()`，生成 role-specific context：

```text
# Retrieved Device Experience

## Device/App Context
- [mem_id confidence=0.82] ...

## Applicable Procedures
- [proc_id] When goal is ..., prefer ...

## Similar Successful Episodes
- [ep_id] Goal: ... Path: ... Outcome: ...

## Calibration Notes
- [cal_id] ...
```

Verifier 额外注入：

```text
# Known Failure Modes And Conflicts

- [fail_id confidence=0.95] Trigger: ... Avoid/verify: ...
- [conflict_id] Memory A conflicts with Memory B. Do not rely on either unless current observation proves it.
```

Role 约束：

- planner 可以使用经验修改计划，但必须仍以当前截图和工具结果为准。
- verifier 不能因为历史 episode 成功就批准本次完成，必须要求本次 observation 证明完成条件。
- executor 不直接看经验，减少它绕过 planner 或按旧经验盲点的概率。

## 写入策略

`TaskEpisodeWriter` 在 `Runtime.Run` 结束后执行，成功和失败都写。

成功 episode 写入：

- 用户目标、completion criteria、最终状态。
- planner 决策序列和 plan revisions。
- 工具调用序列、工具结果、post-action screenshot refs。
- verifier 最终通过理由。
- 可复用经验候选，例如更稳定的入口、已验证坐标系、等待时间。

失败 episode 写入：

- 失败阶段：planning、tool_error、state_mismatch、verification_timeout、max_iterations、model_parse_error。
- 失败原因：工具错误、页面未变化、弹窗遮挡、输入不被接受、OCR/视觉不确定、重复点击无效等。
- 负面经验候选进入 Failure Memory。
- 如果失败发生在使用某条 procedure 之后，降低该 procedure 的 confidence 或增加 `failure_count`。

写入时机：

- 正常完成：同步写 episode metadata 和事件摘要，后台提炼长期经验。
- run 错误或超时：尽量写 partial episode，`outcome.success=false`。
- 如果 writer 失败，只记录日志，不影响用户最终响应。

## 冲突处理

长期记忆增加统一生命周期字段：

```yaml
status: active | replaced | deleted | conflicted | expired
ttl: 30d
expires_at: "..."
applicability: {}
evidence_refs: []
last_validated_at: "..."
success_count: 0
failure_count: 0
conflicts_with: []
supersedes: ""
superseded_by: ""
```

冲突来源：

- 新 observation 与 active memory 明显相反。
- 相同 app/goal/procedure 下，成功路径和失败路径互相否定。
- 当前设备 profile 变化，例如语言、分辨率、输入法变化。
- verifier 多次因同一经验导致失败。

处理规则：

1. 同一事实的更具体新证据优先 supersede 旧记忆。
2. 无法判断谁正确时，两条都标为 `conflicted`，不进入 planner 普通经验，只进入 verifier 的冲突提醒。
3. 过期不是删除。先标 `expired` 或从 index 过滤，GC 再按 retention 删除。
4. 用户显式指令优先级高于自动经验。与用户规则冲突时，自动经验降权或标 conflict。

## TTL 建议

| 类型 | 默认 TTL | 续期条件 |
| --- | --- | --- |
| Device Profile | 90d | 被当前截图/配置再次验证 |
| App Profile | 30d | app 打开、页面结构或入口再次验证 |
| Interaction Procedure | 45d | 过程成功且 verifier 通过 |
| Calibration Memory | 30d | 坐标、等待、截图尺寸再次验证 |
| Failure Memory | 60d | 同类失败再次出现 |
| Task Episode | 30d 完整 trace，180d 摘要 | 被长期记忆引用则保留 evidence excerpt |

`LifecycleManager.Verify` 应扩展为：

- 重建所有 index。
- 过滤 expired/conflicted/replaced memory。
- 检查 episode/artifact 引用是否存在。
- 将缺失原始 trace 的长期记忆降级为 `traceability: excerpt_only`。
- 根据 retention 清理无引用 episode artifacts。

## 与现有代码的接入点

### Runtime

`Runtime.Run` 中，在 `r.buildRoleProfiles(...)` 之前执行：

```go
retrieveReq := MemoryRetrieveRequest{
    Input:       normalizedInput,
    Attachments: req.Attachments,
    Skills:      skillNames,
    ToolNames:   toolNames(availableTools),
}
memoryContext, _ := r.memoryPlane.Retrieve(ctx, retrieveReq)
recorder := r.memoryPlane.NewEpisodeRecorder(retrieveReq, memoryContext)
```

然后把 `memoryContext` 传给 `buildRoleProfiles`。run 结束后：

```go
episode := recorder.Finish(output, metrics, err, tags, entities)
go r.memoryPlane.CommitEpisode(context.Background(), episode)
```

### Role executor

`roleLoopState` 已有 objective、criteria、plan、tool steps、verifier results 和 world state。需要补充：

- parsed planner decision event。
- parsed verifier decision event。
- initial/final world state summary。
- tool result screenshot artifact export hook。
- max iteration / parse error / tool error 的 failure reason。

建议通过 `EpisodeRecorder` 接口记录，而不是从 HTTP server 的 `history` 反推。server history 是 UI artifact，不应成为 memory writer 的唯一来源。

### Role profiles

`buildRoleProfiles` 接收结构化的 `MemoryContext` 并返回 `RoleProfiles`：

```go
func buildRoleProfiles(..., memory MemoryContext) RoleProfiles
```

The planner prompt receives `memory.Planner` plus `memory.Common`. The verifier prompt receives only `memory.Verifier.FailureModes` and `memory.Verifier.Conflicts` as a caution block; `memory.Common` is not injected into verifier. The executor prompt receives no memory context.

### Tools

现有 `recall_memory`、`save_memory` 继续保留给模型和用户显式记忆使用。自动化任务经验不要求模型主动调用工具。

可选新增只读工具：

- `recall_device_memory`：调试用，查看 Device/App/Procedure/Failure。
- `inspect_episode`：调试用，根据 episode id 查看 trace。

这两个工具不是 MVP 必需；核心路径应由 runtime 自动检索。

## MVP 分期

第一阶段：episode 记录与检索注入

- 新增 `TaskEpisodeStore` 和 `TaskEpisodeWriter`。
- 记录 planner/tool/verifier trace。
- 新增 `MemoryPlane.Retrieve`，先从 `long_term` 和 `episodes/index.yaml` 做关键词召回。
- planner/verifier 分角色注入 retrieved context。

第二阶段：设备记忆类型

- 新增 `device/` store 和 schema。
- 从 episode 自动提炼 App Profile、Procedure、Calibration、Failure。
- 扩展 `LongTermMemoryStore` frontmatter：TTL、applicability、evidence refs、conflict fields。

第三阶段：冲突与生命周期

- 扩展 `LifecycleManager.Verify` 和 retention GC。
- 检索时过滤 expired/conflicted/replaced。
- writer 根据成功/失败更新 confidence、success_count、failure_count。

第四阶段：benchmark

- 增加 episode memory benchmark：同类任务第二次执行是否减少工具步数、是否避开历史失败路径。
- 增加冲突 benchmark：语言/分辨率变化后旧经验不应继续污染 planner。
- 增加失败经验 benchmark：失败模式应进入 verifier，防止重复批准无证据完成。

## 验收标准

- 清空 session conversation 后，相同设备上的相似任务仍能从 episode/procedure 中获得经验。
- 相似成功 episode 会改变 planner 的 plan 或 next_step，并在 role_output 中可见 memory ref。
- 已知失败模式会进入 verifier prompt；verifier 不会仅因历史成功经验而通过本次任务。
- 过期、冲突、适用条件不匹配的记忆不会进入 planner 普通经验。
- 任务失败也会产生 episode，并能提取可检索的 failure memory。
- 写入 memory 失败不会让用户任务失败，但必须有日志。

---

## 实现进展 (2026-06-02)

### 已完成：第一阶段 + 第二阶段核心

#### Episode 记录与检索 (第一阶段)

✅ 已实现完整的 episode 记录：
- `TaskEpisodeStore` 和 `EpisodeRecorder` 完整实现
- 记录 planner/tool/verifier trace，生成结构化 `events.jsonl`
- `MemoryPlane.Retrieve` 从 `long_term`、`device`、`episodes` 召回并按角色分流
- planner/verifier 分角色注入 retrieved context

#### 设备记忆类型 (第二阶段核心)

✅ 已实现所有设备记忆类型：

**1. Device Profile**  
- 自动记录屏幕尺寸、语言等设备信息
- 从 episode 中推断并更新 device profile

**2. App Profile（增强版）**  
- **累积式更新**：跨多次 episode 积累 `pages_seen`、`tools_used`、`success_count`
- **失败追踪**：记录 `known_issues` 列表
- **可读内容**：渲染为 "Pages observed: 首页, 购物车 / Tools used: launch_app, touch_gesture"
- 支持 ASCII 安全路径（处理中文 app 名）

**3. Procedure（增强版）**  
- **动作详情存储**：新增 `ProcedureStep` 结构，记录每步的：
  - 工具名、description（从 tool_call arguments 自动提取）
  - 坐标（`x=500,y=850`）、输入文本
  - app_name、page_name、outcome_note
- **按页面索引**：procedure ID 改为 `proc_<hash(app, page, goal)>`，page_name 进入 entities/tags
- **结构化渲染**：在 prompt 中展开前 5 步，显示完整操作路径

**4. Navigation Memory（新增）**  
- 抽取**页面间转移规则**：`美团/首页 → 美团/购物车`
- 记录工具、坐标、描述，与具体任务目标解耦
- 与 procedure 同级路由到 Planner

**5. Calibration Memory**  
- 记录归一化坐标偏好等校准信息
- 带 applicability 条件和 evidence refs

**6. Failure Memory**  
- 失败时写入 failure 类型记忆
- 路由到 Verifier 提示已知失败模式

#### 数据抽取能力

新增 `episode_extraction.go`，提供完整的 episode 事件解析：

- **工具调用解析**：
  - `extractToolCallDescription`：从 JSON 提取 description 字段
  - `extractToolCallCoords`：解析 tap/swipe 坐标
  - `extractToolCallText`：提取输入文本参数

- **步骤抽取**：
  - `episodeProcedureSteps`：配对 tool_call 与 tool_result，吸收 verifier observed_state
  - `summarizeProcedureSteps`：生成 LLM 友好的多行摘要

- **统计分析**：
  - `observedPagesByApp`：收集每个 app 出现过的页面
  - `observedToolsByApp`：统计每个 app 用到的工具
  - `pageTransitions`：抽取页面转移规则

#### 检索与渲染

- ✅ `routeHit`：按 memory 类型路由到 Planner/Verifier 的不同字段
- ✅ `renderMemoryHitLine`：procedure/navigation 展开 Steps 渲染
- ✅ `RenderForRole`：按角色生成不同的 memory context

#### 目录结构

已实现完整目录布局：
```text
memory/device/
├── profile.yaml           # 设备 profile
├── apps/                  # app profile（累积更新）
├── procedures/            # 任务 procedure（带 Steps）
├── navigation/            # 页面转移规则 (新增)
├── calibration/
└── failures/

memory/episodes/
├── index.yaml
└── <yyyy>/<episode_id>/
    ├── episode.yaml
    └── events.jsonl
```

#### 测试覆盖

新增 `device_memory_enhancements_test.go`，覆盖：
- ✅ Procedure Steps 提取、坐标解析、description 保留
- ✅ Page_name 索引和 entities 包含 page
- ✅ Navigation 规则抽取
- ✅ App profile 跨 episode 累积
- ✅ 失败时记录 known_issues

所有既有测试通过。

### 待完成：第三阶段 + 第四阶段

#### 冲突与生命周期 (第三阶段)

🔲 基础字段已添加（`success_count`、`failure_count`、`conflicts_with`、`last_validated_at`），但自动冲突检测逻辑待增强：
- ⏳ 根据多次失败自动标记 conflicted
- ⏳ 检索时硬过滤 conflicted/expired memory
- ⏳ 更新已引用 memory 的 confidence（部分实现在 `updateReferencedMemoryOutcomes`）

#### Benchmark (第四阶段)

🔲 待增加自动化 benchmark：
- ⏳ 同类任务第二次执行减少工具步数
- ⏳ 失败经验防止 verifier 误判
- ⏳ 设备环境变化后旧经验不污染

### 变更文件

| 文件 | 变更内容 |
|------|---------|
| `device_memory.go` | 扩展 `DeviceMemoryItem`（Steps、AppName、PageName、PagesSeen、ToolsUsed、KnownIssues）；新增 `ProcedureStep`；新增 `Get()` 方法；支持 navigation/ui_anchor 目录 |
| `memory_plane.go` | 重写 `extractDeviceLessons`，新增 6 个辅助函数；扩展 `routeHit` 和 `renderMemoryHitLine`；新增 `mergeUniqueStrings`、`appendUniqueMemoryRef` |
| `task_episode.go` | `TaskEpisodeEvent` 新增 `ToolDescription`；`RecordExecution` 提取 description |
| `episode_extraction.go` | **新文件**：完整的工具调用解析、步骤抽取、页面/工具统计、转移规则抽取 |
| `device_memory_enhancements_test.go` | **新文件**：5 个测试用例覆盖全部增强点 |

### 效果对比

| 场景 | 设计目标 | 当前实现 |
|------|---------|---------|
| 相似任务复用 | "在美团点蜜雪冰城"后再执行"在美团点星巴克"能复用导航 | ✅ 命中"美团/首页→购物车"navigation + "购物车"页 procedure |
| Planner 看到的 procedure | 详细的步骤、坐标、页面转移 | ✅ "step 1: launch_app(美团)→首页 / step 2: touch_gesture(@x=500,y=850)→购物车" |
| App 先验知识 | 首次使用某 app 能看到常用页面和工具 | ✅ app_profile 累积 pages_seen、tools_used |
| 失败经验追踪 | 失败模式进入 verifier | ✅ failure memory 路由到 Verifier.FailureModes |
| 页面级索引 | 按 page_name 检索 procedure | ✅ procedure ID 包含 page，page_name 在 entities/tags |

### 与设计文档的差异

1. **UI Anchor 未实现**：预留了目录和类型，但尚未实现"从成功 tap 累积归一化坐标"逻辑
2. **冲突检测部分实现**：基础字段齐全，自动检测逻辑在 `updateReferencedMemoryOutcomes` 有部分实现，需进一步完善
3. **Query Normalizer 简化**：当前直接用关键词匹配 + inferEpisodeApps，未引入独立的 LLM query normalizer

### 下一步优化方向

1. **UI anchor 实现**：从成功的 tap 中累积归一化坐标，记录"某 app/page 下某元素通常在 (x,y)"
2. **增强冲突检测**：设备环境变化（语言/分辨率）时主动降低旧 procedure 的 confidence
3. **语义检索**：接入轻量 embedding 提高召回精度
4. **Benchmark 覆盖**：验证 memory 对任务成功率和步数的实际影响
