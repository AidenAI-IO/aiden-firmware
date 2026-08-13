package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBenchmarkMemoryScopeKeepsLongTermToolsOutOfDefaultMemory(t *testing.T) {
	memoryDir := t.TempDir()
	defaultStore := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	save := NewSaveMemoryTool(defaultStore)
	save.memoryDir = memoryDir
	recall := NewRecallMemoryTool(defaultStore)
	recall.memoryDir = memoryDir

	benchmarkCtx := WithBenchmarkMemoryScope(context.Background(), "run-2026")
	if _, err := save.Call(benchmarkCtx, `{"type":"fact","content":"benchmark only","evidence":["benchmark only"]}`); err != nil {
		t.Fatalf("save benchmark memory: %v", err)
	}

	benchmarkOutput, err := recall.Call(benchmarkCtx, `{}`)
	if err != nil {
		t.Fatalf("recall benchmark memory: %v", err)
	}
	if !strings.Contains(benchmarkOutput, "benchmark only") {
		t.Fatalf("benchmark scope did not recall saved memory: %s", benchmarkOutput)
	}

	defaultOutput, err := recall.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("recall default memory: %v", err)
	}
	if strings.Contains(defaultOutput, "benchmark only") {
		t.Fatalf("benchmark memory leaked into default scope: %s", defaultOutput)
	}
}

func TestBenchmarkMemoryScopeKeepsEpisodeLessonsOutOfDefaultMemory(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	episode := TaskEpisode{
		ID:          "ep_benchmark",
		MemoryScope: "run-2026",
		Status:      "active",
		StartedAt:   "2026-08-13T00:00:00Z",
		EndedAt:     "2026-08-13T00:00:01Z",
		UserGoal:    "open settings",
		Outcome:     TaskEpisodeOutcome{Success: true, VerifierReason: "done"},
		Events: []TaskEpisodeEvent{
			{Type: runEventToolCall, ToolName: "open_app", ToolInput: `{"app":"Settings"}`},
			{Type: "tool_result", ToolName: "open_app", Content: "opened"},
		},
	}

	if err := plane.CommitEpisode(ctx, episode); err != nil {
		t.Fatalf("CommitEpisode: %v", err)
	}
	defaultLongTerm, err := plane.LongTerm().Search(ctx, MemoryQuery{})
	if err != nil {
		t.Fatalf("search default long-term memory: %v", err)
	}
	if len(defaultLongTerm) != 0 {
		t.Fatalf("benchmark lesson leaked into default long-term memory: %#v", defaultLongTerm)
	}
	defaultDevice, err := plane.device.Search(ctx, DeviceMemoryQuery{})
	if err != nil {
		t.Fatalf("search default device memory: %v", err)
	}
	if len(defaultDevice) != 0 {
		t.Fatalf("benchmark lesson leaked into default device memory: %#v", defaultDevice)
	}

	scoped := plane.forBenchmarkScope("run-2026")
	scopedLongTerm, err := scoped.LongTerm().Search(ctx, MemoryQuery{})
	if err != nil {
		t.Fatalf("search scoped long-term memory: %v", err)
	}
	if len(scopedLongTerm) == 0 {
		t.Fatal("benchmark long-term lesson was not persisted in its scope")
	}
	scopedDevice, err := scoped.device.Search(ctx, DeviceMemoryQuery{})
	if err != nil {
		t.Fatalf("search scoped device memory: %v", err)
	}
	if len(scopedDevice) == 0 {
		t.Fatal("benchmark device memory was not persisted in its scope")
	}
}

func TestClearBenchmarkMemoryScopeRemovesOnlySelectedScope(t *testing.T) {
	memoryDir := t.TempDir()
	first := benchmarkMemoryScopeDir(memoryDir, "first")
	second := benchmarkMemoryScopeDir(memoryDir, "second")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", dir, err)
		}
	}

	if err := clearBenchmarkMemoryScope(memoryDir, "first"); err != nil {
		t.Fatalf("clear first scope: %v", err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("first scope still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("second scope should remain: %v", err)
	}
}

func TestBenchmarkClearMemoryScopeEndpointWorksWithoutBenchmarkToken(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	scope := "run-2026"
	scopeDir := benchmarkMemoryScopeDir(memoryDir, scope)
	if err := os.MkdirAll(scopeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll scope: %v", err)
	}

	server := &Server{runtime: &Runtime{
		memoryPlane: NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil),
	}}
	req := httptest.NewRequest(http.MethodPost, "/api/benchmark/memory_scope/clear", nil)
	req.Header.Set(BenchmarkMemoryScopeHeader, scope)
	rec := httptest.NewRecorder()
	server.handleBenchmarkClearMemoryScope(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(scopeDir); !os.IsNotExist(err) {
		t.Fatalf("scope still exists or stat failed: %v", err)
	}
}

func TestBenchmarkClearMemoryScopeWaitsForCanceledScopedActivity(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	scope := "run-2026"
	scopeDir := benchmarkMemoryScopeDir(memoryDir, scope)
	server := &Server{runtime: &Runtime{
		memoryPlane: NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil),
	}}

	activityCtx, cancelActivity := context.WithCancel(context.Background())
	releaseActivity, ok := server.beginBenchmarkMemoryScopeActivity(scope, cancelActivity)
	if !ok {
		t.Fatal("benchmark memory scope activity was rejected")
	}
	activityFinished := make(chan struct{})
	go func() {
		defer close(activityFinished)
		defer releaseActivity()
		<-activityCtx.Done()
		if err := os.MkdirAll(scopeDir, 0o755); err != nil {
			return
		}
		_ = os.WriteFile(filepath.Join(scopeDir, "late-marker"), []byte("x"), 0o644)
	}()

	req := httptest.NewRequest(http.MethodPost, "/api/benchmark/memory_scope/clear", nil)
	req.Header.Set(BenchmarkMemoryScopeHeader, scope)
	rec := httptest.NewRecorder()
	server.handleBenchmarkClearMemoryScope(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	select {
	case <-activityFinished:
	case <-time.After(time.Second):
		t.Fatal("scoped activity did not finish before cleanup returned")
	}
	if _, err := os.Stat(scopeDir); !os.IsNotExist(err) {
		t.Fatalf("scope was recreated by late activity or stat failed: %v", err)
	}
}

func TestBenchmarkMemoryScopeRejectsNewActivityWhileClearing(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	scope := "run-2026"
	server := &Server{}

	releaseActivity, ok := server.beginBenchmarkMemoryScopeActivity(scope, nil)
	if !ok {
		t.Fatal("benchmark memory scope activity was rejected")
	}
	cleanupStarted := make(chan struct{})
	cleanupFinished := make(chan error, 1)
	go func() {
		close(cleanupStarted)
		cleanupFinished <- server.clearBenchmarkMemoryScopeAfterActivities(memoryDir, scope)
	}()
	<-cleanupStarted

	deadline := time.Now().Add(time.Second)
	for {
		if release, accepted := server.beginBenchmarkMemoryScopeActivity(scope, nil); !accepted {
			break
		} else {
			release()
		}
		if time.Now().After(deadline) {
			t.Fatal("scope did not enter closing state")
		}
		time.Sleep(time.Millisecond)
	}

	releaseActivity()
	select {
	case err := <-cleanupFinished:
		if err != nil {
			t.Fatalf("clear benchmark memory scope: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scope cleanup did not finish after activity was released")
	}
}

func TestBenchmarkMemoryScopeConcurrentCleanupWaitsForSameResult(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	scope := "run-2026"
	server := &Server{}

	releaseActivity, ok := server.beginBenchmarkMemoryScopeActivity(scope, nil)
	if !ok {
		t.Fatal("benchmark memory scope activity was rejected")
	}
	firstCleanup := make(chan error, 1)
	secondCleanup := make(chan error, 1)
	go func() {
		firstCleanup <- server.clearBenchmarkMemoryScopeAfterActivities(memoryDir, scope)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		if release, accepted := server.beginBenchmarkMemoryScopeActivity(scope, nil); !accepted {
			break
		} else {
			release()
		}
		if time.Now().After(deadline) {
			t.Fatal("scope did not enter closing state")
		}
		time.Sleep(time.Millisecond)
	}
	go func() {
		secondCleanup <- server.clearBenchmarkMemoryScopeAfterActivities(memoryDir, scope)
	}()

	select {
	case err := <-secondCleanup:
		t.Fatalf("concurrent cleanup returned before activity finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseActivity()
	for index, cleanup := range []<-chan error{firstCleanup, secondCleanup} {
		select {
		case err := <-cleanup:
			if err != nil {
				t.Fatalf("cleanup %d: %v", index+1, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("cleanup %d did not finish", index+1)
		}
	}
}
