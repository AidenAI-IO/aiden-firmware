package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

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
