# 《AI Agent Book》第 8 章：持续进化——文档研读

## 研究范围与来源

- 研究对象：[《Agent 的持续进化》Chapter 8](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md)，固定在提交 `5de66dc8da3c2dc7db9923bed229f4b42bc58c1b`，避免 `main` 后续变化导致行号漂移。
- 本文只提炼该章自身的目标、架构、机制、实施阶段和风险，不分析本仓库当前分支实现，也不判断两者是否一致。

## 一句话结论

本章主张的不是“让 Agent 每次任务后自由反思并立即改写自己”，而是在模型外围建立一个**证据驱动、离线提案、分层验证、灰度发布、可回滚、可信根不可自改**的持续进化系统；反思只是候选经验的生成手段，不能自行成为证据或发布依据。[原文：保存经历不等于学习，L3-L11](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L3-L11) [原文：Reflexion 反思本身不是证据，L117-L129](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L117-L129)

## 核心目标

1. **把一次运行变成可追溯、可诊断的学习信号。** 系统要判断任务是否真的完成、过程是否合规、完成质量是否合适，而不是只保存对话或让模型自由总结。[原文：评价先于总结，L19-L27](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L19-L27)
2. **把经验路由到最自然的能力载体。** 事实和经验规律进入知识；可语言化、需理解语境的策略进入 Prompt/Skill；确定性流程和硬约束进入程序/Harness；高维隐式能力才进入模型参数。[原文：四种载体与边界，L75-L105](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L75-L105)
3. **让任何持久修改先成为候选版本，再证明它修复了问题且没有破坏已有能力和安全边界。** 目标不是“改了什么”，而是建立从证据、提案、验证、灰度到回滚的统一发布协议。[原文：统一发布协议，L300-L318](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L300-L318)
4. **长期维护能力生命周期。** 系统不仅要记住，还要迁移、更新、保持、修剪、淘汰和回滚，避免知识、Prompt、Skill、工具及参数无限累积和相互冲突。[原文：持续学习的四环节，L343-L349](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L343-L349) [原文：离线整理与能力保鲜，L376-L412](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L376-L412)

## 总体架构

本章的生产架构可以概括为下面这条闭环：

```text
在线执行循环
任务执行 ──> 不可变轨迹/环境结果/用户反馈
                         │
                         ▼
三层评价：结果真值 ──> 过程合规 ──> Rubric 质量
                         │
                         ▼
结构化诊断：问题、证据位置、置信度、适用条件
                         │
                         ▼
离线进化循环
跨轨迹对照/聚类 ──> 根因定位 ──> 最小更新提案
                                         │
            ┌────────────┬───────────────┼──────────────┐
            ▼            ▼               ▼              ▼
          知识       Prompt / Skill   程序 / Harness   模型参数
            └────────────┴───────────────┴──────────────┘
                                         │
                                         ▼
                             待验证能力区 / 候选版本
                                         │
                   边界集 ── 保留集 ── 安全集（逐项 veto）
                                         │
                                         ▼
                                  灰度 / 监控
                                  │         │
                                晋升       回滚

可信根（候选不可修改）：验证器、测试、发布门槛、审计日志、稳定备份
```

- **在线/离线双循环是架构主轴。** 在线循环完成任务并追加证据，不直接修改正式 Agent；离线循环聚合轨迹、诊断、提案和验证，两者由版本化经验库与评估集连接。[原文：双循环，L284-L290](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L284-L290)
- **验证面采用三层结构。** 结果验证器读环境真值，过程验证器检查动作、权限和规则，质量验证器才使用带证据 Rubric 的 LLM 判断；结果与过程是硬门。[原文：三层验证，L23-L29](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L23-L29) [原文：硬门控制关系，L47-L64](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L47-L64)
- **能力面不是单一“反思记忆”，而是四种载体的组合。** 同一能力可以拆分到多种载体，例如事实进知识、例外原则进 Skill、不可绕过的权限进程序、高维识别进参数。[原文：四种方式可以组合，L81-L105](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L81-L105)
- **发布面与正式能力隔离。** 新知识、Prompt、Skill、代码和参数都先放入不能服务真实流量的待验证区，通过安全与回归检查才有资格晋升。[原文：待验证区与正式能力隔离，L366-L374](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L366-L374)
- **治理面有不可修改的可信根。** 业务 Agent 可以提出或修改能力产物，但不能改批准自身更新的验证器、测试、门槛、审计记录和稳定备份。[原文：可信根不可自改，L372-L374](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L372-L374)

## 关键机制

### 1. 从轨迹得到结构化而非标量的评价

一次评价应保留任务结果、规则遵从、隐私、事实可靠性、承诺—行动一致性、表达质量、合规变通等维度，以及每个判断的轨迹证据和置信度；单一成功率或满意度无法区分违规成功和合规失败。[原文：多维 Rubric，L31-L47](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L31-L47)

LLM 验证器本身需要专家标注轨迹校准；高风险或低置信度案例要由第二模型或人工复核；负责评价的模型不应直接决定如何改写 Agent，以避免“同一个模型既当裁判又改规则”。[原文：验证器校准与职责隔离，L62-L72](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L62-L72)

### 2. 跨轨迹归纳，而非单轨迹摘要

正式经验知识建议保留三层数据：不可变原始轨迹、单次运行分析/经验草案、由多条同类轨迹支持的正式知识文档。正式文档应带适用场景、策略、禁止项、例外、证据和最近验证时间。[原文：三层经验数据，L107-L119](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L107-L119)

成功、失败和部分成功轨迹都有用，但自然语言反思只参与生成草案；它必须与环境结果一致、获得跨轨迹支持，并在未参与提炼的新任务上带来正迁移，才可以进入正式经验。[原文：反思不是证据，L121-L129](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L121-L129)

### 3. 最小、带来源、可证伪的更新提案

Prompt/Skill 更新应是作用域明确、带来源的最小 diff，同时在触发失败的边界集和原本正常的保留集上测试，而不是重写整份 Prompt。[原文：最小 Prompt diff，L138-L160](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L138-L160)

代码或 Harness 修改还应声明一份可证伪的变更契约：失败证据、推断根因、所属组件、修改、预期修复、可能受损行为，以及验证二者的用例。提案输入不仅应含失败案例，也要含必须保留的成功行为和此前被拒绝的修改。[原文：变更契约与输入边界，L222-L230](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L222-L230)

### 4. 稳定流程“编译”为可验证程序

稳定、重复、可验证的经验应从自然语言规则下沉为工作流、工具或 Harness。一个可复用工作流至少经历：轨迹捕获、参数化、动作前/后及最终状态检查、重置环境后的独立回放、匹配执行、失败后失效并重新探索。[原文：程序化工作流生命周期，L194-L219](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L194-L219)

Agent 自改代码不等于运行进程覆盖自身；应在隔离分支上生成最小补丁，再经过静态检查、单测、安全扫描、失败轨迹重放、旧任务回归和灰度部署。[原文：自修改的软件发布语义，L222-L224](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L222-L224)

### 5. 统一的候选验证与发布协议

所有载体遵守同一协议：候选必须分别通过 `boundary_set`（修复触发问题）、`retention_set`（保持原有能力）和 `safety_set`（守住不可牺牲边界）；任何一项失败都应否决，不能用平均分抵消安全失败。之后才进行小流量灰度、监控、晋升或回滚。[原文：候选、三套集合和灰度回滚，L300-L318](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L300-L318)

### 6. 分开评价“会更新”和“会受益”

章节区分：更新器能否提出有价值的持久修改（harness-updating），以及任务 Agent 后续能否检索、激活和遵循该修改（harness-benefit）。因此需分别观测更新提案有效率、产物激活率、遵循成功率和留出任务增益，不能只看一个端到端总分。[原文：更新能力与受益能力，L320-L331](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L320-L331)

### 7. 睡眠学习与主动遗忘

“睡眠学习”是离线整合循环：达到时间/数量/容量/错误频率门槛后，读取已有能力边界，聚合已评价轨迹，生成局部提案，在迁移/保留/安全集上验证并审批，最后更新索引、过期或归档旧能力，同时保留来源和回滚版本。[原文：五步睡眠周期，L376-L399](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L376-L399)

## 建议的实施阶段（按章节依赖关系整理）

以下是对全章机制的工程落地顺序归纳，并非原文给出的固定里程碑名称。

### 阶段 0：先划可信根与权限边界

先固定哪些对象永远不允许进化提案修改：验证器、测试/评估集、发布门槛、审计日志、稳定版本备份；把不可信证据、候选能力、正式能力分区存放。没有这层，后续“自我改进”可能通过删测试或降低门槛伪造进步。[原文：三道安全边界，L366-L374](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L366-L374)

### 阶段 1：先建设可观测轨迹和三层验证

记录任务、工具动作、环境状态、反馈和版本；实现结果/过程的确定性验证，再补充带 Rubric、证据和不确定度的质量评价，并用专家标注样本校准验证器。章节明确把“评价”而非“总结”视为持续进化起点。[原文：评价起点与三层验证，L19-L29](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L19-L29) [原文：验证器校准，L62-L64](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L62-L64)

### 阶段 2：建立离线证据与诊断流水线

保存不可变轨迹、单次结构化诊断、跨轨迹支持/反驳表；证据不足的偶发故障只积累样本，不触发持久更新。先定位根因，再选最小且容易验证、回滚的修改对象。[原文：知识提炼五步，L111-L121](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L111-L121) [原文：根因先于修改，L294-L298](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L294-L298)

### 阶段 3：先上线低作用面的知识和局部 Skill/Prompt 提案

从带来源的知识条目和局部指令 diff 开始，因为这类更新作用面较小、可审计、易回滚。不要一开始就让系统搜索整个 Harness 或优化器；章节明确把局部修改视为默认选择，只有局部修改长期无法解决跨组件问题时才扩大搜索尺度。[原文：优先局部修改，L264-L274](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L264-L274)

### 阶段 4：将稳定经验下沉到程序/Harness

当规则已经稳定、重复、可确定验证时，把它编译为工作流、工具、参数检查、重试/熔断或安全门禁；同时建设独立回放、状态检查、失效重学、沙盒和供应链扫描。[原文：适合程序化的经验，L194-L219](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L194-L219) [原文：程序提案的外部验证，L232-L249](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L232-L249)

### 阶段 5：打通统一发布协议

为每种能力产物建立候选版本、边界集、保留集、安全集、灰度指标和自动回滚；三类验证各自拥有 veto 权，而不是合成总分。长期指标还应覆盖回退、泛化、Token 成本、安全和工程质量。[原文：发布协议，L300-L318](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L300-L318) [原文：五类长期指标，L333-L341](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L333-L341)

### 阶段 6：建设激活/遵循诊断和离线保鲜

分别评估提案质量、检索/路由激活、规则遵循和留出增益；随后加入定时或门控的离线合并、冲突处理、过期、归档、再验证和索引维护，避免能力库无限增长。[原文：分层评估，L320-L331](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L320-L331) [原文：睡眠学习与修剪，L376-L412](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L376-L412)

### 阶段 7：最后才考虑参数更新与更高层自优化

只有高维感知、风格或隐式策略难以被知识/语言规则/程序完整表达时，才把经验证轨迹转为 SFT、偏好或 RL 数据。搜索整个工作流、Harness 或提案优化器的成本和归因难度更高，应在局部机制成为瓶颈后再做，并继续把可信根放在搜索空间外。[原文：参数更新边界，L256-L262](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L256-L262) [原文：搜索尺度与可信根，L264-L282](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L264-L282)

## 明确反模式与风险

| 反模式 / 风险 | 为什么错误或危险 | 原文依据 |
|---|---|---|
| 把日志、长上下文或向量库存储等同于学习 | 检索一个案例不等于跨案例评价、对照、归纳和验证 | [L5-L11](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L5-L11) |
| 每次任务后根据用户反馈直接在线训练或改正式能力 | 生产反馈不干净，会固化偶然成功、错误归因、提示注入，导致遗忘、漂移或安全退化 | [L9-L11](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L9-L11) |
| 先总结、后验证 | 不知道任务和路径是否正确时，反思只是猜测；错误会被持久放大 | [L19-L27](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L19-L27) |
| 只看最终结果或用户满意度 | 删除失败测试也能“通过”；违规退款也能让用户满意，过程错误会被漏掉 | [L23-L31](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L23-L31) |
| 把轨迹评价压成单一总分 | 丢失问题类型与证据位置，后续无法判断该改知识、Prompt、程序还是参数 | [L45-L49](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L45-L49) |
| 同一个模型既当裁判又直接改规则 | 缺少独立校准和职责隔离，容易自证成功 | [L62-L64](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L62-L64) |
| 一条成功轨迹摘要直接入正式经验库 | 原始轨迹嘈杂，成功可能偶然；缺少失败/部分成功对照、支持门槛和新任务迁移验证 | [L107-L129](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L107-L129) |
| 每条抱怨都追加到 Prompt，或反复重写整份 Prompt/记忆 | 会产生作用域错误、规则冲突、Prompt 膨胀；整篇重写还会逐轮丢失少数关键细节 | [L166-L184](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L166-L184) [L264-L270](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L264-L270) |
| 把确定性流程或硬安全约束继续堆在 Prompt | 模型可能忽略；可确定执行的规则应进入可测试的程序/Harness | [L85-L105](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L85-L105) [L232-L249](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L232-L249) |
| 把动作执行成功当成任务成功 | 按钮都点过并不代表最终状态正确；缺少动作前/后、最终状态和独立回放检查会更快重复错误 | [L202-L219](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L202-L219) |
| 运行中的 Agent 直接覆盖自身或修改稳定版本 | 无隔离、审计、回归、灰度和回滚，无法安全归因 | [L222-L238](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L222-L238) |
| 修改者可以改验证器、测试或发布门槛 | Agent 可删除失败用例或降低阈值，把退化伪装成进步 | [L241-L249](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L241-L249) [L366-L374](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L366-L374) |
| 一开始就搜索整个 Harness/优化器 | 方案空间、评估成本和归因困难都更大；明确局部故障应优先局部补丁 | [L264-L282](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L264-L282) |
| 用平均分抵消安全或保留集失败 | 边界、保留和安全是独立 veto，任何一项失败都不能发布 | [L300-L318](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L300-L318) |
| 只用“进化后总分”判断系统 | 总分混淆了提案是否有效、产物是否被激活、激活后是否遵循等不同瓶颈 | [L320-L331](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L320-L331) |
| 把测试通过或流程完成等同于真实进步 | 开放任务可能稳定产出“像成果的东西”；软件测试通过也不代表长期可维护 | [L351-L364](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L351-L364) |
| 把网页、工具输出或 LLM 摘要直接写成长期指令 | 摘要不是净化；提示注入可能跨会话固化并影响所有后续任务 | [L366-L370](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L366-L370) |
| 让知识、Prompt、Skill、工具和参数只增不减 | 会出现冲突、重复、检索错误、上下文腐化和灾难性遗忘 | [L376-L412](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L376-L412) |

## 判断一个实现是否遵循本章方向的检查表

后续对任何实现做对照时，可以优先问：

1. 反思输入是否来自**已验证**的结果和过程，还是只来自模型自己的文本？
2. 评价是否结构化、带证据和置信度，且结果/过程是硬门？
3. 是否使用多条成功、失败和部分成功轨迹对照，而非单次总结？
4. 是否先做根因定位，再在知识、Prompt/Skill、程序/Harness、参数之间路由？
5. 修改是否是带来源的最小 diff，并声明预期修复与可能回退？
6. 是否有与正式能力隔离的候选区，以及边界集、保留集、安全集？
7. 安全检查是否各自拥有 veto，候选能否灰度、监控和回滚？
8. 提案生成者是否无法修改验证器、测试、发布门槛、审计和稳定备份？
9. 是否分别度量“更新有效”“正确激活”“正确遵循”“留出迁移”？
10. 是否有容量、冲突、时效、过期、归档和审计保留机制？

以上十项是本章方向的核心辨识标准，其总括见章节小结：[L440-L446](https://github.com/bojieli/ai-agent-book/blob/5de66dc8da3c2dc7db9923bed229f4b42bc58c1b/book/chapter8.md#L440-L446)。
