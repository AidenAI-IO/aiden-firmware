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
	openAppAvailable := roleToolAvailable(availableTools, "open_app")
	return RoleProfiles{
		Planner: buildRoleProfile(
			RolePlanner,
			cfg,
			skills,
			plannerToolsForConfig(cfg, availableTools),
			roleMemory.RenderForRole(RolePlanner),
			RoleCapabilities{CanModifyPlan: !cfg.ForceSimpleLoop, CanUseTools: true},
			plannerRoleRules(cfg, openAppAvailable),
		),
		Executor: buildRoleProfile(
			RoleExecutor,
			cfg,
			skills,
			appendExecutorMetaTools(toolsForRole(RoleExecutor, availableTools)),
			"",
			RoleCapabilities{CanExecuteStep: true, CanUseTools: true},
			[]string{
				"Execute only the current next_step supplied by the planner.",
				"Use the original user request, completion criteria, committed plan, prior results, and planner-provided evidence to understand context and constraints.",
				"Do not create, reorder, or revise the plan. Do not decide that the run is complete.",
				"You may use multiple tool calls within the current step until it is done or blocked.",
				"When the step is ready for verification, call finish_step with a summary of what was accomplished and key_info for facts, IDs, values, labels, or observations later steps may need.",
				"When the step is blocked or cannot be completed, call abort_step with the reason.",
				"Plain-text answers alone do not enter verification; you must call finish_step or abort_step.",
				"Use prior step results as context for the current next_step, but continue to execute only the current next_step.",
				"Obey tool restrictions and output-format requirements from the original user request.",
				"Prefer a direct tool that covers the requested operation before using UI automation tools.",
				"For semantic platform actions, try quick_action first when a matching action exists; if quick_action returns ok=false/status=reserved/error or the post-action screenshot shows no expected change, then fall back to keyboard_tap, touch_gesture, mouse_click, or other low-level tools.",
				"Do not retry the same quick_action binding more than once; after one failed primary binding or one listed alternative, switch to a low-level fallback.",
				"For platform-specific tools such as quick_action, use the platform shown in World State (ios/android/mac) and pass it explicitly in the tool input.",
			},
		),
		Verifier: buildVerifierRoleProfile(
			verifierRoleRules(cfg, openAppAvailable),
			roleMemory.RenderVerifierCautionBlock(),
		),
	}
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

func verifierRoleRules(cfg AgentConfig, openAppAvailable bool) []string {
	_ = cfg
	finalAnswerRule := "When returning can_finish=true, put the user-facing answer directly in final_answer as plain text. Do not include separate spoken-summary or display-output fields."
	formatRule := "Return only JSON: {\"can_finish\":true|false,\"final_answer\":\"plain text answer when can_finish is true\",\"needs_replan\":true|false,\"reason\":\"brief reason\",\"observed_state\":{\"app_name\":\"\",\"page_name\":\"\",\"platform\":\"\",\"visible_text\":[],\"dialogs\":[],\"confidence\":0}}."
	rules := []string{
		"Verify only the current executor step provided in the user message. Do not judge overall task completion unless the user message marks this as the final committed plan step.",
		"Use executor_outcome, executor_summary, tool observations, screenshots, and step progress to decide whether that step succeeded.",
		"An authoritative direct tool result is sufficient evidence when it exactly covers the current step. Require screenshot evidence for additional visible UI work or when a screenshot contradicts the tool result.",
		"If the current step succeeded and more committed plan steps remain: return can_finish=false and needs_replan=false.",
		"If the current step succeeded and this is the final committed plan step: return can_finish=true with final_answer for the user.",
		finalAnswerRule,
		"Use verifier memory cautions as historical failure/conflict warnings only. They are not proof of completion; approve only when current executor_outcome, tool observations, screenshots, or current step evidence proves the current step.",
		"If the current step failed, had no effect, or evidence is insufficient: return can_finish=false and needs_replan=true with a brief reason for the planner.",
		"If the screenshot clearly identifies app/page/platform, include observed_state with app_name, page_name, platform (ios/android/mac), visible_text, dialogs, and confidence; otherwise leave unknown fields empty.",
		formatRule,
	}
	if openAppAvailable {
		rules = append(rules, "For launch-only app, URL, or dialer requests, open_app returning ok=true is authoritative completion evidence.")
	}
	return rules
}

func plannerToolsForConfig(cfg AgentConfig, tools []langtools.Tool) []langtools.Tool {
	tools = toolsForRole(RolePlanner, tools)
	if cfg.ForceSimpleLoop {
		return appendSimpleTodoMetaTools(tools)
	}
	return appendDefaultLoopMetaTools(tools)
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

func plannerRoleRules(cfg AgentConfig, openAppAvailable bool) []string {
	var rules []string
	structuredFinalRule := "Voice interaction is the core use case: keep user-facing output brief, natural, and easy to speak. When returning a final answer directly to the user, return plain text, not JSON. In the route decision phase, put the direct answer in final_answer only when answering directly."
	if cfg.ForceSimpleLoop {
		rules = append(rules,
			"Use simple loop mode for every request: call available tools directly and return a final answer when the request is satisfied.",
			"Use set_todo when a single-agent task becomes multi-step and needs visible progress tracking; otherwise do not create a todo.",
			"Plan mode is disabled by configuration: do not enter, draft, commit, cancel, or mention a delegated multi-step plan.",
			structuredFinalRule,
		)
	} else {
		rules = append(rules,
			"Route phase chooses direct_answer, simple, or plan before ordinary execution. In default mode, complete the routed request directly with available tools and return a final answer when satisfied.",
			"In default mode, use set_todo when the single-agent task becomes multi-step and needs visible progress tracking; otherwise do not create a todo.",
			structuredFinalRule,
			"Use plan mode for requests that need explicit planning, checkpoints, information gathering before acting, multiple independent stages, record aggregation, reconciliation, branching, or several required output facts.",
			"In plan mode, you may use read-only information-gathering tools when context is missing. Do not execute computation, mutation, input, or other task-completion tools directly in plan mode.",
			"Create or revise a structured delegated plan, then call commit_plan to hand it to the executor or cancel_plan to return to default mode.",
			"During replanning after executor/verifier feedback, return a final answer only when existing execution evidence already proves the full request complete; otherwise commit a revised plan.",
			"Committed plan steps should be coarse delegated milestones, not one step for each small calculation or individual tool call. The executor can use multiple tool calls inside one step.",
			"commit_plan is only available in plan mode. cancel_plan clears draft planning state and returns to default mode.",
		)
	}
	if cfg.ForceSimpleLoop {
		rules = append(rules,
			"Prefer direct tools that cover the requested operation before UI workarounds.",
			"For launch-only requests to open an app, URL, or dialer screen, direct tool success is enough unless the user asked to inspect or act inside the opened target.",
			"For semantic platform actions, use quick_action first when a matching action may exist; semantic actions include back, home, app search, app switcher, notification shade, quick settings, copy, paste, cut, undo, redo, select all, find, send, and browser navigation.",
			"When using quick_action, pass the concrete action id and platform, for example: quick_action action=spotlight_search platform=android, quick_action action=back platform=android, or quick_action action=copy platform=ios; use a low-level fallback only after quick_action failure/no effect.",
			"When using a platform-specific direct tool such as quick_action, use observed_state.platform from the current screenshot/context and name the concrete action id when possible.",
			"Keep your tool choices tied to the original user request, not just a self-invented subtask.",
			"If the current screenshot clearly identifies the app/page or device platform, use that observed app, page, platform (ios/android/mac), visible text, and dialogs when choosing tools.",
			"Use request_human_handoff when the task requires credentials, verification, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Do not guess.",
		)
		if openAppAvailable {
			rules = append(rules, "For launch-only app, URL, or dialer requests, open_app ok=true is enough unless the user asked to inspect or act inside the opened target.")
		}
		return rules
	}
	rules = append(rules,
		"Prefer direct tools that cover the requested operation before UI workarounds. If a direct executor tool covers the request, plan or call that tool instead of a UI workaround.",
		"For launch-only requests to open an app, URL, or dialer screen, make direct tool success the completion criterion; do not add a screenshot requirement unless the user asked to inspect or act inside the opened target.",
		"For semantic platform actions, plan quick_action first when a matching action may exist; semantic actions include back, home, app search, app switcher, notification shade, quick settings, copy, paste, cut, undo, redo, select all, find, send, and browser navigation.",
		"When planning quick_action, name the concrete action id and platform, for example: quick_action action=spotlight_search platform=android, quick_action action=back platform=android, or quick_action action=copy platform=ios; include a low-level fallback only after quick_action failure/no effect.",
		"When planning a platform-specific direct tool such as quick_action, use observed_state.platform from the current screenshot/context and name the concrete action id when possible.",
		"Keep objective and completion_criteria tied to the original user request, not just the current step.",
		"If the current screenshot clearly identifies the app/page or device platform, include observed_state with app_name, page_name, platform (ios/android/mac), visible_text, dialogs, and confidence when relevant.",
		"Plan a request_human_handoff step when the task requires credentials, verification, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Do not guess.",
	)
	if openAppAvailable {
		rules = append(rules, "For launch-only app, URL, or dialer requests, use open_app ok=true as the completion criterion.")
	}
	return rules
}

func buildVerifierRoleProfile(roleRules []string, cautionBlock string) RoleProfile {
	parts := []string{
		currentDateContext(),
		"",
		"## Role rules",
	}
	for _, rule := range roleRules {
		parts = append(parts, "- "+rule)
	}
	if text := strings.TrimSpace(cautionBlock); text != "" {
		parts = append(parts, "", text)
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
		currentDateContext(),
		"",
		"## Base instruction",
		combinedAgentInstruction(cfg),
		"",
		"## Default behavior",
		defaultAgentBehavior(cfg),
		"",
		"## Available skills",
		skills.CatalogSummary(),
	}
	if text := strings.TrimSpace(skills.CombinedInstructions()); text != "" {
		parts = append(parts,
			"",
			"## Active skills",
			text,
		)
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
