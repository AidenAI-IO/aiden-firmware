package agent

import "testing"

func TestNewSTTClientFromConfigTrimsProviderWhitespace(t *testing.T) {
	client, err := NewSTTClientFromConfig(Config{
		STT: STTConfig{
			Provider: " openai ",
		},
	})
	if err != nil {
		t.Fatalf("NewSTTClientFromConfig() error = %v", err)
	}
	if client == nil {
		t.Fatal("expected STT client")
	}
}
