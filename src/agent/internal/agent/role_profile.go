package agent

import (
	"fmt"
	"strings"

	langtools "github.com/tmc/langchaingo/tools"
)

type RoleName string

const (
	RolePlanner  RoleName = "planner"
	RoleExecutor RoleName = "executor"
	RoleVerifier RoleName = "verifier"
)

type RoleCapabilities struct {
	CanModifyPlan   bool
	CanExecuteStep  bool
	CanUseTools     bool
	CanDecideFinish bool
}

type RoleProfile struct {
	Name         RoleName
	SystemPrompt string
	Skills       ResolvedSkills
	Tools        []langtools.Tool
	Capabilities RoleCapabilities
}

type RoleProfiles struct {
	Planner  RoleProfile
	Executor RoleProfile
	Verifier RoleProfile
}

func buildRoleProfiles(cfg AgentConfig, skills ResolvedSkills, availableTools []langtools.Tool, memoryContext interface{}) RoleProfiles {
	roleMemory := normalizeMemoryContext(memoryContext)
	return RoleProfiles{
		Planner: buildRoleProfile(
			RolePlanner,
			cfg,
			skills,
			availableTools,
			roleMemory.RenderForRole(RolePlanner),
			RoleCapabilities{CanModifyPlan: true, CanUseTools: false},
			[]string{
				"You own the plan. Create or revise the ordered plan from the user request, prior tool observations, and verifier feedback.",
				"No other role can change the plan, so include the current next step explicitly.",
				"Use the executor tool catalog when planning. If a direct executor tool covers the request, plan that tool instead of a UI workaround.",
				"For launch-only requests to open an app, URL, or dialer screen, make the direct tool success the completion criterion, for example open_app ok=true; do not add a screenshot requirement unless the user asked to inspect or act inside the opened target.",
				"For semantic platform actions, plan quick_action first when a matching action may exist; semantic actions include back, home, app search, app switcher, notification shade, quick settings, copy, paste, cut, undo, redo, select all, find, send, and browser navigation.",
				"When planning quick_action, name the concrete action id and platform, for example: quick_action action=spotlight_search platform=android, quick_action action=back platform=android, or quick_action action=copy platform=ios; include a low-level fallback only after quick_action failure/no effect.",
				"Keep objective and completion_criteria tied to the original user request, not just the current step.",
				"If the current screenshot clearly identifies the app/page or device platform, include observed_state with app_name, page_name, platform (ios/android/mac), visible_text, dialogs, and confidence; otherwise leave unknown fields empty.",
				"When planning a platform-specific direct tool such as quick_action, use observed_state.platform from the current screenshot/context and name the concrete action id when possible.",
				"Plan a request_human_handoff step when the task requires credentials, verification, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Do not guess.",
				"You must NEVER call tools directly. Your role is planning only. Return planning decisions as JSON, not tool calls.",
				"Return only JSON: {\"objective\":\"original task in one sentence\",\"completion_criteria\":[\"criterion\"],\"plan\":[\"step\"],\"next_step\":\"step to execute now\",\"reason\":\"brief rationale\",\"observed_state\":{\"app_name\":\"\",\"page_name\":\"\",\"platform\":\"\",\"visible_text\":[],\"dialogs\":[],\"confidence\":0}}.",
			},
		),
		Executor: buildRoleProfile(
			RoleExecutor,
			cfg,
			skills,
			availableTools,
			"",
			RoleCapabilities{CanExecuteStep: true, CanUseTools: true},
			[]string{
				"Execute only the current next_step supplied by the planner.",
				"Do not create, reorder, or revise the plan. Do not decide that the run is complete.",
				"Prefer a direct tool that covers the requested operation before using UI automation tools.",
				"For semantic platform actions, try quick_action first when a matching action exists; if quick_action returns ok=false/status=reserved/error or the post-action screenshot shows no expected change, then fall back to keyboard_tap, touch_gesture, mouse_click, or other low-level tools.",
				"Do not retry the same quick_action binding more than once; after one failed primary binding or one listed alternative, switch to a low-level fallback.",
				"For platform-specific tools such as quick_action, use the platform shown in World State (ios/android/mac) and pass it explicitly in the tool input.",
				"If the next step requires external state or device action, call exactly one appropriate tool. If no tool is needed, return a concise candidate answer for verifier review.",
			},
		),
		Verifier: buildRoleProfile(
			RoleVerifier,
			cfg,
			skills,
			availableTools,
			roleMemory.RenderForRole(RoleVerifier),
			RoleCapabilities{CanDecideFinish: true},
			[]string{
				"You are the only role allowed to decide whether the run can end.",
				"Check the original user request, completion criteria, executor actions, candidate answers, and observations. Finish only when the answer is supported by the available evidence.",
				"Do not approve completion from a generic latest executor result alone; every explicit requirement in the original request must be proven. An authoritative direct tool result is sufficient evidence when it exactly covers the request, such as open_app returning ok=true for a launch-only app, URL, or dialer request. Require screenshot evidence for additional visible UI work or when a screenshot contradicts the tool result.",
				"If the current screenshot clearly identifies the app/page or device platform, include observed_state with app_name, page_name, platform (ios/android/mac), visible_text, dialogs, and confidence; otherwise leave unknown fields empty.",
				"Return only JSON: {\"can_finish\":true|false,\"final_answer\":\"answer when can_finish is true\",\"needs_replan\":true|false,\"reason\":\"brief reason\",\"observed_state\":{\"app_name\":\"\",\"page_name\":\"\",\"platform\":\"\",\"visible_text\":[],\"dialogs\":[],\"confidence\":0}}.",
			},
		),
	}
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
	parts := []string{
		fmt.Sprintf("You are the %s role in a multi-role Aiden agent loop.", name),
		"Base instruction:",
		combinedAgentInstruction(cfg),
		"",
		"Default behavior:",
		defaultAgentBehavior(),
		"",
		"Available skills:",
		skills.CatalogSummary(),
		"",
		"Active skills:",
		skills.CombinedInstructions(),
	}
	if text := strings.TrimSpace(cfg.RuntimeContext); text != "" {
		parts = append(parts,
			"",
			"Runtime context:",
			text,
		)
	}
	parts = append(parts, "", "Role rules:")
	for _, rule := range roleRules {
		parts = append(parts, "- "+rule)
	}
	parts = append(parts, "", "Available tools:")
	if capabilities.CanUseTools {
		parts = append(parts, describeTools(tools))
	} else {
		parts = append(parts, "- This role must not call tools directly.")
		if len(tools) > 0 {
			parts = append(parts, "- Executor tool catalog for planning/review only:")
			parts = append(parts, describeTools(tools))
		} else {
			parts = append(parts, "- none")
		}
	}
	if text := strings.TrimSpace(memoryContext); text != "" {
		parts = append(parts, "", text)
	}
	return RoleProfile{
		Name:         name,
		SystemPrompt: strings.Join(parts, "\n"),
		Skills:       skills,
		Tools:        append([]langtools.Tool{}, tools...),
		Capabilities: capabilities,
	}
}
