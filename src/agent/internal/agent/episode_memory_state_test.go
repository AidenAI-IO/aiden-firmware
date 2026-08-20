package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	modelpkg "aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type episodeMemoryScriptedModel struct {
	mu        sync.Mutex
	responses []string
	calls     [][]llms.MessageContent
}

type episodeMemoryBlockingModel struct {
	inner   *episodeMemoryScriptedModel
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *episodeMemoryBlockingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	blocked := false
	m.once.Do(func() {
		blocked = true
		close(m.started)
	})
	if blocked {
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.inner.GenerateContent(ctx, messages, options...)
}

func (m *episodeMemoryBlockingModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return m.inner.Call(ctx, prompt, options...)
}

func (m *episodeMemoryBlockingModel) CallOptions() []chains.ChainCallOption {
	return m.inner.CallOptions()
}

func (m *episodeMemoryBlockingModel) Spec() modelpkg.ModelSpec {
	return m.inner.Spec()
}

func (m *episodeMemoryScriptedModel) GenerateContent(_ context.Context, messages []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, messages)
	if len(m.responses) == 0 {
		return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: `{}`}}}, nil
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: response}}}, nil
}

func (m *episodeMemoryScriptedModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", nil
}

func (m *episodeMemoryScriptedModel) CallOptions() []chains.ChainCallOption { return nil }

func (m *episodeMemoryScriptedModel) Spec() modelpkg.ModelSpec { return modelpkg.ModelSpec{} }

func (m *episodeMemoryScriptedModel) appendResponses(responses ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, responses...)
}

func (m *episodeMemoryScriptedModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *episodeMemoryScriptedModel) firstCallHasImage() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return false
	}
	for _, message := range m.calls[0] {
		for _, part := range message.Parts {
			if _, ok := part.(llms.BinaryContent); ok {
				return true
			}
		}
	}
	return false
}

func (m *episodeMemoryScriptedModel) firstCallText() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return ""
	}
	var parts []string
	for _, message := range m.calls[0] {
		for _, part := range message.Parts {
			if textPart, ok := part.(llms.TextContent); ok {
				parts = append(parts, textPart.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func TestEpisodeMemoryEpisodeDueHonorsLeaseAndRetry(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		status   episodeMemoryEpisodeStatus
		eligible bool
		due      time.Time
	}{
		{
			name:     "processing lease active",
			status:   episodeMemoryEpisodeStatus{Status: episodeMemoryStatusProcessing, ProcessingStartedAt: now.Add(-episodeMemoryProcessingLease + time.Second).Format(time.RFC3339Nano)},
			eligible: false,
			due:      now.Add(time.Second),
		},
		{
			name:     "processing lease expired",
			status:   episodeMemoryEpisodeStatus{Status: episodeMemoryStatusProcessing, ProcessingStartedAt: now.Add(-episodeMemoryProcessingLease).Format(time.RFC3339Nano)},
			eligible: true,
			due:      now,
		},
		{
			name:     "retry waiting",
			status:   episodeMemoryEpisodeStatus{Status: episodeMemoryStatusRetry, RetryAt: now.Add(time.Second).Format(time.RFC3339Nano)},
			eligible: false,
			due:      now.Add(time.Second),
		},
		{
			name:     "retry due",
			status:   episodeMemoryEpisodeStatus{Status: episodeMemoryStatusRetry, RetryAt: now.Format(time.RFC3339Nano)},
			eligible: true,
			due:      now,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eligible, due := episodeMemoryEpisodeDue(test.status, now)
			if eligible != test.eligible || !due.Equal(test.due) {
				t.Fatalf("episodeMemoryEpisodeDue() = (%v, %v), want (%v, %v)", eligible, due, test.eligible, test.due)
			}
		})
	}
}

func TestEpisodeMemoryStateKeepsOnlyRecentTerminalEpisodes(t *testing.T) {
	base := time.Now().UTC()
	store := newEpisodeMemoryStateStore(filepath.Join(t.TempDir(), "reflection.yaml"), base.Add(-time.Minute))
	for index := 0; index < episodeMemoryRecentTerminals+6; index++ {
		id := fmt.Sprintf("ep_terminal_%03d", index)
		if err := store.CompleteEpisode(id, base.Add(time.Duration(index)*time.Second), episodeMemoryEpisodeStatus{Status: episodeMemoryStatusDone}); err != nil {
			t.Fatalf("CompleteEpisode(%s) error = %v", id, err)
		}
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(state.Episodes) != episodeMemoryRecentTerminals {
		t.Fatalf("terminal state count = %d, want %d", len(state.Episodes), episodeMemoryRecentTerminals)
	}
	if state.CompletedThroughID != fmt.Sprintf("ep_terminal_%03d", episodeMemoryRecentTerminals+5) {
		t.Fatalf("CompletedThroughID = %q, want last Episode", state.CompletedThroughID)
	}
	if _, found := state.Episodes[episodeMemoryStateKey("ep_terminal_000", episodeMemoryExtractorVersion)]; found {
		t.Fatal("old terminal Episode was not pruned")
	}
}

func TestNormalRecallHidesLegacyFailureMemories(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(t.TempDir())
	for _, item := range []DeviceMemoryItem{
		{ID: "legacy_failure", Type: "failure", Status: "active", Title: "legacy", Content: "legacy failure"},
		{ID: "reflected_failure", Type: "failure", Status: "active", Title: "reflected", Content: "reflected failure", Tags: []string{legacyReflectionFailureTag}},
	} {
		if _, err := store.Upsert(ctx, item); err != nil {
			t.Fatalf("Upsert(%s) error = %v", item.ID, err)
		}
	}
	hits, err := store.Search(ctx, DeviceMemoryQuery{Types: []string{"failure"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "reflected_failure" {
		t.Fatalf("failure recall hits = %#v, want only legacy-consolidation-managed memory", hits)
	}
}

func TestNormalRecallHidesConflictedLegacyFailureMemories(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(t.TempDir())
	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID:      "conflicted_reflection_failure",
		Type:    "failure",
		Status:  "conflicted",
		Title:   "conflicted reflected failure",
		Content: "conflicted reflected failure",
		Tags:    []string{legacyReflectionFailureTag},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	hits, err := store.Search(ctx, DeviceMemoryQuery{Types: []string{"conflict"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("conflicted legacy failure recall hits = %#v, want none", hits)
	}
}

func TestLongTermRecallHidesLegacyFailureMemories(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(t.TempDir())
	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "legacy_long_term_failure",
		Type:             "failure",
		Status:           "active",
		Priority:         80,
		Confidence:       0.8,
		Title:            "legacy failure",
		Content:          "legacy failure content",
		EvidenceExcerpts: []string{"legacy evidence"},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}
	hits, err := store.Search(ctx, MemoryQuery{Types: []string{"failure"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("legacy long-term failure recall hits = %#v, want none", hits)
	}
}

func TestRuntimeCallbackRecordsSteerInEpisode(t *testing.T) {
	recorder := NewEpisodeRecorder(MemoryRetrieveRequest{Input: "original goal", EpisodeID: "ep_steer"}, MemoryContext{})
	handler := &runtimeCallbackHandler{episode: recorder, episodeID: recorder.ID()}
	handler.HandleSteerMessage(context.Background(), RunSteerMessage{Content: "use the second result instead"})
	episode := recorder.Finish("", nil, context.Canceled, nil, nil)
	if len(episode.Events) != 1 || episode.Events[0].Type != "steer" || episode.Events[0].Content != "use the second result instead" {
		t.Fatalf("steer episode events = %#v", episode.Events)
	}
}

func TestFailureEpisodeDoesNotWriteImmediateFailureMemory(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	episode := episodeMemoryTestFailureEpisode("ep_no_direct_memory", "open settings")
	if err := plane.CommitEpisode(ctx, episode); err != nil {
		t.Fatalf("CommitEpisode() error = %v", err)
	}
	deviceItems, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("device readAll() error = %v", err)
	}
	for _, item := range deviceItems {
		if item.Type == "failure" {
			t.Fatalf("failure episode wrote immediate device memory: %#v", item)
		}
	}
	results, err := plane.longTerm.Search(ctx, MemoryQuery{Types: []string{"failure"}, Limit: 10})
	if err != nil {
		t.Fatalf("long-term Search() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("failure episode wrote immediate long-term memory: %#v", results)
	}
}

func episodeMemoryTestFailureEpisode(id, goal string) TaskEpisode {
	now := time.Now().UTC()
	return TaskEpisode{
		ID:        id,
		Status:    "active",
		StartedAt: now.Add(-time.Second).Format(time.RFC3339Nano),
		EndedAt:   now.Format(time.RFC3339Nano),
		UserGoal:  goal,
		Outcome: TaskEpisodeOutcome{
			Success:       false,
			FailureReason: "clicked the wrong result",
		},
		Events: []TaskEpisodeEvent{
			{
				EventID:   id + "_call",
				Type:      runEventToolCall,
				ToolName:  "mouse_click",
				ToolInput: `{"x":100,"y":200}`,
			},
			{
				EventID:     id + "_result",
				Type:        "tool_result",
				ToolName:    "mouse_click",
				Observation: "the wrong item opened",
				IsError:     true,
			},
		},
	}
}
