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
	toolSetTodo       = "set_todo"
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
			description: "Commit the draft plan and switch to delegated execution. Use coarse steps because each step may use multiple tool calls; do not create one plan step per small calculation. Declare artifacts for clipboard writes or other data products that later steps must consume.",
			schema: objectArgsSchema(map[string]any{
				"objective":           stringArgSchema("Task objective."),
				"completion_criteria": stringArrayArgSchema("Concrete criteria required before the task is complete."),
				"plan":                stringArrayArgSchema("Ordered execution steps."),
				"artifacts":           planArtifactsArgSchema(),
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

func planArtifactsArgSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "Structured data products that executor steps must prepare and consume. Use target_text/clipboard when a later target-app text entry must use text composed from earlier gathered data.",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"id":            stringArgSchema("Stable artifact id used by later tool calls as artifact_id."),
				"kind":          stringEnumArgSchema("Artifact kind.", planArtifactKindTargetText, planArtifactKindClipboardPayload),
				"delivery":      stringEnumArgSchema("How the artifact is delivered to the target app.", planArtifactDeliveryClipboard),
				"prepare_step":  minIntegerArgSchema("1-based plan step that prepares this artifact.", 1),
				"consume_step":  minIntegerArgSchema("1-based plan step that consumes this artifact.", 1),
				"text_template": stringArgSchema("Template for the target text; use {{source}} placeholders for data gathered during preparation."),
				"source_refs":   stringArrayArgSchema("Structured source references needed to fill the template."),
				"target_app":    stringArgSchema("Target app that will consume this artifact."),
				"target_label":  stringArgSchema("Target chat/contact/field label that will consume this artifact."),
			},
			"required": []string{"id", "kind", "delivery", "prepare_step", "consume_step", "text_template"},
		},
	}
}

func simpleTodoMetaTools() []langtools.Tool {
	return []langtools.Tool{
		&loopMetaTool{
			name:        toolSetTodo,
			description: "Create or replace the todo list in single-agent/default mode when the task has become multi-step. This does not switch to delegated plan mode. Input JSON: {\"objective\":\"task\",\"items\":[\"step\"],\"current_index\":1,\"completed_indices\":[1],\"blocked_indices\":[2],\"reason\":\"brief reason\"}. Use 1-based indices.",
			schema: objectArgsSchema(map[string]any{
				"objective":         stringArgSchema("Task objective for this todo list."),
				"items":             stringArrayArgSchema("Ordered todo items for the current single-agent task."),
				"current_index":     minIntegerArgSchema("1-based index of the item currently being worked on.", 1),
				"completed_indices": integerArrayArgSchema("1-based item indices that are already done."),
				"blocked_indices":   integerArrayArgSchema("1-based item indices that are blocked."),
				"reason":            stringArgSchema("Brief reason for creating or updating the todo list."),
			}, "items", "current_index"),
		},
	}
}

func appendLoopMetaTools(tools []langtools.Tool) []langtools.Tool {
	return appendUniqueTools(tools, loopMetaTools())
}

func appendDefaultLoopMetaTools(tools []langtools.Tool) []langtools.Tool {
	combined := appendUniqueTools(tools, loopMetaTools())
	return appendUniqueTools(combined, simpleTodoMetaTools())
}

func appendSimpleTodoMetaTools(tools []langtools.Tool) []langtools.Tool {
	return appendUniqueTools(tools, simpleTodoMetaTools())
}

func appendUniqueTools(tools []langtools.Tool, additions []langtools.Tool) []langtools.Tool {
	combined := append([]langtools.Tool{}, tools...)
	seen := map[string]bool{}
	for _, tool := range combined {
		if tool != nil {
			seen[strings.ToUpper(tool.Name())] = true
		}
	}
	for _, tool := range additions {
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
	case strings.ToUpper(toolUseSimpleMode), strings.ToUpper(toolEnterPlanMode), strings.ToUpper(toolCommitPlan), strings.ToUpper(toolCancelPlan), strings.ToUpper(toolSetTodo):
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
		if err := validateCommittedPlanPolicy(decision, state.World); err != nil {
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

	case strings.ToUpper(toolSetTodo):
		if phase != phaseDefault {
			return plannerTurnResult{
				Kind: plannerTurnInvalidMeta,
				InvalidMetaStep: &schema.AgentStep{
					Action:      action,
					Observation: "set_todo is only available in single-agent/default mode",
				},
			}
		}
		update, err := parseSetTodoInput(input)
		if err != nil {
			return plannerTurnResult{
				Kind: plannerTurnInvalidMeta,
				InvalidMetaStep: &schema.AgentStep{
					Action:      action,
					Observation: fmt.Sprintf("set_todo failed: %v", err),
				},
			}
		}
		todo, speechEligible := state.applySimpleTodoUpdate(update)
		payload, _ := json.Marshal(map[string]any{
			"status":        "updated",
			"mode":          "simple",
			"items":         len(todo.Items),
			"current_index": todoCurrentStepIndex(todo),
		})
		return plannerTurnResult{
			Kind:               plannerTurnSetTodo,
			Todo:               todo,
			TodoSpeechEligible: speechEligible,
			Step:               &schema.AgentStep{Action: action, Observation: string(payload)},
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
