package agent

import (
	"strings"
	"testing"
)

func evt(id, typ, role, content string) SessionEvent {
	return SessionEvent{EventID: id, Type: typ, Role: role, Content: content}
}

// repeatStr returns content of roughly n*4 tokens (estimateSessionEventTokens
// uses chars/4), so callers can size events against a token budget.
func tokenSizedContent(tokens int) string {
	return strings.Repeat("x", tokens*4)
}

func TestEstimateSessionEventTokensUsesCharsOverFour(t *testing.T) {
	e := evt("e1", "user_input", "user", strings.Repeat("a", 40))
	if got := estimateSessionEventTokens(e); got != 10 {
		t.Fatalf("estimateSessionEventTokens = %d, want 10", got)
	}
	if got := estimateSessionEventTokens(evt("e2", "user_input", "user", "")); got != 0 {
		t.Fatalf("empty content tokens = %d, want 0", got)
	}
}

func TestFindValidSessionCutPointsSkipsToolResultAndSystem(t *testing.T) {
	events := []SessionEvent{
		evt("e0", "user_input", "user", "hi"),
		evt("e1", "assistant_output", "assistant", "calling tool"),
		evt("e2", "tool_result", "tool", "result"),
		evt("e3", "system_event", "system", "runtime ctx"),
		evt("e4", "user_input", "user", "again"),
	}
	cuts := findValidSessionCutPoints(events, 0, len(events))
	// Valid: e0 (user), e1 (assistant), e4 (user). Not e2 (tool_result), not e3 (system).
	want := []int{0, 1, 4}
	if len(cuts) != len(want) {
		t.Fatalf("cuts = %v, want %v", cuts, want)
	}
	for i := range want {
		if cuts[i] != want[i] {
			t.Fatalf("cuts = %v, want %v", cuts, want)
		}
	}
}

func TestFindSessionCutPointKeepsRecentTokensAtUserBoundary(t *testing.T) {
	// Three full turns; each user msg ~ 6000 tokens, assistant ~ 100 tokens.
	events := []SessionEvent{
		evt("u0", "user_input", "user", tokenSizedContent(6000)),
		evt("a0", "assistant_output", "assistant", tokenSizedContent(100)),
		evt("u1", "user_input", "user", tokenSizedContent(6000)),
		evt("a1", "assistant_output", "assistant", tokenSizedContent(100)),
		evt("u2", "user_input", "user", tokenSizedContent(6000)),
		evt("a2", "assistant_output", "assistant", tokenSizedContent(100)),
	}
	// keepRecentTokens = 10000 → walking back accumulates a2(100)+u2(6000)+a1(100)
	// = 6200 < 10000, then u1(6000) → 12200 >= 10000 at index 2 (u1).
	// Closest valid cut point at-or-after index 2 is u1 itself.
	cut := findSessionCutPoint(events, 0, len(events), 10000)
	if cut.FirstKeptIndex != 2 {
		t.Fatalf("FirstKeptIndex = %d, want 2 (u1 boundary)", cut.FirstKeptIndex)
	}
	if cut.IsSplitTurn {
		t.Fatalf("cut at user boundary must not be a split turn")
	}
}

func TestFindSessionCutPointNeverCutsAtToolResult(t *testing.T) {
	events := []SessionEvent{
		evt("u0", "user_input", "user", tokenSizedContent(100)),
		evt("a0", "assistant_output", "assistant", tokenSizedContent(100)),
		evt("u1", "user_input", "user", tokenSizedContent(100)),
		evt("a1", "assistant_output", "assistant", tokenSizedContent(8000)),
		evt("t1", "tool_result", "tool", tokenSizedContent(8000)),
		evt("a2", "assistant_output", "assistant", tokenSizedContent(100)),
	}
	cut := findSessionCutPoint(events, 0, len(events), 5000)
	kept := events[cut.FirstKeptIndex]
	if kept.Type == "tool_result" {
		t.Fatalf("cut point landed on tool_result (index %d)", cut.FirstKeptIndex)
	}
}

func TestFindSessionCutPointMarksSplitTurnInsideAssistant(t *testing.T) {
	// One long turn: user, assistant(big), tool_result(big), assistant(big).
	events := []SessionEvent{
		evt("u0", "user_input", "user", tokenSizedContent(100)),
		evt("a0", "assistant_output", "assistant", tokenSizedContent(100)),
		evt("t0", "tool_result", "tool", tokenSizedContent(100)),
		evt("u1", "user_input", "user", tokenSizedContent(100)),
		evt("a1", "assistant_output", "assistant", tokenSizedContent(8000)),
		evt("t1", "tool_result", "tool", tokenSizedContent(8000)),
		evt("a2", "assistant_output", "assistant", tokenSizedContent(100)),
	}
	// keepRecentTokens small enough to land inside the u1 turn at an assistant.
	cut := findSessionCutPoint(events, 0, len(events), 5000)
	kept := events[cut.FirstKeptIndex]
	if kept.Type != "assistant_output" {
		t.Fatalf("expected cut at assistant_output, got %s @ %d", kept.Type, cut.FirstKeptIndex)
	}
	if !cut.IsSplitTurn {
		t.Fatalf("expected split turn when cutting inside assistant output")
	}
	if cut.TurnStartIndex != 3 {
		t.Fatalf("TurnStartIndex = %d, want 3 (u1)", cut.TurnStartIndex)
	}
}

func TestFindSessionCutPointMergesLeadingSystemEventsIntoKeptRegion(t *testing.T) {
	// system_event right before the chosen user boundary must be pulled into kept.
	events := []SessionEvent{
		evt("u0", "user_input", "user", tokenSizedContent(6000)),
		evt("a0", "assistant_output", "assistant", tokenSizedContent(100)),
		evt("s0", "system_event", "system", "skill/runtime context"),
		evt("u1", "user_input", "user", tokenSizedContent(6000)),
		evt("a1", "assistant_output", "assistant", tokenSizedContent(100)),
	}
	// keepRecentTokens 6500 → back-accumulate a1(100)+u1(6000)=6100 < 6500,
	// then s0(system, ~3 tokens) → still < 6500, then a0(100) → 6200,
	// then u0(6000) → 12200 >= 6500 at index 0. Closest valid cut >= 0 is u0(0)...
	// Use a smaller budget so the cut lands at u1, then verify s0 merges in.
	cut := findSessionCutPoint(events, 0, len(events), 6050)
	// back-accumulate: a1(100)=100, u1(6000)=6100 >= 6050 at index 3 (u1).
	// closest valid cut >= 3 is u1 (3). Then merge leading system events: s0 at 2.
	if cut.FirstKeptIndex != 2 {
		t.Fatalf("FirstKeptIndex = %d, want 2 (system event merged into kept)", cut.FirstKeptIndex)
	}
}

func TestFindSessionCutPointNoCutWhenEverythingFitsBudget(t *testing.T) {
	events := []SessionEvent{
		evt("u0", "user_input", "user", tokenSizedContent(100)),
		evt("a0", "assistant_output", "assistant", tokenSizedContent(100)),
		evt("u1", "user_input", "user", tokenSizedContent(100)),
	}
	cut := findSessionCutPoint(events, 0, len(events), 20000)
	if cut.HasCut {
		t.Fatalf("expected no cut when all events fit budget, got cut at %d", cut.FirstKeptIndex)
	}
}

func TestClampTokenBudgetsStableAcrossContextWindows(t *testing.T) {
	cases := []struct {
		window            int
		wantReserveMax    int // reserve must not exceed this
		wantKeepBudgetMin int // keepRecent must leave room for reserve
	}{
		{window: 8000, wantReserveMax: 4000, wantKeepBudgetMin: 1},
		{window: 32000, wantReserveMax: 8192, wantKeepBudgetMin: 1},
		{window: 128000, wantReserveMax: 8192, wantKeepBudgetMin: 1},
		{window: 1000000, wantReserveMax: 8192, wantKeepBudgetMin: 1},
	}
	for _, tc := range cases {
		reserve, keep := clampTokenBudgets(8192, 20000, tc.window)
		if reserve > tc.wantReserveMax {
			t.Fatalf("window %d: reserve %d exceeds max %d", tc.window, reserve, tc.wantReserveMax)
		}
		if reserve < 1 || keep < tc.wantKeepBudgetMin {
			t.Fatalf("window %d: degenerate budgets reserve=%d keep=%d", tc.window, reserve, keep)
		}
		if reserve+keep > tc.window {
			t.Fatalf("window %d: reserve(%d)+keep(%d) exceeds window", tc.window, reserve, keep)
		}
	}
	// Large windows keep defaults intact.
	reserve, keep := clampTokenBudgets(8192, 20000, 128000)
	if reserve != 8192 || keep != 20000 {
		t.Fatalf("128k window should preserve defaults, got reserve=%d keep=%d", reserve, keep)
	}
}
