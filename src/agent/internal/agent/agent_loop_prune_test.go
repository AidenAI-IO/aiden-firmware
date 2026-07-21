package agent

import (
	"context"
	"strings"
	"testing"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"

	"github.com/tmc/langchaingo/llms"
)

type pruneRecordingModel struct {
	last []llms.MessageContent
}

func (m *pruneRecordingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	m.last = messages
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: "ok"}}}, nil
}

func (m *pruneRecordingModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return "ok", nil
}

func newScreenshotObservationManager(t *testing.T, count int) *contextmanager.ContextManager {
	t.Helper()
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	for i := 0; i < count; i++ {
		stored, err := manager.StoreAttachment("image/jpeg", []byte("img"))
		if err != nil {
			t.Fatalf("StoreAttachment() error = %v", err)
		}
		stored.Source = contextmanager.AttachmentSourceScreenshotObservation
		if err := manager.AppendMessage(contextmanager.Message{
			Role:        contextmanager.MessageRoleUser,
			Content:     "shot",
			Attachments: []contextmanager.Attachment{stored},
		}); err != nil {
			t.Fatalf("AppendMessage() error = %v", err)
		}
	}
	return manager
}

func countBinaryAndOmitted(messages []llms.MessageContent) (binaryCount, omitted int) {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.BinaryContent:
				binaryCount++
			case llms.TextContent:
				if strings.Contains(p.Text, "[Image omitted]") {
					omitted++
				}
			}
		}
	}
	return binaryCount, omitted
}

func TestAgentLoopExecutorPrunesForAnthropicModel(t *testing.T) {
	manager := newScreenshotObservationManager(t, 6)
	model := &pruneRecordingModel{}
	pruner := AnthropicScreenshotPruner{
		Enabled: IsAnthropicModel("openrouter", "anthropic/claude-test"),
		Config:  ScreenshotPruningConfig{}.WithDefaults(),
	}
	exec := executor.NewLLMExecutor(model, manager, pruner)
	if _, err := exec.GenerateContent(context.Background()); err != nil {
		t.Fatal(err)
	}
	binaryCount, omitted := countBinaryAndOmitted(model.last)
	if binaryCount != 4 || omitted != 2 {
		t.Fatalf("binary=%d omitted=%d messages=%#v", binaryCount, omitted, model.last)
	}
	if got := len(manager.MessageListDump().Messages); got != 6 {
		t.Fatalf("persisted message count = %d", got)
	}
	for _, msg := range manager.MessageListDump().Messages {
		if len(msg.Attachments) != 1 {
			t.Fatalf("persisted attachments mutated: %#v", msg)
		}
	}
}

func TestAgentLoopExecutorSkipsPruneForNonAnthropic(t *testing.T) {
	manager := newScreenshotObservationManager(t, 6)
	model := &pruneRecordingModel{}
	pruner := AnthropicScreenshotPruner{
		Enabled: IsAnthropicModel("openrouter", "openai/gpt-4o"),
		Config:  ScreenshotPruningConfig{}.WithDefaults(),
	}
	exec := executor.NewLLMExecutor(model, manager, pruner)
	if _, err := exec.GenerateContent(context.Background()); err != nil {
		t.Fatal(err)
	}
	binaryCount, omitted := countBinaryAndOmitted(model.last)
	if binaryCount != 6 || omitted != 0 {
		t.Fatalf("binary=%d omitted=%d messages=%#v", binaryCount, omitted, model.last)
	}
}

func TestAgentLoopOutboundTransformsEnableForAnthropic(t *testing.T) {
	loop := &AgentLoop{
		Model:             &ModelManager{config: ModelConfig{Provider: "openrouter", Model: "anthropic/x"}},
		ScreenshotPruning: ScreenshotPruningConfig{}.WithDefaults(),
	}
	transforms := loop.outboundTransforms()
	pruner, ok := transforms[0].(AnthropicScreenshotPruner)
	if !ok || !pruner.Enabled {
		t.Fatalf("transforms = %#v", transforms)
	}
}

func TestAgentLoopOutboundTransformsDisableForNonAnthropic(t *testing.T) {
	loop := &AgentLoop{
		Model:             &ModelManager{config: ModelConfig{Provider: "openrouter", Model: "openai/gpt-4o"}},
		ScreenshotPruning: ScreenshotPruningConfig{}.WithDefaults(),
	}
	transforms := loop.outboundTransforms()
	pruner, ok := transforms[0].(AnthropicScreenshotPruner)
	if !ok || pruner.Enabled {
		t.Fatalf("transforms = %#v", transforms)
	}
}
