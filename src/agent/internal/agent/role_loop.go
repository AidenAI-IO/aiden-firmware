package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tmc/langchaingo/schema"
)

type loopPhase string

const (
	phaseDecision  loopPhase = "decision"
	phaseDefault   loopPhase = "default"
	phasePlan      loopPhase = "plan"
	phaseExecution loopPhase = "execution"
)

const defaultCompletionCriterion = "Satisfy the current user request."

type plannerTurnKind int

const (
	plannerTurnTool plannerTurnKind = iota
	plannerTurnFinish
	plannerTurnEnterPlan
	plannerTurnUseSimpleMode
	plannerTurnCommitPlan
	plannerTurnSetTodo
	plannerTurnCancelPlan
	plannerTurnInvalidMeta
	plannerTurnSleep
	plannerTurnSteer
)

type plannerTurnResult struct {
	Kind               plannerTurnKind
	Answer             string
	Step               *schema.AgentStep
	CommittedPlan      plannerDecision
	Todo               TodoState
	TodoSpeechEligible bool
	InvalidMetaStep    *schema.AgentStep
}

type routeMode string

const (
	routeModeDirectAnswer routeMode = "direct_answer"
	routeModeSimple       routeMode = "simple"
	routeModePlan         routeMode = "plan"
)

type routeDecision struct {
	Mode        routeMode `json:"mode"`
	FinalAnswer string    `json:"final_answer,omitempty"`
	Speech      string    `json:"speech,omitempty"`
	Text        string    `json:"text,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Confidence  float64   `json:"confidence,omitempty"`
}

type executorTurnKind int

const (
	executorTurnTool executorTurnKind = iota
	executorTurnFinishStep
	executorTurnAbortStep
	executorTurnInvalidMeta
	executorTurnSleep
	executorTurnSteer
)

const (
	verifierStepStatusSucceeded = "succeeded"
	verifierStepStatusFailed    = "failed"
	verifierStepStatusBlocked   = "blocked"
	verifierStepStatusUncertain = "uncertain"
)

type executorTurnResult struct {
	Kind            executorTurnKind
	Action          *schema.AgentAction
	Step            *schema.AgentStep
	InvalidMetaStep *schema.AgentStep
	ToolError       *ToolError
}

func normalizeVerifierStepStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	status = strings.ReplaceAll(status, "-", "_")
	status = strings.ReplaceAll(status, " ", "_")
	switch status {
	case verifierStepStatusSucceeded:
		return verifierStepStatusSucceeded
	case verifierStepStatusFailed:
		return verifierStepStatusFailed
	case verifierStepStatusBlocked:
		return verifierStepStatusBlocked
	case verifierStepStatusUncertain:
		return verifierStepStatusUncertain
	default:
		return ""
	}
}

func plannerCommitRequiredTurn(action schema.AgentAction) plannerTurnResult {
	if strings.TrimSpace(action.Tool) == "" {
		action.Tool = toolCommitPlan
	}
	return plannerTurnResult{
		Kind: plannerTurnInvalidMeta,
		Step: &schema.AgentStep{
			Action:      action,
			Observation: "plan mode requires commit_plan before delegated execution. You may use read-only information-gathering tools if more context is required, otherwise call commit_plan with a compact delegated plan. Do not use execution or state-changing tools in plan mode.",
		},
	}
}

func plannerPlanModeToolRejectedTurn(action schema.AgentAction) plannerTurnResult {
	if strings.TrimSpace(action.Tool) == "" {
		action.Tool = toolCommitPlan
	}
	return plannerTurnResult{
		Kind: plannerTurnInvalidMeta,
		Step: &schema.AgentStep{
			Action:      action,
			Observation: "plan mode can only use loop meta tools and read-only information-gathering tools before commit_plan. Put execution, computation, and state-changing work into committed executor steps.",
		},
	}
}

func toolNameEqual(got, want string) bool {
	return strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want))
}

func isWaitForWakeupTool(name string) bool {
	return toolNameEqual(name, toolWaitForWakeup)
}

func isHumanHandoffTool(name string) bool {
	return toolNameEqual(name, toolHumanHandoffStep)
}

func isRunPausingTool(name string) bool {
	return isWaitForWakeupTool(name) || isHumanHandoffTool(name)
}

func runPausingToolFinalAnswer(step *schema.AgentStep) string {
	if step != nil && isHumanHandoffTool(step.Action.Tool) {
		return humanHandoffFinalAnswer(step)
	}
	return waitForWakeupFinalAnswer(step)
}

func humanHandoffFinalAnswer(step *schema.AgentStep) string {
	if step != nil {
		if content := toolContentFromAction(step.Action); content != "" {
			return content
		}
		if message := humanHandoffMessageFromInput(step.Action.ToolInput); message != "" {
			return message
		}
		if message := humanHandoffMessageFromObservation(step.Observation); message != "" {
			return message
		}
	}
	return waitForWakeupFinalAnswer(step)
}

func humanHandoffMessageFromInput(input string) string {
	var req HumanHandoffRequest
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &req); err != nil {
		return ""
	}
	return firstNonEmptyString([]string{req.SuggestedAction, req.Details})
}

func humanHandoffMessageFromObservation(observation string) string {
	var payload struct {
		Message         string `json:"message"`
		SuggestedAction string `json:"suggested_action"`
		Details         string `json:"details"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(observation)), &payload); err != nil {
		return ""
	}
	return firstNonEmptyString([]string{payload.Message, payload.SuggestedAction, payload.Details})
}

func waitForWakeupFinalAnswer(step *schema.AgentStep) string {
	if step != nil {
		if content := toolContentFromAction(step.Action); content != "" {
			return content
		}
		if message := toolObservationMessage(step.Observation); message != "" {
			return message
		}
	}
	return "I will wait for the next wakeup."
}

func toolObservationMessage(observation string) string {
	observation = strings.TrimSpace(observation)
	if observation == "" || !strings.HasPrefix(observation, "{") {
		return ""
	}
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(observation), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Message)
}

func (s roleLoopState) canAcceptPlannerFinal(answer string) bool {
	if s.Phase != phasePlan || s.PlanCommitRequired || strings.TrimSpace(answer) == "" {
		return false
	}
	return s.PlanCommitted || s.PlanExhausted || len(s.PlanStepResults) > 0 || len(s.VerifierResults) > 0
}

func (s roleLoopState) isFinalCommittedPlanStep() bool {
	return s.PlanCommitted && len(s.Plan) > 0 && s.PlanStepIndex >= len(s.Plan)-1
}

func (s roleLoopState) hasRemainingCommittedPlanSteps() bool {
	return s.PlanCommitted && len(s.Plan) > 0 && s.PlanStepIndex >= 0 && s.PlanStepIndex < len(s.Plan)-1
}

func (s roleLoopState) normalizeVerifierDecisionForPlanTransition(decision verifierDecision, turnKind executorTurnKind) verifierDecision {
	if unresolved := s.unresolvedTargetTextArtifactIDs(); len(unresolved) > 0 && decision.CanFinish {
		decision.CanFinish = false
		decision.FinalAnswer = ""
		decision.NeedsReplan = false
		decision.Reason = appendVerifierRuntimeReason(decision.Reason, "runtime blocked finish because committed target_text artifact contracts are not consumed: "+strings.Join(unresolved, ", "))
	}
	if decision.NeedsReplan &&
		!decision.CanFinish &&
		turnKind == executorTurnFinishStep &&
		s.hasRemainingCommittedPlanSteps() &&
		normalizeVerifierStepStatus(decision.StepStatus) == verifierStepStatusSucceeded {
		decision.NeedsReplan = false
		decision.Reason = appendVerifierRuntimeReason(decision.Reason, "runtime continued the committed plan because verifier marked the current step succeeded and later steps remain")
	}
	return decision
}

func appendVerifierRuntimeReason(reason, addition string) string {
	reason = strings.TrimSpace(reason)
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return reason
	}
	if reason == "" {
		return addition
	}
	return reason + " " + addition
}

func (s *roleLoopState) beginStepExecution() {
	s.StepToolSteps = nil
	s.StepExecutionResults = nil
	s.ExecutorStepOutcome = ""
	s.ExecutorStepSummary = ""
	s.ExecutorStepKeyInfo = nil
	s.StepExecutionActive = true
}

func (s *roleLoopState) clearStepExecution() {
	s.StepToolSteps = nil
	s.StepExecutionResults = nil
	s.ExecutorStepOutcome = ""
	s.ExecutorStepSummary = ""
	s.ExecutorStepKeyInfo = nil
	s.StepExecutionActive = false
}

func (s *roleLoopState) syncNextStepFromPlanIndex() {
	if len(s.Plan) == 0 {
		s.NextStep = ""
		return
	}
	if s.PlanStepIndex < 0 || s.PlanStepIndex >= len(s.Plan) {
		s.NextStep = ""
		return
	}
	s.NextStep = strings.TrimSpace(s.Plan[s.PlanStepIndex])
}

func (s *roleLoopState) advancePlanStepOrExhaust() bool {
	s.PlanStepIndex++
	if s.PlanStepIndex >= len(s.Plan) {
		s.PlanExhausted = true
		return true
	}
	s.syncNextStepFromPlanIndex()
	return false
}

func (s *roleLoopState) applyCommittedPlan(decision plannerDecision) {
	if objective := strings.TrimSpace(decision.Objective); objective != "" {
		s.Objective = objective
	}
	if len(decision.CompletionCriteria) > 0 {
		s.CompletionCriteria = uniqueNonEmpty(decision.CompletionCriteria)
	}
	s.Plan = append([]string{}, decision.Plan...)
	s.PlanSources = initialPlanSourceStates(decision.Sources)
	s.PlanArtifacts = initialPlanArtifactStates(decision.Artifacts)
	s.PlanStepIndex = 0
	s.PlanCommitted = true
	s.PlanExhausted = false
	s.PlanCommitRequired = false
	s.ConsecutiveCommitPlanFailures = 0
	s.NextStep = ""
	s.syncNextStepFromPlanIndex()
	s.PlannerReason = strings.TrimSpace(decision.Reason)
	s.DraftPlan = plannerDecision{}
}

func (s *roleLoopState) clearCommittedPlan() {
	s.Objective = ""
	s.CompletionCriteria = nil
	s.Plan = nil
	s.PlanSources = nil
	s.PlanArtifacts = nil
	s.NextStep = ""
	s.PlanStepIndex = 0
	s.PlanCommitted = false
	s.PlanExhausted = false
	s.PlanCommitRequired = false
	s.ConsecutiveCommitPlanFailures = 0
	s.PlannerReason = ""
	s.PlanStepResults = nil
	s.DraftPlan = plannerDecision{}
}

func (s *roleLoopState) applyDraftPlan(decision plannerDecision) {
	s.DraftPlan = decision
	if objective := strings.TrimSpace(decision.Objective); objective != "" {
		s.Objective = objective
	}
	if len(decision.CompletionCriteria) > 0 {
		s.CompletionCriteria = uniqueNonEmpty(decision.CompletionCriteria)
	}
	if len(decision.Plan) > 0 {
		s.Plan = append([]string{}, decision.Plan...)
	}
	if len(decision.Sources) > 0 {
		s.PlanSources = initialPlanSourceStates(decision.Sources)
	}
	if len(decision.Artifacts) > 0 {
		s.PlanArtifacts = initialPlanArtifactStates(decision.Artifacts)
	}
	if next := strings.TrimSpace(decision.NextStep); next != "" {
		s.NextStep = next
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		s.PlannerReason = reason
	}
}

func parseCommitPlanInput(raw string) (plannerDecision, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return plannerDecision{}, fmt.Errorf("commit_plan requires a JSON payload")
	}
	var payload struct {
		Objective          string          `json:"objective"`
		CompletionCriteria json.RawMessage `json:"completion_criteria"`
		Plan               json.RawMessage `json:"plan"`
		Sources            json.RawMessage `json:"sources"`
		Artifacts          json.RawMessage `json:"artifacts"`
		Reason             string          `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return plannerDecision{}, fmt.Errorf("commit_plan payload must be valid JSON: %w", err)
	}
	artifacts, misplacedSources, err := parsePlanArtifactsAndMisplacedSources(payload.Artifacts)
	if err != nil {
		return plannerDecision{}, err
	}
	sources, err := parsePlanSources(payload.Sources)
	if err != nil {
		return plannerDecision{}, err
	}
	sources = append(sources, misplacedSources...)
	decision := plannerDecision{
		Objective:          strings.TrimSpace(payload.Objective),
		CompletionCriteria: parseStructuredStringList(payload.CompletionCriteria),
		Plan:               parseStructuredStringList(payload.Plan),
		Sources:            sources,
		Artifacts:          artifacts,
		Reason:             strings.TrimSpace(payload.Reason),
	}
	decision = normalizePhoneWorkflowContracts(decision)
	if len(decision.Plan) == 0 {
		return plannerDecision{}, fmt.Errorf("commit_plan requires a non-empty plan")
	}
	if err := validatePlanArtifacts(decision.Artifacts, len(decision.Plan)); err != nil {
		return plannerDecision{}, err
	}
	if err := validatePlanSources(decision.Sources, decision.Artifacts, len(decision.Plan)); err != nil {
		return plannerDecision{}, err
	}
	if decision.Objective == "" {
		decision.Objective = decision.Plan[0]
	}
	if len(decision.CompletionCriteria) == 0 {
		decision.CompletionCriteria = []string{defaultCompletionCriterion}
	}
	return decision, nil
}

func validateCommittedPlanPolicy(decision plannerDecision, _ worldState) error {
	if err := validateCommittedPlanArtifactContracts(decision); err != nil {
		return fmt.Errorf("phone app planning policy violation: %w", err)
	}
	if err := validatePhoneWorkflowContracts(decision); err != nil {
		return fmt.Errorf("phone app planning policy violation: %w", err)
	}
	return nil
}

func parseStructuredStringList(raw json.RawMessage) []string {
	values := parseStringList(raw)
	if len(values) == 1 {
		if decoded, ok := parseJSONStringArrayString(values[0]); ok {
			return decoded
		}
		if decoded, ok := parseJSONishStringArrayString(values[0]); ok {
			return decoded
		}
		if split := splitStructuredText(values[0]); len(split) > 0 {
			return split
		}
	}
	return uniqueNonEmpty(values)
}

func parseJSONStringArrayString(text string) ([]string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "[") {
		return nil, false
	}
	var values []string
	if err := json.Unmarshal([]byte(text), &values); err != nil {
		return nil, false
	}
	return uniqueNonEmpty(values), true
}

func parseJSONishStringArrayString(text string) ([]string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "[") || !strings.HasSuffix(text, "]") {
		return nil, false
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "["), "]"))
	if inner == "" {
		return nil, false
	}
	parts := jsonishStringArraySeparatorRE.Split(inner, -1)
	if len(parts) <= 1 {
		return nil, false
	}
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, `"`)
		part = strings.TrimSuffix(part, `"`)
		part = strings.ReplaceAll(part, `\"`, `"`)
		part = strings.ReplaceAll(part, `\\`, `\`)
		part = compactPlanWhitespace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return uniqueNonEmpty(values), len(values) > 0
}

func splitStructuredText(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	lines := splitStructuredLines(text)
	if len(lines) > 1 {
		return lines
	}
	if parts := splitByStepMarkers(text); len(parts) > 1 {
		return parts
	}
	if parts := splitByNumberedSemicolons(text); len(parts) > 1 {
		return parts
	}
	return uniqueNonEmpty([]string{text})
}

func splitStructuredLines(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	values := make([]string, 0, len(lines))
	for _, line := range lines {
		line = trimPlanListPrefix(line)
		if line != "" {
			values = append(values, line)
		}
	}
	return uniqueNonEmpty(values)
}

var (
	stepMarkerRE                  = regexp.MustCompile(`(?i)(^|\s+)(step\s*\d+\s*[:：])`)
	numberedMarkerRE              = regexp.MustCompile(`(^|\s*)(\(\d+\))`)
	jsonishStringArraySeparatorRE = regexp.MustCompile(`"\s*,\s*"`)
	planListPrefixRE              = regexp.MustCompile(`(?i)^\s*(?:[-*•]+|\d+[\.)]|step\s*\d+\s*[:：])\s*`)
	criteriaPrefixRE              = regexp.MustCompile(`(?i)^\s*(?:[-*•]+|\d+[\.)])\s*`)
	blankWhitespaceRE             = regexp.MustCompile(`\s+`)
	stageMarkerRE                 = regexp.MustCompile(`(?i)\bstage\s*\d+\b`)
	bulletRecordRE                = regexp.MustCompile(`(?m)^\s*[-*]\s*[^:\n]+:\s*[-+]?\d+(?:\.\d+)?\s*$`)
	routePlanIntentRE             = regexp.MustCompile(`(?i)(?:\b(?:enter|switch|use|choose|select)\s+(?:to\s+)?plan\s+mode\b|\benter_plan_mode\b|["']?mode["']?\s*:\s*["']?plan["']?)`)
	routeSimpleIntentRE           = regexp.MustCompile(`(?i)(?:\b(?:enter|switch|use|choose|select)\s+(?:to\s+)?simple\s+mode\b|\buse_simple_mode\b|["']?mode["']?\s*:\s*["']?simple["']?)`)
	routeDeviceActionRE           = regexp.MustCompile(`(?i)(\b(open|launch|tap|click|type|enter|paste|copy|send|message|text|call)\b|打开|启动|点击|输入|粘贴|复制|发送|发消息|发微信|拨打|查找|查询|查一下|帮我查|问问)`)
	routeDeviceTargetRE           = regexp.MustCompile(`(?i)(\b(phone|iphone|android|app|wechat|clipboard|contacts?|calendar|message|chat)\b|手机|微信|剪切板|剪贴板|通讯录|联系人|电话号|手机号|日历|输入框|聊天|好友)`)
	routeHowToRE                  = regexp.MustCompile(`(?i)^\s*(?:(?:how do i|how to|what is|why|when|where|can you explain)\b|怎么|如何|为什么|什么是)`)
)

func routeTextHasPlanIntent(text string) bool {
	return routePlanIntentRE.MatchString(text)
}

func routeTextHasSimpleIntent(text string) bool {
	return routeSimpleIntentRE.MatchString(text)
}

func splitByStepMarkers(text string) []string {
	matches := stepMarkerRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) <= 1 {
		return nil
	}
	parts := make([]string, 0, len(matches))
	for i, match := range matches {
		start := match[0]
		if start < len(text) && (text[start] == ' ' || text[start] == '\t' || text[start] == '\n') {
			start++
		}
		end := len(text)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		part := strings.TrimSpace(text[start:end])
		if part != "" {
			parts = append(parts, compactPlanWhitespace(part))
		}
	}
	return uniqueNonEmpty(parts)
}

func splitByNumberedSemicolons(text string) []string {
	if !strings.Contains(text, ";") || len(numberedMarkerRE.FindAllStringIndex(text, -1)) <= 1 {
		return nil
	}
	rawParts := strings.Split(text, ";")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, compactPlanWhitespace(part))
		}
	}
	return uniqueNonEmpty(parts)
}

func trimPlanListPrefix(line string) string {
	line = strings.TrimSpace(line)
	line = planListPrefixRE.ReplaceAllString(line, "")
	line = criteriaPrefixRE.ReplaceAllString(line, "")
	return compactPlanWhitespace(line)
}

func compactPlanWhitespace(text string) string {
	return blankWhitespaceRE.ReplaceAllString(strings.TrimSpace(text), " ")
}

func parseOptionalReasonInput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err == nil {
		return strings.TrimSpace(payload.Reason)
	}
	return raw
}

func normalizeRouteDecision(decision routeDecision, request string) routeDecision {
	decision.Mode = normalizeRouteMode(decision.Mode)
	decision.FinalAnswer = strings.TrimSpace(decision.FinalAnswer)
	decision.Speech = strings.TrimSpace(decision.Speech)
	decision.Text = strings.TrimSpace(decision.Text)
	if decision.Text != "" && (decision.Mode == "" || decision.Mode == routeModeDirectAnswer) {
		if decision.FinalAnswer == "" {
			decision.FinalAnswer = decision.Text
		}
	}
	decision.Reason = strings.TrimSpace(decision.Reason)
	if decision.Confidence < 0 {
		decision.Confidence = 0
	}
	if decision.Confidence > 1 {
		decision.Confidence = 1
	}
	if decision.Mode == "" {
		if decision.FinalAnswer != "" {
			decision.Mode = routeModeDirectAnswer
		} else {
			decision.Mode = routeModeSimple
		}
	}
	if decision.Mode == routeModeDirectAnswer && decision.FinalAnswer == "" {
		decision.Mode = routeModeSimple
	}
	if decision.Mode == routeModeDirectAnswer && routeRequiresToolExecution(request) {
		decision.Mode = routeModeSimple
		decision.FinalAnswer = ""
		if decision.Reason == "" {
			decision.Reason = "route policy requires tool execution for device/app operation"
		} else {
			decision.Reason += "; route policy requires tool execution for device/app operation"
		}
	}
	if routeShouldUsePlan(request) && decision.Mode != routeModePlan {
		decision.Mode = routeModePlan
		decision.FinalAnswer = ""
		if decision.Reason == "" {
			decision.Reason = "route policy requires plan mode for this multi-part request"
		} else {
			decision.Reason += "; route policy requires plan mode for this multi-part request"
		}
	}
	return decision
}

func normalizeRouteMode(mode routeMode) routeMode {
	value := strings.ToLower(strings.TrimSpace(string(mode)))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	switch value {
	case "direct", "answer", "direct_answer", "final", "final_answer":
		return routeModeDirectAnswer
	case "simple", "default", "default_mode", "simple_mode", "use_simple_mode":
		return routeModeSimple
	case "plan", "planning", "plan_mode", "plan_execute", "planned":
		return routeModePlan
	default:
		return ""
	}
}

func routeShouldUsePlan(request string) bool {
	text := strings.ToLower(strings.TrimSpace(request))
	if text == "" {
		return false
	}
	if len(stageMarkerRE.FindAllString(text, -1)) >= 2 {
		return true
	}
	if strings.Contains(text, "reconcile") && strings.Contains(text, "invoice") {
		return true
	}
	if strings.Contains(text, "expense list") || strings.Contains(text, "category totals") {
		if strings.Contains(text, "grand total") || strings.Contains(text, "records over") || strings.Contains(text, "highest") {
			return true
		}
	}
	if len(bulletRecordRE.FindAllString(request, -1)) >= 4 {
		if strings.Contains(text, "total") && (strings.Contains(text, "highest") || strings.Contains(text, "records over") || strings.Contains(text, "aggregate")) {
			return true
		}
	}
	requiredSignals := 0
	for _, signal := range []string{"find category totals", "highest", "number of records", "grand total", "choose the final net total"} {
		if strings.Contains(text, signal) {
			requiredSignals++
		}
	}
	return requiredSignals >= 2
}

func routeRequiresToolExecution(request string) bool {
	text := strings.ToLower(strings.TrimSpace(request))
	if text == "" {
		return false
	}
	if routeHowToRE.MatchString(text) && !strings.Contains(text, "帮我") && !strings.Contains(text, "替我") {
		return false
	}
	if routeLooksLikeLaunchOnlyDeviceRequest(text) {
		return false
	}
	return routeDeviceActionRE.MatchString(text) && routeDeviceTargetRE.MatchString(text)
}

func routeLooksLikeLaunchOnlyDeviceRequest(text string) bool {
	hasLaunchVerb := strings.Contains(text, "打开") ||
		strings.Contains(text, "启动") ||
		strings.Contains(text, "open ") ||
		strings.Contains(text, "launch ")
	if !hasLaunchVerb {
		return false
	}
	for _, signal := range []string{
		"发送", "发消息", "发微信", "消息", "问问", "输入", "粘贴", "复制", "剪切板", "剪贴板",
		"通讯录", "联系人", "电话号", "手机号", "搜索", "查找", "查询", "send ", "message",
		"text ", "type ", "enter ", "paste", "copy", "contacts", "calendar", "search", "find",
	} {
		if strings.Contains(text, signal) {
			return false
		}
	}
	return true
}

func plannerTaskForPhase(phase loopPhase, state roleLoopState, forceSimpleLoop bool) string {
	if forceSimpleLoop {
		return "Single-agent simple loop mode: delegated plan mode is disabled by configuration. Use available tools directly and return a final answer when the request is satisfied. If the task becomes multi-step, use set_todo to maintain a visible todo list without switching modes."
	}
	switch phase {
	case phaseDecision:
		return "Route phase: decide the execution path before normal tools are exposed. Return only JSON: {\"mode\":\"direct_answer|simple|plan\",\"final_answer\":\"plain text only for direct_answer\",\"reason\":\"brief rationale\",\"confidence\":0.0-1.0}. Voice interaction is the core use case, so keep direct answers brief and natural for TTS. Use direct_answer only when the final user-facing answer is available now without tools; leave final_answer empty for simple or plan. Use simple for ordinary one-pass execution such as direct tool use, straightforward arithmetic, or short comparisons. Use plan for tasks that need explicit planning, checkpoints, information gathering before acting, multiple independent stages, record aggregation, reconciliation, branching, or several required output facts. Examples: a single expression or comparing two expressions is simple; invoice reconciliation across stages and expense/category aggregation are plan. Important: the visible conversation context only contains recent visible turns and a profile snapshot; prior turns are compressed into archived chunks, and long-term facts/preferences/rules live in a separate memory store — both are invisible to you here. If the user's input asks about or references prior conversation content, previously remembered preferences/rules, or anything else you cannot confirm from the visible context (including denials such as 'we never discussed X'), choose simple so the executor can retrieve from history or memory before answering. Do not assume the visible context is the complete record."
	case phasePlan:
		task := "Plan mode: create or revise a structured delegated plan. You may use read-only information-gathering tools before committing if context is missing. Do not use execution, computation, or state-changing tools in plan mode. Call commit_plan to hand concrete steps to the executor, call cancel_plan only when planning should be abandoned, or return a final answer only when existing execution evidence already proves the task complete."
		if state.PlanCommitRequired {
			task = "Plan mode was selected by the route phase. Gather read-only context only if needed, then call commit_plan before delegated execution, final answer, or cancel_plan. Build a compact plan for the remaining work; each step may include multiple tool calls during execution."
		}
		if state.PlanExhausted {
			task += " The committed plan is exhausted before the task was verified complete; revise the plan and commit_plan again."
		}
		if len(state.VerifierResults) > 0 {
			if last := state.VerifierResults[len(state.VerifierResults)-1]; strings.TrimSpace(last.Reason) != "" {
				task += " Latest verifier feedback for replanning: " + last.Reason
			}
		}
		return task
	default:
		return "Single-agent default mode: use available tools directly as needed and return a final answer when the request is satisfied. If the task becomes multi-step, use set_todo to maintain a visible todo list without switching to delegated plan mode."
	}
}

func writeLoopMode(builder *strings.Builder, state roleLoopState) {
	builder.WriteString("\n\nLoop mode:\n")
	builder.WriteString("- current_mode: ")
	builder.WriteString(string(state.Phase))
	builder.WriteByte('\n')
	if state.ForceSimpleLoop {
		builder.WriteString("- force_simple_loop: true\n")
		builder.WriteString("- delegated plan mode tools are disabled; use available tools directly and return a final answer when done.\n")
		builder.WriteString("- set_todo is available when this single-agent task needs explicit multi-step tracking.\n")
		return
	}
	if state.Phase == phaseDecision {
		builder.WriteString("- this is the upfront route decision before normal tool execution.\n")
		builder.WriteString("- no tools are available in this phase.\n")
		builder.WriteString("- return only JSON with mode direct_answer, simple, or plan.\n")
		builder.WriteString("- direct_answer requires final_answer ready for the user as brief plain text suitable for TTS.\n")
		builder.WriteString("- simple and plan must leave final_answer empty.\n")
		builder.WriteString("- plan is required for explicit planning, checkpoints, information gathering before acting, multi-stage reconciliation, record aggregation, branching, or several required output facts.\n")
	}
	if state.Phase == phasePlan {
		builder.WriteString("- commit_plan is available in plan mode only.\n")
		builder.WriteString("- read-only information-gathering tools may be used before commit_plan when needed.\n")
		builder.WriteString("- execution, computation, and state-changing tools are not executed in plan mode; put that work into committed executor steps.\n")
		builder.WriteString("- cancel_plan returns to default mode and clears draft planning state.\n")
		if state.PlanCommitRequired {
			builder.WriteString("- plan_commit_required: true; gather read-only context only if needed, then call commit_plan.\n")
			builder.WriteString("- use a compact delegated plan; each plan step may include multiple tool calls during execution.\n")
		}
		if state.PlanExhausted {
			builder.WriteString("- plan_exhausted: true\n")
		}
		if state.PlanCommitted && len(state.Plan) > 0 {
			builder.WriteString("- last_committed_plan_steps: ")
			builder.WriteString(fmt.Sprintf("%d", len(state.Plan)))
			builder.WriteByte('\n')
		}
	}
	if state.Phase == phaseDefault {
		builder.WriteString("- final answers in default mode end the run directly without verifier.\n")
		builder.WriteString("- commit_plan is not available in default mode.\n")
		builder.WriteString("- set_todo is available if this single-agent task needs explicit multi-step tracking.\n")
		builder.WriteString("- complete directly without a delegated plan unless the user changes the task or the task needs explicit planning.\n")
	}
}
