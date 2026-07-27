package agent

import (
	"context"
	"testing"

	modelpkg "aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type textInputVisionRecordingModel struct {
	options []llms.CallOption
}

func (m *textInputVisionRecordingModel) GenerateContent(_ context.Context, _ []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	m.options = options
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: `{}`}}}, nil
}

func (m *textInputVisionRecordingModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", nil
}

func (m *textInputVisionRecordingModel) CallOptions() []chains.ChainCallOption { return nil }

func (m *textInputVisionRecordingModel) Spec() modelpkg.ModelSpec { return modelpkg.ModelSpec{} }

func TestVisionJSONBoundsOutputTokens(t *testing.T) {
	model := &textInputVisionRecordingModel{}
	vision := &llmTextInputVision{models: model}
	if _, err := vision.visionJSON(context.Background(), "return json", screenshotResult{Data: "ZmFrZQ=="}); err != nil {
		t.Fatalf("visionJSON() error = %v", err)
	}
	options := llms.CallOptions{}
	for _, option := range model.options {
		option(&options)
	}
	if !options.JSONMode {
		t.Fatal("visionJSON() must request JSON mode")
	}
	if options.MaxTokens != textInputVisionMaxTokens {
		t.Fatalf("MaxTokens = %d, want %d", options.MaxTokens, textInputVisionMaxTokens)
	}
}
