package agent

import (
	"strings"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
)

var _ executor.OutboundMessageTransform = AnthropicScreenshotPruner{}

const (
	screenshotAttachedText    = "The image is attached in the next message."
	screenshotPlaceholderText = "The image is replaced with a placeholder in the next message."
)

type AnthropicScreenshotPruner struct {
	Enabled bool
	Config  ScreenshotPruningConfig
}

func (p AnthropicScreenshotPruner) Transform(messages []contextmanager.Message) []contextmanager.Message {
	if !p.Enabled || len(messages) == 0 {
		return messages
	}
	cfg := p.Config.WithDefaults()

	total := 0
	for _, msg := range messages {
		for _, a := range msg.Attachments {
			if a.Source == contextmanager.AttachmentSourceScreenshotObservation {
				total++
			}
		}
	}
	pruned := cfg.PrunedCount(total)
	if pruned <= 0 {
		return messages
	}

	out := make([]contextmanager.Message, len(messages))
	seen := 0
	for i, msg := range messages {
		cloned := msg
		if len(msg.Attachments) > 0 {
			cloned.Attachments = append([]contextmanager.Attachment(nil), msg.Attachments...)
		}
		if len(msg.ToolCalls) > 0 {
			cloned.ToolCalls = append([]contextmanager.ToolCall(nil), msg.ToolCalls...)
		}
		if len(msg.ToolResults) > 0 {
			cloned.ToolResults = append([]contextmanager.ToolResult(nil), msg.ToolResults...)
		}

		kept := cloned.Attachments[:0]
		for _, a := range cloned.Attachments {
			if a.Source != contextmanager.AttachmentSourceScreenshotObservation {
				kept = append(kept, a)
				continue
			}
			seen++
			if seen <= pruned {
				if i > 0 {
					out[i-1] = rewritePrunedScreenshotToolResult(out[i-1])
				}
				if !strings.Contains(cloned.Content, "[Image omitted]") {
					if strings.TrimSpace(cloned.Content) == "" {
						cloned.Content = "[Image omitted]"
					} else {
						cloned.Content = cloned.Content + "\n[Image omitted]"
					}
				}
				continue
			}
			kept = append(kept, a)
		}
		cloned.Attachments = kept
		out[i] = cloned
	}
	return out
}

func rewritePrunedScreenshotToolResult(msg contextmanager.Message) contextmanager.Message {
	if msg.Role != contextmanager.MessageRoleToolResult || len(msg.ToolResults) == 0 {
		return msg
	}

	cloned := msg
	cloned.ToolResults = append([]contextmanager.ToolResult(nil), msg.ToolResults...)
	for i, result := range cloned.ToolResults {
		if !strings.Contains(result.Content, screenshotAttachedText) {
			continue
		}
		result.Content = strings.ReplaceAll(result.Content, screenshotAttachedText, screenshotPlaceholderText)
		cloned.ToolResults[i] = result
	}
	return cloned
}
