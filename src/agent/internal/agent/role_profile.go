package agent

import (
	"strings"

	"aiden-agent/internal/agent/context_manager"

	langtools "github.com/tmc/langchaingo/tools"
)

type RoleProfile struct {
	SystemPromptStable  string
	SystemPromptDynamic string
	Skills              ResolvedSkills
	Tools               []langtools.Tool
}

// SystemPrompt returns the combined system prompt for logging and legacy checks.
func (p RoleProfile) SystemPrompt() string {
	return context_manager.JoinPromptSections(p.SystemPromptSections())
}

// SystemPromptSections returns cacheable system prompt sections for ContextManager.
func (p RoleProfile) SystemPromptSections() []context_manager.PromptSection {
	sections := make([]context_manager.PromptSection, 0, 2)
	if text := strings.TrimSpace(p.SystemPromptStable); text != "" {
		sections = append(sections, context_manager.PromptSection{
			Text:           text,
			CacheEphemeral: true,
		})
	}
	if text := strings.TrimSpace(p.SystemPromptDynamic); text != "" {
		sections = append(sections, context_manager.PromptSection{Text: text})
	}
	return sections
}

func buildAgentProfile(cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool) RoleProfile {
	cfg.ForceSimpleLoop = true
	openAppAvailable := roleToolAvailable(availableTools, "open_app")
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
		rules = append(rules, "For launch-only app, URL, or dialer requests, open_app ok=true is enough unless the user asked to inspect or act inside the opened target.")
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
	staticParts = append(staticParts, "", "## Role rules")
	for _, rule := range roleRules {
		staticParts = append(staticParts, "- "+rule)
	}
	staticParts = append(staticParts, "", currentDateContext())

	dynamicParts := make([]string, 0, 1)
	if text := strings.TrimSpace(skills.CombinedInstructions()); text != "" {
		dynamicParts = append(dynamicParts, "## Active skills", text)
	}

	return RoleProfile{
		SystemPromptStable:  strings.Join(staticParts, "\n"),
		SystemPromptDynamic: strings.Join(dynamicParts, "\n"),
		Skills:              skills,
		Tools:               append([]langtools.Tool{}, tools...),
	}
}
