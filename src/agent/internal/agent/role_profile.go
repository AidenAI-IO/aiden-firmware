package agent

import (
	"strings"

	langtools "github.com/tmc/langchaingo/tools"
)

type RoleName string

const (
	RoleAgent    RoleName = "agent"
	RolePlanner  RoleName = RoleAgent
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
	cfg.ForceSimpleLoop = true
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
	structuredFinalRule := "Voice interaction is the core use case: keep user-facing output brief, natural, and easy to speak. When returning a final answer directly to the user, return plain text, not JSON."
	rules = append(rules,
		"Use the single-agent loop for every request: call available tools directly and return a final answer when the request is satisfied.",
		"Use set_todo when a task becomes multi-step and needs visible progress tracking; otherwise do not create a todo.",
		"Delegated plan mode is removed: do not enter, draft, commit, cancel, mention, or hand off work to separate roles.",
		structuredFinalRule,
		"Prefer direct tools that cover the requested operation before UI workarounds.",
		"For launch-only requests to open an app, URL, or dialer screen, direct tool success is enough unless the user asked to inspect or act inside the opened target.",
		"For semantic platform actions, use quick_action when a matching action may exist; pass observed_state.platform and the concrete action id when possible, and switch to a low-level fallback after failure/no effect.",
		"Keep your tool choices tied to the original user request, not just a self-invented subtask.",
		"If the current screenshot clearly identifies the app/page or device platform, use that observed app, page, platform (ios/android/mac), visible text, and dialogs when choosing tools.",
		"Call request_human_handoff when the task requires credentials, login-method selection, verification, system/app redirect confirmation, permission dialog confirmation, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Ask the user to complete it on the device; do not ask them to send credentials or private verification details in chat.",
		"For iOS phone tasks that need Aiden app-side tools and a later target app, treat target-app launch/navigation as an expensive phase boundary. Run all reorderable Phone Bridge app-side tools first while Aiden is foreground, including clipboard, calendar, contacts, and notification.",
		"For cross-app phone workflows where target-app work depends on Phone Bridge app-side data or actions, prefer prepare_phone_app_workflow as the first-class Aiden-foreground workflow. It batches direct app-side actions, optional text rendering, clipboard preparation, and optional target-app open before target-app UI work begins.",
		"When target-app text entry will use previously prepared text, use enter_text_in_field or enter_text_via_bridge with the workflow artifact_id when one is available. The runtime can resolve the prepared text from artifact_id, and HID/IME typing remains valid when clipboard paste is unavailable.",
		"For message, email, or post send workflows, text-entry verification is not send evidence. After verified entry, perform an explicit send action and continue until current tool or screenshot evidence proves the send happened, such as an outgoing bubble, sent item, or cleared input after the send action.",
	)
	if openAppAvailable {
		rules = append(rules, "For launch-only app, URL, or dialer requests, open_app ok=true is enough unless the user asked to inspect or act inside the opened target.")
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
