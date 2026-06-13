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

type plannerTurnKind int

const (
	plannerTurnTool plannerTurnKind = iota
	plannerTurnFinish
	plannerTurnEnterPlan
	plannerTurnUseSimpleMode
	plannerTurnCommitPlan
	plannerTurnCancelPlan
	plannerTurnInvalidMeta
)

type plannerTurnResult struct {
	Kind            plannerTurnKind
	Answer          string
	Step            *schema.AgentStep
	CommittedPlan   plannerDecision
	InvalidMetaStep *schema.AgentStep
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
	Reason      string    `json:"reason,omitempty"`
	Confidence  float64   `json:"confidence,omitempty"`
}

type executorTurnKind int

const (
	executorTurnTool executorTurnKind = iota
	executorTurnFinishStep
	executorTurnAbortStep
	executorTurnInvalidMeta
)

type executorTurnResult struct {
	Kind            executorTurnKind
	Action          *schema.AgentAction
	Step            *schema.AgentStep
	InvalidMetaStep *schema.AgentStep
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

func (s roleLoopState) canAcceptPlannerFinal(answer string) bool {
	if s.Phase != phasePlan || s.PlanCommitRequired || strings.TrimSpace(answer) == "" {
		return false
	}
	return s.PlanCommitted || s.PlanExhausted || len(s.PlanStepResults) > 0 || len(s.VerifierResults) > 0
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
	s.PlanStepIndex = 0
	s.PlanCommitted = true
	s.PlanExhausted = false
	s.PlanCommitRequired = false
	s.NextStep = ""
	s.syncNextStepFromPlanIndex()
	s.PlannerReason = strings.TrimSpace(decision.Reason)
	s.DraftPlan = plannerDecision{}
}

func (s *roleLoopState) clearCommittedPlan() {
	s.Objective = ""
	s.CompletionCriteria = nil
	s.Plan = nil
	s.NextStep = ""
	s.PlanStepIndex = 0
	s.PlanCommitted = false
	s.PlanExhausted = false
	s.PlanCommitRequired = false
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
		Reason             string          `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return plannerDecision{}, fmt.Errorf("commit_plan payload must be valid JSON: %w", err)
	}
	decision := plannerDecision{
		Objective:          strings.TrimSpace(payload.Objective),
		CompletionCriteria: parseStructuredStringList(payload.CompletionCriteria),
		Plan:               parseStructuredStringList(payload.Plan),
		Reason:             strings.TrimSpace(payload.Reason),
	}
	if len(decision.Plan) == 0 {
		return plannerDecision{}, fmt.Errorf("commit_plan requires a non-empty plan")
	}
	if decision.Objective == "" {
		decision.Objective = decision.Plan[0]
	}
	if len(decision.CompletionCriteria) == 0 {
		decision.CompletionCriteria = []string{"Satisfy every explicit requirement in the original user request."}
	}
	return decision, nil
}

func parseStructuredStringList(raw json.RawMessage) []string {
	values := parseStringList(raw)
	if len(values) == 1 {
		if split := splitStructuredText(values[0]); len(split) > 0 {
			return split
		}
	}
	return uniqueNonEmpty(values)
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
	stepMarkerRE        = regexp.MustCompile(`(?i)(^|\s+)(step\s*\d+\s*[:：])`)
	numberedMarkerRE    = regexp.MustCompile(`(^|\s*)(\(\d+\))`)
	planListPrefixRE    = regexp.MustCompile(`(?i)^\s*(?:[-*•]+|\d+[\.)]|step\s*\d+\s*[:：])\s*`)
	criteriaPrefixRE    = regexp.MustCompile(`(?i)^\s*(?:[-*•]+|\d+[\.)])\s*`)
	blankWhitespaceRE   = regexp.MustCompile(`\s+`)
	stageMarkerRE       = regexp.MustCompile(`(?i)\bstage\s*\d+\b`)
	bulletRecordRE      = regexp.MustCompile(`(?m)^\s*[-*]\s*[^:\n]+:\s*[-+]?\d+(?:\.\d+)?\s*$`)
	routePlanIntentRE   = regexp.MustCompile(`(?i)(?:\b(?:enter|switch|use|choose|select)\s+(?:to\s+)?plan\s+mode\b|\benter_plan_mode\b|["']?mode["']?\s*:\s*["']?plan["']?)`)
	routeSimpleIntentRE = regexp.MustCompile(`(?i)(?:\b(?:enter|switch|use|choose|select)\s+(?:to\s+)?simple\s+mode\b|\buse_simple_mode\b|["']?mode["']?\s*:\s*["']?simple["']?)`)
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

func plannerTaskForPhase(phase loopPhase, state roleLoopState, forceSimpleLoop bool) string {
	if forceSimpleLoop {
		return "Simple loop mode: plan-mode tools are disabled by configuration. Use available tools directly and return a final answer when the request is satisfied."
	}
	switch phase {
	case phaseDecision:
		return "Route phase: decide the execution path before normal tools are exposed. Return only JSON: {\"mode\":\"direct_answer|simple|plan\",\"final_answer\":\"only for direct_answer\",\"reason\":\"brief rationale\",\"confidence\":0.0-1.0}. Use direct_answer only when the final user-facing answer is available now without tools. Use simple for ordinary one-pass execution such as direct tool use, straightforward arithmetic, or short comparisons. Use plan for tasks that need explicit planning, checkpoints, delegated execution, information gathering before acting, multiple independent stages, record aggregation, reconciliation, branching, or several required output facts. Examples: a single expression or comparing two expressions is simple; invoice reconciliation across stages and expense/category aggregation are plan."
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
		return "Simple/default mode: the route phase selected ordinary execution. Use available tools directly as needed and return a final answer when the request is satisfied."
	}
}

func writeLoopMode(builder *strings.Builder, state roleLoopState) {
	builder.WriteString("\n\nLoop mode:\n")
	builder.WriteString("- current_mode: ")
	builder.WriteString(string(state.Phase))
	builder.WriteByte('\n')
	if state.ForceSimpleLoop {
		builder.WriteString("- force_simple_loop: true\n")
		builder.WriteString("- plan mode tools are disabled; use available tools directly and return a final answer when done.\n")
		return
	}
	if state.Phase == phaseDecision {
		builder.WriteString("- this is the upfront route decision before normal tool execution.\n")
		builder.WriteString("- no tools are available in this phase.\n")
		builder.WriteString("- return only JSON with mode direct_answer, simple, or plan.\n")
		builder.WriteString("- direct_answer requires a final_answer value ready for the user.\n")
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
		builder.WriteString("- this request was routed to simple/default mode; complete it directly without a delegated plan unless the user changes the task.\n")
	}
}
