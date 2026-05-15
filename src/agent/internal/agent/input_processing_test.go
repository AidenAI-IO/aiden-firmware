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

func TestNewTTSClientFromConfigTrimsProviderWhitespace(t *testing.T) {
	client, err := NewTTSClientFromConfig(Config{
		TTS: TTSConfig{
			Provider: " minimax ",
		},
	})
	if err != nil {
		t.Fatalf("NewTTSClientFromConfig() error = %v", err)
	}
	if client == nil {
		t.Fatal("expected TTS client")
	}
}
