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
	correctionExplicitCueRe        = regexp.MustCompile(`(?i)(听错|看错|说错|写错|更正|纠正|不对|应该是|应当是|应该为|应为|\btypo\b|\bcorrection\b|\bcorrect\b|\bactually\b|\bshould be\b)`)
	correctionContrastCueRe        = regexp.MustCompile(`(?i)(^|[\s,，。.!?！？])不是[^，。,.!?！？]+[\s,，。.!?！？]*(是|而是)[^，。,.!?！？]+`)
	correctionEnglishContrastCueRe = regexp.MustCompile(`(?i)\bnot\s+[^,.!?]+[\s,]+but\s+[^,.!?]+`)
	replacementCueRe               = regexp.MustCompile(`(?i)^[\s,，。.!?！？]*(算了|重新|从头|换成|改为新的|replace\b|instead\b|start over\b|new task\b)`)
	continuationReferenceRe        = regexp.MustCompile(`(?i)(刚才|刚刚|上一个|上一条|前面|之前|这个|那个|它|同一个|\b(it|this|that|same|previous)\b|\bthe\s+last\b|\blast\s+(one|message|page|thing|item)\b)`)
	independentCancelActionRe      = regexp.MustCompile(`(?i)^[\s,，。.!?！？]*(取消|撤销|cancel\b)`)
)

type sessionContextView struct {
	RootUserRequest       string
	LatestUserMessage     string
	FollowUpRelation      string
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

func normalizeFollowUpRelation(relation string) string {
	switch strings.TrimSpace(relation) {
	case FollowUpRootRequest:
		return FollowUpRootRequest
	case FollowUpContinuation:
		return FollowUpContinuation
	case FollowUpCorrection:
		return FollowUpCorrection
	case FollowUpReplacement:
		return FollowUpReplacement
	case FollowUpNewTask:
		return FollowUpNewTask
	default:
		return ""
	}
}

func ClassifyFollowUpRelation(prevEvents []SessionEvent, input string, boundary string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return FollowUpRootRequest
	}
	if boundary == BoundaryNew || !hasPreviousUserInput(prevEvents) {
		return FollowUpRootRequest
	}
	if isReplacementFollowUp(trimmed) {
		return FollowUpReplacement
	}
	if isCorrectionFollowUp(trimmed) {
		return FollowUpCorrection
	}
	if hasContinuationReference(trimmed) {
		return FollowUpContinuation
	}
	if actionVerbStartRe.MatchString(trimmed) || independentCancelActionRe.MatchString(trimmed) {
		return FollowUpNewTask
	}
	return FollowUpContinuation
}

func hasPreviousUserInput(events []SessionEvent) bool {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type == "user_input" && strings.TrimSpace(event.Content) != "" {
			return true
		}
	}
	return false
}

func isReplacementFollowUp(input string) bool {
	if replacementCueRe.MatchString(input) {
		return true
	}
	trimmed := strings.TrimLeft(input, " \t\r\n,，。.!?！？")
	for _, prefix := range []string{"不用", "先别", "别"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	if rest, ok := strings.CutPrefix(trimmed, "取消"); ok {
		return isContextualCancelTarget(rest)
	}
	lower := strings.ToLower(trimmed)
	if rest, ok := strings.CutPrefix(lower, "cancel"); ok {
		return isContextualCancelTarget(rest)
	}
	return false
}

func isContextualCancelTarget(target string) bool {
	target = strings.TrimLeft(target, " \t\r\n,，。.!?！？")
	if target == "" {
		return true
	}
	for _, prefix := range []string{
		"掉", "了", "吧", "这个", "那个", "它", "刚才", "刚刚", "上一个", "上一条", "前面", "之前", "当前", "这次", "本次",
		"it", "this", "that", "same", "previous", "the previous", "the last",
		"last one", "last message", "last page", "last thing", "last item",
	} {
		if strings.HasPrefix(target, prefix) {
			return true
		}
	}
	return false
}

func isCorrectionFollowUp(input string) bool {
	if correctionExplicitCueRe.MatchString(input) ||
		correctionContrastCueRe.MatchString(input) ||
		correctionEnglishContrastCueRe.MatchString(input) {
		return true
	}
	trimmed := strings.TrimLeft(input, " \t\r\n,，。.!?！？")
	for _, prefix := range []string{"是", "改成", "改为", "更正为", "纠正为"} {
		if rest, ok := strings.CutPrefix(trimmed, prefix); ok {
			return looksLikeCorrectionValue(rest)
		}
	}
	return false
}

func looksLikeCorrectionValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch strings.ToLower(strings.Trim(value, " \t\r\n,，。.!?！？")) {
	case "的", "的吧", "吧", "吗", "不是", "not":
		return false
	}
	if strings.HasPrefix(value, "不是") {
		return false
	}
	return len([]rune(value)) >= 2
}

func hasContinuationReference(input string) bool {
	return continuationMarkerRe.MatchString(input) || continuationReferenceRe.MatchString(input)
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

func formatSessionContextView(view sessionContextView) string {
	if strings.TrimSpace(view.LatestUserMessage) == "" &&
		strings.TrimSpace(view.FollowUpRelation) == "" &&
		strings.TrimSpace(view.LatestCorrection) == "" &&
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
	if relation := strings.TrimSpace(view.FollowUpRelation); relation != "" {
		builder.WriteString("- Follow-up classification: ")
		builder.WriteString(relation)
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
