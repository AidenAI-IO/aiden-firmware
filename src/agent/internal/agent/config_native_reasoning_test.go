package agent

import (
	"testing"
)

func TestUsesNativeRealtimeReasoningSwitch(t *testing.T) {
	trueVal, falseVal := true, false
	cases := []struct {
		name  string
		model string
		over  *bool
		want  bool
	}{
		{"thinking default", "gemini-3.8-live-extended-thinking", nil, true},
		{"thinking forced legacy", "gemini-3.8-live-extended-thinking", &trueVal, false},
		{"regular live default", "gemini-3.8-live", nil, false},
		{"regular live forced direct", "gemini-3.8-live", &falseVal, true},
		{"flash live unaffected", "gemini-3.1-flash-live-preview", nil, false},
	}
	for _, tc := range cases {
		cfg := Config{}
		cfg.VoiceModel = VoiceModelConfig{Provider: "gemini", Model: tc.model, UseBackendAgent: tc.over}
		// resolve provider into flat form like LoadRuntimeConfig does
		cfg.VoiceModel.Provider = "gemini"
		if got := cfg.UsesNativeRealtimeReasoning(); got != tc.want {
			t.Errorf("%s: got %t want %t", tc.name, got, tc.want)
		}
	}
}
