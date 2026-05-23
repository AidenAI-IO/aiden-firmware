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
