package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneSTTConfigTestAPILoadsConfigWithoutAgentRuntime(t *testing.T) {
	dir := ensureTestConfigDir(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "agent.toml"), []byte("[model]\nprovider = \"fake\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	api := NewSTTConfigTestAPI(filepath.Join(dir, "agent.toml"))
	resp := httptest.NewRecorder()
	api.HandleStart(resp, httptest.NewRequest(http.MethodPost, "/api/config-test/stt/start", strings.NewReader(`{}`)))
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "missing stt_values") {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSTTConfigTestLiveRequestAppliesUnsavedAudioBackend(t *testing.T) {
	var req sttConfigTestLiveStartRequest
	body := `{
		"stt_values":{"provider":"openai-whisper"},
		"audio_values":{"backend":"local","socket":"/tmp/test-audio.sock","sample_rate":16000,"channels":1,"bit_width":16}
	}`
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("decode request: %v", err)
	}

	base := Config{
		Audio: AudioConfig{Backend: AudioBackendAudioService},
	}
	cfg, err := req.appliedConfig(base)
	if err != nil {
		t.Fatalf("appliedConfig() error = %v", err)
	}
	if got := cfg.AudioBackendOrDefault(); got != AudioBackendLocal {
		t.Fatalf("audio backend = %q, want request value %q", got, AudioBackendLocal)
	}
}
