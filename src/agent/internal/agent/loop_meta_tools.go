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

	maxConsecutiveCommitPlanFailures = 3
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
			description: "Commit the draft plan and switch to delegated execution. Use coarse steps because each step may use multiple tool calls; do not create one plan step per small calculation. Declare sources/artifacts for data products later steps must consume, and batch Phone Bridge app-side work before open_app/search_launch_app. When prepare_phone_app_workflow covers the Aiden-foreground preparation phase, make it one coarse step instead of separate app-side tool, clipboard, and target-app launch steps.",
			schema: objectArgsSchema(map[string]any{
				"objective":           stringArgSchema("Task objective."),
				"completion_criteria": stringArrayArgSchema("Concrete criteria required before the task is complete."),
				"plan":                stringArrayArgSchema("Ordered execution steps."),
				"sources":             planSourcesArgSchema(),
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

func planSourcesArgSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "Structured data sources that must be produced by direct app-side tools before artifacts depending on them can be prepared. Use this instead of opening source apps just to read data.",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"id":          stringArgSchema("Stable source id referenced by artifact source_refs, for example contact_lookup."),
				"tool":        stringEnumArgSchema("Direct app-side tool that produces the source.", planSourceToolContacts),
				"action":      stringEnumArgSchema("Tool action that produces the source.", planSourceActionQuery),
				"step":        minIntegerArgSchema("1-based plan step that must produce this source.", 1),
				"query":       stringArgSchema("Exact query value for contacts action=query, when known."),
				"produces":    stringArrayArgSchema("Structured fields produced by this source, for example phone_numbers."),
				"artifact_id": stringArgSchema("Optional artifact id that consumes this source."),
			},
			"required": []string{"id", "tool", "action", "step"},
		},
	}
}

func planArtifactsArgSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "Structured data products that executor steps must prepare and consume before target-app navigation. Use target_text/clipboard when a later target-app text entry should use prepared text. The text may be fixed literal text or a template with {{source}} placeholders.",
		"items": map[string]any{
			"oneOf": []map[string]any{
				targetTextArtifactArgSchema(),
				clipboardPayloadArtifactArgSchema(),
			},
		},
	}
}

func targetTextArtifactArgSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"id":               stringArgSchema("Stable artifact id used by later tool calls as artifact_id."),
		"kind":             stringEnumArgSchema("Artifact kind.", planArtifactKindTargetText),
		"delivery":         stringEnumArgSchema("How the artifact is delivered to the target app.", planArtifactDeliveryClipboard),
		"prepare_step":     minIntegerArgSchema("1-based plan step that prepares this artifact while Aiden is foreground. It must be earlier than consume_step; runtime blocks target-app navigation until the artifact is prepared.", 1),
		"target_open_step": minIntegerArgSchema("Optional diagnostic 1-based plan step that opens or foregrounds target_app. Runtime still blocks actual target-app opening until linked sources and artifacts are prepared.", 1),
		"consume_step":     minIntegerArgSchema("1-based plan step that consumes this artifact in target_app after target-app navigation.", 1),
		"text_template":    stringArgSchema("Fixed target text or a template with {{source}} placeholders."),
		"source_refs":      stringArrayArgSchema("Structured source references needed to fill placeholder templates. Each ref must point at a declared sources id, for example contact_lookup.phone_numbers."),
		"target_app":       stringArgSchema("Target app that will consume this artifact."),
		"target_label":     stringArgSchema("Target chat/contact/field label that will consume this artifact."),
	}, "id", "kind", "delivery", "prepare_step", "consume_step", "text_template", "target_app")
}

func clipboardPayloadArtifactArgSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"id":           stringArgSchema("Stable artifact id used by later tool calls as artifact_id."),
		"kind":         stringEnumArgSchema("Artifact kind.", planArtifactKindClipboardPayload),
		"delivery":     stringEnumArgSchema("How the artifact is delivered.", planArtifactDeliveryClipboard),
		"prepare_step": minIntegerArgSchema("1-based final plan step that writes this payload.", 1),
	}, "id", "kind", "delivery", "prepare_step")
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
		state.ConsecutiveCommitPlanFailures = 0
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
			return commitPlanFailureTurn(state, action, commitPlanFailureObservation(err))
		}
		if err := validateCommittedPlanPolicy(decision, state.World); err != nil {
			return commitPlanFailureTurn(state, action, commitPlanFailureObservation(err))
		}
		state.ConsecutiveCommitPlanFailures = 0
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

func commitPlanFailureTurn(state *roleLoopState, action schema.AgentAction, observation string) plannerTurnResult {
	observation = strings.TrimSpace(observation)
	if observation == "" {
		observation = "commit_plan failed"
	}
	step := &schema.AgentStep{
		Action:      action,
		Observation: observation,
	}
	if state != nil {
		state.ConsecutiveCommitPlanFailures++
		if state.ConsecutiveCommitPlanFailures >= maxConsecutiveCommitPlanFailures {
			state.Phase = phaseDefault
			state.PlanCommitRequired = false
			return plannerTurnResult{
				Kind:   plannerTurnFinish,
				Answer: commitPlanFailureFinalAnswer(observation),
				Step:   step,
			}
		}
	}
	return plannerTurnResult{
		Kind:            plannerTurnInvalidMeta,
		InvalidMetaStep: step,
	}
}

func commitPlanFailureFinalAnswer(observation string) string {
	reason := strings.TrimSpace(strings.TrimPrefix(observation, "commit_plan failed:"))
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(reason), &payload); err == nil && strings.TrimSpace(payload.Error) != "" {
		reason = strings.TrimSpace(payload.Error)
	}
	if reason == "" {
		reason = observation
	}
	return "规划提交连续失败，已停止重试。最后一次错误：" + reason
}

func commitPlanFailureObservation(err error) string {
	if err == nil {
		return "commit_plan failed"
	}
	payload := map[string]any{
		"error": strings.TrimSpace(err.Error()),
	}
	var planErr *planValidationError
	if errors.As(err, &planErr) && planErr != nil {
		if strings.TrimSpace(planErr.Code) != "" {
			payload["code"] = strings.TrimSpace(planErr.Code)
		}
		if len(planErr.Hint) > 0 {
			payload["repair_hint"] = planErr.Hint
		}
	}
	encoded, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return fmt.Sprintf("commit_plan failed: %v", err)
	}
	return "commit_plan failed: " + string(encoded)
}
