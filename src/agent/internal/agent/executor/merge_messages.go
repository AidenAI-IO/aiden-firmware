package executor

import "aiden-agent/internal/agent/messages"

var _ OutboundMessageTransform = (*MergeConsecutiveMessagesTransform)(nil)

// MergeConsecutiveMessagesTransform merges consecutive messages with the same role.
// This is useful for models that don't support consecutive messages from the same role,
// or to reduce token usage by combining related messages.
type MergeConsecutiveMessagesTransform struct{}

func (m MergeConsecutiveMessagesTransform) Transform(messageList []messages.Message) []messages.Message {
	if len(messageList) <= 1 {
		return messageList
	}

	result := make([]messages.Message, 0, len(messageList))
	result = append(result, messageList[0])

	for i := 1; i < len(messageList); i++ {
		current := messageList[i]
		previous := &result[len(result)-1]

		// Only merge if roles match and neither has tool calls
		canMerge := current.Role == previous.Role &&
			current.Role != messages.MessageRoleToolResult &&
			len(current.ToolCalls) == 0 &&
			len(previous.ToolCalls) == 0

		if !canMerge {
			result = append(result, current)
			continue
		}

		// Merge content and attachments
		if current.Content != "" {
			if previous.Content != "" {
				previous.Content = previous.Content + "\n\n" + current.Content
			} else {
				previous.Content = current.Content
			}
		}

		// Merge attachments
		if len(current.Attachments) > 0 {
			previous.Attachments = append(previous.Attachments, current.Attachments...)
		}
	}

	return result
}
