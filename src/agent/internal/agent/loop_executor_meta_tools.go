package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

const (
	toolFinishStep       = "finish_step"
	toolAbortStep        = "abort_step"
	toolHumanHandoffStep = "request_human_handoff"
)

func executorMetaTools() []langtools.Tool {
	return []langtools.Tool{
		&loopMetaTool{
			name:        toolFinishStep,
			description: "Signal the current plan step is ready for verification. Required before verifier review. Input JSON: {\"summary\":\"what was accomplished for this step\",\"key_info\":[\"facts, ids, values, or observations later steps may need\"],\"reason\":\"optional\"}.",
			schema: objectArgsSchema(map[string]any{
				"summary":  stringArgSchema("What was accomplished for this step."),
				"result":   stringArgSchema("Alias for summary."),
				"key_info": stringArrayArgSchema("Facts, ids, values, labels, or observations later steps may need."),
				"reason":   stringArgSchema("Optional reason or verification context."),
			}),
		},
		&loopMetaTool{
			name:        toolAbortStep,
			description: "Signal the current plan step cannot be completed and needs replanning. Required to stop step execution and enter verifier review. Input JSON: {\"reason\":\"why the step failed or is blocked\"}.",
			schema: objectArgsSchema(map[string]any{
				"reason":  stringArgSchema("Why the step failed or is blocked."),
				"summary": stringArgSchema("Alias for reason."),
			}, "reason"),
		},
	}
}

func appendExecutorMetaTools(tools []langtools.Tool) []langtools.Tool {
	combined := append([]langtools.Tool{}, tools...)
	seen := map[string]bool{}
	for _, tool := range combined {
		if tool != nil {
			seen[strings.ToUpper(tool.Name())] = true
		}
	}
	for _, tool := range executorMetaTools() {
		if seen[strings.ToUpper(tool.Name())] {
			continue
		}
		combined = append(combined, tool)
		seen[strings.ToUpper(tool.Name())] = true
	}
	return combined
}

func isExecutorMetaTool(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case strings.ToUpper(toolFinishStep), strings.ToUpper(toolAbortStep):
		return true
	default:
		return false
	}
}

type finishStepPayload struct {
	Summary string
	Reason  string
	KeyInfo []string
}

func parseFinishStepInput(raw string) (finishStepPayload, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return finishStepPayload{}, fmt.Errorf("finish_step requires a JSON payload")
	}
	var payload struct {
		Summary       string          `json:"summary"`
		Result        string          `json:"result"`
		Reason        string          `json:"reason"`
		KeyInfo       json.RawMessage `json:"key_info"`
		ImportantInfo json.RawMessage `json:"important_info"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return finishStepPayload{}, err
	}
	keyInfo := appendStringList(nil, parseStringList(payload.KeyInfo)...)
	keyInfo = appendStringList(keyInfo, parseStringList(payload.ImportantInfo)...)
	summary := strings.TrimSpace(payload.Summary)
	if summary == "" {
		summary = strings.TrimSpace(payload.Result)
	}
	reason := strings.TrimSpace(payload.Reason)
	if summary == "" {
		summary = reason
	}
	if summary == "" && len(keyInfo) > 0 {
		summary = strings.Join(keyInfo, "; ")
	}
	if summary == "" && reason == "" && len(keyInfo) == 0 {
		return finishStepPayload{}, fmt.Errorf("finish_step requires summary, key_info, or reason")
	}
	return finishStepPayload{
		Summary: summary,
		Reason:  reason,
		KeyInfo: keyInfo,
	}, nil
}

func parseAbortStepInput(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("abort_step requires a JSON payload")
	}
	reason := parseOptionalReasonInput(raw)
	if reason == "" {
		var payload struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil {
			reason = strings.TrimSpace(payload.Summary)
		}
	}
	if reason == "" {
		return "", fmt.Errorf("abort_step requires reason")
	}
	return reason, nil
}

func parseStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err == nil {
		return uniqueNonEmpty(values)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return uniqueNonEmpty([]string{value})
	}
	return nil
}

func appendStringList(dst []string, values ...string) []string {
	return uniqueNonEmpty(append(dst, values...))
}

func (e *roleCollaborativeExecutor) handleExecutorMetaTool(
	state *roleLoopState,
	action schema.AgentAction,
) executorTurnResult {
	toolName := strings.ToUpper(strings.TrimSpace(action.Tool))
	input := normalizeToolInput(action.ToolInput)

	switch toolName {
	case strings.ToUpper(toolFinishStep):
		finish, err := parseFinishStepInput(input)
		if err != nil {
			return executorTurnResult{
				Kind: executorTurnInvalidMeta,
				InvalidMetaStep: &schema.AgentStep{
					Action:      action,
					Observation: fmt.Sprintf("finish_step failed: %v", err),
				},
			}
		}
		state.ExecutorStepOutcome = "finished"
		state.ExecutorStepSummary = finish.Summary
		state.ExecutorStepKeyInfo = finish.KeyInfo
		payload, _ := json.Marshal(map[string]any{
			"status":   "finished",
			"summary":  finish.Summary,
			"reason":   finish.Reason,
			"key_info": finish.KeyInfo,
		})
		return executorTurnResult{
			Kind: executorTurnFinishStep,
			Step: &schema.AgentStep{Action: action, Observation: string(payload)},
		}

	case strings.ToUpper(toolAbortStep):
		reason, err := parseAbortStepInput(input)
		if err != nil {
			return executorTurnResult{
				Kind: executorTurnInvalidMeta,
				InvalidMetaStep: &schema.AgentStep{
					Action:      action,
					Observation: fmt.Sprintf("abort_step failed: %v", err),
				},
			}
		}
		state.ExecutorStepOutcome = "aborted"
		state.ExecutorStepSummary = reason
		state.ExecutorStepKeyInfo = nil
		payload, _ := json.Marshal(map[string]string{
			"status": "aborted",
			"reason": reason,
		})
		return executorTurnResult{
			Kind: executorTurnAbortStep,
			Step: &schema.AgentStep{Action: action, Observation: string(payload)},
		}

	default:
		return executorTurnResult{
			Kind: executorTurnInvalidMeta,
			InvalidMetaStep: &schema.AgentStep{
				Action:      action,
				Observation: fmt.Sprintf("%s is not a valid executor step meta tool", action.Tool),
			},
		}
	}
}
