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
	if err := os.WriteFile(filepath.Join(dir, "agent.toml"), []byte("[model_settings.model]\nprovider = \"fake\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	api := NewSTTConfigTestAPI(filepath.Join(dir, "agent.toml"))
	resp := httptest.NewRecorder()
	api.HandleStart(resp, httptest.NewRequest(http.MethodPost, "/api/config-test/stt/start", strings.NewReader(`{}`)))
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "missing stt_values") {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

// The live STT test is most useful while the persisted config is in recovery:
// the flagged stt.provider is what the user is testing a replacement for. The
// candidate values come from the request, so the file only has to be readable,
// while a file that cannot be decoded must still fail.
//
// The request carries no values on purpose: the handler then stops at its own
// request check, which tells the two load outcomes apart without recording audio.
func TestSTTConfigTestLiveSessionLoadsRecoverableConfigAndRejectsDamagedFiles(t *testing.T) {
	dir := ensureTestConfigDir(t, t.TempDir())
	recovery := filepath.Join(dir, "agent.toml")
	writeFile(t, recovery, `[model_settings.model]
provider = "fake"

[voice_settings.mode]
input_mode = "stt"
`)
	if _, err := LoadRuntimeConfig(recovery); err == nil {
		t.Fatal("expected the runtime loader to reject a declared stt mode with no provider")
	}
	if got := sttConfigTestStartBody(t, recovery); got.Code != http.StatusBadRequest ||
		!strings.Contains(got.Body.String(), "missing stt_values") {
		t.Fatalf("the live test refused a recoverable config: status=%d body=%s", got.Code, got.Body.String())
	}

	damaged := filepath.Join(dir, "damaged.toml")
	writeFile(t, damaged, "[broken")
	got := sttConfigTestStartBody(t, damaged)
	if got.Code != http.StatusServiceUnavailable || !strings.Contains(got.Body.String(), "load Agent config") {
		t.Fatalf("status=%d body=%s, want a file it cannot decode to fail", got.Code, got.Body.String())
	}
}

// sttConfigTestStartBody drives the standalone STT test API the way config-web
// does. The request carries no values on purpose: the handler then stops at its
// own request check, which tells the two load outcomes apart without recording
// audio.
func sttConfigTestStartBody(t *testing.T, configPath string) *httptest.ResponseRecorder {
	t.Helper()
	api := NewSTTConfigTestAPI(configPath)
	api.server = newServerForTest(NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	))
	resp := httptest.NewRecorder()
	api.HandleStart(resp, httptest.NewRequest(http.MethodPost, "/api/config-test/stt/start", strings.NewReader(`{}`)))
	return resp
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
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
