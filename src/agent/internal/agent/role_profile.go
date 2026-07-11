package agent

import (
	"strings"

	langtools "github.com/tmc/langchaingo/tools"
)

type RoleProfile struct {
	SystemPrompt string
	Skills       *SkillManager
	Tools        []langtools.Tool
}

func agentRoleRules() []string {
	rules := []string{
		"Prefer direct tools that cover the requested operation before UI workarounds.",
		"For launch-only requests to open an app, URL, or dialer screen, direct tool success is enough unless the user asked to inspect or act inside the opened target.",
		"For semantic platform actions, use quick_action when a matching action may exist; pass observed_state.platform and the concrete action id when possible, and switch to a low-level fallback after failure/no effect.",
		"Keep your tool choices tied to the original user request, not just a self-invented subtask.",
		"If the current screenshot clearly identifies the app/page or device platform, use that observed app, page, platform (ios/android/mac), visible text, and dialogs when choosing tools.",
		"Call request_human_handoff when the task requires credentials, login-method selection, verification, system/app redirect confirmation, permission dialog confirmation, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Ask the user to complete it on the device; do not ask them to send credentials or private verification details in chat.",
		"For launch-only app, URL, or dialer requests, bridge_open_app ok=true is enough unless the user asked to inspect or act inside the opened target.",
	}
	return rules
}

func buildProfile(
	cfg AgentConfig,
	skills *SkillManager,
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
		"The entries below are discovery summaries only. Load the matching skill with skill_read before following its instructions.",
		skills.CatalogSummary(),
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
