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
		"Active skills:",
		"{{.skill_instructions}}",
		"",
		"Conversation history:",
		"{{.history}}",
		"",
		"You can use the following tools:",
		"{{.tool_descriptions}}",
		"",
		"For any request that reads or changes external device or service state, you must use the relevant tool.",
		"Never claim a state change succeeded until you have a tool Observation confirming it.",
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
			"agent_instruction":  cfg.Instruction,
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
		cfg.Instruction,
		"",
		"Active skills:",
		skills.CombinedInstructions(),
		"",
		"You can use the following tools:",
		describeTools(availableTools),
		"",
		"For any request that reads or changes external device or service state, you must use the relevant tool.",
		"Never claim a state change succeeded until you have a tool result confirming it.",
		"If no tool is needed, answer directly.",
	}
	return strings.Join(parts, "\n")
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
