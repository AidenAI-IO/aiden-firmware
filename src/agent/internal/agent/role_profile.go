package agent

import (
	"strings"

	langtools "github.com/tmc/langchaingo/tools"
)

type RoleProfile struct {
	SystemPrompt string
	Skills       ResolvedSkills
	Tools        []langtools.Tool
}

func buildAgentProfile(cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool) RoleProfile {
	cfg.ForceSimpleLoop = true
	openAppAvailable := roleToolAvailable(availableTools, toolBridgeOpenApp)
	return buildRoleProfile(
		cfg,
		skills,
		agentToolsForConfig(availableTools),
		agentRoleRules(openAppAvailable),
	)
}

func roleToolAvailable(tools []langtools.Tool, name string) bool {
	for _, tool := range tools {
		if tool != nil && tool.Name() == name {
			return true
		}
	}
	return false
}

func (cfg AgentConfig) VoiceToolCallSpeechOrDefault() bool {
	if cfg.VoiceToolCallSpeech != nil {
		return *cfg.VoiceToolCallSpeech
	}
	return true
}

func agentToolsForConfig(tools []langtools.Tool) []langtools.Tool {
	return tools
}

func agentRoleRules(openAppAvailable bool) []string {
	rules := []string{
		"Prefer direct tools that cover the requested operation before UI workarounds.",
		"For launch-only requests to open an app, URL, or dialer screen, direct tool success is enough unless the user asked to inspect or act inside the opened target.",
		"For semantic platform actions, use quick_action when a matching action may exist; pass observed_state.platform and the concrete action id when possible, and switch to a low-level fallback after failure/no effect.",
		"Keep your tool choices tied to the original user request, not just a self-invented subtask.",
		"If the current screenshot clearly identifies the app/page or device platform, use that observed app, page, platform (ios/android/mac), visible text, and dialogs when choosing tools.",
		"Call request_human_handoff when the task requires credentials, login-method selection, verification, system/app redirect confirmation, permission dialog confirmation, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Ask the user to complete it on the device; do not ask them to send credentials or private verification details in chat.",
	}
	if openAppAvailable {
		rules = append(rules, "For launch-only app, URL, or dialer requests, bridge_open_app ok=true is enough unless the user asked to inspect or act inside the opened target.")
	}
	return rules
}

func buildRoleProfile(
	cfg AgentConfig,
	skills ResolvedSkills,
	tools []langtools.Tool,
	roleRules []string,
) RoleProfile {
	staticParts := []string{
		"You are the Aiden agent.",
		currentDateContext(),
		"",
		"## Base instruction",
		combinedAgentInstruction(cfg),
		"",
		"## Environment",
		agentEnvironmentGuidance(),
		"",
		"## Default behavior",
		defaultAgentBehavior(),
		"",
		"## Available skills",
		skills.CatalogSummary(),
	}
	if text := strings.TrimSpace(skills.CombinedInstructions()); text != "" {
		staticParts = append(staticParts,
			"",
			"## Active skills",
			text,
		)
	}
	staticParts = append(staticParts, "", "## Role rules")
	for _, rule := range roleRules {
		staticParts = append(staticParts, "- "+rule)
	}

	staticPrompt := strings.Join(staticParts, "\n")

	return RoleProfile{
		SystemPrompt: staticPrompt,
		Skills:       skills,
		Tools:        append([]langtools.Tool{}, tools...),
	}
}
