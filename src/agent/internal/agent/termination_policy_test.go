package agent

import (
	"context"
	"strings"
	"testing"
)

func TestTerminationPolicyRepeatWithoutProgressTerminates(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	obs := `{"width":100,"height":100,"format":"jpeg","data":"abc"}`
	for i := 0; i < 2; i++ {
		decision := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, obs, false)
		if decision.Stop {
			t.Fatalf("iteration %d: unexpected stop: %#v", i+1, decision)
		}
	}
	decision := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, obs, false)
	if !decision.Stop {
		t.Fatalf("expected terminate on repeated no-progress tool call, got %#v", decision)
	}
	if decision.Reason != StopReasonLoopDetected {
		t.Fatalf("reason = %q, want %q", decision.Reason, StopReasonLoopDetected)
	}
}

func TestTerminationPolicyProgressResetsRepeatStreak(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	first := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, `{"data":"screen-a"}`, false)
	if first.Stop {
		t.Fatalf("unexpected stop on first call: %#v", first)
	}
	second := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, `{"data":"screen-b"}`, false)
	if second.Stop {
		t.Fatalf("new screenshot data should count as progress: %#v", second)
	}
	if policy.sameResultStreak != 1 {
		t.Fatalf("sameResultStreak = %d, want 1 after progress", policy.sameResultStreak)
	}
	if policy.stallScore != 0 || policy.tier != TierNone {
		t.Fatalf("progress should reset stall state, score=%d tier=%d", policy.stallScore, policy.tier)
	}
}

func TestTerminationPolicyRestrictTierRecoversAfterProgress(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	sameScreen := `{"width":100,"height":100,"format":"jpeg","data":"same-screen"}`
	for i := 0; i < 2; i++ {
		decision := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, sameScreen, false)
		if decision.Stop {
			t.Fatalf("iteration %d: unexpected stop: %#v", i+1, decision)
		}
	}
	if policy.tier < TierRestrictTools {
		t.Fatalf("tier = %d, want restrict tier", policy.tier)
	}
	if _, allowed := policy.BeforeToolCall("touch_gesture", `{"gesture":"swipe_up"}`); allowed {
		t.Fatal("expected action tool blocked at restrict tier")
	}

	progress := policy.AfterToolCall("screenshot", `{}`, `{"width":100,"height":100,"format":"jpeg","data":"new-screen"}`, false)
	if progress.Stop {
		t.Fatalf("progress should not stop: %#v", progress)
	}
	if policy.tier != TierNone || policy.stallScore != 0 {
		t.Fatalf("progress should clear restriction, score=%d tier=%d", policy.stallScore, policy.tier)
	}
	if _, allowed := policy.BeforeToolCall("touch_gesture", `{"gesture":"swipe_up"}`); !allowed {
		t.Fatal("action tool should be allowed again after progress")
	}
}

func TestTerminationPolicyRestrictsActionToolsAtTierTwo(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	policy.stallScore = DefaultTerminationPolicyConfig().RestrictToolsStallScore
	policy.refreshTier()

	result, allowed := policy.BeforeToolCall("touch_gesture", `{"gesture":"swipe_up"}`)
	if allowed {
		t.Fatal("expected touch_gesture to be blocked at restrict tier")
	}
	if result.Error == nil || !strings.Contains(result.Output, "Loop guard blocked") {
		t.Fatalf("unexpected blocked result: %#v", result)
	}

	_, allowed = policy.BeforeToolCall("screenshot", `{}`)
	if !allowed {
		t.Fatal("screenshot should remain allowed at restrict tier")
	}
}

func TestTerminationPolicySoftNoticeOnlyOncePerTier(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	policy.stallScore = DefaultTerminationPolicyConfig().SoftNoticeStallScore
	policy.refreshTier()

	first := policy.interventionDecision()
	if first.Notice == "" {
		t.Fatal("expected soft notice")
	}
	second := policy.interventionDecision()
	if second.Notice != "" {
		t.Fatalf("expected notice only once per tier, got %q", second.Notice)
	}
}

func TestTerminationPolicyParseFailureLimit(t *testing.T) {
	policy := NewTerminationPolicy(TerminationPolicyConfig{
		ParseFailureLimit: 2,
	})
	if decision := policy.RecordParseFailure(); decision.Stop {
		t.Fatalf("first parse failure should not stop: %#v", decision)
	}
	decision := policy.RecordParseFailure()
	if !decision.Stop || decision.Reason != StopReasonParseFailure {
		t.Fatalf("second parse failure should stop: %#v", decision)
	}
}

func TestTerminationPolicyPartialConfigStaysEnabled(t *testing.T) {
	policy := NewTerminationPolicy(TerminationPolicyConfig{ParseFailureLimit: 2})
	policy.RecordParseFailure()
	decision := policy.RecordParseFailure()
	if !decision.Stop || decision.Reason != StopReasonParseFailure {
		t.Fatalf("partial config should use enabled default, got %#v", decision)
	}
}

func TestTerminationPolicyExplicitDisable(t *testing.T) {
	policy := NewTerminationPolicy(TerminationPolicyConfig{
		Enabled:           terminationPolicyBoolPtr(false),
		ParseFailureLimit: 1,
	})
	if decision := policy.RecordParseFailure(); decision.Stop {
		t.Fatalf("disabled policy should not stop: %#v", decision)
	}
}

func TestTerminationPolicySameResultRequiresSameSignature(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	observation := `{"ok":true,"value":"same"}`
	for _, input := range []string{`{"q":"a"}`, `{"q":"b"}`, `{"q":"c"}`} {
		decision := policy.AfterToolCall("web_search", input, observation, false)
		if decision.Stop {
			t.Fatalf("different inputs with same observation should not stop: %#v", decision)
		}
		if policy.sameResultStreak != 1 {
			t.Fatalf("sameResultStreak = %d, want 1 for input %s", policy.sameResultStreak, input)
		}
	}
}

func TestTerminationPolicyExternalCancel(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	decision := policy.CheckBeforeIteration(ctx, 1, 10)
	if !decision.Stop || decision.Reason != StopReasonExternal {
		t.Fatalf("expected external stop, got %#v", decision)
	}
}

func TestToolCallSignatureStable(t *testing.T) {
	a := toolCallSignature("Screenshot", ` {"x":1} `)
	b := toolCallSignature("screenshot", `{"x":1}`)
	if a != b {
		t.Fatalf("signatures differ: %q vs %q", a, b)
	}
}
