package agent

import "testing"

func TestDefaultConfigDisablesBackendAgent(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.VoiceModel.UseBackendAgent {
		t.Fatal("DefaultConfig().VoiceModel.UseBackendAgent = true, want false")
	}
}
