package agent

import (
	"context"
	"fmt"
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

func TestTerminationPolicyDetectsOscillatingScreenLoop(t *testing.T) {
	// Regression: tapping the same coordinate made the keyboard toggle on/off,
	// so the screenshot alternated between two states (A-B-A-B...). The old
	// guard treated every screen-hash change as progress and reset its
	// counters, so 31 identical taps never tripped. A revisit to a recently
	// seen screen must not count as progress.
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	input := `{"point":{"x":440,"y":590},"type":"tap"}`
	screenA := `{"width":496,"height":1080,"format":"jpeg","data":"screen-A"}`
	screenB := `{"width":496,"height":1080,"format":"jpeg","data":"screen-B"}`

	stopped := false
	for i := 0; i < 12; i++ {
		observation := screenA
		if i%2 == 1 {
			observation = screenB
		}
		decision := policy.AfterToolCall("touch_gesture", input, observation, false)
		if decision.Stop {
			if decision.Reason != StopReasonNoProgress && decision.Reason != StopReasonLoopDetected {
				t.Fatalf("iteration %d: unexpected stop reason: %#v", i+1, decision)
			}
			stopped = true
			if i > 7 {
				t.Fatalf("oscillation detected too late at iteration %d", i+1)
			}
			break
		}
	}
	if !stopped {
		t.Fatal("oscillating screen loop was never terminated")
	}
}

func TestTerminationPolicyAllowsGenuinelyNewScreens(t *testing.T) {
	// Same action signature producing a stream of distinct, never-before-seen
	// screens (e.g. a "next" button paging through fresh content) is real
	// progress and must not be flagged as a loop.
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	input := `{"point":{"x":900,"y":500},"type":"tap"}`
	for i := 0; i < 12; i++ {
		observation := fmt.Sprintf(`{"width":496,"height":1080,"format":"jpeg","data":"page-%d"}`, i)
		if decision := policy.AfterToolCall("touch_gesture", input, observation, false); decision.Stop {
			t.Fatalf("iteration %d: new screens must not stop: %#v", i+1, decision)
		}
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
