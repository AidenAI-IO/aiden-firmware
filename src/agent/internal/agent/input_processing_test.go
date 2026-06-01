package agent

import (
	"strings"
	"testing"
)

func TestNewSTTClientFromConfigTrimsProviderWhitespace(t *testing.T) {
	clearAgentProxyEnv(t)

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

func TestNewSTTClientFromConfigRejectsInvalidProxyEnvironment(t *testing.T) {
	clearAgentProxyEnv(t)
	t.Setenv("HTTP_PROXY", " http://proxy.example:7890")

	_, err := NewSTTClientFromConfig(Config{
		STT: STTConfig{
			Provider: "openai",
		},
	})
	if err == nil {
		t.Fatal("NewSTTClientFromConfig() error = nil, want proxy validation error")
	}
	if !strings.Contains(err.Error(), "proxy environment") {
		t.Fatalf("NewSTTClientFromConfig() error = %v, want proxy environment", err)
	}
}
