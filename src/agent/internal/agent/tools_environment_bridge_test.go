package agent

import (
	"testing"

	"aiden-agent/internal/agent/mnk"
	"aiden-agent/internal/agent/screenprovider"
)

func TestBuiltinToolSetFromConfigUsesEnvironmentBridgeProviders(t *testing.T) {
	tools := NewBuiltinToolSetFromConfig(Config{
		EnvironmentBridge: EnvironmentBridgeConfig{
			Enabled:         true,
			Endpoint:        "http://bridge.example",
			BenchmarkTaskID: "suite:task-1",
		},
	}, ProxyConfig{})

	if _, ok := tools.ScreenProvider().(*screenprovider.HTTP); !ok {
		t.Fatalf("screen provider = %T, want *screenprovider.HTTP", tools.ScreenProvider())
	}
	if _, ok := tools.MNKProvider().(*mnk.HTTPProvider); !ok {
		t.Fatalf("MNK provider = %T, want *mnk.HTTPProvider", tools.MNKProvider())
	}
}
