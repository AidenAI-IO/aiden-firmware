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
	Name                      RoleName
	SystemPrompt              string
	SystemPromptCachePrefix   string
	SystemPromptDynamicSuffix string
	Skills                    ResolvedSkills
	Tools                     []langtools.Tool
	Capabilities              RoleCapabilities
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
				"When the blocker requires private credentials, CAPTCHA, verification code, biometric/identity confirmation, a lock-screen unlock, system/app redirect confirmation, permission dialog confirmation, or another sensitive human action, call request_human_handoff before abort_step; ask the user to complete it on the device, never to send credentials in chat.",
				"Plain-text answers alone do not enter verification; you must call finish_step or abort_step.",
				"Use prior step results as context for the current next_step, but continue to execute only the current next_step.",
				"Obey tool restrictions and output-format requirements from the original user request.",
				"Prefer a direct tool that covers the requested operation before using UI automation tools.",
				"For cross-app phone workflows where target-app work depends on PhoneBridge app-side data or actions, prefer prepare_phone_app_workflow as the first-class Aiden-foreground workflow. It batches direct app-side actions, optional message/text rendering, clipboard preparation, and optional target-app open before target-app UI work begins.",
				"When the current step gathers data that a later iOS target-app message will include and prepare_phone_app_workflow is not available, compose the final message text and call clipboard action=write before finish_step, while Aiden is still foreground. Record the prepared message text in key_info. If clipboard preparation fails, abort or replan; do not open the target app to continue.",
				"When the current step will open or navigate an iOS target app, run all reorderable Phone Bridge app-side tools first while Aiden is foreground, including clipboard, calendar, contacts, and notification. Treat target-app launch/navigation as an expensive phase boundary after which Aiden app-side tools should not be used again unless the user explicitly accepts the cost. For later known text entry, prepare the clipboard before target-app launch/navigation.",
				"For message composition after prepared clipboard text is available in the target chat, use enter_text_in_field or enter_text_via_bridge with artifact_id and send_after_commit=true so one runtime path focuses, pastes, verifies the field text, sends, and verifies the input cleared or changed after send.",
				"For semantic platform actions, prefer quick_action when a matching action exists; pass the platform shown in World State and switch to keyboard_tap, touch_gesture, mouse_click, or another low-level fallback after at most one failed binding or listed alternative.",
				"For device, app, or phone operation steps, do not finish_step from intent alone; continue until current tool or screenshot evidence proves the step. For message/email/post send steps, capture evidence such as an outgoing bubble or cleared input after the send action before finish_step.",
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
	formatRule := "Return only JSON: {\"can_finish\":true|false,\"final_answer\":\"plain text answer when can_finish is true\",\"needs_replan\":true|false,\"step_status\":\"succeeded|failed|blocked|uncertain\",\"reason\":\"brief reason\",\"observed_state\":{\"app_name\":\"\",\"page_name\":\"\",\"platform\":\"\",\"visible_text\":[],\"dialogs\":[],\"confidence\":0}}."
	rules := []string{
		"Default to Simplified Chinese in final_answer and user-facing notes; switch languages only when the user clearly asks for another language.",
		"When writing final_answer, do not mention or hint at internal automation implementation details such as run_script, local scripts, JSONL, script filenames, pre-recorded steps, demo scripts, or automation scripts; describe only whether the user goal is complete, even if those details appear in execution evidence.",
		"Verify only the current executor step provided in the user message. Do not judge overall task completion unless the user message marks this as the final committed plan step.",
		"Use executor_outcome, executor_summary, tool observations, screenshots, and step progress to decide whether that step succeeded.",
		"An authoritative direct tool result is sufficient evidence when it exactly covers the current step. Require screenshot evidence for additional visible UI work or when a screenshot contradicts the tool result.",
		"Device, app, or phone operation steps cannot be approved from intent alone. For send-message/email/post steps, require current evidence that the send happened, such as an outgoing bubble, sent item, or cleared input after the send action.",
		"If the current step succeeded and more committed plan steps remain: return step_status=\"succeeded\", can_finish=false, and needs_replan=false.",
		"If the current step succeeded and this is the final committed plan step: return step_status=\"succeeded\" and can_finish=true with final_answer for the user.",
		finalAnswerRule,
		"Use verifier memory cautions as historical failure/conflict warnings only. They are not proof of completion; approve only when current executor_outcome, tool observations, screenshots, or current step evidence proves the current step.",
		"If the current step failed, had no effect, or evidence is insufficient: return step_status=\"failed\" or \"uncertain\", can_finish=false, and needs_replan=true with a brief reason for the planner.",
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
			"For phone messaging plans with separate app/chat and address-book names, preserve both names in objective, completion criteria, and every replanning pass: query the address book using the address-book name, then open/search the app chat using the app/chat name.",
			"For iOS phone tasks that need Aiden app-side tools and a later target app, treat target-app launch/navigation as an expensive phase boundary. Batch all reorderable Aiden-foreground work before that boundary, including clipboard, calendar, contacts, and notification.",
			"For cross-app phone workflows where target-app work depends on PhoneBridge app-side data or actions, plan prepare_phone_app_workflow as the first-class preparation workflow instead of separate app-side tool + clipboard + target-app launch steps when possible. Make that workflow one coarse milestone that runs direct app-side actions, prepares the final target_text clipboard while Aiden is foreground when needed, and may open the target app last.",
			"If prepare_phone_app_workflow is not available, declare target_text artifacts, gather data, compose fixed or templated final text, and write it to clipboard while Aiden is foreground; do not create a separate target-app launch milestone before app-side preparation. Then open/search the target app last, use target-preserving tools such as enter_text_in_field with the artifact_id and send_after_commit=true when sending a message, and verify.",
			"commit_plan is only available in plan mode. cancel_plan clears draft planning state and returns to default mode.",
		)
	}
	if cfg.ForceSimpleLoop {
		rules = append(rules,
			"Prefer direct tools that cover the requested operation before UI workarounds.",
			"For launch-only requests to open an app, URL, or dialer screen, direct tool success is enough unless the user asked to inspect or act inside the opened target.",
			"For semantic platform actions, use quick_action when a matching action may exist; pass observed_state.platform and the concrete action id when possible, and switch to a low-level fallback after failure/no effect.",
			"For device, app, or phone operation requests, do not return final success from intent alone; call tools and require current tool or screenshot evidence. For message/email/post send workflows, success requires evidence that the send happened, such as an outgoing bubble or cleared input after the send action.",
			"Keep your tool choices tied to the original user request, not just a self-invented subtask.",
			"If the current screenshot clearly identifies the app/page or device platform, use that observed app, page, platform (ios/android/mac), visible text, and dialogs when choosing tools.",
			"Call request_human_handoff when the task requires credentials, login-method selection, verification, system/app redirect confirmation, permission dialog confirmation, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Ask the user to complete it on the device; do not ask them to send credentials or private verification details in chat.",
		)
		if openAppAvailable {
			rules = append(rules, "For launch-only app, URL, or dialer requests, open_app ok=true is enough unless the user asked to inspect or act inside the opened target.")
		}
		return rules
	}
	rules = append(rules,
		"Prefer direct tools that cover the requested operation before UI workarounds. If a direct executor tool covers the request, plan or call that tool instead of a UI workaround.",
		"For launch-only requests to open an app, URL, or dialer screen, make direct tool success the completion criterion; do not add a screenshot requirement unless the user asked to inspect or act inside the opened target.",
		"For semantic platform actions, plan quick_action when a matching action may exist; include observed_state.platform, the concrete action id when possible, and a low-level fallback only after quick_action failure/no effect.",
		"For device, app, or phone operation requests, do not return final success from intent alone; plan or call tools and require current tool or screenshot evidence. For message/email/post send workflows, success requires evidence that the send happened, such as an outgoing bubble or cleared input after the send action.",
		"Keep objective and completion_criteria tied to the original user request, not just the current step.",
		"When the original user request names one person for a messaging app/chat and another name for Contacts/address book lookup, never rewrite the app/chat target to the Contacts/address-book name during planning or replanning unless the user explicitly says they are identical.",
		"If the current screenshot clearly identifies the app/page or device platform, include observed_state with app_name, page_name, platform (ios/android/mac), visible_text, dialogs, and confidence when relevant.",
		"Call request_human_handoff when the task requires credentials, login-method selection, verification, system/app redirect confirmation, permission dialog confirmation, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. In plan mode, request_human_handoff is allowed before commit_plan when the blocker is already known. Ask the user to complete it on the device; do not ask them to send credentials or private verification details in chat.",
	)
	if openAppAvailable {
		rules = append(rules, "For launch-only app, URL, or dialer requests, use open_app ok=true as the completion criterion.")
	}
	return rules
}

func buildVerifierRoleProfile(roleRules []string, cautionBlock string) RoleProfile {
	staticParts := []string{
		currentDateContext(),
		"",
		"## Role rules",
	}
	for _, rule := range roleRules {
		staticParts = append(staticParts, "- "+rule)
	}
	staticPrompt := strings.Join(staticParts, "\n")
	dynamicPrompt := strings.TrimSpace(cautionBlock)
	systemPrompt := joinSystemPromptParts(staticPrompt, dynamicPrompt)

	return RoleProfile{
		Name:                      RoleVerifier,
		SystemPrompt:              systemPrompt,
		SystemPromptCachePrefix:   staticPrompt,
		SystemPromptDynamicSuffix: dynamicPrompt,
		Capabilities:              RoleCapabilities{CanDecideFinish: true},
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
	staticParts := []string{
		fmt.Sprintf("You are the %s role in a multi-role Aiden agent loop.", name),
		currentDateContext(),
		"",
		"## Base instruction",
		combinedAgentInstruction(cfg),
		"",
		"## Environment",
		agentEnvironmentGuidance(),
		"",
		"## Default behavior",
		defaultAgentBehavior(cfg),
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
	// Runtime context and memory change per turn; keep them after the static
	// role rules so the stable prefix stays prompt-cache friendly across turns.
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
