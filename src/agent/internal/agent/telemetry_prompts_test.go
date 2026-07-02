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

func TestTelemetryPromptCaptureRecordsProviderMetadata(t *testing.T) {
	capture := newTelemetryPromptCapture(true)
	res := contentResponseWithInfo("ok", map[string]any{
		"prompt_tokens":                100,
		"completion_tokens":            10,
		"llm_http_to_headers_ms":       int64(1234),
		"llm_time_to_first_content_ms": int64(1500),
		"llm_stream_content_chunks":    3,
		"openrouter_generation_id":     "gen-test",
		"openrouter_provider_name":     "TestProvider",
		"openrouter_metadata": map[string]any{
			"strategy": "fallback",
		},
	})

	capture.Record(
		context.Background(),
		time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 10, 0, 1, 0, time.UTC),
		[]llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("hello")},
		}},
		nil,
		res,
		nil,
		0,
	)

	calls := capture.Snapshot()
	if len(calls) != 1 {
		t.Fatalf("captured calls = %d, want 1", len(calls))
	}
	meta := calls[0].Metadata
	if got := meta["llm_http_to_headers_ms"]; got != int64(1234) {
		t.Fatalf("llm_http_to_headers_ms = %#v, want 1234", got)
	}
	if got := meta["llm_time_to_first_content_ms"]; got != int64(1500) {
		t.Fatalf("llm_time_to_first_content_ms = %#v, want 1500", got)
	}
	if got := meta["llm_stream_content_chunks"]; got != 3 {
		t.Fatalf("llm_stream_content_chunks = %#v, want 3", got)
	}
	if got := meta["openrouter_generation_id"]; got != "gen-test" {
		t.Fatalf("openrouter_generation_id = %#v, want gen-test", got)
	}
	if got := meta["openrouter_provider_name"]; got != "TestProvider" {
		t.Fatalf("openrouter_provider_name = %#v, want TestProvider", got)
	}
	if _, ok := meta["prompt_tokens"]; ok {
		t.Fatalf("usage token fields should stay in usageDetails, metadata = %#v", meta)
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
