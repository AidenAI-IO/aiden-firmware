package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/tmc/langchaingo/prompts"
	langtools "github.com/tmc/langchaingo/tools"
)

var promptNow = time.Now

func buildPrompt(agentName string, cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool) prompts.PromptTemplate {
	template := strings.Join([]string{
		"You are agent {{.agent_name}}.",
		"{{.current_date}}",
		"Base instruction:",
		"{{.agent_instruction}}",
		"",
		"Default behavior:",
		"{{.default_behavior}}",
		"",
		"{{.skill_behavior}}",
		"",
		"Available skills:",
		"{{.skill_catalog}}",
		"",
		"Active skills:",
		"{{.skill_instructions}}",
		"",
		"Conversation history:",
		"{{.history}}",
		"",
		"You can use the following tools:",
		"{{.tool_descriptions}}",
		"",
		"Use the following format:",
		"Question: the user's current request",
		"Thought: reason about the next step",
		"Action: one of [ {{.tool_names}} ]",
		"Action Input: a valid JSON string for the selected tool, unless that tool explicitly accepts bare text",
		"Observation: the tool result",
		"... (the Thought/Action/Action Input/Observation loop can repeat)",
		"Thought: I now know the final answer",
		"Final Answer: the final answer to the original input",
		"",
		"If no tool is needed, go directly to Final Answer.",
		"",
		"Begin!",
		"",
		"Question: {{.input}}",
		"{{.agent_scratchpad}}",
	}, "\n")

	return prompts.PromptTemplate{
		Template:       template,
		TemplateFormat: prompts.TemplateFormatGoTemplate,
		InputVariables: []string{"input", "history", "agent_scratchpad"},
		PartialVariables: map[string]any{
			"agent_name":         agentName,
			"current_date":       currentDateContext(),
			"agent_instruction":  combinedAgentInstruction(cfg),
			"default_behavior":   defaultAgentBehavior(),
			"skill_behavior":     skillBehavior(),
			"skill_catalog":      skills.CatalogSummary(),
			"skill_instructions": skills.CombinedInstructions(),
			"tool_names":         joinToolNames(availableTools),
			"tool_descriptions":  describeTools(availableTools),
		},
	}
}

func buildFunctionAgentSystemMessage(cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool) string {
	parts := []string{
		"You are agent.",
		currentDateContext(),
		"Base instruction:",
		combinedAgentInstruction(cfg),
		"",
		"Default behavior:",
		defaultAgentBehavior(),
		"",
		skillBehavior(),
		"",
		"Available skills:",
		skills.CatalogSummary(),
		"",
		"Active skills:",
		skills.CombinedInstructions(),
		"",
		"You can use the following tools:",
		describeTools(availableTools),
		"",
		"If no tool is needed, answer directly.",
	}
	return strings.Join(parts, "\n")
}

func currentDateContext() string {
	return formatChineseDate(promptNow())
}

func formatChineseDate(t time.Time) string {
	weekdays := []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	return "今天的日期是: " + t.Format("2006年01月02日") + " " + weekdays[t.Weekday()]
}

func combinedAgentInstruction(cfg AgentConfig) string {
	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(cfg.Instruction); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(cfg.AdditionalPrompt); text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, "\n\n")
}

func defaultAgentBehavior() string {
	return strings.Join([]string{
		"## 环境",
		"- 你运行在 Aiden 硬件控制器上，运行时 OS 是 Linux；不一定是截图中显示的设备。",
		"- shell、本地文件、进程和系统命令只作用于 Aiden 硬件控制器，不会操作截图中的目标 UI。shell 工具只在 Aiden 硬件控制器上执行；只在控制器诊断，或用户明确要求在 Aiden 控制器上执行命令时使用 shell。",
		"- 目标设备和目标 OS 根据截图、连接元数据、谨慎行为探测或用户输入推断。",
		"- Aiden 主要用于控制连接的手机或移动 OS；这只是弱先验，不是已检测事实。当截图、工具结果或失败动作与该假设冲突时，必须修正判断。",
		"- 不要因为运行时是 Linux 就推断目标设备也是 Linux；操作目标 UI 时，不要用本地系统命令代替目标控制工具。",
		"",
		"## 默认行为",
		"- 默认用简体中文回答；用户明确使用其他语言时跟随用户语言。最终回复要简短、自然、适合 TTS 播放；除非用户要求，避免 Markdown 表格或长列表。",
		"- 当回答依赖已保存的长期偏好、规则、流程或事实时，先调用 recall_memory；不要直接凭常识回答。普通问题不要为了使用工具而使用工具。",
		"- 当用户要求查看或操作设备、App、设置、联系人、消息、网站、电视 UI 或其他外部状态时，必须使用工具；没有工具结果或截图确认前，不要声称状态已经改变。",
		"- 操作可见目标 UI 时，先在 Available skills 中匹配 device-operator；如果相关且未激活，先用 skill_read 加载再行动。详细 UI playbook 放在 skills 中，不要复制进默认 prompt。",
		"- 每次截图或 post-action screenshot 后，先判断上一步是否真的生效，再执行下一步。除非最新观察显示有必要，不要重复同一个点击、手势、按键或等待。",
		"- 打开 App、查找联系人、设置项、文件、商品、消息或页面内容时，优先使用搜索，而不是盲目滚动。只有没有可见搜索路径，或用户明确要求浏览时才滚动。",
		"- keyboard_text 必须传 JSON，例如 {\"text\":\"App Store\"}。它只支持 US-keyboard ASCII。需要输入非 ASCII 文本时，优先用 ASCII 搜索词或转写、从屏幕候选中选择，或先询问用户。",
		"- 点击和点按要基于最新截图选择可见目标中心点。优先使用 coord_space:\"normalized\" 的 0..1 坐标。仅在已校准时使用 coord_space:\"pixel\"；坐标不确定时先截图，不要用大概位置试点。",
		"- 手机边缘手势优先使用 touch_gesture 的 type \"back\" 表示返回，type \"home\" 表示回主页。必须手写 swipe 时，从物理边缘附近开始。",
		"- 发送消息/邮件、下单、支付、删除数据、修改隐私/安全设置、授权权限或开始通话等不可逆或敏感动作前，先请求确认，除非用户明确要求执行这个最终动作。",
		"- 工具调用的 description 要用用户语言写一句简短自然的话，说明马上要做什么；语音客户端可能会在工具执行时朗读。",
	}, "\n")
}

func skillBehavior() string {
	return strings.Join([]string{
		"## Skills",
		"Skills 是可复用的操作流程，不是 memory。适合 App 操作、排障、设备流程、表单/授权/支付、重复任务和已验证的工具使用模式。",
		"",
		"### 可用信息",
		"- Available skills 列出当前可用 skill 的名称和描述；Active skills 列出本轮已激活并注入的完整说明。",
		"- skill_list 用于浏览或搜索 skills，skill_read 用于加载相关 skill 的 SKILL.md 或链接文件，skill_manage 用于创建、编辑、归档或维护 skill，skill_mark_used 用于记录实际使用。",
		"",
		"### 使用规则",
		"- 行动前先查看 Available skills；对可复用流程、App 操作、排障、设备设置、表单提交、支付/授权或已知重复任务，优先匹配 skill。",
		"- 如果 Available skills 不够判断，再用 skill_list 搜索；找到相关 skill 后，先 skill_read，再执行，除非该 skill 已在 Active skills 中。",
		"- 不要读取所有 skill。只读取和当前任务相关的 skill；如果相关 skill 已在 Active skills 中，优先按已激活说明执行，只有需要链接文件或完整 SKILL.md 细节时才再次 skill_read。",
		"- 已加载 skill 是本次任务 SOP；除非它和用户指令、安全规则、当前屏幕状态或工具结果冲突。skill 过时或部分错误时，基于当前证据调整本次执行。",
		"- 实际按某个 skill 执行后，如果有 skill_mark_used 工具，就用该 skill 名称调用它。",
		"",
		"### 维护规则",
		"- 只有可复用流程才写入或更新 skill；不要保存一次性进度、临时状态、秘密、原始日志或个人事实。",
		"- 修改已有 skill 前必须先 skill_read；小改优先 skill_manage action=patch，整篇重写才用 action=edit。",
		"- skill_manage 只能维护 configDir/skills 下的 skills，以及 references/、templates/、scripts/、assets/ 下的 supporting files。",
		"- 不要直接修改 bundled source 或 configDir/skill-state 文件。",
	}, "\n")
}

func joinToolNames(availableTools []langtools.Tool) string {
	if len(availableTools) == 0 {
		return "none"
	}
	names := make([]string, 0, len(availableTools))
	for _, tool := range availableTools {
		names = append(names, tool.Name())
	}
	return strings.Join(names, ", ")
}

func describeTools(availableTools []langtools.Tool) string {
	if len(availableTools) == 0 {
		return "- none: answer directly without using a tool"
	}

	var builder strings.Builder
	for _, tool := range availableTools {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", tool.Name(), tool.Description()))
	}
	return strings.TrimRight(builder.String(), "\n")
}
