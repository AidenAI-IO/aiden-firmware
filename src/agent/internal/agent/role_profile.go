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
		"When the user's intent is a cataloged semantic platform action such as copy, paste, cut, select all, delete backward/forward, undo, redo, find, send, back, home, app switching, or a browser shortcut, you MUST use quick_action and let runtime select the platform from global device_type state. For go-home/home-screen requests such as 回到桌面, call quick_action with {\"action\":\"home\"} first; on Android it uses KEYCODE_HOME, while touch_gesture {\"type\":\"home\"} remains only a fallback. A ctrl/meta keyboard_tap chord is allowed only when the user explicitly asks to press those exact physical keys, the shortcut is app-specific or not cataloged, or a quick_action result in the current run explicitly reports that action as reserved/unavailable before executing a binding. Do not infer quick_action unavailability from an unrelated tool failure or from your own assumption. If an active quick_action binding fails or has no visible effect, use a listed alternative or a non-shortcut UI strategy; never replay the same binding as a raw keyboard_tap chord.",
		"Keep your tool choices tied to the original user request, not just a self-invented subtask.",
		"If the current screenshot clearly identifies the app/page or device platform, use that observed app, page, platform (ios/android/mac), visible text, and dialogs when choosing tools.",
		"Call request_human_handoff when the task requires credentials, login-method selection, verification, system/app redirect confirmation, permission dialog confirmation, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Ask the user to complete it on the device; do not ask them to send credentials or private verification details in chat.",
		"Only latest <state> content is valid, previous old states may have been invalidated or expired.",
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
	promptParts := []string{
		"You are the Aiden agent.",
		currentDateContext(cfg.Locale),
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

	promptParts = append(promptParts, "", "## Role rules")
	for _, rule := range roleRules {
		promptParts = append(promptParts, "- "+rule)
	}
	promptParts = append(promptParts, "", "## Response language", responseLanguageGuidance(cfg.Locale))
	systemPrompt := strings.Join(promptParts, "\n")

	return RoleProfile{
		SystemPrompt: systemPrompt,
		Skills:       skills,
		Tools:        append([]langtools.Tool{}, tools...),
	}
}
