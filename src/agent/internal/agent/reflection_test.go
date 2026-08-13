package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	modelpkg "aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type reflectionScriptedModel struct {
	mu        sync.Mutex
	responses []string
	calls     [][]llms.MessageContent
}

type reflectionBlockingModel struct {
	inner   *reflectionScriptedModel
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *reflectionBlockingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
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

func (m *reflectionBlockingModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return m.inner.Call(ctx, prompt, options...)
}

func (m *reflectionBlockingModel) CallOptions() []chains.ChainCallOption {
	return m.inner.CallOptions()
}

func (m *reflectionBlockingModel) Spec() modelpkg.ModelSpec {
	return m.inner.Spec()
}

func (m *reflectionScriptedModel) GenerateContent(_ context.Context, messages []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
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

func (m *reflectionScriptedModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", nil
}

func (m *reflectionScriptedModel) CallOptions() []chains.ChainCallOption { return nil }

func (m *reflectionScriptedModel) Spec() modelpkg.ModelSpec { return modelpkg.ModelSpec{} }

func (m *reflectionScriptedModel) appendResponses(responses ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, responses...)
}

func (m *reflectionScriptedModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *reflectionScriptedModel) firstCallHasImage() bool {
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

func (m *reflectionScriptedModel) firstCallText() string {
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

func TestFailureReflectionPromptPrioritizesVisibleEvidenceAndSeparatesCauseFromGuard(t *testing.T) {
	episode := reflectionTestFailureEpisode("ep_prompt_contract", "restore the requested Safari page")
	model := &reflectionScriptedModel{responses: []string{`{"action":"ignore"}`}}
	processor := newFailureReflectionProcessor(
		NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil),
		model,
	)

	if _, err := processor.summarizeFailure(context.Background(), episode); err != nil {
		t.Fatalf("summarizeFailure() error = %v", err)
	}

	prompt := strings.ToLower(model.firstCallText())
	for _, want := range []string{
		"visible screenshot evidence",
		"higher priority",
		"why the user goal failed",
		"what the agent should do differently",
		"address bar",
		"hide the scheme or port",
		"repeated ineffective actions",
		"do not strengthen",
		"diagnostic tool",
		"supported and valid",
		"cause must closely paraphrase",
		"do not express uncertainty as alternatives",
		"remove every diagnosis",
		"bad: the service is unavailable",
		"good: safari kept showing",
		"bad: this is a server-side or network issue",
		"good: the same visible error remained",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("reflection model prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestFailureReflectionModelInputHidesInternalScreenshotPaths(t *testing.T) {
	ctx := context.Background()
	plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
	model := &reflectionScriptedModel{responses: []string{`{"action":"ignore"}`}}
	processor := newFailureReflectionProcessor(plane, model)
	episode := reflectionTestFailureEpisode("ep_screenshot_path", "inspect the visible error")
	episode.Events[1].RawObservation = `{"width":10,"height":10,"format":"jpeg","data":"` + base64.StdEncoding.EncodeToString([]byte("jpeg")) + `"}`
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	stored, err := plane.episodes.Get(ctx, episode.ID)
	if err != nil {
		t.Fatalf("Get() error=%v", err)
	}

	if _, err := processor.summarizeFailure(ctx, stored); err != nil {
		t.Fatalf("summarizeFailure() error = %v", err)
	}

	prompt := model.firstCallText()
	if strings.Contains(prompt, "artifacts/") || strings.Contains(prompt, "step_002") {
		t.Fatalf("reflection model input leaked an internal screenshot path:\n%s", prompt)
	}
	if !strings.Contains(prompt, stored.Events[1].EventID) {
		t.Fatalf("reflection model input missing screenshot event id %q:\n%s", stored.Events[1].EventID, prompt)
	}
	if !model.firstCallHasImage() {
		t.Fatal("reflection model input did not include screenshot binary content")
	}
}

func TestFailureReflectionCreatesPendingThenMergesToActive(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	models := &reflectionScriptedModel{}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	models.appendResponses(`{
  "action":"keep",
  "pattern":"Clicked the stale search result before checking the refreshed page",
  "cause":"The agent reused an old visual assumption",
  "missed_signal":"The result list had changed",
  "guard":"Observe the refreshed result list before clicking",
  "scope":"search results",
  "tags":["search","stale-result","observe-before-click"],
  "evidence_refs":["ep_one_result"]
}`)
	episodeOne := reflectionTestFailureEpisode("ep_one", "search for a product")
	episodeOne.Events[1].RawObservation = `{"width":10,"height":10,"format":"jpeg","data":"` + base64.StdEncoding.EncodeToString([]byte("jpeg")) + `"}`
	if _, err := plane.episodes.AddEpisode(ctx, episodeOne); err != nil {
		t.Fatalf("AddEpisode(ep_one) error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, 5, nil); err != nil {
		t.Fatalf("ProcessBatch(ep_one) error = %v", err)
	}

	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("device memories = %#v, want one failure memory", items)
	}
	memoryID := items[0].ID
	initialConfidence := items[0].Confidence
	initialEntities := append([]string(nil), items[0].Entities...)
	if items[0].Status != "pending" || distinctEpisodeEvidenceCount(items[0].EvidenceRefs) != 1 {
		t.Fatalf("first failure memory = %#v, want pending with one evidence", items[0])
	}
	if !hasReflectionFailureTag(items[0].Tags) {
		t.Fatalf("first failure memory tags = %#v, want %q", items[0].Tags, reflectionFailureTag)
	}
	if !models.firstCallHasImage() {
		t.Fatal("failure summary call did not include the stored screenshot")
	}
	hits, err := plane.device.Search(ctx, DeviceMemoryQuery{Types: []string{"failure"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search(pending) error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("pending failure memory should not be recalled, got %#v", hits)
	}

	models.appendResponses(
		`{
  "action":"keep",
  "pattern":"Clicked a search result without observing the refreshed list",
  "cause":"The agent acted on stale page state",
  "missed_signal":"The visible results no longer matched the prior assumption",
  "guard":"Observe the current result list before clicking",
  "scope":"search results",
  "tags":["search","fresh-observation"],
  "evidence_refs":["ep_two_result"]
}`,
		`{"action":"merge","memory_id":"`+memoryID+`"}`,
	)
	episodeTwo := reflectionTestFailureEpisode("ep_two", "search for another product")
	episodeTwo.RetrievedMemoryRefs = []string{memoryID}
	episodeTwo.Entities = []string{"new-entity-must-not-expand-the-memory"}
	if _, err := plane.episodes.AddEpisode(ctx, episodeTwo); err != nil {
		t.Fatalf("AddEpisode(ep_two) error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, 5, nil); err != nil {
		t.Fatalf("ProcessBatch(ep_two) error = %v", err)
	}

	merged, found, err := plane.device.Get(ctx, memoryID)
	if err != nil || !found {
		t.Fatalf("Get(%s) found=%v error=%v", memoryID, found, err)
	}
	if merged.Status != "active" || distinctEpisodeEvidenceCount(merged.EvidenceRefs) != 2 {
		t.Fatalf("merged failure memory = %#v, want active with two evidence episodes", merged)
	}
	if merged.FailureCount != 1 {
		t.Fatalf("FailureCount = %d, want 1 for recalled memory merged to the same failure", merged.FailureCount)
	}
	if merged.Confidence != initialConfidence || !slices.Equal(merged.Entities, initialEntities) {
		t.Fatalf("merge changed fixed memory fields: confidence=%v entities=%#v", merged.Confidence, merged.Entities)
	}

	if err := plane.updateReferencedMemoryOutcomes(ctx, TaskEpisode{
		ID:                  "ep_success",
		RetrievedMemoryRefs: []string{memoryID},
		Outcome:             TaskEpisodeOutcome{Success: true},
	}); err != nil {
		t.Fatalf("updateReferencedMemoryOutcomes(success) error = %v", err)
	}
	validated, found, err := plane.device.Get(ctx, memoryID)
	if err != nil || !found {
		t.Fatalf("Get(%s) after success found=%v error=%v", memoryID, found, err)
	}
	if validated.Status != "active" || validated.SuccessCount != 1 {
		t.Fatalf("validated failure memory = %#v, want active with SuccessCount=1", validated)
	}
	hits, err = plane.device.Search(ctx, DeviceMemoryQuery{Terms: []string{"search"}, Types: []string{"failure"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search(active) error = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != memoryID {
		t.Fatalf("active failure recall results = %#v, want %s", hits, memoryID)
	}
}

func TestFailureReflectionDoesNotMergeAcrossDevices(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	models := &reflectionScriptedModel{}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if _, err := plane.device.Upsert(ctx, DeviceMemoryItem{
		ID:       "failure_other_device",
		Type:     "failure",
		Status:   "pending",
		Title:    "Clicked stale search result",
		Content:  "Guard: observe refreshed results before clicking",
		DeviceID: "device_b",
		Tags:     []string{reflectionFailureTag, "search"},
		EvidenceRefs: []MemorySourceRef{{
			Type: "episode",
			ID:   "ep_other_device",
		}},
	}); err != nil {
		t.Fatalf("Upsert(other device) error = %v", err)
	}

	models.appendResponses(
		`{
  "action":"keep",
  "pattern":"Clicked a stale search result",
  "cause":"The agent reused an old visual assumption",
  "missed_signal":"The result list had changed",
  "guard":"Observe the refreshed result list before clicking",
  "scope":"search results",
  "tags":["search","stale-result"],
  "evidence_refs":["ep_device_a_result"]
}`,
		`{"action":"merge","memory_id":"failure_other_device"}`,
	)
	episode := reflectionTestFailureEpisode("ep_device_a", "search for a product")
	episode.DeviceScope = map[string]string{"device_id": "device_a"}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, reflectionBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}

	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("device failure memories = %#v, want separate memories per device", items)
	}
	other, found, err := plane.device.Get(ctx, "failure_other_device")
	if err != nil || !found {
		t.Fatalf("Get(other device) found=%v error=%v", found, err)
	}
	if got := distinctEpisodeEvidenceCount(other.EvidenceRefs); got != 1 {
		t.Fatalf("other-device evidence count = %d, want 1", got)
	}
}

func TestFailureReflectionIgnoresCanceledEpisodeWithoutCallingModel(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	models := &reflectionScriptedModel{}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := reflectionTestFailureEpisode("ep_canceled", "open settings")
	episode.Events = []TaskEpisodeEvent{{
		EventID:   "ep_canceled_result",
		Type:      "tool_result",
		IsError:   true,
		ToolError: NewToolError(CodeCanceled, "canceled"),
	}}
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, 5, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if calls := models.callCount(); calls != 0 {
		t.Fatalf("model calls = %d, want 0", calls)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if state.Episodes[episode.ID].Status != reflectionStatusIgnored {
		t.Fatalf("reflection state = %#v, want ignored", state.Episodes[episode.ID])
	}
}

func TestFailureSummaryRequiresCauseMissedSignalAndValidEvidence(t *testing.T) {
	episode := reflectionTestFailureEpisode("ep_summary_validation", "open the right result")
	tests := []struct {
		name     string
		response string
	}{
		{
			name: "missing cause",
			response: `{
  "action":"keep",
  "pattern":"Clicked the wrong result",
  "cause":"",
  "missed_signal":"The result title did not match",
  "guard":"Verify the result title before clicking",
  "scope":"search results",
  "tags":["search"],
  "evidence_refs":["ep_summary_validation_result"]
}`,
		},
		{
			name: "missing missed signal",
			response: `{
  "action":"keep",
  "pattern":"Clicked the wrong result",
  "cause":"The agent assumed the first result was correct",
  "missed_signal":"",
  "guard":"Verify the result title before clicking",
  "scope":"search results",
  "tags":["search"],
  "evidence_refs":["ep_summary_validation_result"]
}`,
		},
		{
			name: "invalid evidence",
			response: `{
  "action":"keep",
  "pattern":"Clicked the wrong result",
  "cause":"The agent assumed the first result was correct",
  "missed_signal":"The result title did not match",
  "guard":"Verify the result title before clicking",
  "scope":"search results",
  "tags":["search"],
  "evidence_refs":["invented_event"]
}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plane := NewFilesystemMemoryPlane(filepath.Join(t.TempDir(), "memory"), DefaultMemoryExtractionConfig(), nil)
			models := &reflectionScriptedModel{responses: []string{test.response}}
			processor := newFailureReflectionProcessor(plane, models)
			if _, err := processor.summarizeFailure(context.Background(), episode); err == nil {
				t.Fatal("summarizeFailure() error = nil, want validation failure")
			}
		})
	}
}

func TestReflectionScreenshotsPreferLatestFailure(t *testing.T) {
	events := []TaskEpisodeEvent{
		{ScreenshotRef: "shot-0.png"},
		{ScreenshotRef: "shot-1.png", IsError: true},
		{ScreenshotRef: "shot-2.png"},
		{ScreenshotRef: "shot-3.png", IsError: true},
		{ScreenshotRef: "shot-4.png"},
	}
	refs := selectReflectionScreenshotRefs(events)
	if len(refs) != 3 {
		t.Fatalf("screenshot refs = %#v, want 3", refs)
	}
	if refs[0] != "shot-3.png" {
		t.Fatalf("first screenshot ref = %q, want latest failure screenshot", refs[0])
	}
	if !containsStringFold(refs, "shot-4.png") {
		t.Fatalf("screenshot refs = %#v, want final state", refs)
	}
}

func TestFailureReflectionRetryReusesMemoryAlreadyWrittenForEpisode(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	models := &reflectionScriptedModel{}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := reflectionTestFailureEpisode("ep_crash_recovery", "open the result")
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	if _, err := plane.device.Upsert(ctx, DeviceMemoryItem{
		ID:           "failure_written_before_crash",
		Type:         "failure",
		Status:       "pending",
		Title:        "already written",
		Content:      "Guard: observe before acting",
		Tags:         []string{reflectionFailureTag},
		EvidenceRefs: []MemorySourceRef{{Type: "episode", ID: episode.ID}},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if _, err := processor.ProcessBatch(ctx, 5, nil); err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if calls := models.callCount(); calls != 0 {
		t.Fatalf("model calls = %d, want 0 for idempotent recovery", calls)
	}
	items, err := plane.device.readAll()
	if err != nil {
		t.Fatalf("readAll() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("failure memories = %#v, want no duplicate", items)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if state.Episodes[episode.ID].Status != reflectionStatusDone {
		t.Fatalf("reflection state = %#v, want done", state.Episodes[episode.ID])
	}
}

func TestFailureReflectionRetryDoesNotBlockLaterEpisodes(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	models := &reflectionScriptedModel{responses: []string{
		`{"action":"keep"}`,
		`{"action":"ignore"}`,
	}}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	base := time.Now().UTC().Add(time.Second)
	first := reflectionTestFailureEpisode("ep_retry_first", "first failure")
	first.StartedAt = base.Format(time.RFC3339Nano)
	first.EndedAt = base.Add(time.Millisecond).Format(time.RFC3339Nano)
	second := reflectionTestFailureEpisode("ep_retry_second", "second failure")
	second.StartedAt = base.Add(time.Second).Format(time.RFC3339Nano)
	second.EndedAt = base.Add(time.Second + time.Millisecond).Format(time.RFC3339Nano)
	for _, episode := range []TaskEpisode{first, second} {
		if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
			t.Fatalf("AddEpisode(%s) error = %v", episode.ID, err)
		}
	}

	result, err := processor.ProcessBatch(ctx, reflectionBatchLimit, nil)
	if err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if calls := models.callCount(); calls != 2 {
		t.Fatalf("model calls = %d, want later eligible Episode processed after retry", calls)
	}
	if result.NextRunAt.IsZero() {
		t.Fatal("ProcessBatch() NextRunAt is zero, want retry due time")
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if state.Episodes[first.ID].Status != reflectionStatusRetry {
		t.Fatalf("first Episode state = %#v, want retry", state.Episodes[first.ID])
	}
	if state.Episodes[second.ID].Status != reflectionStatusIgnored {
		t.Fatalf("second Episode state = %#v, want ignored", state.Episodes[second.ID])
	}

	models.appendResponses(`{"action":"ignore"}`)
	processor.now = func() time.Time { return result.NextRunAt.Add(time.Second) }
	if _, err := processor.ProcessBatch(ctx, reflectionBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch(retry) error = %v", err)
	}
	if calls := models.callCount(); calls != 3 {
		t.Fatalf("model calls after retry = %d, want retried Episode preserved behind cursor", calls)
	}
	state, err = processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot(retry) error = %v", err)
	}
	if state.Episodes[first.ID].Status != reflectionStatusIgnored {
		t.Fatalf("first Episode state after retry = %#v, want ignored", state.Episodes[first.ID])
	}
}

func TestFailureReflectionStopsRetryingAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	models := &reflectionScriptedModel{}
	for index := 0; index < reflectionMaxAttempts; index++ {
		models.appendResponses(`{"action":"keep"}`)
	}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := reflectionTestFailureEpisode("ep_permanent_failure", "open the right result")
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	for attempt := 1; attempt <= reflectionMaxAttempts; attempt++ {
		result, err := processor.ProcessBatch(ctx, reflectionBatchLimit, nil)
		if err != nil {
			t.Fatalf("ProcessBatch(attempt %d) error = %v", attempt, err)
		}
		state, err := processor.state.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot(attempt %d) error = %v", attempt, err)
		}
		status := state.Episodes[episode.ID]
		if attempt < reflectionMaxAttempts {
			if status.Status != reflectionStatusRetry || status.AttemptCount != attempt {
				t.Fatalf("attempt %d status = %#v, want retry with attempt count", attempt, status)
			}
			processor.now = func() time.Time { return result.NextRunAt.Add(time.Second) }
			continue
		}
		if status.Status != reflectionStatusIgnored || status.AttemptCount != reflectionMaxAttempts {
			t.Fatalf("final status = %#v, want ignored after max attempts", status)
		}
	}

	if _, err := processor.ProcessBatch(ctx, reflectionBatchLimit, nil); err != nil {
		t.Fatalf("ProcessBatch(after ignored) error = %v", err)
	}
	if calls := models.callCount(); calls != reflectionMaxAttempts {
		t.Fatalf("model calls = %d, want %d", calls, reflectionMaxAttempts)
	}
}

func TestFailureReflectionBatchLimitLeavesRemainingEpisodePending(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	models := &reflectionScriptedModel{}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	base := time.Now().UTC().Add(time.Second)
	for index := 0; index < reflectionBatchLimit+1; index++ {
		id := fmt.Sprintf("ep_batch_%02d", index)
		episode := reflectionTestFailureEpisode(id, "batch failure")
		episode.StartedAt = base.Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano)
		episode.EndedAt = base.Add(time.Duration(index)*time.Second + time.Millisecond).Format(time.RFC3339Nano)
		if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
			t.Fatalf("AddEpisode(%s) error = %v", id, err)
		}
		models.appendResponses(`{"action":"ignore"}`)
	}

	result, err := processor.ProcessBatch(ctx, reflectionBatchLimit, nil)
	if err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if calls := models.callCount(); calls != reflectionBatchLimit {
		t.Fatalf("model calls = %d, want %d", calls, reflectionBatchLimit)
	}
	if !result.HasPending {
		t.Fatal("ProcessBatch() HasPending = false, want remaining Episode")
	}

	result, err = processor.ProcessBatch(ctx, reflectionBatchLimit, nil)
	if err != nil {
		t.Fatalf("ProcessBatch(second) error = %v", err)
	}
	if calls := models.callCount(); calls != reflectionBatchLimit+1 {
		t.Fatalf("model calls after second batch = %d, want %d", calls, reflectionBatchLimit+1)
	}
	if result.HasPending {
		t.Fatal("ProcessBatch(second) HasPending = true, want queue drained")
	}
}

func TestFailureReflectionStopsBeforeFilteringAnotherEpisode(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	processor := newFailureReflectionProcessor(plane, &reflectionScriptedModel{})
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	episode := reflectionTestFailureEpisode("ep_stop_before_filter", "")
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}
	result, err := processor.ProcessBatch(ctx, reflectionBatchLimit, func() bool { return true })
	if err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if !result.HasPending {
		t.Fatal("ProcessBatch() HasPending = false, want stopped work to remain pending")
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if _, found := state.Episodes[episode.ID]; found {
		t.Fatalf("stopped Episode state = %#v, want untouched", state.Episodes[episode.ID])
	}
}

func TestReflectionEpisodeDueHonorsLeaseAndRetry(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		status   reflectionEpisodeStatus
		eligible bool
		due      time.Time
	}{
		{
			name:     "processing lease active",
			status:   reflectionEpisodeStatus{Status: reflectionStatusProcessing, ProcessingStartedAt: now.Add(-reflectionProcessingLease + time.Second).Format(time.RFC3339Nano)},
			eligible: false,
			due:      now.Add(time.Second),
		},
		{
			name:     "processing lease expired",
			status:   reflectionEpisodeStatus{Status: reflectionStatusProcessing, ProcessingStartedAt: now.Add(-reflectionProcessingLease).Format(time.RFC3339Nano)},
			eligible: true,
			due:      now,
		},
		{
			name:     "retry waiting",
			status:   reflectionEpisodeStatus{Status: reflectionStatusRetry, RetryAt: now.Add(time.Second).Format(time.RFC3339Nano)},
			eligible: false,
			due:      now.Add(time.Second),
		},
		{
			name:     "retry due",
			status:   reflectionEpisodeStatus{Status: reflectionStatusRetry, RetryAt: now.Format(time.RFC3339Nano)},
			eligible: true,
			due:      now,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eligible, due := reflectionEpisodeDue(test.status, now)
			if eligible != test.eligible || !due.Equal(test.due) {
				t.Fatalf("reflectionEpisodeDue() = (%v, %v), want (%v, %v)", eligible, due, test.eligible, test.due)
			}
		})
	}
}

func TestReflectionStateKeepsOnlyRecentTerminalEpisodes(t *testing.T) {
	base := time.Now().UTC()
	store := newReflectionStateStore(filepath.Join(t.TempDir(), "reflection.yaml"), base.Add(-time.Minute))
	for index := 0; index < reflectionRecentTerminals+6; index++ {
		id := fmt.Sprintf("ep_terminal_%03d", index)
		if err := store.CompleteEpisode(id, base.Add(time.Duration(index)*time.Second), reflectionEpisodeStatus{Status: reflectionStatusDone}); err != nil {
			t.Fatalf("CompleteEpisode(%s) error = %v", id, err)
		}
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(state.Episodes) != reflectionRecentTerminals {
		t.Fatalf("terminal state count = %d, want %d", len(state.Episodes), reflectionRecentTerminals)
	}
	if state.CompletedThroughID != fmt.Sprintf("ep_terminal_%03d", reflectionRecentTerminals+5) {
		t.Fatalf("CompletedThroughID = %q, want last Episode", state.CompletedThroughID)
	}
	if _, found := state.Episodes["ep_terminal_000"]; found {
		t.Fatal("old terminal Episode was not pruned")
	}
}

func TestSearchFailureMemoriesUsesOptionalAppAndPage(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(t.TempDir())
	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID:       "failure_location_match",
		Type:     "failure",
		Status:   "pending",
		Title:    "Unrelated wording",
		Content:  "Guard: inspect the current state",
		AppName:  "Taobao",
		PageName: "search_results",
		Tags:     []string{reflectionFailureTag},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	hits, err := store.SearchFailureMemories(ctx, FailureMemoryQuery{Terms: []string{"search_results"}, Limit: 5})
	if err != nil {
		t.Fatalf("SearchFailureMemories() error = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "failure_location_match" {
		t.Fatalf("location search hits = %#v", hits)
	}
}

func TestSearchFailureMemoriesRequiresMatchingDevice(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(t.TempDir())
	for _, item := range []DeviceMemoryItem{
		{ID: "failure_device_a", Type: "failure", Status: "pending", DeviceID: "device_a", Content: "Guard: refresh search results", Tags: []string{reflectionFailureTag}},
		{ID: "failure_device_b", Type: "failure", Status: "pending", DeviceID: "device_b", Content: "Guard: refresh search results", Tags: []string{reflectionFailureTag}},
		{ID: "failure_without_device", Type: "failure", Status: "pending", Content: "Guard: refresh search results", Tags: []string{reflectionFailureTag}},
	} {
		if _, err := store.Upsert(ctx, item); err != nil {
			t.Fatalf("Upsert(%s) error = %v", item.ID, err)
		}
	}
	hits, err := store.SearchFailureMemories(ctx, FailureMemoryQuery{
		Terms:    []string{"refresh search results"},
		DeviceID: "device_a",
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("SearchFailureMemories() error = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "failure_device_a" {
		t.Fatalf("device-scoped hits = %#v, want only failure_device_a", hits)
	}
}

func TestNormalRecallHidesLegacyFailureMemories(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(t.TempDir())
	for _, item := range []DeviceMemoryItem{
		{ID: "legacy_failure", Type: "failure", Status: "active", Title: "legacy", Content: "legacy failure"},
		{ID: "reflected_failure", Type: "failure", Status: "active", Title: "reflected", Content: "reflected failure", Tags: []string{reflectionFailureTag}},
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
		t.Fatalf("failure recall hits = %#v, want only reflection-managed memory", hits)
	}
}

func TestNormalRecallHidesConflictedReflectionFailureMemories(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(t.TempDir())
	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID:      "conflicted_reflection_failure",
		Type:    "failure",
		Status:  "conflicted",
		Title:   "conflicted reflected failure",
		Content: "conflicted reflected failure",
		Tags:    []string{reflectionFailureTag},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	hits, err := store.Search(ctx, DeviceMemoryQuery{Types: []string{"conflict"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("conflicted reflection failure recall hits = %#v, want none", hits)
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

func TestReflectionWorkerWaitsForIdleAndStopsAfterTaskStarts(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	models := &reflectionScriptedModel{}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	models.appendResponses(`{
  "action":"keep",
  "pattern":"Acted before observing the current page",
  "cause":"Stale state assumption",
  "missed_signal":"The current page was not inspected",
  "guard":"Observe before acting",
  "scope":"",
  "tags":["observe","stale-state","guard"],
  "evidence_refs":["ep_idle_result"]
}`)
	if _, err := plane.episodes.AddEpisode(ctx, reflectionTestFailureEpisode("ep_idle", "open the current result")); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	worker := newReflectionWorker(processor)
	worker.idleDelay = 20 * time.Millisecond
	if err := worker.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer worker.Stop()
	worker.TaskStarted()
	time.Sleep(60 * time.Millisecond)
	if calls := models.callCount(); calls != 0 {
		t.Fatalf("model calls while task active = %d, want 0", calls)
	}
	worker.TaskFinished()
	deadline := time.Now().Add(time.Second)
	for models.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls := models.callCount(); calls != 1 {
		t.Fatalf("model calls after idle = %d, want 1", calls)
	}
}

func TestReflectionWorkerStopsAfterCurrentEpisodeWhenTaskStarts(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	inner := &reflectionScriptedModel{}
	inner.appendResponses(`{"action":"ignore"}`, `{"action":"ignore"}`)
	models := &reflectionBlockingModel{
		inner:   inner,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	base := time.Now().UTC().Add(time.Second)
	for index := 0; index < 2; index++ {
		episode := reflectionTestFailureEpisode(fmt.Sprintf("ep_inflight_%d", index), "in-flight failure")
		episode.StartedAt = base.Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano)
		episode.EndedAt = base.Add(time.Duration(index)*time.Second + time.Millisecond).Format(time.RFC3339Nano)
		if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
			t.Fatalf("AddEpisode(%d) error = %v", index, err)
		}
	}

	worker := newReflectionWorker(processor)
	worker.idleDelay = 10 * time.Millisecond
	if err := worker.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer worker.Stop()
	select {
	case <-models.started:
	case <-time.After(time.Second):
		t.Fatal("reflection did not start")
	}
	worker.TaskStarted()
	close(models.release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := processor.state.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		if state.Episodes["ep_inflight_0"].Status == reflectionStatusIgnored {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls := inner.callCount(); calls != 1 {
		t.Fatalf("model calls = %d, want current Episode only", calls)
	}
	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot(final) error = %v", err)
	}
	if _, found := state.Episodes["ep_inflight_1"]; found {
		t.Fatalf("second Episode state = %#v, want unprocessed", state.Episodes["ep_inflight_1"])
	}
}

func TestReflectionWorkerShutdownDoesNotConsumeAttempt(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	inner := &reflectionScriptedModel{}
	models := &reflectionBlockingModel{
		inner:   inner,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	processor := newFailureReflectionProcessor(plane, models)
	if err := processor.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	episode := reflectionTestFailureEpisode("ep_shutdown", "inspect the current result")
	if _, err := plane.episodes.AddEpisode(ctx, episode); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	worker := newReflectionWorker(processor)
	worker.idleDelay = 10 * time.Millisecond
	if err := worker.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-models.started:
	case <-time.After(time.Second):
		t.Fatal("reflection did not start")
	}
	worker.Stop()

	state, err := processor.state.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	status, found := state.Episodes[episode.ID]
	if found && status != (reflectionEpisodeStatus{}) {
		t.Fatalf("shutdown persisted processing failure state: %#v", status)
	}
}

func TestReflectionProcessorsSerializeAcrossRuntimeInstances(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	firstPlane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	secondPlane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	firstInner := &reflectionScriptedModel{responses: []string{`{"action":"ignore"}`}}
	firstModel := &reflectionBlockingModel{
		inner:   firstInner,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	secondModel := &reflectionScriptedModel{responses: []string{`{"action":"ignore"}`}}
	first := newFailureReflectionProcessor(firstPlane, firstModel)
	second := newFailureReflectionProcessor(secondPlane, secondModel)
	if err := first.Initialize(); err != nil {
		t.Fatalf("first Initialize() error = %v", err)
	}
	if err := second.Initialize(); err != nil {
		t.Fatalf("second Initialize() error = %v", err)
	}
	if _, err := firstPlane.episodes.AddEpisode(ctx, reflectionTestFailureEpisode("ep_shared", "shared failure")); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := first.ProcessBatch(ctx, reflectionBatchLimit, nil)
		firstDone <- err
	}()
	select {
	case <-firstModel.started:
	case <-time.After(time.Second):
		t.Fatal("first processor did not acquire the batch")
	}
	if _, err := second.ProcessBatch(ctx, reflectionBatchLimit, nil); err == nil {
		t.Fatal("second processor acquired the same reflection batch concurrently")
	}
	close(firstModel.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first ProcessBatch() error = %v", err)
	}
	if _, err := second.ProcessBatch(ctx, reflectionBatchLimit, nil); err != nil {
		t.Fatalf("second ProcessBatch() after release error = %v", err)
	}
	if calls := secondModel.callCount(); calls != 0 {
		t.Fatalf("second model calls = %d, want completed Episode to be skipped", calls)
	}
}

func TestReflectionWorkerDoesNotAddIdleDelayAfterAlreadyIdle(t *testing.T) {
	worker := newReflectionWorker(nil)
	worker.idleDelay = 50 * time.Millisecond
	time.Sleep(60 * time.Millisecond)

	due := time.Now().Add(200 * time.Millisecond)
	delay := worker.delayFor(due)
	if delay > 225*time.Millisecond {
		t.Fatalf("delayFor() = %v, want retry at due time after idle requirement already satisfied", delay)
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

func TestNewRuntimeWithDepsStartsReflectionWorker(t *testing.T) {
	models := &reflectionScriptedModel{responses: []string{`{
  "action":"keep",
  "pattern":"Clicked the wrong result",
  "cause":"The agent assumed the first result was correct",
  "missed_signal":"The visible title did not match the target",
  "guard":"Verify the visible title before clicking",
  "scope":"search results",
  "tags":["search","verify-title"],
  "evidence_refs":["ep_runtime_reflection_result"]
}`}}
	runtime := NewRuntimeWithDeps(
		Config{ConfigDir: t.TempDir()},
		models,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	defer runtime.Close()
	plane, ok := runtime.memoryPlane.(*FilesystemMemoryPlane)
	if !ok || plane == nil || plane.reflection == nil {
		t.Fatal("NewRuntimeWithDeps() did not start reflection worker")
	}
	plane.reflection.idleDelay = 10 * time.Millisecond
	episode := reflectionTestFailureEpisode("ep_runtime_reflection", "open the right result")
	if err := plane.CommitEpisode(context.Background(), episode); err != nil {
		t.Fatalf("CommitEpisode() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for models.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls := models.callCount(); calls != 1 {
		t.Fatalf("reflection model calls = %d, want 1", calls)
	}
}

func TestNewRuntimeWithDepsSurfacesReflectionInitializationError(t *testing.T) {
	configDir := t.TempDir()
	stateDir := filepath.Join(configDir, "memory", "lifecycle")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "reflection.yaml"), []byte("episodes: ["), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runtime := NewRuntimeWithDeps(
		Config{ConfigDir: configDir},
		&reflectionScriptedModel{},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	defer runtime.Close()
	if err := runtime.ReflectionInitializationError(); err == nil {
		t.Fatal("ReflectionInitializationError() = nil, want malformed state error")
	}
}

func TestFailureEpisodeDoesNotWriteImmediateFailureMemory(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	episode := reflectionTestFailureEpisode("ep_no_direct_memory", "open settings")
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

func reflectionTestFailureEpisode(id, goal string) TaskEpisode {
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
