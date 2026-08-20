package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	episodeMemoryStateVersion     = 3
	legacyReflectionFailureTag    = "reflection:v1"
	episodeMemoryBatchLimit       = 5
	episodeMemoryRecentTerminals  = 64
	episodeMemoryProcessingLease  = 15 * time.Minute
	episodeMemoryRetryDelay       = 5 * time.Minute
	episodeMemoryMaxAttempts      = 3
	episodeMemoryModelCallTimeout = 60 * time.Second
	episodeMemoryBatchLockTimeout = 100 * time.Millisecond
)

type episodeMemoryProcessingStatus string

const (
	episodeMemoryStatusProcessing episodeMemoryProcessingStatus = "processing"
	episodeMemoryStatusProposed   episodeMemoryProcessingStatus = "proposed"
	episodeMemoryStatusDone       episodeMemoryProcessingStatus = "done"
	episodeMemoryStatusIgnored    episodeMemoryProcessingStatus = "ignored"
	episodeMemoryStatusRetry      episodeMemoryProcessingStatus = "retry"
)

type episodeMemoryEpisodeStatus struct {
	Status              episodeMemoryProcessingStatus `yaml:"status"`
	ExtractorVersion    int                           `yaml:"extractor_version,omitempty"`
	ProcessingStartedAt string                        `yaml:"processing_started_at,omitempty"`
	RetryAt             string                        `yaml:"retry_at,omitempty"`
	EndedAt             string                        `yaml:"ended_at,omitempty"`
	LastError           string                        `yaml:"last_error,omitempty"`
	AttemptCount        int                           `yaml:"attempt_count,omitempty"`
	Proposal            *episodeMemoryProposal        `yaml:"proposal,omitempty"`
	Assessment          *episodeMemoryAssessment      `yaml:"assessment,omitempty"`
}

type episodeMemoryStateFile struct {
	Version            int                                   `yaml:"version"`
	ExtractorVersion   int                                   `yaml:"extractor_version"`
	EnabledAt          string                                `yaml:"enabled_at"`
	CompletedThroughAt string                                `yaml:"completed_through_at,omitempty"`
	CompletedThroughID string                                `yaml:"completed_through_id,omitempty"`
	Episodes           map[string]episodeMemoryEpisodeStatus `yaml:"episodes,omitempty"`
}

type episodeMemoryStateStore struct {
	mu          sync.Mutex
	path        string
	bootstrapAt time.Time
}

func newEpisodeMemoryStateStore(path string, bootstrapAt time.Time) *episodeMemoryStateStore {
	return &episodeMemoryStateStore{path: path, bootstrapAt: bootstrapAt.UTC()}
}

func (s *episodeMemoryStateStore) Snapshot() (episodeMemoryStateFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, changed, err := s.loadLocked()
	if err != nil {
		return episodeMemoryStateFile{}, err
	}
	if changed {
		if err := writeYAMLAtomic(s.path, state); err != nil {
			return episodeMemoryStateFile{}, err
		}
	}
	return cloneEpisodeMemoryState(state), nil
}

func (s *episodeMemoryStateStore) SetEpisode(id string, status episodeMemoryEpisodeStatus) error {
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
		state.Episodes = map[string]episodeMemoryEpisodeStatus{}
	}
	key := episodeMemoryStateKey(id, status.ExtractorVersion)
	if status == (episodeMemoryEpisodeStatus{}) {
		delete(state.Episodes, key)
	} else {
		state.Episodes[key] = status
	}
	return writeYAMLAtomic(s.path, state)
}

func (s *episodeMemoryStateStore) CompleteEpisode(id string, endedAt time.Time, status episodeMemoryEpisodeStatus) error {
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
		state.Episodes = map[string]episodeMemoryEpisodeStatus{}
	}
	state.Episodes[episodeMemoryStateKey(id, status.ExtractorVersion)] = status
	setEpisodeMemoryCursor(&state, endedAt, id)
	pruneEpisodeMemoryTerminalStatuses(&state, episodeMemoryRecentTerminals)
	return writeYAMLAtomic(s.path, state)
}

func episodeMemoryStateKey(episodeID string, extractorVersion int) string {
	if extractorVersion <= 0 {
		extractorVersion = episodeMemoryExtractorVersion
	}
	return fmt.Sprintf("%s@v%d", strings.TrimSpace(episodeID), extractorVersion)
}

func (s *episodeMemoryStateStore) loadLocked() (episodeMemoryStateFile, bool, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return episodeMemoryStateFile{}, false, fmt.Errorf("read episode memory state: %w", err)
		}
		return episodeMemoryStateFile{
			Version:          episodeMemoryStateVersion,
			ExtractorVersion: episodeMemoryExtractorVersion,
			EnabledAt:        s.bootstrapAt.Format(time.RFC3339Nano),
			Episodes:         map[string]episodeMemoryEpisodeStatus{},
		}, true, nil
	}
	var state episodeMemoryStateFile
	if err := yaml.Unmarshal(data, &state); err != nil {
		return episodeMemoryStateFile{}, false, fmt.Errorf("decode episode memory state: %w", err)
	}
	changed := false
	if state.Version < episodeMemoryStateVersion {
		state.Version = episodeMemoryStateVersion
		state.ExtractorVersion = episodeMemoryExtractorVersion
		state.CompletedThroughAt = ""
		state.CompletedThroughID = ""
		state.Episodes = map[string]episodeMemoryEpisodeStatus{}
		changed = true
	}
	if state.ExtractorVersion != episodeMemoryExtractorVersion {
		state.ExtractorVersion = episodeMemoryExtractorVersion
		state.CompletedThroughAt = ""
		state.CompletedThroughID = ""
		changed = true
	}
	if strings.TrimSpace(state.EnabledAt) == "" {
		state.EnabledAt = s.bootstrapAt.Format(time.RFC3339Nano)
		changed = true
	}
	if state.Episodes == nil {
		state.Episodes = map[string]episodeMemoryEpisodeStatus{}
		changed = true
	}
	return state, changed, nil
}

func cloneEpisodeMemoryState(state episodeMemoryStateFile) episodeMemoryStateFile {
	cloned := state
	cloned.Episodes = make(map[string]episodeMemoryEpisodeStatus, len(state.Episodes))
	for id, status := range state.Episodes {
		if status.Proposal != nil {
			proposal := cloneEpisodeMemoryProposal(*status.Proposal)
			status.Proposal = &proposal
		}
		if status.Assessment != nil {
			assessment := *status.Assessment
			assessment.EvidenceRefs = append([]string(nil), status.Assessment.EvidenceRefs...)
			status.Assessment = &assessment
		}
		cloned.Episodes[id] = status
	}
	return cloned
}

func setEpisodeMemoryCursor(state *episodeMemoryStateFile, endedAt time.Time, episodeID string) {
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

func pruneEpisodeMemoryTerminalStatuses(state *episodeMemoryStateFile, limit int) {
	if state == nil || limit < 0 || len(state.Episodes) <= limit {
		return
	}
	type terminalStatus struct {
		id      string
		endedAt time.Time
	}
	terminals := make([]terminalStatus, 0, len(state.Episodes))
	for id, status := range state.Episodes {
		if status.Status != episodeMemoryStatusDone && status.Status != episodeMemoryStatusIgnored {
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

type episodeMemoryBatchResult struct {
	HasPending bool
	NextRunAt  time.Time
}

func episodeMemoryEntryAfterCursor(episodeID string, endedAt time.Time, state episodeMemoryStateFile) bool {
	cursorAt, err := time.Parse(time.RFC3339Nano, state.CompletedThroughAt)
	if err != nil {
		return true
	}
	return endedAt.After(cursorAt) || (endedAt.Equal(cursorAt) && episodeID > state.CompletedThroughID)
}

func episodeMemoryEpisodeEndedAt(episode TaskEpisode) (time.Time, error) {
	endedAt, err := time.Parse(time.RFC3339Nano, episode.EndedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse episode memory Episode %s ended_at: %w", episode.ID, err)
	}
	return endedAt, nil
}

func episodeMemoryEpisodeDue(status episodeMemoryEpisodeStatus, now time.Time) (bool, time.Time) {
	switch status.Status {
	case episodeMemoryStatusDone, episodeMemoryStatusIgnored:
		return false, time.Time{}
	case episodeMemoryStatusProposed:
		return true, time.Time{}
	case episodeMemoryStatusProcessing:
		startedAt, err := time.Parse(time.RFC3339Nano, status.ProcessingStartedAt)
		if err != nil {
			return true, time.Time{}
		}
		due := startedAt.Add(episodeMemoryProcessingLease)
		return !now.Before(due), due
	case episodeMemoryStatusRetry:
		retryAt, err := time.Parse(time.RFC3339Nano, status.RetryAt)
		if err != nil {
			return true, time.Time{}
		}
		return !now.Before(retryAt), retryAt
	default:
		return true, time.Time{}
	}
}

func sanitizeEpisodeMemoryScreenshotPaths(text string, screenshotEvents map[string]string) string {
	for ref, eventID := range screenshotEvents {
		text = strings.ReplaceAll(text, ref, "attached screenshot for event "+eventID)
	}
	return text
}

type episodeMemoryScreenshot struct {
	EventID  string
	MIMEType string
	Data     []byte
}

func loadEpisodeMemoryScreenshots(episodesRoot string, episode TaskEpisode) []episodeMemoryScreenshot {
	refs := selectEpisodeMemoryScreenshotRefs(episode.Events)
	if len(refs) == 0 {
		return nil
	}
	episodeDir := EpisodeDirectory(episodesRoot, episode)
	var screenshots []episodeMemoryScreenshot
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
		screenshots = append(screenshots, episodeMemoryScreenshot{EventID: eventID, MIMEType: mimeType, Data: data})
	}
	return screenshots
}

func selectEpisodeMemoryScreenshotRefs(events []TaskEpisodeEvent) []string {
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

func validEpisodeMemoryEventIDs(episode TaskEpisode, ids []string) []string {
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

func episodeMemoryEvidenceRef(episode TaskEpisode, eventIDs []string) MemorySourceRef {
	return MemorySourceRef{Type: "episode", ID: episode.ID, EventIDs: append([]string(nil), eventIDs...)}
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
