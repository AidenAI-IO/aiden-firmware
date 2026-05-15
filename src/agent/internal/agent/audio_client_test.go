package agent

import (
	"encoding/json"
	"testing"
)

func TestAudioRequestUsesTopLevelFormatFields(t *testing.T) {
	req := audioRequest{
		Op:         "start_playback",
		SampleRate: 32000,
		Channels:   1,
		BitWidth:   16,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if decoded["sample_rate"] != float64(32000) {
		t.Fatalf("sample_rate = %v, want 32000", decoded["sample_rate"])
	}
	if decoded["channels"] != float64(1) {
		t.Fatalf("channels = %v, want 1", decoded["channels"])
	}
	if decoded["bit_width"] != float64(16) {
		t.Fatalf("bit_width = %v, want 16", decoded["bit_width"])
	}
	if _, ok := decoded["format"]; ok {
		t.Fatalf("unexpected nested format field in request: %s", string(data))
	}
}

func TestAudioRequestVolumeField(t *testing.T) {
	req := audioRequest{
		Op:     "set_playback_volume",
		Volume: 65,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if decoded["volume"] != float64(65) {
		t.Fatalf("volume = %v, want 65", decoded["volume"])
	}
}
