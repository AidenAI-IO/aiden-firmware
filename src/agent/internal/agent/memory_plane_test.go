package agent

import (
	"aiden-agent/internal/agent/screen"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	langtools "github.com/tmc/langchaingo/tools"
)

func TestPersistentEpisodeRecorderWritesIncrementalEventsAndMarksInterrupted(t *testing.T) {
	ctx := context.Background()
	store := NewTaskEpisodeStore(filepath.Join(t.TempDir(), "episodes"))
	recorder := NewPersistentEpisodeRecorder(MemoryRetrieveRequest{
		Input:     "打开设置",
		EpisodeID: "ep_incremental",
	}, MemoryContext{}, store)

	if err := recorder.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	recorder.append(TaskEpisodeEvent{
		Type:      "planner_decision",
		Role:      "agent",
		Objective: "打开设置",
		Plan:      []string{"打开设置"},
		NextStep:  "点击设置",
	})

	eventPaths, err := filepath.Glob(filepath.Join(store.rootDir, "*", "ep_incremental", "events.jsonl"))
	if err != nil || len(eventPaths) != 1 {
		t.Fatalf("episode events glob paths=%#v err=%v", eventPaths, err)
	}
	data, err := os.ReadFile(eventPaths[0])
	if err != nil {
		t.Fatalf("read incremental events: %v", err)
	}
	if !strings.Contains(string(data), `"planner_decision"`) {
		t.Fatalf("incremental event not persisted:\n%s", data)
	}

	marked, err := store.MarkRunningEpisodesInterrupted(ctx, "restart")
	if err != nil {
		t.Fatalf("MarkRunningEpisodesInterrupted() error = %v", err)
	}
	if marked != 1 {
		t.Fatalf("marked = %d, want 1", marked)
	}
	episode, err := store.Get(ctx, "ep_incremental")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if episode.Status != "interrupted" || episode.Outcome.FailureReason != "restart" {
		t.Fatalf("episode not marked interrupted: %#v", episode)
	}
	if len(episode.Events) != 1 || episode.Events[0].Type != "planner_decision" {
		t.Fatalf("unexpected persisted events: %#v", episode.Events)
	}
}

func TestTaskEpisodeIndexSummaryOmitsScreenshotBase64(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	store := NewTaskEpisodeStore(filepath.Join(memoryDir, "episodes"))
	image := base64.StdEncoding.EncodeToString([]byte("jpeg bytes"))

	if _, err := store.AddEpisode(ctx, TaskEpisode{
		ID:        "ep_screenshot_summary",
		Status:    "active",
		StartedAt: "2026-06-02T00:00:00Z",
		EndedAt:   "2026-06-02T00:00:10Z",
		UserGoal:  "截图任务",
		Outcome: TaskEpisodeOutcome{
			Success:    true,
			FinalState: `{"width":447,"height":972,"format":"jpeg","size":29392,"data":"` + image + `"}`,
		},
		Events: []TaskEpisodeEvent{
			{
				EventID:        "evt_tool",
				Type:           runEventToolCall,
				ToolName:       "screenshot",
				RawObservation: "",
			},
			{
				EventID:        "evt_result",
				Type:           "tool_result",
				ToolName:       "screenshot",
				Observation:    `{"width":447,"height":972,"format":"jpeg","size":29392,"data":"` + image + `"}`,
				RawObservation: `{"width":447,"height":972,"format":"jpeg","size":29392,"data":"` + image + `"}`,
			},
		},
	}); err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}

	for _, rel := range []string{
		filepath.Join("episodes", "index.yaml"),
		filepath.Join("episodes", "2026", "ep_screenshot_summary", "episode.yaml"),
		filepath.Join("episodes", "2026", "ep_screenshot_summary", "events.jsonl"),
	} {
		data, err := os.ReadFile(filepath.Join(memoryDir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if strings.Contains(string(data), `"data"`) || strings.Contains(string(data), image) {
			t.Fatalf("%s should not contain screenshot base64:\n%s", rel, data)
		}
	}
}

func TestRuntimeRetrieveUsesAutomaticScreenHints(t *testing.T) {
	ctx := context.Background()
	configDir := ensureTestConfigDir(t, t.TempDir())
	memoryDir := filepath.Join(configDir, "memory")
	longTerm := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	for _, item := range []MemoryItem{
		{
			ID:               "mem_matching_context",
			Type:             "procedure",
			Priority:         80,
			Confidence:       0.9,
			Tags:             []string{"登录"},
			Entities:         []string{"微信App"},
			Title:            "matching context",
			Content:          "This procedure applies to 640x1200.",
			Applicability:    map[string]string{"screen": "640x1200"},
			EvidenceExcerpts: []string{"matched evidence"},
		},
		{
			ID:               "mem_wrong_screen_runtime",
			Type:             "procedure",
			Priority:         100,
			Confidence:       0.9,
			Tags:             []string{"登录"},
			Entities:         []string{"微信App"},
			Title:            "wrong screen",
			Content:          "This should not apply to 640x1200.",
			Applicability:    map[string]string{"screen": "320x240"},
			EvidenceExcerpts: []string{"wrong screen evidence"},
		},
	} {
		if _, err := longTerm.AddMemory(ctx, item); err != nil {
			t.Fatalf("AddMemory(%s): %v", item.ID, err)
		}
	}

	screen := &screen.ScreenState{}
	screen.Update(640, 1200)
	model := &scriptedModel{responses: roleDirectResponses("done")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:   configDir,
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Answer directly.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(memoryDir),
		&ToolSet{tools: map[string]langtools.Tool{}, screen: screen},
		NewSkillIndex(),
	)
	if _, err := runtime.Run(ctx, RunRequest{Input: "登录微信App"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	plannerPrompt := messageText(model.messages[0])
	for _, unexpected := range []string{"mem_matching_context", "mem_wrong_screen_runtime"} {
		if strings.Contains(plannerPrompt, unexpected) {
			t.Fatalf("planner prompt should not contain %s:\n%s", unexpected, plannerPrompt)
		}
	}
}

func TestLongTermMemorySearchSkipsExpiredMemory(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_expired",
		Type:             "procedure",
		Priority:         90,
		Tags:             []string{"登录"},
		Title:            "过期流程",
		Content:          "旧流程。",
		EvidenceExcerpts: []string{"旧证据"},
		ExpiresAt:        "2000-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("AddMemory expired: %v", err)
	}
	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_active",
		Type:             "procedure",
		Priority:         80,
		Tags:             []string{"登录"},
		Title:            "有效流程",
		Content:          "新流程。",
		EvidenceExcerpts: []string{"新证据"},
		TTL:              "1d",
	}); err != nil {
		t.Fatalf("AddMemory active: %v", err)
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"登录"}, Limit: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].ID != "mem_active" {
		t.Fatalf("expected only active result, got %#v", results)
	}
}

func TestMemoryPlaneUpdatesReferencedMemoryOutcomes(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	longTerm := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_proc",
		Type:             "procedure",
		Status:           "active",
		Priority:         80,
		Confidence:       0.8,
		Tags:             []string{"登录"},
		Entities:         []string{"微信App"},
		Title:            "旧流程",
		Content:          "使用旧流程。",
		EvidenceExcerpts: []string{"旧流程曾成功。"},
		TTL:              "45d",
	}); err != nil {
		t.Fatalf("AddMemory procedure: %v", err)
	}
	device := NewDeviceMemoryStore(filepath.Join(memoryDir, "device"))
	if _, err := device.Upsert(ctx, DeviceMemoryItem{
		ID:         "dev_proc",
		Type:       "procedure",
		Status:     "active",
		Title:      "设备旧流程",
		Content:    "设备上的旧流程。",
		DeviceID:   "default",
		Tags:       []string{"登录"},
		Entities:   []string{"微信App"},
		Confidence: 0.8,
		TTL:        "45d",
	}); err != nil {
		t.Fatalf("Upsert device procedure: %v", err)
	}

	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	for _, episodeID := range []string{"ep_fail_1", "ep_fail_2"} {
		if err := plane.CommitEpisode(ctx, TaskEpisode{
			ID:                  episodeID,
			Status:              "active",
			StartedAt:           "2026-06-01T00:00:00Z",
			EndedAt:             "2026-06-01T00:00:10Z",
			UserGoal:            "登录微信App",
			Tags:                []string{"登录"},
			Entities:            []string{"微信App"},
			RetrievedMemoryRefs: []string{"mem_proc", "dev_proc"},
			Outcome:             TaskEpisodeOutcome{Success: false, FailureReason: "旧流程失败"},
			Events: []TaskEpisodeEvent{
				{EventID: episodeID + "_tool", Type: runEventToolCall, ToolName: "echo"},
			},
		}); err != nil {
			t.Fatalf("CommitEpisode(%s): %v", episodeID, err)
		}
	}
	proc, err := readMemoryMarkdown(longTerm.memoryPath("mem_proc"))
	if err != nil {
		t.Fatalf("read mem_proc: %v", err)
	}
	if proc.Item.Status != "conflicted" || proc.Item.FailureCount != 2 || proc.Item.Confidence >= 0.8 {
		t.Fatalf("expected referenced procedure to be conflicted with lower confidence, got %#v", proc.Item)
	}
	devHits, err := device.Search(ctx, DeviceMemoryQuery{Terms: []string{"设备旧流程"}, Limit: 10})
	if err != nil {
		t.Fatalf("device search: %v", err)
	}
	if len(devHits) == 0 || devHits[0].ID != "dev_proc" || devHits[0].Type != "conflict" {
		t.Fatalf("expected device procedure to become conflict, got %#v", devHits)
	}

	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_fail_fresh",
		Type:             "failure",
		Status:           "active",
		Priority:         80,
		Confidence:       0.8,
		Tags:             []string{"登录"},
		Entities:         []string{"微信App"},
		Title:            "旧失败模式",
		Content:          "这个目标会失败。",
		EvidenceExcerpts: []string{"旧失败证据。"},
	}); err != nil {
		t.Fatalf("AddMemory fresh failure: %v", err)
	}

	if err := plane.CommitEpisode(ctx, TaskEpisode{
		ID:                  "ep_success",
		Status:              "active",
		StartedAt:           "2026-06-01T00:01:00Z",
		EndedAt:             "2026-06-01T00:01:10Z",
		UserGoal:            "登录微信App",
		Tags:                []string{"登录"},
		Entities:            []string{"微信App"},
		RetrievedMemoryRefs: []string{"mem_fail_fresh"},
		Outcome:             TaskEpisodeOutcome{Success: true, VerifierReason: "成功完成"},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_success_tool", Type: runEventToolCall, ToolName: "echo"},
		},
	}); err != nil {
		t.Fatalf("CommitEpisode success: %v", err)
	}
	fail, err := readMemoryMarkdown(longTerm.memoryPath("mem_fail_fresh"))
	if err != nil {
		t.Fatalf("read mem_fail_fresh: %v", err)
	}
	if fail.Item.Status != "conflicted" || !containsMemoryPlaneString(fail.Item.ConflictsWith, "ep_success") {
		t.Fatalf("expected success to conflict with failure memory, got %#v", fail.Item)
	}
}

func TestMemoryPlaneUpdatesReferencedLongTermOutcomesInBatch(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	longTerm := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	refs := []string{"mem_batch_one", "mem_batch_two", "mem_batch_three"}
	for _, id := range refs {
		if _, err := longTerm.AddMemory(ctx, MemoryItem{
			ID:               id,
			Type:             "procedure",
			Status:           "active",
			Priority:         80,
			Confidence:       0.8,
			Title:            id,
			Content:          "Reusable procedure.",
			EvidenceExcerpts: []string{"Reusable procedure succeeded."},
			TTL:              "45d",
		}); err != nil {
			t.Fatalf("AddMemory(%s): %v", id, err)
		}
	}
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	plane.device = nil

	limitedCtx := newCancelAfterDoneContext(ctx, 5)
	if err := plane.updateReferencedMemoryOutcomes(limitedCtx, TaskEpisode{
		ID:                  "ep_batch_success",
		RetrievedMemoryRefs: refs,
		Outcome:             TaskEpisodeOutcome{Success: true},
	}); err != nil {
		t.Fatalf("updateReferencedMemoryOutcomes() error = %v", err)
	}

	for _, id := range refs {
		parsed, err := readMemoryMarkdown(longTerm.memoryPath(id))
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if parsed.Item.SuccessCount != 1 {
			t.Fatalf("%s SuccessCount = %d, want 1", id, parsed.Item.SuccessCount)
		}
	}
}

func TestMemoryPlaneUpdatesReferencedDeviceOutcomesInBatch(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	device := NewDeviceMemoryStore(filepath.Join(memoryDir, "device"))
	refs := []string{"dev_batch_one", "dev_batch_two", "dev_batch_three"}
	for _, id := range refs {
		if _, err := device.Upsert(ctx, DeviceMemoryItem{
			ID:         id,
			Type:       "procedure",
			Status:     "active",
			Title:      id,
			Content:    "Device procedure.",
			DeviceID:   defaultMemoryDeviceID,
			Confidence: 0.8,
			TTL:        "45d",
		}); err != nil {
			t.Fatalf("Upsert(%s): %v", id, err)
		}
	}
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	plane.longTerm = nil

	limitedCtx := newCancelAfterDoneContext(ctx, 5)
	if err := plane.updateReferencedMemoryOutcomes(limitedCtx, TaskEpisode{
		ID:                  "ep_batch_failure",
		RetrievedMemoryRefs: refs,
		Outcome:             TaskEpisodeOutcome{Success: false, FailureReason: "procedure failed"},
	}); err != nil {
		t.Fatalf("updateReferencedMemoryOutcomes() error = %v", err)
	}

	for _, id := range refs {
		item, found, err := device.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if !found {
			t.Fatalf("Get(%s) not found", id)
		}
		if item.FailureCount != 1 {
			t.Fatalf("%s FailureCount = %d, want 1", id, item.FailureCount)
		}
	}
}

type cancelAfterDoneContext struct {
	context.Context
	allowedOpenDoneCalls int

	mu    sync.Mutex
	calls int
	done  chan struct{}
	once  sync.Once
}

func newCancelAfterDoneContext(parent context.Context, allowedOpenDoneCalls int) *cancelAfterDoneContext {
	return &cancelAfterDoneContext{
		Context:              parent,
		allowedOpenDoneCalls: allowedOpenDoneCalls,
		done:                 make(chan struct{}),
	}
}

func (c *cancelAfterDoneContext) Done() <-chan struct{} {
	c.mu.Lock()
	c.calls++
	if c.calls > c.allowedOpenDoneCalls {
		c.once.Do(func() { close(c.done) })
	}
	done := c.done
	c.mu.Unlock()
	return done
}

func (c *cancelAfterDoneContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return c.Context.Err()
	}
}

func containsMemoryPlaneString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
