package agent

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func TestTelemetryPromptMetadataSkipsNilCallOption(t *testing.T) {
	if meta := telemetryPromptMetadata([]llms.CallOption{nil}); meta != nil {
		t.Fatalf("metadata = %#v, want nil", meta)
	}
}

func TestTelemetryPromptCaptureRecordsPromptShapeMetadata(t *testing.T) {
	capture := newTelemetryPromptCapture(true)
	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart("system rules")},
		},
		{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.TextPart("hello"),
				llms.BinaryContent{MIMEType: "image/png", Data: []byte("png")},
			},
		},
	}
	options := []llms.CallOption{llms.WithTools([]llms.Tool{{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        "echo",
			Description: "Echo input.",
		},
	}})}

	capture.Record(
		context.Background(),
		time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 10, 0, 1, 0, time.UTC),
		messages,
		options,
		contentResponse("ok"),
		nil,
		4096,
	)

	calls := capture.Snapshot()
	if len(calls) != 1 {
		t.Fatalf("captured calls = %d, want 1", len(calls))
	}
	meta := calls[0].Metadata
	if got := meta["message_count"]; got != 2 {
		t.Fatalf("message_count = %v, want 2", got)
	}
	if got := meta["text_part_count"]; got != 2 {
		t.Fatalf("text_part_count = %v, want 2", got)
	}
	if got := meta["binary_count"]; got != 1 {
		t.Fatalf("binary_count = %v, want 1", got)
	}
	if got := meta["tool_schema_count"]; got != 1 {
		t.Fatalf("tool_schema_count = %v, want 1", got)
	}
	for _, key := range []string{"estimated_prompt_tokens", "estimated_tool_schema_tokens", "system_text_tokens", "non_system_text_tokens"} {
		value, ok := meta[key].(int)
		if !ok || value <= 0 {
			t.Fatalf("%s = %#v, want positive int", key, meta[key])
		}
	}
	if got := meta["text_chars"]; got != len("system rules")+len("hello") {
		t.Fatalf("text_chars = %v, want %d", got, len("system rules")+len("hello"))
	}
}

func TestTelemetryMessageInputCapturesBinaryWithoutInlineBase64(t *testing.T) {
	image := []byte("jpeg-image-bytes")
	input, media := telemetryMessageInput([]llms.MessageContent{{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.BinaryContent{MIMEType: "image/jpeg", Data: image},
		},
	}})

	if len(media) != 1 {
		t.Fatalf("media count = %d, want 1", len(media))
	}
	if string(media[0].Data) != string(image) {
		t.Fatalf("media data = %q, want original bytes", media[0].Data)
	}
	part := input[0]["parts"].([]map[string]interface{})[0]
	if part["data"] != media[0].Placeholder {
		t.Fatalf("part data = %v, want media placeholder", part["data"])
	}
	if strings.Contains(part["data"].(string), base64.StdEncoding.EncodeToString(image)) {
		t.Fatalf("part data contains inline base64: %q", part["data"])
	}
	if part["size"] != len(image) {
		t.Fatalf("part size = %v, want %d", part["size"], len(image))
	}
}

func TestTelemetryMessageInputExtractsBase64DataURL(t *testing.T) {
	image := []byte("png-image-bytes")
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)
	input, media := telemetryMessageInput([]llms.MessageContent{{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.ImageURLContent{URL: dataURL},
		},
	}})

	if len(media) != 1 {
		t.Fatalf("media count = %d, want 1", len(media))
	}
	part := input[0]["parts"].([]map[string]interface{})[0]
	if part["url"] != media[0].Placeholder {
		t.Fatalf("part url = %v, want media placeholder", part["url"])
	}
	if strings.Contains(part["url"].(string), base64.StdEncoding.EncodeToString(image)) {
		t.Fatalf("part url contains inline base64: %q", part["url"])
	}
	if media[0].ContentType != "image/png" {
		t.Fatalf("media content type = %q, want image/png", media[0].ContentType)
	}
}

func TestTelemetryMessageInputOmitsInvalidDataURL(t *testing.T) {
	input, media := telemetryMessageInput([]llms.MessageContent{{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.ImageURLContent{URL: "data:image/jpeg;base64,not-valid-base64!!!"},
		},
	}})

	if len(media) != 0 {
		t.Fatalf("media count = %d, want 0", len(media))
	}
	part := input[0]["parts"].([]map[string]interface{})[0]
	url, _ := part["url"].(string)
	if strings.HasPrefix(url, "data:") {
		t.Fatalf("invalid data URL remained inline: %q", url)
	}
}
