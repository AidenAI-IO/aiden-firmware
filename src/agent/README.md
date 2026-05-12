# LangChainGo Agent Skeleton

一个用 `github.com/tmc/langchaingo` 搭出来的简单但结构完整的 Go Agent 框架，支持 [Agent Skills](https://agentskills.io) 标准，覆盖：

- 工具的注册与调用
- Agent Skills 的动态加载与激活
- 子 Agent 委派
- Memory 管理
- 模型的配置与切换

## 目录

- `internal/agent`
  - `config.go`: JSON 配置结构与校验
  - `models.go`: 模型工厂，支持 `openai`、`ollama`、`fake`
  - `tools.go`: 工具注册表、内置工具、子 Agent 委派工具
  - `skills.go`: Skills 管理器，支持按需激活
  - `skill_loader.go`: Agent Skills 目录扫描与加载
  - `memory.go`: 基于 `langchaingo/memory` 的 Memory 管理器
  - `prompt.go`: ReAct Prompt 构造，注入 `history`
  - `runtime.go`: 运行时编排，基于 `agents.NewOneShotAgent` + `agents.NewExecutor`
- `cmd/demo`: 示例入口
- `configs/agent.example.json`: 示例配置
- `configs/skills/`: Agent Skills 目录示例

## 关键设计

### 1. Agent Skills 支持

本框架完整支持 [Agent Skills](https://agentskills.io) 标准，实现了 **Progressive Disclosure** 机制：

**Discovery（发现阶段）**：启动时扫描 `skills_dirs` 配置的目录，加载所有 `SKILL.md` 文件的 frontmatter（name + description），构建技能索引。

**Activation（激活阶段）**：运行时通过两种方式激活技能：
- Agent 的 `default_skills` 在启动时自动激活
- LLM 可以通过 `activate_skill` 工具按需激活其他技能

**Execution（执行阶段）**：激活后的技能指令会注入到 Prompt 中，影响 Agent 行为。

#### Skill 文件格式

每个 skill 是一个包含 `SKILL.md` 文件的目录：

```markdown
---
name: planner
description: Task decomposition and delegation.
metadata:
  preferred_model: primary
  allowed_tools: [calculator, policy, delegate_researcher]
  allowed_children: [researcher]
---

Break the problem into small steps. Delegate focused research or drafting work to child agents when that reduces complexity.

When you encounter a task that requires:
- Gathering information from multiple sources
- Performing focused analysis on a specific subtopic
- Researching a narrow question

Consider delegating to the researcher agent using the `delegate_researcher` tool.

Always synthesize the results from child agents into a coherent final answer.
```

**Frontmatter 字段**：
- `name`: 技能名称（必需）
- `description`: 简短描述，用于发现阶段（必需）
- `metadata.preferred_model`: 偏好的模型别名
- `metadata.allowed_tools`: 允许使用的工具列表
- `metadata.allowed_children`: 允许委派的子 Agent 列表

**Body**：完整的技能指令，激活后注入到 Prompt 中。

### 2. 工具注册与调用

工具通过 `ToolRegistry` 统一注册：

```go
registry := agent.NewDefaultToolRegistry()
registry.MustRegister("my_tool", func(name string, cfg agent.ToolConfig) (tools.Tool, error) {
    return &MyTool{name: name}, nil
})
```

内置工具：
- `calculator`: 数学计算
- `echo`: 回显输入，用于调试
- `static`: 返回固定响应，用于策略或模拟
- `activate_skill`: 运行时激活技能（自动注入）

### 3. Skills 配置与管理

Skills 不再在 JSON 中配置，而是从文件系统加载：

```json
{
  "skills_dirs": [
    "/absolute/path/to/skills"
  ]
}
```

Agent 通过 `default_skills` 指定启动时激活的技能：

```json
{
  "agents": {
    "coordinator": {
      "default_skills": ["planner"]
    }
  }
}
```

Skill 支持三类职责：
- 追加行为说明 `instructions`
- 约束允许使用的 `allowed_tools`
- 指定偏好模型 `preferred_model`

多个 Skill 会被合并，最终影响 Prompt、工具可见性和模型选择。

### 4. 子 Agent

父 Agent 的 `children` 会自动暴露为工具，例如子 Agent `researcher` 会对应工具 `delegate_researcher`。

父 Agent 调用这个工具时，实际上会递归调用：

```go
runtime.Run(ctx, agent.RunRequest{
    AgentName: "researcher",
    Input:     subTask,
})
```

这样子 Agent 有自己独立的：
- Prompt
- Skill
- Model
- Memory

### 5. Memory 管理

`MemoryManager` 为每个 agent 保留独立 Memory 句柄，当前支持：

- `buffer`: 完整对话缓冲
- `window`: 最近 N 轮窗口

Memory 不只是保存，还会注入 Prompt 的 `history` 变量，让模型真正看到上下文。

### 6. 模型配置与切换

模型由别名统一管理，例如：

```json
"models": {
  "primary": { "provider": "openai", "model": "gpt-4o-mini" },
  "local": { "provider": "ollama", "model": "qwen3:4b" }
}
```

切换方式有三层优先级：

1. `RunRequest.ModelAlias`
2. `Skill.preferred_model`
3. `Agent.default_model`

## 运行

### 1. 配置环境变量

如果使用 OpenAI：

```bash
export OPENAI_API_KEY=your-key
```

如果使用 Ollama，请先启动本地服务并准备模型。

### 2. 创建 Skills 目录

```bash
mkdir -p configs/skills/my-skill
cat > configs/skills/my-skill/SKILL.md <<'EOF'
---
name: my-skill
description: My custom skill
metadata:
  allowed_tools: [calculator]
---

Custom instructions for this skill.
EOF
```

### 3. 配置 Agent

编辑 `configs/agent.example.json`，设置 `skills_dirs` 为绝对路径：

```json
{
  "skills_dirs": [
    "/absolute/path/to/configs/skills"
  ]
}
```

### 4. 运行 Demo

```bash
go run ./cmd/demo -config configs/agent.example.json -input "帮我规划一个包含计算和研究委派的回答"
```

查看 Memory：

```bash
go run ./cmd/demo -config configs/agent.example.json -show-memory -input "继续上一个话题"
```

覆盖模型：

```bash
go run ./cmd/demo -config configs/agent.example.json -model local -input "用本地模型回答这个问题"
```

## Agent Skills 工作流程

1. **启动时**：扫描 `skills_dirs`，加载所有 `SKILL.md` 的 name + description
2. **运行时**：
   - 自动激活 `default_skills` 中的技能
   - LLM 可以通过 `activate_skill` 工具激活其他技能
   - 激活的技能指令注入到 Prompt 中
3. **工具解析**：
   - 如果技能定义了 `allowed_tools`，只有这些工具可用
   - 否则，所有配置的工具都可用
   - `activate_skill` 工具始终可用（如果有技能可加载）

## 说明

这个实现选择 `agents.NewOneShotAgent` 而不是只绑定 OpenAI Functions Agent，原因是这样可以同时兼容：

- OpenAI
- OpenAI-compatible endpoint
- Ollama 这类不一定支持函数调用协议的模型

如果你后续只跑支持 function-calling 的模型，可以把 `runtime.go` 里的 `OneShotAgent` 替换为 `OpenAIFunctionsAgent`，工具注册层和 Skill/Memory/子 Agent 框架层都可以保持不变。

## Agent Skills 生态

本框架遵循 [Agent Skills](https://agentskills.io) 开放标准，技能可以在不同的 Agent 系统间复用。访问 [agentskills.io](https://agentskills.io) 了解更多信息和社区技能库。
