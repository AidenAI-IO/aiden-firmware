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
	tools := []langtools.Tool{&stubTool{name: "screenshot", description: "Capture screen."}}

	profiles := buildRoleProfiles(
		AgentConfig{Instruction: "base", AdditionalPrompt: "extra"},
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

	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor, profiles.Verifier} {
		for _, want := range []string{"base", "extra", "[ui] inspect before acting"} {
			if !strings.Contains(profile.SystemPrompt, want) {
				t.Fatalf("%s profile missing %q:\n%s", profile.Name, want, profile.SystemPrompt)
			}
		}
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "MEMORY CONTEXT") {
		t.Fatalf("planner profile should receive memory context:\n%s", profiles.Planner.SystemPrompt)
	}
	if strings.Contains(profiles.Executor.SystemPrompt, "MEMORY CONTEXT") || strings.Contains(profiles.Verifier.SystemPrompt, "MEMORY CONTEXT") {
		t.Fatalf("memory context should be planner-only: executor=%q verifier=%q", profiles.Executor.SystemPrompt, profiles.Verifier.SystemPrompt)
	}
	if !strings.Contains(profiles.Executor.SystemPrompt, "Execute only the current next_step") {
		t.Fatalf("executor prompt missing next_step constraint:\n%s", profiles.Executor.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "Executor tool catalog for planning/review only") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "screenshot: Capture screen.") {
		t.Fatalf("planner prompt should expose executor tool catalog without tool-call authority:\n%s", profiles.Planner.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "If a direct executor tool covers the request") {
		t.Fatalf("planner prompt should prefer direct executor tools:\n%s", profiles.Planner.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "open_app ok=true") ||
		!strings.Contains(profiles.Verifier.SystemPrompt, "open_app returning ok=true") {
		t.Fatalf("role prompts should treat launch-only open_app success as direct evidence: planner=%q verifier=%q", profiles.Planner.SystemPrompt, profiles.Verifier.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "plan quick_action first") ||
		!strings.Contains(profiles.Planner.SystemPrompt, "quick_action action=back platform=android") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "try quick_action first") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "fall back to keyboard_tap") {
		t.Fatalf("role prompts should prefer quick_action before low-level fallback: planner=%q executor=%q", profiles.Planner.SystemPrompt, profiles.Executor.SystemPrompt)
	}
	if !strings.Contains(profiles.Planner.SystemPrompt, "observed_state.platform") ||
		!strings.Contains(profiles.Executor.SystemPrompt, "pass it explicitly") ||
		!strings.Contains(profiles.Verifier.SystemPrompt, `"platform":""`) {
		t.Fatalf("role prompts should propagate observed platform for platform-specific tools: planner=%q executor=%q verifier=%q", profiles.Planner.SystemPrompt, profiles.Executor.SystemPrompt, profiles.Verifier.SystemPrompt)
	}
	if !strings.Contains(profiles.Verifier.SystemPrompt, "only role allowed to decide") {
		t.Fatalf("verifier prompt missing finish authority:\n%s", profiles.Verifier.SystemPrompt)
	}
}

func TestRoleCollaborativeExecutorGivesToolsOnlyToExecutor(t *testing.T) {
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
	if len(model.tools) != 3 {
		t.Fatalf("expected planner/executor/verifier calls, got %d", len(model.tools))
	}
	if len(model.tools[0]) != 0 {
		t.Fatalf("planner received callable tools: %#v", model.tools[0])
	}
	if len(model.tools[1]) != 1 || model.tools[1][0].Function == nil || model.tools[1][0].Function.Name != "audio_volume" {
		t.Fatalf("executor did not receive audio_volume function tool: %#v", model.tools[1])
	}
	if len(model.tools[2]) != 0 {
		t.Fatalf("verifier received callable tools: %#v", model.tools[2])
	}

	plannerPrompt := messageText(model.messages[0])
	if !strings.Contains(plannerPrompt, "audio_volume: Get or set audio playback volume.") {
		t.Fatalf("planner did not see executor tool catalog:\n%s", plannerPrompt)
	}
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
		responses: []*llms.ContentResponse{
			plannerResponse("draft answer"),
			contentResponse("premature candidate"),
			verifierContinueResponse("candidate needs another pass"),
			plannerResponse("produce final candidate"),
			contentResponse("second candidate"),
			verifierFinishResponse("verified final"),
		},
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
		responses: []*llms.ContentResponse{
			plannerResponse("first planner step", "first planner step"),
			contentResponse(`{"plan":["executor changed plan"],"next_step":"bad"}`),
			verifierContinueResponse("executor cannot change the plan"),
			plannerResponse("second planner step", "second planner step"),
			contentResponse("candidate"),
			verifierFinishResponse("done"),
		},
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
	if len(model.messages) < 4 {
		t.Fatalf("expected second planner call, got %d model calls", len(model.messages))
	}
	executorPrompt := messageText(model.messages[1])
	if !strings.Contains(executorPrompt, "Planner-approved next_step:\nfirst planner step") {
		t.Fatalf("executor did not receive the planner-approved next_step:\n%s", executorPrompt)
	}
	if strings.Contains(executorPrompt, "Current plan:\n") {
		t.Fatalf("executor should not receive the full plan:\n%s", executorPrompt)
	}
	secondPlannerPrompt := messageText(model.messages[3])
	if !strings.Contains(secondPlannerPrompt, "Current plan:\n1. first planner step") {
		t.Fatalf("second planner did not receive planner-owned plan:\n%s", secondPlannerPrompt)
	}
	if strings.Contains(secondPlannerPrompt, "Current plan:\n1. executor changed plan") {
		t.Fatalf("executor output was treated as a plan mutation:\n%s", secondPlannerPrompt)
	}
}

func TestRoleCollaborativeExecutorRepeatsOriginalRequestForVerifier(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("echo", `{"__arg1":"ok"}`, "done"),
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

	const request = "open Settings, search Bluetooth, and stop only after Bluetooth settings is visible"
	if _, err := runtime.Run(context.Background(), RunRequest{Input: request}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) < 3 {
		t.Fatalf("expected verifier model call, got %d calls", len(model.messages))
	}
	verifierMessages := model.messages[2]
	lastMessageText := messageText([]llms.MessageContent{verifierMessages[len(verifierMessages)-1]})
	for _, want := range []string{
		"Verifier mandatory checklist",
		"Original user request repeated for final verification",
		request,
		"Completion criteria",
		"Do not finish merely because the latest executor step succeeded",
	} {
		if !strings.Contains(lastMessageText, want) {
			t.Fatalf("verifier final prompt missing %q:\n%s", want, lastMessageText)
		}
	}
}

func TestRoleCollaborativeExecutorReplansAfterRepeatedVerifierFailures(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			plannerResponse("repeat same tool"),
			toolCallResponse("call_1", "echo", `{"__arg1":"same"}`),
			verifierContinueResponse("not enough progress"),
			plannerResponse("repeat same tool"),
			toolCallResponse("call_2", "echo", `{"__arg1":"same"}`),
			verifierContinueResponse("still stuck"),
			plannerResponse("answer directly"),
			contentResponse("candidate"),
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
	if model.callCount != 9 {
		t.Fatalf("model call count = %d, want 9", model.callCount)
	}

	secondPlannerPrompt := messageText(model.messages[3])
	for _, want := range []string{
		"Current plan:",
		"Executor results:",
		"Verifier feedback:",
		"not enough progress",
	} {
		if !strings.Contains(secondPlannerPrompt, want) {
			t.Fatalf("second planner prompt missing %q:\n%s", want, secondPlannerPrompt)
		}
	}

	secondExecutorMessages := model.messages[4]
	secondExecutorPrompt := messageText(secondExecutorMessages)
	for _, want := range []string{
		"Planner-approved next_step:\nrepeat same tool",
		"Local execution context (latest prior result only):",
		"tool=echo input=same",
	} {
		if !strings.Contains(secondExecutorPrompt, want) {
			t.Fatalf("second executor prompt missing %q:\n%s", want, secondExecutorPrompt)
		}
	}
	for _, unexpected := range []string{
		"do not get stuck",
		"Current plan:",
		"Completion criteria:",
		"Verifier feedback:",
		"not enough progress",
	} {
		if strings.Contains(secondExecutorPrompt, unexpected) {
			t.Fatalf("second executor prompt should not contain %q:\n%s", unexpected, secondExecutorPrompt)
		}
	}
	if hasMessageRole(secondExecutorMessages, llms.ChatMessageTypeTool) {
		t.Fatalf("executor should not receive prior tool scratchpad messages: %#v", secondExecutorMessages)
	}

	finalVerifierPrompt := messageText(model.messages[8])
	for _, want := range []string{
		"Original user request",
		"do not get stuck",
		"Executor evidence:",
		"tool=echo input=same",
		"candidate_answer=candidate",
	} {
		if !strings.Contains(finalVerifierPrompt, want) {
			t.Fatalf("final verifier prompt missing %q:\n%s", want, finalVerifierPrompt)
		}
	}
	for _, unexpected := range []string{
		"Current plan:",
		"Planner reason:",
		"Verifier feedback:",
		"not enough progress",
		"still stuck",
	} {
		if strings.Contains(finalVerifierPrompt, unexpected) {
			t.Fatalf("final verifier prompt should not contain %q:\n%s", unexpected, finalVerifierPrompt)
		}
	}
	if !hasMessageRole(model.messages[8], llms.ChatMessageTypeTool) {
		t.Fatalf("verifier should receive tool scratchpad messages as operation evidence: %#v", model.messages[8])
	}
}

func TestRoleCollaborativeExecutorSharesScreenshotWorldStateAcrossRoles(t *testing.T) {
	jpegBytes := []byte("world-state-jpeg")
	encodedImage := base64.StdEncoding.EncodeToString(jpegBytes)
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			plannerResponse("inspect screen"),
			toolCallResponse("call_1", "screenshot", `{"__arg1":"{}"}`),
			verifierContinueResponse("need act on visible UI"),
			plannerResponse("answer from current screen"),
			contentResponse("candidate"),
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
	if model.callCount != 6 {
		t.Fatalf("model call count = %d, want 6", model.callCount)
	}

	for _, idx := range []int{2, 3, 4, 5} {
		prompt := messageText(model.messages[idx])
		for _, want := range []string{
			"World State (shared across planner, executor, and verifier):",
			"Latest screenshot: step=1 source_tool=screenshot size=320x240 format=jpeg bytes=16",
			"The current screenshot image is attached to this message.",
		} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("model call %d missing world state %q:\n%s", idx, want, prompt)
			}
		}
		if !hasImageURL(model.messages[idx], "data:image/jpeg;base64,"+encodedImage) {
			t.Fatalf("model call %d missing world-state screenshot image: %#v", idx, model.messages[idx])
		}
		if !finalHumanMessageHasTextBeforeImage(model.messages[idx], "data:image/jpeg;base64,"+encodedImage) {
			t.Fatalf("model call %d should place text before world-state screenshot image: %#v", idx, model.messages[idx])
		}
	}

	secondExecutorPrompt := messageText(model.messages[4])
	if strings.Contains(secondExecutorPrompt, "Original user request") || strings.Contains(secondExecutorPrompt, "Verifier feedback") {
		t.Fatalf("executor should see world state without broader planning context:\n%s", secondExecutorPrompt)
	}
	if hasMessageRole(model.messages[4], llms.ChatMessageTypeTool) {
		t.Fatalf("executor should receive world-state screenshot without prior tool scratchpad: %#v", model.messages[4])
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
	}, 3)

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
	writeWorldState(&builder, state)
	text := builder.String()
	for _, want := range []string{
		"source_tool=keyboard_tap",
		"size=640x480",
		"Post-action output before screenshot: ok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("world state text missing %q:\n%s", want, text)
		}
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
			plannerResponse("inspect screen"),
			toolCallResponse("call_1", "screenshot", `{"__arg1":"{}"}`),
			contentResponse(string(observedVerifier)),
			plannerResponse("answer from observed page"),
			contentResponse("candidate"),
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

	secondPlannerPrompt := messageText(model.messages[3])
	for _, want := range []string{
		"Observed app/page: 微信 / 聊天列表 platform=android confidence=0.82 source_role=verifier screenshot_step=1",
		"Visible text: 微信 | 通讯录",
		"Dialogs: 权限提示",
	} {
		if !strings.Contains(secondPlannerPrompt, want) {
			t.Fatalf("second planner prompt missing %q:\n%s", want, secondPlannerPrompt)
		}
	}

	secondExecutorPrompt := messageText(model.messages[4])
	if !strings.Contains(secondExecutorPrompt, "Observed app/page: 微信 / 聊天列表 platform=android") {
		t.Fatalf("executor should receive structured observed world state:\n%s", secondExecutorPrompt)
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
