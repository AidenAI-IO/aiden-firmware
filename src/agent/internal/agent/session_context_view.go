package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/schema"
)

const (
	sessionContextInputKey = "session_context"
	rootRequestInputKey    = "root_request"
	latestUserInputKey     = "latest_user_message"
)

const maxSessionContextEvents = 200

type sessionContextView struct {
	RootUserRequest       string
	LatestUserMessage     string
	LatestCommittedPlan   *plannerDecision
	LatestVerifierSummary string
}

type sessionContextPlannerMemory struct {
	inner     schema.Memory
	manager   *MemoryManager
	agentName string
}

func newSessionContextPlannerMemory(inner schema.Memory, manager *MemoryManager, agentName string) schema.Memory {
	if inner == nil || manager == nil || strings.TrimSpace(manager.storageDir) == "" {
		return inner
	}
	if strings.TrimSpace(agentName) == "" {
		agentName = "default"
	}
	return &sessionContextPlannerMemory{inner: inner, manager: manager, agentName: agentName}
}

func (m *sessionContextPlannerMemory) GetMemoryKey(ctx context.Context) string {
	return m.inner.GetMemoryKey(ctx)
}

func (m *sessionContextPlannerMemory) MemoryVariables(ctx context.Context) []string {
	variables := append([]string(nil), m.inner.MemoryVariables(ctx)...)
	for _, key := range []string{sessionContextInputKey, rootRequestInputKey, latestUserInputKey} {
		if !slicesContainsString(variables, key) {
			variables = append(variables, key)
		}
	}
	return variables
}

func (m *sessionContextPlannerMemory) LoadMemoryVariables(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	values, err := m.inner.LoadMemoryVariables(ctx, inputs)
	if err != nil {
		return nil, err
	}
	events, err := m.manager.LoadActiveSessionEvents(ctx, maxSessionContextEvents)
	if err != nil {
		return nil, err
	}
	view := BuildSessionContextView(events, currentInputFromMemoryInputs(inputs))
	rendered := formatSessionContextView(view)
	if strings.TrimSpace(rendered) == "" {
		return values, nil
	}
	if values == nil {
		values = map[string]any{}
	}
	values[sessionContextInputKey] = rendered
	if view.RootUserRequest != "" {
		values[rootRequestInputKey] = view.RootUserRequest
	}
	if view.LatestUserMessage != "" {
		values[latestUserInputKey] = view.LatestUserMessage
	}
	return values, nil
}

func (m *sessionContextPlannerMemory) SaveContext(ctx context.Context, inputs map[string]any, outputs map[string]any) error {
	return m.inner.SaveContext(ctx, inputs, outputs)
}

func (m *sessionContextPlannerMemory) Clear(ctx context.Context) error {
	return m.inner.Clear(ctx)
}

func currentInputFromMemoryInputs(inputs map[string]any) string {
	if inputs == nil {
		return ""
	}
	value, ok := inputs["input"]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func BuildSessionContextView(events []SessionEvent, currentInput string) sessionContextView {
	currentInput = strings.TrimSpace(currentInput)
	events = runtimeSessionContextEvents(events)
	if len(events) == 0 && currentInput == "" {
		return sessionContextView{}
	}
	latestUserIndex := latestUserEventIndex(events, currentInput)
	var latestUser SessionEvent
	if latestUserIndex >= 0 {
		latestUser = events[latestUserIndex]
	}
	latestContent := strings.TrimSpace(latestUser.Content)
	if latestContent == "" {
		latestContent = currentInput
	}

	root := latestContent
	rootIndex := latestUserIndex
	if sessionRoot, index := activeSessionRootUserInput(events, latestUserIndex); sessionRoot != "" {
		root = sessionRoot
		rootIndex = index
	}
	view := sessionContextView{
		RootUserRequest:   root,
		LatestUserMessage: latestContent,
	}
	if decision, ok := latestCommittedPlan(events, rootIndex, latestUserIndex); ok {
		view.LatestCommittedPlan = &decision
	}
	if summary := latestVerifierSummary(events, rootIndex, latestUserIndex); summary != "" {
		view.LatestVerifierSummary = summary
	}
	return view
}

func runtimeSessionContextEvents(events []SessionEvent) []SessionEvent {
	hasRuntimeEvents := false
	for _, event := range events {
		if strings.TrimSpace(event.RunID) != "" {
			hasRuntimeEvents = true
			break
		}
	}
	if !hasRuntimeEvents {
		return events
	}
	scoped := make([]SessionEvent, 0, len(events))
	for _, event := range events {
		if strings.TrimSpace(event.RunID) != "" {
			scoped = append(scoped, event)
		}
	}
	return scoped
}

func latestUserEventIndex(events []SessionEvent, currentInput string) int {
	currentInput = strings.TrimSpace(currentInput)
	if currentInput != "" {
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type == "user_input" && strings.TrimSpace(events[i].Content) == currentInput {
				return i
			}
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "user_input" {
			return i
		}
	}
	return -1
}

func activeSessionRootUserInput(events []SessionEvent, latestUserIndex int) (string, int) {
	limit := len(events)
	if latestUserIndex >= 0 && latestUserIndex < limit {
		limit = latestUserIndex + 1
	}
	for i := 0; i < limit; i++ {
		event := events[i]
		if event.Type != "user_input" {
			continue
		}
		content := strings.TrimSpace(event.Content)
		if content != "" {
			return content, i
		}
	}
	return "", -1
}

func sessionDecisionScanWindow(events []SessionEvent, rootIndex, latestUserIndex int) (int, int) {
	limit := latestUserIndex
	if limit < 0 || limit > len(events) {
		limit = len(events)
	}
	start := 0
	if rootIndex >= 0 && rootIndex < len(events) {
		start = rootIndex + 1
	}
	if start > limit {
		start = limit
	}
	return start, limit
}

func latestCommittedPlan(events []SessionEvent, rootIndex, latestUserIndex int) (plannerDecision, bool) {
	start, limit := sessionDecisionScanWindow(events, rootIndex, latestUserIndex)
	for i := limit - 1; i >= start; i-- {
		event := events[i]
		if event.Type != "planner_decision" {
			continue
		}
		if decision, ok := plannerDecisionFromSessionEvent(event); ok {
			return decision, true
		}
	}
	return plannerDecision{}, false
}

func plannerDecisionFromSessionEvent(event SessionEvent) (plannerDecision, bool) {
	decision := plannerDecision{
		Objective:          strings.TrimSpace(event.Objective),
		CompletionCriteria: uniqueNonEmpty(event.CompletionCriteria),
		Plan:               uniqueNonEmpty(event.Plan),
		NextStep:           strings.TrimSpace(event.NextStep),
		Reason:             strings.TrimSpace(event.Reason),
	}
	if len(decision.Plan) == 0 && strings.TrimSpace(event.Content) != "" {
		var payload plannerDecision
		if err := json.Unmarshal([]byte(event.Content), &payload); err == nil {
			decision = payload
			decision.Objective = strings.TrimSpace(decision.Objective)
			decision.CompletionCriteria = uniqueNonEmpty(decision.CompletionCriteria)
			decision.Plan = uniqueNonEmpty(decision.Plan)
			decision.NextStep = strings.TrimSpace(decision.NextStep)
			decision.Reason = strings.TrimSpace(decision.Reason)
		}
	}
	return decision, len(decision.Plan) > 0 || decision.Objective != "" || decision.NextStep != ""
}

func latestVerifierSummary(events []SessionEvent, rootIndex, latestUserIndex int) string {
	start, limit := sessionDecisionScanWindow(events, rootIndex, latestUserIndex)
	for i := limit - 1; i >= start; i-- {
		event := events[i]
		if event.Type != "verifier_decision" {
			continue
		}
		var parts []string
		if event.CanFinish != nil {
			parts = append(parts, fmt.Sprintf("can_finish=%t", *event.CanFinish))
		}
		if event.NeedsReplan {
			parts = append(parts, "needs_replan=true")
		}
		if reason := strings.TrimSpace(event.Reason); reason != "" {
			parts = append(parts, "reason="+singleLineHistoryText(reason))
		}
		if content := strings.TrimSpace(event.Content); content != "" {
			parts = append(parts, "content="+singleLineHistoryText(content))
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func formatSessionContextView(view sessionContextView) string {
	if strings.TrimSpace(view.LatestUserMessage) == "" &&
		view.LatestCommittedPlan == nil &&
		strings.TrimSpace(view.LatestVerifierSummary) == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Session context view:\n")
	if latest := strings.TrimSpace(view.LatestUserMessage); latest != "" {
		builder.WriteString("- Latest user message: ")
		builder.WriteString(singleLineHistoryText(latest))
		builder.WriteByte('\n')
	}
	if view.LatestCommittedPlan != nil {
		decision := view.LatestCommittedPlan
		builder.WriteString("\nLatest committed plan (runtime-accepted):\n")
		if objective := strings.TrimSpace(decision.Objective); objective != "" {
			builder.WriteString("- Objective: ")
			builder.WriteString(singleLineHistoryText(objective))
			builder.WriteByte('\n')
		}
		if len(decision.CompletionCriteria) > 0 {
			builder.WriteString("- Completion criteria: ")
			builder.WriteString(compactStringList(decision.CompletionCriteria, 480))
			builder.WriteByte('\n')
		}
		for i, step := range decision.Plan {
			if step = strings.TrimSpace(step); step != "" {
				builder.WriteString(fmt.Sprintf("%d. %s\n", i+1, singleLineHistoryText(step)))
			}
		}
	}
	if summary := strings.TrimSpace(view.LatestVerifierSummary); summary != "" {
		builder.WriteString("\nLatest verifier decision:\n")
		builder.WriteString("- ")
		builder.WriteString(summary)
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

func slicesContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
