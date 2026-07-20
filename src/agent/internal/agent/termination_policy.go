package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"time"
)

// recentScreenWindow bounds how many distinct recent screenshots are tracked
// to detect oscillating loops (e.g. a tap that toggles the keyboard on/off,
// making the screen alternate between two states). A repeated action that only
// revisits a screen inside this window is not making progress.
const recentScreenWindow = 6

// StopReason explains why the agent loop stopped or intervened.
type StopReason string

const (
	StopReasonBudgetExceeded StopReason = "budget_exceeded"
	StopReasonLoopDetected   StopReason = "loop_detected"
	StopReasonNoProgress     StopReason = "no_progress"
	StopReasonExternal       StopReason = "external"
	StopReasonParseFailure   StopReason = "parse_failure"
)

// InterventionTier escalates from notice to tool restriction to termination.
type InterventionTier int

const (
	TierNone InterventionTier = iota
	TierSoftNotice
	TierRestrictTools
	TierTerminate
)

// TerminationPolicyConfig controls loop-guard thresholds. Zero values use defaults.
type TerminationPolicyConfig struct {
	Enabled                 *bool   `toml:"enabled,omitempty" json:"enabled,omitempty"`
	MaxSeconds              float64 `toml:"max_seconds,omitempty" json:"max_seconds,omitempty"`
	RepeatActionLimit       int     `toml:"repeat_action_limit,omitempty" json:"repeat_action_limit,omitempty"`
	SameResultLimit         int     `toml:"same_result_limit,omitempty" json:"same_result_limit,omitempty"`
	ScreenUnchangedLimit    int     `toml:"screen_unchanged_limit,omitempty" json:"screen_unchanged_limit,omitempty"`
	SoftNoticeStallScore    int     `toml:"soft_notice_stall_score,omitempty" json:"soft_notice_stall_score,omitempty"`
	RestrictToolsStallScore int     `toml:"restrict_tools_stall_score,omitempty" json:"restrict_tools_stall_score,omitempty"`
	TerminateStallScore     int     `toml:"terminate_stall_score,omitempty" json:"terminate_stall_score,omitempty"`
	ParseFailureLimit       int     `toml:"parse_failure_limit,omitempty" json:"parse_failure_limit,omitempty"`
}

func DefaultTerminationPolicyConfig() TerminationPolicyConfig {
	return TerminationPolicyConfig{
		Enabled:                 terminationPolicyBoolPtr(true),
		MaxSeconds:              0,
		RepeatActionLimit:       3,
		SameResultLimit:         3,
		ScreenUnchangedLimit:    5,
		SoftNoticeStallScore:    2,
		RestrictToolsStallScore: 4,
		TerminateStallScore:     6,
		ParseFailureLimit:       3,
	}
}

func (c TerminationPolicyConfig) resolved() TerminationPolicyConfig {
	defaults := DefaultTerminationPolicyConfig()
	if c.Enabled == nil {
		c.Enabled = terminationPolicyBoolPtr(defaults.enabled())
	}
	if c.RepeatActionLimit <= 0 {
		c.RepeatActionLimit = defaults.RepeatActionLimit
	}
	if c.SameResultLimit <= 0 {
		c.SameResultLimit = defaults.SameResultLimit
	}
	if c.ScreenUnchangedLimit <= 0 {
		c.ScreenUnchangedLimit = defaults.ScreenUnchangedLimit
	}
	if c.SoftNoticeStallScore <= 0 {
		c.SoftNoticeStallScore = defaults.SoftNoticeStallScore
	}
	if c.RestrictToolsStallScore <= 0 {
		c.RestrictToolsStallScore = defaults.RestrictToolsStallScore
	}
	if c.TerminateStallScore <= 0 {
		c.TerminateStallScore = defaults.TerminateStallScore
	}
	if c.ParseFailureLimit <= 0 {
		c.ParseFailureLimit = defaults.ParseFailureLimit
	}
	return c
}

func (c TerminationPolicyConfig) enabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c TerminationPolicyConfig) Validate() error {
	if c.MaxSeconds < 0 {
		return fmt.Errorf("termination_policy.max_seconds must be >= 0, got %g", c.MaxSeconds)
	}
	values := []struct {
		name  string
		value int
	}{
		{"repeat_action_limit", c.RepeatActionLimit},
		{"same_result_limit", c.SameResultLimit},
		{"screen_unchanged_limit", c.ScreenUnchangedLimit},
		{"soft_notice_stall_score", c.SoftNoticeStallScore},
		{"restrict_tools_stall_score", c.RestrictToolsStallScore},
		{"terminate_stall_score", c.TerminateStallScore},
		{"parse_failure_limit", c.ParseFailureLimit},
	}
	for _, item := range values {
		if item.value < 0 {
			return fmt.Errorf("termination_policy.%s must be >= 0, got %d", item.name, item.value)
		}
	}
	resolved := c.resolved()
	if resolved.SoftNoticeStallScore >= resolved.RestrictToolsStallScore ||
		resolved.RestrictToolsStallScore >= resolved.TerminateStallScore {
		return fmt.Errorf(
			"termination_policy stall scores must satisfy soft_notice < restrict_tools < terminate, got %d < %d < %d",
			resolved.SoftNoticeStallScore,
			resolved.RestrictToolsStallScore,
			resolved.TerminateStallScore,
		)
	}
	return nil
}

func terminationPolicyBoolPtr(value bool) *bool {
	return &value
}

// TerminationDecision is returned by the policy before/after each loop step.
type TerminationDecision struct {
	Stop          bool
	Reason        StopReason
	Message       string
	Tier          InterventionTier
	Notice        string
	RestrictTools bool
}

// TerminationPolicy implements budget, repeat, and progress-aware loop guards.
type TerminationPolicy struct {
	cfg       TerminationPolicyConfig
	startedAt time.Time

	lastToolSig      string
	sameSigStreak    int
	lastResultHash   string
	sameResultStreak int

	lastScreenHash             string
	screenUnchangedAfterAction int
	screenHashBeforeAction     string
	recentScreenHashes         []string

	parseFailures  int
	stallScore     int
	tier           InterventionTier
	lastNoticeTier InterventionTier

	lastToolName string
}

func NewTerminationPolicy(cfg TerminationPolicyConfig) *TerminationPolicy {
	return &TerminationPolicy{
		cfg:       cfg.resolved(),
		startedAt: time.Now(),
	}
}

func (p *TerminationPolicy) ResetForSteer() {
	if p == nil {
		return
	}
	p.startedAt = time.Now()
	p.lastToolSig = ""
	p.sameSigStreak = 0
	p.lastResultHash = ""
	p.sameResultStreak = 0
	p.lastScreenHash = ""
	p.screenUnchangedAfterAction = 0
	p.screenHashBeforeAction = ""
	p.recentScreenHashes = nil
	p.parseFailures = 0
	p.stallScore = 0
	p.tier = TierNone
	p.lastNoticeTier = TierNone
	p.lastToolName = ""
}

func (p *TerminationPolicy) CheckBeforeIteration(ctx context.Context, iteration, maxIterations int) TerminationDecision {
	if p == nil || !p.cfg.enabled() {
		return TerminationDecision{}
	}
	if ctx != nil && ctx.Err() != nil {
		return p.terminate(StopReasonExternal, "run canceled")
	}
	if p.cfg.MaxSeconds > 0 && time.Since(p.startedAt).Seconds() >= p.cfg.MaxSeconds {
		return p.terminate(StopReasonBudgetExceeded, "wall_clock budget exhausted")
	}
	if maxIterations > 0 && maxIterations < math.MaxInt && iteration > maxIterations {
		return p.terminate(StopReasonBudgetExceeded, "max_iterations budget exhausted")
	}
	if p.parseFailures >= p.cfg.ParseFailureLimit {
		return p.terminate(StopReasonParseFailure, "too many unparseable model outputs")
	}
	if p.shouldTerminate() {
		return p.terminateFromStall()
	}
	return p.interventionDecision()
}

func (p *TerminationPolicy) RecordParseFailure() TerminationDecision {
	if p == nil || !p.cfg.enabled() {
		return TerminationDecision{}
	}
	p.parseFailures++
	p.bumpStall(1)
	if p.parseFailures >= p.cfg.ParseFailureLimit {
		return p.terminate(StopReasonParseFailure, "too many unparseable model outputs")
	}
	return p.interventionDecision()
}

func (p *TerminationPolicy) BeforeToolCall(toolName, input string) (ToolResult, bool) {
	if p == nil || !p.cfg.enabled() || p.tier < TierRestrictTools {
		return ToolResult{}, true
	}
	if !isLoopRestrictedActionTool(toolName) {
		return ToolResult{}, true
	}
	message := fmt.Sprintf(
		"Loop guard blocked %q: repeated actions produced no progress. Use observation tools, request_human_handoff, or explain the blocker to the user instead of repeating UI actions.",
		strings.TrimSpace(toolName),
	)
	return ToolResult{
		Output: message,
		Error:  NewToolError(CodeToolExecutionFailed, message),
	}, false
}

func (p *TerminationPolicy) AfterToolCall(toolName, input, observation string, isError bool) TerminationDecision {
	if p == nil || !p.cfg.enabled() {
		return TerminationDecision{}
	}
	p.lastToolName = strings.TrimSpace(toolName)
	signature := toolCallSignature(toolName, input)
	resultHash := observationProgressHash(toolName, observation)
	previousSignature := p.lastToolSig
	previousResultHash := p.lastResultHash
	previousScreenHash := p.lastScreenHash
	sameSignature := signature != "" && signature == previousSignature
	madeProgress := false

	// Revisiting a screen seen in the recent window is oscillation, not
	// progress: a repeated action toggling the UI between a few states keeps
	// changing the hash but goes nowhere. Computed up front so both the
	// result-hash and screen-hash progress checks below respect it.
	screenHash := extractScreenshotDataHash(observation)
	revisitedScreen := screenHash != "" && p.seenRecentScreen(screenHash)

	if sameSignature {
		p.sameSigStreak++
	} else {
		p.lastToolSig = signature
		p.sameSigStreak = 1
	}

	if resultHash != "" && resultHash == previousResultHash && sameSignature {
		p.sameResultStreak++
	} else {
		p.lastResultHash = resultHash
		p.sameResultStreak = 1
	}
	if resultHash != "" && previousResultHash != "" && resultHash != previousResultHash && !isError && !revisitedScreen {
		madeProgress = true
	}

	if screenHash != "" {
		if previousScreenHash != "" && screenHash != previousScreenHash && !isError && !revisitedScreen {
			madeProgress = true
		}
		if isLoopRestrictedActionTool(toolName) {
			if p.screenHashBeforeAction == "" {
				p.screenHashBeforeAction = p.lastScreenHash
			}
			staticScreen := screenHash == previousScreenHash && previousScreenHash != ""
			oscillating := sameSignature && revisitedScreen && !staticScreen
			if staticScreen || oscillating {
				p.screenUnchangedAfterAction++
				p.bumpStall(2)
			} else {
				p.screenUnchangedAfterAction = 0
				p.screenHashBeforeAction = ""
			}
		}
		p.lastScreenHash = screenHash
		p.pushRecentScreen(screenHash)
	} else if isLoopRestrictedActionTool(toolName) && !isError {
		p.screenUnchangedAfterAction++
		p.bumpStall(1)
	}

	if madeProgress {
		p.recordProgress()
	}
	if !madeProgress && p.sameSigStreak >= 2 {
		p.bumpStall(1)
	}
	if !madeProgress && p.sameResultStreak >= 2 {
		p.bumpStall(2)
	}

	if p.sameSigStreak >= p.cfg.RepeatActionLimit && p.sameResultStreak >= p.cfg.SameResultLimit {
		return p.terminate(StopReasonLoopDetected, "same tool call repeated without new information")
	}
	if p.screenUnchangedAfterAction >= p.cfg.ScreenUnchangedLimit {
		return p.terminate(StopReasonNoProgress, "screen state unchanged after repeated UI actions")
	}
	if p.shouldTerminate() {
		return p.terminateFromStall()
	}
	return p.interventionDecision()
}

func (p *TerminationPolicy) BudgetExhausted(reason string) TerminationDecision {
	if p == nil {
		return TerminationDecision{
			Stop:    true,
			Reason:  StopReasonBudgetExceeded,
			Message: reason,
			Tier:    TierTerminate,
		}
	}
	return p.terminate(StopReasonBudgetExceeded, reason)
}

func (p *TerminationPolicy) bumpStall(delta int) {
	if delta <= 0 {
		return
	}
	p.stallScore += delta
	p.refreshTier()
}

func (p *TerminationPolicy) recordProgress() {
	p.stallScore = 0
	p.tier = TierNone
	p.lastNoticeTier = TierNone
	p.sameSigStreak = 1
	p.sameResultStreak = 1
	p.screenUnchangedAfterAction = 0
	p.screenHashBeforeAction = ""
	// Intentionally keep recentScreenHashes: clearing it here would let an
	// A-B-A-B oscillation reset the window on every "new" state and never be
	// recognized as a revisit.
}

// seenRecentScreen reports whether hash was observed within the recent window.
func (p *TerminationPolicy) seenRecentScreen(hash string) bool {
	if hash == "" {
		return false
	}
	for _, h := range p.recentScreenHashes {
		if h == hash {
			return true
		}
	}
	return false
}

// pushRecentScreen records hash in the bounded recent-screen window, moving an
// existing entry to the most-recent position.
func (p *TerminationPolicy) pushRecentScreen(hash string) {
	if hash == "" {
		return
	}
	for i, h := range p.recentScreenHashes {
		if h == hash {
			p.recentScreenHashes = append(p.recentScreenHashes[:i], p.recentScreenHashes[i+1:]...)
			break
		}
	}
	p.recentScreenHashes = append(p.recentScreenHashes, hash)
	if len(p.recentScreenHashes) > recentScreenWindow {
		p.recentScreenHashes = p.recentScreenHashes[len(p.recentScreenHashes)-recentScreenWindow:]
	}
}

func (p *TerminationPolicy) refreshTier() {
	switch {
	case p.stallScore >= p.cfg.TerminateStallScore:
		p.tier = TierTerminate
	case p.stallScore >= p.cfg.RestrictToolsStallScore:
		p.tier = TierRestrictTools
	case p.stallScore >= p.cfg.SoftNoticeStallScore:
		p.tier = TierSoftNotice
	default:
		p.tier = TierNone
	}
}

func (p *TerminationPolicy) shouldTerminate() bool {
	return p.tier >= TierTerminate
}

func (p *TerminationPolicy) interventionDecision() TerminationDecision {
	if p.tier <= p.lastNoticeTier {
		return TerminationDecision{Tier: p.tier, RestrictTools: p.tier >= TierRestrictTools}
	}
	notice := p.noticeForTier(p.tier)
	p.lastNoticeTier = p.tier
	return TerminationDecision{
		Tier:          p.tier,
		Notice:        notice,
		RestrictTools: p.tier >= TierRestrictTools,
	}
}

func (p *TerminationPolicy) noticeForTier(tier InterventionTier) string {
	switch tier {
	case TierSoftNotice:
		return "Loop guard: recent tool calls are repeating or not changing the screen. Change strategy, verify with observation tools, or call request_human_handoff if blocked."
	case TierRestrictTools:
		return "Loop guard: UI action tools are temporarily restricted because repeated actions produced no progress. Diagnose with screenshot/image_diff or ask the user to intervene."
	default:
		return ""
	}
}

func (p *TerminationPolicy) terminate(reason StopReason, message string) TerminationDecision {
	p.tier = TierTerminate
	return TerminationDecision{
		Stop:    true,
		Reason:  reason,
		Message: message,
		Tier:    TierTerminate,
	}
}

func (p *TerminationPolicy) terminateFromStall() TerminationDecision {
	reason := StopReasonNoProgress
	message := "agent stalled without measurable progress"
	if p.sameSigStreak >= p.cfg.RepeatActionLimit {
		reason = StopReasonLoopDetected
		message = "repeated tool calls without progress"
	}
	return p.terminate(reason, message)
}

func formatLoopGuardStopMessage(decision TerminationDecision, lastTool string) string {
	lastTool = strings.TrimSpace(lastTool)
	parts := []string{
		"I'm stopping here because the task is not making measurable progress.",
		fmt.Sprintf("Stop reason: %s (%s).", decision.Reason, decision.Message),
	}
	if lastTool != "" {
		parts = append(parts, fmt.Sprintf("Most recent tool: %s.", lastTool))
	}
	parts = append(parts, "Please review the current screen, try a different approach, or request human handoff.")
	return strings.Join(parts, " ")
}

func toolCallSignature(toolName, input string) string {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	input = strings.TrimSpace(input)
	if input == "" {
		return toolName
	}
	sum := sha256.Sum256([]byte(input))
	return toolName + ":" + hex.EncodeToString(sum[:8])
}

func observationProgressHash(toolName, observation string) string {
	observation = strings.TrimSpace(observation)
	if observation == "" {
		return ""
	}
	if screenHash := extractScreenshotDataHash(observation); screenHash != "" {
		return "screen:" + screenHash
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(strings.ToLower(strings.TrimSpace(toolName))))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(observation))
	return fmt.Sprintf("obs:%x", hash.Sum64())
}

func extractScreenshotDataHash(observation string) string {
	observation = strings.TrimSpace(observation)
	if observation == "" || !strings.HasPrefix(observation, "{") {
		return ""
	}
	var payload struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(observation), &payload); err != nil {
		return ""
	}
	data := strings.TrimSpace(payload.Data)
	if data == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:12])
}

func isLoopRestrictedActionTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "keyboard_tap", "keyboard_text", "mouse_click", "mouse_move", "mouse_scroll",
		"touch_gesture", "quick_action", "enter_text_in_field", "enter_text_via_bridge",
		"search_launch_app", toolBridgeOpenApp:
		return true
	default:
		return false
	}
}
