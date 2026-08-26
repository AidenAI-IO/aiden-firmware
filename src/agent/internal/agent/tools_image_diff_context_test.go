package agent

import (
	"context"
	"encoding/json"
	"image/color"
	"path/filepath"
	"testing"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/messages"

	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestAgentLoopMakesScreenshotAttachmentsAvailableToImageDiff(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	storeScreenshot := func(data []byte) string {
		t.Helper()
		stored, err := manager.StoreAttachment("image/jpeg", data)
		if err != nil {
			t.Fatalf("StoreAttachment() error = %v", err)
		}
		stored.Source = messages.AttachmentSourceScreenshotObservation
		if err := manager.AppendMessage(messages.Message{
			Role:        messages.MessageRoleUser,
			Attachments: []messages.Attachment{stored},
		}); err != nil {
			t.Fatalf("AppendMessage() error = %v", err)
		}
		return filepath.Base(stored.FilePath)
	}

	beforeID := storeScreenshot(solidImageDiffJPEG(t, color.Black))
	afterID := storeScreenshot(solidImageDiffJPEG(t, color.White))
	input, err := json.Marshal(map[string]string{"before": beforeID, "after": afterID})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	loop := &AgentLoop{contextManager: manager}
	execution := loop.executeToolCall(context.Background(), ToolCallExecution{
		Specs: NewToolSpecs([]langtools.Tool{&ImageDiffTool{}}),
		Action: schema.AgentAction{
			Tool:      "image_diff",
			ToolInput: string(input),
		},
	})
	if execution.Result.Error != nil || execution.Error != nil {
		t.Fatalf("image_diff execution failed: result=%#v err=%v", execution.Result, execution.Error)
	}
	var result imageDiffResult
	if err := json.Unmarshal([]byte(execution.Result.Output), &result); err != nil {
		t.Fatalf("unmarshal output: %v; output=%s", err, execution.Result.Output)
	}
	if !result.Changed || result.DiffRatio != 1 {
		t.Fatalf("result = %#v, want changed=true diff_ratio=1", result)
	}
}

func TestImageDiffIgnoresScreenshotDisplayMarker(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	raw := solidImageDiffJPEG(t, color.Black)
	storeScreenshot := func(marker *messages.ScreenshotDisplayMarker) string {
		t.Helper()
		stored, err := manager.StoreAttachment("image/jpeg", raw)
		if err != nil {
			t.Fatalf("StoreAttachment() error = %v", err)
		}
		stored.Source = messages.AttachmentSourceScreenshotObservation
		stored.DisplayMarker = marker
		if err := manager.AppendMessage(messages.Message{Role: messages.MessageRoleState, Attachments: []messages.Attachment{stored}}); err != nil {
			t.Fatalf("AppendMessage() error = %v", err)
		}
		return filepath.Base(stored.FilePath)
	}

	beforeID := storeScreenshot(nil)
	afterID := storeScreenshot(&messages.ScreenshotDisplayMarker{Type: "tap", X: 500, Y: 500})
	input, err := json.Marshal(map[string]string{"before": beforeID, "after": afterID})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	loop := &AgentLoop{contextManager: manager}
	execution := loop.executeToolCall(context.Background(), ToolCallExecution{
		Specs:  NewToolSpecs([]langtools.Tool{&ImageDiffTool{}}),
		Action: schema.AgentAction{Tool: "image_diff", ToolInput: string(input)},
	})
	if execution.Result.Error != nil || execution.Error != nil {
		t.Fatalf("image_diff execution failed: result=%#v err=%v", execution.Result, execution.Error)
	}
	var result imageDiffResult
	if err := json.Unmarshal([]byte(execution.Result.Output), &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if result.Changed || result.DiffRatio != 0 {
		t.Fatalf("display-only marker affected image_diff: %#v", result)
	}
}
