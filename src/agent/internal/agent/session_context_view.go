package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tmc/langchaingo/schema"
)

const (
	FollowUpRootRequest  = "root_request"
	FollowUpContinuation = "continuation"
	FollowUpCorrection   = "correction"
	FollowUpReplacement  = "replacement"
	FollowUpNewTask      = "new_task"

	sessionContextInputKey = "session_context"
	rootRequestInputKey    = "root_request"
	latestUserInputKey     = "latest_user_message"
	followUpRelationKey    = "follow_up_relation"
)

const maxSessionContextEvents = 200

var (
	correctionCueRe  = regexp.MustCompile(`(?i)(听错|看错|不是|应该是|是\s*[^，。,.!?！？]+$|改成|改为|更正|纠正|说错|写错|typo|correction|correct|actually|should be|not .+ but)`)
	replacementCueRe = regexp.MustCompile(`(?i)^\s*(取消|不用|别|先别|算了|重新|从头|换成|改为新的|replace|instead|start over|new task)\b?`)
)

type sessionContextView struct {
	RootUserRequest       string
	LatestUserMessage     string
	FollowUpRelation      string
	Interpretation        string
	LatestCommittedPlan   *plannerDecision
	LatestVerifierSummary string
	LatestCorrection      string
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
	for _, key := range []string{sessionContextInputKey, rootRequestInputKey, latestUserInputKey, followUpRelationKey} {
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
	if view.FollowUpRelation != "" {
		values[followUpRelationKey] = view.FollowUpRelation
	}
	return values, nil
}

func (m *sessionContextPlannerMemory) SaveContext(ctx context.Context, inputs map[string]any, outputs map[string]any) error {
	return m.inner.SaveContext(ctx, inputs, outputs)
}

func (m *sessionContextPlannerMemory) Clear(ctx context.Context) error {
	return m.inner.Clear(ctx)
}

func ClassifyFollowUpRelation(prevEvents []SessionEvent, input string, boundary string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return FollowUpRootRequest
	}
	if boundary == BoundaryNew || len(prevEvents) == 0 {
		return FollowUpRootRequest
	}
	if replacementCueRe.MatchString(trimmed) {
		return FollowUpReplacement
	}
	if correctionCueRe.MatchString(trimmed) {
		return FollowUpCorrection
	}
	if actionVerbStartRe.MatchString(trimmed) && !continuationMarkerRe.MatchString(trimmed) {
		return FollowUpNewTask
	}
	return FollowUpContinuation
}

func BuildSessionContextView(events []SessionEvent, currentInput string) sessionContextView {
	currentInput = strings.TrimSpace(currentInput)
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
	relation := strings.TrimSpace(latestUser.Relation)
	if relation == "" {
		relation = ClassifyFollowUpRelation(eventsBefore(events, latestUserIndex), latestContent, BoundaryContinue)
	}
	if relation == "" {
		relation = FollowUpRootRequest
	}

	root := latestContent
	taskRootIndex := latestUserIndex
	if relation != FollowUpReplacement && relation != FollowUpNewTask {
		if taskRoot, index := taskRootUserInput(events, latestUserIndex); taskRoot != "" {
			root = taskRoot
			taskRootIndex = index
		}
	}
	view := sessionContextView{
		RootUserRequest:   root,
		LatestUserMessage: latestContent,
		FollowUpRelation:  relation,
		Interpretation:    interpretationForFollowUpRelation(relation),
	}
	if relation == FollowUpCorrection {
		view.LatestCorrection = latestContent
	}
	if relation != FollowUpReplacement && relation != FollowUpNewTask {
		if decision, ok := latestCommittedPlan(events, taskRootIndex, latestUserIndex); ok {
			view.LatestCommittedPlan = &decision
		}
		if summary := latestVerifierSummary(events, taskRootIndex, latestUserIndex); summary != "" {
			view.LatestVerifierSummary = summary
		}
	}
	return view
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

func eventsBefore(events []SessionEvent, index int) []SessionEvent {
	if index <= 0 || index > len(events) {
		return nil
	}
	return events[:index]
}

func taskRootUserInput(events []SessionEvent, latestUserIndex int) (string, int) {
	limit := len(events)
	if latestUserIndex >= 0 && latestUserIndex < limit {
		limit = latestUserIndex + 1
	}
	for i := limit - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != "user_input" {
			continue
		}
		content := strings.TrimSpace(event.Content)
		if content == "" {
			continue
		}
		relation := strings.TrimSpace(event.Relation)
		switch relation {
		case FollowUpRootRequest, FollowUpNewTask, FollowUpReplacement:
			return content, i
		}
	}
	for i := 0; i < limit; i++ {
		if events[i].Type == "user_input" {
			if content := strings.TrimSpace(events[i].Content); content != "" {
				return content, i
			}
		}
	}
	return "", -1
}

func latestCommittedPlan(events []SessionEvent, taskRootIndex, latestUserIndex int) (plannerDecision, bool) {
	limit := latestUserIndex
	if limit < 0 || limit > len(events) {
		limit = len(events)
	}
	start := 0
	if taskRootIndex >= 0 && taskRootIndex < limit {
		start = taskRootIndex + 1
	}
	for i := limit - 1; i >= 0; i-- {
		if i < start {
			break
		}
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

func latestVerifierSummary(events []SessionEvent, taskRootIndex, latestUserIndex int) string {
	limit := latestUserIndex
	if limit < 0 || limit > len(events) {
		limit = len(events)
	}
	start := 0
	if taskRootIndex >= 0 && taskRootIndex < limit {
		start = taskRootIndex + 1
	}
	for i := limit - 1; i >= 0; i-- {
		if i < start {
			break
		}
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

func interpretationForFollowUpRelation(relation string) string {
	switch relation {
	case FollowUpCorrection:
		return "treat the latest user message as a correction to the root request unless it explicitly replaces the task"
	case FollowUpReplacement:
		return "treat the latest user message as an explicit replacement and do not continue the previous task"
	case FollowUpNewTask:
		return "treat the latest user message as a new task; prior session context is lower priority"
	case FollowUpContinuation:
		return "continue the existing task using the root request as the authority"
	default:
		return "treat this as the root request"
	}
}

func formatSessionContextView(view sessionContextView) string {
	if strings.TrimSpace(view.RootUserRequest) == "" && strings.TrimSpace(view.LatestUserMessage) == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Session context view (runtime-normalized, priority ordered):\n")
	if root := strings.TrimSpace(view.RootUserRequest); root != "" {
		builder.WriteString("- Root request: ")
		builder.WriteString(singleLineHistoryText(root))
		builder.WriteByte('\n')
	}
	if latest := strings.TrimSpace(view.LatestUserMessage); latest != "" {
		builder.WriteString("- Latest user message: ")
		builder.WriteString(singleLineHistoryText(latest))
		builder.WriteByte('\n')
	}
	if relation := strings.TrimSpace(view.FollowUpRelation); relation != "" {
		builder.WriteString("- Follow-up classification: ")
		builder.WriteString(relation)
		builder.WriteByte('\n')
	}
	if interpretation := strings.TrimSpace(view.Interpretation); interpretation != "" {
		builder.WriteString("- Interpretation: ")
		builder.WriteString(interpretation)
		builder.WriteByte('\n')
	}
	if correction := strings.TrimSpace(view.LatestCorrection); correction != "" {
		builder.WriteString("- Latest correction: ")
		builder.WriteString(singleLineHistoryText(correction))
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
	builder.WriteString("\nContext priority:\n")
	builder.WriteString("- explicit cancellation or replacement > latest correction > root request > planner inference\n")
	builder.WriteString("- verified evidence > unverified role_output\n")
	builder.WriteString("- committed plan > ordinary chat summary\n")
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
