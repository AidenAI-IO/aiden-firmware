package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTelemetryConfigDefaults(t *testing.T) {
	cfg := TelemetryConfig{}
	if cfg.EnabledOrDefault() {
		t.Fatal("EnabledOrDefault() = true, want false")
	}
	if cfg.ProviderOrDefault() != "langfuse" {
		t.Fatalf("ProviderOrDefault() = %q, want langfuse", cfg.ProviderOrDefault())
	}
	if !cfg.UploadScreenshotsOrDefault() {
		t.Fatal("UploadScreenshotsOrDefault() = false, want true")
	}
	if cfg.UploadTimeoutOrDefault() != 30*time.Second {
		t.Fatalf("UploadTimeoutOrDefault() = %s, want 30s", cfg.UploadTimeoutOrDefault())
	}
	if cfg.MaxRetryOrDefault() != 2 {
		t.Fatalf("MaxRetryOrDefault() = %d, want 2", cfg.MaxRetryOrDefault())
	}
}

func TestConfigValidateTelemetryRequiresBaseURL(t *testing.T) {
	enabled := true
	cfg := Config{
		Model: ModelConfig{Provider: "fake"},
		Telemetry: TelemetryConfig{
			Enabled: &enabled,
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "telemetry.base_url") {
		t.Fatalf("Validate() = %v, want telemetry.base_url error", err)
	}
}

func TestBuildLangfuseBatchMapsPlannerToolVerifier(t *testing.T) {
	start := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	canFinish := true
	episode := TaskEpisode{
		ID:        "ep_test_001",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(12 * time.Second).Format(time.RFC3339Nano),
		UserGoal:  "打开系统设置",
		Outcome: TaskEpisodeOutcome{
			Success:     true,
			FinalAnswer: "已打开设置",
		},
		Tags: []string{"settings"},
		Extra: map[string]interface{}{
			"total_duration_ms": 12000.0,
			"prompt_tokens":     100.0,
		},
		Events: []TaskEpisodeEvent{
			{
				EventID:  "evt1",
				Ts:       start.Format(time.RFC3339Nano),
				Type:     "planner_decision",
				Role:     "planner",
				Objective: "打开系统设置",
				Plan:     []string{"打开系统设置"},
				NextStep: "点击设置图标",
			},
			{
				EventID:  "evt2",
				Ts:       start.Add(2 * time.Second).Format(time.RFC3339Nano),
				Type:     "tool_call",
				ToolName: "mouse_click",
				ToolInput: `{"x":100,"y":200}`,
			},
			{
				EventID:       "evt3",
				Ts:            start.Add(3 * time.Second).Format(time.RFC3339Nano),
				Type:          "tool_result",
				ToolName:      "mouse_click",
				Observation:   `{"action_output":"clicked"}`,
				ScreenshotRef: "artifacts/step_003.jpeg",
			},
			{
				EventID:   "evt4",
				Ts:        start.Add(5 * time.Second).Format(time.RFC3339Nano),
				Type:      "verifier_decision",
				CanFinish: &canFinish,
				Reason:    "设置页面已打开",
				Content:   "已打开设置",
			},
		},
	}

	exporter := NewEpisodeExporter(TelemetryConfig{
		Enabled:  boolPtr(true),
		BaseURL:  "http://langfuse.test",
		Tags:     []string{"aiden-hardware"},
	}, nil)
	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir())
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}
	if len(batch) < 5 {
		t.Fatalf("batch len = %d, want at least 5 events", len(batch))
	}

	types := map[string]int{}
	names := map[string]int{}
	for _, event := range batch {
		types[event.Type]++
		var body map[string]interface{}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if name, _ := body["name"].(string); name != "" {
			names[name]++
		}
	}
	if types["trace-create"] != 1 {
		t.Fatalf("trace-create count = %d, want 1", types["trace-create"])
	}
	if types["score-create"] != 1 {
		t.Fatalf("score-create count = %d, want 1", types["score-create"])
	}
	if names["planner"] != 1 {
		t.Fatalf("planner span count = %d, want 1", names["planner"])
	}
	if names["tool/mouse_click"] != 1 {
		t.Fatalf("tool span count = %d, want 1", names["tool/mouse_click"])
	}
	if names["verifier"] != 1 {
		t.Fatalf("verifier span count = %d, want 1", names["verifier"])
	}
}

func TestExportEpisodeDirUploadsToLangfuse(t *testing.T) {
	var ingestionCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/public/ingestion":
			ingestionCalls++
			user, pass, ok := r.BasicAuth()
			if !ok || user != "pk-test" || pass != "sk-test" {
				t.Errorf("unexpected auth: ok=%v user=%q pass=%q", ok, user, pass)
			}
			body, _ := io.ReadAll(r.Body)
			var req langfuseIngestionRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode ingestion request: %v", err)
			}
			if len(req.Batch) == 0 {
				t.Fatal("expected non-empty ingestion batch")
			}
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`{"successes":[{"id":"ok","status":201}],"errors":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/public/media":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"mediaId":"media-123","uploadUrl":null}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	episodeDir := t.TempDir()
	start := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	episode := TaskEpisode{
		ID:        "ep_export_test",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(5 * time.Second).Format(time.RFC3339Nano),
		UserGoal:  "打开时钟",
		Outcome: TaskEpisodeOutcome{
			Success:     true,
			FinalAnswer: "done",
		},
	}
	if err := os.WriteFile(filepath.Join(episodeDir, "episode.yaml"), []byte(`
id: ep_export_test
started_at: "`+episode.StartedAt+`"
ended_at: "`+episode.EndedAt+`"
user_goal: 打开时钟
outcome:
  success: true
  final_answer: done
`), 0o644); err != nil {
		t.Fatalf("write episode.yaml: %v", err)
	}
	eventsPath := filepath.Join(episodeDir, "events.jsonl")
	if err := writeEpisodeEventsJSONL(eventsPath, []TaskEpisodeEvent{
		{
			EventID:  "evt1",
			Ts:       start.Format(time.RFC3339Nano),
			Type:     "planner_decision",
			NextStep: "打开时钟应用",
			Plan:     []string{"打开时钟应用"},
		},
		{
			EventID:       "evt2",
			Ts:            start.Add(time.Second).Format(time.RFC3339Nano),
			Type:          "tool_result",
			ToolName:      "screenshot",
			Observation:   `{"action_output":"ok"}`,
			ScreenshotRef: "artifacts/step_002.jpeg",
		},
	}); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
	artifactsDir := filepath.Join(episodeDir, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactsDir, "step_002.jpeg"), []byte("fakejpeg"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}

	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-test")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk-test")
	exporter := NewEpisodeExporter(TelemetryConfig{
		Enabled:           boolPtr(true),
		BaseURL:           server.URL,
		UploadScreenshots: boolPtr(true),
		MaxRetry:          0,
	}, nil)
	if err := exporter.ExportEpisodeDir(context.Background(), episodeDir, episode); err != nil {
		t.Fatalf("ExportEpisodeDir() error = %v", err)
	}
	if ingestionCalls == 0 {
		t.Fatal("expected ingestion request")
	}
}
