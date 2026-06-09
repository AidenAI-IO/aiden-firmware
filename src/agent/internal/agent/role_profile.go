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
				"Keep objective and completion_criteria tied to the original user request, not just the current step.",
				"If the current screenshot clearly identifies the app/page, include observed_state with app_name, page_name, visible_text, dialogs, and confidence; otherwise leave it empty.",
				"Plan a request_human_handoff step when the task requires credentials, verification, or human judgment your tools cannot fulfill, or when the user refers to a target you cannot unambiguously identify from the screen. Do not guess.",
				"You must NEVER call tools directly. Your role is planning only. Return planning decisions as JSON, not tool calls.",
				"Return only JSON: {\"objective\":\"original task in one sentence\",\"completion_criteria\":[\"criterion\"],\"plan\":[\"step\"],\"next_step\":\"step to execute now\",\"reason\":\"brief rationale\",\"observed_state\":{\"app_name\":\"\",\"page_name\":\"\",\"visible_text\":[],\"dialogs\":[],\"confidence\":0}}.",
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
				"Never approve completion from the latest executor result alone; every explicit requirement in the original request must be proven.",
				"If the current screenshot clearly identifies the app/page, include observed_state with app_name, page_name, visible_text, dialogs, and confidence; otherwise leave it empty.",
				"Return only JSON: {\"can_finish\":true|false,\"final_answer\":\"answer when can_finish is true\",\"needs_replan\":true|false,\"reason\":\"brief reason\",\"observed_state\":{\"app_name\":\"\",\"page_name\":\"\",\"visible_text\":[],\"dialogs\":[],\"confidence\":0}}.",
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
