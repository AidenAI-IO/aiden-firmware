package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

const (
	toolUseSimpleMode = "use_simple_mode"
	toolEnterPlanMode = "enter_plan_mode"
	toolCommitPlan    = "commit_plan"
	toolCancelPlan    = "cancel_plan"
)

type loopMetaTool struct {
	name        string
	description string
	schema      map[string]any
}

func (t *loopMetaTool) Name() string { return t.name }

func (t *loopMetaTool) Description() string { return t.description }

func (t *loopMetaTool) ArgsSchema() map[string]any {
	return t.schema
}

func (t *loopMetaTool) Call(context.Context, string) (string, error) {
	return "", errors.New("loop meta tool must be handled by the role loop controller")
}

func loopMetaTools() []langtools.Tool {
	return []langtools.Tool{
		&loopMetaTool{
			name:        toolUseSimpleMode,
			description: "Use default/simple mode for short tasks that do not need multi-step planning. Optional JSON input: {\"reason\":\"why simple mode is sufficient\"}.",
			schema: objectArgsSchema(map[string]any{
				"reason": stringArgSchema("Why simple mode is sufficient."),
			}),
		},
		&loopMetaTool{
			name:        toolEnterPlanMode,
			description: "Enter plan mode to draft and commit a multi-step plan. Required in default mode when the task will likely need 3 or more steps. Optional JSON input: {\"reason\":\"why planning is needed\"}.",
			schema: objectArgsSchema(map[string]any{
				"reason": stringArgSchema("Why planning is needed."),
			}),
		},
		&loopMetaTool{
			name:        toolCommitPlan,
			description: "Commit the draft plan and switch to delegated execution. Use coarse steps because each step may use multiple tool calls; do not create one plan step per small calculation. Input JSON: {\"objective\":\"task\",\"completion_criteria\":[\"criterion\"],\"plan\":[\"step\"],\"reason\":\"brief rationale\"}.",
			schema: objectArgsSchema(map[string]any{
				"objective":           stringArgSchema("Task objective."),
				"completion_criteria": stringArrayArgSchema("Concrete criteria required before the task is complete."),
				"plan":                stringArrayArgSchema("Ordered execution steps."),
				"reason":              stringArgSchema("Brief rationale for the committed plan."),
			}, "plan"),
		},
		&loopMetaTool{
			name:        toolCancelPlan,
			description: "Cancel plan mode and return to default mode. Optional JSON input: {\"reason\":\"why planning is cancelled\"}.",
			schema: objectArgsSchema(map[string]any{
				"reason": stringArgSchema("Why planning is cancelled."),
			}),
		},
	}
}

func appendLoopMetaTools(tools []langtools.Tool) []langtools.Tool {
	combined := append([]langtools.Tool{}, tools...)
	seen := map[string]bool{}
	for _, tool := range combined {
		if tool != nil {
			seen[strings.ToUpper(tool.Name())] = true
		}
	}
	for _, tool := range loopMetaTools() {
		if seen[strings.ToUpper(tool.Name())] {
			continue
		}
		combined = append(combined, tool)
		seen[strings.ToUpper(tool.Name())] = true
	}
	return combined
}

func isLoopMetaTool(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case strings.ToUpper(toolUseSimpleMode), strings.ToUpper(toolEnterPlanMode), strings.ToUpper(toolCommitPlan), strings.ToUpper(toolCancelPlan):
		return true
	default:
		return false
	}
}

func (e *roleCollaborativeExecutor) handlePlannerMetaTool(
	phase loopPhase,
	state *roleLoopState,
	action schema.AgentAction,
) plannerTurnResult {
	toolName := strings.ToUpper(strings.TrimSpace(action.Tool))
	input := normalizeToolInput(action.ToolInput)

	switch toolName {
	case strings.ToUpper(toolUseSimpleMode):
		if phase != phaseDecision {
			return plannerTurnResult{
				Kind: plannerTurnInvalidMeta,
				InvalidMetaStep: &schema.AgentStep{
					Action:      action,
					Observation: "use_simple_mode is only available in the upfront decision phase",
				},
			}
		}
		state.Phase = phaseDefault
		return plannerTurnResult{Kind: plannerTurnUseSimpleMode}

	case strings.ToUpper(toolEnterPlanMode):
		if phase != phaseDecision && phase != phaseDefault {
			return plannerTurnResult{
				Kind: plannerTurnInvalidMeta,
				InvalidMetaStep: &schema.AgentStep{
					Action:      action,
					Observation: "enter_plan_mode is only available in decision or default mode",
				},
			}
		}
		state.Phase = phasePlan
		state.PlanExhausted = false
		state.PlanCommitRequired = true
		reason := parseOptionalReasonInput(input)
		observation := `{"status":"entered","phase":"plan"}`
		if reason != "" {
			payload, _ := json.Marshal(map[string]string{"status": "entered", "phase": "plan", "reason": reason})
			observation = string(payload)
		}
		return plannerTurnResult{
			Kind: plannerTurnEnterPlan,
			Step: &schema.AgentStep{Action: action, Observation: observation},
		}

	case strings.ToUpper(toolCommitPlan):
		if phase != phasePlan {
			return plannerTurnResult{
				Kind: plannerTurnInvalidMeta,
				InvalidMetaStep: &schema.AgentStep{
					Action:      action,
					Observation: "commit_plan is only available in plan mode",
				},
			}
		}
		decision, err := parseCommitPlanInput(input)
		if err != nil {
			return plannerTurnResult{
				Kind: plannerTurnInvalidMeta,
				InvalidMetaStep: &schema.AgentStep{
					Action:      action,
					Observation: fmt.Sprintf("commit_plan failed: %v", err),
				},
			}
		}
		state.applyCommittedPlan(decision)
		state.applyCommittedPlanTodo(decision)
		state.Phase = phaseExecution
		payload, _ := json.Marshal(map[string]any{
			"status": "committed",
			"phase":  "execution",
			"steps":  len(state.Plan),
		})
		return plannerTurnResult{
			Kind:          plannerTurnCommitPlan,
			CommittedPlan: decision,
			Step:          &schema.AgentStep{Action: action, Observation: string(payload)},
		}

	case strings.ToUpper(toolCancelPlan):
		if phase != phasePlan {
			return plannerTurnResult{
				Kind: plannerTurnInvalidMeta,
				InvalidMetaStep: &schema.AgentStep{
					Action:      action,
					Observation: "cancel_plan is only available in plan mode",
				},
			}
		}
		state.clearCommittedPlan()
		state.Phase = phaseDefault
		reason := parseOptionalReasonInput(input)
		observation := `{"status":"cancelled","phase":"default"}`
		if reason != "" {
			payload, _ := json.Marshal(map[string]string{"status": "cancelled", "phase": "default", "reason": reason})
			observation = string(payload)
		}
		return plannerTurnResult{
			Kind: plannerTurnCancelPlan,
			Step: &schema.AgentStep{Action: action, Observation: observation},
		}
	default:
		return plannerTurnResult{
			Kind: plannerTurnInvalidMeta,
			InvalidMetaStep: &schema.AgentStep{
				Action:      action,
				Observation: fmt.Sprintf("%s is not a valid loop meta tool", action.Tool),
			},
		}
	}
}
