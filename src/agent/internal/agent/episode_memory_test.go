package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type panickingEpisodeMemoryBatchProcessor struct{}

func (panickingEpisodeMemoryBatchProcessor) Initialize() error { return nil }
func (panickingEpisodeMemoryBatchProcessor) NextRunAt(context.Context) (time.Time, error) {
	return time.Time{}, nil
}
func (panickingEpisodeMemoryBatchProcessor) ProcessBatch(context.Context, int, func() bool) (episodeMemoryBatchResult, error) {
	panic("batch failed")
}
func (panickingEpisodeMemoryBatchProcessor) logBatchError(error) {}

func TestEpisodeMemoryWorkerCleansUpPanickingBatch(t *testing.T) {
	worker := newEpisodeMemoryWorker(panickingEpisodeMemoryBatchProcessor{})
	worker.mu.Lock()
	batchCtx, cancel := worker.startBatchLocked(context.Background())
	worker.mu.Unlock()

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("executeBatch() did not propagate panic")
			}
		}()
		_, _ = worker.executeBatch(batchCtx, cancel)
	}()

	if batchCtx.Err() != context.Canceled {
		t.Fatalf("batch context error = %v, want canceled", batchCtx.Err())
	}
	done := make(chan struct{})
	go func() {
		worker.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker wait group leaked after panic")
	}
	worker.mu.Lock()
	running := worker.running
	worker.mu.Unlock()
	if running {
		t.Fatal("worker remained busy after panic")
	}
}

func TestProcessEpisodeMemoryNowContinuesAcrossBoundedBatches(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	response := `{
	  "episode_assessment":{"goal_result":"achieved","reason":"The app opened.","evidence_refs":["result"]},
	  "candidates":[]
	}`
	responses := make([]string, episodeMemoryBatchLimit+1)
	for index := range responses {
		responses[index] = response
	}
	model := &episodeMemoryScriptedModel{responses: responses}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for index := 0; index < episodeMemoryBatchLimit+1; index++ {
		episodeID := fmt.Sprintf("ep_batch_%02d", index)
		endedAt := time.Date(2026, 8, 14, 0, 0, index+1, 0, time.UTC)
		if _, err := plane.episodes.AddEpisode(ctx, TaskEpisode{
			ID: episodeID, Status: "active",
			StartedAt: endedAt.Add(-time.Second).Format(time.RFC3339Nano),
			EndedAt:   endedAt.Format(time.RFC3339Nano),
			UserGoal:  "Open Settings",
			Events: []TaskEpisodeEvent{
				{EventID: "call", Type: runEventToolCall, ToolName: "open_app"},
				{EventID: "result", Type: "tool_result", ToolName: "open_app", Observation: "Settings opened"},
			},
		}); err != nil {
			t.Fatalf("AddEpisode(%s) error = %v", episodeID, err)
		}
	}
	worker := newEpisodeMemoryWorker(processor)
	plane.episodeMemory = worker
	status, _, err := plane.ProcessEpisodeMemoryNow(ctx, fmt.Sprintf("ep_batch_%02d", episodeMemoryBatchLimit))
	if err != nil {
		t.Fatalf("ProcessEpisodeMemoryNow() error = %v", err)
	}
	if status.Status != episodeMemoryStatusDone {
		t.Fatalf("status = %q, want done", status.Status)
	}
	if got := model.callCount(); got != episodeMemoryBatchLimit+1 {
		t.Fatalf("model calls = %d, want %d", got, episodeMemoryBatchLimit+1)
	}
}

func TestProcessEpisodeMemoryNowWaitsForBusyWorker(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	inner := &episodeMemoryScriptedModel{responses: []string{`{
	  "episode_assessment":{"goal_result":"achieved","reason":"The app opened.","evidence_refs":["result"]},
	  "candidates":[]
	}`}}
	model := &episodeMemoryBlockingModel{inner: inner, started: make(chan struct{}), release: make(chan struct{})}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episodeID := "ep_busy_process_now"
	if _, err := plane.episodes.AddEpisode(ctx, TaskEpisode{
		ID: episodeID, Status: "active", StartedAt: "2026-08-14T00:00:00Z", EndedAt: "2026-08-14T00:00:01Z",
		UserGoal: "Open Settings",
		Events: []TaskEpisodeEvent{
			{EventID: "call", Type: runEventToolCall, ToolName: "open_app"},
			{EventID: "result", Type: "tool_result", ToolName: "open_app", Observation: "Settings opened"},
		},
	}); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	worker := newEpisodeMemoryWorker(processor)
	plane.episodeMemory = worker
	backgroundDone := make(chan error, 1)
	go func() {
		_, err := worker.ProcessNow(ctx)
		backgroundDone <- err
	}()
	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("background batch did not start")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(model.release)
	}()
	status, _, err := plane.ProcessEpisodeMemoryNow(ctx, episodeID)
	if err != nil {
		t.Fatalf("ProcessEpisodeMemoryNow() error = %v", err)
	}
	if status.Status != episodeMemoryStatusDone {
		t.Fatalf("status = %q, want done", status.Status)
	}
	if err := <-backgroundDone; err != nil {
		t.Fatalf("background ProcessNow() error = %v", err)
	}
}

func TestEpisodeMemoryProcessorPrefiltersNoiseAndProcessesSuccessfulDeviceEpisode(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"achieved","reason":"The tool result confirms the requested app opened.","evidence_refs":["ep_success_result"]},
  "candidates":[]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	for _, episode := range []TaskEpisode{
		{
			ID: "ep_greeting", Status: "active", StartedAt: "2026-08-14T00:00:00Z", EndedAt: "2026-08-14T00:00:01Z",
			UserGoal: "你好", Outcome: TaskEpisodeOutcome{Success: true, FinalAnswer: "你好！"},
			Events: []TaskEpisodeEvent{{EventID: "ep_greeting_answer", Type: "assistant_output", Content: "你好！"}},
		},
		{
			ID: "ep_memory_only", Status: "active", StartedAt: "2026-08-14T00:00:02Z", EndedAt: "2026-08-14T00:00:03Z",
			UserGoal: "记住我喜欢深色模式", Outcome: TaskEpisodeOutcome{Success: true},
			Events: []TaskEpisodeEvent{
				{EventID: "ep_memory_call", Type: runEventToolCall, ToolName: "save_memory", ToolInput: `{}`},
				{EventID: "ep_memory_result", Type: "tool_result", ToolName: "save_memory", Content: "saved"},
			},
		},
		{
			ID: "ep_success", Status: "active", StartedAt: "2026-08-14T00:00:04Z", EndedAt: "2026-08-14T00:00:05Z",
			UserGoal: "打开设置", Outcome: TaskEpisodeOutcome{Success: true, FinalState: "Settings is visible"},
			Events: []TaskEpisodeEvent{
				{EventID: "ep_success_call", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{"app":"Settings"}`},
				{EventID: "ep_success_result", Type: "tool_result", ToolName: "launch_app", Content: "opened Settings", Observation: "Settings is visible"},
			},
		},
	} {
		if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
			t.Fatalf("AddEpisode(%s) error = %v", episode.ID, err)
		}
	}

	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if got := model.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1 for only the successful device Episode", got)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if state.Episodes[episodeMemoryStateKey("ep_greeting", episodeMemoryExtractorVersion)].Status != episodeMemoryStatusIgnored || state.Episodes[episodeMemoryStateKey("ep_memory_only", episodeMemoryExtractorVersion)].Status != episodeMemoryStatusIgnored {
		t.Fatalf("noise statuses = %#v, want ignored", state.Episodes)
	}
	if state.Episodes[episodeMemoryStateKey("ep_success", episodeMemoryExtractorVersion)].Status != episodeMemoryStatusDone {
		t.Fatalf("successful device Episode status = %#v, want done", state.Episodes[episodeMemoryStateKey("ep_success", episodeMemoryExtractorVersion)])
	}
}

func TestEpisodeMemoryProcessorCreatesMultipleTypedMemoriesWithOneModelCall(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"achieved","reason":"Settings opened and the display page became visible.","evidence_refs":["ep_extract_result"]},
  "candidates":[
    {
      "lesson_key":"open_display_settings",
      "type":"procedure",
      "action":"create",
	  "retention":"durable",
      "memory_revision":1,
      "unresolved_conflict":false,
      "situation":"When the user wants the display settings on this device",
      "guidance":"Open Settings, then select Display",
      "expected_effect":"The Display settings page is visible",
      "scope":{"device_id":"device_a","app_name":"Settings","goal_pattern":"open display settings"},
      "tags":["settings","display"],
      "evidence_refs":["ep_extract_call","ep_extract_result","ep_extract_display_call","ep_extract_display_result"]
    },
    {
      "lesson_key":"settings_display_location",
      "type":"fact",
      "action":"create",
	  "retention":"durable",
      "unresolved_conflict":false,
      "situation":"In the Settings app on this device",
      "guidance":"Use the Display entry to reach display controls",
      "expected_effect":"Display controls can be found without searching",
      "scope":{"device_id":"device_a","app_name":"Settings"},
      "tags":["settings","display-entry"],
      "evidence_refs":["ep_extract_result"]
    }
  ]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := TaskEpisode{
		ID: "ep_extract", Status: "active", StartedAt: "2026-08-14T01:00:00Z", EndedAt: "2026-08-14T01:00:05Z",
		UserGoal: "打开显示设置", DeviceScope: map[string]string{"device_id": "device_a"},
		Outcome: TaskEpisodeOutcome{Success: true, FinalState: "Display settings visible"},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_extract_call", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{"app":"Settings"}`},
			{EventID: "ep_extract_result", Type: "tool_result", ToolName: "launch_app", Content: "Display settings visible", Observation: "Display settings visible"},
			{EventID: "ep_extract_display_call", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","point":{"x":500,"y":500}}`},
			{EventID: "ep_extract_display_result", Type: "tool_result", ToolName: "touch_gesture", Content: "Display page opened", Observation: "Display controls visible"},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if got := model.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want extraction and retention audit", got)
	}
	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("device memories = %#v, want 2", items)
	}
	types := map[string]bool{}
	for _, item := range items {
		types[item.Type] = true
		if item.Status != "active" || item.Revision != 1 || item.LessonKey == "" {
			t.Fatalf("created memory = %#v, want active revision 1 with lesson key", item)
		}
		if !hasEpisodeEvidence(item.EvidenceRefs, episode.ID) {
			t.Fatalf("created memory missing Episode evidence: %#v", item)
		}
	}
	if !types["procedure"] || !types["fact"] {
		t.Fatalf("memory types = %#v, want procedure and fact", types)
	}
}

func TestEpisodeMemoryProcessorUpdatesRevisionAndQuarantinesUnresolvedConflict(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	if _, err := plane.device.Upsert(ctx, DeviceMemoryItem{
		ID: "devmem_settings_fact", Type: "fact", Status: "active", Revision: 1,
		ExtractorVersion: episodeMemoryExtractorVersion, LessonKey: "settings_display_location",
		Title: "Display is under Settings", Summary: "Open Display from Settings",
		Content:  "Situation: In Settings\nGuidance: Open Display\nExpected effect: Display controls are visible",
		DeviceID: "device_a", AppName: "Settings", Tags: []string{episodeMemoryTag, "settings", "display"},
		Applicability: map[string]string{"device_id": "device_a", "app_name": "Settings"},
		EvidenceRefs:  []MemorySourceRef{{Type: "episode", ID: "ep_original", EventIDs: []string{"evt_original"}}},
	}); err != nil {
		t.Fatalf("Upsert(existing) error = %v", err)
	}
	model := &episodeMemoryScriptedModel{responses: []string{
		`{
  "episode_assessment":{"goal_result":"achieved","reason":"The Display entry opened the controls.","evidence_refs":["ep_update_result"]},
  "candidates":[{
    "lesson_key":"settings_display_location_update","type":"fact","action":"update","retention":"durable","memory_id":"devmem_settings_fact","memory_revision":1,
    "unresolved_conflict":false,"situation":"In Settings on device A","guidance":"Open the Display entry from the main list","expected_effect":"Display controls are visible",
    "scope":{"device_id":"device_a","app_name":"Settings","page_name":"main"},"tags":["settings","display"],
    "evidence_refs":["ep_update_result"]
  }]
}`,
		`{
  "episode_assessment":{"goal_result":"unknown","reason":"The same scope now shows a different location and there is not enough evidence to condition it.","evidence_refs":["ep_conflict_result"]},
  "candidates":[{
    "lesson_key":"settings_display_location_conflict","type":"fact","action":"update","retention":"durable","memory_id":"devmem_settings_fact","memory_revision":2,
    "unresolved_conflict":true,"conflict_reason":"The same Settings scope showed an incompatible location without a distinguishing precondition.",
    "situation":"In Settings on device A","guidance":"Do not rely on one fixed Display location until the differing UI states can be distinguished","expected_effect":"The agent avoids following an unsafe location rule",
    "scope":{"device_id":"device_a","app_name":"Settings"},"tags":["settings","display"],
    "evidence_refs":["ep_conflict_result"]
  }]
}`,
	}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for index, episode := range []TaskEpisode{
		{
			ID: "ep_update", Status: "active", StartedAt: "2026-08-14T02:00:00Z", EndedAt: "2026-08-14T02:00:05Z",
			UserGoal: "打开设置里的显示", DeviceScope: map[string]string{"device_id": "device_a"}, Outcome: TaskEpisodeOutcome{Success: true},
			Events: []TaskEpisodeEvent{
				{EventID: "ep_update_call", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","point":{"x":500,"y":500}}`},
				{EventID: "ep_update_result", Type: "tool_result", ToolName: "touch_gesture", Content: "Display controls visible"},
			},
		},
		{
			ID: "ep_conflict", Status: "active", StartedAt: "2026-08-14T03:00:00Z", EndedAt: "2026-08-14T03:00:05Z",
			UserGoal: "打开设置里的显示", DeviceScope: map[string]string{"device_id": "device_a"}, Outcome: TaskEpisodeOutcome{Success: false},
			Events: []TaskEpisodeEvent{
				{EventID: "ep_conflict_call", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","point":{"x":500,"y":500}}`},
				{EventID: "ep_conflict_result", Type: "tool_result", ToolName: "touch_gesture", Content: "A different Settings section opened"},
			},
		},
	} {
		if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
			t.Fatalf("AddEpisode(%d) error = %v", index, err)
		}
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if got := model.callCount(); got != 4 {
		t.Fatalf("model calls = %d, want extraction and retention audit per Episode", got)
	}
	updated, found, err := plane.device.Get(ctx, "devmem_settings_fact")
	if err != nil || !found {
		t.Fatalf("Get(updated) found=%v error=%v", found, err)
	}
	if updated.Status != "disputed" || updated.Revision != 3 || len(updated.RevisionHistory) != 2 {
		t.Fatalf("updated memory = %#v, want disputed revision 3 with two prior revisions", updated)
	}
	if distinctEpisodeEvidenceCount(updated.EvidenceRefs) != 3 {
		t.Fatalf("evidence refs = %#v, want original plus two updates", updated.EvidenceRefs)
	}
	hits, err := plane.device.Search(ctx, DeviceMemoryQuery{Terms: []string{"Display"}, Limit: 8})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("normal Recall returned disputed memory: %#v", hits)
	}
}

func TestEpisodeMemoryProcessorResumesPersistedProposalWithoutCallingModelAgain(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := TaskEpisode{
		ID: "ep_resume", Status: "active", StartedAt: "2026-08-14T04:00:00Z", EndedAt: "2026-08-14T04:00:05Z",
		UserGoal: "查看当前屏幕尺寸", DeviceScope: map[string]string{"device_id": "device_a"}, Outcome: TaskEpisodeOutcome{Success: true},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_resume_call", Type: runEventToolCall, ToolName: "screenshot", ToolInput: `{}`},
			{EventID: "ep_resume_result", Type: "tool_result", ToolName: "screenshot", Observation: `{"width":1080,"height":1920}`},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	proposal := episodeMemoryProposal{
		EpisodeAssessment: episodeMemoryAssessment{GoalResult: "achieved", Reason: "The screenshot result reports the dimensions.", EvidenceRefs: []string{"ep_resume_result"}},
		Candidates: []episodeMemoryCandidate{{
			LessonKey: "screen_dimensions", Type: "fact", Action: "create", Retention: episodeMemoryRetentionDurable,
			Situation: "When operating device A", Guidance: "Use a 1080x1920 screen model", ExpectedEffect: "Coordinates are interpreted against the observed screen size",
			Scope: map[string]string{"device_id": "device_a", "screen": "1080x1920"}, Tags: []string{"screen", "dimensions"}, EvidenceRefs: []string{"ep_resume_result"},
		}},
	}
	if err := processor.state.SetEpisode(episode.ID, episodeMemoryEpisodeStatus{
		Status: episodeMemoryStatusProposed, ExtractorVersion: episodeMemoryExtractorVersion, Proposal: &proposal,
	}); err != nil {
		t.Fatalf("SetEpisode(proposed) error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if got := model.callCount(); got != 0 {
		t.Fatalf("model calls = %d, want 0 when resuming proposed output", got)
	}
	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 1 || items[0].LessonKey != "screen_dimensions" {
		t.Fatalf("resumed memories = %#v, want persisted proposal applied once", items)
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch(second) error = %v", err)
	}
	items, err = plane.device.readAll()
	if err != nil || len(items) != 1 {
		t.Fatalf("second pass memories = %#v error=%v, want no duplicate", items, err)
	}
}

func TestEpisodeMemoryProcessorDoesNotCarryErrorIntoNextPersistedProposal(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	failedEpisode := TaskEpisode{
		ID: "ep_extract_error", Status: "active", StartedAt: "2026-08-14T04:10:00Z", EndedAt: "2026-08-14T04:10:05Z",
		UserGoal: "打开设置", DeviceScope: map[string]string{"device_id": "device_a"}, Outcome: TaskEpisodeOutcome{Success: false},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_extract_error_call", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{"app":"Settings"}`},
			{EventID: "ep_extract_error_result", Type: "tool_result", ToolName: "launch_app", Content: "request failed"},
		},
	}
	resumedEpisode := TaskEpisode{
		ID: "ep_resume_after_error", Status: "active", StartedAt: "2026-08-14T04:11:00Z", EndedAt: "2026-08-14T04:11:05Z",
		UserGoal: "查看当前屏幕尺寸", DeviceScope: map[string]string{"device_id": "device_a"}, Outcome: TaskEpisodeOutcome{Success: true},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_resume_after_error_call", Type: runEventToolCall, ToolName: "screenshot", ToolInput: `{}`},
			{EventID: "ep_resume_after_error_result", Type: "tool_result", ToolName: "screenshot", Observation: `{"width":1080,"height":1920}`},
		},
	}
	for _, episode := range []TaskEpisode{failedEpisode, resumedEpisode} {
		if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
			t.Fatalf("AddEpisode(%s) error = %v", episode.ID, err)
		}
	}
	proposal := episodeMemoryProposal{
		EpisodeAssessment: episodeMemoryAssessment{GoalResult: "achieved", Reason: "The screenshot reports the dimensions.", EvidenceRefs: []string{"ep_resume_after_error_result"}},
		Candidates: []episodeMemoryCandidate{{
			LessonKey: "screen_dimensions_after_error", Type: "fact", Action: "create", Retention: episodeMemoryRetentionDurable,
			Situation: "When operating device A", Guidance: "Use a 1080x1920 screen model", ExpectedEffect: "Coordinates use the observed dimensions",
			Scope: map[string]string{"device_id": "device_a", "screen": "1080x1920"}, Tags: []string{"screen", "dimensions"}, EvidenceRefs: []string{"ep_resume_after_error_result"},
		}},
	}
	if err := processor.state.SetEpisode(resumedEpisode.ID, episodeMemoryEpisodeStatus{
		Status: episodeMemoryStatusProposed, ExtractorVersion: episodeMemoryExtractorVersion, Proposal: &proposal,
	}); err != nil {
		t.Fatalf("SetEpisode(proposed) error = %v", err)
	}

	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if got := model.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want only the failing extraction call", got)
	}
	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 1 || items[0].LessonKey != "screen_dimensions_after_error" {
		t.Fatalf("memories = %#v, want persisted proposal applied after prior error", items)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if status := state.Episodes[episodeMemoryStateKey(failedEpisode.ID, episodeMemoryExtractorVersion)]; status.Status != episodeMemoryStatusIgnored {
		t.Fatalf("failed extraction status = %#v, want ignored", status)
	}
	if status := state.Episodes[episodeMemoryStateKey(resumedEpisode.ID, episodeMemoryExtractorVersion)]; status.Status != episodeMemoryStatusDone {
		t.Fatalf("persisted proposal status = %#v, want done", status)
	}
}

func TestEpisodeMemoryProcessorUsesGoalResultToRejectFalseSuccessProcedure(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"not_achieved","reason":"The user said the requested message was not sent.","evidence_refs":["ep_false_success_steer"]},
  "candidates":[
    {
      "lesson_key":"send_message_path","type":"procedure","action":"create","retention":"durable","unresolved_conflict":false,
      "situation":"When sending a message","guidance":"Tap Send after entering text","expected_effect":"The requested message is sent",
      "scope":{"device_id":"device_a","app_name":"Messages","goal_pattern":"send message"},"tags":["messages"],
      "evidence_refs":["ep_false_success_call","ep_false_success_result"]
    },
    {
      "lesson_key":"verify_message_sent","type":"failure","action":"create","retention":"durable","unresolved_conflict":false,
      "situation":"After attempting to send a message","guidance":"Verify the requested message appears as sent before reporting completion","expected_effect":"The agent does not claim success when the message was not sent",
      "scope":{"device_id":"device_a","app_name":"Messages"},"tags":["messages","verify-before-finish"],
      "evidence_refs":["ep_false_success_result","ep_false_success_steer"]
    }
  ]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := TaskEpisode{
		ID: "ep_false_success", Status: "active", StartedAt: "2026-08-14T05:00:00Z", EndedAt: "2026-08-14T05:00:05Z",
		UserGoal: "发送消息", DeviceScope: map[string]string{"device_id": "device_a"},
		Outcome: TaskEpisodeOutcome{Success: true, FinalAnswer: "已发送"},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_false_success_call", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","point":{"x":900,"y":900}}`},
			{EventID: "ep_false_success_result", Type: "tool_result", ToolName: "touch_gesture", Content: "tap completed"},
			{EventID: "ep_false_success_steer", Type: "steer", Content: "并没有发送出去"},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 1 || items[0].Type != "failure" || items[0].LessonKey != "verify_message_sent" {
		t.Fatalf("memories = %#v, want only reusable failure guard", items)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if assessment := state.Episodes[episodeMemoryStateKey(episode.ID, episodeMemoryExtractorVersion)].Assessment; assessment == nil || assessment.GoalResult != "not_achieved" {
		t.Fatalf("persisted assessment = %#v, want not_achieved audit result", assessment)
	}
}

func TestEpisodeMemoryProcessorAcceptsNonCanceledStructuredErrorWithoutPairedDeviceCall(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"not_achieved","reason":"The tool returned a structured execution error.","evidence_refs":["ep_error_result"]},
  "candidates":[]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for _, episode := range []TaskEpisode{
		{
			ID: "ep_error", Status: "active", StartedAt: "2026-08-14T06:00:00Z", EndedAt: "2026-08-14T06:00:01Z", UserGoal: "打开应用",
			Events: []TaskEpisodeEvent{{EventID: "ep_error_result", Type: "tool_result", IsError: true, ToolError: NewToolError(CodeToolExecutionFailed, "launch failed")}},
		},
		{
			ID: "ep_canceled_only", Status: "active", StartedAt: "2026-08-14T06:00:02Z", EndedAt: "2026-08-14T06:00:03Z", UserGoal: "打开应用",
			Events: []TaskEpisodeEvent{{EventID: "ep_canceled_result", Type: "tool_result", IsError: true, ToolError: NewToolError(CodeCanceled, "canceled")}},
		},
	} {
		if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
			t.Fatalf("AddEpisode(%s) error = %v", episode.ID, err)
		}
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if got := model.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want only the non-canceled structured error", got)
	}
}

func TestEpisodeMemoryModelInputUsesDirectEvidenceWithoutVerifierState(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"unknown","reason":"The final visible state is insufficient.","evidence_refs":[]},
  "candidates":[]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{
		ID: "ep_direct_evidence", Status: "active", StartedAt: "2026-08-14T07:00:00Z", EndedAt: "2026-08-14T07:00:01Z", UserGoal: "检查当前页面",
		Outcome: TaskEpisodeOutcome{Success: true, VerifierReason: "SECRET_VERIFIER_REASON"},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_direct_call", Type: runEventToolCall, ToolName: "screenshot"},
			{
				EventID: "ep_direct_result", Type: "tool_result", ToolName: "screenshot",
				ObservedState:  &observedWorldState{AppName: "SECRET_OBSERVED_APP", PageName: "SECRET_OBSERVED_PAGE"},
				RawObservation: `{"width":10,"height":10,"format":"jpeg","data":"` + base64.StdEncoding.EncodeToString([]byte("jpeg")) + `"}`,
			},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	stored, err := plane.episodes.Get(ctx, episode.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if _, err := processor.proposeEpisode(ctx, stored); err != nil {
		t.Fatalf("proposeEpisode() error = %v", err)
	}
	prompt := model.firstCallText()
	for _, forbidden := range []string{"SECRET_VERIFIER_REASON", "SECRET_OBSERVED_APP", "SECRET_OBSERVED_PAGE", "verifier_reason", "observed_state"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("Episode Memory model input contains %q:\n%s", forbidden, prompt)
		}
	}
	if !strings.Contains(prompt, "For action=create, omit memory_id and memory_revision.") {
		t.Fatal("Episode Memory model input does not distinguish create fields from update fields")
	}
	if strings.Contains(prompt, `"memory_revision": 1`) {
		t.Fatal("Episode Memory create schema still presents memory_revision as a required field")
	}
	if !strings.Contains(prompt, "must emit at least one candidate") {
		t.Fatal("Episode Memory model input does not require retention of directly verified reusable lessons")
	}
	if !model.firstCallHasImage() {
		t.Fatal("Episode Memory model input did not attach the persisted screenshot")
	}
}

func TestEpisodeMemoryWorkerCancelsBackgroundModelWhenForegroundTaskStarts(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	inner := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"unknown","reason":"No final visible proof was recorded.","evidence_refs":[]},
  "candidates":[]
}`}}
	model := &episodeMemoryBlockingModel{inner: inner, started: make(chan struct{}), release: make(chan struct{})}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	episode := TaskEpisode{
		ID: "ep_preempt", Status: "active", StartedAt: "2026-08-14T08:00:00Z", EndedAt: "2026-08-14T08:00:01Z", UserGoal: "截图",
		Events: []TaskEpisodeEvent{
			{EventID: "ep_preempt_call", Type: runEventToolCall, ToolName: "screenshot"},
			{EventID: "ep_preempt_result", Type: "tool_result", ToolName: "screenshot", Observation: "screen captured"},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	worker := newEpisodeMemoryWorker(processor)
	worker.idleDelay = 10 * time.Millisecond
	if err := worker.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer worker.Stop()
	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("Episode Memory model call did not start")
	}
	worker.TaskStarted()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := processor.state.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		status := state.Episodes[episodeMemoryStateKey(episode.ID, episodeMemoryExtractorVersion)]
		if status.Status != episodeMemoryStatusProcessing {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls := inner.callCount(); calls != 0 {
		t.Fatalf("completed model calls while foreground task started = %d, want 0", calls)
	}
	worker.TaskFinished()
	deadline = time.Now().Add(time.Second)
	for inner.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls := inner.callCount(); calls != 1 {
		t.Fatalf("model calls after foreground task finished = %d, want resumed Episode once", calls)
	}
}

func TestEpisodeMemoryProcessorRequeuesProposalWhenMemoryRevisionChanged(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	if _, err := plane.device.Upsert(ctx, DeviceMemoryItem{
		ID: "devmem_revision", Type: "fact", Status: "active", Revision: 2, LessonKey: "existing",
		Title: "Settings location", Summary: "Open Display", Content: "Display is in Settings", DeviceID: "device_a",
		Tags: []string{episodeMemoryTag, "settings"}, Applicability: map[string]string{"device_id": "device_a", "app_name": "Settings"},
	}); err != nil {
		t.Fatalf("Upsert(existing) error = %v", err)
	}
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"achieved","reason":"The tool result confirms the current location.","evidence_refs":["ep_revision_result"]},
  "candidates":[{
    "lesson_key":"refresh_revision","type":"fact","action":"update","retention":"durable","memory_id":"devmem_revision","memory_revision":2,
    "unresolved_conflict":false,"situation":"In Settings on device A","guidance":"Open Display from the main list","expected_effect":"Display controls become visible",
    "scope":{"device_id":"device_a","app_name":"Settings"},"tags":["settings"],"evidence_refs":["ep_revision_result"]
  }]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := TaskEpisode{
		ID: "ep_revision", Status: "active", StartedAt: "2026-08-14T09:00:00Z", EndedAt: "2026-08-14T09:00:01Z", UserGoal: "打开设置显示",
		DeviceScope: map[string]string{"device_id": "device_a"}, RetrievedMemoryRefs: []string{"devmem_revision"}, Outcome: TaskEpisodeOutcome{Success: true},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_revision_call", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","point":{"x":500,"y":500}}`},
			{EventID: "ep_revision_result", Type: "tool_result", ToolName: "touch_gesture", Content: "Display controls visible"},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	stale := episodeMemoryProposal{
		EpisodeAssessment: episodeMemoryAssessment{GoalResult: "achieved", Reason: "stale proposal", EvidenceRefs: []string{"ep_revision_result"}},
		ExistingRevisions: map[string]int{"devmem_revision": 1},
		Candidates: []episodeMemoryCandidate{{
			LessonKey: "stale_revision", Type: "fact", Action: "update", Retention: episodeMemoryRetentionDurable, MemoryID: "devmem_revision", MemoryRevision: 1,
			Situation: "In Settings", Guidance: "Open Display", ExpectedEffect: "Display opens",
			Scope: map[string]string{"device_id": "device_a", "app_name": "Settings"}, EvidenceRefs: []string{"ep_revision_result"},
		}},
	}
	if err := processor.state.SetEpisode(episode.ID, episodeMemoryEpisodeStatus{
		Status: episodeMemoryStatusProposed, ExtractorVersion: episodeMemoryExtractorVersion, Proposal: &stale,
	}); err != nil {
		t.Fatalf("SetEpisode(stale proposed) error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch(stale) error = %v", err)
	}
	if got := model.callCount(); got != 0 {
		t.Fatalf("model calls for stale persisted proposal = %d, want 0 before requeue", got)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if status := state.Episodes[episodeMemoryStateKey(episode.ID, episodeMemoryExtractorVersion)]; status.Status != episodeMemoryStatusRetry || status.Proposal != nil {
		t.Fatalf("stale proposal state = %#v, want retry without old proposal", status)
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch(requeued) error = %v", err)
	}
	if got := model.callCount(); got != 2 {
		t.Fatalf("model calls after requeue = %d, want extraction and retention audit", got)
	}
	updated, found, err := plane.device.Get(ctx, "devmem_revision")
	if err != nil || !found || updated.Revision != 3 {
		t.Fatalf("updated memory found=%v error=%v item=%#v, want revision 3", found, err, updated)
	}
}

func TestMemoryPlaneNotifiesEpisodeMemoryWorkerForSuccessfulEpisode(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"achieved","reason":"The app-open result confirms the goal.","evidence_refs":["ep_notify_result"]},
  "candidates":[]
}`}}
	if err := plane.StartEpisodeMemory(model); err != nil {
		t.Fatalf("StartEpisodeMemory() error = %v", err)
	}
	defer plane.StopEpisodeMemory()
	plane.episodeMemoryMu.RLock()
	worker := plane.episodeMemory
	plane.episodeMemoryMu.RUnlock()
	worker.mu.Lock()
	worker.idleDelay = 10 * time.Millisecond
	worker.mu.Unlock()
	now := time.Now().UTC()
	if err := plane.CommitEpisode(ctx, TaskEpisode{
		ID: "ep_notify", Status: "active", StartedAt: now.Format(time.RFC3339Nano), EndedAt: now.Add(time.Millisecond).Format(time.RFC3339Nano),
		UserGoal: "打开设置", Outcome: TaskEpisodeOutcome{Success: true},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_notify_call", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{"app":"Settings"}`},
			{EventID: "ep_notify_result", Type: "tool_result", ToolName: "launch_app", Content: "Settings opened"},
		},
	}); err != nil {
		t.Fatalf("CommitEpisode() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for model.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := model.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want successful Episode notification to trigger extraction", got)
	}
}

func TestEpisodeMemoryAssessmentRejectsIndirectEvidence(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"achieved","reason":"The action was attempted.","evidence_refs":["ep_indirect_call"]},
  "candidates":[]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{
		ID: "ep_indirect", Status: "active", StartedAt: "2026-08-14T10:00:00Z", EndedAt: "2026-08-14T10:00:01Z", UserGoal: "打开设置",
		Events: []TaskEpisodeEvent{
			{EventID: "ep_indirect_call", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{"app":"Settings"}`},
			{EventID: "ep_indirect_result", Type: "tool_result", ToolName: "launch_app", Content: "request accepted"},
		},
	}
	if _, err := processor.proposeEpisode(ctx, episode); err == nil || !strings.Contains(err.Error(), "requires direct evidence") {
		t.Fatalf("proposeEpisode() error = %v, want direct-evidence rejection", err)
	}
}

func TestEpisodeMemoryAssessmentDowngradesUnsupportedFailureToUnknown(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"not_achieved","reason":"The final page was not verified.","evidence_refs":["open_result"]},
  "candidates":[]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{
		ID: "ep_missing_verification", Status: "completed", UserGoal: "Open and verify the requested page",
		Outcome: TaskEpisodeOutcome{Success: true, FinalAnswer: "Done."},
		Events: []TaskEpisodeEvent{
			{EventID: "open_call", Type: runEventToolCall, ToolName: "open_app"},
			{EventID: "open_result", Type: "tool_result", ToolName: "open_app", Observation: "The app opened, but the requested page was not verified."},
		},
	}

	proposal, err := processor.proposeEpisode(ctx, episode)
	if err != nil {
		t.Fatalf("proposeEpisode() error = %v", err)
	}
	if proposal.EpisodeAssessment.GoalResult != episodeGoalUnknown {
		t.Fatalf("goal_result = %q, want unknown without structured failure evidence", proposal.EpisodeAssessment.GoalResult)
	}
	if len(proposal.Candidates) != 0 {
		t.Fatalf("candidates = %#v, want none after unsupported failure downgrade", proposal.Candidates)
	}
}

func TestAbandonedEpisodeWithoutActionableFailureDoesNotCreateFailureMemory(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"not_achieved","reason":"The task was abandoned before completion.","evidence_refs":["open_result"]},
  "candidates":[{"lesson_key":"abandoned-open","type":"failure","action":"create","retention":"durable","situation":"Opening Settings was not enough.","guidance":"Do something else.","expected_effect":"The task should complete.","scope":{},"tags":[],"evidence_refs":["open_result"]}]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{
		ID: "ep_abandoned", Status: "abandoned", UserGoal: "Open and verify the requested page",
		Events: []TaskEpisodeEvent{
			{EventID: "open_call", Type: runEventToolCall, ToolName: "open_app"},
			{EventID: "open_result", Type: "tool_result", ToolName: "open_app", Observation: "Settings opened"},
		},
	}

	proposal, err := processor.proposeEpisode(ctx, episode)
	if err != nil {
		t.Fatalf("proposeEpisode() error = %v", err)
	}
	if proposal.EpisodeAssessment.GoalResult != episodeGoalUnknown {
		t.Fatalf("goal_result = %q, want unknown for abandoned episode without failure evidence", proposal.EpisodeAssessment.GoalResult)
	}
	if len(proposal.Candidates) != 0 {
		t.Fatalf("candidates = %#v, want none for abandoned episode without failure evidence", proposal.Candidates)
	}
}

func TestHasDirectEpisodeFailureEvidenceUsesStructuredSignals(t *testing.T) {
	tests := []struct {
		name    string
		episode TaskEpisode
		refs    []string
		want    bool
	}{
		{name: "missing verification only", episode: TaskEpisode{Status: "completed", Events: []TaskEpisodeEvent{{EventID: "result", Type: "tool_result"}}}, refs: []string{"result"}, want: false},
		{name: "structured error", episode: TaskEpisode{Status: "completed", Events: []TaskEpisodeEvent{{EventID: "result", Type: "tool_result", IsError: true}}}, refs: []string{"result"}, want: true},
		{name: "user correction", episode: TaskEpisode{Status: "completed", Events: []TaskEpisodeEvent{{EventID: "steer", Type: "steer"}}}, refs: []string{"steer"}, want: true},
		{name: "abandoned without failure signal", episode: TaskEpisode{Status: "abandoned"}, want: false},
		{name: "explicit interruption", episode: TaskEpisode{Status: "interrupted"}, want: true},
		{name: "failure reason", episode: TaskEpisode{Status: "completed", Outcome: TaskEpisodeOutcome{FailureReason: "runtime stopped"}}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasDirectEpisodeFailureEvidence(test.episode, test.refs); got != test.want {
				t.Fatalf("hasDirectEpisodeFailureEvidence() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFailureCandidateMustReferenceNotAchievedEvidence(t *testing.T) {
	episode := TaskEpisode{Events: []TaskEpisodeEvent{
		{EventID: "call", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{"app":"Settings"}`},
		{EventID: "result", Type: "tool_result", ToolName: "launch_app", Content: "The requested page did not open"},
	}}
	assessment := episodeMemoryAssessment{
		GoalResult:   episodeGoalNotAchieved,
		Reason:       "The result shows the requested page did not open.",
		EvidenceRefs: []string{"result"},
	}
	candidate := episodeMemoryCandidate{
		LessonKey:      "check_page_opened",
		Type:           episodeMemoryTypeFailure,
		Action:         episodeMemoryActionCreate,
		Retention:      episodeMemoryRetentionDurable,
		Situation:      "After launching Settings",
		Guidance:       "Check that the requested page is visible before continuing",
		ExpectedEffect: "The agent stops when the requested page did not open",
		Scope:          map[string]string{"app_name": "Settings"},
		EvidenceRefs:   []string{"call"},
	}

	if _, ok := validateEpisodeMemoryCandidate(episode, assessment, candidate, map[string]bool{}); ok {
		t.Fatal("failure Candidate citing only an action was accepted")
	}
	candidate.EvidenceRefs = []string{"call", "result"}
	if _, ok := validateEpisodeMemoryCandidate(episode, assessment, candidate, map[string]bool{}); !ok {
		t.Fatal("failure Candidate citing the not-achieved result was rejected")
	}
}

func TestEpisodeMemoryCandidateRequiresDurableRetention(t *testing.T) {
	episode := TaskEpisode{Events: []TaskEpisodeEvent{
		{EventID: "call", Type: runEventToolCall, ToolName: "read_screen", ToolInput: `{}`},
		{EventID: "result", Type: "tool_result", ToolName: "read_screen", Observation: "The requested value is visible."},
	}}
	assessment := episodeMemoryAssessment{
		GoalResult:   episodeGoalAchieved,
		Reason:       "The result directly shows the requested value.",
		EvidenceRefs: []string{"result"},
	}
	base := episodeMemoryCandidate{
		LessonKey:      "observed_value",
		Type:           episodeMemoryTypeFact,
		Action:         episodeMemoryActionCreate,
		Situation:      "When inspecting this device configuration",
		Guidance:       "Use the observed IP address as the configured endpoint",
		ExpectedEffect: "The device endpoint can be selected correctly",
		Scope:          map[string]string{"device_id": "device_a"},
		EvidenceRefs:   []string{"result"},
	}

	tests := []struct {
		name      string
		retention episodeMemoryRetention
		want      bool
	}{
		{name: "durable", retention: episodeMemoryRetentionDurable, want: true},
		{name: "transient", retention: episodeMemoryRetentionTransient, want: false},
		{name: "sensitive", retention: episodeMemoryRetentionSensitive, want: false},
		{name: "missing", retention: "", want: false},
		{name: "unknown", retention: "session_only", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Retention = test.retention
			_, got := validateEpisodeMemoryCandidate(episode, assessment, candidate, map[string]bool{})
			if got != test.want {
				t.Fatalf("candidate accepted = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEpisodeMemoryCandidatePreservesEpisodeScopeBoundaries(t *testing.T) {
	episode := TaskEpisode{
		DeviceScope: map[string]string{"device_id": "device_a", "app_name": "Example", "app_version": "7"},
		Events:      []TaskEpisodeEvent{{EventID: "result", Type: "tool_result", ToolName: "read_screen", Observation: "The page is visible."}},
	}
	base := episodeMemoryCandidate{
		LessonKey: "versioned_fact", Type: episodeMemoryTypeFact, Action: episodeMemoryActionCreate,
		Retention: episodeMemoryRetentionDurable, Situation: "On the page", Guidance: "Use the visible label",
		ExpectedEffect: "The page can be identified", Scope: map[string]string{"app_name": "Example"}, EvidenceRefs: []string{"result"},
	}

	validated, ok := validateEpisodeMemoryCandidate(
		episode,
		episodeMemoryAssessment{GoalResult: episodeGoalAchieved},
		base,
		map[string]bool{},
	)
	if !ok || validated.Scope["app_version"] != "7" || validated.Scope["device_id"] != "device_a" {
		t.Fatalf("validated candidate = %#v ok=%v, want Episode scope boundaries preserved", validated, ok)
	}

	base.Scope["app_version"] = "8"
	if _, ok := validateEpisodeMemoryCandidate(
		episode,
		episodeMemoryAssessment{GoalResult: episodeGoalAchieved},
		base,
		map[string]bool{},
	); ok {
		t.Fatal("candidate with scope conflicting with the Episode was accepted")
	}
}

func TestEpisodeMemoryRetentionAuditRejectsSensitiveProcedure(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	firstPass := `{
  "episode_assessment":{"goal_result":"achieved","reason":"The destination page confirms completion.","evidence_refs":["verify_result"]},
  "candidates":[{
    "lesson_key":"authentication_flow","type":"procedure","action":"create","retention":"durable","unresolved_conflict":false,
    "situation":"During authentication","guidance":"Enter verification value 913204, then verify the destination page","expected_effect":"Authentication succeeds",
    "scope":{"device_id":"device_a","app_name":"Auth"},"tags":["authentication"],"evidence_refs":["value_call","value_result","verify_call","verify_result"]
  }]
}`
	audited := `{
  "reviews":[{
    "lesson_key":"authentication_flow","decision":"discard",
    "reason":"The proposed guidance embeds run-bound authentication material that must not persist."
  }]
}`
	model := &episodeMemoryScriptedModel{
		responses:      []string{firstPass},
		auditResponses: []string{audited},
	}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{
		ID: "ep_sensitive_fact", DeviceScope: map[string]string{"device_id": "device_a"},
		Events: []TaskEpisodeEvent{
			{EventID: "value_call", Type: runEventToolCall, ToolName: "read_screen", ToolInput: `{}`},
			{EventID: "value_result", Type: "tool_result", ToolName: "read_screen", Observation: "Verification value 913204 was accepted."},
			{EventID: "verify_call", Type: runEventToolCall, ToolName: "read_screen", ToolInput: `{}`},
			{EventID: "verify_result", Type: "tool_result", ToolName: "read_screen", Observation: "The destination page is visible."},
		},
	}

	proposal, err := processor.proposeEpisode(ctx, episode)
	if err != nil {
		t.Fatalf("proposeEpisode() error = %v", err)
	}
	if got := model.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want extraction and retention audit", got)
	}
	if err := processor.applyProposal(ctx, episode, proposal); err != nil {
		t.Fatalf("applyProposal() error = %v", err)
	}
	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("device memories = %#v, want sensitive procedure discarded", items)
	}
}

func TestEpisodeMemoryRetentionAuditGeneralizesRunBoundValue(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	firstPass := `{
  "episode_assessment":{"goal_result":"achieved","reason":"The destination page confirms completion.","evidence_refs":["verify_result"]},
  "candidates":[{
    "lesson_key":"complete_challenge","type":"procedure","action":"create","retention":"durable","unresolved_conflict":false,
    "situation":"When this service presents an interactive challenge","guidance":"Enter the observed response river-glass-amber, then verify the destination page","expected_effect":"The challenge completes",
    "scope":{"device_id":"device_a","app_name":"Service"},"tags":["challenge"],"evidence_refs":["value_call","value_result","verify_call","verify_result"]
  }]
}`
	audited := `{
  "reviews":[{
    "lesson_key":"complete_challenge","decision":"retain",
    "reason":"The verified sequence is reusable after removing the response that was valid only for this run.",
	    "rewrite":{
	      "situation":"When this service presents an interactive challenge","guidance":"Use the response provided for the current challenge, then verify the destination page before reporting completion","expected_effect":"The challenge completes without reusing an earlier response",
	      "scope":{"device_id":"device_a","app_name":"Service"},"tags":["challenge","verify-completion"],"evidence_refs":["value_call","value_result","verify_call","verify_result"]
	    }
  }]
}`
	model := &episodeMemoryScriptedModel{
		responses:      []string{firstPass},
		auditResponses: []string{audited},
	}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{
		ID: "ep_generalized_value", DeviceScope: map[string]string{"device_id": "device_a"},
		Events: []TaskEpisodeEvent{
			{EventID: "value_call", Type: runEventToolCall, ToolName: "read_screen", ToolInput: `{}`},
			{EventID: "value_result", Type: "tool_result", ToolName: "read_screen", Observation: "The current response river-glass-amber was accepted."},
			{EventID: "verify_call", Type: runEventToolCall, ToolName: "read_screen", ToolInput: `{}`},
			{EventID: "verify_result", Type: "tool_result", ToolName: "read_screen", Observation: "The destination page is visible."},
		},
	}

	proposal, err := processor.proposeEpisode(ctx, episode)
	if err != nil {
		t.Fatalf("proposeEpisode() error = %v", err)
	}
	if len(proposal.Candidates) != 1 {
		t.Fatalf("audited candidates = %#v, want one generalized candidate", proposal.Candidates)
	}
	if strings.Contains(proposal.Candidates[0].Guidance, "river-glass-amber") {
		t.Fatalf("audited guidance retained run-bound value: %q", proposal.Candidates[0].Guidance)
	}
	if err := processor.applyProposal(ctx, episode, proposal); err != nil {
		t.Fatalf("applyProposal() error = %v", err)
	}
	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 1 || strings.Contains(items[0].Content, "river-glass-amber") {
		t.Fatalf("device memories = %#v, want one generalized memory without the run-bound value", items)
	}
}

func TestEpisodeMemoryRetentionAuditFailureIsReported(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{
		responses: []string{`{
  "episode_assessment":{"goal_result":"achieved","reason":"The observed page confirms the inspection completed.","evidence_refs":["result"]},
  "candidates":[{
    "lesson_key":"observed_page_fact","type":"fact","action":"create","retention":"durable","unresolved_conflict":false,
    "situation":"On the inspected page","guidance":"Use the observed page label","expected_effect":"The page can be identified",
    "scope":{"device_id":"device_a","app_name":"Example"},"tags":["page"],"evidence_refs":["result"]
  }]
}`},
		auditResponses: []string{`not-json`},
	}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{
		ID: "ep_audit_failure", DeviceScope: map[string]string{"device_id": "device_a"},
		Events: []TaskEpisodeEvent{
			{EventID: "call", Type: runEventToolCall, ToolName: "read_screen", ToolInput: `{}`},
			{EventID: "result", Type: "tool_result", ToolName: "read_screen", Observation: "The inspected page is visible."},
		},
	}

	_, err := processor.proposeEpisode(ctx, episode)
	if err == nil || !strings.Contains(err.Error(), "parse episode memory retention audit") {
		t.Fatalf("proposeEpisode() error = %v, want retention audit parse failure", err)
	}
	if got := model.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want extraction and failed retention audit", got)
	}
}

func TestRetainedEpisodeMemoryCandidatesFailsClosedOnAmbiguousAudit(t *testing.T) {
	base := episodeMemoryCandidate{
		LessonKey: "stable_route", Type: episodeMemoryTypeNavigation, Action: episodeMemoryActionCreate,
		Retention: episodeMemoryRetentionDurable, Situation: "On the target page", Guidance: "Open the details entry",
		ExpectedEffect: "The details page is visible", Scope: map[string]string{"device_id": "device_a"}, EvidenceRefs: []string{"result"},
	}
	validReview := episodeMemoryRetentionReview{
		LessonKey: base.LessonKey, Decision: episodeMemoryRetentionDecisionRetain, Reason: "durable and scoped",
		Rewrite: &episodeMemoryRetentionRewrite{
			Situation: base.Situation, Guidance: base.Guidance, ExpectedEffect: base.ExpectedEffect,
			Scope: base.Scope, Tags: base.Tags, EvidenceRefs: base.EvidenceRefs,
		},
	}

	tests := []struct {
		name  string
		audit episodeMemoryRetentionAudit
	}{
		{name: "missing review", audit: episodeMemoryRetentionAudit{}},
		{name: "duplicate review", audit: episodeMemoryRetentionAudit{Reviews: []episodeMemoryRetentionReview{validReview, validReview}}},
		{name: "missing rewrite", audit: episodeMemoryRetentionAudit{Reviews: []episodeMemoryRetentionReview{{
			LessonKey: base.LessonKey, Decision: episodeMemoryRetentionDecisionRetain, Reason: "rewrite omitted",
		}}}},
		{name: "missing reason", audit: episodeMemoryRetentionAudit{Reviews: []episodeMemoryRetentionReview{{
			LessonKey: base.LessonKey, Decision: episodeMemoryRetentionDecisionRetain, Rewrite: validReview.Rewrite,
		}}}},
		{name: "discard decision", audit: episodeMemoryRetentionAudit{Reviews: []episodeMemoryRetentionReview{{
			LessonKey: base.LessonKey, Decision: episodeMemoryRetentionDecisionDiscard, Reason: "not safe to retain",
		}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := retainedEpisodeMemoryCandidates([]episodeMemoryCandidate{base}, test.audit); len(got) != 0 {
				t.Fatalf("retained candidates = %#v, want ambiguous audit to discard candidate", got)
			}
		})
	}
}

func TestRetainedEpisodeMemoryCandidatesPreservesExtractedScopeBoundary(t *testing.T) {
	base := episodeMemoryCandidate{
		LessonKey: "versioned_route", Type: episodeMemoryTypeProcedure, Action: episodeMemoryActionCreate,
		Retention: episodeMemoryRetentionDurable, Situation: "In build 7", Guidance: "Run the verified flow",
		ExpectedEffect: "The value persists", Scope: map[string]string{"app_name": "Example", "app_version": "7"},
		Tags: []string{"save"}, EvidenceRefs: []string{"result"},
	}
	audit := episodeMemoryRetentionAudit{Reviews: []episodeMemoryRetentionReview{{
		LessonKey: base.LessonKey, Decision: episodeMemoryRetentionDecisionRetain, Reason: "the procedure remains reusable",
		Rewrite: &episodeMemoryRetentionRewrite{
			Situation: "In the app", Guidance: base.Guidance, ExpectedEffect: base.ExpectedEffect,
			Scope: map[string]string{"app_name": "Example"}, Tags: base.Tags, EvidenceRefs: base.EvidenceRefs,
		},
	}}}

	got := retainedEpisodeMemoryCandidates([]episodeMemoryCandidate{base}, audit)
	if len(got) != 1 || got[0].Scope["app_version"] != "7" {
		t.Fatalf("retained candidates = %#v, want extracted version boundary preserved", got)
	}
}

func TestRetainedEpisodeMemoryCandidatesRejectsChangedEvidenceRefs(t *testing.T) {
	base := episodeMemoryCandidate{
		LessonKey: "verified_route", Type: episodeMemoryTypeProcedure, Action: episodeMemoryActionCreate,
		Retention: episodeMemoryRetentionDurable, Situation: "In the app", Guidance: "Run the verified flow",
		ExpectedEffect: "The value persists", Scope: map[string]string{"app_name": "Example"},
		EvidenceRefs: []string{"call", "result"},
	}
	audit := episodeMemoryRetentionAudit{Reviews: []episodeMemoryRetentionReview{{
		LessonKey: base.LessonKey, Decision: episodeMemoryRetentionDecisionRetain, Reason: "reusable",
		Rewrite: &episodeMemoryRetentionRewrite{
			Situation: base.Situation, Guidance: base.Guidance, ExpectedEffect: base.ExpectedEffect,
			Scope: base.Scope, EvidenceRefs: []string{"result", "invented"},
		},
	}}}

	if got := retainedEpisodeMemoryCandidates([]episodeMemoryCandidate{base}, audit); len(got) != 0 {
		t.Fatalf("retained candidates = %#v, want audit with changed evidence refs discarded", got)
	}
}

func TestCompactEpisodeMemoryCandidatesMergesDuplicateEvidence(t *testing.T) {
	first := episodeMemoryCandidate{
		LessonKey: "same_lesson", Type: episodeMemoryTypeProcedure, Action: episodeMemoryActionCreate,
		Retention: episodeMemoryRetentionDurable, Situation: "In the scoped app", Guidance: "Run the verified flow",
		ExpectedEffect: "The value persists", Scope: map[string]string{"app_version": "7"},
		Tags: []string{"save"}, EvidenceRefs: []string{"first_result"},
	}
	second := first
	second.Tags = []string{"verify"}
	second.EvidenceRefs = []string{"second_result"}

	got := compactEpisodeMemoryCandidates([]episodeMemoryCandidate{first, second})
	if len(got) != 1 {
		t.Fatalf("compacted candidates = %#v, want one candidate", got)
	}
	if len(got[0].Tags) != 2 || len(got[0].EvidenceRefs) != 2 {
		t.Fatalf("compacted candidate = %#v, want merged tags and evidence", got[0])
	}
}

func TestCompactEpisodeMemoryCandidatesRejectsConflictingIdentity(t *testing.T) {
	first := episodeMemoryCandidate{
		LessonKey: "same_lesson", Type: episodeMemoryTypeProcedure, Action: episodeMemoryActionCreate,
		Retention: episodeMemoryRetentionDurable, Situation: "In the scoped app", Guidance: "Run the verified flow",
		ExpectedEffect: "The value persists", Scope: map[string]string{"app_version": "7"}, EvidenceRefs: []string{"first_result"},
	}
	second := first
	second.Type = episodeMemoryTypeFailure
	second.EvidenceRefs = []string{"second_result"}

	if got := compactEpisodeMemoryCandidates([]episodeMemoryCandidate{first, second}); len(got) != 0 {
		t.Fatalf("compacted candidates = %#v, want ambiguous lesson identity discarded", got)
	}
}

func TestEpisodeMemoryReviewsEmptyMultiStepProposalOnce(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{
		`{
  "episode_assessment":{"goal_result":"achieved","reason":"The final result confirms the requested page opened.","evidence_refs":["result_2"]},
  "candidates":[]
}`,
		`{
  "episode_assessment":{"goal_result":"achieved","reason":"The final result confirms the requested page opened.","evidence_refs":["result_2"]},
  "candidates":[{
    "lesson_key":"open_target_page","type":"procedure","action":"create","retention":"durable","unresolved_conflict":false,
    "situation":"When opening the target page on this device","guidance":"Open Settings, then select the target page","expected_effect":"The target page is visible",
    "scope":{"device_id":"device_a","app_name":"Settings"},"tags":["settings"],
    "evidence_refs":["call_1","result_1","call_2","result_2"]
  }]
}`,
	}}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{
		ID: "ep_review", DeviceScope: map[string]string{"device_id": "device_a"},
		Events: []TaskEpisodeEvent{
			{EventID: "call_1", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{"app":"Settings"}`},
			{EventID: "result_1", Type: "tool_result", ToolName: "launch_app", Observation: "Settings is visible."},
			{EventID: "call_2", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","target":"Target"}`},
			{EventID: "result_2", Type: "tool_result", ToolName: "touch_gesture", Observation: "The target page is visible."},
		},
	}

	proposal, err := processor.proposeEpisode(ctx, episode)
	if err != nil {
		t.Fatalf("proposeEpisode() error = %v", err)
	}
	if got := model.callCount(); got != 3 {
		t.Fatalf("model calls = %d, want extraction, omission review, and retention audit", got)
	}
	if len(proposal.Candidates) != 1 || proposal.Candidates[0].Retention != episodeMemoryRetentionDurable {
		t.Fatalf("reviewed proposal = %#v, want one durable candidate", proposal)
	}
	if err := processor.applyProposal(ctx, episode, proposal); err != nil {
		t.Fatalf("applyProposal() error = %v", err)
	}
	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("device memories = %#v, want one reviewed memory", items)
	}
}

func TestEpisodeMemoryReviewCanRemainEmpty(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	empty := `{
  "episode_assessment":{"goal_result":"achieved","reason":"The final result confirms the requested page opened.","evidence_refs":["result_2"]},
  "candidates":[]
}`
	model := &episodeMemoryScriptedModel{responses: []string{empty, empty}}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{Events: []TaskEpisodeEvent{
		{EventID: "call_1", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{}`},
		{EventID: "result_1", Type: "tool_result", ToolName: "launch_app", Observation: "Settings is visible."},
		{EventID: "call_2", Type: runEventToolCall, ToolName: "read_screen", ToolInput: `{}`},
		{EventID: "result_2", Type: "tool_result", ToolName: "read_screen", Observation: "The requested page is visible."},
	}}

	proposal, err := processor.proposeEpisode(ctx, episode)
	if err != nil {
		t.Fatalf("proposeEpisode() error = %v", err)
	}
	if got := model.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want one extraction and one review", got)
	}
	if len(proposal.Candidates) != 0 {
		t.Fatalf("reviewed candidates = %#v, want empty", proposal.Candidates)
	}
}

func TestEpisodeMemoryOmissionReviewFailureIsRetried(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{
		`{
  "episode_assessment":{"goal_result":"achieved","reason":"The final result confirms the page opened.","evidence_refs":["result_2"]},
  "candidates":[]
}`,
		`not-json`,
	}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := TaskEpisode{
		ID: "ep_review_retry", Status: "active", StartedAt: "2026-08-14T10:25:00Z", EndedAt: "2026-08-14T10:25:01Z",
		UserGoal: "Open the target page", DeviceScope: map[string]string{"device_id": "device_a"},
		Events: []TaskEpisodeEvent{
			{EventID: "call_1", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{}`},
			{EventID: "result_1", Type: "tool_result", ToolName: "launch_app", Observation: "The app is visible."},
			{EventID: "call_2", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{}`},
			{EventID: "result_2", Type: "tool_result", ToolName: "touch_gesture", Observation: "The target page is visible."},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	status := state.Episodes[episodeMemoryStateKey(episode.ID, episodeMemoryExtractorVersion)]
	if status.Status != episodeMemoryStatusRetry || status.AttemptCount != 1 || !strings.Contains(status.LastError, "omission review") {
		t.Fatalf("state = %#v, want retry after omission-review failure", status)
	}
}

func TestEpisodeMemoryDoesNotReviewUnknownAssessment(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"unknown","reason":"The final state was not observed.","evidence_refs":[]},
  "candidates":[]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{Events: []TaskEpisodeEvent{
		{EventID: "call_1", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{}`},
		{EventID: "result_1", Type: "tool_result", ToolName: "launch_app", Observation: "Settings is visible."},
		{EventID: "call_2", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{}`},
		{EventID: "result_2", Type: "tool_result", ToolName: "touch_gesture", Observation: "The action completed without final proof."},
	}}

	if _, err := processor.proposeEpisode(ctx, episode); err != nil {
		t.Fatalf("proposeEpisode() error = %v", err)
	}
	if got := model.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want no review for unknown assessment", got)
	}
}

func TestEpisodeMemoryDiscardsTransientReviewCandidate(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{
		`{
  "episode_assessment":{"goal_result":"achieved","reason":"The final result confirms the inspection completed.","evidence_refs":["result_2"]},
  "candidates":[]
}`,
		`{
  "episode_assessment":{"goal_result":"achieved","reason":"The final result confirms the inspection completed.","evidence_refs":["result_2"]},
  "candidates":[{
    "lesson_key":"current_observation","type":"fact","action":"create","retention":"transient","unresolved_conflict":false,
    "situation":"During the current inspection","guidance":"Reuse the value currently shown on screen","expected_effect":"The current value is available",
    "scope":{"device_id":"device_a"},"tags":["inspection"],"evidence_refs":["result_2"]
  }]
}`,
	}}
	processor := newEpisodeMemoryProcessor(plane, model)
	episode := TaskEpisode{
		ID: "ep_transient_review", DeviceScope: map[string]string{"device_id": "device_a"},
		Events: []TaskEpisodeEvent{
			{EventID: "call_1", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{}`},
			{EventID: "result_1", Type: "tool_result", ToolName: "launch_app", Observation: "The details page is visible."},
			{EventID: "call_2", Type: runEventToolCall, ToolName: "read_screen", ToolInput: `{}`},
			{EventID: "result_2", Type: "tool_result", ToolName: "read_screen", Observation: "The requested current value is visible."},
		},
	}

	proposal, err := processor.proposeEpisode(ctx, episode)
	if err != nil {
		t.Fatalf("proposeEpisode() error = %v", err)
	}
	if err := processor.applyProposal(ctx, episode, proposal); err != nil {
		t.Fatalf("applyProposal() error = %v", err)
	}
	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("device memories = %#v, want transient review candidate discarded", items)
	}
}

func TestNavigationCandidateLinksResultEvidenceToToolCalls(t *testing.T) {
	episode := TaskEpisode{Events: []TaskEpisodeEvent{
		{EventID: "ethernet_call", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","target":"Ethernet"}`},
		{EventID: "ethernet_result", Type: "tool_result", ToolName: "touch_gesture", Observation: "The Ethernet page is visible."},
		{EventID: "interface_call", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","target":"Aiden HID+ECM"}`},
		{EventID: "interface_result", Type: "tool_result", ToolName: "touch_gesture", Observation: "The IPv4 details page is visible."},
	}}
	candidate := episodeMemoryCandidate{
		LessonKey:      "ios_aiden_ethernet_path",
		Type:           episodeMemoryTypeNavigation,
		Action:         episodeMemoryActionCreate,
		Retention:      episodeMemoryRetentionDurable,
		MemoryRevision: 1,
		Situation:      "When opening the Aiden USB Ethernet details on iOS",
		Guidance:       "Open Ethernet, then Aiden HID+ECM",
		ExpectedEffect: "The IPv4 details page is visible",
		Scope:          map[string]string{"app_name": "Settings"},
		EvidenceRefs:   []string{"ethernet_result", "interface_result"},
	}

	validated, ok := validateEpisodeMemoryCandidate(episode, episodeMemoryAssessment{GoalResult: episodeGoalAchieved}, candidate, map[string]bool{})
	if !ok {
		t.Fatal("navigation Candidate citing paired tool results was rejected")
	}
	if validated.MemoryRevision != 0 {
		t.Fatalf("create memory_revision = %d, want 0", validated.MemoryRevision)
	}
	for _, want := range []string{"ethernet_result", "interface_result", "ethernet_call", "interface_call"} {
		found := false
		for _, ref := range validated.EvidenceRefs {
			if ref == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("evidence refs = %#v, missing %q", validated.EvidenceRefs, want)
		}
	}
}

func TestEpisodeMemoryProcessorProcessesInterruptedEpisodeWithDeviceEvidence(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"unknown","reason":"The task was interrupted after a device action, before final proof.","evidence_refs":[]},
  "candidates":[]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for _, episode := range []TaskEpisode{
		{
			ID: "ep_interrupted_after_action", Status: "interrupted", StartedAt: "2026-08-14T10:10:00Z", EndedAt: "2026-08-14T10:10:01Z", UserGoal: "打开设置",
			Events: []TaskEpisodeEvent{
				{EventID: "ep_interrupted_call", Type: runEventToolCall, ToolName: "launch_app", ToolInput: `{"app":"Settings"}`},
				{EventID: "ep_interrupted_result", Type: "tool_result", ToolName: "launch_app", Content: "Settings opened"},
			},
		},
		{
			ID: "ep_interrupted_before_action", Status: "interrupted", StartedAt: "2026-08-14T10:10:02Z", EndedAt: "2026-08-14T10:10:03Z", UserGoal: "打开设置",
			Events: []TaskEpisodeEvent{{EventID: "ep_interrupted_cancel", Type: "tool_result", IsError: true, ToolError: NewToolError(CodeCanceled, "canceled")}},
		},
	} {
		if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
			t.Fatalf("AddEpisode(%s) error = %v", episode.ID, err)
		}
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if got := model.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want only interrupted Episode with effective device evidence", got)
	}
}

func TestEpisodeMemoryExtractionFailureIsNotRetried(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{responses: []string{
		`not-json`,
		`{"episode_assessment":{"goal_result":"unknown","reason":"unused","evidence_refs":[]},"candidates":[]}`,
	}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := TaskEpisode{
		ID: "ep_bad_proposal", Status: "active", StartedAt: "2026-08-14T10:20:00Z", EndedAt: "2026-08-14T10:20:01Z", UserGoal: "打开设置",
		Events: []TaskEpisodeEvent{
			{EventID: "ep_bad_call", Type: runEventToolCall, ToolName: "launch_app"},
			{EventID: "ep_bad_result", Type: "tool_result", ToolName: "launch_app", Content: "Settings opened"},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	for pass := 0; pass < 2; pass++ {
		if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
			t.Fatalf("ProcessBatch(%d) error = %v", pass, err)
		}
	}
	if got := model.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want one extraction attempt", got)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	status := state.Episodes[episodeMemoryStateKey(episode.ID, episodeMemoryExtractorVersion)]
	if status.Status != episodeMemoryStatusIgnored || status.AttemptCount != 1 {
		t.Fatalf("state = %#v, want one terminal ignored attempt", status)
	}
}

func TestEpisodeMemoryRetentionAuditFailureIsRetried(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &episodeMemoryScriptedModel{
		responses: []string{`{
  "episode_assessment":{"goal_result":"achieved","reason":"The final page was observed.","evidence_refs":["result"]},
  "candidates":[{
    "lesson_key":"observed_page_fact","type":"fact","action":"create","retention":"durable","unresolved_conflict":false,
    "situation":"On the inspected page","guidance":"Use the observed page label","expected_effect":"The page can be identified",
    "scope":{"device_id":"device_a","app_name":"Example"},"tags":["page"],"evidence_refs":["result"]
  }]
}`},
		auditResponses: []string{`not-json`},
	}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := TaskEpisode{
		ID: "ep_audit_retry", Status: "active", StartedAt: "2026-08-14T10:30:00Z", EndedAt: "2026-08-14T10:30:01Z",
		UserGoal: "Inspect the example page", DeviceScope: map[string]string{"device_id": "device_a"},
		Events: []TaskEpisodeEvent{
			{EventID: "call", Type: runEventToolCall, ToolName: "read_screen", ToolInput: `{}`},
			{EventID: "result", Type: "tool_result", ToolName: "read_screen", Observation: "The final page is visible."},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	status := state.Episodes[episodeMemoryStateKey(episode.ID, episodeMemoryExtractorVersion)]
	if status.Status != episodeMemoryStatusRetry || status.AttemptCount != 1 || !strings.Contains(status.LastError, "retention audit") {
		t.Fatalf("state = %#v, want retry after retention audit failure", status)
	}
}

func TestEpisodeMemoryProcedureUpdatePreservesExistingSteps(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	oldSteps := []ProcedureStep{{Tool: "launch_app", Text: "Settings", OutcomeNote: "Settings opened"}}
	if _, err := plane.device.Upsert(ctx, DeviceMemoryItem{
		ID: "devmem_procedure", Type: "procedure", Status: deviceMemoryStatusActive, Revision: 1,
		Title: "Open display settings", Summary: "Open Settings", Content: "old merged procedure", DeviceID: "device_a",
		Tags: []string{episodeMemoryTag}, Applicability: map[string]string{"device_id": "device_a", "app_name": "Settings"}, Steps: oldSteps,
	}); err != nil {
		t.Fatalf("Upsert(existing) error = %v", err)
	}
	model := &episodeMemoryScriptedModel{responses: []string{`{
  "episode_assessment":{"goal_result":"achieved","reason":"The final tool result shows the Display controls.","evidence_refs":["ep_proc_result_2"]},
  "candidates":[{
    "lesson_key":"merge_display_path","type":"procedure","action":"update","retention":"durable","memory_id":"devmem_procedure","memory_revision":1,
    "unresolved_conflict":false,"situation":"When opening display settings","guidance":"Open Settings and select Display","expected_effect":"Display controls are visible",
    "scope":{"device_id":"device_a","app_name":"Settings"},"tags":["display"],
    "evidence_refs":["ep_proc_call_1","ep_proc_result_1","ep_proc_call_2","ep_proc_result_2"]
  }]
}`}}
	processor := newEpisodeMemoryProcessor(plane, model)
	processor.state.bootstrapAt = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := TaskEpisode{
		ID: "ep_proc_update", Status: "active", StartedAt: "2026-08-14T10:30:00Z", EndedAt: "2026-08-14T10:30:05Z", UserGoal: "打开显示设置",
		DeviceScope: map[string]string{"device_id": "device_a"}, RetrievedMemoryRefs: []string{"devmem_procedure"},
		Events: []TaskEpisodeEvent{
			{EventID: "ep_proc_call_1", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","point":{"x":100,"y":200}}`},
			{EventID: "ep_proc_result_1", Type: "tool_result", ToolName: "touch_gesture", Content: "Settings list visible"},
			{EventID: "ep_proc_call_2", Type: runEventToolCall, ToolName: "touch_gesture", ToolInput: `{"type":"tap","point":{"x":100,"y":400}}`},
			{EventID: "ep_proc_result_2", Type: "tool_result", ToolName: "touch_gesture", Content: "Display controls visible"},
		},
	}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, episodeMemoryBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	updated, found, err := plane.device.Get(ctx, "devmem_procedure")
	if err != nil || !found {
		t.Fatalf("Get(updated) found=%v error=%v", found, err)
	}
	if len(updated.Steps) != 3 || updated.Steps[0] != oldSteps[0] {
		t.Fatalf("updated steps = %#v, want old step plus two new evidenced steps", updated.Steps)
	}
	if len(updated.RevisionHistory) != 1 || len(updated.RevisionHistory[0].Steps) != 1 {
		t.Fatalf("revision history = %#v, want prior step snapshot", updated.RevisionHistory)
	}
}

func TestSearchEpisodeMemoryCandidatesPrioritizesPreferredAndSameScope(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(t.TempDir())
	items := []DeviceMemoryItem{
		{ID: "preferred", Type: "fact", Status: deviceMemoryStatusActive, DeviceID: "device_a", AppName: "Other", Title: "preferred"},
		{ID: "same_active", Type: "fact", Status: deviceMemoryStatusActive, DeviceID: "device_a", AppName: "Settings", Title: "same active"},
		{ID: "same_disputed", Type: "fact", Status: deviceMemoryStatusDisputed, DeviceID: "device_a", AppName: "Settings", Title: "same disputed"},
		{ID: "other_related", Type: "fact", Status: deviceMemoryStatusActive, DeviceID: "device_a", AppName: "Other", Content: "needle"},
	}
	for _, item := range items {
		item.Applicability = map[string]string{"device_id": item.DeviceID, "app_name": item.AppName}
		if _, err := store.Upsert(ctx, item); err != nil {
			t.Fatalf("Upsert(%s) error = %v", item.ID, err)
		}
	}
	got, err := store.SearchEpisodeMemoryCandidates(ctx, EpisodeMemoryCandidateQuery{
		Terms: []string{"needle"}, PreferredIDs: []string{"preferred"}, DeviceID: "device_a",
		Scope: map[string]string{"device_id": "device_a", "app_name": "Settings"}, Limit: 4, CharBudget: 12000,
	})
	if err != nil {
		t.Fatalf("SearchEpisodeMemoryCandidates() error = %v", err)
	}
	want := []string{"preferred", "same_active", "same_disputed", "other_related"}
	if len(got) != len(want) {
		t.Fatalf("candidate IDs = %#v, want %v", got, want)
	}
	for index, id := range want {
		if got[index].ID != id {
			t.Fatalf("candidate[%d] = %s, want %s; all=%#v", index, got[index].ID, id, got)
		}
	}
}

func TestEpisodeMemoryCreateDoesNotDuplicateExistingScope(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	if _, err := plane.device.Upsert(ctx, DeviceMemoryItem{
		ID: "existing_scope", Type: "fact", Status: deviceMemoryStatusActive, Title: "Existing conclusion", Summary: "Use the existing rule",
		DeviceID: "device_a", AppName: "Settings", Applicability: map[string]string{"device_id": "device_a", "app_name": "Settings"},
		Tags: []string{episodeMemoryTag},
	}); err != nil {
		t.Fatalf("Upsert(existing) error = %v", err)
	}
	processor := newEpisodeMemoryProcessor(plane, &episodeMemoryScriptedModel{})
	episode := TaskEpisode{ID: "ep_collision", DeviceScope: map[string]string{"device_id": "device_a"}}
	candidate := episodeMemoryCandidate{
		LessonKey: "different_wording", Type: episodeMemoryTypeFact, Action: episodeMemoryActionCreate, Retention: episodeMemoryRetentionDurable,
		Situation: "Different conclusion", Guidance: "Use a different rule", ExpectedEffect: "Different effect",
		Scope: map[string]string{"device_id": "device_a", "app_name": "Settings"}, EvidenceRefs: []string{"evt"},
	}
	id, err := processor.createMemory(ctx, episode, candidate)
	if err != nil {
		t.Fatalf("createMemory() error = %v", err)
	}
	if id != "existing_scope" {
		t.Fatalf("createMemory() id = %q, want existing scoped memory", id)
	}
	items, err := plane.device.readAll()
	if err != nil || len(items) != 1 {
		t.Fatalf("memories = %#v error=%v, want no duplicate", items, err)
	}
}

func TestLimitDeviceMemoryRecallHonorsBudget(t *testing.T) {
	results := make([]MemoryHit, 0, 5)
	for index := 0; index < 5; index++ {
		results = append(results, MemoryHit{
			ID: "memory_" + string(rune('a'+index)), Type: "procedure", Title: strings.Repeat("title", 40),
			Summary: strings.Repeat("summary", 80), Content: strings.Repeat("content", 300),
			Steps: []ProcedureStep{{Tool: "touch_gesture", Description: strings.Repeat("step", 80)}},
		})
	}
	limited := limitDeviceMemoryRecall(results, 4800)
	encoded, err := json.Marshal(map[string]any{"results": limited})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if len(encoded) > 4800 {
		t.Fatalf("recall payload bytes = %d, want <= 4800", len(encoded))
	}
	if len(limited) == 0 || len(limited) > 5 {
		t.Fatalf("limited results = %d, want 1..5", len(limited))
	}
}
