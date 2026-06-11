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
			appendLoopMetaTools(availableTools),
			roleMemory.RenderForRole(RolePlanner),
			RoleCapabilities{CanModifyPlan: true, CanUseTools: true},
			[]string{
				"In default mode, handle only simple tasks: a direct answer, one tool call, or at most two short steps total.",
				"If completing the request will likely need three or more steps, call enter_plan_mode first. Do not keep executing directly in default mode once you expect 3+ steps.",
				"Also call enter_plan_mode for branching, sustained tracking, or tasks that need explicit completion criteria across multiple UI states.",
				"In plan mode, explore with tools, maintain a draft plan, then call commit_plan to delegate execution or cancel_plan to return to default mode.",
				"commit_plan is only available in plan mode. cancel_plan clears draft planning state and returns to default mode.",
				"Prefer direct tools that cover the requested operation before UI workarounds. If a direct executor tool covers the request, plan or call that tool instead of a UI workaround.",
				"For launch-only requests to open an app, URL, or dialer screen, make the direct tool success the completion criterion, for example open_app ok=true; do not add a screenshot requirement unless the user asked to inspect or act inside the opened target.",
				"For semantic platform actions, plan quick_action first when a matching action may exist; semantic actions include back, home, app search, app switcher, notification shade, quick settings, copy, paste, cut, undo, redo, select all, find, send, and browser navigation.",
				"When planning quick_action, name the concrete action id and platform, for example: quick_action action=spotlight_search platform=android, quick_action action=back platform=android, or quick_action action=copy platform=ios; include a low-level fallback only after quick_action failure/no effect.",
				"When planning a platform-specific direct tool such as quick_action, use observed_state.platform from the current screenshot/context and name the concrete action id when possible.",
				"Keep objective and completion_criteria tied to the original user request, not just the current step.",
				"If the current screenshot clearly identifies the app/page or device platform, include observed_state with app_name, page_name, platform (ios/android/mac), visible_text, dialogs, and confidence when relevant.",
				"Plan a request_human_handoff step when the task requires credentials, verification, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Do not guess.",
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
		Verifier: buildVerifierRoleProfile([]string{
			"Verify only the current executor step provided in the user message. Do not judge overall task completion unless the user message marks this as the final committed plan step.",
			"Use the latest executor result, tool observations, screenshots, and candidate answers to decide whether that step succeeded.",
			"An authoritative direct tool result is sufficient evidence when it exactly covers the current step, such as open_app returning ok=true for a launch-only app, URL, or dialer request. Require screenshot evidence for additional visible UI work or when a screenshot contradicts the tool result.",
			"If the current step succeeded and more committed plan steps remain: return can_finish=false and needs_replan=false.",
			"If the current step succeeded and this is the final committed plan step: return can_finish=true with final_answer for the user.",
			"If the current step failed, had no effect, or evidence is insufficient: return can_finish=false and needs_replan=true with a brief reason for the planner.",
			"If the screenshot clearly identifies app/page/platform, include observed_state with app_name, page_name, platform (ios/android/mac), visible_text, dialogs, and confidence; otherwise leave unknown fields empty.",
			"Return only JSON: {\"can_finish\":true|false,\"final_answer\":\"answer when can_finish is true\",\"needs_replan\":true|false,\"reason\":\"brief reason\",\"observed_state\":{\"app_name\":\"\",\"page_name\":\"\",\"platform\":\"\",\"visible_text\":[],\"dialogs\":[],\"confidence\":0}}.",
		}),
	}
}

func buildVerifierRoleProfile(roleRules []string) RoleProfile {
	parts := []string{"## Role rules"}
	for _, rule := range roleRules {
		parts = append(parts, "- "+rule)
	}
	return RoleProfile{
		Name:         RoleVerifier,
		SystemPrompt: strings.Join(parts, "\n"),
		Capabilities: RoleCapabilities{CanDecideFinish: true},
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
		"",
		"## Base instruction",
		combinedAgentInstruction(cfg),
		"",
		"## Default behavior",
		defaultAgentBehavior(),
		"",
		"## Available skills",
		skills.CatalogSummary(),
		"",
		"## Active skills",
		skills.CombinedInstructions(),
	}
	if text := strings.TrimSpace(cfg.RuntimeContext); text != "" {
		parts = append(parts,
			"",
			"## Runtime context",
			text,
		)
	}
	parts = append(parts, "", "## Role rules")
	for _, rule := range roleRules {
		parts = append(parts, "- "+rule)
	}
	if name != RoleVerifier {
		parts = append(parts, "", "## Available tools")
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
