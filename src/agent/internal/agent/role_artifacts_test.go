package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tmc/langchaingo/schema"
)

func TestPreparedTextArtifactActionFillsMissingText(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 1,
		Plan:          []string{"prepare message", "send message"},
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:          "wechat_message_text",
			Kind:        planArtifactKindTargetText,
			Delivery:    planArtifactDeliveryClipboard,
			PrepareStep: 1,
			ConsumeStep: 2,
			TargetApp:   "WeChat",
		}}),
	}
	state.PlanArtifacts[0].PreparedText = "Please confirm whether 5550103 is still active."
	state.PlanArtifacts[0].PreparedAt = time.Now()

	action := schema.AgentAction{
		Tool:      "enter_text_in_field",
		ToolInput: `{"artifact_id":"wechat_message_text","focus":{"coord_space":"normalized","x":400,"y":959},"platform":"ios","send_after_commit":true}`,
	}

	filled := state.resolvePreparedTextArtifactAction(action)
	var payload map[string]any
	if err := json.Unmarshal([]byte(filled.ToolInput), &payload); err != nil {
		t.Fatalf("decode filled input: %v", err)
	}
	if got := payload["text"]; got != "Please confirm whether 5550103 is still active." {
		t.Fatalf("filled text = %#v", got)
	}

	call := ToolCall{
		Spec:   ToolSpec{Name: "enter_text_in_field"},
		Action: filled,
		Input:  filled.ToolInput,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("prepared text entry allowed=%v result=%#v, want allowed", allowed, result)
	}
}

func TestPreparedTextArtifactActionKeepsExplicitMismatchForContractRejection(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 1,
		Plan:          []string{"prepare message", "send message"},
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:          "wechat_message_text",
			Kind:        planArtifactKindTargetText,
			Delivery:    planArtifactDeliveryClipboard,
			PrepareStep: 1,
			ConsumeStep: 2,
			TargetApp:   "WeChat",
		}}),
	}
	state.PlanArtifacts[0].PreparedText = "Prepared text"
	state.PlanArtifacts[0].PreparedAt = time.Now()

	action := schema.AgentAction{
		Tool:      "enter_text_in_field",
		ToolInput: `{"artifact_id":"wechat_message_text","text":"Different text","focus":{"coord_space":"normalized","x":400,"y":959}}`,
	}

	filled := state.resolvePreparedTextArtifactAction(action)
	var payload map[string]any
	if err := json.Unmarshal([]byte(filled.ToolInput), &payload); err != nil {
		t.Fatalf("decode filled input: %v", err)
	}
	if got := payload["text"]; got != "Different text" {
		t.Fatalf("explicit text = %#v, want mismatch preserved", got)
	}
	call := ToolCall{
		Spec:   ToolSpec{Name: "enter_text_in_field"},
		Action: filled,
		Input:  filled.ToolInput,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil {
		t.Fatalf("mismatched text entry allowed=%v result=%#v, want rejection", allowed, result)
	}
}
