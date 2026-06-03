package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnrichEpisodeTelemetryAddsModelAndVersion(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"current_version":"20260603-120000-deadbeef"}`), 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	t.Setenv("AIDEN_OTA_STATE_PATH", statePath)

	buildCommit = "abc1234"
	buildVersion = "20260603-120000-abc1234"
	t.Cleanup(func() {
		buildCommit = "unknown"
		buildVersion = "dev"
	})

	episode := TaskEpisode{}
	enrichEpisodeTelemetry(&episode, Config{
		Model: ModelConfig{
			Provider: "openrouter",
			Model:    "google/gemini-3.5-flash",
		},
	})

	if got := episode.Extra["model"]; got != "openrouter/google/gemini-3.5-flash" {
		t.Fatalf("model = %v", got)
	}
	if got := episode.Extra["model_provider"]; got != "openrouter" {
		t.Fatalf("model_provider = %v", got)
	}
	if got := episode.Extra["model_name"]; got != "google/gemini-3.5-flash" {
		t.Fatalf("model_name = %v", got)
	}
	if got := episode.Extra["agent_commit"]; got != "abc1234" {
		t.Fatalf("agent_commit = %v", got)
	}
	if got := episode.Extra["agent_build"]; got != "20260603-120000-abc1234" {
		t.Fatalf("agent_build = %v", got)
	}
}

func TestTraceReleaseAndVersionFromEpisodeExtra(t *testing.T) {
	episode := TaskEpisode{
		Extra: map[string]interface{}{
			"agent_commit":     "abc1234",
			"agent_build":      "20260603-120000-abc1234",
			"model":            "openrouter/google/gemini-3.5-flash",
			"firmware_version": "20260603-120000-abc1234",
		},
	}
	if got := traceReleaseFromEpisode(episode); got != "abc1234" {
		t.Fatalf("traceReleaseFromEpisode() = %q", got)
	}
	if got := traceVersionFromEpisode(episode); got != "20260603-120000-abc1234" {
		t.Fatalf("traceVersionFromEpisode() = %q", got)
	}
}

func TestBuildLangfuseBatchIncludesModelAndRelease(t *testing.T) {
	start := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	episode := TaskEpisode{
		ID:        "ep_meta_test",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(time.Second).Format(time.RFC3339Nano),
		UserGoal:  "打开设置",
		Outcome:   TaskEpisodeOutcome{Success: true, FinalAnswer: "ok"},
		Extra: map[string]interface{}{
			"model":        "openrouter/google/gemini-3.5-flash",
			"agent_commit": "abc1234",
			"agent_build":  "20260603-120000-abc1234",
		},
	}
	exporter := NewEpisodeExporter(TelemetryConfig{Enabled: boolPtr(true), BaseURL: "http://langfuse.test"}, nil)
	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir())
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}
	if len(batch) == 0 {
		t.Fatal("expected batch events")
	}
	var body map[string]interface{}
	if err := json.Unmarshal(batch[0].Body, &body); err != nil {
		t.Fatalf("decode trace body: %v", err)
	}
	if body["release"] != "abc1234" {
		t.Fatalf("release = %v", body["release"])
	}
	if body["version"] != "20260603-120000-abc1234" {
		t.Fatalf("version = %v", body["version"])
	}
	meta, ok := body["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata = %T", body["metadata"])
	}
	if meta["model"] != "openrouter/google/gemini-3.5-flash" {
		t.Fatalf("metadata.model = %v", meta["model"])
	}
}
