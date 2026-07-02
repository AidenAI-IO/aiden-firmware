package agent

import (
	"strings"

	langtools "github.com/tmc/langchaingo/tools"
)

type RoleName string

const RoleAgent RoleName = "agent"

type RoleCapabilities struct {
	CanUseTools bool
}

type RoleProfile struct {
	Name                      RoleName
	SystemPrompt              string
	SystemPromptCachePrefix   string
	SystemPromptDynamicSuffix string
	Skills                    ResolvedSkills
	Tools                     []langtools.Tool
	Capabilities              RoleCapabilities
}

func buildAgentProfile(cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool, memoryContext interface{}) RoleProfile {
	cfg.ForceSimpleLoop = true
	roleMemory := normalizeMemoryContext(memoryContext)
	openAppAvailable := roleToolAvailable(availableTools, "open_app")
	return buildRoleProfile(
		RoleAgent,
		cfg,
		skills,
		agentToolsForConfig(availableTools),
		roleMemory.RenderForRole(RoleAgent),
		RoleCapabilities{CanUseTools: true},
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
	return toolsForRole(RoleAgent, tools)
}

func toolsForRole(role RoleName, tools []langtools.Tool) []langtools.Tool {
	filtered := make([]langtools.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if !NewToolSpec(tool).AgentExposedToRole(role) {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func toolSpecsForRole(role RoleName, tools []langtools.Tool) *ToolSpecs {
	return NewToolSpecs(toolsForRole(role, tools))
}

func agentRoleRules(openAppAvailable bool) []string {
	structuredFinalRule := "Voice interaction is the core use case: keep user-facing output brief, natural, and easy to speak. When returning a final answer directly to the user, return plain text, not JSON."
	rules := []string{
		"Use the single-agent loop for every request: call available tools directly and return a final answer when the request is satisfied.",
		structuredFinalRule,
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
	name RoleName,
	cfg AgentConfig,
	skills ResolvedSkills,
	tools []langtools.Tool,
	memoryContext string,
	capabilities RoleCapabilities,
	roleRules []string,
) RoleProfile {
	staticParts := []string{
		"You are the single Aiden agent.",
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
	var dynamicParts []string
	if text := strings.TrimSpace(cfg.RuntimeContext); text != "" {
		dynamicParts = append(dynamicParts,
			"## Runtime context",
			text,
		)
	}
	if text := strings.TrimSpace(memoryContext); text != "" {
		if len(dynamicParts) > 0 {
			dynamicParts = append(dynamicParts, "")
		}
		dynamicParts = append(dynamicParts, text)
	}
	staticPrompt := strings.Join(staticParts, "\n")
	dynamicPrompt := strings.TrimSpace(strings.Join(dynamicParts, "\n"))
	systemPrompt := joinSystemPromptParts(staticPrompt, dynamicPrompt)
	return RoleProfile{
		Name:                      name,
		SystemPrompt:              systemPrompt,
		SystemPromptCachePrefix:   staticPrompt,
		SystemPromptDynamicSuffix: dynamicPrompt,
		Skills:                    skills,
		Tools:                     append([]langtools.Tool{}, tools...),
		Capabilities:              capabilities,
	}
}

func joinSystemPromptParts(cachePrefix, dynamicSuffix string) string {
	cachePrefix = strings.TrimSpace(cachePrefix)
	dynamicSuffix = strings.TrimSpace(dynamicSuffix)
	switch {
	case cachePrefix == "":
		return dynamicSuffix
	case dynamicSuffix == "":
		return cachePrefix
	default:
		return cachePrefix + "\n\n" + dynamicSuffix
	}
}
