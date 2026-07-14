package agent

import (
	"context"
	"strings"
	"testing"
)

func TestTerminationPolicyRepeatWithoutProgressTerminates(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	observation := `{"width":100,"height":100,"format":"jpeg","data":"abc"}`
	for i := 0; i < 2; i++ {
		if decision := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, observation, false); decision.Stop {
			t.Fatalf("iteration %d: unexpected stop: %#v", i+1, decision)
		}
	}
	decision := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, observation, false)
	if !decision.Stop || decision.Reason != StopReasonLoopDetected {
		t.Fatalf("expected loop termination, got %#v", decision)
	}
}

func TestTerminationPolicyRestrictsActionToolsAndRecoversAfterProgress(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	sameScreen := `{"width":100,"height":100,"format":"jpeg","data":"same-screen"}`
	for i := 0; i < 2; i++ {
		if decision := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, sameScreen, false); decision.Stop {
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
	if progress.Stop || policy.tier != TierNone || policy.stallScore != 0 {
		t.Fatalf("progress should clear restriction, decision=%#v score=%d tier=%d", progress, policy.stallScore, policy.tier)
	}
	if _, allowed := policy.BeforeToolCall("touch_gesture", `{"gesture":"swipe_up"}`); !allowed {
		t.Fatal("action tool should be allowed after progress")
	}
}

func TestTerminationPolicyParseFailureLimit(t *testing.T) {
	policy := NewTerminationPolicy(TerminationPolicyConfig{ParseFailureLimit: 2})
	if decision := policy.RecordParseFailure(); decision.Stop {
		t.Fatalf("first parse failure should not stop: %#v", decision)
	}
	decision := policy.RecordParseFailure()
	if !decision.Stop || decision.Reason != StopReasonParseFailure {
		t.Fatalf("second parse failure should stop: %#v", decision)
	}
}

func TestTerminationPolicySameResultRequiresSameSignature(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	for _, input := range []string{`{"q":"a"}`, `{"q":"b"}`, `{"q":"c"}`} {
		decision := policy.AfterToolCall("web_search", input, `{"ok":true,"value":"same"}`, false)
		if decision.Stop || policy.sameResultStreak != 1 {
			t.Fatalf("input %s should not count as a repeated action: %#v, streak=%d", input, decision, policy.sameResultStreak)
		}
	}
}

func TestTerminationPolicyExternalCancel(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	decision := policy.CheckBeforeIteration(ctx, 1, 10)
	if !decision.Stop || decision.Reason != StopReasonExternal || !strings.Contains(decision.Message, "canceled") {
		t.Fatalf("expected external stop, got %#v", decision)
	}
}
