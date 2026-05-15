package agent

import (
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestFunctionAgentParseOutputSkipsNilToolCalls(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{
				{ID: "ignored", Type: "function"},
				{
					ID:   "call_1",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "echo",
						Arguments: `{"__arg1":"hello"}`,
					},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	if actions[0].Tool != "echo" || actions[0].ToolInput != "hello" || actions[0].ToolID != "call_1" {
		t.Fatalf("unexpected action: %#v", actions[0])
	}
}

func TestBuildImagePartDefaultsMimeType(t *testing.T) {
	part := buildImagePart("", []byte("image-bytes"))
	imagePart, ok := part.(llms.ImageURLContent)
	if !ok {
		t.Fatalf("expected image url content, got %T", part)
	}
	if imagePart.URL != "data:image/png;base64,aW1hZ2UtYnl0ZXM=" {
		t.Fatalf("unexpected image url: %q", imagePart.URL)
	}
}
