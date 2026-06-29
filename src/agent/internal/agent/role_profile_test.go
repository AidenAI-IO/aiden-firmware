package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestBuildRoleProfilesInjectsSkillsAndCapabilities(t *testing.T) {
	skills := ResolvedSkills{
		Names:        []string{"ui"},
		Instructions: []string{"[ui] inspect before acting"},
	}
	tools := []langtools.Tool{
		&stubTool{name: "screenshot", description: "Capture screen."},
		&stubTool{name: "save_memory", description: "Save memory."},
	}
	toolSpeechDisabled := false

	profiles := buildRoleProfiles(
		AgentConfig{Instruction: "base", AdditionalPrompt: "extra", VoiceToolCallSpeech: &toolSpeechDisabled},
		skills,
		tools,
		"MEMORY CONTEXT",
	)

	if !profiles.Planner.Capabilities.CanModifyPlan || profiles.Executor.Capabilities.CanModifyPlan || profiles.Verifier.Capabilities.CanModifyPlan {
		t.Fatalf("only planner should be able to modify plans: %#v %#v %#v", profiles.Planner.Capabilities, profiles.Executor.Capabilities, profiles.Verifier.Capabilities)
	}
	if !profiles.Executor.Capabilities.CanExecuteStep || !profiles.Executor.Capabilities.CanUseTools {
		t.Fatalf("executor should be the tool-using execution role: %#v", profiles.Executor.Capabilities)
	}
	if !profiles.Verifier.Capabilities.CanDecideFinish || profiles.Executor.Capabilities.CanDecideFinish {
		t.Fatalf("only verifier should decide finish: verifier=%#v executor=%#v", profiles.Verifier.Capabilities, profiles.Executor.Capabilities)
	}

	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor} {
		for _, want := range []string{"base", "extra", "[ui] inspect before acting"} {
			if !strings.Contains(profile.SystemPrompt, want) {
				t.Fatalf("%s profile missing %q:\n%s", profile.Name, want, profile.SystemPrompt)
			}
		}
		for _, want := range []string{
			"## Base instruction",
			"## Environment",
			"## Default behavior",
			"## Available skills",
			"## Active skills",
			"## Role rules",
		} {
			if !strings.Contains(profile.SystemPrompt, want) {
				t.Fatalf("%s profile missing markdown section %q:\n%s", profile.Name, want, profile.SystemPrompt)
			}
		}
		if strings.Contains(profile.SystemPrompt, "## Available tools") {
			t.Fatalf("%s profile should not duplicate tool catalog in prompt:\n%s", profile.Name, profile.SystemPrompt)
		}
		for _, unexpected := range []string{"### Environment", "### Default Behavior"} {
			if strings.Contains(profile.SystemPrompt, unexpected) {
				t.Fatalf("%s profile should not include nested default prompt section %q:\n%s", profile.Name, unexpected, profile.SystemPrompt)
			}
		}
	}
	if !strings.Contains(profiles.Verifier.SystemPrompt, "## Role rules") {
		t.Fatalf("verifier profile missing role rules:\n%s", profiles.Verifier.SystemPrompt)
	}
	for _, unexpected := range []string{
		"## Base instruction",
		"## Default behavior",
		"## Available skills",
		"## Active skills",
		"## Available tools",
		"MEMORY CONTEXT",
		"base",
		"extra",
	} {
		if strings.Contains(profiles.Verifier.SystemPrompt, unexpected) {
			t.Fatalf("verifier profile should not include general prompt context, found %q:\n%s", unexpected, profiles.Verifier.SystemPrompt)
		}
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "MEMORY CONTEXT") {
		t.Fatalf("planner profile should receive memory context:\n%s", profiles.Planner.SystemPrompt)
	}
	if strings.Contains(profiles.Executor.SystemPrompt, "MEMORY CONTEXT") {
		t.Fatalf("memory context should not be injected into executor prompt: %q", profiles.Executor.SystemPrompt)
	}
	if strings.Contains(profiles.Verifier.SystemPrompt, "MEMORY CONTEXT") {
		t.Fatalf("memory context should not be injected into verifier prompt: %q", profiles.Verifier.SystemPrompt)
	}
	if !strings.Contains(profiles.Executor.SystemPrompt, "Execute only the current next_step") {
		t.Fatalf("executor prompt missing next_step constraint:\n%s", profiles.Executor.SystemPrompt)
	}
	if !strings.Contains(profiles.Executor.SystemPrompt, "request_human_handoff before abort_step") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "never to send credentials in chat") {
		t.Fatalf("executor prompt should require handoff before credential-sensitive aborts:\n%s", profiles.Executor.SystemPrompt)
	}
	if strings.Contains(profiles.Planner.SystemPrompt, "screenshot: Capture screen.") ||
		strings.Contains(profiles.Planner.SystemPrompt, "enter_plan_mode:") {
		t.Fatalf("planner prompt should not duplicate callable tool descriptions:\n%s", profiles.Planner.SystemPrompt)
	}
	hasProfileTool := func(profile RoleProfile, name string) bool {
		for _, tool := range profile.Tools {
			if tool.Name() == name {
				return true
			}
		}
		return false
	}
	if !hasProfileTool(profiles.Planner, "screenshot") || !hasProfileTool(profiles.Planner, "enter_plan_mode") {
		t.Fatalf("planner profile should retain callable tools and loop meta tools: %#v", profiles.Planner.Tools)
	}
	if !hasProfileTool(profiles.Planner, "save_memory") {
		t.Fatalf("planner profile should retain save_memory: %#v", profiles.Planner.Tools)
	}
	if hasProfileTool(profiles.Executor, "save_memory") {
		t.Fatalf("executor profile should not expose save_memory by default: %#v", profiles.Executor.Tools)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "Route phase chooses direct_answer, simple, or plan") {
		t.Fatalf("planner prompt should describe route-selected execution:\n%s", profiles.Planner.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "read-only information-gathering tools") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "record aggregation") {
		t.Fatalf("planner prompt should describe plan mode and readonly gathering:\n%s", profiles.Planner.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "Prefer direct tools that cover the requested operation") {
		t.Fatalf("planner prompt should prefer direct tools:\n%s", profiles.Planner.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "Call request_human_handoff") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "do not ask them to send credentials") {
		t.Fatalf("planner prompt should call handoff for sensitive user input:\n%s", profiles.Planner.SystemPrompt)
	}
	if strings.Contains(profiles.Planner.SystemPrompt, "open_app") ||
		strings.Contains(profiles.Verifier.SystemPrompt, "open_app") {
		t.Fatalf("role prompts should not mention open_app when that tool is unavailable: planner=%q verifier=%q", profiles.Planner.SystemPrompt, profiles.Verifier.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "plan quick_action when a matching action may exist") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "observed_state.platform") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "prefer quick_action when a matching action exists") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "switch to keyboard_tap") {
		t.Fatalf("role prompts should prefer quick_action before low-level fallback: planner=%q executor=%q", profiles.Planner.SystemPrompt, profiles.Executor.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "do not return final success from intent alone") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "outgoing bubble or cleared input after the send action") ||
		!strings.Contains(profiles.Verifier.SystemPrompt, "send-message/email/post steps") {
		t.Fatalf("role prompts should guard device operations and message sends: planner=%q executor=%q verifier=%q", profiles.Planner.SystemPrompt, profiles.Executor.SystemPrompt, profiles.Verifier.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "separate app/chat and address-book names") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "query the address book using the address-book name") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "never rewrite the app/chat target to the Contacts/address-book name") {
		t.Fatalf("planner prompt should preserve distinct app/chat and contacts names: planner=%q", profiles.Planner.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "batch all Aiden-foreground work before target-app navigation") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "first milestone must gather the data, compose the final message, and write it to clipboard") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "write it to clipboard while Aiden is foreground") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "do not create a separate target-app launch milestone before clipboard preparation") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "call clipboard action=write before finish_step") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "prepare the clipboard with clipboard action=write before target-app launch/navigation") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "Treat target-app launch/navigation as the boundary") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "Do not call clipboard after the target app/chat is already foreground") {
		t.Fatalf("role prompts should front-load iOS clipboard preparation: planner=%q executor=%q", profiles.Planner.SystemPrompt, profiles.Executor.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "platform (ios/android/mac)") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "platform shown in World State") ||
		!strings.Contains(profiles.Verifier.SystemPrompt, `"platform":""`) {
		t.Fatalf("role prompts should propagate observed platform for platform-specific tools: planner=%q executor=%q verifier=%q", profiles.Planner.SystemPrompt, profiles.Executor.SystemPrompt, profiles.Verifier.SystemPrompt)
	}
	if !strings.Contains(profiles.Verifier.SystemPrompt, "current executor step") ||
		!strings.Contains(profiles.Verifier.SystemPrompt, "final committed plan step") {
		t.Fatalf("verifier prompt should focus on per-step verification:\n%s", profiles.Verifier.SystemPrompt)
	}
	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor, profiles.Verifier} {
		for _, want := range []string{"Simplified Chinese", "do not mention or hint at internal automation implementation details", "run_script", "JSONL"} {
			if !strings.Contains(profile.SystemPrompt, want) {
				t.Fatalf("%s profile missing user-facing language/privacy rule %q:\n%s", profile.Name, want, profile.SystemPrompt)
			}
		}
	}
}

func TestBuildRoleProfilesOmitsActiveSkillsSectionWhenEmpty(t *testing.T) {
	profiles := buildRoleProfiles(
		AgentConfig{},
		ResolvedSkills{},
		nil,
		MemoryContext{},
	)

	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor} {
		for _, unexpected := range []string{
			"## Active skills",
			"No extra skill is active.",
		} {
			if strings.Contains(profile.SystemPrompt, unexpected) {
				t.Fatalf("%s profile should omit empty active skills section, found %q:\n%s", profile.Name, unexpected, profile.SystemPrompt)
			}
		}
	}
}

func TestBuildRoleProfilesInjectsVerifierCautionMemory(t *testing.T) {
	skills := ResolvedSkills{
		Names:        []string{"ui"},
		Instructions: []string{"[ui] skill instruction should stay out"},
	}
	memory := MemoryContext{
		Common: RoleMemoryContext{
			SessionSummary: "COMMON SESSION SHOULD STAY OUT",
			Profile:        "COMMON PROFILE SHOULD STAY OUT",
		},
		Planner: RoleMemoryContext{
			Procedures: []MemoryHit{{
				ID:      "planner_proc",
				Type:    "procedure",
				Summary: "planner procedure should stay out",
			}},
		},
		Verifier: RoleMemoryContext{
			FailureModes: []MemoryHit{
				{
					ID:      "failure_mem",
					Type:    "failure",
					Summary: "require fresh screen evidence before approving",
				},
				{
					ID:      "failed_episode",
					Type:    "task_episode_failure",
					Summary: "prior failed episode approved a stale screen",
				},
			},
			Conflicts: []MemoryHit{{
				ID:      "conflict_mem",
				Type:    "conflict",
				Summary: "old route conflicts with the current layout",
			}},
		},
	}

	profiles := buildRoleProfiles(
		AgentConfig{Instruction: "BASE SHOULD STAY OUT"},
		skills,
		nil,
		memory,
	)
	prompt := profiles.Verifier.SystemPrompt

	for _, want := range []string{
		"## Verifier memory cautions",
		"historical failure/conflict warnings only",
		"not proof of completion",
		"current executor_outcome, tool observations, screenshots, or current step evidence",
		"failure_mem",
		"require fresh screen evidence before approving",
		"failed_episode",
		"prior failed episode approved a stale screen",
		"conflict_mem",
		"old route conflicts with the current layout",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("verifier prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unexpected := range []string{
		"## Base instruction",
		"BASE SHOULD STAY OUT",
		"[ui] skill instruction should stay out",
		"COMMON SESSION SHOULD STAY OUT",
		"COMMON PROFILE SHOULD STAY OUT",
		"planner_proc",
		"planner procedure should stay out",
	} {
		if strings.Contains(prompt, unexpected) {
			t.Fatalf("verifier caution memory leaked %q:\n%s", unexpected, prompt)
		}
	}
}

func TestBuildRoleProfilesIncludesOpenAppRulesOnlyWhenAvailable(t *testing.T) {
	profiles := buildRoleProfiles(
		AgentConfig{},
		ResolvedSkills{},
		[]langtools.Tool{&stubTool{name: "open_app", description: "Open app."}},
		MemoryContext{},
	)

	if !strings.Contains(profiles.Planner.SystemPrompt, "open_app ok=true") ||
		!strings.Contains(profiles.Verifier.SystemPrompt, "open_app returning ok=true") {
		t.Fatalf("role prompts should treat launch-only open_app success as direct evidence when tool is available: planner=%q verifier=%q", profiles.Planner.SystemPrompt, profiles.Verifier.SystemPrompt)
	}
}

func TestBuildRoleProfilesUsePlainFinalAnswersForVoiceFirstOutput(t *testing.T) {
	profiles := buildRoleProfiles(
		AgentConfig{},
		ResolvedSkills{},
		nil,
		MemoryContext{},
	)

	for _, profile := range []RoleProfile{profiles.Planner, profiles.Verifier} {
		if strings.Contains(profile.SystemPrompt, `"speech":`) {
			t.Fatalf("%s prompt should not require structured speech:\n%s", profile.Name, profile.SystemPrompt)
		}
		if profile.Name == RolePlanner && (!strings.Contains(profile.SystemPrompt, "Voice interaction is the core use case") || !strings.Contains(profile.SystemPrompt, "plain text")) {
			t.Fatalf("planner prompt should ask for concise plain-text final answers:\n%s", profile.SystemPrompt)
		}
		if profile.Name == RoleVerifier && !strings.Contains(profile.SystemPrompt, "final_answer") {
			t.Fatalf("verifier prompt should keep final_answer as the plain user-facing answer:\n%s", profile.SystemPrompt)
		}
	}
}

func TestRoleCollaborativeExecutorPassesToolsViaWithTools(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{\"volume\":3}"}`, "Volume set to 3."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get or set audio playback volume.",
		output:      `{"volume":3}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use direct tools."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"audio_volume": tool}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "set volume to 3"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "Volume set to 3." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.tools) != 2 {
		t.Fatalf("expected one route call and one default-mode planner call, got %d", len(model.tools))
	}
	if len(model.tools[0]) != 0 {
		t.Fatalf("route phase should not expose function tools: %#v", model.tools[0])
	}
	if !llmToolsContain(model.tools[1], "audio_volume") {
		t.Fatalf("default-mode planner did not receive audio_volume function tool: %#v", model.tools[1])
	}
	if !llmToolsContain(model.tools[1], "enter_plan_mode") {
		t.Fatalf("default-mode planner did not receive loop meta function tool: %#v", model.tools[1])
	}
	if len(model.messages) != 2 {
		t.Fatalf("expected two planner model calls, got %d", len(model.messages))
	}

	plannerPrompt := messageText(model.messages[0])
	for _, unexpected := range []string{
		"audio_volume: Get or set audio playback volume.",
		"enter_plan_mode:",
		"## Available tools",
	} {
		if strings.Contains(plannerPrompt, unexpected) {
			t.Fatalf("planner prompt should not duplicate tool catalog %q:\n%s", unexpected, plannerPrompt)
		}
	}
}

func TestExecutorRoleDoesNotExposePlannerOnlySaveMemoryTool(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("save_1", "save_memory", `{"type":"profile","title":"Location","content":"The user is in Shanghai."}`),
		},
	}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{
			Executor: RoleProfile{Name: RoleExecutor, SystemPrompt: "executor"},
		},
		[]langtools.Tool{
			&stubTool{name: "screenshot", description: "Capture screen."},
			&stubTool{name: "save_memory", description: "Save memory.", output: `{"status":"saved"}`},
		},
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{
		Phase:               phaseExecution,
		PlanCommitted:       true,
		StepExecutionActive: true,
		NextStep:            "remember the user's location",
	}
	inputs := map[string]string{"input": "remember that my location is Shanghai", "history": ""}

	turn, err := executor.callExecutorTurn(context.Background(), inputs, state, toolSpecsForRole(RoleExecutor, executor.Tools))
	if err != nil {
		t.Fatalf("executor turn error: %v", err)
	}
	if len(model.tools) != 1 {
		t.Fatalf("expected one executor model call, got %d", len(model.tools))
	}
	if !llmToolsContain(model.tools[0], "screenshot") || !llmToolsContain(model.tools[0], toolFinishStep) {
		t.Fatalf("executor should receive ordinary executor tools and step meta tools: %#v", model.tools[0])
	}
	if llmToolsContain(model.tools[0], "save_memory") {
		t.Fatalf("executor received planner-only save_memory tool: %#v", model.tools[0])
	}
	if turn.Kind != executorTurnTool || turn.Step == nil {
		t.Fatalf("executor turn = %#v, want invalid tool step", turn)
	}
	if !strings.Contains(turn.Step.Observation, "save_memory is not a valid tool") {
		t.Fatalf("executor should reject planner-only save_memory at execution time: %q", turn.Step.Observation)
	}
}

func TestForceSimpleLoopOmitsPlanMetaTools(t *testing.T) {
	tools := []langtools.Tool{&stubTool{name: "audio_volume", description: "Get or set audio playback volume."}}
	profiles := buildRoleProfiles(
		AgentConfig{Instruction: "Use direct tools.", ForceSimpleLoop: true},
		ResolvedSkills{},
		tools,
		MemoryContext{},
	)

	if hasProfileTool(profiles.Planner, toolEnterPlanMode) ||
		hasProfileTool(profiles.Planner, toolCommitPlan) ||
		hasProfileTool(profiles.Planner, toolCancelPlan) {
		t.Fatalf("planner profile should not expose plan meta tools: %#v", profiles.Planner.Tools)
	}
	if profiles.Planner.Capabilities.CanModifyPlan {
		t.Fatalf("force_simple_loop planner should not advertise plan modification capability: %#v", profiles.Planner.Capabilities)
	}
	if !hasProfileTool(profiles.Planner, "audio_volume") {
		t.Fatalf("planner profile should keep normal tools: %#v", profiles.Planner.Tools)
	}
	for _, unexpected := range []string{"call enter_plan_mode", "commit_plan is only available", "cancel_plan clears"} {
		if strings.Contains(profiles.Planner.SystemPrompt, unexpected) {
			t.Fatalf("force_simple_loop planner prompt should not mention %q:\n%s", unexpected, profiles.Planner.SystemPrompt)
		}
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "Plan mode is disabled by configuration") {
		t.Fatalf("force_simple_loop planner prompt missing disabled-plan guidance:\n%s", profiles.Planner.SystemPrompt)
	}
}

func TestForceSimpleLoopPlannerCallOmitsPlanMetaTools(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{\"volume\":3}"}`, "Volume set to 3."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get or set audio playback volume.",
		output:      `{"volume":3}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use direct tools.", ForceSimpleLoop: true},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"audio_volume": tool}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "set volume to 3"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "Volume set to 3." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.tools) != 2 {
		t.Fatalf("expected two default-mode planner calls, got %d", len(model.tools))
	}
	if !llmToolsContain(model.tools[0], "audio_volume") {
		t.Fatalf("planner did not receive audio_volume function tool: %#v", model.tools[0])
	}
	if llmToolsContain(model.tools[0], toolEnterPlanMode) ||
		llmToolsContain(model.tools[0], toolCommitPlan) ||
		llmToolsContain(model.tools[0], toolCancelPlan) {
		t.Fatalf("force_simple_loop planner received plan meta tools: %#v", model.tools[0])
	}
	plannerPrompt := messageText(model.messages[0])
	for _, unexpected := range []string{"call enter_plan_mode", "commit_plan is available", "if the task likely needs 3+ steps", "logical subtasks"} {
		if strings.Contains(plannerPrompt, unexpected) {
			t.Fatalf("force_simple_loop prompt should not contain %q:\n%s", unexpected, plannerPrompt)
		}
	}
	for _, unexpected := range []string{
		"Planner runtime context (synthetic; not a new user request):",
		"Loop mode:",
		"force_simple_loop: true",
		"Original user request / root request:",
		"Latest user message:",
		"Session context view:",
	} {
		if strings.Contains(plannerPrompt, unexpected) {
			t.Fatalf("first-turn force_simple_loop planner prompt should not include runtime context %q:\n%s", unexpected, plannerPrompt)
		}
	}
}

func TestForceSimpleLoopRejectsUnexpectedPlanMetaToolCall(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			enterPlanModeToolCall(),
			contentResponse("done"),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use direct tools.", ForceSimpleLoop: true},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "try plan mode"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("output = %q, want done", result.Output)
	}
	if model.callCount != 2 {
		t.Fatalf("model calls = %d, want 2", model.callCount)
	}
	secondPrompt := messageText(model.messages[1])
	for _, unexpected := range []string{
		"Planner runtime context (synthetic; not a new user request):",
		"current_mode: default",
		"force_simple_loop: true",
		"current_mode: plan",
		"commit_plan is available",
	} {
		if strings.Contains(secondPrompt, unexpected) {
			t.Fatalf("force_simple_loop planner prompt should not expose runtime context %q:\n%s", unexpected, secondPrompt)
		}
	}
}

func llmToolsContain(tools []llms.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == name {
			return true
		}
	}
	return false
}

func TestRoleJSONFenceIsParsedAndLoggedAsCompactJSON(t *testing.T) {
	raw := "```json\n" +
		`{"objective":"在美团中找到最近的蜜雪冰城","completion_criteria":["进入店铺","加购两杯"],"plan":["点加号","加入购物车"],"next_step":"点击 +","reason":"需要两杯"}` +
		"\n```"

	decision := parsePlannerDecision(contentResponse(raw), "fallback")
	if decision.Objective != "在美团中找到最近的蜜雪冰城" {
		t.Fatalf("objective = %q", decision.Objective)
	}
	if decision.NextStep != "点击 +" {
		t.Fatalf("next_step = %q", decision.NextStep)
	}
	if len(decision.CompletionCriteria) != 2 {
		t.Fatalf("completion criteria = %#v", decision.CompletionCriteria)
	}

	debug := roleResponseDebugText(contentResponse(raw))
	if strings.Contains(debug, "```") || strings.Contains(debug, "\n") {
		t.Fatalf("debug output should be compact unfenced JSON, got %q", debug)
	}
	if !strings.Contains(debug, `"next_step":"点击 +"`) {
		t.Fatalf("debug output missing planner JSON: %q", debug)
	}
}

func TestRoleCollaborativeExecutorRequiresVerifierToFinish(t *testing.T) {
	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"draft answer", "produce final candidate"},
			contentResponse("premature candidate"),
			finishStepToolCall("premature candidate"),
			verifierStepContinueResponse("candidate needs another pass"),
			finishStepToolCall("second candidate"),
			verifierFinishResponse("verified final"),
		),
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "verified final" {
		t.Fatalf("output = %q, want verifier-approved final", result.Output)
	}
}

func TestRoleCollaborativeExecutorIgnoresExecutorPlanMutation(t *testing.T) {
	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"first planner step", "second planner step"},
			contentResponse(`{"plan":["executor changed plan"],"next_step":"bad"}`),
			finishStepToolCall("candidate"),
			verifierStepContinueResponse("executor cannot change the plan"),
			finishStepToolCall("candidate"),
			verifierFinishResponse("done"),
		),
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) < 6 {
		t.Fatalf("expected committed execution flow, got %d model calls", len(model.messages))
	}
	executorPrompt := messageText(model.messages[2])
	if !strings.Contains(executorPrompt, "Planner-approved next_step:\nfirst planner step") {
		t.Fatalf("executor did not receive the planner-approved next_step:\n%s", executorPrompt)
	}
	if !strings.Contains(executorPrompt, "Committed plan:") {
		t.Fatalf("executor should receive committed plan context:\n%s", executorPrompt)
	}
	secondExecutorPrompt := messageText(model.messages[5])
	if !strings.Contains(secondExecutorPrompt, "Planner-approved next_step:\nsecond planner step") {
		t.Fatalf("second executor did not receive the next committed step:\n%s", secondExecutorPrompt)
	}
	for _, want := range []string{
		"Prior step results",
		"step_index=1",
		"summary=\"candidate\"",
		"Committed plan:",
	} {
		if !strings.Contains(secondExecutorPrompt, want) {
			t.Fatalf("second executor prompt missing prior step context %q:\n%s", want, secondExecutorPrompt)
		}
	}
	if strings.Contains(secondExecutorPrompt, "Current plan:\n1. executor changed plan") {
		t.Fatalf("executor output was treated as a plan mutation:\n%s", secondExecutorPrompt)
	}
}

func TestRoleCollaborativeExecutorShowsCurrentStepForVerifier(t *testing.T) {
	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"use echo"},
			toolCallResponse("call_1", "echo", `{"__arg1":"ok"}`),
			finishStepToolCall("used echo"),
			verifierFinishResponse("done"),
		),
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"echo": &stubTool{name: "echo", description: "Echo.", output: "ok"},
		}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "open Settings and use echo"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) < 5 {
		t.Fatalf("expected verifier model call, got %d calls", len(model.messages))
	}
	verifierSystemPrompt := messageText(model.messages[4][:1])
	verifierUserPrompt := messageText([]llms.MessageContent{model.messages[4][len(model.messages[4])-1]})
	if strings.Contains(verifierSystemPrompt, "## Base instruction") ||
		strings.Contains(verifierSystemPrompt, "## Available skills") {
		t.Fatalf("verifier system prompt should not include base instructions or skills:\n%s", verifierSystemPrompt)
	}
	for _, want := range []string{
		"Original user request",
		"open Settings and use echo",
		"Completion criteria:",
		"Current plan:",
		"Current step under verification:",
		"step_index:",
		"total_committed_steps:",
		"is_final_committed_step:",
		"step_text: use echo",
		"Executor activity for this step:",
		"executor_outcome: finished",
		"tool=echo",
	} {
		if !strings.Contains(verifierUserPrompt, want) {
			t.Fatalf("verifier user prompt missing %q:\n%s", want, verifierUserPrompt)
		}
	}
	for _, unexpected := range []string{
		"Verifier mandatory checklist",
		"Original user request repeated for final verification",
	} {
		if strings.Contains(verifierUserPrompt, unexpected) {
			t.Fatalf("verifier user prompt should not contain %q:\n%s", unexpected, verifierUserPrompt)
		}
	}
}

func TestRoleCollaborativeExecutorReplansAfterRepeatedVerifierFailures(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			enterPlanModeToolCall(),
			commitPlanToolCall("repeat same tool"),
			toolCallResponse("call_1", "echo", `{"__arg1":"same"}`),
			finishStepToolCall("same"),
			verifierContinueResponse("not enough progress"),
			commitPlanToolCall("repeat same tool"),
			toolCallResponse("call_2", "echo", `{"__arg1":"same"}`),
			finishStepToolCall("same again"),
			verifierContinueResponse("still stuck"),
			commitPlanToolCall("answer directly"),
			finishStepToolCall("candidate"),
			verifierFinishResponse("done"),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"echo": &stubTool{name: "echo", description: "Echo.", output: "same result"},
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "do not get stuck",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("unexpected output: %q", result.Output)
	}

	for _, event := range events {
		if event.Type != "role_output" {
			continue
		}
		switch event.Role {
		case string(RolePlanner), string(RoleExecutor), string(RoleVerifier):
		default:
			t.Fatalf("unexpected role output event: %#v", event)
		}
	}
	if model.callCount != 12 {
		t.Fatalf("model call count = %d, want 12", model.callCount)
	}

	secondPlannerPrompt := messageText(model.messages[5])
	for _, want := range []string{
		"Planner runtime context (synthetic; not a new user request):",
		"Current plan:",
		"Prior step results",
		"Verifier feedback:",
		"not enough progress",
		"needs_replan=true",
		"repeat same tool",
	} {
		if !strings.Contains(secondPlannerPrompt, want) {
			t.Fatalf("second planner prompt missing %q:\n%s", want, secondPlannerPrompt)
		}
	}
	if strings.Contains(secondPlannerPrompt, "Executor results:") {
		t.Fatalf("planner runtime context should not duplicate execution history as executor results:\n%s", secondPlannerPrompt)
	}

	secondExecutorMessages := model.messages[7]
	secondExecutorPrompt := messageText(secondExecutorMessages)
	for _, want := range []string{
		"Original user request",
		"do not get stuck",
		"Completion criteria:",
		"Committed plan:",
		"Prior step results",
		"summary=\"same\"",
		"needs_replan=true",
		"verifier_note=\"not enough progress\"",
		"Planner-approved next_step:\nrepeat same tool",
		"Current step progress:",
		"tool=echo input=same",
	} {
		if !strings.Contains(secondExecutorPrompt, want) {
			t.Fatalf("second executor prompt missing %q:\n%s", want, secondExecutorPrompt)
		}
	}
	for _, unexpected := range []string{
		"Current plan:",
		"Verifier feedback:",
	} {
		if strings.Contains(secondExecutorPrompt, unexpected) {
			t.Fatalf("second executor prompt should not contain %q:\n%s", unexpected, secondExecutorPrompt)
		}
	}
	if !hasMessageRole(secondExecutorMessages, llms.ChatMessageTypeTool) {
		t.Fatalf("executor should receive in-step tool scratchpad on follow-up turns: %#v", secondExecutorMessages)
	}
	if strings.Contains(messageText(secondExecutorMessages), "call_1") {
		t.Fatalf("executor should not receive prior step tool scratchpad: %#v", secondExecutorMessages)
	}

	finalVerifierPrompt := messageText(model.messages[11])
	for _, want := range []string{
		"Original user request",
		"do not get stuck",
		"Current plan:",
		"Completion criteria:",
		"Prior step results",
		"Current step under verification:",
		"step_text: answer directly",
		"Executor activity for this step:",
		"executor_summary: candidate",
	} {
		if !strings.Contains(finalVerifierPrompt, want) {
			t.Fatalf("final verifier prompt missing %q:\n%s", want, finalVerifierPrompt)
		}
	}
	for _, unexpected := range []string{
		"Verifier feedback:",
	} {
		if strings.Contains(finalVerifierPrompt, unexpected) {
			t.Fatalf("final verifier prompt should not contain %q:\n%s", unexpected, finalVerifierPrompt)
		}
	}
	if hasMessageRole(model.messages[11], llms.ChatMessageTypeTool) {
		t.Fatalf("verifier should not receive full tool scratchpad history: %#v", model.messages[11])
	}
}

func TestRoleCollaborativeExecutorSharesScreenshotWorldStateOnlyWithVerifier(t *testing.T) {
	jpegBytes := []byte("world-state-jpeg")
	encodedImage := base64.StdEncoding.EncodeToString(jpegBytes)
	imageURL := "data:image/jpeg;base64," + encodedImage
	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"inspect screen", "answer from current screen"},
			toolCallResponse("call_1", "screenshot", `{"__arg1":"{}"}`),
			finishStepToolCall("inspected screen"),
			verifierStepContinueResponse("need act on visible UI"),
			finishStepToolCall("candidate"),
			verifierFinishResponse("done"),
		),
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": &stubTool{
				name:        "screenshot",
				description: "Capture screen.",
				visual:      true,
				output:      `{"width":320,"height":240,"format":"jpeg","size":16,"data":"` + encodedImage + `"}`,
			},
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "inspect and continue"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if model.callCount != 7 {
		t.Fatalf("model call count = %d, want 7", model.callCount)
	}

	for _, idx := range []int{4, 6} {
		prompt := messageText(model.messages[idx])
		for _, want := range []string{
			"World State (shared across planner, executor, and verifier):",
			"Latest screenshot: step=3 source_tool=screenshot size=320x240 format=jpeg bytes=16",
			"The current screenshot image is attached to this message.",
		} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("verifier model call %d missing world-state screenshot text %q:\n%s", idx, want, prompt)
			}
		}
		if got := imageURLCount(model.messages[idx], imageURL); got != 1 {
			t.Fatalf("verifier model call %d should receive one world-state screenshot image, got %d", idx, got)
		}
		if !finalHumanMessageHasTextBeforeImage(model.messages[idx], imageURL) {
			t.Fatalf("verifier model call %d should place text before world-state screenshot image: %#v", idx, model.messages[idx])
		}
	}

	executorPrompt := messageText(model.messages[5])
	for _, unexpected := range []string{
		"Latest screenshot:",
		"The current screenshot image is attached to this message.",
		"Screenshot source input:",
		"Post-action output before screenshot:",
	} {
		if strings.Contains(executorPrompt, unexpected) {
			t.Fatalf("executor should not receive world-state screenshot text %q:\n%s", unexpected, executorPrompt)
		}
	}
	if got := imageURLCount(model.messages[5], imageURL); got != 0 {
		t.Fatalf("executor should not receive world-state screenshot image, got %d copies", got)
	}

	secondExecutorPrompt := messageText(model.messages[5])
	for _, want := range []string{
		"Original user request",
		"inspect and continue",
		"Committed plan:",
	} {
		if !strings.Contains(secondExecutorPrompt, want) {
			t.Fatalf("second executor prompt missing context %q:\n%s", want, secondExecutorPrompt)
		}
	}
	if strings.Contains(secondExecutorPrompt, "Verifier feedback") {
		t.Fatalf("executor should not receive planner-level verifier feedback section:\n%s", secondExecutorPrompt)
	}
	if hasMessageRole(model.messages[5], llms.ChatMessageTypeTool) {
		t.Fatalf("second step executor without tools should not receive step scratchpad: %#v", model.messages[5])
	}
}

func TestRoleCollaborativeExecutorOmitsWorldStateLatestScreenshotWhenLatestExecutorToolResultIsScreenshot(t *testing.T) {
	jpegBytes := []byte("executor-scratchpad-jpeg")
	encodedImage := base64.StdEncoding.EncodeToString(jpegBytes)
	imageURL := "data:image/jpeg;base64," + encodedImage
	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"inspect screen"},
			toolCallResponse("call_1", "screenshot", `{"__arg1":"{}"}`),
			finishStepToolCall("inspected screen"),
			verifierFinishResponse("done"),
		),
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": &stubTool{
				name:        "screenshot",
				description: "Capture screen.",
				visual:      true,
				output:      `{"width":320,"height":240,"format":"jpeg","size":16,"data":"` + encodedImage + `"}`,
			},
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "inspect current screen"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if model.callCount != 5 {
		t.Fatalf("model call count = %d, want 5", model.callCount)
	}

	executorFollowup := model.messages[3]
	prompt := messageText(executorFollowup)
	if !hasMessageRole(executorFollowup, llms.ChatMessageTypeTool) {
		t.Fatalf("executor follow-up should receive current step scratchpad: %#v", executorFollowup)
	}
	if got := imageURLCount(executorFollowup, imageURL); got != 1 {
		t.Fatalf("executor follow-up should include screenshot only from scratchpad, got %d copies", got)
	}
	for _, want := range []string{
		"World State (shared across planner, executor, and verifier):",
		"This image is the screenshot observation returned by the screenshot tool.",
		"Original user request",
		"inspect current screen",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("executor follow-up missing %q:\n%s", want, prompt)
		}
	}
	for _, unexpected := range []string{
		"Latest screenshot:",
		"The current screenshot image is attached to this message.",
		"Screenshot source input:",
	} {
		if strings.Contains(prompt, unexpected) {
			t.Fatalf("executor follow-up should not include world-state screenshot text %q:\n%s", unexpected, prompt)
		}
	}
}

func TestRoleMessagesAttachCurrentStepScreenshotOnlyFromScratchpad(t *testing.T) {
	worldBytes := []byte("stale-world-screenshot")
	stepBytes := []byte("latest-tool-screenshot")
	state := roleLoopState{
		World:         worldState{LatestScreenshot: testWorldScreenshot(worldBytes)},
		StepToolSteps: []schema.AgentStep{testScreenshotObservationStep("keyboard_tap", stepBytes)},
	}
	executor := &roleCollaborativeExecutor{
		Tools: []langtools.Tool{&stubTool{name: "keyboard_tap", visual: true}},
	}

	messages := executor.roleMessages(RoleProfile{Name: RoleExecutor, SystemPrompt: "executor"}, map[string]string{"input": "inspect"}, state, "Executor task.")
	prompt := messageText(messages)
	worldImageURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(worldBytes)
	stepImageURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(stepBytes)

	if strings.Contains(prompt, "Latest screenshot:") ||
		strings.Contains(prompt, "The current screenshot image is attached to this message.") {
		t.Fatalf("executor prompt should omit world latest screenshot when latest tool result is a screenshot:\n%s", prompt)
	}
	if hasImageURL(messages, worldImageURL) {
		t.Fatalf("executor messages should not attach world-state screenshot: %#v", messages)
	}
	if got := imageURLCount(messages, stepImageURL); got != 1 {
		t.Fatalf("executor messages should include latest tool-result screenshot once, got %d", got)
	}
}

func TestRoleMessagesAttachWorldScreenshotOnlyForVerifier(t *testing.T) {
	worldBytes := []byte("verifier-world-screenshot")
	state := roleLoopState{
		World: worldState{LatestScreenshot: testWorldScreenshot(worldBytes)},
	}
	executor := &roleCollaborativeExecutor{}
	imageURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(worldBytes)

	plannerMessages := executor.roleMessages(RoleProfile{Name: RolePlanner, SystemPrompt: "planner"}, map[string]string{"input": "inspect"}, state, "Planner task.")
	executorMessages := executor.roleMessages(RoleProfile{Name: RoleExecutor, SystemPrompt: "executor"}, map[string]string{"input": "inspect"}, state, "Executor task.")
	verifierMessages := executor.roleMessages(RoleProfile{Name: RoleVerifier, SystemPrompt: "verifier"}, map[string]string{"input": "inspect"}, state, "Verifier task.")

	for role, messages := range map[string][]llms.MessageContent{
		"planner":  plannerMessages,
		"executor": executorMessages,
	} {
		prompt := messageText(messages)
		if strings.Contains(prompt, "Latest screenshot:") {
			t.Fatalf("%s should not receive world-state latest screenshot text:\n%s", role, prompt)
		}
		if hasImageURL(messages, imageURL) {
			t.Fatalf("%s should not receive world-state latest screenshot image: %#v", role, messages)
		}
	}

	verifierPrompt := messageText(verifierMessages)
	if !strings.Contains(verifierPrompt, "Latest screenshot: step=1 source_tool=screenshot size=320x240 format=jpeg bytes=25") {
		t.Fatalf("verifier should receive world-state latest screenshot text:\n%s", verifierPrompt)
	}
	if !hasImageURL(verifierMessages, imageURL) {
		t.Fatalf("verifier should receive world-state latest screenshot image: %#v", verifierMessages)
	}
}

func TestWorldStateUpdatesFromPostActionScreenshot(t *testing.T) {
	jpegBytes := []byte("post-action-jpeg")
	state := worldState{}
	state.UpdateFromStep(schema.AgentStep{
		Action: schema.AgentAction{
			Tool:      "keyboard_tap",
			ToolInput: `{"keys":["enter"]}`,
		},
		Observation: `{"action_output":"ok","width":640,"height":480,"format":"jpeg","size":16,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}, 3, []langtools.Tool{&stubTool{name: "keyboard_tap", visual: true}})

	if state.LatestScreenshot == nil {
		t.Fatal("expected world state screenshot")
	}
	if state.LatestScreenshot.SourceTool != "keyboard_tap" ||
		state.LatestScreenshot.ActionOutput != "ok" ||
		state.LatestScreenshot.Width != 640 ||
		state.LatestScreenshot.Height != 480 {
		t.Fatalf("unexpected world screenshot: %#v", state.LatestScreenshot)
	}

	var builder strings.Builder
	writeWorldStateWithOptions(&builder, state, worldStatePromptOptions{IncludeLatestScreenshot: true})
	text := builder.String()
	for _, want := range []string{
		"source_tool=keyboard_tap",
		"size=640x480",
		"Post-action output before screenshot: ok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("verifier world state text missing %q:\n%s", want, text)
		}
	}
}

func TestDefaultWorldStateDoesNotRenderLatestScreenshot(t *testing.T) {
	state := worldState{LatestScreenshot: testWorldScreenshot([]byte("default-hidden-screenshot"))}

	var builder strings.Builder
	writeWorldState(&builder, state)
	text := builder.String()
	if strings.Contains(text, "Latest screenshot:") ||
		strings.Contains(text, "The current screenshot image is attached to this message.") {
		t.Fatalf("world state should not render latest screenshot fields:\n%s", text)
	}
}

func TestWorldStateIgnoresScreenshotShapedObservationFromNonVisualTool(t *testing.T) {
	worldBytes := []byte("previous-world-screenshot")
	state := worldState{LatestScreenshot: testWorldScreenshot(worldBytes)}
	state.UpdateFromStep(testScreenshotObservationStep("metadata_dump", []byte("metadata-image-shaped-payload")), 3, []langtools.Tool{&stubTool{name: "metadata_dump"}})

	if state.LatestScreenshot == nil {
		t.Fatal("expected previous world screenshot to remain")
	}
	if string(state.LatestScreenshot.Data) != string(worldBytes) {
		t.Fatalf("world screenshot was overwritten by non visual tool: %#v", state.LatestScreenshot)
	}
}

func TestExecutionResultLineSummarizesScreenshotObservationWithoutBase64(t *testing.T) {
	image := base64.StdEncoding.EncodeToString([]byte("raw-screenshot-bytes"))
	result := roleExecutionResult{
		Action: &schema.AgentAction{
			Tool:      "screenshot",
			ToolInput: "{}",
		},
		Step: &schema.AgentStep{
			Action:      schema.AgentAction{Tool: "screenshot"},
			Observation: `{"width":320,"height":240,"format":"jpeg","size":20,"data":"` + image + `"}`,
		},
	}

	var builder strings.Builder
	writeExecutionResultLine(&builder, 1, result)
	text := builder.String()
	for _, want := range []string{
		"tool=screenshot input={}",
		"screenshot returned a screenshot observation: format=jpeg width=320 height=240 size=20 bytes",
		"Image data omitted from text summary.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("execution result summary missing %q:\n%s", want, text)
		}
	}
	for _, unexpected := range []string{`"data"`, image} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("execution result summary leaked screenshot payload %q:\n%s", unexpected, text)
		}
	}
}

func TestRoleCollaborativeExecutorUpdatesWorldStateFromObservedState(t *testing.T) {
	jpegBytes := []byte("observed-state-jpeg")
	encodedImage := base64.StdEncoding.EncodeToString(jpegBytes)
	observedVerifier, _ := json.Marshal(map[string]any{
		"can_finish":   false,
		"needs_replan": true,
		"reason":       "need act on observed page",
		"observed_state": map[string]any{
			"app_name":     "微信",
			"page_name":    "聊天列表",
			"platform":     "Android",
			"visible_text": []string{"微信", "通讯录"},
			"dialogs":      []string{"权限提示"},
			"confidence":   0.82,
		},
	})
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			enterPlanModeToolCall(),
			commitPlanToolCall("inspect screen"),
			toolCallResponse("call_1", "screenshot", `{"__arg1":"{}"}`),
			finishStepToolCall("inspected"),
			contentResponse(string(observedVerifier)),
			commitPlanToolCall("answer from observed page"),
			finishStepToolCall("candidate"),
			verifierFinishResponse("done"),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": &stubTool{
				name:        "screenshot",
				description: "Capture screen.",
				visual:      true,
				output:      `{"width":320,"height":240,"format":"jpeg","size":19,"data":"` + encodedImage + `"}`,
			},
		}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "inspect current app"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	secondPlannerMessages := model.messages[5]
	secondPlannerPrompt := messageText([]llms.MessageContent{secondPlannerMessages[len(secondPlannerMessages)-1]})
	for _, want := range []string{
		"Planner runtime context (synthetic; not a new user request):",
		"World State",
		"Observed app/page: 微信 / 聊天列表 platform=android confidence=0.82 source_role=verifier",
		"Visible text: 微信 | 通讯录",
		"Dialogs: 权限提示",
	} {
		if !strings.Contains(secondPlannerPrompt, want) {
			t.Fatalf("second planner prompt missing world state field %q:\n%s", want, secondPlannerPrompt)
		}
	}
	for _, part := range secondPlannerMessages[len(secondPlannerMessages)-1].Parts {
		if _, ok := part.(llms.ImageURLContent); ok {
			t.Fatalf("planner runtime state should be compact text without world-state image: %#v", secondPlannerMessages[len(secondPlannerMessages)-1])
		}
	}

	secondExecutorPrompt := messageText(model.messages[6])
	if !strings.Contains(secondExecutorPrompt, "Observed app/page: 微信 / 聊天列表 platform=android") {
		t.Fatalf("executor should receive structured observed world state:\n%s", secondExecutorPrompt)
	}
	if strings.Contains(secondPlannerPrompt, "screenshot_step=") || strings.Contains(secondExecutorPrompt, "screenshot_step=") {
		t.Fatalf("planner/executor should not receive screenshot_step from verifier-only screenshot state:\nplanner:\n%s\nexecutor:\n%s", secondPlannerPrompt, secondExecutorPrompt)
	}
	finalVerifierPrompt := messageText(model.messages[7])
	if !strings.Contains(finalVerifierPrompt, "screenshot_step=3") {
		t.Fatalf("verifier should receive screenshot_step with verifier-only screenshot state:\n%s", finalVerifierPrompt)
	}
}

func messageText(messages []llms.MessageContent) string {
	var builder strings.Builder
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if text, ok := part.(llms.TextContent); ok {
				builder.WriteString(text.Text)
				builder.WriteByte('\n')
			}
		}
	}
	return builder.String()
}

func testWorldScreenshot(data []byte) *worldScreenshot {
	return &worldScreenshot{
		SourceTool: "screenshot",
		Width:      320,
		Height:     240,
		Format:     "jpeg",
		Size:       len(data),
		Data:       data,
		StepNumber: 1,
	}
}

func testScreenshotObservationStep(tool string, data []byte) schema.AgentStep {
	observation, _ := json.Marshal(postActionScreenshotResult{
		screenshotResult: screenshotResult{
			Width:  320,
			Height: 240,
			Format: "jpeg",
			Size:   len(data),
			Data:   base64.StdEncoding.EncodeToString(data),
		},
	})
	return schema.AgentStep{
		Action:      schema.AgentAction{Tool: tool},
		Observation: string(observation),
	}
}

func hasMessageRole(messages []llms.MessageContent, role llms.ChatMessageType) bool {
	for _, msg := range messages {
		if msg.Role == role {
			return true
		}
	}
	return false
}

func hasImageURL(messages []llms.MessageContent, want string) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			image, ok := part.(llms.ImageURLContent)
			if ok && image.URL == want {
				return true
			}
		}
	}
	return false
}

func imageURLCount(messages []llms.MessageContent, want string) int {
	count := 0
	for _, msg := range messages {
		for _, part := range msg.Parts {
			image, ok := part.(llms.ImageURLContent)
			if ok && image.URL == want {
				count++
			}
		}
	}
	return count
}

func finalHumanMessageHasTextBeforeImage(messages []llms.MessageContent, imageURL string) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != llms.ChatMessageTypeHuman {
			continue
		}
		textIndex := -1
		imageIndex := -1
		for j, part := range messages[i].Parts {
			if _, ok := part.(llms.TextContent); ok && textIndex < 0 {
				textIndex = j
			}
			image, ok := part.(llms.ImageURLContent)
			if ok && image.URL == imageURL && imageIndex < 0 {
				imageIndex = j
			}
		}
		return textIndex >= 0 && imageIndex >= 0 && textIndex < imageIndex
	}
	return false
}

func TestWriteWorldStateIncludesDeviceEnvironment(t *testing.T) {
	state := worldState{}
	isTablet := true
	state.UpdateDeviceEnvironment(&PhoneEnvironment{
		CapturedAt:       "2026-06-15T01:41:43.469Z",
		Source:           "aiden-app",
		Platform:         "android",
		SystemName:       "Android",
		SystemVersion:    "15",
		Locale:           "zh_CN",
		Language:         "zh",
		Region:           "CN",
		TimeZone:         "Asia/Shanghai",
		UTCOffsetMinutes: intPtr(480),
		UTCOffset:        "+08:00",
		IsTablet:         &isTablet,
		Manufacturer:     "LENOVO",
		Brand:            "Lenovo",
		Model:            "TB322FC",
		DeviceName:       "阿兴",
		Screen: PhoneScreenInfo{
			Width:         float64Ptr(692.3636363636),
			Height:        float64Ptr(1105.4545454545),
			WidthPixels:   intPtr(1904),
			HeightPixels:  intPtr(3040),
			Scale:         float64Ptr(2.75),
			Density:       float64Ptr(2.75),
			DensityDPI:    intPtr(440),
			ScaledDensity: float64Ptr(2.75),
		},
		Battery: PhoneBatteryInfo{
			Level:    float64Ptr(0.94),
			Charging: boolPtrRoleProfile(true),
			State:    "charging",
		},
		ThirdPartyApps: []AvailableAppInfo{{Name: "微信", Available: true}},
	})

	var builder strings.Builder
	writeWorldState(&builder, state)
	text := builder.String()
	for _, want := range []string{
		"Device environment: available platform=android system=Android version=15 tablet=true",
		"Device source: aiden-app, captured_at=2026-06-15T01:41:43.469Z",
		"Device locale: zh_CN, language=zh, region=CN, timezone=Asia/Shanghai",
		"Device time: utc_offset=+08:00, utc_offset_minutes=480",
		"Device hardware: manufacturer=LENOVO, brand=Lenovo, model=TB322FC, device_name=阿兴",
		"Device screen: 692.36x1105.45 pt/dp, 1904x3040 px, scale=2.75, density=2.75, density_dpi=440, scaled_density=2.75",
		"Device battery: level=94%, charging=true, state=charging",
		"Confirmed third-party apps: 微信",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("world state text missing %q:\n%s", want, text)
		}
	}
}

func intPtr(v int) *int { return &v }

func float64Ptr(v float64) *float64 { return &v }

func boolPtrRoleProfile(v bool) *bool { return &v }
