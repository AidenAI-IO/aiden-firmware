package agent

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
)

func messageText(messages []llms.MessageContent) string {
	var builder strings.Builder
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if text, ok := part.(llms.TextContent); ok {
				builder.WriteString(text.Text)
				builder.WriteByte('\n')
			}
		}
	}
	return builder.String()
}

func intPtr(v int) *int { return &v }

func testScreenshotObservationStep(tool string, data []byte) schema.AgentStep {
	observation, _ := json.Marshal(postActionScreenshotResult{
		screenshotResult: screenshotResult{
			Width:  320,
			Height: 240,
			Format: "jpeg",
			Size:   len(data),
			Data:   base64.StdEncoding.EncodeToString(data),
		},
	})
	return schema.AgentStep{
		Action:      schema.AgentAction{Tool: tool},
		Observation: string(observation),
	}
}
