package agent

import (
	"testing"
	"time"
)

// Helper: build a SessionEvent with a timestamp at offset duration from now.
func eventAt(t time.Time, eventType, content string) SessionEvent {
	return SessionEvent{
		EventID: "evt_test_" + t.Format("150405.000000000"),
		Ts:      t.Format(time.RFC3339Nano),
		Type:    eventType,
		Role:    "user",
		Content: content,
	}
}

func eventsAt(t time.Time, count int, eventType, content string) []SessionEvent {
	events := make([]SessionEvent, 0, count)
	for i := 0; i < count; i++ {
		events = append(events, eventAt(t.Add(time.Duration(i)*time.Second), eventType, content))
	}
	return events
}

func TestClassifyTurnBoundary_NoPrevEvents(t *testing.T) {
	// First-ever input: no prior events. Should default to "new" (no context to continue).
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	boundary, _ := ClassifyTurnBoundary(nil, "打开微信", now, cfg, BoundaryEpisodeContext{})
	if boundary != BoundaryNew {
		t.Fatalf("empty prev events should yield 'new', got %q", boundary)
	}
}

func TestClassifyTurnBoundary_TimeGapShort(t *testing.T) {
	// Short gap (< 3m default): strong continuation, even on a generic action verb.
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := []SessionEvent{eventAt(now.Add(-2*time.Minute), "user_input", "打开微信")}
	boundary, reason := ClassifyTurnBoundary(prev, "再发一条消息", now, cfg, BoundaryEpisodeContext{})
	if boundary != BoundaryContinue {
		t.Fatalf("short time gap should force 'continue', got %q (reason=%s)", boundary, reason)
	}
	if reason != BoundaryReasonTimeGapShort {
		t.Fatalf("expected reason %q, got %q", BoundaryReasonTimeGapShort, reason)
	}
}

func TestClassifyTurnBoundary_TimeGapLong(t *testing.T) {
	// Long gap (> 5min default): strong new-session, even with a continuation marker.
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := []SessionEvent{eventAt(now.Add(-10*time.Minute), "user_input", "打开微信")}
	boundary, reason := ClassifyTurnBoundary(prev, "对了再帮我看一下", now, cfg, BoundaryEpisodeContext{})
	if boundary != BoundaryNew {
		t.Fatalf("long time gap should force 'new', got %q (reason=%s)", boundary, reason)
	}
	if reason != BoundaryReasonTimeGapLong {
		t.Fatalf("expected reason %q, got %q", BoundaryReasonTimeGapLong, reason)
	}
}

func TestClassifyTurnBoundary_ContinuationMarker(t *testing.T) {
	// Mid-range gap + sentence-initial continuation marker → continue.
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := eventsAt(now.Add(-4*time.Minute), cfg.SmallSessionEventThreshold+1, "user_input", "查一下今天天气")
	cases := []string{
		"对了再帮我看下后天的",
		"然后呢？",
		"还有北京的天气",
		"继续往下翻",
		"接着说",
		"刚才那个网页打开",
	}
	for _, input := range cases {
		boundary, reason := ClassifyTurnBoundary(prev, input, now, cfg, BoundaryEpisodeContext{})
		if boundary != BoundaryContinue {
			t.Errorf("input %q should yield 'continue', got %q (reason=%s)", input, boundary, reason)
		}
	}
}

func TestClassifyTurnBoundary_ActionVerbStartsNewTask(t *testing.T) {
	// Mid-range gap + sentence-initial action verb on new entity → new.
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := eventsAt(now.Add(-4*time.Minute), cfg.SmallSessionEventThreshold+1, "user_input", "查一下今天天气")
	cases := []string{
		"打开微信",
		"发消息给老婆",
		"帮我订一张机票",
		"找一下附近的咖啡店",
	}
	for _, input := range cases {
		boundary, reason := ClassifyTurnBoundary(prev, input, now, cfg, BoundaryEpisodeContext{})
		if boundary != BoundaryNew {
			t.Errorf("input %q should yield 'new', got %q (reason=%s)", input, boundary, reason)
		}
	}
}

func TestClassifyTurnBoundary_AppDivergenceIsWeakNewSignal(t *testing.T) {
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := eventsAt(now.Add(-4*time.Minute), cfg.SmallSessionEventThreshold+1, "user_input", "查天气")
	for i := range prev {
		prev[i].AppName = "WeatherApp"
	}
	boundary, reason := ClassifyTurnBoundary(prev, "好的", now, cfg, BoundaryEpisodeContext{CurrentAppName: "WeChat"})
	if boundary != BoundaryNew {
		t.Fatalf("app divergence with neutral input should keep default new, got %q", boundary)
	}
	if reason != BoundaryReasonAppDivergence {
		t.Fatalf("expected app divergence reason, got %q", reason)
	}

	boundary, reason = ClassifyTurnBoundary(prev, "再帮我看后天的", now, cfg, BoundaryEpisodeContext{CurrentAppName: "WeChat"})
	if boundary != BoundaryContinue {
		t.Fatalf("continuation marker should override weak app divergence, got %q (reason=%s)", boundary, reason)
	}
}

func TestClassifyTurnBoundary_SmallSessionContinuesUnrelatedMidGap(t *testing.T) {
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := eventsAt(now.Add(-4*time.Minute), cfg.SmallSessionEventThreshold, "user_input", "查一下今天天气")

	boundary, reason := ClassifyTurnBoundary(prev, "打开微信", now, cfg, BoundaryEpisodeContext{})
	if boundary != BoundaryContinue {
		t.Fatalf("small session should continue even for unrelated action input, got %q (reason=%s)", boundary, reason)
	}
	if reason != BoundaryReasonSmallSession {
		t.Fatalf("expected reason %q, got %q", BoundaryReasonSmallSession, reason)
	}
}

func TestClassifyTurnBoundary_RunningEpisodeBiasesContinue(t *testing.T) {
	// Mid-range gap + neutral input + running episode → continue.
	// Same input + no episode → new (default bias).
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := eventsAt(now.Add(-4*time.Minute), cfg.SmallSessionEventThreshold+1, "user_input", "查一下今天天气")
	neutralInput := "好的"

	withEpisode := BoundaryEpisodeContext{HasRunning: true}
	boundary, _ := ClassifyTurnBoundary(prev, neutralInput, now, cfg, withEpisode)
	if boundary != BoundaryContinue {
		t.Errorf("running episode + neutral input should yield 'continue', got %q", boundary)
	}

	noEpisode := BoundaryEpisodeContext{HasRunning: false}
	boundary2, _ := ClassifyTurnBoundary(prev, neutralInput, now, cfg, noEpisode)
	if boundary2 != BoundaryNew {
		t.Errorf("no episode + neutral input should default to 'new', got %q", boundary2)
	}
}

func TestClassifyTurnBoundary_ActiveEpisodeBiasesNeutralFollowUpContinue(t *testing.T) {
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := eventsAt(now.Add(-4*time.Minute), cfg.SmallSessionEventThreshold+1, "user_input", "查一下今天天气")

	boundary, reason := ClassifyTurnBoundary(prev, "确认", now, cfg, BoundaryEpisodeContext{HasActive: true})
	if boundary != BoundaryContinue {
		t.Fatalf("active episode + neutral confirmation should yield 'continue', got %q (reason=%s)", boundary, reason)
	}
	if reason != BoundaryReasonActiveEpisode {
		t.Fatalf("expected reason %q, got %q", BoundaryReasonActiveEpisode, reason)
	}
}

func TestClassifyTurnBoundary_BiasFavorsFalsePositiveContinue(t *testing.T) {
	// Per design: prefer 误连 over 误切. Mid-range gap, ambiguous input (no
	// clear marker, no clear action verb) — should still bias toward 'new'
	// only when no other signal is present, but clearly continue when even
	// a single weak continuation signal appears.
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := eventsAt(now.Add(-4*time.Minute), cfg.SmallSessionEventThreshold+1, "user_input", "查一下天气")
	// Weak marker "把它" should be enough.
	boundary, _ := ClassifyTurnBoundary(prev, "把它保存下来", now, cfg, BoundaryEpisodeContext{})
	if boundary != BoundaryContinue {
		t.Errorf("weak continuation marker '把它' should yield 'continue', got %q", boundary)
	}
}

func TestClassifyTurnBoundary_MalformedTimestampFallsBack(t *testing.T) {
	// Bad Ts on prev events shouldn't crash; should fall back to scoring path
	// with no time-gap shortcut.
	cfg := DefaultBoundaryConfig()
	now := time.Now()
	prev := []SessionEvent{{
		EventID: "evt_bad",
		Ts:      "not-a-timestamp",
		Type:    "user_input",
		Role:    "user",
		Content: "查天气",
	}}
	boundary, _ := ClassifyTurnBoundary(prev, "对了再帮我看下", now, cfg, BoundaryEpisodeContext{})
	if boundary != BoundaryContinue {
		t.Errorf("malformed timestamp + continuation marker should yield 'continue', got %q", boundary)
	}
}

func TestDefaultBoundaryConfig_SaneDefaults(t *testing.T) {
	cfg := DefaultBoundaryConfig()
	if cfg.ShortGapSeconds != 180 {
		t.Errorf("ShortGapSeconds = %d, want 180", cfg.ShortGapSeconds)
	}
	if cfg.SmallSessionEventThreshold != 16 {
		t.Errorf("SmallSessionEventThreshold = %d, want 16", cfg.SmallSessionEventThreshold)
	}
	if cfg.ShortGapSeconds <= 0 {
		t.Errorf("ShortGapSeconds should be positive, got %d", cfg.ShortGapSeconds)
	}
	if cfg.LongGapSeconds <= cfg.ShortGapSeconds {
		t.Errorf("LongGapSeconds should exceed ShortGapSeconds; got short=%d long=%d",
			cfg.ShortGapSeconds, cfg.LongGapSeconds)
	}
}

func TestClassifyTurnBoundary_ZeroConfigUsesDefaults(t *testing.T) {
	now := time.Now()
	prev := []SessionEvent{eventAt(now.Add(-10*time.Second), "user_input", "打开微信")}
	boundary, reason := ClassifyTurnBoundary(prev, "打开天气", now, BoundaryConfig{}, BoundaryEpisodeContext{})
	if boundary != BoundaryContinue {
		t.Fatalf("zero config should use default short gap and continue, got %q (reason=%s)", boundary, reason)
	}
}
