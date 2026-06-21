package agent

import (
	"regexp"
	"strings"
	"time"
)

// Boundary decisions returned by ClassifyTurnBoundary.
const (
	BoundaryContinue = "continue"
	BoundaryNew      = "new"
)

// Reason codes returned alongside a boundary decision. Codes are stable
// strings so they can be logged and counted for telemetry without parsing
// natural-language explanations.
const (
	BoundaryReasonTimeGapShort     = "time_gap_short"
	BoundaryReasonTimeGapLong      = "time_gap_long"
	BoundaryReasonNoPrev           = "no_prev_events"
	BoundaryReasonContinuationWord = "continuation_word"
	BoundaryReasonActionVerb       = "action_verb"
	BoundaryReasonRunningEpisode   = "running_episode"
	BoundaryReasonSmallSession     = "small_session"
	BoundaryReasonDefaultNew       = "default_new"
	BoundaryReasonScoreContinue    = "score_continue"
)

// BoundaryConfig tunes ClassifyTurnBoundary. All fields are positive; zero
// values are replaced by DefaultBoundaryConfig at classification time.
type BoundaryConfig struct {
	// ShortGapSeconds: time since last event below this threshold forces
	// a "continue" decision regardless of input content.
	ShortGapSeconds int
	// LongGapSeconds: time since last event above this threshold forces
	// a "new" decision regardless of input content.
	LongGapSeconds int
	// SmallSessionEventThreshold: when the active session has no more than
	// this many events, keep the session together in the mid-range gap even if
	// the new input looks unrelated. Short sessions are cheap to carry and
	// splitting them early tends to lose useful context.
	SmallSessionEventThreshold int
	// ContinueScoreThreshold: total score above which a "continue" decision
	// overrides the default "new" bias in the mid-range gap. Tuned to favor
	// false-positive continuation over false-positive new, since continuation
	// has token-based compression as a backstop while a false new boundary
	// directly degrades the current turn.
	ContinueScoreThreshold int
}

// DefaultBoundaryConfig returns the sane defaults for voice agent usage.
func DefaultBoundaryConfig() BoundaryConfig {
	return BoundaryConfig{
		ShortGapSeconds:            300,
		LongGapSeconds:             1800,
		SmallSessionEventThreshold: 16,
		ContinueScoreThreshold:     2,
	}
}

// BoundaryEpisodeContext carries the minimal snapshot of episode state that
// the classifier needs. Passing this in (instead of querying a recorder)
// keeps ClassifyTurnBoundary pure and testable.
type BoundaryEpisodeContext struct {
	// HasRunning is true when there is an in-flight TaskEpisode. It biases
	// toward "continue" because the user is mid-task and is unlikely to be
	// opening a new one. Unlike a finished episode, "running" is a genuine
	// state signal that is independent of the time gap: a task is in-flight no
	// matter how long the user paused before the next turn.
	HasRunning bool
}

// continuationMarkerRe matches sentence-initial continuation cues.
// Anchored to start-of-string after optional whitespace/punctuation so the
// same words mid-sentence (where they're unlikely to be continuation cues)
// don't trigger.
var continuationMarkerRe = regexp.MustCompile(
	`(?i)^[\s,，。.!?！？]*(对了|然后|还有|继续|接着|接下来|刚才|刚刚|刚|上一个|那个|这个|把它|把那个|把刚才|再|再说|再帮|再帮我|再来|再给|再给我|also\b|and\b|then\b|continue\b|next\b|again\b|previous\b|same\b|that\b|this\b|what about\b|how about\b|one more\b|another\b)`,
)

// actionVerbStartRe matches sentence-initial action verbs that typically
// kick off a new task. The match is intentionally loose: anything starting
// with "打开/open/帮我/help me/查/search/发/send/订/book" etc. is suspect
// of opening a new task.
var actionVerbStartRe = regexp.MustCompile(
	`(?i)^[\s,，。.!?！？]*(打开|关闭|启动|帮我|给我|查一下|查看|查询|查|找一下|找|搜一下|搜索|搜|发消息|发条|发给|发|订|预订|订一张|播放|播|定个闹钟|设置|open\b|close\b|launch\b|start\b|help me\b|show me\b|check\b|look up\b|look for\b|find\b|search\b|google\b|send\b|message\b|text\b|book\b|reserve\b|order\b|play\b|set\b|create\b|turn on\b|turn off\b)`,
)

// ClassifyTurnBoundary decides whether a new user input continues the
// previous session or starts a new one. It is pure, side-effect-free, and
// uses only local signals (timestamps, regex on input, episode state).
//
// Decision flow:
//  1. No prior events: return "new" (no context to continue).
//  2. Time gap < ShortGapSeconds: return "continue" (rapid follow-up).
//  3. Time gap > LongGapSeconds: return "new" (long idle = task switch).
//  4. Mid-range with few active session events: bias "continue" to avoid
//     prematurely splitting a tiny context.
//  5. Mid-range: accumulate scoring signals; "continue" if score crosses
//     ContinueScoreThreshold, otherwise default "new".
//
// The bias deliberately favors false-positive "continue" over false-positive
// "new": accidental continuation is recoverable via token-based compression,
// but an accidental new boundary directly degrades the response by dropping
// live context.
func ClassifyTurnBoundary(
	prevEvents []SessionEvent,
	newInput string,
	now time.Time,
	cfg BoundaryConfig,
	episode BoundaryEpisodeContext,
) (boundary string, reason string) {
	cfg = normalizeBoundaryConfig(cfg)

	if episode.HasRunning && len(prevEvents) == 0 {
		return BoundaryContinue, BoundaryReasonRunningEpisode
	}

	// 1. No previous events means there is no session to continue unless an
	// episode is already running. The running episode case is handled above
	// because the active session event file may be empty after a crash or
	// pre-fix run that never flushed events.
	if len(prevEvents) == 0 {
		return BoundaryNew, BoundaryReasonNoPrev
	}

	// 2. Time gap shortcuts. Walk events in reverse to find the most recent
	// parseable timestamp; tolerate malformed Ts on individual events.
	if lastTs, ok := mostRecentEventTime(prevEvents); ok {
		gap := now.Sub(lastTs)
		if gap >= 0 && gap < time.Duration(cfg.ShortGapSeconds)*time.Second {
			return BoundaryContinue, BoundaryReasonTimeGapShort
		}
		if gap > time.Duration(cfg.LongGapSeconds)*time.Second {
			return BoundaryNew, BoundaryReasonTimeGapLong
		}
	}

	// 3. Mid-range scoring. Score accumulates "continue" weight; an action
	// verb that opens a new task counts as negative weight (toward "new").
	score := 0
	primaryReason := BoundaryReasonDefaultNew

	trimmed := strings.TrimSpace(newInput)
	smallSession := cfg.SmallSessionEventThreshold > 0 && len(prevEvents) <= cfg.SmallSessionEventThreshold

	if continuationMarkerRe.MatchString(trimmed) {
		score += 2
		primaryReason = BoundaryReasonContinuationWord
	}
	if actionVerbStartRe.MatchString(trimmed) {
		// Action verb opening usually signals a fresh task, but a sentence that
		// also starts with a continuation marker remains ambiguous and should
		// not erase the stronger continuation cue.
		if primaryReason != BoundaryReasonContinuationWord {
			score -= 2
			primaryReason = BoundaryReasonActionVerb
		}
	}
	if episode.HasRunning {
		// An in-flight episode means a neutral utterance is overwhelmingly a
		// continuation. Weight equals the continue threshold so this signal
		// alone carries the decision when no other evidence contradicts it.
		// A *finished* episode is deliberately not used here: "recently
		// finished" is just the time gap re-encoded (the episode's EndedAt is
		// the most recent event), so it carries no information the gap shortcut
		// hasn't already spent, and in the mid-range it is always true — which
		// silently collapses the scoring into a 30-minute cliff.
		score += 2
		if primaryReason == BoundaryReasonDefaultNew {
			primaryReason = BoundaryReasonRunningEpisode
		}
	}
	if smallSession {
		if score < cfg.ContinueScoreThreshold {
			score = cfg.ContinueScoreThreshold
		}
		switch primaryReason {
		case BoundaryReasonDefaultNew, BoundaryReasonActionVerb:
			primaryReason = BoundaryReasonSmallSession
		}
	}

	if score >= cfg.ContinueScoreThreshold {
		// If the dominant signal was a marker or running episode, that's
		// the more informative reason; otherwise fall back to a generic
		// score-based reason.
		if primaryReason == BoundaryReasonDefaultNew {
			primaryReason = BoundaryReasonScoreContinue
		}
		return BoundaryContinue, primaryReason
	}
	return BoundaryNew, primaryReason
}

func normalizeBoundaryConfig(cfg BoundaryConfig) BoundaryConfig {
	defaults := DefaultBoundaryConfig()
	if cfg.ShortGapSeconds <= 0 {
		cfg.ShortGapSeconds = defaults.ShortGapSeconds
	}
	if cfg.LongGapSeconds <= cfg.ShortGapSeconds {
		cfg.LongGapSeconds = defaults.LongGapSeconds
	}
	if cfg.LongGapSeconds <= cfg.ShortGapSeconds {
		cfg.LongGapSeconds = cfg.ShortGapSeconds + defaults.LongGapSeconds
	}
	if cfg.SmallSessionEventThreshold <= 0 {
		cfg.SmallSessionEventThreshold = defaults.SmallSessionEventThreshold
	}
	if cfg.ContinueScoreThreshold <= 0 {
		cfg.ContinueScoreThreshold = defaults.ContinueScoreThreshold
	}
	return cfg
}

// mostRecentEventTime returns the latest parseable Ts from the given events.
// Walks in reverse order so a single malformed entry doesn't poison the
// signal; most events are well-formed and the last one is usually fine.
func mostRecentEventTime(events []SessionEvent) (time.Time, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Ts == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, events[i].Ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
