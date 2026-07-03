package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type TaskEpisodeStore struct {
	mu      sync.Mutex
	rootDir string
}

type TaskEpisodeWriter struct {
	plane *FilesystemMemoryPlane
}

type TaskEpisode struct {
	ID                  string                 `yaml:"id" json:"id"`
	Status              string                 `yaml:"status" json:"status"`
	StartedAt           string                 `yaml:"started_at" json:"started_at"`
	EndedAt             string                 `yaml:"ended_at" json:"ended_at"`
	UserGoal            string                 `yaml:"user_goal" json:"user_goal"`
	NormalizedGoal      map[string]string      `yaml:"normalized_goal,omitempty" json:"normalized_goal,omitempty"`
	DeviceScope         map[string]string      `yaml:"device_scope,omitempty" json:"device_scope,omitempty"`
	InitialState        TaskEpisodeState       `yaml:"initial_state,omitempty" json:"initial_state,omitempty"`
	Outcome             TaskEpisodeOutcome     `yaml:"outcome" json:"outcome"`
	RetrievedMemoryRefs []string               `yaml:"retrieved_memory_refs,omitempty" json:"retrieved_memory_refs,omitempty"`
	ReusableLessons     []string               `yaml:"reusable_lessons,omitempty" json:"reusable_lessons,omitempty"`
	FailureCauses       []string               `yaml:"failure_causes,omitempty" json:"failure_causes,omitempty"`
	Conflicts           []string               `yaml:"conflicts,omitempty" json:"conflicts,omitempty"`
	Tags                []string               `yaml:"tags,omitempty" json:"tags,omitempty"`
	Entities            []string               `yaml:"entities,omitempty" json:"entities,omitempty"`
	Events              []TaskEpisodeEvent     `yaml:"-" json:"events,omitempty"`
	Extra               map[string]interface{} `yaml:"extra,omitempty" json:"extra,omitempty"`
}

type TaskEpisodeState struct {
	Summary       string `yaml:"summary,omitempty" json:"summary,omitempty"`
	ScreenshotRef string `yaml:"screenshot_ref,omitempty" json:"screenshot_ref,omitempty"`
}

type TaskEpisodeOutcome struct {
	Success        bool   `yaml:"success" json:"success"`
	FinalState     string `yaml:"final_state,omitempty" json:"final_state,omitempty"`
	FinalAnswer    string `yaml:"final_answer,omitempty" json:"final_answer,omitempty"`
	VerifierReason string `yaml:"verifier_reason,omitempty" json:"verifier_reason,omitempty"`
	FailureReason  string `yaml:"failure_reason,omitempty" json:"failure_reason,omitempty"`
}

type TaskEpisodeEvent struct {
	EventID            string              `json:"event_id" yaml:"event_id"`
	Ts                 string              `json:"ts" yaml:"ts"`
	Type               string              `json:"type" yaml:"type"`
	Role               string              `json:"role,omitempty" yaml:"role,omitempty"`
	Objective          string              `json:"objective,omitempty" yaml:"objective,omitempty"`
	CompletionCriteria []string            `json:"completion_criteria,omitempty" yaml:"completion_criteria,omitempty"`
	Plan               []string            `json:"plan,omitempty" yaml:"plan,omitempty"`
	NextStep           string              `json:"next_step,omitempty" yaml:"next_step,omitempty"`
	ToolName           string              `json:"tool_name,omitempty" yaml:"tool_name,omitempty"`
	ToolInput          string              `json:"tool_input,omitempty" yaml:"tool_input,omitempty"`
	ToolError          *ToolError          `json:"tool_error,omitempty" yaml:"tool_error,omitempty"`
	Content            string              `json:"content,omitempty" yaml:"content,omitempty"`
	SpeechEligible     bool                `json:"speech_eligible,omitempty" yaml:"speech_eligible,omitempty"`
	Observation        string              `json:"observation,omitempty" yaml:"observation,omitempty"`
	ScreenshotRef      string              `json:"screenshot_ref,omitempty" yaml:"screenshot_ref,omitempty"`
	CanFinish          *bool               `json:"can_finish,omitempty" yaml:"can_finish,omitempty"`
	NeedsReplan        bool                `json:"needs_replan,omitempty" yaml:"needs_replan,omitempty"`
	Reason             string              `json:"reason,omitempty" yaml:"reason,omitempty"`
	IsError            bool                `json:"is_error,omitempty" yaml:"is_error,omitempty"`
	ObservedState      *observedWorldState `json:"observed_state,omitempty" yaml:"observed_state,omitempty"`
	RawObservation     string              `json:"-" yaml:"-"`
}

type EpisodeQuery struct {
	Terms          []string `json:"terms,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Entities       []string `json:"entities,omitempty"`
	Success        *bool    `json:"success,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	IncludeExpired bool     `json:"include_expired,omitempty"`
}

type episodeIndex struct {
	Version   int                 `yaml:"version"`
	UpdatedAt string              `yaml:"updated_at"`
	Episodes  []episodeIndexEntry `yaml:"episodes"`
}

type episodeIndexEntry struct {
	ID             string   `yaml:"id"`
	File           string   `yaml:"file"`
	EventsFile     string   `yaml:"events_file"`
	Status         string   `yaml:"status"`
	UserGoal       string   `yaml:"user_goal"`
	Summary        string   `yaml:"summary"`
	Success        bool     `yaml:"success"`
	StartedAt      string   `yaml:"started_at"`
	EndedAt        string   `yaml:"ended_at"`
	Tags           []string `yaml:"tags,omitempty"`
	Entities       []string `yaml:"entities,omitempty"`
	FailureReason  string   `yaml:"failure_reason,omitempty"`
	VerifierReason string   `yaml:"verifier_reason,omitempty"`
}

type EpisodeRecorder struct {
	mu        sync.Mutex
	id        string
	startedAt time.Time
	request   MemoryRetrieveRequest
	retrieved MemoryContext
	events    []TaskEpisodeEvent
	counter   int
	store     *TaskEpisodeStore
	started   bool
	startErr  error
}

func NewTaskEpisodeStore(rootDir string) *TaskEpisodeStore {
	return &TaskEpisodeStore{rootDir: rootDir}
}

func NewTaskEpisodeWriter(plane *FilesystemMemoryPlane) *TaskEpisodeWriter {
	if plane == nil {
		return nil
	}
	return &TaskEpisodeWriter{plane: plane}
}

func (w *TaskEpisodeWriter) Write(ctx context.Context, episode TaskEpisode) error {
	if w == nil || w.plane == nil {
		return nil
	}
	return w.plane.CommitEpisode(ctx, episode)
}

func NewEpisodeRecorder(req MemoryRetrieveRequest, retrieved MemoryContext) *EpisodeRecorder {
	return newEpisodeRecorder(req, retrieved, nil)
}

func NewPersistentEpisodeRecorder(req MemoryRetrieveRequest, retrieved MemoryContext, store *TaskEpisodeStore) *EpisodeRecorder {
	return newEpisodeRecorder(req, retrieved, store)
}

func newEpisodeRecorder(req MemoryRetrieveRequest, retrieved MemoryContext, store *TaskEpisodeStore) *EpisodeRecorder {
	startedAt := time.Now().UTC()
	id := strings.TrimSpace(req.EpisodeID)
	if id == "" {
		id = newTaskEpisodeID(startedAt)
	}
	return &EpisodeRecorder{
		id:        id,
		startedAt: startedAt,
		request:   req,
		retrieved: retrieved,
		store:     store,
	}
}

func newTaskEpisodeID(ts time.Time) string {
	return "ep_" + strconvTimeID(ts)
}

func (r *EpisodeRecorder) ID() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

func (r *EpisodeRecorder) Start(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.started {
		err := r.startErr
		r.mu.Unlock()
		return err
	}
	r.started = true
	if r.store == nil {
		r.mu.Unlock()
		return nil
	}
	episode := r.baseEpisodeLocked("running", time.Time{})
	r.mu.Unlock()

	if _, err := r.store.StartEpisode(ctx, episode); err != nil {
		r.mu.Lock()
		r.startErr = err
		r.mu.Unlock()
		return err
	}
	return nil
}

func (r *EpisodeRecorder) RecordDefaultFinish(answer string) {
	if r == nil {
		return
	}
	r.append(TaskEpisodeEvent{
		Type:    "default_finish",
		Role:    "agent",
		Content: strings.TrimSpace(answer),
	})
}

func (r *EpisodeRecorder) RecordPlannerExecution(result roleExecutionResult) {
	r.recordExecution(result)
}

func (r *EpisodeRecorder) RecordExecution(result roleExecutionResult) {
	r.recordExecution(result)
}

func (r *EpisodeRecorder) recordExecution(result roleExecutionResult) {
	if r == nil {
		return
	}
	if strings.TrimSpace(result.CandidateAnswer) != "" {
		r.append(TaskEpisodeEvent{
			Type:    "candidate_answer",
			Role:    "agent",
			Content: result.CandidateAnswer,
		})
	}
	if result.Action != nil {
		input := normalizeToolInput(result.Action.ToolInput)
		event := TaskEpisodeEvent{
			Type:      runEventToolCall,
			Role:      "agent",
			ToolName:  result.Action.Tool,
			ToolInput: input,
			Content:   toolContentFromAction(*result.Action),
		}
		r.append(event)
	}
	if result.Step != nil {
		event := TaskEpisodeEvent{
			Type:        "tool_result",
			Role:        "tool",
			Observation: compactToolObservation(result.Step.Observation),
			IsError:     result.ToolError != nil,
			ToolError:   cloneToolError(result.ToolError),
		}
		if result.Step.Action.Tool != "" {
			event.ToolName = result.Step.Action.Tool
			event.ToolInput = normalizeToolInput(result.Step.Action.ToolInput)
		}
		event.RawObservation = result.Step.Observation
		r.append(event)
	}
}

func (r *EpisodeRecorder) Finish(output string, metrics *RunMetrics, runErr error, tags []string, entities []string) TaskEpisode {
	if r == nil {
		return TaskEpisode{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	episode := r.baseEpisodeLocked("active", now)
	episode.Outcome = TaskEpisodeOutcome{Success: runErr == nil, FinalAnswer: strings.TrimSpace(output)}
	episode.Tags = uniqueNonEmpty(tags)
	episode.Entities = uniqueNonEmpty(entities)
	if metrics != nil {
		episode.Extra = map[string]interface{}{
			"total_duration_ms": metrics.TotalDuration,
			"prompt_tokens":     metrics.PromptTokens,
			"completion_tokens": metrics.CompletionTokens,
			"total_tokens":      metrics.TotalTokens,
		}
		if metrics.FirstTokenTime > 0 {
			episode.Extra["first_token_time_ms"] = metrics.FirstTokenTime
		}
	}
	if runErr != nil {
		episode.Outcome.FailureReason = runErr.Error()
		episode.FailureCauses = []string{runErr.Error()}
	}
	for i := len(r.events) - 1; i >= 0; i-- {
		evt := r.events[i]
		if evt.Type == "verifier_decision" {
			episode.Outcome.VerifierReason = evt.Reason
			if evt.CanFinish != nil {
				// The last verifier decision is authoritative: a clean run that the
				// verifier rejected (can_finish=false) must not be stored as success,
				// otherwise it feeds wrong lessons back into memory.
				episode.Outcome.Success = runErr == nil && *evt.CanFinish
				if *evt.CanFinish && strings.TrimSpace(evt.Content) != "" {
					episode.Outcome.FinalAnswer = strings.TrimSpace(evt.Content)
				}
			}
			break
		}
	}
	episode.Outcome.FinalState = inferEpisodeFinalState(episode.Events)
	episode.DeviceScope = mergeEpisodeDeviceScope(episode.DeviceScope, episode.Events)
	episode.ReusableLessons = inferReusableLessons(episode)
	return episode
}

func (r *EpisodeRecorder) append(event TaskEpisodeEvent) {
	r.mu.Lock()
	r.counter++
	now := time.Now().UTC()
	if event.EventID == "" {
		event.EventID = fmt.Sprintf("ep_evt_%s_%d", strconvTimeID(now), r.counter)
	}
	if event.Ts == "" {
		event.Ts = now.Format(time.RFC3339Nano)
	}
	r.events = append(r.events, event)
	store := r.store
	episode := r.baseEpisodeLocked("running", time.Time{})
	step := len(r.events)
	r.mu.Unlock()

	if store != nil {
		_ = store.AppendEpisodeEvent(context.Background(), episode, event, step)
	}
}

func (r *EpisodeRecorder) baseEpisodeLocked(status string, endedAt time.Time) TaskEpisode {
	episode := TaskEpisode{
		ID:                  r.id,
		Status:              status,
		StartedAt:           r.startedAt.Format(time.RFC3339Nano),
		UserGoal:            strings.TrimSpace(r.request.Input),
		DeviceScope:         r.deviceScope(),
		RetrievedMemoryRefs: r.retrieved.ReferenceIDs(),
		Events:              append([]TaskEpisodeEvent(nil), r.events...),
	}
	if !endedAt.IsZero() {
		episode.EndedAt = endedAt.Format(time.RFC3339Nano)
	}
	return episode
}

func (r *EpisodeRecorder) deviceScope() map[string]string {
	scope := map[string]string{}
	if deviceID := strings.TrimSpace(r.request.DeviceID); deviceID != "" {
		scope["device_id"] = deviceID
	}
	if r.request.CurrentHints.ScreenshotWidth > 0 && r.request.CurrentHints.ScreenshotHeight > 0 {
		scope["screen"] = fmt.Sprintf("%dx%d", r.request.CurrentHints.ScreenshotWidth, r.request.CurrentHints.ScreenshotHeight)
	}
	if language := strings.TrimSpace(r.request.CurrentHints.Language); language != "" {
		scope["language"] = language
	}
	if len(scope) == 0 {
		return nil
	}
	return scope
}

func mergeEpisodeDeviceScope(scope map[string]string, events []TaskEpisodeEvent) map[string]string {
	if len(scope) == 0 {
		scope = map[string]string{}
	}
	if _, ok := scope["screen"]; !ok {
		if screen := inferEpisodeScreen(events); screen != "" {
			scope["screen"] = screen
		}
	}
	if len(scope) == 0 {
		return nil
	}
	return scope
}

func (s *TaskEpisodeStore) AddEpisode(ctx context.Context, episode TaskEpisode) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if strings.TrimSpace(episode.ID) == "" {
		episode.ID = "ep_" + strconvTimeID(now)
	}
	if strings.TrimSpace(episode.Status) == "" {
		episode.Status = "active"
	}
	if strings.TrimSpace(episode.StartedAt) == "" {
		episode.StartedAt = now.Format(time.RFC3339Nano)
	}
	if strings.TrimSpace(episode.EndedAt) == "" {
		episode.EndedAt = now.Format(time.RFC3339Nano)
	}

	dir := s.episodeDir(episode)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		return "", fmt.Errorf("create episode dir: %w", err)
	}
	events := append([]TaskEpisodeEvent(nil), episode.Events...)
	for i := range events {
		if err := s.materializeEventArtifact(dir, &events[i], i+1); err != nil {
			return "", err
		}
	}
	if inferred := inferEpisodeFinalState(events); inferred != "" {
		episode.Outcome.FinalState = inferred
	}
	episode.Events = nil
	if err := writeYAMLAtomic(filepath.Join(dir, "episode.yaml"), episode); err != nil {
		return "", fmt.Errorf("write episode metadata: %w", err)
	}
	if err := writeEpisodeEventsJSONL(filepath.Join(dir, "events.jsonl"), events); err != nil {
		return "", err
	}
	if err := s.upsertIndexEntry(episode, dir, events); err != nil {
		return "", err
	}
	return episode.ID, nil
}

func (s *TaskEpisodeStore) StartEpisode(ctx context.Context, episode TaskEpisode) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if strings.TrimSpace(episode.ID) == "" {
		episode.ID = newTaskEpisodeID(now)
	}
	if strings.TrimSpace(episode.Status) == "" {
		episode.Status = "running"
	}
	if strings.TrimSpace(episode.StartedAt) == "" {
		episode.StartedAt = now.Format(time.RFC3339Nano)
	}

	dir := s.episodeDir(episode)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		return "", fmt.Errorf("create episode dir: %w", err)
	}
	episode.Events = nil
	if err := writeYAMLAtomic(filepath.Join(dir, "episode.yaml"), episode); err != nil {
		return "", fmt.Errorf("write episode metadata: %w", err)
	}
	if err := writeEpisodeEventsJSONL(filepath.Join(dir, "events.jsonl"), nil); err != nil {
		return "", err
	}
	if err := s.upsertIndexEntry(episode, dir, nil); err != nil {
		return "", err
	}
	return episode.ID, nil
}

func (s *TaskEpisodeStore) AppendEpisodeEvent(ctx context.Context, episode TaskEpisode, event TaskEpisodeEvent, step int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" || strings.TrimSpace(episode.ID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(episode.Status) == "" {
		episode.Status = "running"
	}
	if strings.TrimSpace(episode.StartedAt) == "" {
		episode.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	dir := s.episodeDir(episode)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		return fmt.Errorf("create episode dir: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "episode.yaml")); os.IsNotExist(err) {
		episode.Events = nil
		if err := writeYAMLAtomic(filepath.Join(dir, "episode.yaml"), episode); err != nil {
			return fmt.Errorf("write episode metadata: %w", err)
		}
		if err := s.upsertIndexEntry(episode, dir, nil); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if step <= 0 {
		step = 1
	}
	if err := s.materializeEventArtifact(dir, &event, step); err != nil {
		return err
	}
	event.RawObservation = ""
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open episode events: %w", err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(event); err != nil {
		return fmt.Errorf("append episode event: %w", err)
	}
	return nil
}

func (s *TaskEpisodeStore) MarkRunningEpisodesInterrupted(ctx context.Context, reason string) (int, error) {
	episodes, err := s.MarkRunningEpisodesInterruptedWithDetails(ctx, reason)
	return len(episodes), err
}

func (s *TaskEpisodeStore) MarkRunningEpisodesInterruptedWithDetails(ctx context.Context, reason string) ([]TaskEpisode, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return nil, nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "agent stopped before the task episode completed"
	}
	if _, err := os.Stat(s.indexPath()); os.IsNotExist(err) {
		if err := s.RebuildIndex(ctx); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	index, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	changed := false
	var interrupted []TaskEpisode
	for i := range index.Episodes {
		entry := index.Episodes[i]
		if entry.Status != "running" {
			continue
		}
		path := filepath.Join(s.rootDir, entry.File)
		data, err := os.ReadFile(path)
		if err != nil {
			return interrupted, err
		}
		var episode TaskEpisode
		if err := yaml.Unmarshal(data, &episode); err != nil {
			return interrupted, err
		}
		episode.Status = "interrupted"
		episode.EndedAt = now
		episode.Outcome.Success = false
		if strings.TrimSpace(episode.Outcome.FailureReason) == "" {
			episode.Outcome.FailureReason = reason
		}
		if len(episode.FailureCauses) == 0 {
			episode.FailureCauses = []string{reason}
		}
		dir := filepath.Dir(path)
		eventsPath := filepath.Join(dir, "events.jsonl")
		var events []TaskEpisodeEvent
		if _, err := os.Stat(eventsPath); err == nil {
			events, _ = readEpisodeEvents(eventsPath)
		}
		record := episode
		record.Events = append([]TaskEpisodeEvent(nil), events...)
		episode.Events = nil
		if err := writeYAMLAtomic(path, episode); err != nil {
			return interrupted, err
		}
		index.Episodes[i] = s.indexEntryForEpisode(episode, dir, events)
		interrupted = append(interrupted, record)
		changed = true
	}
	if changed {
		index.UpdatedAt = now
		if err := writeYAMLAtomic(s.indexPath(), index); err != nil {
			return interrupted, err
		}
	}
	return interrupted, nil
}

func (s *TaskEpisodeStore) Search(ctx context.Context, query EpisodeQuery) ([]MemoryHit, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return nil, nil
	}
	index, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 5
	}
	terms := normalizeSearchTerms(append(append([]string(nil), query.Terms...), append(query.Tags, query.Entities...)...))
	var matches []episodeIndexEntry
	for _, entry := range index.Episodes {
		if entry.Status != "active" {
			continue
		}
		if query.Success != nil && entry.Success != *query.Success {
			continue
		}
		if len(query.Tags) > 0 && !matchesAny(query.Tags, entry.Tags) {
			continue
		}
		if len(query.Entities) > 0 && !matchesAny(query.Entities, entry.Entities) {
			continue
		}
		if len(terms) > 0 && scoreEpisodeEntry(entry, terms) == 0 {
			continue
		}
		matches = append(matches, entry)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		scoreI := scoreEpisodeEntry(matches[i], terms)
		scoreJ := scoreEpisodeEntry(matches[j], terms)
		if scoreI == scoreJ {
			return matches[i].StartedAt > matches[j].StartedAt
		}
		return scoreI > scoreJ
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	hits := make([]MemoryHit, 0, len(matches))
	for _, entry := range matches {
		hits = append(hits, MemoryHit{
			ID:       entry.ID,
			Type:     "task_episode",
			Title:    entry.UserGoal,
			Summary:  entry.Summary,
			Content:  entry.Summary,
			Tags:     append([]string(nil), entry.Tags...),
			Entities: append([]string(nil), entry.Entities...),
			Source:   "episodes",
			FilePath: filepath.Join(s.rootDir, entry.File),
		})
	}
	return hits, nil
}

func (s *TaskEpisodeStore) Get(ctx context.Context, id string) (TaskEpisode, error) {
	select {
	case <-ctx.Done():
		return TaskEpisode{}, ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return TaskEpisode{}, fmt.Errorf("episode store is not configured")
	}
	index, err := s.loadIndex()
	if err != nil {
		return TaskEpisode{}, err
	}
	if episode, ok, err := s.getFromIndex(ctx, index, id); ok || err != nil {
		return episode, err
	}
	if err := s.RebuildIndex(ctx); err != nil {
		return TaskEpisode{}, err
	}
	index, err = s.loadIndex()
	if err != nil {
		return TaskEpisode{}, err
	}
	if episode, ok, err := s.getFromIndex(ctx, index, id); ok || err != nil {
		return episode, err
	}
	return TaskEpisode{}, fmt.Errorf("episode not found: %s", id)
}

func (s *TaskEpisodeStore) getFromIndex(ctx context.Context, index episodeIndex, id string) (TaskEpisode, bool, error) {
	for _, entry := range index.Episodes {
		if entry.ID != id {
			continue
		}
		path := filepath.Join(s.rootDir, entry.File)
		data, err := os.ReadFile(path)
		if err != nil {
			return TaskEpisode{}, true, err
		}
		var episode TaskEpisode
		if err := yaml.Unmarshal(data, &episode); err != nil {
			return TaskEpisode{}, true, err
		}
		if entry.EventsFile != "" {
			events, err := readEpisodeEvents(filepath.Join(s.rootDir, entry.EventsFile))
			if err != nil {
				return TaskEpisode{}, true, err
			}
			episode.Events = events
		}
		return episode, true, nil
	}
	return TaskEpisode{}, false, nil
}

func (s *TaskEpisodeStore) RebuildIndex(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var entries []episodeIndexEntry
	if _, err := os.Stat(s.rootDir); err != nil {
		if os.IsNotExist(err) {
			return writeYAMLAtomic(s.indexPath(), episodeIndex{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		}
		return err
	}
	err := filepath.WalkDir(s.rootDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "episode.yaml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var episode TaskEpisode
		if err := yaml.Unmarshal(data, &episode); err != nil {
			return fmt.Errorf("decode episode metadata %q: %w", path, err)
		}
		dir := filepath.Dir(path)
		eventsPath := filepath.Join(dir, "events.jsonl")
		var events []TaskEpisodeEvent
		if _, err := os.Stat(eventsPath); err == nil {
			events, _ = readEpisodeEvents(eventsPath)
		}
		entries = append(entries, s.indexEntryForEpisode(episode, dir, events))
		return nil
	})
	if err != nil {
		return err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].StartedAt > entries[j].StartedAt
	})
	return writeYAMLAtomic(s.indexPath(), episodeIndex{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Episodes:  entries,
	})
}

func (s *TaskEpisodeStore) episodeDir(episode TaskEpisode) string {
	year := "unknown"
	if parsed, err := time.Parse(time.RFC3339Nano, episode.StartedAt); err == nil {
		year = parsed.UTC().Format("2006")
	}
	return filepath.Join(s.rootDir, year, safePathName(episode.ID))
}

// EpisodeDirectory returns the on-disk directory for a committed episode.
func EpisodeDirectory(episodesRoot string, episode TaskEpisode) string {
	store := &TaskEpisodeStore{rootDir: episodesRoot}
	return store.episodeDir(episode)
}

func (s *TaskEpisodeStore) indexPath() string {
	return filepath.Join(s.rootDir, "index.yaml")
}

func (s *TaskEpisodeStore) loadIndex() (episodeIndex, error) {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return episodeIndex{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
		}
		return episodeIndex{}, fmt.Errorf("read episode index: %w", err)
	}
	var index episodeIndex
	if err := yaml.Unmarshal(data, &index); err != nil {
		return episodeIndex{}, fmt.Errorf("decode episode index: %w", err)
	}
	return index, nil
}

func (s *TaskEpisodeStore) upsertIndexEntry(episode TaskEpisode, dir string, events []TaskEpisodeEvent) error {
	index, err := s.loadIndex()
	if err != nil {
		return err
	}
	entry := s.indexEntryForEpisode(episode, dir, events)
	found := false
	for i := range index.Episodes {
		if index.Episodes[i].ID == entry.ID {
			index.Episodes[i] = entry
			found = true
			break
		}
	}
	if !found {
		index.Episodes = append(index.Episodes, entry)
	}
	sort.SliceStable(index.Episodes, func(i, j int) bool {
		return index.Episodes[i].StartedAt > index.Episodes[j].StartedAt
	})
	index.Version = 1
	index.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return writeYAMLAtomic(s.indexPath(), index)
}

func (s *TaskEpisodeStore) indexEntryForEpisode(episode TaskEpisode, dir string, events []TaskEpisodeEvent) episodeIndexEntry {
	relMeta, _ := filepath.Rel(s.rootDir, filepath.Join(dir, "episode.yaml"))
	relEvents := ""
	eventsPath := filepath.Join(dir, "events.jsonl")
	if _, err := os.Stat(eventsPath); err == nil {
		if rel, relErr := filepath.Rel(s.rootDir, eventsPath); relErr == nil {
			relEvents = filepath.ToSlash(rel)
		}
	}
	return episodeIndexEntry{
		ID:             episode.ID,
		File:           filepath.ToSlash(relMeta),
		EventsFile:     relEvents,
		Status:         episode.Status,
		UserGoal:       episode.UserGoal,
		Summary:        summarizeEpisodeForIndex(episode, events),
		Success:        episode.Outcome.Success,
		StartedAt:      episode.StartedAt,
		EndedAt:        episode.EndedAt,
		Tags:           append([]string(nil), episode.Tags...),
		Entities:       append([]string(nil), episode.Entities...),
		FailureReason:  episode.Outcome.FailureReason,
		VerifierReason: episode.Outcome.VerifierReason,
	}
}

func (s *TaskEpisodeStore) materializeEventArtifact(dir string, event *TaskEpisodeEvent, step int) error {
	raw := strings.TrimSpace(event.RawObservation)
	if raw == "" {
		return nil
	}
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil
	}
	if strings.TrimSpace(result.ScreenshotRef) != "" {
		event.ScreenshotRef = result.ScreenshotRef
		event.Observation = compactMaterializedScreenshotObservation(result)
		return nil
	}
	if result.Data == "" {
		return nil
	}
	imageBytes, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil || len(imageBytes) == 0 {
		return nil
	}
	format := strings.TrimSpace(result.Format)
	if format == "" {
		format = "jpeg"
	}
	name := fmt.Sprintf("step_%03d.%s", step, safePathName(format))
	rel := filepath.ToSlash(filepath.Join("artifacts", name))
	if err := os.WriteFile(filepath.Join(dir, rel), imageBytes, 0o644); err != nil {
		return fmt.Errorf("write episode artifact: %w", err)
	}
	event.ScreenshotRef = rel
	result.Format = format
	result.Size = len(imageBytes)
	result.Data = ""
	result.ScreenshotRef = rel
	event.Observation = compactMaterializedScreenshotObservation(result)
	return nil
}

func compactMaterializedScreenshotObservation(result postActionScreenshotResult) string {
	compact := map[string]interface{}{
		"width":          result.Width,
		"height":         result.Height,
		"format":         result.Format,
		"size":           result.Size,
		"screenshot_ref": result.ScreenshotRef,
	}
	if strings.TrimSpace(result.ActionOutput) != "" {
		compact["action_output"] = strings.TrimSpace(result.ActionOutput)
	}
	data, _ := json.Marshal(compact)
	return string(data)
}

func writeEpisodeEventsJSONL(path string, events []TaskEpisodeEvent) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open episode events: %w", err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, event := range events {
		event.RawObservation = ""
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("encode episode event: %w", err)
		}
	}
	return nil
}

func readEpisodeEvents(path string) ([]TaskEpisodeEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []TaskEpisodeEvent
	validData := make([]byte, 0)
	repairedTruncatedTail := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event TaskEpisodeEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			if isTruncatedJSONLineError(err) {
				repairedTruncatedTail = true
				break
			}
			return nil, err
		}
		validData = append(validData, line...)
		validData = append(validData, '\n')
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if repairedTruncatedTail {
		_ = writeFileAtomic(path, validData, 0o644)
	}
	return events, nil
}

func summarizeEpisodeForIndex(episode TaskEpisode, events []TaskEpisodeEvent) string {
	var parts []string
	goal := strings.TrimSpace(episode.UserGoal)
	if goal != "" {
		parts = append(parts, "Goal: "+goal)
	}
	var tools []string
	for _, event := range events {
		if event.Type == runEventToolCall && strings.TrimSpace(event.ToolName) != "" {
			tools = append(tools, event.ToolName)
		}
	}
	if len(tools) > 0 {
		parts = append(parts, "Path: "+strings.Join(tools, " -> "))
	}
	if episode.Outcome.Success {
		if final := strings.TrimSpace(episode.Outcome.FinalState); final != "" {
			parts = append(parts, "Outcome: success, "+final)
		} else {
			parts = append(parts, "Outcome: success")
		}
	} else {
		reason := strings.TrimSpace(episode.Outcome.FailureReason)
		if reason == "" {
			reason = firstNonEmptyString(episode.FailureCauses)
		}
		if reason != "" {
			parts = append(parts, "Outcome: failed, "+truncateForLog(reason, 160))
		} else {
			parts = append(parts, "Outcome: failed")
		}
	}
	return strings.Join(parts, ". ")
}

func scoreEpisodeEntry(entry episodeIndexEntry, terms []string) int {
	if len(terms) == 0 {
		return 1
	}
	haystack := strings.ToLower(strings.Join([]string{
		entry.UserGoal,
		entry.Summary,
		strings.Join(entry.Tags, " "),
		strings.Join(entry.Entities, " "),
		entry.FailureReason,
		entry.VerifierReason,
	}, " "))
	score := 0
	for _, term := range terms {
		if term != "" && strings.Contains(haystack, strings.ToLower(term)) {
			score++
		}
	}
	return score
}

func inferEpisodeFinalState(events []TaskEpisodeEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		evt := events[i]
		if evt.Type == "tool_result" && strings.TrimSpace(evt.Observation) != "" {
			return compactEpisodeObservation(evt.Observation)
		}
	}
	return ""
}

func compactEpisodeObservation(observation string) string {
	observation = strings.TrimSpace(observation)
	if observation == "" {
		return ""
	}
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(observation), &result); err == nil && result.Data != "" {
		format := strings.TrimSpace(result.Format)
		if format == "" {
			format = "jpeg"
		}
		compact := map[string]interface{}{
			"width":  result.Width,
			"height": result.Height,
			"format": format,
			"size":   result.Size,
		}
		if strings.TrimSpace(result.ActionOutput) != "" {
			compact["action_output"] = strings.TrimSpace(result.ActionOutput)
		}
		data, _ := json.Marshal(compact)
		return string(data)
	}
	return compactToolObservation(observation)
}

func inferReusableLessons(episode TaskEpisode) []string {
	if !episode.Outcome.Success {
		return nil
	}
	var tools []string
	for _, evt := range episode.Events {
		if evt.Type == runEventToolCall && strings.TrimSpace(evt.ToolName) != "" {
			tools = append(tools, evt.ToolName)
		}
	}
	if len(tools) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("For goal %q, verified tool path: %s.", truncateForLog(episode.UserGoal, 80), strings.Join(tools, " -> "))}
}

func episodeToolSequence(events []TaskEpisodeEvent) []string {
	var tools []string
	for _, evt := range events {
		if evt.Type == runEventToolCall && strings.TrimSpace(evt.ToolName) != "" {
			tools = append(tools, evt.ToolName)
		}
	}
	return tools
}

func writeYAMLAtomic(path string, value interface{}) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode yaml: %w", err)
	}
	return writeFileAtomic(path, data, 0o644)
}

func safePathName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		switch r {
		case '-', '_', '.':
			return r
		default:
			return '_'
		}
	}, value)
	if safe == "" {
		return "unknown"
	}
	return safe
}

func firstNonEmptyString(values []string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
