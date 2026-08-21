package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/llms"
)

const (
	episodeMemoryExtractorVersion = 1
	episodeMemoryTag              = "episode-memory:v1"

	deviceMemoryStatusActive     deviceMemoryStatus = "active"
	deviceMemoryStatusPending    deviceMemoryStatus = "pending"
	deviceMemoryStatusDisputed   deviceMemoryStatus = "disputed"
	deviceMemoryStatusConflicted deviceMemoryStatus = "conflicted"
)

var errEpisodeMemoryRevisionChanged = errors.New("episode memory revision changed")

type episodeGoalResult string

const (
	episodeGoalAchieved    episodeGoalResult = "achieved"
	episodeGoalNotAchieved episodeGoalResult = "not_achieved"
	episodeGoalUnknown     episodeGoalResult = "unknown"
)

type episodeMemoryType string

const (
	episodeMemoryTypeProcedure   episodeMemoryType = "procedure"
	episodeMemoryTypeNavigation  episodeMemoryType = "navigation"
	episodeMemoryTypeCalibration episodeMemoryType = "calibration"
	episodeMemoryTypeFailure     episodeMemoryType = "failure"
	episodeMemoryTypeFact        episodeMemoryType = "fact"
)

type episodeMemoryAction string

const (
	episodeMemoryActionCreate episodeMemoryAction = "create"
	episodeMemoryActionUpdate episodeMemoryAction = "update"
)

type episodeMemoryAssessment struct {
	GoalResult   episodeGoalResult `json:"goal_result" yaml:"goal_result"`
	Reason       string            `json:"reason" yaml:"reason"`
	EvidenceRefs []string          `json:"evidence_refs" yaml:"evidence_refs"`
}

type episodeMemoryCandidate struct {
	LessonKey          string              `json:"lesson_key" yaml:"lesson_key"`
	Type               episodeMemoryType   `json:"type" yaml:"type"`
	Action             episodeMemoryAction `json:"action" yaml:"action"`
	MemoryID           string              `json:"memory_id,omitempty" yaml:"memory_id,omitempty"`
	MemoryRevision     int                 `json:"memory_revision,omitempty" yaml:"memory_revision,omitempty"`
	UnresolvedConflict bool                `json:"unresolved_conflict" yaml:"unresolved_conflict"`
	ConflictReason     string              `json:"conflict_reason,omitempty" yaml:"conflict_reason,omitempty"`
	Situation          string              `json:"situation" yaml:"situation"`
	Guidance           string              `json:"guidance" yaml:"guidance"`
	ExpectedEffect     string              `json:"expected_effect" yaml:"expected_effect"`
	Scope              map[string]string   `json:"scope" yaml:"scope"`
	Tags               []string            `json:"tags" yaml:"tags"`
	EvidenceRefs       []string            `json:"evidence_refs" yaml:"evidence_refs"`
}

type episodeMemoryProposal struct {
	EpisodeAssessment episodeMemoryAssessment  `json:"episode_assessment" yaml:"episode_assessment"`
	Candidates        []episodeMemoryCandidate `json:"candidates" yaml:"candidates"`
	ExistingRevisions map[string]int           `json:"-" yaml:"existing_revisions,omitempty"`
}

func cloneEpisodeMemoryProposal(proposal episodeMemoryProposal) episodeMemoryProposal {
	cloned := proposal
	cloned.EpisodeAssessment.EvidenceRefs = append([]string(nil), proposal.EpisodeAssessment.EvidenceRefs...)
	cloned.Candidates = make([]episodeMemoryCandidate, len(proposal.Candidates))
	for i, candidate := range proposal.Candidates {
		cloned.Candidates[i] = candidate
		cloned.Candidates[i].Scope = cloneStringMap(candidate.Scope)
		cloned.Candidates[i].Tags = append([]string(nil), candidate.Tags...)
		cloned.Candidates[i].EvidenceRefs = append([]string(nil), candidate.EvidenceRefs...)
	}
	cloned.ExistingRevisions = make(map[string]int, len(proposal.ExistingRevisions))
	for id, revision := range proposal.ExistingRevisions {
		cloned.ExistingRevisions[id] = revision
	}
	return cloned
}

type episodeMemoryProcessor struct {
	plane *FilesystemMemoryPlane
	merge *MemoryMergeEngine
	state *episodeMemoryStateStore
	now   func() time.Time
	lock  string
}

var _ MemoryProcessor = (*episodeMemoryProcessor)(nil)

func newEpisodeMemoryProcessor(plane *FilesystemMemoryPlane, models model.Model) *episodeMemoryProcessor {
	return newEpisodeMemoryProcessorWithGate(plane, models, nil)
}

func newEpisodeMemoryProcessorWithGate(plane *FilesystemMemoryPlane, models model.Model, gate *MemoryRunGate) *episodeMemoryProcessor {
	bootstrapAt := time.Now().UTC()
	return &episodeMemoryProcessor{
		plane: plane,
		merge: NewMemoryMergeEngineWithGate(models, gate),
		state: newEpisodeMemoryStateStore(filepath.Join(plane.memoryDir, "lifecycle", "reflection.yaml"), bootstrapAt),
		now:   func() time.Time { return time.Now().UTC() },
		lock:  filepath.Join(plane.memoryDir, "lifecycle", "reflection.lock"),
	}
}

func (p *episodeMemoryProcessor) Initialize() error {
	if p == nil || p.state == nil {
		return nil
	}
	_, err := p.state.Snapshot()
	return err
}

func (p *episodeMemoryProcessor) logBatchError(err error) {
	if err != nil && p != nil && p.plane != nil && p.plane.logger != nil {
		p.plane.logger.Warn("[episode-memory] batch failed: %v", err)
	}
}

func (p *episodeMemoryProcessor) NextRunAt(ctx context.Context) (time.Time, error) {
	state, episodes, err := p.loadWork(ctx)
	if err != nil {
		return time.Time{}, err
	}
	now := p.now()
	var next time.Time
	for _, episode := range episodes {
		eligible, due := episodeMemoryEpisodeDue(state.Episodes[episodeMemoryStateKey(episode.ID, episodeMemoryExtractorVersion)], now)
		if eligible {
			return now, nil
		}
		if !due.IsZero() && (next.IsZero() || due.Before(next)) {
			next = due
		}
	}
	return next, nil
}

func (p *episodeMemoryProcessor) ProcessBatch(ctx context.Context, limit int, shouldStop func() bool) (episodeMemoryBatchResult, error) {
	if p == nil {
		return episodeMemoryBatchResult{}, nil
	}
	lock := &FileLock{path: p.lock}
	if err := lock.Lock(episodeMemoryBatchLockTimeout); err != nil {
		return episodeMemoryBatchResult{}, fmt.Errorf("acquire episode memory batch lock: %w", err)
	}
	result, err := p.processBatchLocked(ctx, limit, shouldStop)
	if unlockErr := lock.Unlock(); err == nil && unlockErr != nil {
		err = unlockErr
	}
	return result, err
}

func (p *episodeMemoryProcessor) processBatchLocked(ctx context.Context, limit int, shouldStop func() bool) (episodeMemoryBatchResult, error) {
	if limit <= 0 {
		limit = episodeMemoryBatchLimit
	}
	state, episodes, loadErr := p.loadWork(ctx)
	if loadErr != nil {
		return episodeMemoryBatchResult{}, loadErr
	}
	processed := 0
	result := episodeMemoryBatchResult{}
	for _, episode := range episodes {
		if shouldStop != nil && shouldStop() {
			result.HasPending = true
			break
		}
		stateKey := episodeMemoryStateKey(episode.ID, episodeMemoryExtractorVersion)
		status := state.Episodes[stateKey]
		restoreStatus := status
		eligible, due := episodeMemoryEpisodeDue(status, p.now())
		if !eligible {
			if !due.IsZero() && (result.NextRunAt.IsZero() || due.Before(result.NextRunAt)) {
				result.NextRunAt = due
			}
			continue
		}
		if status.Status == episodeMemoryStatusProcessing {
			endedAt, endErr := episodeMemoryEpisodeEndedAt(episode)
			if endErr != nil {
				return result, endErr
			}
			ignored := episodeMemoryEpisodeStatus{
				Status:           episodeMemoryStatusIgnored,
				ExtractorVersion: episodeMemoryExtractorVersion,
				LastError:        "previous extraction attempt ended before a proposal was persisted",
				AttemptCount:     max(status.AttemptCount, 1),
			}
			if completeErr := p.state.CompleteEpisode(episode.ID, endedAt, ignored); completeErr != nil {
				return result, completeErr
			}
			state.Episodes[stateKey] = ignored
			continue
		}
		if reason := invalidEpisodeMemoryReason(episode); reason != "" {
			endedAt, endErr := episodeMemoryEpisodeEndedAt(episode)
			if endErr != nil {
				return result, endErr
			}
			ignored := episodeMemoryEpisodeStatus{Status: episodeMemoryStatusIgnored, ExtractorVersion: episodeMemoryExtractorVersion, LastError: reason}
			if err := p.state.CompleteEpisode(episode.ID, endedAt, ignored); err != nil {
				return result, err
			}
			state.Episodes[stateKey] = ignored
			continue
		}
		if processed >= limit {
			result.HasPending = true
			break
		}

		var proposal episodeMemoryProposal
		extractionFailed := false
		var err error
		if status.Proposal != nil {
			proposal = cloneEpisodeMemoryProposal(*status.Proposal)
		} else {
			processing := episodeMemoryEpisodeStatus{
				Status:              episodeMemoryStatusProcessing,
				ExtractorVersion:    episodeMemoryExtractorVersion,
				ProcessingStartedAt: p.now().Format(time.RFC3339Nano),
				AttemptCount:        status.AttemptCount,
			}
			if err := p.state.SetEpisode(episode.ID, processing); err != nil {
				return result, err
			}
			proposal, err = p.proposeEpisode(ctx, episode)
			extractionFailed = err != nil
			if err == nil {
				persisted := cloneEpisodeMemoryProposal(proposal)
				status = episodeMemoryEpisodeStatus{
					Status:           episodeMemoryStatusProposed,
					ExtractorVersion: episodeMemoryExtractorVersion,
					AttemptCount:     status.AttemptCount,
					Proposal:         &persisted,
				}
				err = p.state.SetEpisode(episode.ID, status)
				if err == nil {
					restoreStatus = status
				}
			}
		}
		if err == nil {
			err = p.applyProposal(ctx, episode, proposal)
		}
		processed++
		if err != nil {
			if ctx.Err() != nil {
				if restoreErr := p.state.SetEpisode(episode.ID, restoreStatus); restoreErr != nil {
					return result, restoreErr
				}
				result.HasPending = true
				return result, nil
			}
			if extractionFailed {
				endedAt, endErr := episodeMemoryEpisodeEndedAt(episode)
				if endErr != nil {
					return result, endErr
				}
				ignored := episodeMemoryEpisodeStatus{
					Status:           episodeMemoryStatusIgnored,
					ExtractorVersion: episodeMemoryExtractorVersion,
					LastError:        truncateForLog(err.Error(), 500),
					AttemptCount:     max(status.AttemptCount, 0) + 1,
				}
				if completeErr := p.state.CompleteEpisode(episode.ID, endedAt, ignored); completeErr != nil {
					return result, completeErr
				}
				state.Episodes[stateKey] = ignored
				continue
			}
			if errors.Is(err, errEpisodeMemoryRevisionChanged) {
				retryAt := p.now()
				retry := episodeMemoryEpisodeStatus{
					Status:           episodeMemoryStatusRetry,
					ExtractorVersion: episodeMemoryExtractorVersion,
					RetryAt:          retryAt.Format(time.RFC3339Nano),
					LastError:        truncateForLog(err.Error(), 500),
					AttemptCount:     status.AttemptCount,
				}
				if setErr := p.state.SetEpisode(episode.ID, retry); setErr != nil {
					return result, setErr
				}
				state.Episodes[stateKey] = retry
				result.HasPending = true
				if result.NextRunAt.IsZero() || retryAt.Before(result.NextRunAt) {
					result.NextRunAt = retryAt
				}
				continue
			}
			attemptCount := status.AttemptCount + 1
			if attemptCount >= episodeMemoryMaxAttempts {
				endedAt, endErr := episodeMemoryEpisodeEndedAt(episode)
				if endErr != nil {
					return result, endErr
				}
				ignored := episodeMemoryEpisodeStatus{
					Status:           episodeMemoryStatusIgnored,
					ExtractorVersion: episodeMemoryExtractorVersion,
					LastError:        truncateForLog(err.Error(), 500),
					AttemptCount:     attemptCount,
				}
				if completeErr := p.state.CompleteEpisode(episode.ID, endedAt, ignored); completeErr != nil {
					return result, completeErr
				}
				state.Episodes[stateKey] = ignored
				continue
			}
			retryAt := p.now().Add(episodeMemoryRetryDelay)
			retry := episodeMemoryEpisodeStatus{
				Status:           episodeMemoryStatusRetry,
				ExtractorVersion: episodeMemoryExtractorVersion,
				RetryAt:          retryAt.Format(time.RFC3339Nano),
				LastError:        truncateForLog(err.Error(), 500),
				AttemptCount:     attemptCount,
			}
			if status.Proposal != nil {
				persisted := cloneEpisodeMemoryProposal(*status.Proposal)
				retry.Proposal = &persisted
			}
			if setErr := p.state.SetEpisode(episode.ID, retry); setErr != nil {
				return result, setErr
			}
			state.Episodes[stateKey] = retry
			if result.NextRunAt.IsZero() || retryAt.Before(result.NextRunAt) {
				result.NextRunAt = retryAt
			}
			continue
		}
		endedAt, endErr := episodeMemoryEpisodeEndedAt(episode)
		if endErr != nil {
			return result, endErr
		}
		assessment := proposal.EpisodeAssessment
		assessment.EvidenceRefs = append([]string(nil), proposal.EpisodeAssessment.EvidenceRefs...)
		completed := episodeMemoryEpisodeStatus{
			Status:           episodeMemoryStatusDone,
			ExtractorVersion: episodeMemoryExtractorVersion,
			Assessment:       &assessment,
		}
		if err := p.state.CompleteEpisode(episode.ID, endedAt, completed); err != nil {
			return result, err
		}
		state.Episodes[stateKey] = completed
	}
	return result, nil
}

func (p *episodeMemoryProcessor) loadWork(ctx context.Context) (episodeMemoryStateFile, []TaskEpisode, error) {
	if p == nil || p.plane == nil || p.plane.episodes == nil || p.plane.device == nil || p.merge == nil {
		return episodeMemoryStateFile{}, nil, nil
	}
	state, err := p.state.Snapshot()
	if err != nil {
		return episodeMemoryStateFile{}, nil, err
	}
	enabledAt, err := time.Parse(time.RFC3339Nano, state.EnabledAt)
	if err != nil {
		return episodeMemoryStateFile{}, nil, fmt.Errorf("parse episode memory enabled_at: %w", err)
	}
	episodes, err := p.plane.episodes.listCompletedEpisodesSince(ctx, enabledAt, func(entry episodeIndexEntry, endedAt time.Time) bool {
		episodeStatus := state.Episodes[episodeMemoryStateKey(entry.ID, episodeMemoryExtractorVersion)]
		status := episodeStatus.Status
		if episodeStatus.ExtractorVersion == episodeMemoryExtractorVersion && (status == episodeMemoryStatusDone || status == episodeMemoryStatusIgnored) {
			return false
		}
		if episodeStatus.ExtractorVersion == episodeMemoryExtractorVersion && (status == episodeMemoryStatusProcessing || status == episodeMemoryStatusRetry || status == episodeMemoryStatusProposed) {
			return true
		}
		return episodeMemoryEntryAfterCursor(entry.ID, endedAt, state)
	})
	return state, episodes, err
}

func (p *episodeMemoryProcessor) proposeEpisode(ctx context.Context, episode TaskEpisode) (episodeMemoryProposal, error) {
	payload, err := json.MarshalIndent(episodeMemoryPayload(episode), "", "  ")
	if err != nil {
		return episodeMemoryProposal{}, err
	}
	var existing []DeviceMemoryItem
	_, raw, err := p.merge.Extract(ctx, MemoryMergeRequest{
		Search: func(ctx context.Context) ([]MemoryMergeReference, error) {
			var err error
			existing, err = p.plane.device.SearchEpisodeMemoryCandidates(ctx, EpisodeMemoryCandidateQuery{
				Terms:        episodeMemorySearchTerms(episode),
				PreferredIDs: episode.RetrievedMemoryRefs,
				DeviceID:     firstNonEmptyString([]string{episode.DeviceScope["device_id"], defaultMemoryDeviceID}),
				Scope:        episodeMemoryRetrievalScope(episode),
				Limit:        8,
				CharBudget:   12000,
			})
			if err != nil {
				return nil, err
			}
			refs := make([]MemoryMergeReference, 0, len(existing))
			for _, item := range existing {
				refs = append(refs, MemoryMergeReference{Scope: "device", ID: item.ID, Type: item.Type, Status: string(item.Status), Title: item.Title, Summary: item.Summary, Content: item.Content, Tags: item.Tags, Entities: item.Entities, Revision: effectiveDeviceMemoryRevision(item)})
			}
			return refs, nil
		},
		BuildMessages: func(_ []MemoryMergeReference) ([]llms.MessageContent, error) {
			parts := []llms.ContentPart{llms.TextPart(buildEpisodeMemoryPrompt(string(payload), existing))}
			for _, screenshot := range loadEpisodeMemoryScreenshots(p.plane.episodes.rootDir, episode) {
				parts = append(parts, llms.TextPart("Attached screenshot evidence for Episode event id: "+screenshot.EventID))
				parts = append(parts, llms.BinaryContent{MIMEType: screenshot.MIMEType, Data: screenshot.Data})
			}
			return []llms.MessageContent{
				llms.TextParts(llms.ChatMessageTypeSystem, "You assess completed device task episodes and extract reusable device memories. Output JSON only."),
				{Role: llms.ChatMessageTypeHuman, Parts: parts},
			}, nil
		},
		MaxTokens: 2200,
		Timeout:   episodeMemoryModelCallTimeout,
	})
	if err != nil {
		return episodeMemoryProposal{}, fmt.Errorf("extract episode memory: %w", err)
	}
	var proposal episodeMemoryProposal
	if err := json.Unmarshal([]byte(raw), &proposal); err != nil {
		return episodeMemoryProposal{}, fmt.Errorf("parse episode memory proposal: %w", err)
	}
	proposal.EpisodeAssessment.GoalResult = episodeGoalResult(strings.ToLower(strings.TrimSpace(string(proposal.EpisodeAssessment.GoalResult))))
	proposal.EpisodeAssessment.Reason = strings.TrimSpace(proposal.EpisodeAssessment.Reason)
	switch proposal.EpisodeAssessment.GoalResult {
	case episodeGoalAchieved, episodeGoalNotAchieved, episodeGoalUnknown:
	default:
		return episodeMemoryProposal{}, fmt.Errorf("invalid episode goal_result %q", proposal.EpisodeAssessment.GoalResult)
	}
	proposal.EpisodeAssessment.EvidenceRefs = validEpisodeMemoryEventIDs(episode, proposal.EpisodeAssessment.EvidenceRefs)
	if proposal.EpisodeAssessment.Reason == "" {
		return episodeMemoryProposal{}, fmt.Errorf("episode assessment requires a reason")
	}
	if proposal.EpisodeAssessment.GoalResult != episodeGoalUnknown && !hasDirectEpisodeAssessmentEvidence(episode, proposal.EpisodeAssessment.EvidenceRefs) {
		return episodeMemoryProposal{}, fmt.Errorf("episode assessment %s requires direct evidence", proposal.EpisodeAssessment.GoalResult)
	}
	if len(proposal.Candidates) > 3 {
		proposal.Candidates = proposal.Candidates[:3]
	}
	proposal.ExistingRevisions = make(map[string]int, len(existing))
	for _, item := range existing {
		proposal.ExistingRevisions[item.ID] = effectiveDeviceMemoryRevision(item)
	}
	return proposal, nil
}

func hasDirectEpisodeAssessmentEvidence(episode TaskEpisode, refs []string) bool {
	allowed := make(map[string]bool, len(refs))
	for _, ref := range refs {
		allowed[ref] = true
	}
	for _, event := range episode.Events {
		if !allowed[event.EventID] {
			continue
		}
		if event.Type == "tool_result" || event.Type == "steer" || event.ToolError != nil || event.IsError || strings.TrimSpace(event.ScreenshotRef) != "" {
			return true
		}
	}
	return false
}

type episodeMemoryEventPayload struct {
	EventID       string     `json:"event_id,omitempty"`
	Type          string     `json:"type"`
	Role          string     `json:"role,omitempty"`
	Objective     string     `json:"objective,omitempty"`
	ToolName      string     `json:"tool_name,omitempty"`
	ToolInput     string     `json:"tool_input,omitempty"`
	ToolError     *ToolError `json:"tool_error,omitempty"`
	IsError       bool       `json:"is_error,omitempty"`
	Content       string     `json:"content,omitempty"`
	Observation   string     `json:"observation,omitempty"`
	ScreenshotRef string     `json:"screenshot_ref,omitempty"`
	Reason        string     `json:"reason,omitempty"`
}

func episodeMemoryPayload(episode TaskEpisode) map[string]interface{} {
	screenshotEvents := make(map[string]string)
	for _, event := range episode.Events {
		if ref := strings.TrimSpace(event.ScreenshotRef); ref != "" {
			screenshotEvents[ref] = event.EventID
		}
	}
	events := episode.Events
	if len(events) > 60 {
		events = events[len(events)-60:]
	}
	views := make([]episodeMemoryEventPayload, 0, len(events))
	for _, event := range events {
		view := episodeMemoryEventPayload{
			EventID:     event.EventID,
			Type:        event.Type,
			Role:        event.Role,
			Objective:   truncateForLog(event.Objective, 500),
			ToolName:    event.ToolName,
			ToolInput:   truncateForLog(event.ToolInput, 800),
			ToolError:   event.ToolError,
			IsError:     event.IsError,
			Content:     truncateForLog(sanitizeEpisodeMemoryScreenshotPaths(event.Content, screenshotEvents), 1200),
			Observation: truncateForLog(sanitizeEpisodeMemoryScreenshotPaths(event.Observation, screenshotEvents), 1600),
			Reason:      truncateForLog(event.Reason, 800),
		}
		if strings.TrimSpace(event.ScreenshotRef) != "" {
			view.ScreenshotRef = "attached screenshot for event " + event.EventID
		}
		views = append(views, view)
	}
	return map[string]interface{}{
		"episode_id":   episode.ID,
		"user_goal":    episode.UserGoal,
		"device_scope": episode.DeviceScope,
		"recorded_outcome": map[string]interface{}{
			"success":        episode.Outcome.Success,
			"final_state":    sanitizeEpisodeMemoryScreenshotPaths(episode.Outcome.FinalState, screenshotEvents),
			"final_answer":   sanitizeEpisodeMemoryScreenshotPaths(episode.Outcome.FinalAnswer, screenshotEvents),
			"failure_reason": sanitizeEpisodeMemoryScreenshotPaths(episode.Outcome.FailureReason, screenshotEvents),
		},
		"tags":              episode.Tags,
		"entities":          episode.Entities,
		"recalled_memories": episode.RetrievedMemoryRefs,
		"events":            views,
	}
}

func (p *episodeMemoryProcessor) applyProposal(ctx context.Context, episode TaskEpisode, proposal episodeMemoryProposal) error {
	seenLessonKeys := map[string]bool{}
	for _, raw := range proposal.Candidates {
		candidate, ok := validateEpisodeMemoryCandidate(episode, proposal.EpisodeAssessment, raw, seenLessonKeys)
		if !ok {
			continue
		}
		seenLessonKeys[candidate.LessonKey] = true
		switch candidate.Action {
		case episodeMemoryActionCreate:
			if _, err := p.createMemory(ctx, episode, candidate); err != nil {
				return err
			}
		case episodeMemoryActionUpdate:
			if revision, ok := proposal.ExistingRevisions[candidate.MemoryID]; !ok || candidate.MemoryRevision != revision {
				continue
			}
			current, found, err := p.plane.device.Get(ctx, candidate.MemoryID)
			if err != nil {
				return err
			}
			if found && hasEpisodeEvidence(current.EvidenceRefs, episode.ID) {
				continue
			}
			if err := p.updateMemory(ctx, episode, candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateEpisodeMemoryCandidate(episode TaskEpisode, assessment episodeMemoryAssessment, candidate episodeMemoryCandidate, seen map[string]bool) (episodeMemoryCandidate, bool) {
	candidate.LessonKey = strings.TrimSpace(candidate.LessonKey)
	candidate.Type = episodeMemoryType(strings.ToLower(strings.TrimSpace(string(candidate.Type))))
	candidate.Action = episodeMemoryAction(strings.ToLower(strings.TrimSpace(string(candidate.Action))))
	candidate.MemoryID = strings.TrimSpace(candidate.MemoryID)
	candidate.Situation = strings.TrimSpace(candidate.Situation)
	candidate.Guidance = strings.TrimSpace(candidate.Guidance)
	candidate.ExpectedEffect = strings.TrimSpace(candidate.ExpectedEffect)
	candidate.ConflictReason = strings.TrimSpace(candidate.ConflictReason)
	candidate.Scope = normalizeEpisodeMemoryScope(candidate.Scope)
	if candidate.LessonKey == "" || seen[candidate.LessonKey] {
		return episodeMemoryCandidate{}, false
	}
	switch candidate.Type {
	case episodeMemoryTypeProcedure, episodeMemoryTypeNavigation, episodeMemoryTypeCalibration, episodeMemoryTypeFailure, episodeMemoryTypeFact:
	default:
		return episodeMemoryCandidate{}, false
	}
	if candidate.Action != episodeMemoryActionCreate && candidate.Action != episodeMemoryActionUpdate {
		return episodeMemoryCandidate{}, false
	}
	if candidate.Action == episodeMemoryActionUpdate && candidate.MemoryID == "" {
		return episodeMemoryCandidate{}, false
	}
	if candidate.Action == episodeMemoryActionCreate && (candidate.MemoryID != "" || candidate.UnresolvedConflict) {
		return episodeMemoryCandidate{}, false
	}
	if candidate.Action == episodeMemoryActionCreate {
		candidate.MemoryRevision = 0
	}
	if candidate.UnresolvedConflict && candidate.ConflictReason == "" {
		return episodeMemoryCandidate{}, false
	}
	if !candidate.UnresolvedConflict {
		candidate.ConflictReason = ""
	}
	if candidate.Situation == "" || candidate.Guidance == "" || candidate.ExpectedEffect == "" || len(candidate.Scope) == 0 {
		return episodeMemoryCandidate{}, false
	}
	originalRefs := uniqueNonEmpty(candidate.EvidenceRefs)
	candidate.EvidenceRefs = validEpisodeMemoryEventIDs(episode, originalRefs)
	if len(candidate.EvidenceRefs) == 0 || len(candidate.EvidenceRefs) != len(originalRefs) {
		return episodeMemoryCandidate{}, false
	}
	candidate.EvidenceRefs = expandEpisodeMemoryEvidenceRefs(episode, candidate.EvidenceRefs)
	if !episodeMemoryTypeEvidenceValid(candidate.Type, episode, candidate.EvidenceRefs, assessment) {
		return episodeMemoryCandidate{}, false
	}
	if candidate.Type == episodeMemoryTypeProcedure && assessment.GoalResult == episodeGoalNotAchieved && !isPartialProcedureScope(candidate.Scope) {
		return episodeMemoryCandidate{}, false
	}
	if containsTemporaryEpisodeValue(candidate) {
		return episodeMemoryCandidate{}, false
	}
	candidate.Tags = normalizeEpisodeMemoryTags(candidate.Tags)
	return candidate, true
}

func expandEpisodeMemoryEvidenceRefs(episode TaskEpisode, refs []string) []string {
	expanded := append([]string(nil), refs...)
	pendingCalls := make(map[string][]string)
	pairedCalls := make(map[string]string)
	for _, event := range episode.Events {
		name := strings.ToLower(strings.TrimSpace(event.ToolName))
		if name == "" || !isEpisodeMemoryDeviceTool(name) {
			continue
		}
		switch event.Type {
		case runEventToolCall:
			pendingCalls[name] = append(pendingCalls[name], event.EventID)
		case "tool_result":
			calls := pendingCalls[name]
			if len(calls) == 0 {
				continue
			}
			pairedCalls[event.EventID] = calls[len(calls)-1]
			pendingCalls[name] = calls[:len(calls)-1]
		}
	}
	for _, ref := range refs {
		if callID := pairedCalls[ref]; callID != "" {
			expanded = appendUniqueString(expanded, callID)
		}
	}
	return expanded
}

func episodeMemoryTypeEvidenceValid(memoryType episodeMemoryType, episode TaskEpisode, refs []string, assessment episodeMemoryAssessment) bool {
	events := make(map[string]TaskEpisodeEvent, len(episode.Events))
	for _, event := range episode.Events {
		events[event.EventID] = event
	}
	callCount, resultCount, observationCount := 0, 0, 0
	hasCoordinateInput, hasProblem := false, false
	for _, ref := range refs {
		event := events[ref]
		switch event.Type {
		case runEventToolCall:
			if isEpisodeMemoryDeviceTool(event.ToolName) {
				callCount++
				input := strings.ToLower(event.ToolInput)
				hasCoordinateInput = hasCoordinateInput || strings.Contains(input, `"x"`) || strings.Contains(input, `"y"`) || strings.Contains(input, "coord") || strings.Contains(input, "point")
			}
		case "tool_result":
			resultCount++
			if strings.TrimSpace(event.Content) != "" || strings.TrimSpace(event.Observation) != "" || strings.TrimSpace(event.ScreenshotRef) != "" {
				observationCount++
			}
		case "steer":
			hasProblem = true
		}
		if event.Type != "tool_result" && (strings.TrimSpace(event.Observation) != "" || strings.TrimSpace(event.ScreenshotRef) != "") {
			observationCount++
		}
		if event.IsError || (event.ToolError != nil && event.ToolError.Code != CodeCanceled) {
			hasProblem = true
		}
	}
	switch memoryType {
	case episodeMemoryTypeProcedure:
		return callCount >= 2 && resultCount >= 2 && observationCount >= 1
	case episodeMemoryTypeNavigation:
		return callCount >= 1 && resultCount >= 1 && observationCount >= 2
	case episodeMemoryTypeCalibration:
		return callCount >= 1 && resultCount >= 1 && hasCoordinateInput && observationCount >= 1
	case episodeMemoryTypeFailure:
		return hasProblem || (assessment.GoalResult == episodeGoalNotAchieved && episodeMemoryRefsOverlap(refs, assessment.EvidenceRefs))
	case episodeMemoryTypeFact:
		return resultCount >= 1 && observationCount >= 1
	default:
		return false
	}
}

func episodeMemoryRefsOverlap(left, right []string) bool {
	seen := make(map[string]bool, len(left))
	for _, ref := range left {
		seen[strings.TrimSpace(ref)] = true
	}
	for _, ref := range right {
		if seen[strings.TrimSpace(ref)] {
			return true
		}
	}
	return false
}

func isPartialProcedureScope(scope map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(scope["partial"]), "true") || strings.EqualFold(strings.TrimSpace(scope["goal_scope"]), "partial")
}

func containsTemporaryEpisodeValue(candidate episodeMemoryCandidate) bool {
	text := strings.ToLower(strings.Join([]string{candidate.Situation, candidate.Guidance, candidate.ExpectedEffect}, " "))
	for _, marker := range []string{"one-time password", "temporary verification code", "一次性验证码", "临时验证码"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func normalizeEpisodeMemoryTags(tags []string) []string {
	result := []string{episodeMemoryTag}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || strings.EqualFold(tag, episodeMemoryTag) {
			continue
		}
		result = appendUniqueString(result, truncateForLog(tag, 48))
		if len(result) == 9 {
			break
		}
	}
	return result
}

func normalizeEpisodeMemoryScope(scope map[string]string) map[string]string {
	result := make(map[string]string, len(scope))
	for key, value := range scope {
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			result[key] = value
		}
	}
	return result
}

func (p *episodeMemoryProcessor) createMemory(ctx context.Context, episode TaskEpisode, candidate episodeMemoryCandidate) (string, error) {
	if existing, found, err := p.plane.device.FindEpisodeMemoryByLesson(ctx, episode.ID, candidate.LessonKey); err != nil {
		return "", err
	} else if found {
		return existing.ID, nil
	}
	deviceID := firstNonEmptyString([]string{candidate.Scope["device_id"], episode.DeviceScope["device_id"], defaultMemoryDeviceID})
	existingID := ""
	if scoped, equivalent, found, err := p.findMemoryInScope(ctx, candidate, deviceID); err != nil {
		return "", err
	} else if found {
		if !equivalent {
			return scoped.ID, nil
		}
		existingID = scoped.ID
	}
	priority, confidence, ttl := episodeMemoryDefaults(candidate.Type)
	item := DeviceMemoryItem{
		ID:               firstNonEmptyString([]string{existingID, "devmem_" + stableMemoryID(episode.ID, candidate.LessonKey)}),
		Type:             string(candidate.Type),
		Status:           deviceMemoryStatusActive,
		Revision:         1,
		ExtractorVersion: episodeMemoryExtractorVersion,
		LessonKey:        candidate.LessonKey,
		Title:            truncateForLog(candidate.Situation, 120),
		Summary:          truncateForLog(candidate.Guidance, 240),
		Content:          renderEpisodeMemoryContent(candidate),
		DeviceID:         deviceID,
		AppName:          candidate.Scope["app_name"],
		PageName:         candidate.Scope["page_name"],
		Tags:             candidate.Tags,
		Entities:         append([]string(nil), episode.Entities...),
		Confidence:       confidence,
		Priority:         priority,
		TTL:              ttl,
		Applicability:    cloneStringMap(candidate.Scope),
		EvidenceRefs:     []MemorySourceRef{episodeMemoryEvidenceRef(episode, candidate.EvidenceRefs)},
	}
	if candidate.Type == episodeMemoryTypeProcedure {
		item.Steps = episodeMemoryProcedureSteps(episode, candidate.EvidenceRefs)
	}
	result, err := p.plane.device.ApplyMemoryIntent(ctx, MemoryIntent{DeviceItem: &item})
	return result.ID, err
}

func (p *episodeMemoryProcessor) findMemoryInScope(ctx context.Context, candidate episodeMemoryCandidate, deviceID string) (DeviceMemoryItem, bool, bool, error) {
	items, err := p.plane.device.readAll()
	if err != nil {
		return DeviceMemoryItem{}, false, false, err
	}
	for _, item := range items {
		select {
		case <-ctx.Done():
			return DeviceMemoryItem{}, false, false, ctx.Err()
		default:
		}
		if item.Type != string(candidate.Type) || (item.Status != deviceMemoryStatusActive && item.Status != deviceMemoryStatusDisputed) {
			continue
		}
		if item.DeviceID != "" && deviceID != "" && !strings.EqualFold(item.DeviceID, deviceID) {
			continue
		}
		if !equalEpisodeMemoryScope(item.Applicability, candidate.Scope) {
			continue
		}
		equivalent := normalizeEpisodeMemoryText(item.Title) == normalizeEpisodeMemoryText(candidate.Situation) &&
			normalizeEpisodeMemoryText(item.Summary) == normalizeEpisodeMemoryText(candidate.Guidance)
		return item, equivalent, true, nil
	}
	return DeviceMemoryItem{}, false, false, nil
}

func equalEpisodeMemoryScope(left, right map[string]string) bool {
	left = normalizeEpisodeMemoryScope(left)
	right = normalizeEpisodeMemoryScope(right)
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if !strings.EqualFold(value, right[key]) {
			return false
		}
	}
	return true
}

func normalizeEpisodeMemoryText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func (p *episodeMemoryProcessor) updateMemory(ctx context.Context, episode TaskEpisode, candidate episodeMemoryCandidate) error {
	existing, found, err := p.plane.device.Get(ctx, candidate.MemoryID)
	if err != nil {
		return err
	}
	if !found || existing.Type != string(candidate.Type) {
		return nil
	}
	if effectiveDeviceMemoryRevision(existing) != candidate.MemoryRevision {
		return fmt.Errorf("%w: device memory %s changed from revision %d", errEpisodeMemoryRevisionChanged, candidate.MemoryID, candidate.MemoryRevision)
	}
	deviceID := firstNonEmptyString([]string{candidate.Scope["device_id"], episode.DeviceScope["device_id"], defaultMemoryDeviceID})
	if existing.DeviceID != "" && deviceID != "" && !strings.EqualFold(existing.DeviceID, deviceID) {
		return nil
	}
	newStatus := deviceMemoryStatusActive
	if candidate.UnresolvedConflict {
		newStatus = deviceMemoryStatusDisputed
	}
	newTitle := truncateForLog(candidate.Situation, 120)
	newSummary := truncateForLog(candidate.Guidance, 240)
	newContent := renderEpisodeMemoryContent(candidate)
	newScope := cloneStringMap(candidate.Scope)
	var newSteps []ProcedureStep
	if candidate.Type == episodeMemoryTypeProcedure {
		newSteps = mergeEpisodeMemorySteps(existing.Steps, episodeMemoryProcedureSteps(episode, candidate.EvidenceRefs))
	}
	item := DeviceMemoryItem{
		ID: candidate.MemoryID, Type: string(candidate.Type), Status: newStatus,
		Revision: candidate.MemoryRevision, ExtractorVersion: episodeMemoryExtractorVersion,
		Title: newTitle, Summary: newSummary, Content: newContent, DeviceID: deviceID,
		AppName: candidate.Scope["app_name"], PageName: candidate.Scope["page_name"],
		Tags: normalizeEpisodeMemoryTags(candidate.Tags), Applicability: newScope,
		EvidenceRefs: []MemorySourceRef{episodeMemoryEvidenceRef(episode, candidate.EvidenceRefs)},
		Steps:        newSteps,
	}
	if candidate.UnresolvedConflict {
		item.ConflictsWith = []string{episode.ID}
	}
	_, err = p.plane.device.ApplyMemoryIntent(ctx, MemoryIntent{DeviceItem: &item, ExpectedRevision: candidate.MemoryRevision})
	return err
}

func equalEpisodeMemorySteps(left, right []ProcedureStep) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mergeEpisodeMemorySteps(existing, observed []ProcedureStep) []ProcedureStep {
	merged := append([]ProcedureStep(nil), existing...)
	for _, step := range observed {
		duplicate := false
		for _, current := range merged {
			if current == step {
				duplicate = true
				break
			}
		}
		if !duplicate {
			merged = append(merged, step)
		}
	}
	return merged
}

func effectiveDeviceMemoryRevision(item DeviceMemoryItem) int {
	if item.Revision > 0 {
		return item.Revision
	}
	return 1
}

func episodeMemorySearchTerms(episode TaskEpisode) []string {
	terms := []string{episode.UserGoal}
	terms = append(terms, episode.Tags...)
	terms = append(terms, episode.Entities...)
	for _, event := range episode.Events {
		if event.Type == runEventToolCall {
			terms = append(terms, event.ToolName)
		}
		if event.Type == "tool_result" || event.Type == "steer" {
			terms = append(terms, truncateForLog(firstNonEmptyString([]string{event.Observation, event.Content, event.Reason}), 240))
		}
	}
	return uniqueNonEmpty(terms)
}

func episodeMemoryRetrievalScope(episode TaskEpisode) map[string]string {
	scope := normalizeEpisodeMemoryScope(episode.DeviceScope)
	if scope == nil {
		scope = map[string]string{}
	}
	if _, ok := scope["device_id"]; !ok {
		scope["device_id"] = defaultMemoryDeviceID
	}
	for key, value := range episode.NormalizedGoal {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "app_name" && key != "page_name" && key != "goal_pattern" {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			scope[key] = value
		}
	}
	return scope
}

func episodeMemoryDefaults(memoryType episodeMemoryType) (priority int, confidence float64, ttl string) {
	switch memoryType {
	case episodeMemoryTypeFailure:
		return 80, 0.7, "60d"
	case episodeMemoryTypeProcedure:
		return 70, 0.75, "45d"
	case episodeMemoryTypeCalibration:
		return 75, 0.75, "30d"
	case episodeMemoryTypeNavigation:
		return 65, 0.7, "30d"
	default:
		return 60, 0.7, "45d"
	}
}

func renderEpisodeMemoryContent(candidate episodeMemoryCandidate) string {
	parts := []string{
		"Situation: " + candidate.Situation,
		"Guidance: " + candidate.Guidance,
		"Expected effect: " + candidate.ExpectedEffect,
	}
	if candidate.ConflictReason != "" {
		parts = append(parts, "Conflict: "+candidate.ConflictReason)
	}
	return strings.Join(parts, "\n")
}

func episodeMemoryProcedureSteps(episode TaskEpisode, refs []string) []ProcedureStep {
	allowed := make(map[string]bool, len(refs))
	for _, ref := range refs {
		allowed[ref] = true
	}
	var steps []ProcedureStep
	for index, event := range episode.Events {
		if event.Type != runEventToolCall || !allowed[event.EventID] || !isEpisodeMemoryDeviceTool(event.ToolName) {
			continue
		}
		step := ProcedureStep{
			Tool:        event.ToolName,
			Description: truncateForLog(event.Content, 160),
			Coords:      extractToolCallCoords(event.ToolInput),
			Text:        extractToolCallText(event.ToolInput),
		}
		for nextIndex := index + 1; nextIndex < len(episode.Events); nextIndex++ {
			next := episode.Events[nextIndex]
			if next.Type == runEventToolCall {
				break
			}
			if next.Type == "tool_result" && next.ToolName == event.ToolName && allowed[next.EventID] {
				step.OutcomeNote = truncateForLog(firstNonEmptyString([]string{next.Observation, next.Content}), 240)
				break
			}
		}
		steps = append(steps, step)
	}
	return steps
}

func invalidEpisodeMemoryReason(episode TaskEpisode) string {
	if strings.TrimSpace(episode.EndedAt) == "" || episode.Status == "running" {
		return "episode is not completed"
	}
	if strings.TrimSpace(episode.UserGoal) == "" {
		return "missing user goal"
	}
	if hasDeviceToolCallAndResult(episode.Events) || hasNonCanceledStructuredError(episode.Events) {
		return ""
	}
	return "missing device action or non-canceled structured error"
}

func hasDeviceToolCallAndResult(events []TaskEpisodeEvent) bool {
	pendingCalls := make(map[string]int)
	for _, event := range events {
		name := strings.ToLower(strings.TrimSpace(event.ToolName))
		if !isEpisodeMemoryDeviceTool(name) {
			continue
		}
		switch event.Type {
		case runEventToolCall:
			pendingCalls[name]++
		case "tool_result":
			if pendingCalls[name] > 0 {
				return true
			}
		}
	}
	return false
}

func hasNonCanceledStructuredError(events []TaskEpisodeEvent) bool {
	for _, event := range events {
		if event.ToolError != nil && event.ToolError.Code != CodeCanceled {
			return true
		}
		if event.IsError && (event.ToolError == nil || event.ToolError.Code != CodeCanceled) {
			return true
		}
	}
	return false
}

func isEpisodeMemoryDeviceTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	if metadata, ok := builtInToolSpecMetadata[name]; ok {
		switch metadata.Category {
		case "memory", "skills", "web", "handoff":
			return false
		case "system":
			return name == "shell"
		}
	}
	return name != toolWaitForWakeup
}

func (s *TaskEpisodeStore) listCompletedEpisodesSince(ctx context.Context, since time.Time, include func(episodeIndexEntry, time.Time) bool) ([]TaskEpisode, error) {
	if s == nil || s.rootDir == "" {
		return nil, nil
	}
	index, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	type datedEntry struct {
		entry episodeIndexEntry
		ended time.Time
	}
	var matches []datedEntry
	for _, entry := range index.Episodes {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if entry.Status == "running" || strings.TrimSpace(entry.EndedAt) == "" {
			continue
		}
		endedAt, err := time.Parse(time.RFC3339Nano, entry.EndedAt)
		if err != nil || endedAt.Before(since) {
			continue
		}
		if include != nil && !include(entry, endedAt) {
			continue
		}
		matches = append(matches, datedEntry{entry: entry, ended: endedAt})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].ended.Equal(matches[j].ended) {
			return matches[i].entry.ID < matches[j].entry.ID
		}
		return matches[i].ended.Before(matches[j].ended)
	})
	episodes := make([]TaskEpisode, 0, len(matches))
	for _, match := range matches {
		episode, found, err := s.getFromIndex(index, match.entry.ID)
		if err != nil {
			return nil, err
		}
		if found {
			episodes = append(episodes, episode)
		}
	}
	return episodes, nil
}

func buildEpisodeMemoryPrompt(payload string, existing []DeviceMemoryItem) string {
	type memoryView struct {
		ID            string            `json:"id"`
		Type          string            `json:"type"`
		Status        string            `json:"status"`
		Revision      int               `json:"revision"`
		Title         string            `json:"title,omitempty"`
		Summary       string            `json:"summary,omitempty"`
		Content       string            `json:"content,omitempty"`
		Applicability map[string]string `json:"scope,omitempty"`
		Tags          []string          `json:"tags,omitempty"`
	}
	views := make([]memoryView, 0, len(existing))
	for _, item := range existing {
		views = append(views, memoryView{
			ID: item.ID, Type: item.Type, Status: string(item.Status), Revision: effectiveDeviceMemoryRevision(item),
			Title: item.Title, Summary: item.Summary, Content: item.Content,
			Applicability: cloneStringMap(item.Applicability), Tags: append([]string(nil), item.Tags...),
		})
	}
	memoryJSON, _ := json.MarshalIndent(views, "", "  ")
	return `Assess this completed Episode and return exactly one JSON object matching this schema:
{
  "episode_assessment": {
    "goal_result": "achieved | not_achieved | unknown",
    "reason": "evidence-based explanation",
    "evidence_refs": ["real Episode event ids"]
  },
  "candidates": [{
    "lesson_key": "unique stable key within this Episode",
    "type": "procedure | navigation | calibration | failure | fact",
    "action": "create | update",
    "memory_id": "required only for update",
    "unresolved_conflict": false,
    "conflict_reason": "required only when unresolved_conflict is true",
    "situation": "when this lesson applies",
    "guidance": "what the future Agent should do or consider",
    "expected_effect": "directly observable expected result",
    "scope": {"device_id":"...", "app_name":"...", "page_name":"...", "goal_pattern":"...", "precondition":"..."},
    "tags": ["short retrieval terms"],
    "evidence_refs": ["real Episode event ids"]
  }]
}

Return at most 3 independent candidates; an empty candidates array is correct when nothing is worth retaining. Every candidate must be reusable in future similar tasks, change future behavior or decisions, have explicit scope, add new knowledge or evidence, and be safer to recall than to omit. Do not retain greetings, task-specific prose, temporary values, OTPs, transient page contents, or information already explicitly saved through a Memory-management tool.
When direct evidence verifies a non-obvious workaround, device-specific route, operational correction, stop condition, or stable fact that satisfies the type rules, you must emit at least one candidate. Do not return an empty candidates array merely because the Episode achieved its goal.

Assess goal_result independently from the recorded success flag. achieved and not_achieved require direct result, final-state, screenshot, or user-correction evidence. Use unknown when final proof is missing and say what is missing. User steer is correction evidence, not an admission gate. Do not rely on verifier_decision or ObservedState; they are not part of this pipeline.

Type rules:
- procedure: reusable multi-step goal path supported by tool calls and results. If goal_result is not_achieved, only emit an independently proven partial procedure and set scope.partial="true".
- navigation: independently reusable page entry or transition, supported by an action and comparable before/after observations. Keep a navigation step inside a procedure when it has no independent recall value.
- calibration: coordinate, screen, control, or coordinate-space relationship supported by coordinate input and post-action evidence.
- failure: reusable guard, check, stop condition, or recovery action. A failed Episode is not automatically a failure memory; omit one-off failures with no actionable future guard.
- fact: stable device, app, or page fact directly observed by a tool. Do not turn guesses into facts.

Deduplication and conflict rules:
- Use action=create only when no existing memory has the same scoped lesson.
- For action=create, omit memory_id and memory_revision.
- Use action=update with an existing memory_id and its exact memory_revision when the lesson overlaps. Output the complete merged situation, guidance, expected_effect, scope, and tags; preserve valid older conditions rather than merely copying the new Episode.
- device_profile and app_profile entries are deterministic context only. Do not update them or duplicate facts already represented by them.
- Resolve apparent conflicts by conditioning the merged memory on version, page, account state, or another evidenced precondition when possible.
- Set unresolved_conflict=true only when the same scope still has incompatible conclusions and no safe condition can distinguish them. The memory will be quarantined as disputed.
- An achieved Episode is not automatically a procedure, and an Episode-level failure is not automatically a failure memory.

Evidence rules: cite only real event ids. Prefer tool results, structured errors, attached screenshots, final visible state, and user correction over Agent commentary. A cited tool result is deterministically linked to its paired tool call before type validation. Screenshots support only what is visibly shown. Preserve uncertainty and never invent causal ownership, UI state, app/page names, or unsupported recovery tools.

Existing related Device Memories (maximum 8, including disputed records):
` + string(memoryJSON) + `

Episode:
` + payload
}
