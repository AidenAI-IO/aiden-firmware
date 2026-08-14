# Claude Code 的持久 Memory 机制

> 核对日期：2026-08-14。本文把 Anthropic 官方文档、Anthropic 官方仓库 changelog 和第三方提取的 prompt 分开作为不同强度的证据。

## 一句话结论

Claude Code 公开实现的是两类互补机制：

1. `CLAUDE.md` / `.claude/rules/`：由人或团队维护、启动时或访问相关目录时加载的持久指令；
2. Auto memory：Claude 在正常工作过程中自行读写本地 Markdown 文件，新会话只自动加载其入口文件的一小段。

Anthropic 的官方文档和官方 changelog **没有公开描述**一个统一的 `Episode 结束 → 自动反思 → 候选 Memory → 多 Episode 合并 → 置信度晋升` 流水线。也没有公开 success/failed 门控、证据计数、pending/active 状态或两次独立 Episode 后晋升等机制。因此不能把 Claude Code 说成已经官方公开了我们讨论的 Episode-to-Memory consolidation；它对用户可见的模型更接近“Agent 在正常工作中自行维护一个可见、可编辑的项目笔记本”。

主要官方来源：

- [Manage Claude's memory](https://code.claude.com/docs/en/memory)
- [Common workflows: resume previous conversations](https://code.claude.com/docs/en/common-workflows#resume-previous-conversations)
- [CLI reference](https://code.claude.com/docs/en/cli-reference)
- [Claude Code official repository](https://github.com/anthropics/claude-code)
- [Claude Code official changelog](https://github.com/anthropics/claude-code/blob/1f6015b5d578adf79c8527443328a216d6b6a3f1/CHANGELOG.md)

附录中的 [Dream memory consolidation prompt](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md) 来自 **Piebald-AI 第三方仓库**，不属于上述官方来源。

## 1. `CLAUDE.md` 的层级

官方文档列出的持久说明大致分为以下作用域：[Memory types](https://code.claude.com/docs/en/memory#memory-types)

| 类型 | 典型位置 | 用途 |
|---|---|---|
| Managed policy | macOS `/Library/Application Support/ClaudeCode/CLAUDE.md`；Linux/WSL `/etc/claude-code/CLAUDE.md`；Windows `C:\Program Files\ClaudeCode\CLAUDE.md` | 组织统一政策，由管理员部署 |
| Project memory | `./CLAUDE.md` 或 `./.claude/CLAUDE.md` | 可提交到版本库的团队项目说明 |
| Project rules | `./.claude/rules/*.md` | 模块化、可按路径限定的项目规则 |
| User memory | `~/.claude/CLAUDE.md` | 用户跨项目偏好 |
| Project local memory | `./CLAUDE.local.md` | 不共享的本地项目说明 |
| Auto memory | `~/.claude/projects/<project>/memory/` | Claude 自行维护的项目知识 |

Claude Code 会沿目录层级发现适用的 `CLAUDE.md`。与当前工作目录及其父目录相关的说明会进入会话上下文；更深层子目录中的说明在 Claude 访问相应目录/文件时再加载，而不是无条件把整个仓库的说明全部放入启动上下文。[How Claude looks up memories](https://code.claude.com/docs/en/memory#how-claude-looks-up-memories)

`CLAUDE.md` 还支持使用 `@path` 导入其他文件；`.claude/rules/` 则适合把大型说明拆成主题文件，并可通过 frontmatter 的 `paths` 将规则限制到特定文件范围。[CLAUDE.md imports](https://code.claude.com/docs/en/memory#import-additional-files) [Modular rules](https://code.claude.com/docs/en/memory#organize-rules-with-clauderules)

这套机制的关键特征是：Memory 本质上是普通文件，内容显式、可审查、可版本控制；它不是隐藏的模型参数更新。

## 2. Auto memory 如何存储和加载

官方文档将 Auto memory 描述为 Claude 在工作中自行积累的项目知识，例如项目模式、调试经验、架构决策和用户偏好。[Auto memory](https://code.claude.com/docs/en/memory#auto-memory)

存储布局为：

```text
~/.claude/projects/<project>/memory/
├── MEMORY.md
└── other-topic-files.md
```

其中：

- `MEMORY.md` 是入口/索引；
- 每次新会话只自动载入 `MEMORY.md` 的前 200 行，且当前版本同时有 25KB 的读取上限；这个 25KB 限制在官方 changelog 中被明确记录。[Official changelog, L3057](https://github.com/anthropics/claude-code/blob/1f6015b5d578adf79c8527443328a216d6b6a3f1/CHANGELOG.md#L3057)
- 其他主题文件和超过入口加载范围的内容不会全部预注入，Claude 可以在需要时通过文件工具读取；
- Claude 使用普通文件读写工具维护这些文件，因此用户可以查看、编辑或删除其内容；
- 可以通过 `/memory` 查看和管理相关 Memory 文件。

官方 changelog 还补充了几个公开的工程细节：

- `autoMemoryDirectory` 可改变 Auto memory 的存储目录。[Official changelog, L3282](https://github.com/anthropics/claude-code/blob/1f6015b5d578adf79c8527443328a216d6b6a3f1/CHANGELOG.md#L3282)
- 同一 Git 仓库的不同 worktree 共享 project config 和 Auto memory。[Official changelog, L3565](https://github.com/anthropics/claude-code/blob/1f6015b5d578adf79c8527443328a216d6b6a3f1/CHANGELOG.md#L3565)
- Memory 文件有 last-modified 时间戳，用于帮助 Claude 判断内容的新旧；这是时效性信号，不等于 revision history 或冲突版本管理。[Official changelog, L3262](https://github.com/anthropics/claude-code/blob/1f6015b5d578adf79c8527443328a216d6b6a3f1/CHANGELOG.md#L3262)
- 当 `MEMORY.md` 接近限制时，Agent 会被提醒压缩索引；超过读取上限的写入现在会明确报错。[Official changelog, L1018](https://github.com/anthropics/claude-code/blob/1f6015b5d578adf79c8527443328a216d6b6a3f1/CHANGELOG.md#L1018) [Official changelog, L577](https://github.com/anthropics/claude-code/blob/1f6015b5d578adf79c8527443328a216d6b6a3f1/CHANGELOG.md#L577)
- `--bare -p` 会完全禁用 Auto memory，说明 Auto memory 是可以从精简执行模式中移除的独立产品能力。[Official changelog, L3074](https://github.com/anthropics/claude-code/blob/1f6015b5d578adf79c8527443328a216d6b6a3f1/CHANGELOG.md#L3074)

这是一种控制启动上下文成本的两级结构：短入口进入每次会话，详细内容按需读取。它没有要求新增一个同步 verifier，也没有要求每次启动时检索整个历史会话。

## 3. Auto memory 在什么时机写入

官方描述是 Claude **在工作过程中**决定什么值得记住并更新 Auto memory。官方 changelog 也只进一步确认“Claude 会自动把有用上下文保存到 Auto memory”。[Auto memory](https://code.claude.com/docs/en/memory#auto-memory) [Official changelog, L3600](https://github.com/anthropics/claude-code/blob/1f6015b5d578adf79c8527443328a216d6b6a3f1/CHANGELOG.md#L3600)

公开资料没有把写入绑定为一个固定的“任务结束 hook”，也没有承诺每个会话或每个任务结束时都运行一次独立反思。

据此可以确定：

- 写 Memory 属于正常 Agent 工作中的自主文件操作；
- 它可能在会话尚未结束时发生；
- 是否记录、记录什么以及何时更新，由 Claude 基于当前工作判断；
- Memory 一经写入，本项目后续新会话可通过 `MEMORY.md` 启动加载或按需读取受益。

但仅凭官方公开资料，不能进一步断言它内部存在后台 Worker、统一 Episode 扫描、固定 idle 时间、每 Episode 一次模型调用或离线批量 consolidation。

## 4. 官方到底公开了哪些生命周期机制

| 问题 | 官方公开结论 | 证据边界 |
|---|---|---|
| 会不会自动保存 Memory | **会** | 官方文档和 changelog 公开说明 Claude 会在工作中保存有用上下文；未公开具体选择算法 |
| 是否有后台 Dream 调度 | **未公开** | 没有官方文档说明 idle timer、独立 Worker、调度频率、取消或重试语义 |
| 是否按 Episode success/failed 门控 | **未公开** | 官方只用“useful context”和“Claude's judgment”描述保存决策，没有 Episode outcome 协议 |
| 是否有 candidate/pending/active 状态 | **未公开** | 用户可见层是 Markdown 文件，官方未公开状态机或 promotion API |
| 是否要多个 Episode 才晋升 | **未公开** | 没有“两次独立证据”、support count 或 confidence threshold 的官方说明 |
| 是否有冲突版本、supersede/revision history | **未公开** | 官方只公开了可编辑文件和 last-modified 时间戳，不足以推导出版本图、回滚或冲突解析算法 |

这里的“未公开”是一个证据结论：官方文档和官方仓库中没有足以支持这些说法的公开设计。它不等于断言 Claude Code 的未公开实现内部绝对不存在任何相关启发式逻辑。

## 5. 附录 Dream prompt 的来源与可用范围

附录链接指向 Piebald-AI 的 `claude-code-system-prompts` 仓库，不是 Anthropic 官方仓库。该仓库 README 自述这些文本是从 Claude Code 编译产物中提取的；Dream prompt 文件的 metadata 标注了 Claude Code `2.1.224`。但“如何提取”和“文本是否对应完整线上行为”仍是第三方主张。[Piebald README: Extraction](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/README.md#extraction) [Dream prompt metadata, L1-L20](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L1-L20)

这份 prompt 文本本身可以支持的结论只是：

- 它要求一次反思式 pass 先了解已有 topic files，再查看近期 Session logs，必要时才窄范围搜 transcript。[Dream prompt, L35-L51](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L35-L51)
- 它倾向把新信息 merge 到已有主题文件，修正 contradicted facts，并移除 stale/superseded 内容，而不是建立显式的 `reinforce/refine/supersede/conflict` 状态机。[Dream prompt, L53-L69](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L53-L69)
- 它要求保持短索引，详细内容放在 topic files。[Dream prompt, L62-L69](https://github.com/Piebald-AI/claude-code-system-prompts/blob/1079f627e155563da543781cd80eae0fe665bbd7/system-prompts/agent-prompt-dream-memory-consolidation.md#L62-L69)

它**不能单独证明** Claude Code 何时触发这个 pass、是否每个 Session 都运行、是否使用 idle/后台 Worker、前台到来时是否取消，也不能证明 success/failed 门控、pending/active、多 Episode 晋升或冲突版本管理。所以它可以作为 Aiden 附录的操作范式参考，不能作为“Claude Code 官方已公开这套后台架构”的证据。

## 6. Session resume 与 Memory 是不同机制

Claude Code 还会保存会话历史，并提供以下恢复方式：[Resume previous conversations](https://code.claude.com/docs/en/common-workflows#resume-previous-conversations) [CLI reference](https://code.claude.com/docs/en/cli-reference)

```text
claude --continue          恢复当前项目最近一次会话
claude --resume            选择一个历史会话恢复
claude --resume <id>       按 session ID 恢复
/resume                    在交互界面选择会话
```

二者语义不同：

| 机制 | 保存内容 | 后续如何使用 |
|---|---|---|
| Session history / resume | 某一次会话的完整对话和工作上下文 | 恢复并继续同一个历史会话 |
| `CLAUDE.md` / Auto memory | 跨会话保留的精简规则、偏好和项目知识 | 启动新会话时加载，或工作中按需读取 |

因此，能够 `resume` 不代表历史 Episode 已被提炼为 Memory；它只是继续原会话。反过来，新开会话不需要恢复旧 transcript，也能从 `CLAUDE.md` 和 Auto memory 获得持久知识。

## 7. 官方没有公开的机制

截至上述官方文档，未找到 Anthropic 对以下机制的公开说明：

- 每个结束 Episode 自动进入统一反思队列；
- 根据 Episode success/failed 决定是否提炼；
- 对轨迹做统一 outcome verification 后才允许写 Memory；
- 将单 Episode 产物先保存为 candidate/pending；
- 通过多条独立 Episode 支持后将 Memory 晋升为 active；
- 为 Memory 保存 confidence、support/contradiction count 或生命周期状态；
- 判断 Memory 在后续任务中是否 matched/applied/effective；
- 自动合并、降级、冲突隔离或淘汰旧 Memory。

这里的准确表述应是“官方文档没有公开这些机制”，而不是断言 Claude Code 内部绝对不存在任何未公开启发式逻辑。

## 8. 与第一版 `Episode → Memory` 方案的启示

如果参考 Claude Code，而不是照搬我们之前设计的完整学习闭环，第一版可以更简单：

```text
Episode 完成
→ 后台判断是否存在可复用经验
→ 直接创建或更新一个短 Markdown Memory
→ 新 Episode 启动时加载短入口，详情按需读取
```

最值得借鉴的不是 promotion 机制，而是这三点：

1. **持久知识是透明文件。** 用户可以直接审查和纠正，避免隐藏状态。
2. **入口短、详情按需。** 不把全部 Memory 注入每次 Agent Loop，保护上下文大小和 latency。
3. **Memory 与 session history 分离。** Episode 是原始历史，Memory 是少量跨会话知识；恢复历史不能代替提炼，提炼也不必复制完整历史。

与 Claude Code 不同，我们若要保证 Memory 质量，可以额外保留最小的来源字段，如 `source_episode_id` 和 `evidence_refs`。但 `pending/active`、多 Episode 晋升、置信度和效果归因都不是复刻 Claude Code 所必需的第一版能力，也不是 Anthropic 官方已公开的设计。

### 对 `update` 逻辑的直接启示

如果目标是“先做得像 Claude Code 的公开机制”，V1 不需要为 `update` 建立 `reinforce / refine / supersede / conflict` 四分类。公开官方机制只需要支持“找到相关 Markdown 文件并编辑”；第三方 Dream prompt 也更像“优先合并到已有 topic file，删掉被推翻或过时的句子”，而不是一个多状态 Memory lifecycle。

因此更贴近 Claude Code 、也更简单的 V1 可以是：

```text
ignore
create(topic, content)
update(topic, merged_content)
```

如果新旧事实冲突且无法安全合并，Aiden 可以采取自己的保守规则，例如不更改旧正文、记录一条待复核反证。这是 Aiden 的额外质量约束，不应宣称为 Claude Code 的官方公开实现。
