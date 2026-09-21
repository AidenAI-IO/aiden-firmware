package agent

import (
	"testing"
)

func TestRealtimeDirectToolsSwitch(t *testing.T) {
	trueVal, falseVal := true, false
	cases := []struct {
		name string
		over *bool
		want bool
	}{
		{"unset uses backend agent", nil, false},
		{"true uses backend agent", &trueVal, false},
		{"false forces direct tools", &falseVal, true},
	}
	for _, tc := range cases {
		cfg := Config{}
		cfg.VoiceModel = VoiceModelConfig{Provider: "gemini", Model: "gemini-3.8-live-extended-thinking", UseBackendAgent: tc.over}
		if got := cfg.RealtimeDirectTools(); got != tc.want {
			t.Errorf("%s: got %t want %t", tc.name, got, tc.want)
		}
	}
}
