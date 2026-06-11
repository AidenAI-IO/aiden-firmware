package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/schema"
)

type loopPhase string

const (
	phaseDefault   loopPhase = "default"
	phasePlan      loopPhase = "plan"
	phaseExecution loopPhase = "execution"
)

type plannerTurnKind int

const (
	plannerTurnTool plannerTurnKind = iota
	plannerTurnFinish
	plannerTurnEnterPlan
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
		s.Phase = phasePlan
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
	s.PlannerReason = ""
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
		Objective          string   `json:"objective"`
		CompletionCriteria []string `json:"completion_criteria"`
		Plan               []string `json:"plan"`
		Reason             string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return plannerDecision{}, fmt.Errorf("commit_plan payload must be valid JSON: %w", err)
	}
	decision := plannerDecision{
		Objective:          strings.TrimSpace(payload.Objective),
		CompletionCriteria: uniqueNonEmpty(payload.CompletionCriteria),
		Plan:               uniqueNonEmpty(payload.Plan),
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

func plannerTaskForPhase(phase loopPhase, state roleLoopState) string {
	switch phase {
	case phasePlan:
		task := "Plan mode: explore with tools, maintain a draft plan, then call commit_plan when ready for delegated execution or cancel_plan to return to default mode."
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
		return "Default mode: execute simple tasks directly with tools and return a final answer when done. Call enter_plan_mode when the task needs a multi-step plan, branching, or sustained tracking."
	}
}

func writeLoopMode(builder *strings.Builder, state roleLoopState) {
	builder.WriteString("\n\nLoop mode:\n")
	builder.WriteString("- current_mode: ")
	builder.WriteString(string(state.Phase))
	builder.WriteByte('\n')
	if state.Phase == phasePlan {
		builder.WriteString("- commit_plan is available in plan mode only.\n")
		builder.WriteString("- cancel_plan returns to default mode and clears draft planning state.\n")
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
	}
}
