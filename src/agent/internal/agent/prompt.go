package agent

import (
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/prompts"
	langtools "github.com/tmc/langchaingo/tools"
)

func buildPrompt(agentName string, cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool) prompts.PromptTemplate {
	template := strings.Join([]string{
		"You are agent {{.agent_name}}.",
		"Base instruction:",
		"{{.agent_instruction}}",
		"",
		"Default behavior:",
		"{{.default_behavior}}",
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
		"Action Input: a plain string input for the selected tool",
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
			"agent_instruction":  combinedAgentInstruction(cfg),
			"default_behavior":   defaultAgentBehavior(),
			"skill_instructions": skills.CombinedInstructions(),
			"tool_names":         joinToolNames(availableTools),
			"tool_descriptions":  describeTools(availableTools),
		},
	}
}

func buildFunctionAgentSystemMessage(cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool) string {
	parts := []string{
		"You are agent.",
		"Base instruction:",
		combinedAgentInstruction(cfg),
		"",
		"Default behavior:",
		defaultAgentBehavior(),
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
		"- 默认用简体中文回答；用户明确要求其他语言时再切换。",
		"- 最终回复会用于 TTS 播放，所以要口语化、简短、自然。除非用户要求，别用 Markdown 表格或长列表。",
		"- 需要读取或改变手机、外部设备或服务状态时，必须使用相关工具；没有工具结果确认前，不要声称状态改变已经成功。",
		"- 可以连续组合多个工具完成任务。用户要求操作手机或拨打电话时，把它当作手机 UI 自动化任务：先用截图确认状态，再组合 touch_gesture、mouse_click、keyboard_text、keyboard_tap 等工具导航、输入号码或联系人并点击拨号。不要因为没有单独的拨打电话工具就说做不到。",
		"- 每次截图或输入工具返回 post-action screenshot 后，都要先根据最新画面判断上一步是否已经生效、焦点是否改变、页面是否跳转；不要连续重复同一个点击、手势或按键。只有截图明确显示上一次没有生效，或用户明确要求双击/重复操作时，才重复同一动作。",
		"- 在手机上打开 App、查找联系人、设置项、商品或页面内容时，优先使用系统搜索、App 内搜索或页面上的搜索框；不要先靠连续滑动、翻页来碰运气。只有截图确认没有可用搜索入口，或用户明确要求浏览列表时，才滚动查找。",
		"- keyboard_text 是模拟美式键盘按键，必须传 JSON，例如 {\"text\":\"App Store\"}；不要传裸字符串。它只能输入 ASCII 可键入字符，不能直接输入中文、emoji 或其他非键盘字符。需要中文时，改用拼音/英文关键词输入并从输入法候选或搜索结果中选择；无法确定候选时先询问用户。",
		"- 点击要以最新截图为准，选择可见目标的中心点，并优先使用 coord_space:\"normalized\" 的 0..1 坐标；手机投屏/截图可能被缩放，pixel 坐标容易和实际触控坐标偏移。除非用户明确要求或坐标系已经校准，不要使用 coord_space:\"pixel\"。坐标不确定时先截图确认，不要用大概位置连续试点。",
		"- 用户明确要求“拨打”才真正点下呼叫；如果只要求准备拨号，或号码/联系人信息不够，就先停在确认步骤或追问。",
		"- 调用工具时，description 要用用户语言写一句简短口语化的话，说明马上要做什么；语音客户端可能会在工具执行时朗读。",
		"- 手机边缘手势必须从物理边缘附近开始，参数不要保守。返回优先用 touch_gesture 的 type \"back\"；回主屏优先用 type \"home\"。如果手写 swipe，左边缘返回用 start.x=0.001 左右，底边回主页用 start.y=0.999 左右。",
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
