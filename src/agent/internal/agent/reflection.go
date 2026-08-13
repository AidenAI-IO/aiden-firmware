package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/llms"
	"gopkg.in/yaml.v3"
)

const (
	reflectionStateVersion     = 1
	reflectionFailureTag       = "reflection:v1"
	reflectionBatchLimit       = 5
	reflectionCandidateLimit   = 5
	reflectionRecentTerminals  = 64
	reflectionProcessingLease  = 15 * time.Minute
	reflectionRetryDelay       = 5 * time.Minute
	reflectionMaxAttempts      = 3
	reflectionModelCallTimeout = 60 * time.Second
	reflectionBatchLockTimeout = 100 * time.Millisecond

	reflectionStatusProcessing = "processing"
	reflectionStatusDone       = "done"
	reflectionStatusIgnored    = "ignored"
	reflectionStatusRetry      = "retry"
	reflectionActionKeep       = "keep"
	reflectionActionIgnore     = "ignore"
	reflectionActionMerge      = "merge"
	reflectionActionCreate     = "create"
)

type reflectionEpisodeStatus struct {
	Status              string `yaml:"status"`
	ProcessingStartedAt string `yaml:"processing_started_at,omitempty"`
	RetryAt             string `yaml:"retry_at,omitempty"`
	EndedAt             string `yaml:"ended_at,omitempty"`
	LastError           string `yaml:"last_error,omitempty"`
	AttemptCount        int    `yaml:"attempt_count,omitempty"`
}

type reflectionStateFile struct {
	Version            int                                `yaml:"version"`
	EnabledAt          string                             `yaml:"enabled_at"`
	CompletedThroughAt string                             `yaml:"completed_through_at,omitempty"`
	CompletedThroughID string                             `yaml:"completed_through_id,omitempty"`
	Episodes           map[string]reflectionEpisodeStatus `yaml:"episodes,omitempty"`
}

type reflectionStateStore struct {
	mu          sync.Mutex
	path        string
	bootstrapAt time.Time
}

func newReflectionStateStore(path string, bootstrapAt time.Time) *reflectionStateStore {
	return &reflectionStateStore{path: path, bootstrapAt: bootstrapAt.UTC()}
}

func (s *reflectionStateStore) Snapshot() (reflectionStateFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, changed, err := s.loadLocked()
	if err != nil {
		return reflectionStateFile{}, err
	}
	if changed {
		if err := writeYAMLAtomic(s.path, state); err != nil {
			return reflectionStateFile{}, err
		}
	}
	return cloneReflectionState(state), nil
}

func (s *reflectionStateStore) SetEpisode(id string, status reflectionEpisodeStatus) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, _, err := s.loadLocked()
	if err != nil {
		return err
	}
	if state.Episodes == nil {
		state.Episodes = map[string]reflectionEpisodeStatus{}
	}
	if status == (reflectionEpisodeStatus{}) {
		delete(state.Episodes, id)
	} else {
		state.Episodes[id] = status
	}
	return writeYAMLAtomic(s.path, state)
}

func (s *reflectionStateStore) CompleteEpisode(id string, endedAt time.Time, status reflectionEpisodeStatus) error {
	id = strings.TrimSpace(id)
	if id == "" || endedAt.IsZero() {
		return nil
	}
	endedAt = endedAt.UTC()
	status.EndedAt = endedAt.Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	state, _, err := s.loadLocked()
	if err != nil {
		return err
	}
	if state.Episodes == nil {
		state.Episodes = map[string]reflectionEpisodeStatus{}
	}
	state.Episodes[id] = status
	setReflectionCursor(&state, endedAt, id)
	pruneReflectionTerminalStatuses(&state, reflectionRecentTerminals)
	return writeYAMLAtomic(s.path, state)
}

func (s *reflectionStateStore) loadLocked() (reflectionStateFile, bool, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return reflectionStateFile{}, false, fmt.Errorf("read reflection state: %w", err)
		}
		return reflectionStateFile{
			Version:   reflectionStateVersion,
			EnabledAt: s.bootstrapAt.Format(time.RFC3339Nano),
			Episodes:  map[string]reflectionEpisodeStatus{},
		}, true, nil
	}
	var state reflectionStateFile
	if err := yaml.Unmarshal(data, &state); err != nil {
		return reflectionStateFile{}, false, fmt.Errorf("decode reflection state: %w", err)
	}
	changed := false
	if state.Version == 0 {
		state.Version = reflectionStateVersion
		changed = true
	}
	if strings.TrimSpace(state.EnabledAt) == "" {
		state.EnabledAt = s.bootstrapAt.Format(time.RFC3339Nano)
		changed = true
	}
	if state.Episodes == nil {
		state.Episodes = map[string]reflectionEpisodeStatus{}
		changed = true
	}
	return state, changed, nil
}

func cloneReflectionState(state reflectionStateFile) reflectionStateFile {
	cloned := state
	cloned.Episodes = make(map[string]reflectionEpisodeStatus, len(state.Episodes))
	for id, status := range state.Episodes {
		cloned.Episodes[id] = status
	}
	return cloned
}

func setReflectionCursor(state *reflectionStateFile, endedAt time.Time, episodeID string) {
	if state == nil || endedAt.IsZero() || strings.TrimSpace(episodeID) == "" {
		return
	}
	currentAt, err := time.Parse(time.RFC3339Nano, state.CompletedThroughAt)
	if err == nil && (endedAt.Before(currentAt) || (endedAt.Equal(currentAt) && episodeID <= state.CompletedThroughID)) {
		return
	}
	state.CompletedThroughAt = endedAt.UTC().Format(time.RFC3339Nano)
	state.CompletedThroughID = episodeID
}

func pruneReflectionTerminalStatuses(state *reflectionStateFile, limit int) {
	if state == nil || limit < 0 || len(state.Episodes) <= limit {
		return
	}
	type terminalStatus struct {
		id      string
		endedAt time.Time
	}
	terminals := make([]terminalStatus, 0, len(state.Episodes))
	for id, status := range state.Episodes {
		if status.Status != reflectionStatusDone && status.Status != reflectionStatusIgnored {
			continue
		}
		endedAt, err := time.Parse(time.RFC3339Nano, status.EndedAt)
		if err != nil {
			endedAt = time.Time{}
		}
		terminals = append(terminals, terminalStatus{id: id, endedAt: endedAt})
	}
	if len(terminals) <= limit {
		return
	}
	sort.SliceStable(terminals, func(i, j int) bool {
		if terminals[i].endedAt.Equal(terminals[j].endedAt) {
			return terminals[i].id > terminals[j].id
		}
		return terminals[i].endedAt.After(terminals[j].endedAt)
	})
	for _, terminal := range terminals[limit:] {
		delete(state.Episodes, terminal.id)
	}
}

type failureSummary struct {
	Action       string   `json:"action"`
	Pattern      string   `json:"pattern"`
	Cause        string   `json:"cause"`
	MissedSignal string   `json:"missed_signal"`
	Guard        string   `json:"guard"`
	Scope        string   `json:"scope"`
	Tags         []string `json:"tags"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type failureMergeDecision struct {
	Action   string `json:"action"`
	MemoryID string `json:"memory_id"`
}

type reflectionBatchResult struct {
	HasPending bool
	NextRunAt  time.Time
}

type failureReflectionProcessor struct {
	plane *FilesystemMemoryPlane
	model model.Model
	state *reflectionStateStore
	now   func() time.Time
	lock  string
}

func newFailureReflectionProcessor(plane *FilesystemMemoryPlane, models model.Model) *failureReflectionProcessor {
	bootstrapAt := time.Now().UTC()
	return &failureReflectionProcessor{
		plane: plane,
		model: models,
		state: newReflectionStateStore(filepath.Join(plane.memoryDir, "lifecycle", "reflection.yaml"), bootstrapAt),
		now:   func() time.Time { return time.Now().UTC() },
		lock:  filepath.Join(plane.memoryDir, "lifecycle", "reflection.lock"),
	}
}

func (p *failureReflectionProcessor) Initialize() error {
	if p == nil || p.state == nil {
		return nil
	}
	_, err := p.state.Snapshot()
	return err
}

func (p *failureReflectionProcessor) logBatchError(err error) {
	if err != nil && p != nil && p.plane != nil && p.plane.logger != nil {
		p.plane.logger.Warn("[reflection] batch failed: %v", err)
	}
}

func (p *failureReflectionProcessor) NextRunAt(ctx context.Context) (time.Time, error) {
	state, episodes, err := p.loadWork(ctx)
	if err != nil {
		return time.Time{}, err
	}
	now := p.now()
	var next time.Time
	for _, episode := range episodes {
		eligible, due := reflectionEpisodeDue(state.Episodes[episode.ID], now)
		if eligible {
			return now, nil
		}
		if !due.IsZero() && (next.IsZero() || due.Before(next)) {
			next = due
		}
	}
	return next, nil
}

func (p *failureReflectionProcessor) ProcessBatch(ctx context.Context, limit int, shouldStop func() bool) (reflectionBatchResult, error) {
	if p == nil {
		return reflectionBatchResult{}, nil
	}
	lock := &FileLock{path: p.lock}
	if err := lock.Lock(reflectionBatchLockTimeout); err != nil {
		return reflectionBatchResult{}, fmt.Errorf("acquire reflection batch lock: %w", err)
	}
	result, err := p.processBatchLocked(ctx, limit, shouldStop)
	if unlockErr := lock.Unlock(); err == nil && unlockErr != nil {
		err = unlockErr
	}
	return result, err
}

func (p *failureReflectionProcessor) processBatchLocked(ctx context.Context, limit int, shouldStop func() bool) (reflectionBatchResult, error) {
	if limit <= 0 {
		limit = reflectionBatchLimit
	}
	state, episodes, err := p.loadWork(ctx)
	if err != nil {
		return reflectionBatchResult{}, err
	}
	now := p.now()
	processed := 0
	result := reflectionBatchResult{}
	for index, episode := range episodes {
		if shouldStop != nil && shouldStop() {
			result.HasPending = true
			break
		}
		status := state.Episodes[episode.ID]
		eligible, due := reflectionEpisodeDue(status, now)
		if !eligible {
			if !due.IsZero() && (result.NextRunAt.IsZero() || due.Before(result.NextRunAt)) {
				result.NextRunAt = due
			}
			continue
		}
		if reason := invalidReflectionEpisodeReason(episode); reason != "" {
			ignored := reflectionEpisodeStatus{Status: reflectionStatusIgnored, LastError: reason}
			endedAt, err := reflectionEpisodeEndedAt(episode)
			if err != nil {
				return result, err
			}
			if err := p.state.CompleteEpisode(episode.ID, endedAt, ignored); err != nil {
				return result, err
			}
			state.Episodes[episode.ID] = ignored
			setReflectionCursor(&state, endedAt, episode.ID)
			continue
		}
		if processed >= limit || (shouldStop != nil && shouldStop()) {
			result.HasPending = true
			break
		}

		startedAt := p.now()
		processing := reflectionEpisodeStatus{
			Status:              reflectionStatusProcessing,
			ProcessingStartedAt: startedAt.Format(time.RFC3339Nano),
			AttemptCount:        status.AttemptCount,
		}
		if err := p.state.SetEpisode(episode.ID, processing); err != nil {
			return result, err
		}
		state.Episodes[episode.ID] = processing

		finalStatus, processErr := p.processEpisode(ctx, episode)
		processed++
		if processErr != nil {
			if ctx.Err() != nil {
				if err := p.state.SetEpisode(episode.ID, status); err != nil {
					return result, err
				}
				if status == (reflectionEpisodeStatus{}) {
					delete(state.Episodes, episode.ID)
				} else {
					state.Episodes[episode.ID] = status
				}
				result.HasPending = true
				return result, nil
			}
			attemptCount := status.AttemptCount + 1
			lastError := truncateForLog(processErr.Error(), 500)
			if attemptCount >= reflectionMaxAttempts {
				ignored := reflectionEpisodeStatus{
					Status:       reflectionStatusIgnored,
					LastError:    lastError,
					AttemptCount: attemptCount,
				}
				endedAt, err := reflectionEpisodeEndedAt(episode)
				if err != nil {
					return result, err
				}
				if err := p.state.CompleteEpisode(episode.ID, endedAt, ignored); err != nil {
					return result, err
				}
				state.Episodes[episode.ID] = ignored
				setReflectionCursor(&state, endedAt, episode.ID)
				continue
			}
			retryAt := p.now().Add(reflectionRetryDelay)
			retry := reflectionEpisodeStatus{
				Status:       reflectionStatusRetry,
				RetryAt:      retryAt.Format(time.RFC3339Nano),
				LastError:    lastError,
				AttemptCount: attemptCount,
			}
			if err := p.state.SetEpisode(episode.ID, retry); err != nil {
				return result, err
			}
			state.Episodes[episode.ID] = retry
			if result.NextRunAt.IsZero() || retryAt.Before(result.NextRunAt) {
				result.NextRunAt = retryAt
			}
			continue
		} else {
			completed := reflectionEpisodeStatus{Status: finalStatus}
			endedAt, err := reflectionEpisodeEndedAt(episode)
			if err != nil {
				return result, err
			}
			if err := p.state.CompleteEpisode(episode.ID, endedAt, completed); err != nil {
				return result, err
			}
			state.Episodes[episode.ID] = completed
			setReflectionCursor(&state, endedAt, episode.ID)
		}
		if shouldStop != nil && shouldStop() && index < len(episodes)-1 {
			result.HasPending = true
			break
		}
	}
	return result, nil
}

func (p *failureReflectionProcessor) loadWork(ctx context.Context) (reflectionStateFile, []TaskEpisode, error) {
	if p == nil || p.plane == nil || p.plane.episodes == nil || p.plane.device == nil || p.model == nil {
		return reflectionStateFile{}, nil, nil
	}
	state, err := p.state.Snapshot()
	if err != nil {
		return reflectionStateFile{}, nil, err
	}
	enabledAt, err := time.Parse(time.RFC3339Nano, state.EnabledAt)
	if err != nil {
		return reflectionStateFile{}, nil, fmt.Errorf("parse reflection enabled_at: %w", err)
	}
	episodes, err := p.plane.episodes.listCompletedFailuresSince(ctx, enabledAt, func(entry episodeIndexEntry, endedAt time.Time) bool {
		status := state.Episodes[entry.ID].Status
		if status == reflectionStatusDone || status == reflectionStatusIgnored {
			return false
		}
		if status == reflectionStatusProcessing || status == reflectionStatusRetry {
			return true
		}
		return reflectionEntryAfterCursor(entry.ID, endedAt, state)
	})
	if err != nil {
		return reflectionStateFile{}, nil, err
	}
	return state, episodes, nil
}

func reflectionEntryAfterCursor(episodeID string, endedAt time.Time, state reflectionStateFile) bool {
	cursorAt, err := time.Parse(time.RFC3339Nano, state.CompletedThroughAt)
	if err != nil {
		return true
	}
	return endedAt.After(cursorAt) || (endedAt.Equal(cursorAt) && episodeID > state.CompletedThroughID)
}

func reflectionEpisodeEndedAt(episode TaskEpisode) (time.Time, error) {
	endedAt, err := time.Parse(time.RFC3339Nano, episode.EndedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse reflection episode %s ended_at: %w", episode.ID, err)
	}
	return endedAt, nil
}

func reflectionEpisodeDue(status reflectionEpisodeStatus, now time.Time) (bool, time.Time) {
	switch status.Status {
	case reflectionStatusDone, reflectionStatusIgnored:
		return false, time.Time{}
	case reflectionStatusProcessing:
		startedAt, err := time.Parse(time.RFC3339Nano, status.ProcessingStartedAt)
		if err != nil {
			return true, time.Time{}
		}
		due := startedAt.Add(reflectionProcessingLease)
		return !now.Before(due), due
	case reflectionStatusRetry:
		retryAt, err := time.Parse(time.RFC3339Nano, status.RetryAt)
		if err != nil {
			return true, time.Time{}
		}
		return !now.Before(retryAt), retryAt
	default:
		return true, time.Time{}
	}
}

func (p *failureReflectionProcessor) processEpisode(ctx context.Context, episode TaskEpisode) (string, error) {
	if _, found, err := p.plane.device.FindReflectionFailureByEpisode(ctx, episode.ID); err != nil {
		return "", err
	} else if found {
		return reflectionStatusDone, nil
	}
	summary, err := p.summarizeFailure(ctx, episode)
	if err != nil {
		return "", err
	}
	if summary.Action == reflectionActionIgnore {
		return reflectionStatusIgnored, nil
	}
	terms := []string{summary.Pattern, summary.Cause, summary.MissedSignal, summary.Guard, summary.Scope}
	terms = append(terms, summary.Tags...)
	terms = append(terms, episode.Tags...)
	terms = append(terms, episode.Entities...)
	appName, pageName := reflectionEpisodeAppPage(episode)
	terms = append(terms, appName, pageName)
	deviceID := firstNonEmptyString([]string{episode.DeviceScope["device_id"], defaultMemoryDeviceID})
	candidates, err := p.plane.device.SearchFailureMemories(ctx, FailureMemoryQuery{
		Terms:        terms,
		PreferredIDs: episode.RetrievedMemoryRefs,
		DeviceID:     deviceID,
		Limit:        reflectionCandidateLimit,
	})
	if err != nil {
		return "", err
	}
	decision := failureMergeDecision{Action: reflectionActionCreate}
	if len(candidates) > 0 {
		decision, err = p.decideFailureMerge(ctx, summary, candidates)
		if err != nil {
			return "", err
		}
	}
	if decision.Action == reflectionActionMerge {
		if err := p.mergeFailureMemory(ctx, decision.MemoryID, summary, episode); err != nil {
			return "", err
		}
		return reflectionStatusDone, nil
	}
	if _, err := p.createFailureMemory(ctx, summary, episode); err != nil {
		return "", err
	}
	return reflectionStatusDone, nil
}

func (p *failureReflectionProcessor) summarizeFailure(ctx context.Context, episode TaskEpisode) (failureSummary, error) {
	payload, err := json.MarshalIndent(reflectionEpisodePayload(episode), "", "  ")
	if err != nil {
		return failureSummary{}, err
	}
	parts := []llms.ContentPart{llms.TextPart(buildFailureSummaryPrompt(string(payload)))}
	for _, screenshot := range loadReflectionScreenshots(p.plane.episodes.rootDir, episode) {
		parts = append(parts, llms.TextPart("Attached screenshot evidence for Episode event id: "+screenshot.EventID))
		parts = append(parts, llms.BinaryContent{MIMEType: screenshot.MIMEType, Data: screenshot.Data})
	}
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "You extract reusable failure-prevention lessons from agent task episodes. Output JSON only."),
		{Role: llms.ChatMessageTypeHuman, Parts: parts},
	}
	callCtx, cancel := context.WithTimeout(ctx, reflectionModelCallTimeout)
	defer cancel()
	response, err := p.model.GenerateContent(callCtx, messages, llms.WithJSONMode(), llms.WithMaxTokens(1400))
	if err != nil {
		return failureSummary{}, fmt.Errorf("generate failure summary: %w", err)
	}
	if response == nil || len(response.Choices) == 0 {
		return failureSummary{}, fmt.Errorf("generate failure summary: empty response")
	}
	var summary failureSummary
	if err := json.Unmarshal([]byte(stripJSONFences(response.Choices[0].Content)), &summary); err != nil {
		return failureSummary{}, fmt.Errorf("parse failure summary: %w", err)
	}
	summary.Action = strings.ToLower(strings.TrimSpace(summary.Action))
	if summary.Action == reflectionActionIgnore {
		return summary, nil
	}
	if summary.Action != reflectionActionKeep {
		return failureSummary{}, fmt.Errorf("invalid failure summary action %q", summary.Action)
	}
	summary.Pattern = strings.TrimSpace(summary.Pattern)
	summary.Cause = strings.TrimSpace(summary.Cause)
	summary.MissedSignal = strings.TrimSpace(summary.MissedSignal)
	summary.Guard = strings.TrimSpace(summary.Guard)
	summary.Scope = strings.TrimSpace(summary.Scope)
	if summary.Pattern == "" || summary.Cause == "" || summary.MissedSignal == "" || summary.Guard == "" {
		return failureSummary{}, fmt.Errorf("failure summary requires pattern, cause, missed_signal, and guard")
	}
	summary.Tags = normalizeReflectionTags(summary.Tags)
	summary.EvidenceRefs = validReflectionEventIDs(episode, summary.EvidenceRefs)
	if len(summary.EvidenceRefs) == 0 {
		return failureSummary{}, fmt.Errorf("failure summary requires at least one valid evidence_ref")
	}
	return summary, nil
}

func (p *failureReflectionProcessor) decideFailureMerge(ctx context.Context, summary failureSummary, candidates []DeviceMemoryItem) (failureMergeDecision, error) {
	type candidateView struct {
		ID            string   `json:"id"`
		Status        string   `json:"status"`
		Title         string   `json:"title"`
		Summary       string   `json:"summary"`
		Content       string   `json:"content"`
		Tags          []string `json:"tags"`
		AppName       string   `json:"app_name,omitempty"`
		PageName      string   `json:"page_name,omitempty"`
		EvidenceCount int      `json:"evidence_count"`
	}
	views := make([]candidateView, 0, len(candidates))
	allowed := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate.ID] = true
		views = append(views, candidateView{
			ID:            candidate.ID,
			Status:        candidate.Status,
			Title:         candidate.Title,
			Summary:       candidate.Summary,
			Content:       candidate.Content,
			Tags:          candidate.Tags,
			AppName:       candidate.AppName,
			PageName:      candidate.PageName,
			EvidenceCount: distinctEpisodeEvidenceCount(candidate.EvidenceRefs),
		})
	}
	input, err := json.MarshalIndent(map[string]interface{}{"failure_summary": summary, "candidate_memories": views}, "", "  ")
	if err != nil {
		return failureMergeDecision{}, err
	}
	prompt := `Decide whether this failure is the same reusable failure type as one candidate memory.

Return exactly one JSON object:
- {"action":"merge","memory_id":"candidate id"} only when the underlying mistake and prevention guard are materially the same.
- {"action":"create"} when no candidate is the same failure type.

App, page, tags, or similar wording alone are not enough to merge. Output JSON only.

Input:
` + string(input)
	callCtx, cancel := context.WithTimeout(ctx, reflectionModelCallTimeout)
	defer cancel()
	response, err := p.model.GenerateContent(callCtx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "You deduplicate failure memories conservatively. Output JSON only."),
		llms.TextParts(llms.ChatMessageTypeHuman, prompt),
	}, llms.WithJSONMode(), llms.WithMaxTokens(300))
	if err != nil {
		return failureMergeDecision{}, fmt.Errorf("decide failure memory merge: %w", err)
	}
	if response == nil || len(response.Choices) == 0 {
		return failureMergeDecision{}, fmt.Errorf("decide failure memory merge: empty response")
	}
	var decision failureMergeDecision
	if err := json.Unmarshal([]byte(stripJSONFences(response.Choices[0].Content)), &decision); err != nil {
		return failureMergeDecision{}, fmt.Errorf("parse failure memory decision: %w", err)
	}
	decision.Action = strings.ToLower(strings.TrimSpace(decision.Action))
	decision.MemoryID = strings.TrimSpace(decision.MemoryID)
	switch decision.Action {
	case reflectionActionCreate:
		decision.MemoryID = ""
		return decision, nil
	case reflectionActionMerge:
		if !allowed[decision.MemoryID] {
			return failureMergeDecision{}, fmt.Errorf("merge decision referenced unknown memory %q", decision.MemoryID)
		}
		return decision, nil
	default:
		return failureMergeDecision{}, fmt.Errorf("invalid failure memory action %q", decision.Action)
	}
}

func (p *failureReflectionProcessor) createFailureMemory(ctx context.Context, summary failureSummary, episode TaskEpisode) (string, error) {
	appName, pageName := reflectionEpisodeAppPage(episode)
	tags := mergeUniqueStrings([]string{reflectionFailureTag}, summary.Tags)
	item := DeviceMemoryItem{
		Type:         "failure",
		Status:       "pending",
		Title:        truncateForLog(summary.Pattern, 120),
		Summary:      truncateForLog(summary.Guard, 240),
		Content:      renderFailureMemoryContent(summary),
		DeviceID:     firstNonEmptyString([]string{episode.DeviceScope["device_id"], defaultMemoryDeviceID}),
		AppName:      appName,
		PageName:     pageName,
		Tags:         tags,
		Entities:     append([]string(nil), episode.Entities...),
		Confidence:   0.65,
		Priority:     80,
		TTL:          "60d",
		EvidenceRefs: []MemorySourceRef{reflectionEvidenceRef(episode, summary.EvidenceRefs)},
	}
	return p.plane.device.Upsert(ctx, item)
}

func (p *failureReflectionProcessor) mergeFailureMemory(ctx context.Context, memoryID string, summary failureSummary, episode TaskEpisode) error {
	updated := false
	err := p.plane.device.Update(ctx, memoryID, func(item *DeviceMemoryItem) {
		if item == nil || item.Type != "failure" || !hasReflectionFailureTag(item.Tags) {
			return
		}
		updated = true
		if hasEpisodeEvidence(item.EvidenceRefs, episode.ID) {
			return
		}
		item.EvidenceRefs = append(item.EvidenceRefs, reflectionEvidenceRef(episode, summary.EvidenceRefs))
		item.Tags = mergeUniqueStrings(item.Tags, summary.Tags)
		if distinctEpisodeEvidenceCount(item.EvidenceRefs) >= 2 {
			item.Status = "active"
		}
		if containsStringFold(episode.RetrievedMemoryRefs, item.ID) {
			item.FailureCount++
		}
	})
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("failure memory not found or not managed by reflection: %s", memoryID)
	}
	return nil
}

func invalidReflectionEpisodeReason(episode TaskEpisode) string {
	if episode.Status == "interrupted" {
		return "interrupted episode"
	}
	if strings.TrimSpace(episode.EndedAt) == "" || episode.Status == "running" || episode.Outcome.Success {
		return "not a completed failure episode"
	}
	if strings.TrimSpace(episode.UserGoal) == "" {
		return "missing user goal"
	}
	if !hasUsefulReflectionEvents(episode.Events) {
		return "missing useful events"
	}
	if onlyCanceledToolErrors(episode.Events) {
		return "only structured error was canceled"
	}
	return ""
}

func hasUsefulReflectionEvents(events []TaskEpisodeEvent) bool {
	for _, event := range events {
		switch event.Type {
		case runEventToolCall, "tool_result", "planner_decision", "verifier_decision", "role_output", "steer":
			return true
		}
	}
	return false
}

func onlyCanceledToolErrors(events []TaskEpisodeEvent) bool {
	found := false
	for _, event := range events {
		if event.ToolError != nil {
			found = true
			if event.ToolError.Code != CodeCanceled {
				return false
			}
			continue
		}
		if event.IsError {
			return false
		}
	}
	return found
}

func (s *TaskEpisodeStore) listCompletedFailuresSince(ctx context.Context, since time.Time, include func(episodeIndexEntry, time.Time) bool) ([]TaskEpisode, error) {
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
		if entry.Status == "running" || entry.Success || strings.TrimSpace(entry.EndedAt) == "" {
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

type reflectionEventPayload struct {
	EventID       string     `json:"event_id,omitempty"`
	Type          string     `json:"type"`
	Role          string     `json:"role,omitempty"`
	Objective     string     `json:"objective,omitempty"`
	ToolName      string     `json:"tool_name,omitempty"`
	ToolInput     string     `json:"tool_input,omitempty"`
	ToolError     *ToolError `json:"tool_error,omitempty"`
	Content       string     `json:"content,omitempty"`
	Observation   string     `json:"observation,omitempty"`
	ScreenshotRef string     `json:"screenshot_ref,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	CanFinish     *bool      `json:"can_finish,omitempty"`
	AppName       string     `json:"app_name,omitempty"`
	PageName      string     `json:"page_name,omitempty"`
}

type reflectionOutcomePayload struct {
	Success        bool   `json:"success"`
	FinalState     string `json:"final_state,omitempty"`
	FinalAnswer    string `json:"final_answer,omitempty"`
	VerifierReason string `json:"verifier_reason,omitempty"`
	FailureReason  string `json:"failure_reason,omitempty"`
}

func reflectionEpisodePayload(episode TaskEpisode) map[string]interface{} {
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
	views := make([]reflectionEventPayload, 0, len(events))
	for _, event := range events {
		view := reflectionEventPayload{
			EventID:     event.EventID,
			Type:        event.Type,
			Role:        event.Role,
			Objective:   truncateForLog(event.Objective, 500),
			ToolName:    event.ToolName,
			ToolInput:   truncateForLog(event.ToolInput, 800),
			ToolError:   event.ToolError,
			Content:     truncateForLog(sanitizeReflectionScreenshotPaths(event.Content, screenshotEvents), 1200),
			Observation: truncateForLog(sanitizeReflectionScreenshotPaths(event.Observation, screenshotEvents), 1600),
			Reason:      truncateForLog(event.Reason, 800),
			CanFinish:   event.CanFinish,
		}
		if strings.TrimSpace(event.ScreenshotRef) != "" {
			view.ScreenshotRef = "attached screenshot for event " + event.EventID
		}
		if event.ObservedState != nil {
			view.AppName = event.ObservedState.AppName
			view.PageName = event.ObservedState.PageName
		}
		views = append(views, view)
	}
	return map[string]interface{}{
		"episode_id":   episode.ID,
		"user_goal":    episode.UserGoal,
		"device_scope": episode.DeviceScope,
		"outcome": reflectionOutcomePayload{
			Success:        episode.Outcome.Success,
			FinalState:     sanitizeReflectionScreenshotPaths(episode.Outcome.FinalState, screenshotEvents),
			FinalAnswer:    sanitizeReflectionScreenshotPaths(episode.Outcome.FinalAnswer, screenshotEvents),
			VerifierReason: sanitizeReflectionScreenshotPaths(episode.Outcome.VerifierReason, screenshotEvents),
			FailureReason:  sanitizeReflectionScreenshotPaths(episode.Outcome.FailureReason, screenshotEvents),
		},
		"failure_causes":    episode.FailureCauses,
		"tags":              episode.Tags,
		"entities":          episode.Entities,
		"recalled_memories": episode.RetrievedMemoryRefs,
		"events":            views,
	}
}

func sanitizeReflectionScreenshotPaths(text string, screenshotEvents map[string]string) string {
	for ref, eventID := range screenshotEvents {
		text = strings.ReplaceAll(text, ref, "attached screenshot for event "+eventID)
	}
	return text
}

func buildFailureSummaryPrompt(payload string) string {
	return `Review this single failed task episode and extract at most one reusable failure-prevention lesson.

Return one JSON object with exactly these fields:
{
  "action": "keep" or "ignore",
  "pattern": "reproducible mistake or failure pattern",
  "cause": "evidence-backed reason why the user goal failed",
  "missed_signal": "direct evidence the agent overlooked",
  "guard": "what the agent should do differently to prevent recurrence",
  "scope": "known app/page/task scope, or empty string",
  "tags": ["up to 8 short search terms"],
  "evidence_refs": ["event ids that support the lesson"]
}

Evidence rules:
- Visible screenshot evidence and direct tool errors or verifier observations have higher priority than the Agent's own commentary, guesses, or outcome explanation. When they conflict, trust the direct evidence and avoid unsupported causal claims.
- Use attached screenshots as evidence only for what is visibly shown. Describe the visible signal instead of copying screenshot filenames into the lesson. Cite the event id whose screenshot_ref identifies that screenshot; never put a screenshot filename in evidence_refs.
- Preserve the evidence's level of certainty. Do not strengthen a visible message such as "the server unexpectedly interrupted the connection" into "the server is unreachable", "the service is down", or another diagnosis unless independent Episode evidence establishes it.
- Do not label an error as client-side, server-side, network-side, or browser-side unless independent Episode evidence establishes that ownership. Prefer the exact visible failure wording over a broader diagnosis.
- When the primary evidence is a visible error message, cause must closely paraphrase that message and the observed unmet goal. Do not express uncertainty as alternatives such as "unreachable or dropped the connection"; keep only the directly supported statement.
- Evidence-preserving examples:
  - Bad: The service is unavailable or the connection is unstable. Good: Safari kept showing that the server unexpectedly interrupted the connection, so the requested page did not load.
  - Bad: This is a server-side or network issue. Good: The same visible error remained after the attempted actions, so those actions made no progress.
- Separate why the user goal failed from what the Agent should do differently: cause records the actual evidence-backed task failure; guard records the corrective Agent behavior. Do not rewrite an external failure as an Agent input mistake merely to make it actionable.
- A browser address bar may hide the scheme or port after navigation. Its simplified display alone is not evidence that the Agent omitted them; require corroborating input, navigation, or error evidence before making that claim.

Decision rules:
- Use action=ignore for cancellation, insufficient evidence, or a one-off infrastructure/provider failure with no reusable Agent guard.
- When an external failure is directly visible but the Agent also repeated ineffective actions, ignored no-progress evidence, or claimed success without verification, action=keep may capture that reusable Agent behavior. Preserve the visible external cause accurately and put the stop, escalation, retry, or verification rule in guard.
- Do not prescribe a diagnostic tool or environment-specific action unless the Episode shows it is supported and valid for the controlled target. A safe guard may instead stop repeated actions, report the direct evidence, and request human handoff or an appropriate target-side check.
- For action=keep, pattern, cause, missed_signal, and guard must all be non-empty, and evidence_refs must contain at least one real event id from the Episode.

Before output, remove every diagnosis, ownership label, or proposed tool that is not directly supported by an Episode event or attached screenshot. Do not invent app, page, verifier output, user correction, screenshot content, or causal details. Keep the lesson in the episode's language. Output JSON only.

Episode:
` + payload
}

type reflectionScreenshot struct {
	EventID  string
	MIMEType string
	Data     []byte
}

func loadReflectionScreenshots(episodesRoot string, episode TaskEpisode) []reflectionScreenshot {
	refs := selectReflectionScreenshotRefs(episode.Events)
	if len(refs) == 0 {
		return nil
	}
	episodeDir := EpisodeDirectory(episodesRoot, episode)
	var screenshots []reflectionScreenshot
	for _, ref := range refs {
		clean := filepath.Clean(strings.TrimSpace(ref))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(episodeDir, clean))
		if err != nil || len(data) == 0 || len(data) > 8<<20 {
			continue
		}
		mimeType := "image/jpeg"
		switch strings.ToLower(filepath.Ext(clean)) {
		case ".png":
			mimeType = "image/png"
		case ".webp":
			mimeType = "image/webp"
		}
		eventID := ""
		for _, event := range episode.Events {
			if strings.TrimSpace(event.ScreenshotRef) == ref {
				eventID = event.EventID
				break
			}
		}
		if eventID == "" {
			continue
		}
		screenshots = append(screenshots, reflectionScreenshot{EventID: eventID, MIMEType: mimeType, Data: data})
	}
	return screenshots
}

func selectReflectionScreenshotRefs(events []TaskEpisodeEvent) []string {
	type indexedRef struct {
		index int
		ref   string
	}
	var refs []indexedRef
	seen := map[string]bool{}
	for index, event := range events {
		ref := strings.TrimSpace(event.ScreenshotRef)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, indexedRef{index: index, ref: ref})
	}
	if len(refs) <= 3 {
		result := make([]string, 0, len(refs))
		for _, ref := range refs {
			result = append(result, ref.ref)
		}
		return result
	}
	errorIndex := -1
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.IsError || event.ToolError != nil {
			errorIndex = index
			break
		}
	}
	var selected []string
	appendRef := func(ref string) {
		if ref != "" && !containsStringFold(selected, ref) && len(selected) < 3 {
			selected = append(selected, ref)
		}
	}
	if errorIndex >= 0 {
		for index := len(refs) - 1; index >= 0; index-- {
			if refs[index].index <= errorIndex {
				appendRef(refs[index].ref)
				break
			}
		}
		for _, ref := range refs {
			if ref.index >= errorIndex {
				appendRef(ref.ref)
				break
			}
		}
	}
	appendRef(refs[len(refs)-1].ref)
	appendRef(refs[0].ref)
	appendRef(refs[len(refs)/2].ref)
	return selected
}

func normalizeReflectionTags(tags []string) []string {
	var normalized []string
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || strings.EqualFold(tag, reflectionFailureTag) {
			continue
		}
		normalized = appendUniqueString(normalized, truncateForLog(tag, 48))
		if len(normalized) == 8 {
			break
		}
	}
	return normalized
}

func validReflectionEventIDs(episode TaskEpisode, ids []string) []string {
	valid := make(map[string]bool, len(episode.Events))
	for _, event := range episode.Events {
		if strings.TrimSpace(event.EventID) != "" {
			valid[event.EventID] = true
		}
	}
	var result []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if valid[id] {
			result = appendUniqueString(result, id)
		}
	}
	return result
}

func reflectionEvidenceRef(episode TaskEpisode, eventIDs []string) MemorySourceRef {
	return MemorySourceRef{Type: "episode", ID: episode.ID, EventIDs: append([]string(nil), eventIDs...)}
}

func renderFailureMemoryContent(summary failureSummary) string {
	parts := []string{"Pattern: " + summary.Pattern}
	if strings.TrimSpace(summary.Cause) != "" {
		parts = append(parts, "Cause: "+strings.TrimSpace(summary.Cause))
	}
	if strings.TrimSpace(summary.MissedSignal) != "" {
		parts = append(parts, "Missed signal: "+strings.TrimSpace(summary.MissedSignal))
	}
	parts = append(parts, "Guard: "+summary.Guard)
	if strings.TrimSpace(summary.Scope) != "" {
		parts = append(parts, "Scope: "+strings.TrimSpace(summary.Scope))
	}
	return strings.Join(parts, "\n")
}

func reflectionEpisodeAppPage(episode TaskEpisode) (string, string) {
	for index := len(episode.Events) - 1; index >= 0; index-- {
		state := episode.Events[index].ObservedState
		if state == nil {
			continue
		}
		if strings.TrimSpace(state.AppName) != "" || strings.TrimSpace(state.PageName) != "" {
			return strings.TrimSpace(state.AppName), strings.TrimSpace(state.PageName)
		}
	}
	apps := inferEpisodeApps(episode)
	if len(apps) > 0 {
		return apps[0], ""
	}
	return "", ""
}

func hasEpisodeEvidence(refs []MemorySourceRef, episodeID string) bool {
	for _, ref := range refs {
		if ref.Type == "episode" && ref.ID == episodeID {
			return true
		}
	}
	return false
}

func distinctEpisodeEvidenceCount(refs []MemorySourceRef) int {
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.Type == "episode" && strings.TrimSpace(ref.ID) != "" {
			seen[ref.ID] = true
		}
	}
	return len(seen)
}

func containsStringFold(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}
