package agent

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

func TestMemoryPlaneRetrieveRoutesExperienceByRole(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	longTerm := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_open_wechat",
		Type:             "procedure",
		Priority:         80,
		Confidence:       0.9,
		Tags:             []string{"登录"},
		Entities:         []string{"微信App"},
		Title:            "微信打开路径",
		Content:          "打开微信App时优先使用系统搜索。",
		EvidenceExcerpts: []string{"成功用系统搜索打开微信。"},
	}); err != nil {
		t.Fatalf("AddMemory procedure: %v", err)
	}
	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_wechat_failure",
		Type:             "failure",
		Priority:         90,
		Confidence:       0.95,
		Tags:             []string{"登录"},
		Entities:         []string{"微信App"},
		Title:            "微信失败模式",
		Content:          "直接输入中文联系人容易失败，应使用拼音候选并用截图确认。",
		EvidenceExcerpts: []string{"中文输入未生效。"},
	}); err != nil {
		t.Fatalf("AddMemory failure: %v", err)
	}
	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_wrong_language",
		Type:             "procedure",
		Priority:         100,
		Confidence:       0.99,
		Tags:             []string{"登录"},
		Entities:         []string{"微信App"},
		Title:            "Wrong language procedure",
		Content:          "This should not apply to zh-CN.",
		Applicability:    map[string]string{"language": "en-US"},
		EvidenceExcerpts: []string{"English-only evidence."},
	}); err != nil {
		t.Fatalf("AddMemory wrong language: %v", err)
	}
	for _, item := range []MemoryItem{
		{
			ID:               "mem_conflict_a",
			Type:             "procedure",
			Priority:         90,
			Tags:             []string{"登录"},
			Entities:         []string{"微信App"},
			Title:            "冲突流程 A",
			Content:          "登录微信App时总是直接输入中文。",
			EvidenceExcerpts: []string{"A"},
		},
		{
			ID:               "mem_conflict_b",
			Type:             "procedure",
			Priority:         90,
			Tags:             []string{"登录"},
			Entities:         []string{"微信App"},
			Title:            "冲突流程 B",
			Content:          "登录微信App时不能直接输入中文。",
			EvidenceExcerpts: []string{"B"},
		},
	} {
		if _, err := longTerm.AddMemory(ctx, item); err != nil {
			t.Fatalf("AddMemory conflict item %s: %v", item.ID, err)
		}
	}
	if err := longTerm.MarkConflict(ctx, "mem_conflict_a", "mem_conflict_b", "contradictory procedures"); err != nil {
		t.Fatalf("MarkConflict: %v", err)
	}
	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_expired_conflict",
		Type:             "procedure",
		Status:           "conflicted",
		Priority:         100,
		Tags:             []string{"登录"},
		Entities:         []string{"微信App"},
		Title:            "过期冲突流程",
		Content:          "这条冲突已经过期，不应进入 verifier。",
		EvidenceExcerpts: []string{"expired"},
		ExpiresAt:        "2000-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("AddMemory expired conflict: %v", err)
	}
	episodes := NewTaskEpisodeStore(filepath.Join(memoryDir, "episodes"))
	if _, err := episodes.AddEpisode(ctx, TaskEpisode{
		ID:        "ep_success",
		Status:    "active",
		StartedAt: "2026-06-01T00:00:00Z",
		EndedAt:   "2026-06-01T00:00:10Z",
		UserGoal:  "打开微信App",
		Tags:      []string{"登录"},
		Entities:  []string{"微信App"},
		Outcome:   TaskEpisodeOutcome{Success: true, VerifierReason: "已打开"},
		Events: []TaskEpisodeEvent{
			{EventID: "evt_1", Type: runEventToolCall, ToolName: "touch_gesture"},
		},
	}); err != nil {
		t.Fatalf("AddEpisode success: %v", err)
	}
	if _, err := episodes.AddEpisode(ctx, TaskEpisode{
		ID:        "ep_failure",
		Status:    "active",
		StartedAt: "2026-06-01T00:01:00Z",
		EndedAt:   "2026-06-01T00:01:10Z",
		UserGoal:  "打开微信App",
		Tags:      []string{"登录"},
		Entities:  []string{"微信App"},
		Outcome:   TaskEpisodeOutcome{Success: false, FailureReason: "未找到入口"},
		Events: []TaskEpisodeEvent{
			{EventID: "evt_2", Type: "verifier_decision", Reason: "未找到入口"},
		},
	}); err != nil {
		t.Fatalf("AddEpisode failure: %v", err)
	}

	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	got, err := plane.Retrieve(ctx, MemoryRetrieveRequest{
		Input:    "登录微信App",
		DeviceID: "default",
		CurrentHints: CurrentEnvironmentHints{
			Language: "zh-CN",
		},
	})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}

	if len(got.Planner.Procedures) == 0 || got.Planner.Procedures[0].ID != "mem_open_wechat" {
		t.Fatalf("planner procedures = %#v", got.Planner.Procedures)
	}
	for _, hit := range got.Planner.Procedures {
		if hit.ID == "mem_wrong_language" || hit.ID == "mem_conflict_a" || hit.ID == "mem_conflict_b" {
			t.Fatalf("ineligible memory reached planner: %#v", got.Planner.Procedures)
		}
	}
	if len(got.Planner.SimilarEpisodes) == 0 || got.Planner.SimilarEpisodes[0].ID != "ep_success" {
		t.Fatalf("planner episodes = %#v", got.Planner.SimilarEpisodes)
	}
	if len(got.Verifier.FailureModes) < 2 {
		t.Fatalf("verifier failure modes = %#v", got.Verifier.FailureModes)
	}
	if len(got.Verifier.Conflicts) == 0 {
		t.Fatalf("expected conflicts to be routed to verifier")
	}
	for _, hit := range got.Verifier.Conflicts {
		if hit.ID == "mem_expired_conflict" {
			t.Fatalf("expired conflict reached verifier: %#v", got.Verifier.Conflicts)
		}
	}
	renderedAgent := got.Render()
	if !strings.Contains(renderedAgent, "Retrieved Device Experience") || !strings.Contains(renderedAgent, "mem_open_wechat") {
		t.Fatalf("agent memory prompt missing retrieved experience:\n%s", renderedAgent)
	}
	if !strings.Contains(renderedAgent, "Known Failure Modes") || !strings.Contains(renderedAgent, "mem_wechat_failure") {
		t.Fatalf("agent memory prompt missing failure memory:\n%s", renderedAgent)
	}
}

func TestMemoryPlaneRetrieveRefreshesPlannerExperienceForDifferentTaskInActiveSession(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	longTerm := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	for _, item := range []MemoryItem{
		{
			ID:               "mem_open_wechat",
			Type:             "procedure",
			Priority:         90,
			Confidence:       0.9,
			Entities:         []string{"微信App"},
			Title:            "微信打开路径",
			Content:          "打开微信App时先使用搜索。",
			EvidenceExcerpts: []string{"微信路径验证成功。"},
		},
		{
			ID:               "mem_open_alipay",
			Type:             "procedure",
			Priority:         90,
			Confidence:       0.9,
			Entities:         []string{"支付宝App"},
			Title:            "支付宝打开路径",
			Content:          "打开支付宝App时先使用搜索。",
			EvidenceExcerpts: []string{"支付宝路径验证成功。"},
		},
	} {
		if _, err := longTerm.AddMemory(ctx, item); err != nil {
			t.Fatalf("AddMemory(%s): %v", item.ID, err)
		}
	}

	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	first, err := plane.Retrieve(ctx, MemoryRetrieveRequest{
		Input:    "打开微信App",
		DeviceID: "default",
	})
	if err != nil {
		t.Fatalf("first Retrieve() error = %v", err)
	}
	if len(first.Planner.Procedures) == 0 || first.Planner.Procedures[0].ID != "mem_open_wechat" {
		t.Fatalf("first planner procedures = %#v", first.Planner.Procedures)
	}
	metadata := readSessionMetadataForTest(t, filepath.Join(memoryDir, "session", sessionMetadataFileName))
	if metadata.State == nil || metadata.State.RetrievedDeviceExperience == nil {
		t.Fatalf("session metadata missing retrieved device experience snapshot: %#v", metadata)
	}
	if got := metadata.State.RetrievedDeviceExperience.Planner.Procedures[0].ID; got != "mem_open_wechat" {
		t.Fatalf("metadata planner snapshot = %q, want mem_open_wechat", got)
	}
	if metadata.State.RetrievedDeviceExperience.QueryKey == "" {
		t.Fatalf("metadata planner snapshot missing query key")
	}
	if _, err := os.Stat(filepath.Join(memoryDir, "session", "retrieved_device_experience.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected feature-specific snapshot file, stat err = %v", err)
	}

	second, err := plane.Retrieve(ctx, MemoryRetrieveRequest{
		Input:    "打开支付宝App",
		DeviceID: "default",
	})
	if err != nil {
		t.Fatalf("second Retrieve() error = %v", err)
	}
	if len(second.Planner.Procedures) == 0 || second.Planner.Procedures[0].ID != "mem_open_alipay" {
		t.Fatalf("second planner procedures = %#v, want task-specific retrieval", second.Planner.Procedures)
	}
	for _, hit := range second.Planner.Procedures {
		if hit.ID == "mem_open_wechat" {
			t.Fatalf("different task should not reuse first planner snapshot: %#v", second.Planner.Procedures)
		}
	}
}

func TestMemoryPlaneRetrieveRefreshesPlannerExperienceForDifferentEpisodeInActiveSession(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	longTerm := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_wechat_old",
		Type:             "procedure",
		Priority:         70,
		Confidence:       0.8,
		Entities:         []string{"微信App"},
		Title:            "旧微信路径",
		Content:          "打开微信App时使用旧路径。",
		EvidenceExcerpts: []string{"旧路径验证成功。"},
	}); err != nil {
		t.Fatalf("AddMemory(old): %v", err)
	}

	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	first, err := plane.Retrieve(ctx, MemoryRetrieveRequest{
		Input:     "打开微信App",
		EpisodeID: "ep_first",
		DeviceID:  "default",
	})
	if err != nil {
		t.Fatalf("first Retrieve() error = %v", err)
	}
	if len(first.Planner.Procedures) == 0 || first.Planner.Procedures[0].ID != "mem_wechat_old" {
		t.Fatalf("first planner procedures = %#v", first.Planner.Procedures)
	}

	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_wechat_new",
		Type:             "procedure",
		Priority:         95,
		Confidence:       0.9,
		Entities:         []string{"微信App"},
		Title:            "新微信路径",
		Content:          "打开微信App时使用新路径。",
		EvidenceExcerpts: []string{"新路径验证成功。"},
	}); err != nil {
		t.Fatalf("AddMemory(new): %v", err)
	}

	second, err := plane.Retrieve(ctx, MemoryRetrieveRequest{
		Input:     "打开微信App",
		EpisodeID: "ep_second",
		DeviceID:  "default",
	})
	if err != nil {
		t.Fatalf("second Retrieve() error = %v", err)
	}
	if len(second.Planner.Procedures) == 0 || second.Planner.Procedures[0].ID != "mem_wechat_new" {
		t.Fatalf("second planner procedures = %#v, want refreshed memory", second.Planner.Procedures)
	}
}

func TestMemoryPlaneRetrieveRefreshesPlannerExperienceAfterSessionRotate(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	longTerm := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	for _, item := range []MemoryItem{
		{
			ID:               "mem_open_wechat",
			Type:             "procedure",
			Priority:         90,
			Confidence:       0.9,
			Entities:         []string{"微信App"},
			Title:            "微信打开路径",
			Content:          "打开微信App时先使用搜索。",
			EvidenceExcerpts: []string{"微信路径验证成功。"},
		},
		{
			ID:               "mem_open_alipay",
			Type:             "procedure",
			Priority:         90,
			Confidence:       0.9,
			Entities:         []string{"支付宝App"},
			Title:            "支付宝打开路径",
			Content:          "打开支付宝App时先使用搜索。",
			EvidenceExcerpts: []string{"支付宝路径验证成功。"},
		},
	} {
		if _, err := longTerm.AddMemory(ctx, item); err != nil {
			t.Fatalf("AddMemory(%s): %v", item.ID, err)
		}
	}

	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	if _, err := plane.Retrieve(ctx, MemoryRetrieveRequest{
		Input:    "打开微信App",
		DeviceID: "default",
	}); err != nil {
		t.Fatalf("first Retrieve() error = %v", err)
	}

	manager := NewMemoryManager(memoryDir)
	if err := manager.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Content: "打开微信App",
	}, SessionEventMetadata{}); err != nil {
		t.Fatalf("AppendSessionEvent() error = %v", err)
	}
	if _, err := manager.RotateSessionEventsDetailed(); err != nil {
		t.Fatalf("RotateSessionEventsDetailed() error = %v", err)
	}

	afterRotate, err := plane.Retrieve(ctx, MemoryRetrieveRequest{
		Input:    "打开支付宝App",
		DeviceID: "default",
	})
	if err != nil {
		t.Fatalf("second Retrieve() error = %v", err)
	}
	if len(afterRotate.Planner.Procedures) == 0 || afterRotate.Planner.Procedures[0].ID != "mem_open_alipay" {
		t.Fatalf("after rotate planner procedures = %#v", afterRotate.Planner.Procedures)
	}
}

func TestMemoryPlaneRetrieveIgnoresPlannerSnapshotWithUnknownVersion(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	longTerm := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_open_alipay",
		Type:             "procedure",
		Priority:         90,
		Confidence:       0.9,
		Entities:         []string{"支付宝App"},
		Title:            "支付宝打开路径",
		Content:          "打开支付宝App时先使用搜索。",
		EvidenceExcerpts: []string{"支付宝路径验证成功。"},
	}); err != nil {
		t.Fatalf("AddMemory(mem_open_alipay): %v", err)
	}

	sessionDir := filepath.Join(memoryDir, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(sessionDir): %v", err)
	}
	if err := writeSessionMetadata(filepath.Join(sessionDir, sessionMetadataFileName), sessionMetadata{
		SessionID: "session_stale_snapshot",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		State: &sessionState{
			RetrievedDeviceExperience: &sessionPlannerExperienceSnapshot{
				Version: sessionPlannerExperienceSnapshotVersion + 1,
				Planner: RoleMemoryContext{
					Procedures: []MemoryHit{{ID: "mem_stale"}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("writeSessionMetadata(): %v", err)
	}

	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	got, err := plane.Retrieve(ctx, MemoryRetrieveRequest{
		Input:    "打开支付宝App",
		DeviceID: "default",
	})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(got.Planner.Procedures) == 0 || got.Planner.Procedures[0].ID != "mem_open_alipay" {
		t.Fatalf("planner procedures = %#v, want fresh retrieval", got.Planner.Procedures)
	}
	for _, hit := range got.Planner.Procedures {
		if hit.ID == "mem_stale" {
			t.Fatalf("stale planner snapshot should be ignored: %#v", got.Planner.Procedures)
		}
	}

	metadata := readSessionMetadataForTest(t, filepath.Join(memoryDir, "session", sessionMetadataFileName))
	if metadata.State == nil || metadata.State.RetrievedDeviceExperience == nil {
		t.Fatalf("session metadata missing refreshed planner snapshot: %#v", metadata)
	}
	if got := metadata.State.RetrievedDeviceExperience.Version; got != sessionPlannerExperienceSnapshotVersion {
		t.Fatalf("snapshot version = %d, want %d", got, sessionPlannerExperienceSnapshotVersion)
	}
	if got := metadata.State.RetrievedDeviceExperience.Planner.Procedures[0].ID; got != "mem_open_alipay" {
		t.Fatalf("metadata planner snapshot = %q, want mem_open_alipay", got)
	}
}

func TestMemoryPlaneFreezePlannerSnapshotUsesFirstConcurrentRetriever(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)

	firstComputeStarted := make(chan struct{})
	firstMayFinish := make(chan struct{})
	secondCalled := make(chan struct{})
	secondComputeStarted := make(chan struct{})

	type retrieveResult struct {
		out MemoryContext
		err error
	}
	firstDone := make(chan retrieveResult, 1)
	secondDone := make(chan retrieveResult, 1)

	go func() {
		out, err := plane.retrieveWithFrozenPlannerSnapshot("same-query", func() (MemoryContext, error) {
			close(firstComputeStarted)
			<-firstMayFinish
			return MemoryContext{
				Planner: RoleMemoryContext{
					Procedures: []MemoryHit{{ID: "mem_first"}},
				},
			}, nil
		})
		firstDone <- retrieveResult{out: out, err: err}
	}()

	<-firstComputeStarted

	go func() {
		close(secondCalled)
		out, err := plane.retrieveWithFrozenPlannerSnapshot("same-query", func() (MemoryContext, error) {
			close(secondComputeStarted)
			return MemoryContext{
				Planner: RoleMemoryContext{
					Procedures: []MemoryHit{{ID: "mem_second"}},
				},
			}, nil
		})
		secondDone <- retrieveResult{out: out, err: err}
	}()

	<-secondCalled
	select {
	case <-secondComputeStarted:
		t.Fatalf("second concurrent retriever started computing before first freeze completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(firstMayFinish)

	first := <-firstDone
	if first.err != nil {
		t.Fatalf("first retrieveWithFrozenPlannerSnapshot() error = %v", first.err)
	}
	second := <-secondDone
	if second.err != nil {
		t.Fatalf("second retrieveWithFrozenPlannerSnapshot() error = %v", second.err)
	}

	if got := first.out.Planner.Procedures[0].ID; got != "mem_first" {
		t.Fatalf("first planner snapshot = %q, want mem_first", got)
	}
	if got := second.out.Planner.Procedures[0].ID; got != "mem_first" {
		t.Fatalf("second planner snapshot = %q, want mem_first", got)
	}

	metadata := readSessionMetadataForTest(t, filepath.Join(memoryDir, "session", sessionMetadataFileName))
	if metadata.State == nil || metadata.State.RetrievedDeviceExperience == nil {
		t.Fatalf("session metadata missing retrieved device experience snapshot: %#v", metadata)
	}
	if got := metadata.State.RetrievedDeviceExperience.Planner.Procedures[0].ID; got != "mem_first" {
		t.Fatalf("metadata planner snapshot = %q, want mem_first", got)
	}
}
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
	configDir := t.TempDir()
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

	screen := &screenState{}
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
