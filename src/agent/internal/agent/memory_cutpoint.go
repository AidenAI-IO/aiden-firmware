package agent

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// memory_cutpoint.go holds the pure functions that decide where to split the
// session event stream during compression. They are kept free of I/O so they
// can be unit-tested in isolation and reasoned about independently of the
// MemoryManager's filesystem maintenance loop.
//
// The design borrows the token-based cut-point idea from pi's compaction
// (reserve/keep-recent budgets, legal boundaries, split-turn handling) but
// stays on Aiden's events.jsonl + chunks model rather than adopting a session
// entry tree. See the task notes for the full comparison.

// estimateSessionEventTokens approximates the token cost of a single session
// event. It splits the content by script: CJK characters are counted at
// ~1 token each (their real tokenizer cost), while the remaining
// (mostly Latin/ASCII) bytes use the conventional chars/4 heuristic. The plain
// byte-based chars/4 rule underestimates CJK badly — a 3-byte UTF-8 hanzi would
// score 0.75 tokens instead of >=1 — which would let the kept window overflow
// the budget. The split estimate intentionally overestimates slightly so the
// window stays within budget rather than creeping over it.
func estimateSessionEventTokens(event SessionEvent) int {
	if len(event.Content) == 0 {
		return 0
	}
	cjkTokens := 0
	nonCJKBytes := 0
	for _, r := range event.Content {
		if isCJK(r) {
			cjkTokens++
		} else {
			nonCJKBytes += utf8.RuneLen(r)
		}
	}
	return cjkTokens + (nonCJKBytes+3)/4
}

// isCJK reports whether r belongs to a CJK script (Han ideographs plus the
// Japanese and Korean syllabaries), which tokenizers split at roughly one token
// per character rather than the chars/4 ratio that holds for Latin text.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// sessionCutEligibility classifies an event type for cut-point selection.
type sessionCutEligibility int

const (
	// cutForbidden: the event must never start a kept window because it depends
	// on a preceding event (tool_result follows its tool_call) or carries
	// ambient state that is meaningless on its own (system/screen context).
	cutForbidden sessionCutEligibility = iota
	// cutTurnBoundary: a complete turn boundary (user input). Cutting here is
	// clean and never produces a split turn.
	cutTurnBoundary
	// cutSplitAllowed: a legal cut point that sits in the middle of a turn
	// (assistant/role output, tool call). Cutting here splits the turn.
	cutSplitAllowed
)

// classifySessionCutEligibility maps an event to its cut eligibility. It keys
// off Type first (the canonical field for session events) and falls back to
// Role so older events or alternate producers still classify sensibly.
func classifySessionCutEligibility(event SessionEvent) sessionCutEligibility {
	switch event.Type {
	case "user_input":
		return cutTurnBoundary
	case "assistant_output", "role_output", "tool_call":
		return cutSplitAllowed
	case "tool_result", "system_event", "screen_context":
		return cutForbidden
	}
	// Fall back to role when the type is unknown/empty.
	switch event.Role {
	case "user":
		return cutTurnBoundary
	case "assistant":
		return cutSplitAllowed
	case "tool", "system":
		return cutForbidden
	}
	return cutForbidden
}

// isSessionStateEvent reports whether an event is pure ambient state (system or
// screen context) that should be pulled into the kept window when it
// immediately precedes the chosen cut point, rather than left dangling at the
// tail of the compacted history.
func isSessionStateEvent(event SessionEvent) bool {
	switch event.Type {
	case "system_event", "screen_context":
		return true
	}
	if event.Type == "" && event.Role == "system" {
		return true
	}
	return false
}

// findValidSessionCutPoints returns the indices in [start, end) where a kept
// window may legally begin: turn boundaries and split-allowed events. Forbidden
// events (tool_result, system/screen context) are excluded.
func findValidSessionCutPoints(events []SessionEvent, start, end int) []int {
	cuts := make([]int, 0, end-start)
	for i := start; i < end; i++ {
		if classifySessionCutEligibility(events[i]) != cutForbidden {
			cuts = append(cuts, i)
		}
	}
	return cuts
}

// findSessionTurnStartIndex walks backwards from idx (inclusive) to find the
// user_input that opens the turn containing idx. Returns -1 when no turn start
// exists at or before idx within [start, idx].
func findSessionTurnStartIndex(events []SessionEvent, idx, start int) int {
	for i := idx; i >= start; i-- {
		if classifySessionCutEligibility(events[i]) == cutTurnBoundary {
			return i
		}
	}
	return -1
}

// SessionCutPoint describes where to split the event stream.
type SessionCutPoint struct {
	// HasCut is false when every event fits within keepRecentTokens, i.e. no
	// token-driven compaction is warranted.
	HasCut bool
	// FirstKeptIndex is the index of the first event to retain in the hot
	// window. Everything before it is compacted.
	FirstKeptIndex int
	// IsSplitTurn is true when FirstKeptIndex falls inside a turn (the cut event
	// is not a user boundary), so a turn-prefix summary should be produced.
	IsSplitTurn bool
	// TurnStartIndex is the user_input opening the split turn, or -1 when not a
	// split turn.
	TurnStartIndex int
}

// findSessionCutPoint chooses the cut point that retains roughly
// keepRecentTokens of the most recent events, never cutting at a forbidden
// event. It walks backwards from the newest event accumulating estimated
// tokens; once the budget is reached it snaps to the closest legal cut point at
// or after that position. Leading ambient-state events immediately before the
// chosen cut are merged into the kept window so the hot window never opens on a
// dangling system/screen context event.
//
// Only events in [start, end) are considered.
func findSessionCutPoint(events []SessionEvent, start, end, keepRecentTokens int) SessionCutPoint {
	cuts := findValidSessionCutPoints(events, start, end)
	if len(cuts) == 0 {
		return SessionCutPoint{HasCut: false, FirstKeptIndex: start, TurnStartIndex: -1}
	}

	accumulated := 0
	reached := false
	budgetIndex := start
	for i := end - 1; i >= start; i-- {
		accumulated += estimateSessionEventTokens(events[i])
		if accumulated >= keepRecentTokens {
			budgetIndex = i
			reached = true
			break
		}
	}

	// Everything fits: no compaction needed.
	if !reached {
		return SessionCutPoint{HasCut: false, FirstKeptIndex: start, TurnStartIndex: -1}
	}

	// Snap to the closest legal cut point at or after budgetIndex. If none
	// exists after it (the tail is all forbidden events), fall back to the
	// newest legal cut point so we still keep a coherent window.
	cutIndex := cuts[len(cuts)-1]
	for _, c := range cuts {
		if c >= budgetIndex {
			cutIndex = c
			break
		}
	}

	return buildSessionCutPoint(events, start, cutIndex)
}

// buildSessionCutPoint turns a chosen legal cut index into a SessionCutPoint:
// it classifies the split-turn status from the real cut event, then merges any
// leading ambient-state events into the kept window so the hot window never
// opens on a dangling system/screen context event. cutIndex must already be a
// legal (non-forbidden) cut point in [start, end). Returns HasCut=false when the
// cut collapses to start (nothing left to compact).
func buildSessionCutPoint(events []SessionEvent, start, cutIndex int) SessionCutPoint {
	// If the chosen cut is the very first event there is no history to compact.
	if cutIndex <= start {
		return SessionCutPoint{HasCut: false, FirstKeptIndex: start, TurnStartIndex: -1}
	}

	// Determine split-turn status from the chosen cut event BEFORE merging
	// leading state events, so the turn classification reflects the real cut.
	isUserBoundary := classifySessionCutEligibility(events[cutIndex]) == cutTurnBoundary
	turnStart := -1
	if !isUserBoundary {
		turnStart = findSessionTurnStartIndex(events, cutIndex, start)
	}

	// Merge any leading ambient-state events into the kept window.
	keptIndex := cutIndex
	for keptIndex > start && isSessionStateEvent(events[keptIndex-1]) {
		keptIndex--
	}
	if keptIndex <= start {
		return SessionCutPoint{HasCut: false, FirstKeptIndex: start, TurnStartIndex: -1}
	}

	return SessionCutPoint{
		HasCut:         true,
		FirstKeptIndex: keptIndex,
		IsSplitTurn:    !isUserBoundary && turnStart != -1,
		TurnStartIndex: turnStart,
	}
}

// snapToLegalCutAtOrBefore returns the largest legal cut index in (start, end)
// that is at or before target, preferring to keep more events (snap earlier).
// When no legal cut exists at or before target it falls forward to the first
// legal cut after target. Returns -1 when no legal cut exists in (start, end)
// at all, i.e. compaction cannot make progress without orphaning an event.
//
// This lets the count-based fallback land on the same kind of legal boundary
// the token path uses, instead of an arbitrary index that may sit on a
// forbidden event (tool_result/system) and orphan the hot window.
func snapToLegalCutAtOrBefore(events []SessionEvent, start, end, target int) int {
	cuts := findValidSessionCutPoints(events, start, end)
	best := -1
	for _, c := range cuts {
		if c <= start {
			continue // index 0 leaves no history to compact
		}
		if c <= target {
			best = c // keep advancing toward target; prefer the largest
			continue
		}
		// c > target: only useful as a forward fallback when nothing fit before.
		if best == -1 {
			return c
		}
		break
	}
	return best
}

// clampTokenBudgets bounds the reserve and keep-recent token budgets so they
// remain sane across context windows from 8k to 1M. Reserve never exceeds half
// the window; keep-recent never eats into the reserve headroom. This keeps
// small-window models (e.g. 8k) from configuring a reserve larger than the
// window while leaving large-window defaults untouched.
func clampTokenBudgets(reserveTokens, keepRecentTokens, contextWindow int) (reserve, keepRecent int) {
	reserve = reserveTokens
	keepRecent = keepRecentTokens
	if reserve < 1 {
		reserve = 1
	}
	if keepRecent < 1 {
		keepRecent = 1
	}
	if contextWindow <= 0 {
		return reserve, keepRecent
	}

	// Reserve must leave at least half the window for actual context.
	if maxReserve := contextWindow / 2; reserve > maxReserve {
		reserve = maxReserve
	}
	if reserve < 1 {
		reserve = 1
	}

	// Keep-recent must fit alongside the reserve inside the window.
	if maxKeep := contextWindow - reserve; keepRecent > maxKeep {
		keepRecent = maxKeep
	}
	if keepRecent < 1 {
		keepRecent = 1
	}
	return reserve, keepRecent
}

// firstNonEmptyEventID returns the EventID of the event at idx when in range.
func firstNonEmptyEventID(events []SessionEvent, idx int) string {
	if idx < 0 || idx >= len(events) {
		return ""
	}
	return strings.TrimSpace(events[idx].EventID)
}

// sumSessionEventTokens totals the estimated tokens across events.
func sumSessionEventTokens(events []SessionEvent) int {
	total := 0
	for _, e := range events {
		total += estimateSessionEventTokens(e)
	}
	return total
}
