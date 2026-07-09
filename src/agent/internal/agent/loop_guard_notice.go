package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"aiden-agent/internal/agent/context_manager"
)

// LoopGuardNoticeConfig controls soft loop-guard notices. Zero values use defaults.
type LoopGuardNoticeConfig struct {
	Enabled                        *bool
	ToolResultNoticeThreshold      int
	RepeatToolNoticeThreshold      int
	SameObservationNoticeThreshold int
	NoticeEvery                    int
}

func DefaultLoopGuardNoticeConfig() LoopGuardNoticeConfig {
	return LoopGuardNoticeConfig{
		Enabled:                        loopGuardBoolPtr(true),
		ToolResultNoticeThreshold:      6,
		RepeatToolNoticeThreshold:      3,
		SameObservationNoticeThreshold: 3,
		NoticeEvery:                    3,
	}
}

func (c LoopGuardNoticeConfig) resolved() LoopGuardNoticeConfig {
	defaults := DefaultLoopGuardNoticeConfig()
	if c.Enabled == nil {
		c.Enabled = loopGuardBoolPtr(defaults.EnabledOrDefault())
	}
	if c.ToolResultNoticeThreshold <= 0 {
		c.ToolResultNoticeThreshold = defaults.ToolResultNoticeThreshold
	}
	if c.RepeatToolNoticeThreshold <= 0 {
		c.RepeatToolNoticeThreshold = defaults.RepeatToolNoticeThreshold
	}
	if c.SameObservationNoticeThreshold <= 0 {
		c.SameObservationNoticeThreshold = defaults.SameObservationNoticeThreshold
	}
	if c.NoticeEvery <= 0 {
		c.NoticeEvery = defaults.NoticeEvery
	}
	return c
}

func (c LoopGuardNoticeConfig) EnabledOrDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func loopGuardBoolPtr(value bool) *bool {
	return &value
}

// LoopGuardNoticePolicy injects guidance after suspicious tool-result patterns.
// It never blocks tools or terminates the run; the model decides whether to stop.
type LoopGuardNoticePolicy struct {
	cfg LoopGuardNoticeConfig

	toolResults           int
	lastNoticeAt          int
	lastToolName          string
	sameToolStreak        int
	lastObservationHash   string
	sameObservationStreak int
}

func NewLoopGuardNoticePolicy(cfg LoopGuardNoticeConfig) *LoopGuardNoticePolicy {
	return &LoopGuardNoticePolicy{cfg: cfg.resolved()}
}

func (p *LoopGuardNoticePolicy) Enabled() bool {
	return p != nil && p.cfg.EnabledOrDefault()
}

func (p *LoopGuardNoticePolicy) AppendMessageHook() context_manager.AppendMessageHook {
	return func(message context_manager.Message) context_manager.AppendMessageHookResult {
		result := context_manager.AppendMessageHookResult{Message: &message}
		if p == nil || !p.Enabled() || message.Role != context_manager.MessageRoleToolResult {
			return result
		}
		if notice := p.RecordToolResultMessage(message); notice != "" {
			result.After = append(result.After, context_manager.Message{
				Role:    context_manager.MessageRoleNotice,
				Content: notice,
			})
		}
		return result
	}
}

func (p *LoopGuardNoticePolicy) RecordToolResultMessage(message context_manager.Message) string {
	if p == nil || !p.Enabled() {
		return ""
	}
	var notice string
	for _, toolResult := range message.ToolResults {
		if next := p.RecordToolResult(toolResult.Name, toolResult.Content); next != "" {
			notice = next
		}
	}
	return notice
}

func (p *LoopGuardNoticePolicy) RecordToolResult(toolName, observation string) string {
	if p == nil || !p.Enabled() {
		return ""
	}
	p.toolResults++
	toolName = strings.TrimSpace(toolName)
	if toolName != "" && strings.EqualFold(toolName, p.lastToolName) {
		p.sameToolStreak++
	} else {
		p.lastToolName = toolName
		p.sameToolStreak = 1
	}

	observationHash := loopGuardObservationHash(observation)
	if observationHash != "" && observationHash == p.lastObservationHash {
		p.sameObservationStreak++
	} else {
		p.lastObservationHash = observationHash
		p.sameObservationStreak = 1
	}
	if !p.shouldNotice() {
		return ""
	}
	p.lastNoticeAt = p.toolResults
	return p.notice(toolName)
}

func (p *LoopGuardNoticePolicy) shouldNotice() bool {
	if p == nil {
		return false
	}
	tooManyTools := p.toolResults >= p.cfg.ToolResultNoticeThreshold
	repeatingTool := p.sameToolStreak >= p.cfg.RepeatToolNoticeThreshold
	repeatingObservation := p.sameObservationStreak >= p.cfg.SameObservationNoticeThreshold
	if !tooManyTools && !repeatingTool && !repeatingObservation {
		return false
	}
	return p.lastNoticeAt == 0 || p.toolResults-p.lastNoticeAt >= p.cfg.NoticeEvery
}

func (p *LoopGuardNoticePolicy) notice(toolName string) string {
	reasons := []string{fmt.Sprintf("%d tool results have been observed", p.toolResults)}
	if p.sameToolStreak >= p.cfg.RepeatToolNoticeThreshold && strings.TrimSpace(toolName) != "" {
		reasons = append(reasons, fmt.Sprintf("%q has been used %d times in a row", toolName, p.sameToolStreak))
	}
	if p.sameObservationStreak >= p.cfg.SameObservationNoticeThreshold {
		reasons = append(reasons, fmt.Sprintf("the last observation repeated %d times", p.sameObservationStreak))
	}
	return "Loop guard notice: " + strings.Join(reasons, "; ") + ". Before calling another tool, decide whether the last results show real progress toward the user's goal. If they do not, stop retrying the same action; explain the blocker, ask for clarification, request_human_handoff, or switch to a different diagnostic step."
}

func loopGuardObservationHash(observation string) string {
	observation = strings.TrimSpace(observation)
	if observation == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(observation))
	return hex.EncodeToString(sum[:12])
}
