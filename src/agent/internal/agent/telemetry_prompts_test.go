package agent

import (
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestTelemetryPromptMetadataSkipsNilCallOption(t *testing.T) {
	if meta := telemetryPromptMetadata([]llms.CallOption{nil}); meta != nil {
		t.Fatalf("metadata = %#v, want nil", meta)
	}
}
