# Episode → Memory 参考对照：Chapter 8 与 Claude Code Dream Prompt

## 研究范围

本文对照当前第一版方案 [Aiden Episode 到 Memory](../04-agent/episode-memory-extraction.md)，研究两份公开材料对以下问题的参考意义：哪些 Episode 值得沉淀、何时整合、Memory 如何组织、怎样保留证据、如何合并与检索，以及哪些复杂机制应留到后续版本。

来源固定在以下版本，避免 `main` 后续变化造成内容和行号漂移：

1. [《AI Agent Book》第 8 章：Agent 的持续进化](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md)，提交 `515ae5d53df20aeaaea47d9f904fd5820d386947`。
2. [Agent Prompt: Dream memory consolidation](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md)，提交 `1079f627e155563da543781cd80eae0fe665bbd7`，文件标注适用于 Claude Code `2.1.224`。[Prompt metadata, L1-L20](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L1-L20)

### 关于 Claude 资料的证据边界

第二份资料来自 **Piebald-AI 的第三方公开整理仓库**，不是 Anthropic 官方仓库。该仓库 README 自述内容由 Claude Code 编译产物提取，但这一主张仍由第三方提供。[Piebald README: Extraction](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/README.md#extraction)

因此，本文只把它当作一份公开可见的“Dream consolidation 提示词文本”来分析。它可以证明该提示词要求模型怎样整理 Memory，但不能单独证明：

- Claude Code 何时、以何种触发器运行它；
- 是否每个 Session 都运行；
- 是否在后台、空闲期或独立进程运行；
- 调度、并发、取消、重试和持久化事务如何实现；
- 该提示词是否覆盖 Claude Code 的全部 Memory 机制。

## 一句话结论

当前方案的主轴与两份资料一致：**所有完成 Episode 先作为原始证据落盘，前台不做同步学习，后台查看现有 Memory 后执行 `ignore / create / update`，并避免把全部历史和 Memory 注入下一次任务。**

两份资料带来的不同约束是：

- Chapter 8 提供质量与安全上限：先评价、保留来源、跨轨迹验证、候选与正式能力隔离；
- Dream prompt 提供一个更轻的维护操作范式：先定向现有 Memory，再读取近期信号，优先合并主题文件，最后修剪短索引。

当前第一版最大的有意简化是：**单条 Episode 通过本地校验后即可直接写成 active Memory。** 这比 Chapter 8 的正式知识标准更宽松，应明确视为 V1 的质量风险；但不必因此在第一版立即引入完整的 pending、跨 Episode 晋升和回归验证系统。

## 对照表

| 维度 | Chapter 8 | Dream prompt | 对当前第一版的含义 |
| --- | --- | --- | --- |
| 学习入口 | 已记录且经过评价的成功、失败、部分成功轨迹 | 最近 1–3 天 Session logs、漂移的旧 Memory；必要时窄搜 transcript | 不应按 `Success bool` 过滤；所有结束 Episode 可被扫描，但未必都调用模型或生成 Memory |
| 判断标准 | 环境结果、过程合规、带证据 Rubric；证据不足应保持不确定 | 只说寻找“worth persisting”的信息，具体保存规范委托给另一段 auto-memory prompt | V1 必须自有明确 eligibility 和确定性校验，不能从 Dream prompt 推导完整准入标准 |
| 整合时机 | 在线追加不可变证据，离线按时间、数量、容量或错误门槛批量整理 | 单独执行 Orient → Gather → Consolidate → Prune 的 reflective pass | 支持现有“Episode 先落盘、空闲后处理”，不支持恢复同步 PEV |
| 产物结构 | 原始轨迹 → 单次分析/草案 → 多轨迹正式知识；正式文档含 Scope、策略、例外、证据、最近验证时间 | 顶层 topic files；短 `INDEX_FILE` 或由 frontmatter 生成的索引 | V1 可继续使用 typed YAML Device Memory；格式不是重点，关键是短入口、详细内容分离 |
| 合并更新 | 建支持/反驳证据表，达到门槛后进入正式知识 | 先读现有 topic files，优先 merge，避免近重复；修正冲突与过期事实 | `create/update/ignore` 是合理的最小动作集合；创建前必须搜索候选 Memory |
| 来源与证据 | 强制保留原始证据、位置、时间、版本；反思本身不是证据 | 从 logs/transcripts 采集信号，但该 prompt 没要求把来源字段写进 Memory | 当前 `source_episode_id + evidence_refs` 应保留，并建议同时保留观测时间 |
| 检索成本 | 知识按需检索；长期观察 Token 效率和检索错误 | 不全读 transcript；只窄搜；索引有行数和约 25KB 上限，条目是一行 hook | Recall 应有条数和内容预算；详情按需读取，不能预注入完整 Episode 或全部 Memory |
| 删除与修剪 | 过期、归档、删除，但保留来源与回滚版本 | 删除被反证的事实，移除 stale/superseded 项，修剪索引 | V1 暂不自动删除是稳妥的；自动 pruning 应在版本和回滚能力之后实现 |
| 安全 | 网页、工具输出和摘要都是不可信证据；不得直接升级为长期指令 | 本 prompt 只引用另一段 auto-memory 规则作为“what NOT to save”的权威来源 | 安全边界应采用 Chapter 8，而不能从 Dream prompt 自身补全 |

## 一、哪些 Episode 应进入处理

### 两份资料共同支持的判断

不应使用下面的入口门控：

```text
success → 学习
failed  → 反思
```

Chapter 8 明确指出，成功、失败和部分成功轨迹都能贡献不同信息；成功轨迹提供策略草案，失败轨迹提供排除性知识，部分成功轨迹用于定位有效片段和未完成部分。[Chapter 8, L117-L129](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L117-L129)

Dream prompt 同样不使用 success/failed 过滤，而是读取近期 Session logs，并检查已漂移的 Memory；只有需要补充某个已怀疑的重要细节时才窄搜 transcript。[Dream prompt, L42-L51](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L42-L51)

因此，当前设计的入口是合理的：所有结束 Episode 都进入扫描范围，`Outcome.Success` 只是背景信号，不是学习开关。

### “进入扫描”不等于“生成 Memory”

Chapter 8 强调学习从评价开始，而不是从自由总结开始；环境结果和过程证据比 Agent 自己的最终描述可靠，证据不足时应明确不确定。[Chapter 8, L19-L29](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L19-L29)

结合当前第一版，Episode 真正沉淀为 Memory 的最小标准仍应是：

```text
存在直接证据
+ 对未来相似任务可复用
+ 会改变未来行为或决策
+ 适用 Scope 明确
+ 相对已有 Memory 有新内容或新证据
```

这与现有设计文档的五项标准一致。Dream prompt 的 “worth persisting” 过于宽泛，不能替代上述工程规则；而且它明确把保存格式和“不应保存什么”委托给另一段 auto-memory prompt，因此仅凭该文件无法得到完整准入条件。[Dream prompt, L53-L60](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L53-L60)

## 二、何时提炼：支持后台，但不证明具体调度

Chapter 8 明确主张在线/离线双循环：在线 Agent 只完成任务并追加不可变证据，后台在空闲期或门控条件满足时读取一批经历，整合、验证和修剪。[Chapter 8, L376-L386](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L376-L386)

Dream prompt 本身就是一个独立的 consolidation pass，分为：

```text
Orient existing memories
→ Gather recent signal
→ Consolidate
→ Prune/index
```

[Dream prompt, L35-L69](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L35-L69)

这两点共同支持我们维持：

```text
前台：Episode 完整落盘 + 非阻塞通知
后台：资格检查 + 提炼 + create/update/ignore
```

但 Dream prompt 没有说明真实触发条件，不能用它证明 Claude Code 一定在“空闲两分钟”后运行，或一定支持前台到来时取消。因此，我们自己的 idle、抢占和 retry 规则仍须由 Aiden 的 latency 约束独立定义。

## 三、Memory 输出与存储形态

### Chapter 8 的理想形态

Chapter 8 推荐三层结构：不可变原始轨迹、单次运行分析/经验草案、多条同类轨迹支持的正式知识文档；正式文档应写清适用场景、策略、禁止项、例外、来源和最近验证时间。[Chapter 8, L107-L121](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L107-L121)

### Dream prompt 的操作形态

Dream prompt 要求先浏览已有 topic files，优先更新已有主题而不是制造近重复；Memory 内容进入 topic file，索引只保存简短指针。若索引不是自动生成的，还要求保持在 `${INDEX_MAX_LINES}` 行和约 25KB 以内，每条入口约 150 字符。[Dream prompt, L35-L40](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L35-L40) [Dream prompt, L62-L69](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L62-L69)

### 对当前存储方案的结论

两份资料都不要求 Memory 必须使用 Markdown。当前第一版继续把 Episode-derived Memory 写入 `device/procedures`、`device/failures`、`device/memories` 和 `device/calibration` 是合理的；类型化 YAML 更符合现有 `DeviceMemoryStore` 和 recall 路径。

Dream prompt 真正值得借鉴的是存储原则，而不是文件扩展名：

1. 先看已有 Memory，再决定 create/update；
2. 主题正文与检索入口分离；
3. 索引或自动注入内容必须有容量上限；
4. 详细历史按需读取，不进入每次任务的固定上下文。

## 四、证据与 provenance

Chapter 8 在这一点上比 Dream prompt 严格得多：反思和 LLM 摘要不是证据；系统应保留原始位置、来源、采集时间和版本，且不可信网页/工具文本不能直接成为长期指令。[Chapter 8, L117-L121](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L117-L121) [Chapter 8, L366-L374](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L366-L374)

Dream prompt 规定从 Session logs 和窄范围 transcript search 采集近期信号，但没有在这份 prompt 内要求把原始日志位置写入最终 Memory。[Dream prompt, L42-L55](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L42-L55)

因此应继续采用当前方案中更强的约束：

- `source_episode_id` 必填；
- `evidence_refs` 至少一条且必须真实存在；
- 建议同时保存 `observed_at` 或等价 Episode 时间；
- Agent 最终声称完成、`runErr == nil`、模型反思文本都不能单独构成证据；
- 网页和工具输出只作为不可信 evidence，不得直接升级为行为指令。

Dream prompt 另有一个低成本细节值得吸收：相对时间应转换成绝对时间，避免 Memory 在未来失去语义。[Dream prompt, L53-L60](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L53-L60)

## 五、Create、Update 与冲突处理

当前第一版的 `ignore / create / update` 与 Dream prompt 高度一致：它明确要求优先把新信号合入已有 topic files，而不是创建近重复文件，并修正已被当前证据推翻的旧事实。[Dream prompt, L53-L60](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L53-L60)

Chapter 8 则给出更严格的长期目标：每个经验草案记录支持与反驳轨迹，只有达到支持门槛才进入正式知识；环境变化时应能精确撤销结论。[Chapter 8, L111-L121](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L111-L121)

对第一版的取舍应是：

- 保留 `ignore / create / update`；
- create 前搜索有限数量候选；
- update 只能更新输入给模型的候选 Memory；
- 追加去重后的 Episode evidence，不整篇自由重写；
- 第一版不允许自动 delete；发现强冲突时宁可 ignore/记录待处理，也不要直接删除旧 Memory。

最后一点有意比 Dream prompt 保守。Dream prompt 允许删除 contradicted facts 和 stale/superseded Memory，但如果没有版本、快照和回滚能力，自动删除不适合直接搬进 V1。[Dream prompt, L57-L69](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L57-L69)

## 六、Recall 与上下文成本

Dream prompt 对成本控制给出三个明确模式：

1. 不完整读取大型 transcript；
2. 只对已怀疑重要的细节做 narrow grep；
3. 索引只是短 hook，详细内容放在主题文件中。[Dream prompt, L29-L51](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L29-L51) [Dream prompt, L62-L69](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L62-L69)

Chapter 8 也把 Token 效率列为持续进化的长期指标，并指出知识库增长会带来冲突和检索错误。[Chapter 8, L333-L341](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L333-L341) [Chapter 8, L405-L412](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L405-L412)

因此当前方案除了“前台零新增 LLM/工具调用”之外，还应在实现验收时观察：

- 每次自动激活的 Memory 条数；
- 注入 Agent context 的摘要长度；
- recall 命中后是否还会加载大量无关正文；
- Memory 增长后检索延迟和误召回是否上升。

这不要求第一版新增路由 LLM。继续复用本地 `recall_device_memory`，只返回少量候选和短指导即可。

## 七、与当前第一版的主要冲突或缺口

### 1. 单 Episode 直接 active，弱于 Chapter 8 的正式门槛

当前设计允许一条 Episode 经一次 LLM 提炼和本地 schema 校验后直接成为 active Memory。Chapter 8 要求正式经验获得跨轨迹支持，并在未参与提炼的新任务上验证正迁移；一次自然语言反思本身不构成证据。[Chapter 8, L117-L129](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L117-L129)

这是 V1 最主要的质量差距。第一版若保持直接 active，应把风险限制在：

- 只写低作用面的 Device Memory；
- Scope 尽量窄；
- 必须带真实证据引用；
- 不允许写入全局 Prompt、Skill 或硬安全规则；
- 不把“动作执行成功”提炼成“目标已完成”的 Procedure。

### 2. 同一次 LLM 同时评价 outcome 并提出 Memory

当前设计让一次后台 LLM 同时输出 `goal_outcome` 和 create/update 提案。Chapter 8 更推荐把评价与进化职责分开，避免同一个模型既当裁判又直接改规则。[Chapter 8, L62-L64](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L62-L64)

第一版不必增加第二次 LLM 调用，但应降低该字段的权威性：环境状态和工具证据是硬约束，LLM 的 `goal_outcome` 只是提炼辅助判断；没有完成证据时，不得据模型自评生成完整 Procedure。

### 3. 每 Episode 一次提炼，弱于批量对照

Chapter 8 的经验来自跨轨迹对照；Dream prompt 也是按近期 Session logs 做一轮批量整理，而不是明示“每个 Episode 固定一次模型调用”。[Chapter 8, L111-L121](https://github.com/bojieli/ai-agent-book/blob/515ae5d53df20aeaaea47d9f904fd5820d386947/book/chapter8.md#L111-L121) [Dream prompt, L42-L58](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L42-L58)

当前逐 Episode 处理更容易实现幂等和故障恢复，可以保留为 V1；批量聚类、支持/反驳表和周期性重整应作为后续增强，而不是第一版前置条件。

## 八、建议的第一版最小修正版

不扩大当前范围，建议把方案收敛为：

```text
所有结束 Episode 完整落盘
→ 空闲 Worker 扫描
→ 确定性资格检查
→ 本地检索少量现有 Device Memory
→ 一次后台 LLM 输出 ignore / create / update
→ 确定性 schema、来源与安全校验
→ 写入对应 Device Memory 类型目录
→ 后续任务通过有界 recall 使用
```

相对当前文档，只需要强调以下最小约束：

1. **全量扫描，不全量沉淀。** 所有完成 Episode 有资格被看见，但空轨迹、无证据和一次性信息直接 ignore。
2. **证据硬于模型评价。** `goal_outcome` 是辅助信息；环境状态、工具结果和用户纠正优先。
3. **先读后写。** create 前必须查现有候选，优先 update，避免近重复。
4. **最小增量更新。** update 追加证据、收窄 Scope 或补清 guidance，不让模型无约束重写整条 Memory。
5. **来源可追溯。** 至少保存 `source_episode_id`、`evidence_refs` 和时间信息。
6. **只进入 Device Memory。** 不自动写 long-term、Prompt、Skill、代码或模型参数。
7. **直接 active 仅作为 V1 简化。** 只允许低作用面、窄 Scope、可追溯的操作经验；把多 Episode 晋升留到后续。
8. **Recall 有界。** 不增加前台 LLM/工具调用，不注入完整 Episode；只给少量短 Memory，详细内容按需读取。
9. **V1 不自动删除。** 冲突、stale、supersede、prune 在具备版本和回滚后再做。
10. **前台 latency 不变。** Dream prompt 只能支持“独立 consolidation pass”的方向，具体 idle、抢占和取消仍由 Aiden 自己实现和测量。

## 九、建议明确延后的能力

以下机制有长期价值，但不应挤入第一版：

- 单 Episode distillation 中间层；
- `pending → active`；
- 多 Episode 支持/反驳门槛；
- 独立 outcome/process/quality verifier；
- held-out 迁移验证与灰度发布；
- Memory 的 confidence、版本、TTL、stale、retired；
- 自动冲突解决、删除、修剪和回滚；
- 周期性 Dream 式全库 consolidation；
- matched/activated/applied/effective 效果归因；
- Prompt、Skill、Harness 或参数演进。

这些是 Chapter 8 所描述的完整持续学习系统的重要组成部分，但不是验证第一版 `Episode → Device Memory` 数据流所必需的能力。

## 最终判断

两份参考没有推翻当前方案，反而让第一版边界更清楚：

```text
Chapter 8 决定质量底线：评价优先、证据可追溯、经验不能等同于轨迹摘要。

Dream prompt 提供维护范式：先理解现有 Memory，再看近期信号，优先合并，保持入口短小。

Aiden 第一版负责最小闭环：所有 Episode 落盘，后台提炼 0/1 条低作用面 Device Memory，
用确定性证据与安全校验兜底，并保持 Agent Loop 零新增同步 latency。
```

第一版可以继续采用 `ignore / create / update`，但应诚实标注：直接 active 是为了快速验证闭环而接受的简化，不等同于 Chapter 8 所定义的“已被跨轨迹和新任务验证的正式经验”。
