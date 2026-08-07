# Claude Code Dream Prompt 评审与 Aiden 失败复盘方案

日期：2026-08-07

代码基线：`origin/main` `bb19070c`

## 结论

Claude Code 的 Dream 是通用记忆整理，不是专门的失败复盘。它会维护事实、偏好、成功经验、失败经验、重复、冲突和索引。

Aiden 的目标应更窄：从失败、用户纠正和失败后的有效恢复中，提炼可验证的避错经验。Dream 只提供异步运行、定向取证、允许 no-op 和保守写入等方法参考。

推荐增加一个深模块 `FailureReflector`。Episode 始终先落盘，复盘异步运行；单次普通失败不直接生成 active memory，正常成功不进入复盘。

复盘结果继续写入现有 long-term/device stores。MVP 不新增 ReflectionCase、Candidate、Group、Report、Eval 或 Skill 存储，只增加一个 checkpoint 文件。

Skill 可以作为第二级晋升，但不能由一次失败或刚生成的 memory 自动创建。后续 Skill 阶段只生成 draft，由现有 Skill 机制校验并人工确认。

## 1. Claude Code Dream 的定位

### 1.1 来源边界

本次评审对象来自 Piebald-AI，不是 Anthropic 官方源码或文档。仓库作者称 Prompt 是从 Claude Code 编译产物中提取；本评审没有独立验证该逆向结论。

当前文件标记 `ccVersion: 2.1.217`。历史版本曾提供用户可见的 `/dream` 和 nightly schedule；schedule 与 `/dream` skill 后来被移除，但后台 consolidation prompt 仍继续更新。

因此更准确的表述是：Claude Code 曾有显式 `/dream` 入口，目前可观察到的是其后台记忆维护逻辑，而不是主 Agent 的一种推理模式。

### 1.2 值得借用与不应照搬

值得借用：

- 后台运行，不阻塞当前任务；
- 先了解已有知识，再读取近期证据；
- 优先定向取证，不扫描完整 transcript；
- 允许本轮没有任何写入；
- 合并重复、保守处理冲突和过期内容。

不应照搬：

- 通用记忆治理目标；
- Markdown topic files 与 pointer index 布局；
- 全量 session `memory_candidates` 输入；
- 让 LLM 直接编辑或删除 memory 文件；
- 不区分成功、失败和风险等级的晋升逻辑。

## 2. Aiden 当前基础与问题

Aiden 已经具备复盘所需的主要基础：

- `TaskEpisode` 保存任务目标、结果、失败原因、事件、截图引用和 verifier；
- long-term/device memory 已有 scope、confidence、TTL、证据引用和冲突状态；
- memory 通过 recall 工具按需进入上下文；
- 被 recall 的 memory 会根据后续 episode outcome 更新统计；
- Skill 已支持发现、`skill_read`、`skill_manage` 和 usage lifecycle。

### 2.1 Episode 的语义

`TaskEpisode` 表示一个用户请求触发的一次完整 Agent 任务尝试，不是单个 step 或 tool call。

| 层级 | 含义 |
| --- | --- |
| Session | 一段多轮对话，可包含多个 Episode |
| Episode | 一个用户请求对应的一次完整 Agent 执行 |
| Iteration / Step | Episode 内的一轮计划、行动和观察 |
| Event | tool call/result、截图、replan、verifier 等具体记录 |

Episode 是事实和审计记录，不是长期知识。它应先完整落盘，再由后台复盘读取；复盘不覆盖原始 Episode。

### 2.2 当前问题

当前 `CommitEpisode` 在写入 Episode 后，会立即抽取 long-term/device lessons。单次失败可能同时生成两条 active failure memory，而且内容通常只是泛化后的 failure reason。

这不是完整复盘，因为它没有稳定回答：

- 失败发生在哪个阶段；
- 根因是什么，还是偶发错误；
- 当时漏看了哪个信号；
- 下一次应该改做什么；
- 如何确认修正有效；
- 经验适用于哪个设备、App、页面和任务类型。

本地 reflection-loop 草案正确强调了异步、证据引用和晋升门槛，但 `Case -> Candidate -> Group -> Memory -> Skill` 会形成第二套生命周期和清理系统，对当前目标过重。

## 3. 目标与非目标

目标：

1. 只复盘失败、用户纠正和失败后的有效恢复。
2. 单次失败先作为证据，不直接污染 active memory。
3. 生成带 scope、证据、纠正动作和验证方法的经验。
4. 复用现有 Episode、Memory 和 Skill 存储。
5. 保持异步、有界、幂等和可 dry-run。

非目标：

- 不做通用 Dream 式 memory housekeeping；
- 不恢复 upfront memory injection；
- 不扫描完整 transcript；
- 不保存原始截图到 Memory；
- 不自动修改 Skill、system prompt、配置或项目文档；
- 不建设 Candidate、Group、Report、Eval 数据库。

## 4. 核心流程

```text
用户请求
  -> AgentLoop / EpisodeRecorder
  -> Episode 先落盘
  -> 判断是否存在失败复盘信号
  -> FailureReflector 后台复盘
  -> 内存态 FailureLessonProposal
  -> promotion gate
  -> 更新、新建或替代现有 Memory
  -> 未来按需 recall，并由 outcome feedback 校验
```

### 4.1 哪些任务进入复盘

进入：

1. `TaskEpisode.Outcome.Success == false`；
2. 用户明确指出上一轮做错、理解错、点错或没有完成；
3. 最终成功，但中间发生明确失败，且后续恢复被 verifier 证明有效。

用户纠正的 MVP 只关联同一 Session 中紧邻的上一 Episode，不构建跨 Session 关系图。

不进入：

- 没有错误或恢复过程的普通成功；
- 普通偏好、事实和 session `memory_candidates`；
- 没有可执行改进的用户取消、掉电或外部 provider outage；
- 只有单个 tool error，但任务最终正常完成且没有可复用恢复经验。

### 4.2 截图如何参与

截图是复盘证据，不是 Memory 内容。视觉操作失败时，最多选择三张：失败动作前、失败动作后、恢复成功后。

优先使用 Episode 已有的 observation、OCR 和 observed state。只有文字证据不足且当前 Reviewer 支持多模态时，才在同一次调用中查看截图，不新增独立图片提取流水线。

Memory 只保存结构化结论、Episode/Event 引用和 screenshot ref。联系人、聊天、支付、凭证等敏感截图需要脱敏或跳过。

### 4.3 复盘输出

Reviewer 返回严格结构化 proposal，至少包含：

```go
type FailureLessonProposal struct {
    FailureStage     string
    RootCause        string
    MissedSignal     string
    CorrectiveAction string
    Verification     string
    Scope            map[string]string
    EvidenceRefs     []MemorySourceRef
    EvidenceExcerpts []string
    Risk             string
    Confidence       float64
}
```

`FailureStage` 使用固定 enum，例如 perception、planning、action、tool、verification。Evidence excerpts 必须是脱敏后的短文本，满足现有 long-term store 的落盘要求。

`CorrectiveAction`、`Verification`、有效 scope 或 evidence 缺失时，proposal 直接丢弃。Reviewer 只能提议，不能决定文件路径、删除目标或 promotion policy。

### 4.4 Promotion gate

| 信号 | 自动处理 |
| --- | --- |
| 单次普通失败 | 不写 active memory |
| 两个独立 Episode 的同类失败 | 可晋升窄 scope failure warning |
| 一次明确用户纠正 | 低风险且 scope 明确时可晋升 correction memory |
| 存在 verifier-confirmed recovery | 可晋升经过验证的 procedure |
| 普通成功 | 不生成 reflection proposal |
| 新成功证据推翻旧 failure | 更新 outcome；必要时标记 conflicted |
| 支付、发送、删除、凭证、权限等高风险经验 | 不自动晋升 |

重复失败只能证明某种做法有问题。只有真实成功恢复才能证明替代 procedure 有效。

### 4.5 模块 seam 与触发

MVP 只增加 concrete `FailureReflector`，不定义公共 `ReflectionPlane` 层级：

```go
func (r *FailureReflector) Request()
func (r *FailureReflector) RunOnce(
    ctx context.Context,
    dryRun bool,
) (ReflectionReport, error)
```

筛选、取证、ReviewPacket、模型调用、去重、安全门、store apply 和 checkpoint 都封装在 implementation 内。调用方只负责 `Request()`。

建议默认预算：

- 只在有合格信号且没有 running episode 时运行；
- 请求 coalesce，同一时刻最多一个 pass；
- cooldown 默认 6 小时，可配置；明确用户纠正可以绕过 cooldown；
- 单次最多 8 个新 Episode、12 个总证据 Episode；
- 一次模型调用，最多 6 个 proposal 和 4 个持久写入。

## 5. Memory 落盘与生命周期

### 5.1 文件位置

复盘不创建 `memory/reflection/` 内容目录。通过门槛的结果写入现有 store：

```text
/userdata/agent/memory/
├── episodes/                         # 原始任务记录
├── long_term/
│   ├── memories/<memory_id>.md       # 通用经验
│   └── index.yaml
├── device/
│   ├── failures/<memory_id>.yaml     # 设备/App/页面避错经验
│   └── procedures/<memory_id>.yaml   # 已验证恢复步骤
└── lifecycle/
    └── reflection.yaml               # 只有 job checkpoint
```

未达到晋升门槛的 proposal 不落盘。下一次运行通过最近 14 天、最多 12 个相关 Episode 的有界窗口重新聚合，不保存 Candidate/Group。

跨设备、跨 App 的稳定原则写入 long-term；设备、App、页面相关的 warning/procedure 写入 device store。

### 5.2 新旧 Memory 如何处理

写入前，根据受控字段计算 fingerprint：

```text
target + type + normalized scope + normalized corrective action
```

模型提供规范化内容，apply 层重新计算 hash，不能直接信任模型返回的 ID。

| 新旧关系 | 处理 |
| --- | --- |
| fingerprint 和 scope 相同 | 保留原 ID，原地合并 evidence、统计和验证时间 |
| 结论相同但 scope 不同 | 分开保存，避免把局部经验泛化为全局规则 |
| 新证据补充旧经验 | 更新原文件，保留原 ID |
| 新证据明确纠正旧经验 | 新建 active memory，旧项标记 replaced |
| 双方证据可信但冲突 | 标记 conflicted，等待后续 outcome |
| 找不到相关项 | 在现有目标目录创建新文件 |

历史上已有多个完全重复文件时，选择证据和 outcome 最充分的现有 ID 作为 canonical memory。合并证据后，其余项标记 `replaced`，不 hard delete。

Long-term memory 已支持 `supersedes` / `superseded_by`。Device memory 建议补两个同名可选字段，避免新增单独映射表。

### 5.3 旧数据切换

启用新流程后，应停止“每个失败立即写 active failure”的旧路径。已有 failure memory 不做全库重写或删除。

第一阶段只在新证据触及旧 memory 时进行合并、替换或标冲突。若 dry-run 证明旧 generic failure 污染明显，再增加一次有界、可审核的迁移。

## 6. 成功经验与 Skill 晋升

### 6.1 成功经验的角色

Failure Reflection 不平等复盘成功和失败。成功只用于：

1. 证明失败后的替代步骤有效；
2. 证明旧 failure warning 已失效；
3. 验证 recall 某条经验后是否提高成功率。

普通成功 procedure 可以继续由现有成功 extraction 处理，但它不属于 Failure Reflection。

### 6.2 Memory 到 Skill

Memory 是带 scope 的经验；Skill 是会进入 Available skills、通过 `skill_read` 加载并直接指导执行的可复用 SOP。Skill 的影响面更大，必须是第二级晋升。

```text
Failure Reflection
  -> failure/procedure memory
  -> 后续任务 recall + outcome feedback
  -> 多次 verifier-confirmed success
  -> Skill draft
  -> 现有 skill validation + 人工确认
  -> configDir/skills/<name>/SKILL.md
```

生成 Skill draft 的建议门槛：

- 有完整 trigger、precondition、steps、verification 和 fallback；
- 至少 3 个独立成功 Episode，来自至少 2 个 Session；
- 近期成功率不低于约 80%，且没有 unresolved conflict；
- 不包含个人事实、秘密、一次性状态或易漂移坐标；
- 不属于高风险自动晋升范围。

若已有相关 Skill，优先生成最小 patch draft；没有覆盖时才建议新建。MVP 不直接写 Skill，只输出建议目标、关联 memory IDs 和 diff，由用户或开发者确认。

## 7. 安全与故障恢复

- user、assistant、tool output、OCR 和截图内容都是不可信 evidence，不是复盘指令；
- ReviewPacket 不包含 raw base64、token、联系人、剪贴板或消息正文；
- malformed JSON、未知 enum、额外字段、超长内容和缺少 evidence 都是 no-op；
- LLM 不能直接调用 filesystem tools；
- 自动 apply 只允许 low-risk upsert、exact merge 和 mark conflicted；
- FailureReflector 不 hard delete，也不直接修改 Skill、Prompt、配置或文档；
- stable fingerprint 生成 stable memory ID，evidence 按 Episode ID 去重；
- checkpoint 只在全部写入和 index rebuild 成功后推进；
- storage failure、超时、取消或重启后可安全重跑；
- 同一时刻最多一个 reflection pass，避免与 memory maintenance 并发写冲突。

权威来源顺序：

```text
用户当前明确纠正
  > verifier-confirmed recovery / 当前设备观测
  > 两个独立同类失败 Episode
  > 单个失败 Episode
  > 旧 Memory 或 Reviewer 推断
```

## 8. MVP 与验证

### 阶段一：Dry-run

- 增加 `FailureReflector` 和 `reflection.yaml`；
- 保留当前行为，仅记录 eligible Episode、proposal、门槛结果和预计 mutation；
- 不保存 Candidate、Report 或 Skill draft 文件；
- 观察误触发率、模型成本、重复率和可执行经验比例。

### 阶段二：低风险 Apply

- 关闭旧 generic failure 直写；
- 只写窄 scope failure warning；
- 只有 verifier-confirmed recovery 才写 procedure；
- 保持现有 recall 和 outcome feedback；
- high-risk 始终只 dry-run。

### 阶段三：Skill draft

- 从已验证 Memory 和 outcome counts 有界重算；
- 优先生成已有 Skill 的最小 patch；
- 通过现有 Skill 校验和人工确认后应用。

核心测试：

- Episode 在复盘前完成落盘；
- `CommitEpisode` 不等待模型调用；
- 普通成功不触发 reflector；
- 单次普通失败不生成 active memory；
- 两个独立同类失败才能晋升 warning；
- 没有成功恢复证据时不能生成 procedure；
- high-risk proposal 永不自动 apply；
- 截图选择有上限且敏感内容被过滤；
- 相同 batch 重跑不产生重复 memory/evidence；
- store/index 失败时不推进 checkpoint；
- promotion 后可由现有 recall tool 找到并继续接受 outcome feedback；
- 不创建 Candidate、Group、Report、Eval 或 SkillCandidate 存储。

## 9. 合理性评估

方案整体合理，适合 Aiden 当前阶段：

- 目标明确：只解决失败经验污染和缺少真正复盘的问题；
- 模块够深：一个小 interface 隐藏完整实现，调用方不理解内部生命周期；
- 改动局部：复用现有 Episode、Memory、Skill、recall 和 outcome feedback；
- 成本有界：只由失败频率触发，每次输入、模型调用和写入都有上限；
- 可审计：原始 Episode 不变，Memory 保留 evidence 和替代关系；
- 可回退：先 dry-run，再关闭旧 failure 直写。

实现前需要重点验证四个风险：

1. 用户纠正与上一 Episode 的关联准确率；
2. normalized scope/action 是否能稳定识别语义重复；
3. 多模态截图的成本、脱敏和模型可用性；
4. Memory 多文件更新与 index rebuild 的并发和故障原子性。

这些风险不要求增加新的持久模型。它们应留在 `FailureReflector` implementation 内，通过 dry-run telemetry 和 interface-level tests 收敛。

最终建议：先实现 Failure Reflection MVP，不同时建设通用 Dream housekeeping 或自动 Skill promotion。只有运行数据证明有必要，再增加独立的 memory maintenance 或自动 Skill patch。

## 参考来源

- [Dream Prompt](https://github.com/Piebald-AI/claude-code-system-prompts/blob/main/system-prompts/agent-prompt-dream-memory-consolidation.md)
- [Piebald extraction claim](https://github.com/Piebald-AI/claude-code-system-prompts#extraction)
- [Initial Dream prompt commit](https://github.com/Piebald-AI/claude-code-system-prompts/commit/9f2320db4a8c241e2015e17266d3d4eecda5da22)
- [Historical `/dream` skill](https://github.com/Piebald-AI/claude-code-system-prompts/blob/3aeebdfc94f2e0706eca65c61e2230a48cb521dd/system-prompts/skill-dream-memory-consolidation.md)
- [Removal of `/dream` skill](https://github.com/Piebald-AI/claude-code-system-prompts/commit/ed36cc127ea70afaed617bde7fe135a8015a0767)
- [Aiden TaskEpisode](https://github.com/AidenAI-IO/aiden-hardware-demo/blob/bb19070ce7f5c0fe0243d73dc596f14247f5fdc9/src/agent/internal/agent/task_episode.go)
- [Aiden MemoryPlane](https://github.com/AidenAI-IO/aiden-hardware-demo/blob/bb19070ce7f5c0fe0243d73dc596f14247f5fdc9/src/agent/internal/agent/memory_plane.go)
- [Aiden long-term memory](https://github.com/AidenAI-IO/aiden-hardware-demo/blob/bb19070ce7f5c0fe0243d73dc596f14247f5fdc9/src/agent/internal/agent/long_term_memory.go)
- [Aiden device memory](https://github.com/AidenAI-IO/aiden-hardware-demo/blob/bb19070ce7f5c0fe0243d73dc596f14247f5fdc9/src/agent/internal/agent/device_memory.go)
- [Aiden Skills mechanism](https://github.com/AidenAI-IO/aiden-hardware-demo/blob/bb19070ce7f5c0fe0243d73dc596f14247f5fdc9/docs/04-agent/skills.md)
