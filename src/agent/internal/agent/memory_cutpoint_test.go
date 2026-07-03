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

func TestEstimateSessionEventTokensCountsCJKPerCharacter(t *testing.T) {
	// 10 CJK runes (30 UTF-8 bytes). The byte-based chars/4 heuristic would
	// underestimate this at (30+3)/4 = 8 tokens; CJK is ~1 token/char, so the
	// estimate must be at least the rune count to avoid overflowing the budget.
	cjk := strings.Repeat("你好世界平", 2)
	if got := estimateSessionEventTokens(evt("e1", "user_input", "user", cjk)); got != 10 {
		t.Fatalf("CJK tokens = %d, want 10", got)
	}

	// Mixed: 5 ASCII bytes (chars/4 path) + 2 CJK runes (1 token each).
	// want = 2 + ceil(5/4) = 2 + 2 = 4.
	if got := estimateSessionEventTokens(evt("e2", "user_input", "user", "hello你好")); got != 4 {
		t.Fatalf("mixed tokens = %d, want 4", got)
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
	if !cut.HasCut {
		t.Fatal("expected compaction cut, got HasCut=false")
	}
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

func TestPlanCompactionCountFallbackNeverOpensOnForbiddenEvent(t *testing.T) {
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 4
	m := NewMemoryManager("", WithExtractionConfig(cfg))

	// Tiny events so the token cut point finds nothing (everything fits the
	// keep-recent budget) and planCompaction falls back to the count path.
	// rawCutIndex = len(events) - keepCount = 12 - 4 = 8, which lands on a
	// tool_result. The only legal cut in the tail (a3 @ 11) shrinks the window
	// below keepCount/2, which previously dropped the plan onto the raw orphan.
	events := []SessionEvent{
		evt("u0", "user_input", "user", "hi"),
		evt("a0", "assistant_output", "assistant", "ok"),
		evt("t0", "tool_result", "tool", "r"),
		evt("u1", "user_input", "user", "hi"),
		evt("a1", "assistant_output", "assistant", "ok"),
		evt("t1", "tool_result", "tool", "r"),
		evt("u2", "user_input", "user", "hi"),
		evt("a2", "assistant_output", "assistant", "ok"),
		evt("t2", "tool_result", "tool", "r"), // rawCutIndex = 8
		evt("t3", "tool_result", "tool", "r"),
		evt("t4", "tool_result", "tool", "r"),
		evt("a3", "assistant_output", "assistant", "ok"),
	}

	plan := m.planCompaction(events, 200000)
	if !plan.ok {
		t.Fatal("expected count-based fallback to produce a plan")
	}
	if plan.mode != "count" {
		t.Fatalf("expected count mode (token path should find no cut), got %q", plan.mode)
	}
	if got := classifySessionCutEligibility(events[plan.cutIndex]); got == cutForbidden {
		t.Fatalf("hot window opens on forbidden event %q at index %d", events[plan.cutIndex].Type, plan.cutIndex)
	}
	// Cutting inside a turn must be flagged so the turn-prefix context is
	// prepended; otherwise the suffix dangles without its opening user_input.
	if events[plan.cutIndex].Type != "user_input" && !plan.isSplitTurn {
		t.Fatalf("cut at non-boundary index %d must be a split turn", plan.cutIndex)
	}
}

func TestSnapToLegalCutAtOrBeforePrefersLargestBeforeTarget(t *testing.T) {
	events := []SessionEvent{
		evt("u0", "user_input", "user", "hi"),            // 0 legal
		evt("a0", "assistant_output", "assistant", "ok"), // 1 legal
		evt("t0", "tool_result", "tool", "r"),            // 2 forbidden
		evt("t1", "tool_result", "tool", "r"),            // 3 forbidden
		evt("u1", "user_input", "user", "hi"),            // 4 legal
	}
	// target 3 (forbidden) → largest legal cut <= 3 and > start(0) is index 1.
	if got := snapToLegalCutAtOrBefore(events, 0, len(events), 3); got != 1 {
		t.Fatalf("snap(target=3) = %d, want 1", got)
	}
	// target 0 (== start) → no legal cut in (start, target]; fall forward to 1.
	if got := snapToLegalCutAtOrBefore(events, 0, len(events), 0); got != 1 {
		t.Fatalf("snap(target=0) = %d, want 1 (first legal after start)", got)
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
