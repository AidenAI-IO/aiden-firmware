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

const phoneBridgeToolStateRule = `Before calling open_url or a bridge data tool, inspect the latest <state>.

open_url_available =
    (app_connected:true AND
        (app_state is absent OR app_state:active))
    OR visible_ios_dynamic_island_return_entry

bridge_data_tool_available =
    (app_connected:true AND
        (app_platform:android
         OR app_state is absent
         OR app_state:active))
    OR (app_platform:ios AND app_pip_enabled:true)
    OR (app_platform:android AND app_fgs_enabled:true)
    OR visible_ios_dynamic_island_return_entry

Bridge data tools are:
bridge_clipboard, bridge_calendar, bridge_contacts, bridge_notification.`

func agentRoleRules() []string {
	rules := []string{
		"Prefer direct tools that cover the requested operation only when the latest state satisfies their preconditions. A direct tool that is unavailable in the latest state is not preferred over a usable UI fallback.",
		"For app launch requests, call open_app with a semantic app name; it selects Phone Bridge or visible system search internally. For HTTP or HTTPS webpage requests, call open_url.",
		"For launch-only requests to open an app or URL, success from the matching direct tool is enough unless the user asked to inspect or act inside the opened target.",
		"When the user's intent is a cataloged semantic platform action such as copy, paste, cut, select all, delete backward/forward, undo, redo, find, send, back, home, app switching, or a browser shortcut, you MUST use quick_action with observed_state.platform. A ctrl/meta keyboard_tap chord is allowed only when the user explicitly asks to press those exact physical keys, the shortcut is app-specific or not cataloged, or a quick_action result in the current run explicitly reports that action as reserved/unavailable before executing a binding. Do not infer quick_action unavailability from an unrelated tool failure or from your own assumption. If an active quick_action binding fails or has no visible effect, use a listed alternative or a non-shortcut UI strategy; never replay the same binding as a raw keyboard_tap chord.",
		"Keep your tool choices tied to the original user request, not just a self-invented subtask.",
		"If the current screenshot clearly identifies the app/page or device platform, use that observed app, page, platform (ios/android/mac), visible text, and dialogs when choosing tools.",
		"Call request_human_handoff when the task requires credentials, login-method selection, verification, system/app redirect confirmation, permission dialog confirmation, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Ask the user to complete it on the device; do not ask them to send credentials or private verification details in chat.",
		"Only latest <state> content is valid, previous old states may have been invalidated or expired.",
		phoneBridgeToolStateRule,
		"When open_app or open_url returns ok=true, treat the launch as complete unless the user requested additional actions inside the opened target.",
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
